package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
)

// Round-trips an exec request through the real handle() -> handleControl() path
// and checks the passphrase gate and the output.
func TestControlExec(t *testing.T) {
	self.Name = "tester"
	remoteOn, remotePw = true, "secret"

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	p := &peer{Addr: "127.0.0.1", beacon: beacon{Port: ln.Addr().(*net.TCPAddr).Port, Name: "peer"}}

	r, err := callControl(p, "exec", execArgs{Line: "echo hello-landrop"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || !strings.Contains(r.Out, "hello-landrop") {
		t.Fatalf("exec failed: %+v", r)
	}

	// Wrong passphrase must be rejected (receiver keeps "secret").
	c, _ := net.Dial("tcp", net.JoinHostPort(p.Addr, fmt.Sprint(p.Port)))
	json.NewEncoder(c).Encode(header{Type: "control", From: "tester", Op: "exec", Pass: "wrong"})
	var resp ctrlResp
	json.NewDecoder(c).Decode(&resp)
	c.Close()
	if resp.OK || resp.Err == "" {
		t.Fatalf("bad passphrase was not rejected: %+v", resp)
	}

	// Disabled toggle must be rejected.
	remoteOn = false
	r, _ = callControl(p, "exec", execArgs{Line: "echo nope"})
	if r.OK {
		t.Fatalf("disabled remote control still ran: %+v", r)
	}
}
