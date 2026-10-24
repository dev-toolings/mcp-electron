package electron

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-toolings/mcp-electron/internal/engine"
)

func TestPidPorts(t *testing.T) {
	for _, c := range []struct {
		cmd     string
		cdp     int
		given   bool
		inspect int
	}{
		{"/Applications/Foo.app/Contents/MacOS/Foo", 0, false, 9229},
		{"electron . --remote-debugging-port=9333", 9333, false, 9229},
		{"electron --inspect=127.0.0.1:9400 .", 0, true, 9400},
		{"electron --inspect .", 0, true, 9229},
		{"electron --inspect-port=9500 .", 0, false, 9500},
		{"electron --inspect-brk=9600 .", 0, true, 9600},
	} {
		cdp, given, inspect := pidPorts(c.cmd)
		if cdp != c.cdp || given != c.given || inspect != c.inspect {
			t.Errorf("%q: got %d %v %d, want %d %v %d", c.cmd, cdp, given, inspect, c.cdp, c.given, c.inspect)
		}
	}
	if got := appName("/Users/k/escape/node_modules/.cache/escape-dev/Escape (Alpha).app/Contents/MacOS/Electron ."); got != "Escape (Alpha)" {
		t.Errorf("appName: %q", got)
	}
	if got := helperType("/x/Electron Helper --type=renderer --foo"); got != "renderer" {
		t.Errorf("helperType: %q", got)
	}
}

// TestE2EAttachByPID starts an Electron app with no debugging option at all, attaches to it by pid, drives its
// window through the bridge, and checks that nothing stays open in the app afterwards: the inspector SIGUSR1
// opened is closed again, and the bridge stops after the session.
func TestE2EAttachByPID(t *testing.T) {
	target, env := e2eSpec(t)
	tgt, err := Resolve(target)
	if err != nil {
		t.Fatal(err)
	}
	if probeInspector(context.Background(), loopbackEndpoint(defaultInspectPort)) != "" {
		t.Skipf("a Node inspector already listens on %d", defaultInspectPort)
	}
	fixture, err := filepath.Abs("testdata/pidapp")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := os.MkdirTemp(tempRoot(), "mcp-e2e-pid-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(profile) })
	p, err := startOwned(tgt.Executable, []string{fixture, "--user-data-dir=" + profile}, mergeEnv(env, 0, 0), fixture, NewRing(200), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.terminate() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// The window needs a moment: the attach itself does not wait for one.
	time.Sleep(3 * time.Second)

	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	attach := func() *Session {
		t.Helper()
		sess, err := m.Attach(ctx, AttachSpec{PID: p.pid})
		if err != nil {
			t.Fatalf("attach by pid: %v", err)
		}
		if sess.Owned || sess.PID != p.pid || !strings.Contains(sess.Note, "bridge") {
			t.Fatalf("unexpected session: %+v", sess.Info)
		}
		return sess
	}
	sess := attach()
	var b *engine.Browser
	waitFor(t, "first window", 30*time.Second, func() bool {
		if b, err = sess.Browser(ctx); err != nil {
			return false
		}
		w, _ := b.Windows()
		return len(w) > 0
	})
	if title := pageTitle(t, sess, 0); title != "pid fixture" {
		t.Fatalf("title through the bridge: %q", title)
	}
	page, err := b.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	el, err := page.Timeout(10 * time.Second).Element("#go")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ClickElement(page, el); err != nil {
		t.Fatalf("click through the bridge: %v", err)
	}
	waitFor(t, "click effect", 10*time.Second, func() bool {
		r, err := page.Eval(`() => document.querySelector('#go').textContent`)
		return err == nil && r.Value.Str() == "clicked"
	})
	waitFor(t, "inspector closed again", 10*time.Second, func() bool {
		return probeInspector(ctx, loopbackEndpoint(defaultInspectPort)) == ""
	})
	endpoint := sess.Endpoint
	if _, err := m.Stop(sess.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bridge stopped after the session", 20*time.Second, func() bool {
		_, ok := probeCDP(ctx, endpoint)
		return !ok
	})
	if p.exited() {
		t.Fatal("the app died with the session")
	}
	// A second attach opens the inspector again and starts a fresh bridge.
	again := attach()
	if again.Endpoint == endpoint {
		t.Fatal("the second attach reused a bridge that should have stopped")
	}
	if title := pageTitle(t, again, 0); title != "pid fixture" {
		t.Fatalf("title after re-attach: %q", title)
	}
	if _, err := m.Stop(again.ID); err != nil {
		t.Fatal(err)
	}
}
