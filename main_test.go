package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Round-trips a file over the wire protocol and checks it lands in ~/Downloads intact.
func TestFileRoundTrip(t *testing.T) {
	self.Name = "tester"
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { c, _ := ln.Accept(); handle(c) }()
	p := &peer{Addr: "127.0.0.1", beacon: beacon{Port: ln.Addr().(*net.TCPAddr).Port, Name: "peer"}}
	data := bytes.Repeat([]byte("landrop"), 1<<18) // ~1.8 MB
	name := "landrop_test_" + time.Now().Format("150405.000") + ".bin"
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "Downloads", name)
	defer os.Remove(path)
	// simulate an interrupted transfer: half the file already in the .part
	os.WriteFile(path+".part", data[:len(data)/2], 0o644)
	tmp := filepath.Join(t.TempDir(), "src")
	os.WriteFile(tmp, data, 0o644)
	f, _ := os.Open(tmp)
	defer f.Close()
	if err := send(p, header{Type: "file", From: "tester", Name: "../../" + name, Size: int64(len(data))}, f); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for i := 0; i < 50; i++ {
		got, _ = os.ReadFile(path)
		if len(got) == len(data) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("file mismatch: got %d bytes, want %d", len(got), len(data))
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[float64]string{512: "512.0 B", 1500: "1.5 KB", 12.3e6: "12.3 MB", 2e9: "2.0 GB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%v) = %q, want %q", in, got, want)
		}
	}
}
