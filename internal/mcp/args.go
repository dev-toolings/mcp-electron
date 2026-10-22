package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dev-toolings/mcp-electron/internal/electron"
	"github.com/dev-toolings/mcp-electron/internal/engine"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// session resolves the session argument (the only session when omitted).
func (s *Server) session(req mcpgo.CallToolRequest) (*electron.Session, error) {
	return s.mgr.Get(strings.TrimSpace(mcpgo.ParseString(req, "session", "")))
}

// pageFn is the body of a tool that works on one window of one session.
type pageFn func(sess *electron.Session, b *engine.Browser, page *rod.Page) (*mcpgo.CallToolResult, error)

// withPage resolves the session and window, serialises UI actions on the
// session, and runs fn. Errors become tool errors, never protocol errors, so
// the agent reads them.
func (s *Server) withPage(ctx context.Context, req mcpgo.CallToolRequest, fn pageFn) (*mcpgo.CallToolResult, error) {
	sess, err := s.session(req)
	if err != nil {
		return errResult(err)
	}
	window := mcpgo.ParseInt(req, "window", 0)
	sess.Actions().Lock()
	defer sess.Actions().Unlock()
	b, page, err := sess.Page(ctx, window)
	if err != nil {
		return errResult(err)
	}
	return fn(sess, b, page)
}

// normalizeTarget accepts "@3", "3", "e3", "ref:3" (refs), a CSS selector,
// or a Playwright locator (getByRole/getByText/getByLabel). The shorthands
// text=…, label=… and role=NAME[ name=…] are expanded to locators.
func normalizeTarget(in string) string {
	t := strings.TrimSpace(in)
	if t == "" {
		return ""
	}
	t = strings.TrimPrefix(t, "ref:")
	if isDigits(t) {
		return "@" + t
	}
	if strings.HasPrefix(t, "@") || strings.HasPrefix(t, "getBy") {
		return t
	}
	if r := engine.InternalRef(t); r != t {
		return r
	}
	if v, ok := strings.CutPrefix(t, "text="); ok {
		return fmt.Sprintf("getByText(%q)", strings.Trim(v, `"'`))
	}
	if v, ok := strings.CutPrefix(t, "label="); ok {
		return fmt.Sprintf("getByLabel(%q)", strings.Trim(v, `"'`))
	}
	if v, ok := strings.CutPrefix(t, "role="); ok {
		role, name, hasName := strings.Cut(v, " name=")
		role = strings.TrimSpace(role)
		if !hasName {
			return fmt.Sprintf("getByRole(%q)", role)
		}
		name = strings.TrimSpace(name)
		if regexLiteral.MatchString(name) {
			return fmt.Sprintf("getByRole(%q, { name: %s })", role, name)
		}
		return fmt.Sprintf("getByRole(%q, { name: %q })", role, strings.Trim(name, `"'`))
	}
	return t
}

// regexLiteral recognises a JavaScript regex literal (/source/flags) given
// as the name of the role=… shorthand.
var regexLiteral = regexp.MustCompile(`^/.+/[a-z]*$`)

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isRef(target string) bool { return strings.HasPrefix(target, "@") }

// lookupTimeout bounds how long an action waits for its target to appear.
func lookupTimeout(req mcpgo.CallToolRequest) time.Duration {
	ms := mcpgo.ParseInt(req, "timeout_ms", 10000)
	if ms <= 0 {
		ms = 10000
	}
	if ms > 120000 {
		ms = 120000
	}
	return time.Duration(ms) * time.Millisecond
}

// evalTimeout bounds eval and eval_main: 8 s by default, 2 minutes at most.
func evalTimeout(req mcpgo.CallToolRequest) time.Duration {
	ms := mcpgo.ParseInt(req, "timeout_ms", 8000)
	if ms <= 0 {
		ms = 8000
	}
	if ms > 120000 {
		ms = 120000
	}
	return time.Duration(ms) * time.Millisecond
}

// resolve waits for the target to be visible and returns the element.
func resolve(page *rod.Page, b *engine.Browser, target string, timeout time.Duration) (*rod.Element, error) {
	if target == "" {
		return nil, fmt.Errorf("target is required (@ref from snapshot, a CSS selector, or getByRole/getByText/getByLabel)")
	}
	el, err := engine.WaitForTarget(page, target, b.Snapshot(page), engine.StateVisible, timeout)
	if err != nil {
		return nil, err
	}
	if el == nil {
		return nil, fmt.Errorf("%s: not found", target)
	}
	if err := engine.EnsureActionable(el, timeout); err != nil {
		return nil, fmt.Errorf("%s: %w", target, err)
	}
	return el, nil
}

// hoverMode is the click tool's hover_first: auto (absent) hovers only a
// target hidden by visibility or opacity, always also reveals a
// display:none target through its nearest rendered ancestor, never keeps
// the strict visible-and-actionable wait.
type hoverMode int

const (
	hoverAuto hoverMode = iota
	hoverAlways
	hoverNever
)

func parseHoverMode(req mcpgo.CallToolRequest) hoverMode {
	v, ok := req.GetArguments()["hover_first"]
	if !ok || v == nil {
		return hoverAuto
	}
	if b, ok := v.(bool); ok {
		if b {
			return hoverAlways
		}
		return hoverNever
	}
	return hoverAuto
}

// resolveClickable resolves a click target. A target that only shows on
// hover (per-row buttons styled with :hover) is revealed with a real mouse
// move over it (or over its row) before the usual actionability checks, so
// the click that follows is a real mouse event on a visible element.
func resolveClickable(page *rod.Page, b *engine.Browser, target string, timeout time.Duration, mode hoverMode) (el *rod.Element, note string, err error) {
	if mode == hoverNever {
		el, err := resolve(page, b, target, timeout)
		return el, "", err
	}
	if target == "" {
		return nil, "", fmt.Errorf("target is required (@ref from snapshot, a CSS selector, or getByRole/getByText/getByLabel)")
	}
	deadline := time.Now().Add(timeout)
	el, err = engine.WaitForTarget(page, target, b.Snapshot(page), engine.StateAttached, timeout)
	if err != nil {
		return nil, "", err
	}
	if el == nil {
		return nil, "", fmt.Errorf("%s: not found", target)
	}
	reveal, err := engine.InspectHoverReveal(el)
	if err != nil {
		if mode == hoverAlways {
			return nil, "", fmt.Errorf("%s: %w", target, err)
		}
		// Auto mode only reveals boxed elements; anything else takes the
		// ordinary path, which waits for the target to show up.
		reveal = nil
	}
	hover := reveal != nil && (mode == hoverAlways || reveal.HiddenBy == "visibility" || reveal.HiddenBy == "opacity")
	if !hover {
		el, err := resolve(page, b, target, max(time.Until(deadline), time.Second))
		return el, "", err
	}
	if err := engine.HoverToReveal(page, reveal); err != nil {
		return nil, "", fmt.Errorf("%s: %w", target, err)
	}
	if reveal.HiddenBy != "" {
		note = fmt.Sprintf(" (revealed by hovering, was hidden by %s)", reveal.HiddenBy)
		if reveal.Ancestor != "" {
			note = fmt.Sprintf(" (revealed by hovering %s, was hidden by %s)", reveal.Ancestor, reveal.HiddenBy)
		}
	}
	if err := engine.EnsureActionable(el, max(time.Until(deadline), time.Second)); err != nil {
		if reveal.HiddenBy != "" {
			return nil, "", fmt.Errorf("%s: %w (it was hidden by %s and hovering it did not reveal it)", target, err, reveal.HiddenBy)
		}
		return nil, "", fmt.Errorf("%s: %w", target, err)
	}
	return el, note, nil
}

func parseButton(s string) (proto.InputMouseButton, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "left":
		return proto.InputMouseButtonLeft, nil
	case "right":
		return proto.InputMouseButtonRight, nil
	case "middle":
		return proto.InputMouseButtonMiddle, nil
	}
	return "", fmt.Errorf("button must be left, right or middle")
}

func snapshotMode(req mcpgo.CallToolRequest) (engine.SnapshotMode, error) {
	return engine.ParseSnapshotMode(mcpgo.ParseString(req, "snapshot", "diff"))
}

// mutate runs an action and reports what changed on the page according to
// the snapshot mode: nothing, a diff of the interactive elements, or the
// whole skeleton.
func mutate(b *engine.Browser, page *rod.Page, mode engine.SnapshotMode, summary string, act func() error) (*mcpgo.CallToolResult, error) {
	return mutateWith(b, page, mode, func() string { return summary }, act)
}

// mutateWith is mutate with a summary computed after the action ran.
func mutateWith(b *engine.Browser, page *rod.Page, mode engine.SnapshotMode, summaryFn func() string, act func() error) (*mcpgo.CallToolResult, error) {
	prev := b.Snapshot(page)
	if err := act(); err != nil {
		return errResult(err)
	}
	summary := summaryFn()
	if mode == engine.SnapshotModeNone {
		return text(summary)
	}
	diff, result, err := engine.CaptureMutation(b, page, prev)
	if err != nil {
		return textf("%s\n(page changed but could not be re-read: %v)", summary, err)
	}
	if mode == engine.SnapshotModeFull {
		return textf("%s\n%s", summary, renderTree(result))
	}
	return textf("%s\n%s", summary, engine.FormatDiff(diff))
}
