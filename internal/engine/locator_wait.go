package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// ElementState describes the desired element state for WaitForLocator.
type ElementState string

const (
	// StateAttached waits until the element is present in the DOM.
	StateAttached ElementState = "attached"
	// StateVisible waits until the element is visible (not display:none, not hidden).
	StateVisible ElementState = "visible"
	// StateHidden waits until no element matching the target is visible:
	// every match is hidden (display:none, visibility:hidden, empty box) or
	// detached, or nothing matches. With several matches, all of them must
	// be hidden; an element merely scrolled out of view still counts as
	// visible.
	StateHidden ElementState = "hidden"
	// StateEnabled waits until the element is not disabled.
	StateEnabled ElementState = "enabled"
	// StateStable waits until the element's bounding box stops moving for 100ms.
	StateStable ElementState = "stable"
)

// ParseElementState validates and normalises the string form of a state.
// Empty string defaults to StateAttached.
func ParseElementState(s string) (ElementState, error) {
	return parseElementState(s)
}

func parseElementState(s string) (ElementState, error) {
	switch ElementState(s) {
	case StateAttached, StateVisible, StateHidden, StateEnabled, StateStable:
		return ElementState(s), nil
	case "none", "":
		return StateAttached, nil
	default:
		return "", fmt.Errorf("unknown element state %q: use attached|visible|hidden|enabled|stable|none", s)
	}
}

// WaitForLocator resolves a Locator with retry-until-found-or-stable logic.
//
// It polls every 100 ms with exponential backoff up to 500 ms per interval.
// Deadline is set by timeout (0 = no wait, resolve once and return).
//
// Returns the resolved element or a timeout/context error.
func WaitForLocator(page *rod.Page, loc Locator, state ElementState, timeout time.Duration) (*rod.Element, error) {
	if state == StateHidden {
		return nil, waitNoneVisible(page, locatorDescription(loc), timeout, func() (int, error) {
			matches, err := visibleLocatorMatches(page, loc)
			return len(matches), err
		})
	}
	if timeout <= 0 {
		// No wait: one-shot resolve.
		el, err := ResolveByLocator(page, loc)
		if err != nil {
			if state == StateHidden {
				// Not found → effectively hidden.
				return nil, nil
			}
			return nil, err
		}
		return el, checkState(el, state)
	}

	ctx, cancel := context.WithTimeout(page.GetContext(), timeout)
	defer cancel()

	interval := 100 * time.Millisecond
	const maxInterval = 500 * time.Millisecond

	for {
		el, resolveErr := ResolveByLocator(page, loc)
		if resolveErr == nil {
			if stateErr := checkState(el, state); stateErr == nil {
				return el, nil
			}
		} else if isAmbiguousLocatorError(resolveErr) {
			return nil, resolveErr
		} else if state == StateHidden {
			// Element not found in the a11y tree at all → it is effectively hidden.
			return nil, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for locator (state=%s) timed out after %s", state, timeout)
		case <-time.After(interval):
		}

		interval *= 2
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}

func isAmbiguousLocatorError(err error) bool {
	var countErr *LocatorMatchCountError
	return errors.As(err, &countErr) && countErr.Count > 1
}

// WaitForTarget waits for a ref, strict CSS selector, or supported Playwright
// locator string. Refs retain their persisted snapshot mapping; CSS selectors
// and locator strings are resolved afresh while polling.
func WaitForTarget(page *rod.Page, target string, snapshot *PageSnapshot, state ElementState, timeout time.Duration) (*rod.Element, error) {
	if strings.HasPrefix(InternalRef(strings.TrimSpace(target)), "@") {
		return WaitForRef(page, target, snapshot, state, timeout)
	}
	if locator, isLocator, err := parsePlaywrightLocator(target); isLocator {
		if err != nil {
			return nil, err
		}
		return WaitForLocator(page, locator, state, timeout)
	}
	if state == StateHidden {
		return nil, waitNoneVisible(page, fmt.Sprintf("CSS selector %q", target), timeout, func() (int, error) {
			return visibleCSSMatches(page, target)
		})
	}
	if timeout <= 0 {
		el, err := ResolveTarget(page, target, snapshot)
		if err != nil {
			if state == StateHidden {
				return nil, nil
			}
			return nil, err
		}
		return el, checkState(el, state)
	}

	ctx, cancel := context.WithTimeout(page.GetContext(), timeout)
	defer cancel()
	interval := 100 * time.Millisecond
	const maxInterval = 500 * time.Millisecond
	for {
		el, err := ResolveTarget(page, target, snapshot)
		if err == nil {
			if stateErr := checkState(el, state); stateErr == nil {
				return el, nil
			}
		} else if state == StateHidden {
			return nil, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for target %q (state=%s) timed out after %s", target, state, timeout)
		case <-time.After(interval):
		}
		interval *= 2
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}

// WaitForRef waits until the element identified by ref in snapshot reaches the
// desired state.
//
// Refs resolve through ResolveRefSemantic: backendNodeId first, then
// role+name+nth. Ambiguous matches fail closed (ErrAmbiguousRef).
//
// Returns the resolved element once it satisfies state, or an error.
func WaitForRef(page *rod.Page, ref string, snapshot *PageSnapshot, state ElementState, timeout time.Duration) (*rod.Element, error) {
	parsed, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, fmt.Errorf("%w: run preview, extract, or navigate --extract first", ErrStaleRef)
	}
	if _, ok := snapshot.Refs[parsed]; !ok {
		return nil, fmt.Errorf("%w: ref %s not found in last snapshot", ErrStaleRef, parsed)
	}

	resolve := func() (*rod.Element, error) {
		return ResolveRefSemantic(page, parsed, snapshot)
	}

	// For hidden, a ref that no longer resolves (element removed) is hidden.
	check := func() (*rod.Element, bool, error) {
		el, err := resolve()
		if err != nil {
			if state == StateHidden && !errors.Is(err, ErrAmbiguousRef) {
				return nil, true, nil
			}
			return nil, false, err
		}
		if err := checkState(el, state); err != nil {
			return nil, false, err
		}
		if state == StateHidden {
			return nil, true, nil
		}
		return el, true, nil
	}

	if timeout <= 0 {
		el, _, err := check()
		return el, err
	}

	ctx, cancel := context.WithTimeout(page.GetContext(), timeout)
	defer cancel()

	interval := 100 * time.Millisecond
	const maxInterval = 500 * time.Millisecond
	var last error

	for {
		el, ok, err := check()
		if ok {
			return el, nil
		}
		if errors.Is(err, ErrAmbiguousRef) {
			return nil, err
		}
		last = err

		select {
		case <-ctx.Done():
			if last != nil {
				return nil, fmt.Errorf("wait for ref %s (state=%s) timed out after %s: %w", parsed, state, timeout, last)
			}
			return nil, fmt.Errorf("wait for ref %s (state=%s) timed out after %s", parsed, state, timeout)
		case <-time.After(interval):
		}

		interval *= 2
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}

// visibleCSSMatchesJS counts the elements a selector matches and how many of
// them are visible by the rule of rod's Element.Visible (rendered, not
// visibility:hidden, non-empty box), in one round trip.
const visibleCSSMatchesJS = `(sel) => {
	const els = document.querySelectorAll(sel);
	let visible = 0;
	for (const el of els) {
		const style = window.getComputedStyle(el);
		const box = el.getBoundingClientRect();
		if (style.display !== 'none' && style.visibility !== 'hidden' && !!(box.top || box.bottom || box.width || box.height)) visible++;
	}
	return visible;
}`

// visibleCSSMatches returns how many elements matching selector are visible.
func visibleCSSMatches(page *rod.Page, selector string) (int, error) {
	res, err := page.Eval(visibleCSSMatchesJS, selector)
	if err != nil {
		if strings.Contains(err.Error(), "is not a valid selector") {
			return 0, &permanentWaitError{fmt.Errorf("CSS selector %q is invalid: %w", selector, err)}
		}
		return 0, err
	}
	return res.Value.Int(), nil
}

// permanentWaitError stops a wait at once (bad input, not a page state).
type permanentWaitError struct{ err error }

func (e *permanentWaitError) Error() string { return e.err.Error() }
func (e *permanentWaitError) Unwrap() error { return e.err }

// waitNoneVisible polls count until it reports no visible match. Transient
// errors (a navigation destroying the context) are retried; a locator or
// selector that cannot be evaluated fails at once.
func waitNoneVisible(page *rod.Page, what string, timeout time.Duration, count func() (int, error)) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	interval := 100 * time.Millisecond
	const maxInterval = 500 * time.Millisecond
	var last error
	visible := -1
	for {
		n, err := count()
		var perm *permanentWaitError
		switch {
		case errors.As(err, &perm):
			return err
		case err == nil && n == 0:
			return nil
		case err == nil:
			visible, last = n, nil
		default:
			last = err
		}
		if timeout <= 0 || !time.Now().Before(deadline) {
			if last != nil {
				return fmt.Errorf("wait for %s (state=hidden) timed out after %s: %w", what, timeout, last)
			}
			return fmt.Errorf("wait for %s (state=hidden) timed out after %s: %d matching element(s) still visible", what, timeout, visible)
		}
		select {
		case <-page.GetContext().Done():
			return page.GetContext().Err()
		case <-time.After(min(interval, time.Until(deadline))):
		}
		interval *= 2
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}

// checkState verifies that el satisfies state.
// Returns nil if the condition holds, or a descriptive error.
func checkState(el *rod.Element, state ElementState) error {
	switch state {
	case StateAttached:
		// Already attached by the time we have a *rod.Element.
		return nil

	case StateVisible:
		visible, err := el.Visible()
		if err != nil {
			return fmt.Errorf("visible check: %w", err)
		}
		if !visible {
			return fmt.Errorf("element is not visible")
		}
		return nil

	case StateHidden:
		visible, err := el.Visible()
		if err != nil {
			// Treat error (element gone) as hidden.
			return nil
		}
		if visible {
			return fmt.Errorf("element is still visible")
		}
		return nil

	case StateEnabled:
		disabled, err := el.Eval(`() => this.disabled`)
		if err != nil {
			return fmt.Errorf("enabled check: %w", err)
		}
		if disabled != nil && disabled.Value.Val() == true {
			return fmt.Errorf("element is disabled")
		}
		return nil

	case StateStable:
		return checkStable(el)
	}
	return nil
}

// checkStable verifies the element's bounding box has not moved for 100 ms.
func checkStable(el *rod.Element) error {
	box1, err := el.Shape()
	if err != nil {
		return fmt.Errorf("stable check: %w", err)
	}
	time.Sleep(100 * time.Millisecond)
	box2, err := el.Shape()
	if err != nil {
		return fmt.Errorf("stable check second read: %w", err)
	}
	if len(box1.Quads) == 0 || len(box2.Quads) == 0 {
		return nil
	}
	q1, q2 := box1.Quads[0], box2.Quads[0]
	if len(q1) >= 2 && len(q2) >= 2 {
		dx := q1[0] - q2[0]
		dy := q1[1] - q2[1]
		const tol = 1.0
		if dx > tol || dx < -tol || dy > tol || dy < -tol {
			return fmt.Errorf("element is moving (dx=%.1f dy=%.1f)", dx, dy)
		}
	}
	return nil
}
