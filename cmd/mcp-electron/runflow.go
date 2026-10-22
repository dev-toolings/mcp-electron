package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dev-toolings/mcp-electron/internal/electron"
	"github.com/dev-toolings/mcp-electron/internal/flow"
	"github.com/dev-toolings/mcp-electron/internal/mcp"
)

type kvFlag map[string]string

func (k kvFlag) String() string { return "" }
func (k kvFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return fmt.Errorf("expected NAME=VALUE, got %q", v)
	}
	k[name] = value
	return nil
}

// runFlow launches an application, runs one flow file against its first
// window and exits 1 when a step fails (for CI and pre-commit checks).
func runFlow(args []string) error {
	fs := flag.NewFlagSet("run-flow", flag.ContinueOnError)
	target := fs.String("target", "", "Electron project dir, .app or executable to launch")
	endpoint := fs.String("attach", "", "attach to a running app instead (http://127.0.0.1:PORT)")
	window := fs.Int("window", 0, "window index")
	timeout := fs.Duration("timeout", 10*time.Second, "element lookup timeout")
	startup := fs.Duration("startup-timeout", 0, "wait for the app's debugging port")
	noIsolate := fs.Bool("no-isolate", false, "do not give the app a private user-data-dir")
	out := fs.String("out", "", "directory for screenshots (default: a temp dir)")
	env := kvFlag{}
	fs.Var(env, "env", "application environment variable NAME=VALUE (repeatable)")
	flowEnv := kvFlag{}
	fs.Var(flowEnv, "var", "flow ${NAME} value (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mcp-electron run-flow FILE (--target PATH | --attach URL | -- CMD ARGS...) [flags]")
		fs.PrintDefaults()
	}
	// The flow file may come first (flag parsing stops at the first
	// positional argument) or after the flags; a dev command follows "--".
	var file string
	if len(args) > 0 && !isFlag(args[0]) {
		file, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := fs.Args()
	if file == "" {
		if len(command) == 0 {
			fs.Usage()
			return errors.New("flow file is required")
		}
		file, command = command[0], command[1:]
	}
	f, err := flow.ParseFile(file)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if *target == "" && *endpoint == "" && len(command) == 0 {
		return errors.New("give --target, --attach or a dev command after the flow file")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mgr, err := electron.NewManager(*out)
	if err != nil {
		return err
	}
	defer mgr.Close()
	var sess *electron.Session
	if *endpoint != "" {
		sess, err = mgr.Attach(ctx, electron.AttachSpec{Endpoint: *endpoint})
	} else {
		sess, err = mgr.Launch(ctx, electron.LaunchSpec{Target: *target, Command: command, Env: env, Isolate: !*noIsolate, Inspect: true, StartupTimeout: *startup})
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "launched %s (cdp %s)\n", sess.Name, sess.Endpoint)
	b, page, err := sess.Page(ctx, *window)
	if err != nil {
		return err
	}
	r := &flow.Runner{Browser: b, Page: page, Press: mcp.PressKeys, OutDir: sess.Dir, Env: flowEnv, LookupTimeout: *timeout}
	if main := sess.Main(); main != nil {
		r.Main = func(ctx context.Context, expr string) (string, error) { return main.Eval(ctx, expr, 8*time.Second) }
	}
	res := r.Run(ctx, f)
	fmt.Println(res.Summary())
	if !res.Passed {
		return errors.New("flow failed")
	}
	return nil
}
