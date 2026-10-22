package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TextPattern is a JavaScript regular expression literal (/source/flags)
// given as a locator name or text. Accessible names are matched in Go with
// the RE2 translation; getByText runs the original in the page.
type TextPattern struct {
	Source string
	Flags  string
	re     *regexp.Regexp
}

// String renders the literal as written.
func (p *TextPattern) String() string { return "/" + p.Source + "/" + p.Flags }

// Match reports whether s (whitespace-normalised) matches.
func (p *TextPattern) Match(s string) bool { return p.re.MatchString(normalizeSpace(s)) }

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// newTextPattern compiles a JavaScript regex for Go: flags i, m and s map to
// RE2 flags, g/y/u/d/v are irrelevant to a single test, \uXXXX and \u{X}
// escapes become \x{X}. Constructs RE2 lacks (lookaround, backreferences)
// are reported instead of silently matching something else.
func newTextPattern(source, flags string) (*TextPattern, error) {
	var goFlags string
	for _, f := range flags {
		switch f {
		case 'i', 'm', 's':
			if !strings.ContainsRune(goFlags, f) {
				goFlags += string(f)
			}
		case 'g', 'y', 'u', 'd', 'v':
		default:
			return nil, fmt.Errorf("regex /%s/%s: unknown flag %q", source, flags, f)
		}
	}
	var b strings.Builder
	if goFlags != "" {
		b.WriteString("(?" + goFlags + ")")
	}
	for i := 0; i < len(source); i++ {
		c := source[i]
		if c != '\\' || i+1 >= len(source) {
			b.WriteByte(c)
			continue
		}
		next := source[i+1]
		if next == 'u' {
			if i+2 < len(source) && source[i+2] == '{' {
				if end := strings.IndexByte(source[i+3:], '}'); end >= 0 {
					b.WriteString(`\x{` + source[i+3:i+3+end] + `}`)
					i += 3 + end
					continue
				}
			}
			if i+6 <= len(source) {
				if _, err := strconv.ParseUint(source[i+2:i+6], 16, 16); err == nil {
					b.WriteString(`\x{` + source[i+2:i+6] + `}`)
					i += 5
					continue
				}
			}
		}
		b.WriteByte(c)
		b.WriteByte(next)
		i++
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("regex /%s/%s is not supported for accessible names (RE2 syntax: no lookaround or backreferences): %w", source, flags, err)
	}
	return &TextPattern{Source: source, Flags: flags, re: re}, nil
}

// locatorLexer reads the small JavaScript subset of a Playwright locator
// call: string and regex literals, booleans, identifiers, punctuation.
type locatorLexer struct {
	s string
	i int
}

func (l *locatorLexer) skipSpace() {
	for l.i < len(l.s) {
		r, n := utf8.DecodeRuneInString(l.s[l.i:])
		if !unicode.IsSpace(r) {
			return
		}
		l.i += n
	}
}

func (l *locatorLexer) eat(tok string) bool {
	l.skipSpace()
	if strings.HasPrefix(l.s[l.i:], tok) {
		l.i += len(tok)
		return true
	}
	return false
}

func (l *locatorLexer) peek() byte {
	l.skipSpace()
	if l.i < len(l.s) {
		return l.s[l.i]
	}
	return 0
}

func (l *locatorLexer) ident() string {
	l.skipSpace()
	start := l.i
	for l.i < len(l.s) {
		c := l.s[l.i]
		if c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || l.i > start && c >= '0' && c <= '9' {
			l.i++
			continue
		}
		break
	}
	return l.s[start:l.i]
}

// stringLit reads a '…', "…" or `…` literal (no ${} interpolation).
func (l *locatorLexer) stringLit() (string, error) {
	l.skipSpace()
	if l.i >= len(l.s) {
		return "", fmt.Errorf("expected a string")
	}
	quote := l.s[l.i]
	if quote != '"' && quote != '\'' && quote != '`' {
		return "", fmt.Errorf("expected a string at %q", l.rest())
	}
	l.i++
	var b strings.Builder
	for l.i < len(l.s) {
		c := l.s[l.i]
		switch {
		case c == quote:
			l.i++
			return b.String(), nil
		case quote == '`' && c == '$' && l.i+1 < len(l.s) && l.s[l.i+1] == '{':
			return "", fmt.Errorf("template interpolation is not supported")
		case c == '\\' && l.i+1 < len(l.s):
			l.i++
			e := l.s[l.i]
			l.i++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case '0':
				b.WriteByte(0)
			case 'x':
				if l.i+2 > len(l.s) {
					return "", fmt.Errorf("bad \\x escape")
				}
				v, err := strconv.ParseUint(l.s[l.i:l.i+2], 16, 8)
				if err != nil {
					return "", fmt.Errorf("bad \\x escape")
				}
				b.WriteRune(rune(v))
				l.i += 2
			case 'u':
				hex := ""
				if l.i < len(l.s) && l.s[l.i] == '{' {
					end := strings.IndexByte(l.s[l.i:], '}')
					if end < 0 {
						return "", fmt.Errorf("bad \\u escape")
					}
					hex, l.i = l.s[l.i+1:l.i+end], l.i+end+1
				} else if l.i+4 <= len(l.s) {
					hex, l.i = l.s[l.i:l.i+4], l.i+4
				}
				v, err := strconv.ParseUint(hex, 16, 32)
				if err != nil {
					return "", fmt.Errorf("bad \\u escape")
				}
				b.WriteRune(rune(v))
			case '\n':
				// line continuation
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
			l.i++
		}
	}
	return "", fmt.Errorf("unterminated string")
}

// regexLit reads /source/flags; a '/' inside a [...] class or escaped does
// not end the source.
func (l *locatorLexer) regexLit() (*TextPattern, error) {
	l.skipSpace()
	if l.i >= len(l.s) || l.s[l.i] != '/' {
		return nil, fmt.Errorf("expected a regex")
	}
	start := l.i + 1
	inClass := false
	for j := start; j < len(l.s); j++ {
		switch c := l.s[j]; {
		case c == '\\':
			j++
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '/' && !inClass:
			source := l.s[start:j]
			if source == "" {
				return nil, fmt.Errorf("empty regex")
			}
			l.i = j + 1
			flags := l.ident()
			return newTextPattern(source, flags)
		case c == '\n':
			return nil, fmt.Errorf("unterminated regex")
		}
	}
	return nil, fmt.Errorf("unterminated regex")
}

func (l *locatorLexer) rest() string { return l.s[l.i:] }

// locatorValue is a string or a regex argument.
type locatorValue struct {
	text    string
	pattern *TextPattern
}

func (l *locatorLexer) value() (locatorValue, error) {
	if l.peek() == '/' {
		p, err := l.regexLit()
		return locatorValue{pattern: p}, err
	}
	s, err := l.stringLit()
	return locatorValue{text: s}, err
}

// options reads `{ key: value, … }` with keys from allowed.
func (l *locatorLexer) options(allowed ...string) (map[string]any, error) {
	if !l.eat("{") {
		return nil, fmt.Errorf("expected an options object at %q", l.rest())
	}
	out := map[string]any{}
	for {
		if l.eat("}") {
			return out, nil
		}
		var key string
		if c := l.peek(); c == '"' || c == '\'' {
			k, err := l.stringLit()
			if err != nil {
				return nil, err
			}
			key = k
		} else {
			key = l.ident()
		}
		if key == "" {
			return nil, fmt.Errorf("expected an option name at %q", l.rest())
		}
		ok := false
		for _, a := range allowed {
			ok = ok || a == key
		}
		if !ok {
			return nil, fmt.Errorf("option %q is not supported (supported: %s)", key, strings.Join(allowed, ", "))
		}
		if !l.eat(":") {
			return nil, fmt.Errorf("expected ':' after %s", key)
		}
		switch key {
		case "exact":
			switch w := l.ident(); w {
			case "true":
				out[key] = true
			case "false":
				out[key] = false
			default:
				return nil, fmt.Errorf("exact must be true or false")
			}
		default:
			v, err := l.value()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out[key] = v
		}
		if !l.eat(",") {
			if !l.eat("}") {
				return nil, fmt.Errorf("expected ',' or '}' at %q", l.rest())
			}
			return out, nil
		}
	}
}

var locatorCallPrefix = regexp.MustCompile(`^(?:page\.)?getBy([A-Za-z]+)\(`)

// parsePlaywrightLocator parses getByRole / getByText / getByLabel with
// string or regex arguments:
//
//	getByRole("button", { name: "Save" })      name: case-insensitive substring
//	getByRole("button", { name: /^Mode : / })  name: regex (JS literal)
//	getByRole("button", { name: "Save", exact: true })
//	getByText("Save") / getByText(/^Save$/i) / getByText("Save", { exact: true })
//	getByLabel("Email") / getByLabel(/mail/i)
//
// Anything else that starts with getBy is reported as an error (isLocator
// true) rather than tried as a CSS selector that can never match.
func parsePlaywrightLocator(target string) (Locator, bool, error) {
	target = strings.TrimSpace(target)
	m := locatorCallPrefix.FindStringSubmatch(target)
	if m == nil {
		return Locator{}, false, nil
	}
	fail := func(format string, args ...any) (Locator, bool, error) {
		return Locator{}, true, fmt.Errorf("playwright locator %q: %s", target, fmt.Sprintf(format, args...))
	}
	l := &locatorLexer{s: target, i: len(m[0])}
	var loc Locator
	switch m[1] {
	case "Role":
		role, err := l.stringLit()
		if err != nil {
			return fail("role: %v", err)
		}
		loc.Role = role
		if l.eat(",") && l.peek() != ')' {
			opts, err := l.options("name", "exact")
			if err != nil {
				return fail("%v", err)
			}
			if v, ok := opts["name"].(locatorValue); ok {
				loc.Name, loc.Pattern = v.text, v.pattern
				if v.pattern != nil {
					loc.Name = v.pattern.String()
				}
			}
			loc.Exact, _ = opts["exact"].(bool)
		}
	case "Text", "Label":
		v, err := l.value()
		if err != nil {
			return fail("%v", err)
		}
		text := v.text
		if v.pattern != nil {
			text = v.pattern.String()
		}
		if m[1] == "Text" {
			loc.Text = text
		} else {
			loc.Label = text
		}
		loc.Pattern = v.pattern
		if l.eat(",") && l.peek() != ')' {
			opts, err := l.options("exact")
			if err != nil {
				return fail("%v", err)
			}
			loc.Exact, _ = opts["exact"].(bool)
		}
	default:
		return fail("getBy%s is not supported: use getByRole, getByText or getByLabel (or a CSS selector)", m[1])
	}
	if !l.eat(")") {
		return fail("expected ')' at %q", l.rest())
	}
	if l.skipSpace(); l.i != len(l.s) {
		return fail("unexpected %q after the call (chained calls are not supported)", l.rest())
	}
	if loc.IsEmpty() {
		return fail("empty locator")
	}
	return loc, true, nil
}
