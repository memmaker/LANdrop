// Remote control: a peer executes a request only when its "Allow Remote Control"
// toggle is on and the shared passphrase matches. Requests arrive over the same
// TCP transport as chat and file transfer, as header{Type:"control"}.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"time"
)

type execArgs struct {
	Line    string `json:"line"`              // run through the platform shell
	Dir     string `json:"dir,omitempty"`     // working directory
	Timeout int    `json:"timeout,omitempty"` // seconds; 0 => 60
}

type launchArgs struct {
	App  string   `json:"app"`
	Args []string `json:"args,omitempty"`
}

// ctrlResp is the reply to any control request.
type ctrlResp struct {
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
	Out  string `json:"out,omitempty"`
	Code int    `json:"code"`
}

// handleControl verifies the toggle and passphrase, then dispatches. It writes
// one ctrlResp back on c.
func handleControl(c net.Conn, h header) {
	reply := func(r ctrlResp) { json.NewEncoder(c).Encode(r) }
	if !remoteAllowed() {
		reply(ctrlResp{Err: "remote control is disabled on this machine"})
		return
	}
	if remotePass() == "" || h.Pass != remotePass() {
		appendLog("rejected remote " + h.Op + " from " + h.From + " (bad passphrase)")
		reply(ctrlResp{Err: "bad passphrase"})
		return
	}
	appendLog(fmt.Sprintf("remote %s from %s", h.Op, h.From))
	switch h.Op {
	case "exec":
		var a execArgs
		json.Unmarshal(h.Data, &a)
		reply(runExec(a))
	case "launch":
		var a launchArgs
		json.Unmarshal(h.Data, &a)
		reply(runLaunch(a))
	default:
		reply(ctrlResp{Err: "unknown op: " + h.Op})
	}
}

func runExec(a execArgs) ctrlResp {
	if a.Line == "" {
		return ctrlResp{Err: "empty command"}
	}
	d := time.Duration(a.Timeout) * time.Second
	if d == 0 {
		d = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", a.Line)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", a.Line)
	}
	cmd.Dir = a.Dir
	out, err := cmd.CombinedOutput()
	r := ctrlResp{OK: err == nil, Out: string(out)}
	if cmd.ProcessState != nil {
		r.Code = cmd.ProcessState.ExitCode()
	}
	if err != nil && r.Code == 0 {
		r.Err = err.Error() // couldn't start, or timed out
	}
	return r
}

func runLaunch(a launchArgs) ctrlResp {
	if a.App == "" {
		return ctrlResp{Err: "empty app"}
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", append([]string{"-a", a.App}, a.Args...)...)
	case "windows":
		cmd = exec.Command("cmd", append([]string{"/c", "start", "", a.App}, a.Args...)...)
	default:
		cmd = exec.Command(a.App, a.Args...)
	}
	if err := cmd.Start(); err != nil {
		return ctrlResp{Err: err.Error()}
	}
	pid := cmd.Process.Pid
	go cmd.Wait() // reap; the app keeps running on its own
	return ctrlResp{OK: true, Out: fmt.Sprintf("launched %s (pid %d)", a.App, pid)}
}

// callControl sends a control request to p and returns its reply. Used by the
// MCP tool handlers on the gateway machine.
func callControl(p *peer, op string, data any) (ctrlResp, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return ctrlResp{}, err
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(p.Addr, fmt.Sprint(p.Port)), 5*time.Second)
	if err != nil {
		return ctrlResp{}, err
	}
	defer c.Close()
	h := header{Type: "control", From: myName(), Op: op, Pass: remotePass(), Data: raw}
	if err := json.NewEncoder(c).Encode(h); err != nil {
		return ctrlResp{}, err
	}
	c.SetReadDeadline(time.Now().Add(10 * time.Minute))
	var r ctrlResp
	if err := json.NewDecoder(c).Decode(&r); err != nil {
		return ctrlResp{}, err
	}
	return r, nil
}

// findPeer resolves an agent-supplied identifier (peer ID or display name) to a
// live peer. Name match is a convenience; ID is exact.
func findPeer(id string) (*peer, error) {
	mu.Lock()
	defer mu.Unlock()
	if p, ok := peers[id]; ok {
		return p, nil
	}
	for _, p := range peers {
		if p.Name == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no online peer with id or name %q", id)
}
