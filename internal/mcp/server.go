// Package mcp exposes mcp-electron as a Model Context Protocol server over
// stdio: tools to launch, observe, drive and debug an Electron application
// from Claude Code, Codex or any other MCP host.
//
// One long-lived session per application keeps the CDP connection, the
// per-window ref snapshots and the logs across tool calls, so a ref read by
// snapshot stays valid for the next click.
package mcp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dev-toolings/mcp-electron/internal/electron"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

// Options configure the server.
type Options struct {
	// ArtifactsRoot receives per-session screenshots and flow outputs
	// (default: the user cache directory, mcp-electron/sessions).
	ArtifactsRoot string
	Version       string
	// MaxSessions caps open sessions (0: electron.DefaultMaxSessions).
	MaxSessions int
	// IdleTimeout stops a launched app unused for that long (0: default,
	// negative: never).
	IdleTimeout time.Duration
}

// Server holds the application sessions and the tool handlers.
type Server struct {
	opts  Options
	mgr   *electron.Manager
	trace *Tracer
}

// New creates a server; Build registers the tools on an MCP server.
func New(opts Options) (*Server, error) {
	mgr, err := electron.NewManager(opts.ArtifactsRoot)
	if err == nil {
		mgr.MaxSessions, mgr.IdleTimeout = opts.MaxSessions, opts.IdleTimeout
	}
	if err != nil {
		return nil, err
	}
	return &Server{opts: opts, mgr: mgr, trace: NewTracer(tracePath())}, nil
}

// Manager exposes the session manager (CLI flows reuse it).
func (s *Server) Manager() *electron.Manager { return s.mgr }

// Instructions is what the MCP host shows the model about this server.
const Instructions = `mcp-electron drives one Electron application for development and debugging.
Loop: launch (project dir, .app, or a dev command) or attach → snapshot (refs @N) → click/type/press/… by ref, CSS selector or getByRole/getByText/getByLabel → wait_for → logs on errors → screenshot when layout matters → stop.
Actions return a diff of the interactive elements; re-snapshot after a navigation or a big UI change (refs are per window).
eval runs JavaScript in a window (renderer); eval_main runs in the main process (app, BrowserWindow, ipcMain, dialog… are in scope).
A dev command (bun run dev) that restarts Electron on recompilation is followed automatically, by a launched or an attached session: tools answer "restarting" until the new instance is reachable; never attach again (attach to an endpoint already driven returns its session).
When a call fails, hangs, times out or is interrupted, read trace (problems: true; with_stacks: true for a call reported stuck): every call's arguments, duration and outcome, host cancellations, and the server's goroutines when a call ran past 20 s. Use it to find the cause and fix mcp-electron itself.`

// toolDef is one entry of the tool table.
type toolDef struct {
	tool    mcpgo.Tool
	handler func(*Server, context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
}

// Build returns an MCP server with every tool registered.
func (s *Server) Build() *mcpsrv.MCPServer {
	srv := mcpsrv.NewMCPServer("mcp-electron", s.opts.Version,
		mcpsrv.WithToolCapabilities(false),
		mcpsrv.WithInstructions(Instructions),
		mcpsrv.WithRecovery(),
	)
	for _, def := range s.tools() {
		h := def.handler
		srv.AddTool(def.tool, s.trace.Wrap(def.tool.Name, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return h(s, ctx, req)
		}))
	}
	return srv
}

// Tools returns the sorted tool names.
func (s *Server) Tools() []string {
	defs := s.tools()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.tool.Name)
	}
	sort.Strings(names)
	return names
}

func (s *Server) tools() []toolDef {
	var defs []toolDef
	defs = append(defs, s.sessionTools()...)
	defs = append(defs, s.pageTools()...)
	defs = append(defs, s.flowTools()...)
	defs = append(defs, s.traceTools()...)
	return defs
}

// Close stops every application the server launched.
func (s *Server) Close() {
	s.mgr.Close()
	s.trace.Close()
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[mcp-electron] "+strings.TrimRight(format, "\n")+"\n", args...)
}
