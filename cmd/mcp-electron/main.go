// mcp-electron is an MCP server (stdio) that lets a coding agent launch,
// observe, drive and debug an Electron application.
//
//	mcp-electron                 serve MCP over stdio (default)
//	mcp-electron install         register with Claude Code (and --codex) and install the skill
//	mcp-electron run-flow FILE   run a Maestro-style flow against a launched app (CI)
//	mcp-electron version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dev-toolings/mcp-electron/internal/electron"
	"github.com/dev-toolings/mcp-electron/internal/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	cmd := "mcp"
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "mcp", "serve":
		err = serve(args)
	case "install":
		err = install(args)
	case "run-flow":
		err = runFlow(args)
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "mcp-electron: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-electron: %v\n", err)
		os.Exit(1)
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

func usage() {
	fmt.Fprint(os.Stderr, `mcp-electron: drive and debug an Electron app from an MCP agent

  mcp-electron [mcp] [--artifacts DIR] [--max-sessions N] [--idle-timeout D]
                                            serve MCP over stdio (defaults: 3 sessions,
                                            unused launched apps stopped after 30m)
  mcp-electron install [--dry-run] [--no-codex] [--dest DIR | --bin PATH]
                                            copy the binary to ~/.local/bin, register it
                                            with Claude Code (user scope) and Codex, and
                                            install the skill; --dry-run writes nothing
  mcp-electron install --uninstall [--dry-run]
                                            remove registrations, skills and the copy
  mcp-electron run-flow FILE (--target PATH | --command CMD...) [--env K=V]...
                                            launch the app, run a flow, exit 1 on failure
  mcp-electron version
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	artifacts := fs.String("artifacts", "", "directory for screenshots and flow outputs")
	maxSessions := fs.Int("max-sessions", electron.DefaultMaxSessions, "open sessions allowed at once (each is an Electron process tree)")
	idle := fs.Duration("idle-timeout", electron.DefaultIdleTimeout, "stop a launched app unused for this long (0 disables)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *idle == 0 {
		*idle = -1
	}
	s, err := mcp.New(mcp.Options{ArtifactsRoot: *artifacts, Version: version, MaxSessions: *maxSessions, IdleTimeout: *idle})
	if err != nil {
		return err
	}
	defer s.Close()
	// Stop every launched application when the host disconnects or on a
	// signal: an agent's forgotten session must not outlive the server.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := mcpsrv.NewStdioServer(s.Build())
	srv.SetErrorLogger(log.New(os.Stderr, "[mcp-electron] ", log.LstdFlags))
	fmt.Fprintln(os.Stderr, "[mcp-electron] ready on stdio")
	if err := srv.Listen(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
