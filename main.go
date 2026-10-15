// LANdrop: LAN peer discovery, chat and file transfer. Single binary, no server.
package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

//go:embed Icon.png
var iconPNG []byte

const mcastAddr = "239.77.77.77:7777"

type beacon struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Port int    `json:"port"`
}

type header struct {
	Type string `json:"type"` // "chat" | "file"
	From string `json:"from"`
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size,omitempty"`
}

type peer struct {
	beacon
	Addr string
	Seen time.Time
}

var (
	self      beacon
	mu        sync.Mutex
	peers     = map[string]*peer{}
	log       = widget.NewLabel("")
	logScroll = container.NewVScroll(log)
	peerList  *widget.List
	selected  = -1
	transfers = container.NewVBox()
	gui       bool
)

func main() {
	host, _ := os.Hostname()
	self = beacon{ID: fmt.Sprintf("%s-%d", host, os.Getpid()), Name: host}

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	self.Port = ln.Addr().(*net.TCPAddr).Port

	a := app.New()
	a.SetIcon(fyne.NewStaticResource("Icon.png", iconPNG))
	gui = true
	w := a.NewWindow("LANdrop - " + host)
	log.Wrapping = fyne.TextWrapWord

	peerList = widget.NewList(
		func() int { mu.Lock(); defer mu.Unlock(); return len(peers) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if p := sortedPeers(); i < len(p) {
				o.(*widget.Label).SetText(p[i].Name + " (" + p[i].Addr + ")")
			}
		})
	peerList.OnSelected = func(i widget.ListItemID) { selected = i }
	peerList.OnUnselected = func(widget.ListItemID) { selected = -1 }

	msg := widget.NewEntry()
	msg.SetPlaceHolder("Message (Enter to send) · /name <new name>")
	msg.OnSubmitted = func(s string) {
		if s == "" {
			return
		}
		if n, ok := strings.CutPrefix(s, "/name "); ok {
			n = strings.TrimSpace(n)
			if n == "" || len(n) > 64 {
				appendLog("usage: /name <new name>")
				return
			}
			mu.Lock()
			self.Name = n
			mu.Unlock()
			a.Preferences().SetString("name", n)
			w.SetTitle("LANdrop - " + n)
			appendLog("you are now \"" + n + "\" (peers update within a few seconds)")
			msg.SetText("")
			return
		}
		if p := target(w); p != nil {
			go send(p, header{Type: "chat", From: myName(), Text: s}, nil)
			appendLog("me -> " + p.Name + ": " + s)
			msg.SetText("")
		}
	}
	sendFile := widget.NewButton("Send File…", func() {
		p := target(w)
		if p == nil {
			return
		}
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			go sendPath(p, path, filepath.Base(path))
		}, w)
	})

	split := container.NewHSplit(peerList,
		container.NewBorder(nil, container.NewBorder(nil, nil, nil, sendFile, msg), nil, nil, container.NewBorder(nil, transfers, nil, nil, logScroll)))
	split.Offset = 0.3
	w.SetContent(split)
	w.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) {
		p := target(w)
		if p == nil {
			return
		}
		for _, u := range uris {
			root := u.Path()
			filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(filepath.Dir(root), path)
				go sendPath(p, path, filepath.ToSlash(rel))
				return nil
			})
		}
	})
	w.Resize(fyne.NewSize(800, 500))

	go announce()
	go discover()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	w.ShowAndRun()
}

var sem = make(chan struct{}, 4) // ponytail: max 4 parallel sends, raise if LAN is idle on many small files

func sendPath(p *peer, path, name string) {
	sem <- struct{}{}
	defer func() { <-sem }()
	f, err := os.Open(path)
	if err != nil {
		appendLog("error: " + err.Error())
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	appendLog(fmt.Sprintf("sending %s (%s) -> %s", name, humanBytes(float64(st.Size())), p.Name))
	t := time.Now()
	if err := sendFileRetry(p, header{Type: "file", From: myName(), Name: name, Size: st.Size()}, f); err != nil {
		appendLog("error: " + err.Error())
		return
	}
	appendLog(fmt.Sprintf("sent %s in %s (%s/s)", name, time.Since(t).Round(time.Millisecond), humanBytes(float64(st.Size())/time.Since(t).Seconds())))
}

func myName() string { mu.Lock(); defer mu.Unlock(); return self.Name }

func sortedPeers() []*peer {
	mu.Lock()
	defer mu.Unlock()
	out := make([]*peer, 0, len(peers))
	for _, p := range peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func target(w fyne.Window) *peer {
	if p := sortedPeers(); selected >= 0 && selected < len(p) {
		return p[selected]
	}
	dialog.ShowInformation("No peer", "Select a computer on the left first.", w)
	return nil
}

func appendLog(s string) {
	if !gui {
		return // no GUI (tests)
	}
	fyne.Do(func() {
		log.SetText(log.Text + time.Now().Format("15:04:05 ") + s + "\n")
		logScroll.ScrollToBottom()
	})
}

// announce multicasts our beacon every 2s.
func announce() {
	addr, _ := net.ResolveUDPAddr("udp4", mcastAddr)
	c, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		appendLog("multicast error: " + err.Error())
		return
	}
	for ; ; time.Sleep(2 * time.Second) {
		mu.Lock()
		b, _ := json.Marshal(self)
		mu.Unlock()
		c.Write(b)
	}
}

// discover listens for beacons; peers unseen for 7s are dropped.
func discover() {
	addr, _ := net.ResolveUDPAddr("udp4", mcastAddr)
	c, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		appendLog("multicast error: " + err.Error())
		return
	}
	c.SetReadBuffer(1 << 16)
	buf := make([]byte, 1500)
	for {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, from, err := c.ReadFromUDP(buf)
		var b beacon
		if err == nil && json.Unmarshal(buf[:n], &b) == nil && b.ID != self.ID {
			mu.Lock()
			peers[b.ID] = &peer{beacon: b, Addr: from.IP.String(), Seen: time.Now()}
			mu.Unlock()
		}
		mu.Lock()
		for id, p := range peers {
			if time.Since(p.Seen) > 7*time.Second {
				delete(peers, id)
			}
		}
		mu.Unlock()
		fyne.Do(peerList.Refresh)
	}
}

// progress is one transfer: a row (label, bar, cancel) that writes through to w.
type progress struct {
	w     io.Writer
	n, of int64
	last  time.Time
	bar   *widget.ProgressBar
	stat  *widget.Label
	row   fyne.CanvasObject
	start time.Time
	from  int64 // bytes already present when this transfer started
}

func newProgress(w io.Writer, c net.Conn, label string, n, of int64) *progress {
	p := &progress{w: w, n: n, of: of, from: n, start: time.Now()}
	if !gui {
		return p
	}
	p.bar = widget.NewProgressBar()
	p.stat = widget.NewLabel("")
	p.row = container.NewBorder(nil, nil, widget.NewLabel(label), container.NewHBox(p.stat, widget.NewButton("Cancel", func() { c.Close() })), p.bar)
	fyne.Do(func() { transfers.Add(p.row) })
	return p
}

func (p *progress) done() {
	if gui {
		fyne.Do(func() { transfers.Remove(p.row) })
	}
}

func (p *progress) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if gui && (time.Since(p.last) > 100*time.Millisecond || p.n == p.of) {
		p.last = time.Now()
		v := float64(p.n) / float64(p.of)
		speed := float64(p.n-p.from) / time.Since(p.start).Seconds()
		stat := humanBytes(speed) + "/s"
		if speed > 0 {
			stat += "  ETA " + (time.Duration(float64(p.of-p.n)/speed) * time.Second).Round(time.Second).String()
		}
		fyne.Do(func() { p.bar.SetValue(v); p.stat.SetText(stat) })
	}
	return n, err
}

func humanBytes(b float64) string {
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if b < 1000 {
			return fmt.Sprintf("%.1f %s", b, u)
		}
		b /= 1000
	}
	return fmt.Sprintf("%.1f PB", b)
}

// send writes a JSON header line then streams body (if any) over one TCP connection.
// For files the receiver first answers with the byte offset it already has (resume).
func send(p *peer, h header, body *os.File) error {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(p.Addr, fmt.Sprint(p.Port)), 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(h); err != nil {
		return err
	}
	if body == nil {
		return nil
	}
	var off struct{ Offset int64 }
	if err := json.NewDecoder(c).Decode(&off); err != nil {
		return err
	}
	if _, err := body.Seek(off.Offset, io.SeekStart); err != nil {
		return err
	}
	pr := newProgress(c, c, "→ "+h.Name, off.Offset, h.Size)
	defer pr.done()
	_, err = io.Copy(pr, body)
	return err
}

// sendFileRetry resumes automatically on failure; a manual re-send also resumes.
func sendFileRetry(p *peer, h header, f *os.File) (err error) {
	for i := 0; i < 3; i++ {
		if err = send(p, h, f); err == nil {
			return nil
		}
		if strings.Contains(err.Error(), "closed") {
			return err // cancelled locally
		}
		appendLog("retrying: " + err.Error())
		time.Sleep(2 * time.Second)
	}
	return err
}

func handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReaderSize(c, 1<<20)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var h header
	if json.Unmarshal(line, &h) != nil {
		return
	}
	switch h.Type {
	case "chat":
		appendLog(h.From + ": " + h.Text)
	case "file":
		dir, _ := os.UserHomeDir()
		dir = filepath.Join(dir, "Downloads")
		os.MkdirAll(dir, 0o755)
		name := filepath.Clean(filepath.FromSlash(h.Name)) // trust boundary: must stay inside Downloads
		if name == "." || name == "" || h.Size < 0 || filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
			name = filepath.Base(h.Name)
		}
		part := filepath.Join(dir, name+".part")
		os.MkdirAll(filepath.Dir(part), 0o755)
		f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			appendLog("error: " + err.Error())
			return
		}
		var offset int64
		if st, _ := f.Stat(); st != nil && st.Size() <= h.Size {
			offset = st.Size() // ponytail: resume keyed on name+size only; add a hash if files change in place
		}
		f.Truncate(offset)
		f.Seek(offset, io.SeekStart)
		if json.NewEncoder(c).Encode(map[string]int64{"offset": offset}) != nil {
			f.Close()
			return
		}
		appendLog(fmt.Sprintf("receiving %s (%s) from %s at %d%%", name, humanBytes(float64(h.Size)), h.From, offset*100/max(h.Size, 1)))
		t := time.Now()
		pr := newProgress(f, c, "← "+name, offset, h.Size)
		n, err := io.CopyN(pr, r, h.Size-offset)
		pr.done()
		f.Close()
		if err != nil || n != h.Size-offset {
			appendLog("transfer interrupted, will resume: " + name)
			return
		}
		path := filepath.Join(dir, name)
		for i := 1; ; i++ { // don't overwrite
			if _, err := os.Stat(path); err != nil {
				break
			}
			ext := filepath.Ext(name)
			path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
		}
		os.Rename(part, path)
		appendLog(fmt.Sprintf("saved %s in %s (%s/s)", path, time.Since(t).Round(time.Millisecond), humanBytes(float64(h.Size-offset)/time.Since(t).Seconds())))
	}
}
