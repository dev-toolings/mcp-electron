package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// minimize hides a page the way a minimised or hidden app window is hidden:
// visibilityState "hidden", no animation frames.
func minimize(t *testing.T, page *rod.Page) {
	t.Helper()
	win, err := proto.BrowserGetWindowForTarget{TargetID: page.TargetID}.Call(page)
	if err != nil {
		t.Fatalf("window for target: %v", err)
	}
	if err := (proto.BrowserSetWindowBounds{WindowID: win.WindowID, Bounds: &proto.BrowserBounds{WindowState: proto.BrowserWindowStateMinimized}}).Call(page); err != nil {
		t.Fatalf("minimize: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		res, err := page.Eval(`() => document.visibilityState`)
		if err == nil && res.Value.Str() == "hidden" {
			return
		}
		if time.Now().After(deadline) {
			t.Skipf("this Chrome does not hide a minimised headless window (visibilityState %v, %v)", res, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type pageRate struct {
	Visibility string `json:"vis"`
	Focus      bool   `json:"focus"`
	Frames     int    `json:"raf"`
	Timers     int    `json:"timers"`
}

// rate counts animation frames and 10 ms timer turns over 600 ms.
func rate(t *testing.T, page *rod.Page) pageRate {
	t.Helper()
	res, err := page.Timeout(5 * time.Second).Eval(`() => new Promise(done => {
		const t0 = performance.now(); let raf = 0, timers = 0;
		requestAnimationFrame(function f() { raf++; requestAnimationFrame(f); });
		(function loop() { timers++; if (performance.now() - t0 < 600) setTimeout(loop, 10); else done({vis: document.visibilityState, focus: document.hasFocus(), raf, timers}); })();
	})`)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	var r pageRate
	if err := res.Value.Unmarshal(&r); err != nil {
		t.Fatalf("measure: %v", err)
	}
	return r
}

// TestKeepActiveRunsAHiddenPage: a minimised window gets no animation frame
// (so anything waiting for a repaint hangs) until KeepActive; then it reads
// as visible and focused and paints again, and the window was never raised.
func TestKeepActiveRunsAHiddenPage(t *testing.T) {
	_, page := newIsolatedPage(t)
	minimize(t, page)
	if r := rate(t, page); r.Frames != 0 || r.Visibility != "hidden" {
		t.Fatalf("minimised page not hidden: %+v", r)
	}
	if err := KeepActive(page, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	r := rate(t, page)
	if r.Visibility != "visible" || !r.Focus || r.Frames < 10 || r.Timers < 20 {
		t.Fatalf("kept-active hidden page still throttled: %+v", r)
	}
	win, err := proto.BrowserGetWindowForTarget{TargetID: page.TargetID}.Call(page)
	if err != nil {
		t.Fatal(err)
	}
	if win.Bounds.WindowState != proto.BrowserWindowStateMinimized {
		t.Fatalf("KeepActive changed the window state to %s", win.Bounds.WindowState)
	}
}

// TestTypeElementInAHiddenPage: typing focuses the field without waiting for
// a repaint, so it works in a hidden window even without KeepActive.
func TestTypeElementInAHiddenPage(t *testing.T) {
	_, page := newIsolatedPage(t)
	if err := page.Navigate(dataURL(`<input id="name" value="old">`)); err != nil {
		t.Fatal(err)
	}
	_ = page.WaitLoad()
	minimize(t, page)
	el, err := page.Element("#name")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := TypeElement(page.Timeout(4*time.Second), el, "new"); err != nil {
		t.Fatalf("type in a hidden page: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("type took %s in a hidden page", d)
	}
	res, err := page.Eval(`() => document.querySelector("#name").value`)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Value.Str(); got != "new" {
		t.Fatalf("value %q, want %q", got, "new")
	}
}

// TestActionsInAHiddenPage: the actions that used rod's repaint wait (focus,
// scroll into view, select) answer at once in a hidden window, without
// KeepActive. A capture and mouse input need a frame, which Chromium does not
// produce for a hidden window: they fail within their budget instead of
// hanging, and work once the page is kept active.
func TestActionsInAHiddenPage(t *testing.T) {
	_, page := newIsolatedPage(t)
	if err := page.Navigate(dataURL(`<div style="height:3000px"></div>
		<select id="pick"><option>Alpha</option><option>Beta</option></select>
		<button id="b" style="width:80px;height:30px">Go</button>`)); err != nil {
		t.Fatal(err)
	}
	_ = page.WaitLoad()
	minimize(t, page)
	bounded := page.Timeout(4 * time.Second)
	within := func(name string, d time.Duration, fn func() error) error {
		t.Helper()
		start := time.Now()
		err := fn()
		if took := time.Since(start); took > d {
			t.Fatalf("%s took %s in a hidden page (err %v)", name, took, err)
		}
		return err
	}
	sel, err := bounded.Element("#pick")
	if err != nil {
		t.Fatal(err)
	}
	if err := within("select", 2*time.Second, func() error { return SelectOptions(sel, []string{"Beta"}) }); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got, _ := page.Eval(`() => document.querySelector("#pick").value`); got.Value.Str() != "Beta" {
		t.Fatalf("selected %v", got.Value)
	}
	if err := SelectOptions(sel, []string{"Gamma"}); err == nil {
		t.Fatal("selecting a missing option succeeded")
	}
	btn, err := bounded.Element("#b")
	if err != nil {
		t.Fatal(err)
	}
	if err := within("scroll", 2*time.Second, func() error { return ScrollIntoViewNow(btn) }); err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if err := within("focus", 2*time.Second, func() error { return FocusElement(btn) }); err != nil {
		t.Fatalf("focus: %v", err)
	}
	// Mouse input needs a frame: a bounded failure without KeepActive,
	// delivered with it.
	_ = within("hover", 5*time.Second, func() error { return HoverElement(page.Timeout(2*time.Second), btn) })
	if err := KeepActive(page, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	active := page.Timeout(4 * time.Second)
	if btn, err = active.Element("#b"); err != nil {
		t.Fatal(err)
	}
	if err := within("hover (kept active)", 3*time.Second, func() error { return HoverElement(active, btn) }); err != nil {
		t.Fatalf("hover in a kept-active hidden page: %v", err)
	}
}

// TestElementScreenshotOfAHiddenPageIsBounded: a clipped capture of a
// minimised window gets no frame; it fails within the call's deadline and
// says why, instead of waiting for the window to be shown.
func TestElementScreenshotOfAHiddenPageIsBounded(t *testing.T) {
	_, page := newIsolatedPage(t)
	if err := page.Navigate(dataURL(`<button id="b" style="width:80px;height:30px">Go</button>`)); err != nil {
		t.Fatal(err)
	}
	_ = page.WaitLoad()
	minimize(t, page)
	bounded := page.Timeout(3 * time.Second)
	btn, err := bounded.Element("#b")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = elementScreenshotFmt(btn, "png", 0)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("element screenshot took %s", took)
	}
	if err == nil {
		t.Skip("this Chrome paints minimised windows: nothing to bound")
	}
	if !strings.Contains(err.Error(), "hidden or minimised") {
		t.Fatalf("unexplained capture failure: %v", err)
	}
}

func TestChainedAndInvalidTargetsFailAtOnce(t *testing.T) {
	_, page := newIsolatedPage(t)
	if err := page.Navigate(dataURL(`<div class="panel"><button title=">> next">Go</button></div>`)); err != nil {
		t.Fatal(err)
	}
	_ = page.WaitLoad()
	for _, target := range []string{
		`.panel >> role=button name=Go`,
		`(main button[aria-label="x"]) >> nth=0`,
		`getByRole("dialog") >> getByRole("button", { name: "Close" })`,
		`.panel button[`,
	} {
		start := time.Now()
		_, err := WaitForTarget(page, target, nil, StateVisible, 5*time.Second)
		if err == nil || time.Since(start) > time.Second {
			t.Errorf("%q: err %v after %s, want an immediate error", target, err, time.Since(start))
		}
	}
	// A >> inside an attribute value is not chaining.
	if _, err := WaitForTarget(page, `button[title=">> next"]`, nil, StateVisible, 2*time.Second); err != nil {
		t.Fatalf("quoted >>: %v", err)
	}
}

func TestWaitForTextGoneAndCancellation(t *testing.T) {
	_, page := newIsolatedPage(t)
	if err := page.Navigate(dataURL(`<p id="m">Loading your space</p><script>setTimeout(() => document.getElementById("m").remove(), 300)</script>`)); err != nil {
		t.Fatal(err)
	}
	if _, err := WaitForAgent(page, nil, nil, WaitSpec{Text: "Loading your space", State: "hidden"}, 3*time.Second); err != nil {
		t.Fatalf("text hidden: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	err := WaitForText(page.Context(ctx), "never shown", 10*time.Second)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("a cancelled wait for text ran %s (err %v)", time.Since(start), err)
	}
}
