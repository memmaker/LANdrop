// MCP gateway: the running app serves an MCP server on 127.0.0.1 so a local LLM
// agent can drive peers on the LAN. It binds to localhost only; it is never
// reachable from the network. Each tool forwards to a peer as a control request.
package main

import (
	"context"
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

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	appendLog("MCP server on http://" + mcpAddr + " (localhost only)")
	if err := http.ListenAndServe(mcpAddr, handler); err != nil {
		appendLog("MCP server error: " + err.Error())
	}
}
