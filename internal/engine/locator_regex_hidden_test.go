package engine

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParsePlaywrightLocatorRegexAndOptions(t *testing.T) {
	type want struct {
		role, name, label, text string
		source, flags           string
		exact                   bool
	}
	cases := map[string]want{
		`getByRole("button", { name: /^Mode : / })`:          {role: "button", name: "/^Mode : /", source: "^Mode : "},
		`getByRole('button', { name: /mode/i })`:             {role: "button", name: "/mode/i", source: "mode", flags: "i"},
		`getByRole("button", { name: /a\/b[/]c/ })`:          {role: "button", name: `/a\/b[/]c/`, source: `a\/b[/]c`},
		`getByRole("button", { exact: true, name: "Save" })`: {role: "button", name: "Save", exact: true},
		`getByRole("button", { "name": "Save", })`:           {role: "button", name: "Save"},
		`getByRole("button", { name: "a } b ) c" })`:         {role: "button", name: "a } b ) c"},
		`getByRole("button", { name: "Léa \"x\"" })`:         {role: "button", name: `Léa "x"`},
		`page.getByText(/^Save$/)`:                           {text: "/^Save$/", source: "^Save$"},
		`getByText("Save", { exact: true })`:                 {text: "Save", exact: true},
		`getByLabel(/e-?mail/i)`:                             {label: "/e-?mail/i", source: "e-?mail", flags: "i"},
		"getByRole(`tab`)":                                   {role: "tab"},
		`  getByRole("button", { name: /^Mode : / })  `:      {role: "button", name: "/^Mode : /", source: "^Mode : "},
		`getByRole("button", { name: /ét\u{e9}/ })`:          {role: "button", name: `/ét\u{e9}/`, source: `ét\u{e9}`},
		`getByRole("button",{name:/^Mode : /})`:              {role: "button", name: "/^Mode : /", source: "^Mode : "},
		`getByRole("button", { name: /x/ , exact: false })`:  {role: "button", name: "/x/", source: "x"},
		`getByText(/Row \d+/)`:                               {text: `/Row \d+/`, source: `Row \d+`},
	}
	for in, w := range cases {
		loc, ok, err := parsePlaywrightLocator(in)
		if err != nil || !ok {
			t.Errorf("%s: ok=%t err=%v", in, ok, err)
			continue
		}
		if loc.Role != w.role || loc.Name != w.name || loc.Label != w.label || loc.Text != w.text || loc.Exact != w.exact {
			t.Errorf("%s: got %+v, want %+v", in, loc, w)
		}
		if (loc.Pattern != nil) != (w.source != "") {
			t.Errorf("%s: pattern %v, want source %q", in, loc.Pattern, w.source)
			continue
		}
		if loc.Pattern != nil && (loc.Pattern.Source != w.source || loc.Pattern.Flags != w.flags) {
			t.Errorf("%s: pattern %s, want /%s/%s", in, loc.Pattern, w.source, w.flags)
		}
	}

	bad := map[string]string{
		`getByTestId("save")`:                          "not supported",
		`getByRole("button", { name: /(?<=a)b/ })`:     "RE2",
		`getByRole("button", { level: 2 })`:            `"level" is not supported`,
		`getByRole("button", { name: /unterminated })`: "unterminated regex",
		`getByRole("button", { name: "x" }).first()`:   "chained calls",
		`getByRole(/button/)`:                          "role",
		`getByText("x", { name: "y" })`:                `"name" is not supported`,
		`getByRole("button", { name: /x/q })`:          "unknown flag",
	}
	for in, msg := range bad {
		_, ok, err := parsePlaywrightLocator(in)
		if !ok || err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: ok=%t err=%v, want an error mentioning %q", in, ok, err, msg)
		}
	}
	if _, ok, err := parsePlaywrightLocator(`#save`); ok || err != nil {
		t.Errorf("CSS parsed as a locator: ok=%t err=%v", ok, err)
	}
}

func TestTextPatternMatch(t *testing.T) {
	p, err := newTextPattern(`^Mode : `, "")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Match("Mode :   Auto") || p.Match("Le Mode : Auto") || p.Match("mode : auto") {
		t.Fatal("anchored, case-sensitive, whitespace-normalised match expected")
	}
	p, err = newTextPattern(`café`, "i")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Match("CAFÉ") && !p.Match("Café") {
		t.Fatal("\\u escape and i flag expected")
	}
}

// TestGetByRoleRegexName is the reported case: a regex name timed out
// although the button existed.
func TestGetByRoleRegexName(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	_, page := newIsolatedPage(t)
	html := `<!doctype html><body>
		<button id="mode">Mode : Auto</button>
		<button>Le Mode : Manuel</button>
		<label for="mail">E-mail</label><input id="mail">
		<p>Row 12</p>
	</body>`
	if _, err := Navigate(page, dataURL(html), "load"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	el, err := WaitForTarget(page, `getByRole("button", { name: /^Mode : / })`, nil, StateVisible, 3*time.Second)
	if err != nil {
		t.Fatalf("regex name: %v", err)
	}
	if id, _ := el.Attribute("id"); id == nil || *id != "mode" {
		t.Fatalf("resolved the wrong button: %v", id)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("regex resolution took %s", d)
	}
	if _, err := ResolveTarget(page, `getByRole("button", { name: /mode : /i })`, nil); err == nil || !strings.Contains(err.Error(), "matched 2") {
		t.Fatalf("case-insensitive regex must match both buttons: %v", err)
	}
	if _, err := ResolveTarget(page, `getByRole("button", { name: "Mode : Auto", exact: true })`, nil); err != nil {
		t.Fatalf("exact name: %v", err)
	}
	if _, err := ResolveTarget(page, `getByRole("button", { name: "mode : auto", exact: true })`, nil); err == nil {
		t.Fatal("exact is case-sensitive")
	}
	if _, err := ResolveTarget(page, `getByLabel(/^e-?mail$/i)`, nil); err != nil {
		t.Fatalf("label regex: %v", err)
	}
	el, err = ResolveTarget(page, `getByText(/^Row \d+$/)`, nil)
	if err != nil {
		t.Fatalf("text regex: %v", err)
	}
	if tag, _ := el.Eval(`() => this.tagName`); tag.Value.Str() != "P" {
		t.Fatalf("text regex resolved %s, want the innermost <p>", tag.Value.Str())
	}
	// A malformed locator fails at once instead of waiting as a CSS selector.
	start = time.Now()
	if _, err := WaitForTarget(page, `getByRole("button", { name: /^Mode })`, nil, StateVisible, 3*time.Second); err == nil {
		t.Fatal("malformed locator must fail")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("malformed locator waited %s", d)
	}
}

// TestWaitHiddenWithSeveralMatches is the reported case: three visible
// matches must not satisfy state hidden.
func TestWaitHiddenWithSeveralMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	_, page := newIsolatedPage(t)
	html := `<!doctype html><body>
		<div class="item">Row one</div>
		<div class="item" style="display:none">Row two</div>
		<div class="item">Row three</div>
		<div class="item" style="position:absolute; top:5000px">Row far</div>
		<div class="gone">Gone</div>
	</body>`
	if _, err := Navigate(page, dataURL(html), "load"); err != nil {
		t.Fatal(err)
	}

	_, err := WaitForTarget(page, ".item", nil, StateHidden, 400*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "3 matching element(s) still visible") {
		t.Fatalf("css: want 3 still visible (the off-screen one counts), got %v", err)
	}
	_, err = WaitForTarget(page, `getByText("Row")`, nil, StateHidden, 400*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still visible") {
		t.Fatalf("locator: want still visible, got %v", err)
	}
	if _, err := WaitForTarget(page, ".nothing-matches", nil, StateHidden, 400*time.Millisecond); err != nil {
		t.Fatalf("no match is hidden: %v", err)
	}
	start := time.Now()
	if _, err := WaitForTarget(page, "div[", nil, StateHidden, 3*time.Second); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid selector must fail, got %v", err)
	} else if d := time.Since(start); d > time.Second {
		t.Fatalf("invalid selector waited %s", d)
	}

	// All matches become hidden: one by one, the wait ends after the last.
	if _, err := page.Eval(`() => {
		const items = [...document.querySelectorAll('.item')];
		items.forEach((el, i) => setTimeout(() => { el.style.visibility = 'hidden'; }, 150 * (i + 1)));
	}`); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if _, err := WaitForTarget(page, ".item", nil, StateHidden, 3*time.Second); err != nil {
		t.Fatalf("css hidden: %v", err)
	}
	if d := time.Since(start); d < 450*time.Millisecond {
		t.Fatalf("hidden reported after %s, before the last match was hidden", d)
	}
	if _, err := WaitForTarget(page, `getByText("Row")`, nil, StateHidden, time.Second); err != nil {
		t.Fatalf("locator hidden: %v", err)
	}
}

// TestWaitHiddenRefRemoved: a ref whose element is removed is hidden.
func TestWaitHiddenRefRemoved(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	b, page := newIsolatedPage(t)
	if _, err := Navigate(page, dataURL(`<!doctype html><button id="b">Remove me</button>`), "load"); err != nil {
		t.Fatal(err)
	}
	res, err := Extract(page, LevelSkeleton, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SaveSnapshot(page, res); err != nil {
		t.Fatal(err)
	}
	snap := b.Snapshot(page)
	ref := ""
	for r, info := range snap.Refs {
		if info.Name == "Remove me" {
			ref = r
		}
	}
	if ref == "" {
		t.Fatalf("no ref for the button in %+v", snap.Refs)
	}
	if _, err := WaitForTarget(page, ref, snap, StateHidden, 300*time.Millisecond); err == nil {
		t.Fatal("a visible ref is not hidden")
	}
	if _, err := page.Eval(`() => setTimeout(() => document.getElementById('b').remove(), 100)`); err != nil {
		t.Fatal(err)
	}
	if el, err := WaitForTarget(page, ref, snap, StateHidden, 3*time.Second); err != nil || el != nil {
		t.Fatalf("removed ref: el=%v err=%v", el, err)
	}
}

func TestEvalTimeoutSaysWhy(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	_, page := newIsolatedPage(t)
	if _, err := Navigate(page, dataURL(`<!doctype html><p>eval</p>`), "load"); err != nil {
		t.Fatal(err)
	}
	_, err := EvalJSTimeout(page, `new Promise(() => {})`, "", nil, 300*time.Millisecond)
	var te *EvalTimeoutError
	if !errors.As(err, &te) || !te.Responsive || !strings.Contains(err.Error(), "timeout_ms=300") || !strings.Contains(err.Error(), "still answers") {
		t.Fatalf("pending promise: %v", err)
	}
	// A slow promise within a raised timeout succeeds.
	v, err := EvalJSTimeout(page, `new Promise(r => setTimeout(() => r('slow'), 600))`, "", nil, 3*time.Second)
	if err != nil || v != "slow" {
		t.Fatalf("slow promise: %q %v", v, err)
	}
	// A busy main thread is reported as such.
	_, err = EvalJSTimeout(page, `(() => { const end = Date.now() + 2500; while (Date.now() < end) {} return 1 })()`, "", nil, 300*time.Millisecond)
	if !errors.As(err, &te) || te.Responsive || !strings.Contains(err.Error(), "main thread is busy") {
		t.Fatalf("busy renderer: %v", err)
	}
	// Once the long task ends the window answers again.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v, err := EvalJSTimeout(page, `1 + 1`, "", nil, time.Second); err == nil && v == "2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("window did not recover")
		}
	}
}

func TestHoverRevealPerRowButton(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	_, page := newIsolatedPage(t)
	html := `<!doctype html><style>
		.row { display:flex; gap:8px; padding:8px; width:300px; }
		.row .del { visibility:hidden; }
		.row:hover .del { visibility:visible; }
		.row .fade { opacity:0; }
		.row:hover .fade { opacity:1; }
		.row .menu { display:none; }
		.row:hover .menu { display:inline-block; }
	</style><body>
		<div class="row" id="r1"><span>First</span><button class="del" onclick="window.clicked='del'">Delete</button></div>
		<div class="row" id="r2"><span>Second</span><button class="fade" onclick="window.clicked='fade'">Archive</button></div>
		<div class="row" id="r3"><span>Third</span><button class="menu" onclick="window.clicked='menu'">Menu</button></div>
	</body>`
	if _, err := Navigate(page, dataURL(html), "load"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ sel, hiddenBy, clicked string }{
		{"#r1 .del", "visibility", "del"},
		{"#r2 .fade", "opacity", "fade"},
		{"#r3 .menu", "display", "menu"},
	} {
		el, err := ResolveTarget(page, c.sel, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := EnsureActionable(el, 200*time.Millisecond); err == nil {
			t.Fatalf("%s must start hidden", c.sel)
		}
		r, err := InspectHoverReveal(el)
		if err != nil {
			t.Fatalf("%s: %v", c.sel, err)
		}
		if r.HiddenBy != c.hiddenBy {
			t.Fatalf("%s: hidden by %q, want %q", c.sel, r.HiddenBy, c.hiddenBy)
		}
		if c.hiddenBy == "display" && !strings.Contains(r.Ancestor, "r3") {
			t.Fatalf("%s: hover host %q, want the row", c.sel, r.Ancestor)
		}
		if err := HoverToReveal(page, r); err != nil {
			t.Fatal(err)
		}
		if err := EnsureActionable(el, 2*time.Second); err != nil {
			t.Fatalf("%s after hover: %v", c.sel, err)
		}
		if err := ClickElement(page, el); err != nil {
			t.Fatalf("%s click: %v", c.sel, err)
		}
		got, err := page.Eval(`() => window.clicked`)
		if err != nil || got.Value.Str() != c.clicked {
			t.Fatalf("%s: clicked=%v err=%v", c.sel, got, err)
		}
	}
}
