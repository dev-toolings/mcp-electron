package engine

import (
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// KeepActive makes a page behave as if its window were shown and focused
// while it is hidden, minimised or covered, without raising the window or
// taking OS focus. Chromium throttles a hidden page: requestAnimationFrame
// stops and timers run about once per second, so an eval with a setTimeout
// loop hit its timeout and rod's Focus (which waits for a frame) never
// returned (Escape, 2026-10-24). Focus emulation counts as a capture of the
// page, which keeps it visible to the renderer: visibilityState "visible",
// document.hasFocus() true, frames and timers at full rate. The emulation
// belongs to this CDP session: it ends when the connection closes, so the
// app is left as it was. Page.setWebLifecycleState "active" thaws a page
// Chromium froze; targets that do not support it are left alone.
func KeepActive(page *rod.Page, timeout time.Duration) error {
	if page == nil {
		return fmt.Errorf("keep active: no page")
	}
	p := page.Timeout(timeout)
	defer p.CancelTimeout()
	if err := (proto.EmulationSetFocusEmulationEnabled{Enabled: true}).Call(p); err != nil {
		return fmt.Errorf("focus emulation: %w", err)
	}
	_ = proto.PageSetWebLifecycleState{State: proto.PageSetWebLifecycleStateStateActive}.Call(p)
	return nil
}

// FocusElement scrolls el into view and focuses it in one call. rod's
// Focus first waits for a repaint (requestAnimationFrame), which never comes
// in a hidden window, so a focus on a background app used to spend the whole
// action budget and fail with « no answer from the page ».
func FocusElement(el *rod.Element) error {
	_, err := el.Evaluate(rod.Eval(`function () {
		this.scrollIntoView({block: "nearest", inline: "nearest", behavior: "instant"});
		this.focus({preventScroll: true});
	}`).ByUser())
	return err
}

// selectAllNow selects the whole content of an input, a textarea or an
// editable element, without rod's repaint wait (see FocusElement).
func selectAllNow(el *rod.Element) error {
	_, err := el.Evaluate(rod.Eval(`function () {
		if (typeof this.select === "function") { this.select(); return; }
		const range = document.createRange();
		range.selectNodeContents(this);
		const sel = window.getSelection();
		sel.removeAllRanges();
		sel.addRange(range);
	}`).ByUser())
	return err
}

// documentHidden reports whether el's document is hidden (its window is
// minimised, hidden or covered and KeepActive did not take). An unreadable
// state counts as visible: the caller then acts as it always did.
func documentHidden(el *rod.Element) bool {
	res, err := el.Eval(`() => document.visibilityState === "hidden"`)
	return err == nil && res.Value.Bool()
}

// ScrollIntoViewNow centres el in its scroll containers in one call. rod's
// ScrollIntoView first waits for two animation frames on the page's own
// context, which no tool deadline reaches: in a hidden window an element
// screenshot waited 19 minutes for them (trace, 2026-10-24).
func ScrollIntoViewNow(el *rod.Element) error {
	_, err := el.Eval(`() => this.scrollIntoView({block: "center", inline: "center", behavior: "instant"})`)
	return err
}

// SelectOptions selects the options of a <select> whose text contains each
// value, then fires input and change, like rod's Select without its repaint
// wait (see FocusElement). It fails when no option matches.
func SelectOptions(el *rod.Element, values []string) error {
	if err := FocusElement(el); err != nil {
		return err
	}
	res, err := el.Evaluate(rod.Eval(`function (values) {
		if (!this.options) throw new Error("not a <select> element");
		const opts = Array.from(this.options);
		let found = false;
		for (const v of values) {
			const o = opts.find(o => o.innerText.includes(v) || o.label.includes(v) || o.value === v);
			if (o) { o.selected = true; found = true; }
		}
		this.dispatchEvent(new Event("input", {bubbles: true}));
		this.dispatchEvent(new Event("change", {bubbles: true}));
		return found;
	}`, values).ByUser())
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return fmt.Errorf("no option matches %q", values)
	}
	return nil
}
