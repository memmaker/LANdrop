// Remote control: a peer executes a request only when its "Allow Remote Control"
// toggle is on and the shared passphrase matches. Requests arrive over the same
// TCP transport as chat and file transfer, as header{Type:"control"}.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

type inputArgs struct {
	Text string `json:"text,omitempty"` // literal text to type
	Key  string `json:"key,omitempty"`  // one key or chord, e.g. "enter", "cmd+c"
}

type screenshotArgs struct {
	Display int `json:"display,omitempty"` // 0 = primary
}

type putArgs struct {
	Path string `json:"path"` // destination path on the peer
	Size int64  `json:"size"`
}

type getArgs struct {
	Path string `json:"path"` // source path on the peer
}

// ctrlResp is the reply to any control request.
type ctrlResp struct {
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
	Out  string `json:"out,omitempty"`
	Code int    `json:"code"`
	N    int64  `json:"n,omitempty"`   // byte count, for put/get
	Img  []byte `json:"img,omitempty"` // PNG, for screenshot (JSON-encoded as base64)
}

// Kill switch: every in-flight exec registers its cancel here so the user can
// stop all running remote commands at once from the GUI.
var (
	runMu   sync.Mutex
	running = map[int]context.CancelFunc{}
	runSeq  int
)

func registerCancel(c context.CancelFunc) int {
	runMu.Lock()
	defer runMu.Unlock()
	runSeq++
	running[runSeq] = c
	return runSeq
}

func unregisterCancel(id int) {
	runMu.Lock()
	defer runMu.Unlock()
	delete(running, id)
}

// killAll cancels every in-flight remote command. Returns how many were running.
func killAll() int {
	runMu.Lock()
	defer runMu.Unlock()
	n := len(running)
	for id, c := range running {
		c()
		delete(running, id)
	}
	return n
}

// handleControl verifies the toggle and passphrase, then dispatches. It writes
// one ctrlResp back on c (newline-free, so a following byte stream is clean).
// r is the buffered reader over c, already positioned past the header line.
func handleControl(c net.Conn, r *bufio.Reader, h header) {
	reply := func(resp ctrlResp) { b, _ := json.Marshal(resp); c.Write(b) }
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
	setControlled(h.From + ": " + h.Op)
	defer setControlled("")
	switch h.Op {
	case "exec":
		var a execArgs
		json.Unmarshal(h.Data, &a)
		reply(runExec(a))
	case "launch":
		var a launchArgs
		json.Unmarshal(h.Data, &a)
		reply(runLaunch(a))
	case "screenshot":
		var a screenshotArgs
		json.Unmarshal(h.Data, &a)
		png, err := captureScreen(a.Display)
		if err != nil {
			reply(ctrlResp{Err: err.Error()})
			return
		}
		reply(ctrlResp{OK: true, Img: png})
	case "input":
		var a inputArgs
		json.Unmarshal(h.Data, &a)
		if err := sendInput(a); err != nil {
			reply(ctrlResp{Err: err.Error()})
			return
		}
		reply(ctrlResp{OK: true})
	case "put":
		var a putArgs
		json.Unmarshal(h.Data, &a)
		recvFile(r, reply, a)
	case "get":
		var a getArgs
		json.Unmarshal(h.Data, &a)
		sendFile(c, reply, a)
	default:
		reply(ctrlResp{Err: "unknown op: " + h.Op})
	}
}

// expandHome turns a leading ~/ into the peer's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// recvFile (op "put") writes an incoming byte stream to an explicit path. It
// acks readiness, reads exactly a.Size bytes, then reports the final status.
func recvFile(r *bufio.Reader, reply func(ctrlResp), a putArgs) {
	path := expandHome(a.Path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		reply(ctrlResp{Err: err.Error()})
		return
	}
	f, err := os.Create(path)
	if err != nil {
		reply(ctrlResp{Err: err.Error()})
		return
	}
	defer f.Close()
	reply(ctrlResp{OK: true}) // ready; sender starts streaming
	n, err := io.CopyN(f, r, a.Size)
	if err != nil {
		reply(ctrlResp{Err: err.Error(), N: n})
		return
	}
	reply(ctrlResp{OK: true, N: n})
}

// sendFile (op "get") streams an explicit path back to the requester: a status
// with the size, then the raw bytes.
func sendFile(c net.Conn, reply func(ctrlResp), a getArgs) {
	f, err := os.Open(expandHome(a.Path))
	if err != nil {
		reply(ctrlResp{Err: err.Error()})
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		reply(ctrlResp{Err: "not a regular file"})
		return
	}
	reply(ctrlResp{OK: true, N: st.Size()})
	io.Copy(c, f)
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
	id := registerCancel(cancel) // so the kill switch can stop it
	defer unregisterCancel(id)
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

// putFile streams a local file to p, which writes it at remote. Used by the
// put_file MCP tool on the gateway.
func putFile(p *peer, local, remote string) (ctrlResp, error) {
	f, err := os.Open(local)
	if err != nil {
		return ctrlResp{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ctrlResp{}, err
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(p.Addr, fmt.Sprint(p.Port)), 5*time.Second)
	if err != nil {
		return ctrlResp{}, err
	}
	defer c.Close()
	raw, _ := json.Marshal(putArgs{Path: remote, Size: st.Size()})
	h := header{Type: "control", From: myName(), Op: "put", Pass: remotePass(), Data: raw}
	if err := json.NewEncoder(c).Encode(h); err != nil {
		return ctrlResp{}, err
	}
	dec := json.NewDecoder(c)
	var ack ctrlResp
	if err := dec.Decode(&ack); err != nil {
		return ctrlResp{}, err
	}
	if !ack.OK {
		return ack, nil
	}
	if _, err := io.Copy(c, f); err != nil {
		return ctrlResp{}, err
	}
	var done ctrlResp
	if err := dec.Decode(&done); err != nil {
		return ctrlResp{}, err
	}
	return done, nil
}

// getFile pulls remote from p and writes it to the local path.
func getFile(p *peer, remote, local string) (ctrlResp, error) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(p.Addr, fmt.Sprint(p.Port)), 5*time.Second)
	if err != nil {
		return ctrlResp{}, err
	}
	defer c.Close()
	raw, _ := json.Marshal(getArgs{Path: remote})
	h := header{Type: "control", From: myName(), Op: "get", Pass: remotePass(), Data: raw}
	if err := json.NewEncoder(c).Encode(h); err != nil {
		return ctrlResp{}, err
	}
	c.SetReadDeadline(time.Now().Add(10 * time.Minute))
	dec := json.NewDecoder(c)
	var resp ctrlResp
	if err := dec.Decode(&resp); err != nil {
		return ctrlResp{}, err
	}
	if !resp.OK {
		return resp, nil
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return ctrlResp{}, err
	}
	f, err := os.Create(local)
	if err != nil {
		return ctrlResp{}, err
	}
	defer f.Close()
	// Bytes the decoder read past the JSON status are the start of the file.
	src := io.MultiReader(dec.Buffered(), c)
	n, err := io.CopyN(f, src, resp.N)
	if err != nil {
		return ctrlResp{}, err
	}
	return ctrlResp{OK: true, N: n}, nil
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
