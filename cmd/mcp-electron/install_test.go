package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeCLI struct {
	registered map[string]bool
	calls      []string
}

func (f *fakeCLI) run(name string, argv ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(argv, " "))
	switch argv[1] {
	case "get":
		if f.registered[name] {
			return "mcp-electron: ok", nil
		}
		return `No MCP server named "mcp-electron"`, errors.New("exit status 1")
	case "add":
		f.registered[name] = true
	case "remove":
		delete(f.registered, name)
	}
	return "", nil
}

func newTestInstaller(t *testing.T, dryRun bool, cli *fakeCLI) (*installer, *strings.Builder) {
	t.Helper()
	out := &strings.Builder{}
	return &installer{dryRun: dryRun, out: out, home: t.TempDir(), lookPath: func(s string) (string, error) { return "/bin/" + s, nil }, run: cli.run}, out
}

func TestInstallRegistersCopiesAndWritesSkill(t *testing.T) {
	cli := &fakeCLI{registered: map[string]bool{"claude": true}}
	in, out := newTestInstaller(t, false, cli)
	src := filepath.Join(t.TempDir(), "src-binary")
	if err := os.WriteFile(src, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(in.home, ".local", "bin", serverName)
	if err := in.copyBinary(src, exe); err != nil {
		t.Fatal(err)
	}
	if err := in.install(exe, true); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("binary not copied: %v", err)
	}
	for _, p := range []string{filepath.Join(in.home, ".claude", "skills", serverName, "SKILL.md"), filepath.Join(in.home, ".codex", "skills", serverName, "references", "tools.md")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("skill file missing: %s", p)
		}
	}
	joined := strings.Join(cli.calls, "\n")
	// Already registered with Claude: removed first, then added; Codex just added.
	if !strings.Contains(joined, "claude mcp remove --scope user mcp-electron") || !strings.Contains(joined, "claude mcp add --scope user mcp-electron -- "+exe+" mcp") || !strings.Contains(joined, "codex mcp add mcp-electron -- "+exe+" mcp") {
		t.Fatalf("unexpected calls:\n%s", joined)
	}
	if !cli.registered["claude"] || !cli.registered["codex"] {
		t.Fatal("both hosts must end registered")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	cli := &fakeCLI{registered: map[string]bool{}}
	in, out := newTestInstaller(t, true, cli)
	src := filepath.Join(t.TempDir(), "src-binary")
	if err := os.WriteFile(src, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(in.home, ".local", "bin", serverName)
	if err := in.copyBinary(src, exe); err != nil {
		t.Fatal(err)
	}
	if err := in.install(exe, true); err != nil {
		t.Fatalf("dry install: %v", err)
	}
	if err := in.uninstall(filepath.Dir(exe), true); err != nil {
		t.Fatalf("dry uninstall: %v", err)
	}
	entries, _ := os.ReadDir(in.home)
	if len(entries) != 0 {
		t.Fatalf("dry run created files under home: %v", entries)
	}
	for _, c := range cli.calls {
		if !strings.Contains(c, " get ") {
			t.Fatalf("dry run ran a mutating command: %s", c)
		}
	}
	if !strings.Contains(out.String(), "dry run: nothing is written") || !strings.Contains(out.String(), "$ claude mcp add") || !strings.Contains(out.String(), "copy "+src) {
		t.Fatalf("dry run must print its plan:\n%s", out)
	}
}

func TestUninstallRemovesEverythingAndVerifies(t *testing.T) {
	cli := &fakeCLI{registered: map[string]bool{"claude": true, "codex": true}}
	in, out := newTestInstaller(t, false, cli)
	exe := filepath.Join(in.home, ".local", "bin", serverName)
	skillFile := filepath.Join(in.home, ".claude", "skills", serverName, "SKILL.md")
	for _, p := range []string{exe, skillFile} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := in.uninstall(filepath.Dir(exe), true); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	for _, p := range []string{exe, filepath.Join(in.home, ".claude", "skills", serverName)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still present", p)
		}
	}
	if len(cli.registered) != 0 {
		t.Fatalf("hosts still registered: %v", cli.registered)
	}
	if !strings.Contains(out.String(), "Codex\nnot registered") && !strings.Contains(out.String(), "$ codex mcp remove mcp-electron") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestUninstallReportsStubbornRegistration(t *testing.T) {
	cli := &fakeCLI{registered: map[string]bool{"claude": true}}
	stubborn := func(name string, argv ...string) (string, error) {
		if argv[1] == "remove" {
			return "", nil // pretends to succeed but keeps the entry
		}
		return cli.run(name, argv...)
	}
	in, _ := newTestInstaller(t, false, cli)
	in.run = stubborn
	err := in.uninstall(t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "still registered") {
		t.Fatalf("expected a verification failure, got %v", err)
	}
}
