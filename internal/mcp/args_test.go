package mcp

import (
	"testing"
	"time"

	"github.com/go-rod/rod/lib/input"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestNormalizeTarget(t *testing.T) {
	for in, want := range map[string]string{
		"@3":                           "@3",
		"3":                            "@3",
		"e12":                          "@12",
		"ref:7":                        "@7",
		"text=Save":                    `getByText("Save")`,
		"label=Email":                  `getByLabel("Email")`,
		"role=button name=Save now":    `getByRole("button", { name: "Save now" })`,
		"role=dialog":                  `getByRole("dialog")`,
		"role=button name=/^Mode : /i": `getByRole("button", { name: /^Mode : /i })`,
		"#save":                        "#save",
		`getByText("x")`:               `getByText("x")`,
	} {
		if got := normalizeTarget(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestEvalTimeoutAndHoverMode(t *testing.T) {
	req := func(args map[string]any) mcpgo.CallToolRequest {
		r := mcpgo.CallToolRequest{}
		r.Params.Arguments = args
		return r
	}
	for _, c := range []struct {
		args map[string]any
		want time.Duration
	}{
		{map[string]any{}, 8 * time.Second},
		{map[string]any{"timeout_ms": 30000}, 30 * time.Second},
		{map[string]any{"timeout_ms": 999999}, 120 * time.Second},
		{map[string]any{"timeout_ms": -5}, 8 * time.Second},
	} {
		if got := evalTimeout(req(c.args)); got != c.want {
			t.Errorf("evalTimeout(%v) = %s, want %s", c.args, got, c.want)
		}
	}
	if parseHoverMode(req(map[string]any{})) != hoverAuto || parseHoverMode(req(map[string]any{"hover_first": true})) != hoverAlways || parseHoverMode(req(map[string]any{"hover_first": false})) != hoverNever {
		t.Error("hover_first: absent → auto, true → always, false → never")
	}
}

func TestParseKeyCombo(t *testing.T) {
	mods, key, err := parseKeyCombo("cmd+shift+p")
	if err != nil || len(mods) != 2 || mods[0] != input.MetaLeft || mods[1] != input.ShiftLeft || key != input.Key('p') {
		t.Fatalf("cmd+shift+p → %v %v %v", mods, key, err)
	}
	if _, key, err := parseKeyCombo("Enter"); err != nil || key != input.Enter {
		t.Fatalf("Enter → %v %v", key, err)
	}
	if _, key, err := parseKeyCombo("shift++"); err != nil || key != input.Key('+') {
		t.Fatalf("shift++ → %v %v", key, err)
	}
	if _, _, err := parseKeyCombo("hyper+x"); err == nil {
		t.Fatal("unknown modifier must fail")
	}
	if _, _, err := parseKeyCombo("é"); err == nil {
		t.Fatal("non-US character must fail with a hint")
	}
}
