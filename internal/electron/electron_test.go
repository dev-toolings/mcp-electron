package electron

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExpandPorts(t *testing.T) {
	got := expandPorts("--remote-debugging-port=${MCP_ELECTRON_CDP_PORT} --inspect=127.0.0.1:$MCP_ELECTRON_INSPECT_PORT", 9222, 9229)
	want := "--remote-debugging-port=9222 --inspect=127.0.0.1:9229"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMergeEnvExportsPortsAndDropsRunAsNode(t *testing.T) {
	t.Setenv("ELECTRON_RUN_AS_NODE", "1")
	t.Setenv("KEEP_ME", "yes")
	env := mergeEnv(map[string]string{"APP_DATA": "/tmp/x", "KEEP_ME": "override"}, 9222, 9229)
	joined := strings.Join(env, "\n")
	for _, want := range []string{"APP_DATA=/tmp/x", "KEEP_ME=override", EnvCDPPort + "=9222", EnvInspectPort + "=9229"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}
	if strings.Contains(joined, "ELECTRON_RUN_AS_NODE") {
		t.Error("ELECTRON_RUN_AS_NODE must be dropped")
	}
	if strings.Count(joined, "KEEP_ME=") != 1 {
		t.Error("overridden variable must appear once")
	}
}

func TestResolveProjectRequiresElectron(t *testing.T) {
	dir := t.TempDir()
	if _, err := Resolve(dir); err == nil || !strings.Contains(err.Error(), "package.json") {
		t.Fatalf("expected a package.json error, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"demo","productName":"Demo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(dir); err == nil || !strings.Contains(err.Error(), "node_modules/electron") {
		t.Fatalf("expected a missing electron error, got %v", err)
	}
	bin := electronBinaryPathFor(dir)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tgt, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Name != "Demo" || tgt.Dir != dir || len(tgt.Args) != 1 || tgt.Args[0] != "." || tgt.Packaged {
		t.Fatalf("unexpected target %+v", tgt)
	}
}

func electronBinaryPathFor(dir string) string {
	// Mirror electronBinary's layout for the current OS.
	defer func() { _ = recover() }()
	candidates := []string{
		filepath.Join(dir, "node_modules", "electron", "dist", "Electron.app", "Contents", "MacOS", "Electron"),
		filepath.Join(dir, "node_modules", "electron", "dist", "electron.exe"),
		filepath.Join(dir, "node_modules", "electron", "dist", "electron"),
	}
	for _, c := range candidates {
		_ = os.MkdirAll(filepath.Dir(c), 0o755)
		_ = os.WriteFile(c, []byte("#!/bin/sh\n"), 0o755)
	}
	return electronBinary(dir)
}

func TestResolveBundleRejectsNonElectron(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Demo.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>Demo</string><key>CFBundleIdentifier</key><string>dev.demo</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(app); err == nil || !strings.Contains(err.Error(), "Electron Framework") {
		t.Fatalf("expected a non-Electron error, got %v", err)
	}
	if err := os.MkdirAll(filepath.Join(app, "Contents", "Frameworks", "Electron Framework.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	tgt, err := Resolve(app)
	if err != nil {
		t.Fatal(err)
	}
	if tgt.BundleID != "dev.demo" || tgt.Name != "Demo" || !tgt.Packaged {
		t.Fatalf("unexpected target %+v", tgt)
	}
}

func TestRingKeepsLastLinesAndFinds(t *testing.T) {
	r := NewRing(3)
	w := r.Writer("stderr")
	_, _ = w.Write([]byte("one\ntwo\nDebugger listening on ws://127.0.0.1:9229/abc\nfour\n"))
	e := r.Entries()
	if len(e) != 3 || e[0].Text != "two" || e[2].Text != "four" {
		t.Fatalf("entries %+v", e)
	}
	if got := r.Find("Debugger listening on "); got != "ws://127.0.0.1:9229/abc" {
		t.Fatalf("find %q", got)
	}
	if !e[0].IsError() {
		t.Fatal("stderr lines are errors")
	}
}

func TestProbeCDPRejectsNodeInspector(t *testing.T) {
	// No server: probe must fail fast, not hang.
	if _, ok := probeCDP(t.Context(), "http://127.0.0.1:1"); ok {
		t.Fatal("probe of a closed port must fail")
	}
}

func TestParsePSReadsMemoryAndCPU(t *testing.T) {
	list := parsePS("  12   1 204800  3.5 /Apps/Demo.app/Contents/MacOS/Demo --flag a b\n 13  12 1024 0.0 helper\nbad line\n")
	if len(list) != 2 {
		t.Fatalf("entries %+v", list)
	}
	if list[0].pid != 12 || list[0].ppid != 1 || list[0].rssKB != 204800 || list[0].cpu != 3.5 || list[0].cmd != "/Apps/Demo.app/Contents/MacOS/Demo --flag a b" {
		t.Fatalf("entry %+v", list[0])
	}
}

func TestManagerAdmitCapsSessions(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.MaxSessions = 2
	for i := 0; i < 2; i++ {
		s := &Session{Info: Info{Name: "fake"}}
		if err := m.admit(); err != nil {
			t.Fatalf("session %d refused: %v", i, err)
		}
		if _, err := m.add(s); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { close(s.stopWatch) })
	}
	err = m.admit()
	if err == nil || !strings.Contains(err.Error(), "2 sessions are already open") || !strings.Contains(err.Error(), "s1 (fake)") {
		t.Fatalf("expected a cap error naming the sessions, got %v", err)
	}
}

func TestIdleTimeoutStopsOwnedSession(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.IdleTimeout = 500 * time.Millisecond
	s := &Session{Info: Info{Name: "fake", Owned: true}, ring: NewRing(10)}
	if _, err := m.add(s); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if st.IdleStopIn <= 0 || st.IdleStopIn > 500*time.Millisecond {
		t.Fatalf("idle stop not announced: %+v", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(m.List()) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if len(m.List()) != 0 {
		t.Fatal("idle session was not stopped")
	}
	if got := s.ring.Find("stopped after "); !strings.Contains(got, "idle timeout") {
		t.Fatalf("idle reason not logged: %q", got)
	}
}

func TestStopDeletesArtifactsUnlessKept(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []bool{false, true} {
		s, err := m.add(&Session{Info: Info{Name: "fake"}})
		if err != nil {
			t.Fatal(err)
		}
		shot := filepath.Join(s.Dir, "screenshot.png")
		if err := os.WriteFile(shot, []byte("png"), 0o600); err != nil {
			t.Fatal(err)
		}
		stop := m.Stop
		if keep {
			stop = m.StopKeepArtifacts
		}
		rep, err := stop(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(s.Dir)
		if keep {
			if statErr != nil || !rep.ArtifactsKept || rep.ArtifactsDir != s.Dir {
				t.Fatalf("keep: artifacts must stay, got %v %+v", statErr, rep)
			}
		} else if !os.IsNotExist(statErr) || rep.ArtifactsKept {
			t.Fatalf("stop: artifacts must be deleted, got %v %+v", statErr, rep)
		}
	}
}

func TestNewManagerSweepsOnlyStaleArtifacts(t *testing.T) {
	root := t.TempDir()
	stale, fresh := filepath.Join(root, "old-s1"), filepath.Join(root, "new-s1")
	for _, d := range []string{stale, fresh} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-StaleArtifactsAge - time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale directory kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh directory of a possibly live session removed: %v", err)
	}
}

func TestRefreshForgetsExitedDescendants(t *testing.T) {
	if testing.Short() {
		t.Skip("runs ps")
	}
	p := &ownedProc{pid: os.Getpid(), seen: map[int]string{
		// No macOS or Linux pid reaches this value: an exited process.
		99999999: "gone --user-data-dir=/tmp/x",
	}}
	p.refresh()
	if _, ok := p.seen[99999999]; ok {
		t.Fatal("an exited process must be forgotten")
	}
}
