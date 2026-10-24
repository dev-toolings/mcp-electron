package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// xpathLiteral safely escapes a string for use as an XPath string literal.
// It handles strings containing both single and double quotes by using the
// concat() function to build a safe literal that cannot be injected.
func xpathLiteral(s string) string {
	// If no quotes, simple case
	if !strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		return "'" + s + "'"
	}
	// If only double quotes, wrap in single quotes
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	// If only single quotes, wrap in double quotes
	if !strings.Contains(s, "\"") {
		return "\"" + s + "\""
	}
	// Mixed quotes: use concat() to build a literal safely
	parts := strings.Split(s, "'")
	var b strings.Builder
	b.WriteString("concat(")
	for i, p := range parts {
		if i > 0 {
			b.WriteString(", \"'\", ")
		}
		b.WriteString("'")
		b.WriteString(p)
		b.WriteString("'")
	}
	b.WriteString(")")
	return b.String()
}

// Locator describes a semantic element match. At least one field must be set.
// When multiple fields are set, the match is conjunctive (all must hold).
type Locator struct {
	// Role matches the ARIA role (see engine.interactiveRoles, engine.skeletonRoles).
	// Accepts canonical role strings or their one-letter agent abbreviation
	// ("b"=button, "a"=link, "t"=textbox, etc.).
	Role string

	// Name matches the accessible name. Comparison is case-insensitive and uses
	// substring matching, so "Sign in" matches "Sign in now".
	Name string

	// Label matches the accessible label derived from <label for=...> or
	// aria-labelledby. In Chromium's a11y tree, that's exposed as the name of
	// a textbox / combobox — so this is equivalent to Name for inputs.
	// Included as a separate field for ergonomic CLI flags (--by-label).
	Label string

	// Text matches via page-wide text search — like Playwright's getByText.
	// When set, Role is ignored.
	Text string

	// Pattern, when set, replaces the substring match of Name, Label or Text
	// (whichever this locator matches on) with a regex; that field then
	// holds the literal for messages.
	Pattern *TextPattern

	// Exact switches the string match to a case-sensitive whole-string
	// comparison (whitespace-normalised), like Playwright's exact: true.
	Exact bool
}

// IsEmpty reports whether the locator has no criteria set.
func (l Locator) IsEmpty() bool {
	return l.Role == "" && l.Name == "" && l.Label == "" && l.Text == ""
}

// ResolveByLocator returns the single visible element matching the locator. Matching
// strategy:
//   - If Text is set: XPath text contains (case-insensitive). Matches <button>,
//     <a>, <label>, generic text containers.
//   - Else: extract the a11y tree at skeleton level and filter by
//     (role, name|label).
//
// Zero or multiple visible matches are errors. This strictness is shared by
// action --by-* flags and generated Playwright locator strings so an ambiguous
// agent instruction can never silently act on the first element in tree order.
func ResolveByLocator(page *rod.Page, loc Locator) (*rod.Element, error) {
	if loc.IsEmpty() {
		return nil, fmt.Errorf("locator: at least one of --by-role, --by-text, --by-name, --by-label required")
	}
	return resolveStrictByLocator(page, loc)
}

// ResolveTarget resolves one interaction target. Targets may be snapshot refs
// (@N or playwright-cli eN), a strict CSS selector, or the small Playwright
// locator subset emitted by generate-locator: getByRole, getByText, and
// getByLabel (with an optional { name: "..." } for getByRole).
//
// CSS selectors must match exactly one element. This deliberately avoids the
// accidental-first-match behavior that is especially unsafe for agent input.
func ResolveTarget(page *rod.Page, target string, snapshot *PageSnapshot) (*rod.Element, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("target is required")
	}
	if strings.HasPrefix(InternalRef(target), "@") {
		return ResolveRef(page, target, snapshot)
	}
	if err := chainedTargetError(target); err != nil {
		return nil, err
	}
	if locator, isLocator, err := parsePlaywrightLocator(target); isLocator {
		if err != nil {
			return nil, err
		}
		return resolveStrictByLocator(page, locator)
	}

	elements, err := page.Elements(target)
	if err != nil {
		return nil, fmt.Errorf("CSS selector %q: %w", target, err)
	}
	if len(elements) != 1 {
		return nil, fmt.Errorf("CSS selector %q matched %d elements; target must be unique", target, len(elements))
	}
	return elements[0], nil
}

// resolveStrictByLocator matches the strict target semantics used by CSS
// selectors: exactly one visible element must be eligible for interaction.
func resolveStrictByLocator(page *rod.Page, loc Locator) (*rod.Element, error) {
	matches, err := visibleLocatorMatches(page, loc)
	if err != nil {
		return nil, err
	}
	return requireUniqueLocatorMatch(matches, locatorDescription(loc))
}

func locatorDescription(loc Locator) string {
	if loc.Text != "" {
		return fmt.Sprintf("text=%s", quoteOrPattern(loc.Text, loc.Pattern))
	}
	return fmt.Sprintf("role=%q name=%s", normaliseRole(loc.Role), quoteOrPattern(pickName(loc), loc.Pattern))
}

func quoteOrPattern(s string, p *TextPattern) string {
	if p != nil {
		return p.String()
	}
	return strconv.Quote(s)
}

// visibleLocatorMatches returns every visible element the locator matches.
func visibleLocatorMatches(page *rod.Page, loc Locator) ([]*rod.Element, error) {
	if loc.Text != "" {
		return visibleTextMatches(page, loc)
	}

	role := normaliseRole(loc.Role)
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("a11y tree: %w", err)
	}

	seen := make(map[proto.DOMBackendNodeID]struct{})
	matches := make([]*rod.Element, 0, 1)
	for _, n := range result.Nodes {
		if !roleMatches(n, role) || !locatorNameMatches(n, loc) || n.BackendDOMNodeID == 0 {
			continue
		}
		if loc.Label != "" && loc.Role == "" && !labelTargetRole(n) {
			continue
		}
		if _, ok := seen[n.BackendDOMNodeID]; ok {
			continue
		}
		el, err := page.ElementFromNode(&proto.DOMNode{BackendNodeID: n.BackendDOMNodeID})
		if err != nil {
			continue
		}
		visible, err := el.Visible()
		if err != nil || !visible {
			continue
		}
		seen[n.BackendDOMNodeID] = struct{}{}
		matches = append(matches, el)
	}
	return matches, nil
}

func labelTargetRole(n *proto.AccessibilityAXNode) bool {
	switch strings.ToLower(axValueStr(n.Role)) {
	case "textbox", "combobox", "checkbox", "radio", "switch", "slider", "spinbutton", "listbox", "button":
		return true
	default:
		return false
	}
}

// visibleTextMatches matches like Playwright's getByText: a case-insensitive,
// whitespace-normalised substring of the element text (or a regex, or the
// whole text with exact), keeping only the innermost matching elements (an
// ancestor whose text contains the match does not count). For a substring,
// when several remain, one whose whole text equals the query wins.
func visibleTextMatches(page *rod.Page, loc Locator) ([]*rod.Element, error) {
	mode, source, flags := "substring", "", ""
	switch {
	case loc.Pattern != nil:
		mode, source, flags = "regex", loc.Pattern.Source, loc.Pattern.Flags
	case loc.Exact:
		mode = "exact"
	}
	els, err := page.ElementsByJS(rod.Eval(textCandidatesJS, loc.Text, mode, source, flags))
	if err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	matches := make([]*rod.Element, 0, len(els))
	for _, el := range els {
		visible, err := el.Visible()
		if err == nil && visible {
			matches = append(matches, el)
		}
	}
	return matches, nil
}

// textCandidatesJS returns the innermost elements whose text matches the
// query. For a substring, if a descendant matches, every element between it
// and the root also contains that text, so checking direct children is
// enough to drop ancestors; the same rule keeps the innermost regex match.
const textCandidatesJS = `(text, mode, source, flags) => {
	const space = s => (s || "").replace(/\s+/g, " ").trim();
	const norm = s => space(s).toLowerCase();
	const q = norm(text);
	const exactQ = space(text);
	const re = mode === "regex" ? new RegExp(source, (flags || "").replace(/[gy]/g, "")) : null;
	const matches = mode === "regex" ? (s => re.test(space(s))) : mode === "exact" ? (s => space(s) === exactQ) : (s => norm(s).includes(q));
	if ((mode !== "regex" && !q) || !document.body) return [];
	const skip = new Set(["SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE", "HEAD", "svg", "path"]);
	const all = document.body.querySelectorAll("*");
	const hit = new Set();
	for (const el of all) {
		if (skip.has(el.tagName)) continue;
		if (matches(el.textContent)) hit.add(el);
	}
	const minimal = [];
	for (const el of hit) {
		let inner = false;
		for (const c of el.children) { if (hit.has(c)) { inner = true; break; } }
		if (!inner) minimal.push(el);
	}
	if (mode !== "substring") return minimal;
	const exact = minimal.filter(el => norm(el.textContent) === q);
	return exact.length === 1 ? exact : minimal;
}`

func requireUniqueLocatorMatch(matches []*rod.Element, description string) (*rod.Element, error) {
	if len(matches) != 1 {
		return nil, &LocatorMatchCountError{Description: description, Count: len(matches)}
	}
	return matches[0], nil
}

// LocatorMatchCountError identifies a strict semantic-locator cardinality
// failure. Waiters retry zero matches, but reject ambiguity immediately.
type LocatorMatchCountError struct {
	Description string
	Count       int
}

func (e *LocatorMatchCountError) Error() string {
	return fmt.Sprintf("playwright locator %s matched %d visible elements; target must be unique", e.Description, e.Count)
}

func pickName(loc Locator) string {
	if loc.Name != "" {
		return loc.Name
	}
	return loc.Label
}

func roleMatches(n *proto.AccessibilityAXNode, want string) bool {
	if want == "" {
		return true
	}
	got := strings.ToLower(axValueStr(n.Role))
	want = strings.ToLower(want)
	return got == want
}

// locatorNameMatches applies the locator's name (or label) criterion to an
// accessibility node: regex, exact, or case-insensitive substring.
func locatorNameMatches(n *proto.AccessibilityAXNode, loc Locator) bool {
	if loc.Pattern != nil {
		return loc.Pattern.Match(axValueStr(n.Name))
	}
	want := pickName(loc)
	if want == "" {
		return true
	}
	if loc.Exact {
		return normalizeSpace(axValueStr(n.Name)) == normalizeSpace(want)
	}
	return nameMatches(n, want)
}

func nameMatches(n *proto.AccessibilityAXNode, want string) bool {

	if want == "" {
		return true
	}
	got := strings.ToLower(axValueStr(n.Name))
	return strings.Contains(got, strings.ToLower(want))
}

// normaliseRole expands one-letter role codes to full role names. Unknown
// inputs are returned as-is so users can pass "dialog", "tooltip", etc.
func normaliseRole(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "":
		return ""
	case "b", "button":
		return "button"
	case "a", "link":
		return "link"
	case "t", "textbox", "input":
		return "textbox"
	case "c", "checkbox":
		return "checkbox"
	case "r", "radio":
		return "radio"
	case "s", "combobox", "select":
		return "combobox"
	case "m", "menuitem":
		return "menuitem"
	case "x", "tab":
		return "tab"
	case "h", "heading":
		return "heading"
	case "img", "image":
		return "image"
	}
	return strings.ToLower(r)
}

// chainedTargetError rejects Playwright's « >> » chaining (« .panel >>
// role=button name=X », « css >> nth=0 », « getByRole(…) >> getByRole(…) »),
// which is not supported: as a CSS selector it can never match, and every
// such call used to wait its whole timeout before failing (trace,
// 2026-10-24). A « >> » inside quotes (an attribute value) is not chaining.
func chainedTargetError(target string) error {
	var quote rune
	prev := rune(0)
	for i, r := range target {
		switch {
		case quote != 0:
			if r == quote && prev != '\\' {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '>' && strings.HasPrefix(target[i:], ">>"):
			return fmt.Errorf("target %q: chaining with >> is not supported; use one CSS selector (.panel button[aria-label=\"Connect\"], :nth-child, :has()), one getByRole/getByText/getByLabel, or a @ref from snapshot", target)
		}
		prev = r
	}
	return nil
}

// invalidSelector reports a CSS selector the page rejects as malformed:
// waiting cannot make it match.
func invalidSelector(err error) bool {
	return err != nil && strings.Contains(err.Error(), "is not a valid selector")
}
