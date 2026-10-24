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

func TestTimeoutAliasAndRoleBracketShorthand(t *testing.T) {
	if got := lookupTimeout(call(map[string]any{"timeout": "30000"})); got != 30*time.Second {
		t.Fatalf("timeout alias: %s", got)
	}
	if got := lookupTimeout(call(map[string]any{"timeout": 5000, "timeout_ms": 2000})); got != 2*time.Second {
		t.Fatalf("timeout_ms must win: %s", got)
	}
	for in, want := range map[string]string{
		`role=textbox[name="Message à l’agent"]`: `getByRole("textbox", { name: "Message à l’agent" })`,
		`role=button[name='Save']`:              `getByRole("button", { name: "Save" })`,
		`role=button[name=/^Mode/i]`:            `getByRole("button", { name: /^Mode/i })`,
	} {
		if got := normalizeTarget(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestFormFieldsAcceptsObjectsAndLists(t *testing.T) {
	cases := []struct {
		raw  any
		want []formField
	}{
		{`{"@4":"kev","@3":"host"}`, []formField{{"@3", "host"}, {"@4", "kev"}}},
		{`[{"target":"@4","value":"kev"},{"target":"@3","text":"host"}]`, []formField{{"@4", "kev"}, {"@3", "host"}}},
		{map[string]any{"@1": "a"}, []formField{{"@1", "a"}}},
		{[]any{map[string]any{"target": "@2", "value": "b"}}, []formField{{"@2", "b"}}},
	}
	for _, c := range cases {
		got, err := formFields(c.raw)
		if err != nil {
			t.Fatalf("%v: %v", c.raw, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%v: %v", c.raw, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%v: %v, want %v", c.raw, got, c.want)
			}
		}
	}
	for _, bad := range []any{"", `[{"target":"@1"}]`, `{"@1": 3}`, `[1]`, 42, nil} {
		if _, err := formFields(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
