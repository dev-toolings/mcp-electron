package mcp

import (
	"fmt"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

// namedKeys maps the key names an agent writes to Rod input keys.
var namedKeys = map[string]input.Key{
	"enter": input.Enter, "return": input.Enter, "tab": input.Tab, "escape": input.Escape, "esc": input.Escape,
	"backspace": input.Backspace, "delete": input.Delete, "del": input.Delete, "space": input.Space,
	"arrowup": input.ArrowUp, "up": input.ArrowUp, "arrowdown": input.ArrowDown, "down": input.ArrowDown,
	"arrowleft": input.ArrowLeft, "left": input.ArrowLeft, "arrowright": input.ArrowRight, "right": input.ArrowRight,
	"home": input.Home, "end": input.End, "pageup": input.PageUp, "pagedown": input.PageDown, "insert": input.Insert,
	"f1": input.F1, "f2": input.F2, "f3": input.F3, "f4": input.F4, "f5": input.F5, "f6": input.F6,
	"f7": input.F7, "f8": input.F8, "f9": input.F9, "f10": input.F10, "f11": input.F11, "f12": input.F12,
}

var modifierKeys = map[string]input.Key{
	"cmd": input.MetaLeft, "command": input.MetaLeft, "meta": input.MetaLeft, "super": input.MetaLeft, "win": input.MetaLeft,
	"ctrl": input.ControlLeft, "control": input.ControlLeft,
	"alt": input.AltLeft, "option": input.AltLeft, "opt": input.AltLeft,
	"shift": input.ShiftLeft,
}

// parseKeyCombo turns "cmd+shift+p", "Enter" or "a" into modifiers and a key.
func parseKeyCombo(combo string) (mods []input.Key, key input.Key, err error) {
	parts := strings.Split(strings.TrimSpace(combo), "+")
	if len(parts) == 0 || combo == "" {
		return nil, 0, fmt.Errorf("key is required")
	}
	// "shift++" means the + key: rebuild a trailing empty part as "+".
	if strings.HasSuffix(combo, "+") && len(parts) >= 2 && parts[len(parts)-1] == "" {
		parts = append(parts[:len(parts)-2], "+")
	}
	for i, p := range parts {
		name := strings.ToLower(strings.TrimSpace(p))
		if i < len(parts)-1 {
			m, ok := modifierKeys[name]
			if !ok {
				return nil, 0, fmt.Errorf("unknown modifier %q in %q (cmd, ctrl, alt, shift)", p, combo)
			}
			mods = append(mods, m)
			continue
		}
		if k, ok := namedKeys[name]; ok {
			key = k
			continue
		}
		r := []rune(strings.TrimSpace(p))
		if len(r) != 1 {
			return nil, 0, fmt.Errorf("unknown key %q: use a single character, Enter, Tab, Escape, Backspace, Delete, Arrow*, Home, End, PageUp, PageDown, F1-F12, optionally with cmd/ctrl/alt/shift+", p)
		}
		if r[0] > 0x7f {
			return nil, 0, fmt.Errorf("key %q is not on the US layout: type it with the type tool instead", p)
		}
		key = input.Key(r[0])
	}
	return mods, key, nil
}

// pressKeys dispatches a key combination to the page's focused element.
func pressKeys(page *rod.Page, combo string) error {
	mods, key, err := parseKeyCombo(combo)
	if err != nil {
		return err
	}
	ka := page.KeyActions()
	if len(mods) > 0 {
		ka = ka.Press(mods...)
	}
	ka = ka.Type(key)
	if len(mods) > 0 {
		ka = ka.Release(mods...)
	}
	if err := ka.Do(); err != nil {
		return fmt.Errorf("press %s: %w", combo, err)
	}
	return nil
}

// PressKeys is the exported form for the CLI flow runner.
func PressKeys(page *rod.Page, combo string) error { return pressKeys(page, combo) }
