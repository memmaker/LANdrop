package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ctrlListener starts a handle() loop on loopback and returns a peer pointing at it.
func ctrlListener(t *testing.T) *peer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	return &peer{Addr: "127.0.0.1", beacon: beacon{Port: ln.Addr().(*net.TCPAddr).Port, Name: "peer"}}
}

// Round-trips a file out to a peer (put) and back (get), checking the bytes
// survive the JSON-status-then-stream framing on one connection.
func TestControlFiles(t *testing.T) {
	self.Name = "tester"
	remoteOn, remotePw = true, "secret"
	p := ctrlListener(t)

	dir := t.TempDir()
	data := bytes.Repeat([]byte("landrop-files-0123456789"), 5000) // ~117 KB
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}

	pushed := filepath.Join(dir, "pushed.bin")
	if r, err := putFile(p, src, pushed); err != nil || !r.OK {
		t.Fatalf("put: %+v err=%v", r, err)
	}
	if got, _ := os.ReadFile(pushed); !bytes.Equal(got, data) {
		t.Fatalf("put mismatch: got %d bytes, want %d", len(got), len(data))
	}

	pulled := filepath.Join(dir, "pulled.bin")
	if r, err := getFile(p, pushed, pulled); err != nil || !r.OK {
		t.Fatalf("get: %+v err=%v", r, err)
	}
	if got, _ := os.ReadFile(pulled); !bytes.Equal(got, data) {
		t.Fatalf("get mismatch: got %d bytes, want %d", len(got), len(data))
	}
}

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
