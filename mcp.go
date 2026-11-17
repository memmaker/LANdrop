// MCP gateway: the running app serves an MCP server on 127.0.0.1 so a local LLM
// agent can drive peers on the LAN. It binds to localhost only; it is never
// reachable from the network. Each tool forwards to a peer as a control request.
package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpAddr = "127.0.0.1:7788"

type peerInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Addr string `json:"addr"`
}

type execIn struct {
	Peer    string `json:"peer" jsonschema:"peer id or display name, from list_peers"`
	Line    string `json:"line" jsonschema:"command line, run through the platform shell (sh on macOS, cmd on Windows)"`
	Dir     string `json:"dir,omitempty" jsonschema:"working directory"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"timeout in seconds (default 60)"`
}

type launchIn struct {
	Peer string   `json:"peer" jsonschema:"peer id or display name, from list_peers"`
	App  string   `json:"app" jsonschema:"application to open (name or path)"`
	Args []string `json:"args,omitempty" jsonschema:"arguments passed to the application"`
}

type screenshotIn struct {
	Peer    string `json:"peer" jsonschema:"peer id or display name, from list_peers"`
	Display int    `json:"display,omitempty" jsonschema:"display index, 0 = primary"`
}

type inputIn struct {
	Peer string `json:"peer" jsonschema:"peer id or display name, from list_peers"`
	Text string `json:"text,omitempty" jsonschema:"literal text to type"`
	Key  string `json:"key,omitempty" jsonschema:"one key or chord, e.g. enter, tab, cmd+c, ctrl+shift+t"`
}

type putIn struct {
	Peer   string `json:"peer" jsonschema:"peer id or display name, from list_peers"`
	Local  string `json:"local" jsonschema:"source file path on this (gateway) machine"`
	Remote string `json:"remote" jsonschema:"destination file path on the peer (~ allowed)"`
}

type getIn struct {
	Peer   string `json:"peer" jsonschema:"peer id or display name, from list_peers"`
	Remote string `json:"remote" jsonschema:"source file path on the peer (~ allowed)"`
	Local  string `json:"local" jsonschema:"destination file path on this (gateway) machine"`
}

type listOut struct {
	Peers []peerInfo `json:"peers"`
}

// startMCP runs the MCP HTTP server (localhost only).
func startMCP() {
	s := mcp.NewServer(&mcp.Implementation{Name: "landrop", Version: "0.1.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "list_peers",
		Description: "List LANdrop machines currently online on the LAN."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listOut, error) {
			out := listOut{Peers: []peerInfo{}}
			for _, p := range sortedPeers() {
				out.Peers = append(out.Peers, peerInfo{ID: p.ID, Name: p.Name, Addr: p.Addr})
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "exec",
		Description: "Run a shell command on a peer and return its combined output and exit code. The peer must have remote control enabled and the matching passphrase."},
		func(_ context.Context, _ *mcp.CallToolRequest, in execIn) (*mcp.CallToolResult, ctrlResp, error) {
			p, err := findPeer(in.Peer)
			if err != nil {
				return nil, ctrlResp{}, err
			}
			r, err := callControl(p, "exec", execArgs{Line: in.Line, Dir: in.Dir, Timeout: in.Timeout})
			return nil, r, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "launch_app",
		Description: "Open a desktop application on a peer."},
		func(_ context.Context, _ *mcp.CallToolRequest, in launchIn) (*mcp.CallToolResult, ctrlResp, error) {
			p, err := findPeer(in.Peer)
			if err != nil {
				return nil, ctrlResp{}, err
			}
			r, err := callControl(p, "launch", launchArgs{App: in.App, Args: in.Args})
			return nil, r, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "screenshot",
		Description: "Capture a peer's screen and return it as a PNG image. On macOS the peer needs the Screen Recording permission."},
		func(_ context.Context, _ *mcp.CallToolRequest, in screenshotIn) (*mcp.CallToolResult, struct{}, error) {
			p, err := findPeer(in.Peer)
			if err != nil {
				return nil, struct{}{}, err
			}
			r, err := callControl(p, "screenshot", screenshotArgs{Display: in.Display})
			if err != nil {
				return nil, struct{}{}, err
			}
			if !r.OK {
				return nil, struct{}{}, fmt.Errorf("%s", r.Err)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: r.Img, MIMEType: "image/png"}}}, struct{}{}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "send_input",
		Description: "Type text or press a key/chord on a peer. On macOS the peer needs the Accessibility permission."},
		func(_ context.Context, _ *mcp.CallToolRequest, in inputIn) (*mcp.CallToolResult, ctrlResp, error) {
			p, err := findPeer(in.Peer)
			if err != nil {
				return nil, ctrlResp{}, err
			}
			r, err := callControl(p, "input", inputArgs{Text: in.Text, Key: in.Key})
			return nil, r, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "put_file",
		Description: "Copy a file from this machine to a peer at an explicit destination path."},
		func(_ context.Context, _ *mcp.CallToolRequest, in putIn) (*mcp.CallToolResult, ctrlResp, error) {
			p, err := findPeer(in.Peer)
			if err != nil {
				return nil, ctrlResp{}, err
			}
			r, err := putFile(p, in.Local, in.Remote)
			return nil, r, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_file",
		Description: "Copy a file from a peer to an explicit destination path on this machine."},
		func(_ context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, ctrlResp, error) {
			p, err := findPeer(in.Peer)
			if err != nil {
				return nil, ctrlResp{}, err
			}
			r, err := getFile(p, in.Remote, in.Local)
			return nil, r, err
		})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	appendLog("MCP server on http://" + mcpAddr + " (localhost only)")
	if err := http.ListenAndServe(mcpAddr, handler); err != nil {
		appendLog("MCP server error: " + err.Error())
	}
}
