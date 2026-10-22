package flow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dev-toolings/mcp-electron/internal/engine"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Runner executes flows against one window.
type Runner struct {
	Browser *engine.Browser
	Page    *rod.Page
	// Main evaluates JavaScript in the main process (nil when unavailable).
	Main func(ctx context.Context, expr string) (string, error)
	// Press dispatches a key combination.
	Press func(page *rod.Page, combo string) error
	// OutDir receives screenshots (takeScreenshot and failure captures).
	// Empty disables writing them.
	OutDir string
	// Env supplies ${NAME} values; the flow header's env overrides it.
	Env map[string]string
	// LookupTimeout is the default element lookup timeout (10 s).
	LookupTimeout time.Duration
	// Launch handles launchApp; nil makes launchApp a no-op.
	Launch func(ctx context.Context) error
}

// StepResult is the outcome of one executed command.
type StepResult struct {
	Index      string `json:"index"`
	Command    string `json:"command"`
	Label      string `json:"label,omitempty"`
	Line       int    `json:"line"`
	Status     string `json:"status"` // passed, failed, skipped (optional failure)
	DurationMS int64  `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
	Error      string `json:"error,omitempty"`
	Screenshot string `json:"screenshot,omitempty"`
}

// Result is a whole flow run.
type Result struct {
	Passed bool         `json:"passed"`
	Steps  []StepResult `json:"steps"`
	// FailureScreen is the compact UI outline at the failing step.
	FailureScreen string `json:"failure_screen,omitempty"`
	// FailureScreenshot is the PNG captured at the failing step.
	FailureScreenshot string `json:"failure_screenshot,omitempty"`
	DurationMS        int64  `json:"duration_ms"`
}

// Summary renders the result for an agent.
func (r *Result) Summary() string {
	var sb strings.Builder
	status := "PASSED"
	if !r.Passed {
		status = "FAILED"
	}
	fmt.Fprintf(&sb, "%s in %s, %d step(s)\n", status, time.Duration(r.DurationMS)*time.Millisecond, len(r.Steps))
	for _, s := range r.Steps {
		mark := "✓"
		switch s.Status {
		case "failed":
			mark = "✗"
		case "skipped":
			mark = "~"
		}
		fmt.Fprintf(&sb, "%s %s %s", mark, s.Index, s.Command)
		if s.Label != "" {
			fmt.Fprintf(&sb, " (%s)", s.Label)
		}
		if s.Detail != "" {
			fmt.Fprintf(&sb, ": %s", s.Detail)
		}
		if s.Error != "" {
			fmt.Fprintf(&sb, " — %s", s.Error)
		}
		if s.Screenshot != "" {
			fmt.Fprintf(&sb, " [%s]", s.Screenshot)
		}
		sb.WriteString("\n")
	}
	if r.FailureScreen != "" {
		sb.WriteString("screen at failure:\n" + r.FailureScreen + "\n")
	}
	if r.FailureScreenshot != "" {
		sb.WriteString("screenshot: " + r.FailureScreenshot + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Run executes the flow. It stops at the first failing non-optional step and
// records the screen at that point.
func (r *Runner) Run(ctx context.Context, f *Flow) *Result {
	env := map[string]string{}
	for k, v := range r.Env {
		env[k] = v
	}
	for k, v := range f.Env {
		env[k] = v
	}
	start := time.Now()
	res := &Result{Passed: true}
	r.run(ctx, f.Steps, env, "", res)
	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

func (r *Runner) run(ctx context.Context, steps []Step, env map[string]string, prefix string, res *Result) bool {
	for i, s := range steps {
		idx := prefix + strconv.Itoa(i+1)
		if err := ctx.Err(); err != nil {
			res.Passed = false
			res.Steps = append(res.Steps, StepResult{Index: idx, Command: s.Command, Line: s.Line, Status: "failed", Error: err.Error()})
			return false
		}
		switch s.Command {
		case "repeat":
			for k := 0; k < s.Count; k++ {
				if !r.run(ctx, s.Steps, env, fmt.Sprintf("%s.%d.", idx, k+1), res) {
					return false
				}
			}
			continue
		case "runFlow":
			if !r.run(ctx, s.Steps, env, idx+".", res) {
				return false
			}
			continue
		}
		t0 := time.Now()
		sr := StepResult{Index: idx, Command: s.Command, Label: s.Label, Line: s.Line}
		detail, shot, err := r.exec(ctx, s, env)
		sr.DurationMS = time.Since(t0).Milliseconds()
		sr.Detail, sr.Screenshot = detail, shot
		switch {
		case err == nil:
			sr.Status = "passed"
		case s.Optional && ctx.Err() == nil:
			sr.Status, sr.Error = "skipped", err.Error()
		default:
			sr.Status, sr.Error = "failed", err.Error()
			if ctx.Err() != nil {
				sr.Error = "interrupted: " + ctx.Err().Error()
			}
			res.Steps = append(res.Steps, sr)
			res.Passed = false
			if ctx.Err() == nil {
				r.captureFailure(idx, res)
			}
			return false
		}
		res.Steps = append(res.Steps, sr)
	}
	return true
}

func (r *Runner) captureFailure(idx string, res *Result) {
	if result, err := engine.Extract(r.Page, engine.LevelSkeleton, ""); err == nil {
		_ = r.Browser.SaveSnapshot(r.Page, result)
		res.FailureScreen = strings.TrimRight(engine.FormatTextProfile(result, engine.RenderProfile{Agent: true, Format: "text", MaxLabelLen: 80, AbbrevRoles: true}), "\n")
	}
	if p, err := r.screenshot("failure-step-" + strings.ReplaceAll(idx, ".", "_")); err == nil {
		res.FailureScreenshot = p
	}
}

func (r *Runner) lookup(s Step) time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	if r.LookupTimeout > 0 {
		return r.LookupTimeout
	}
	return 10 * time.Second
}

// settle gives the UI a moment to react after an action, bounded.
func (r *Runner) settle() {
	_ = engine.WaitForImminentDOM(r.Page, 150*time.Millisecond)
}

func (r *Runner) find(target string, timeout time.Duration) (*rod.Element, error) {
	el, err := engine.WaitForTarget(r.Page, target, r.Browser.Snapshot(r.Page), engine.StateVisible, timeout)
	if err != nil {
		return nil, err
	}
	if el == nil {
		return nil, fmt.Errorf("%s: not found", target)
	}
	return el, nil
}

func (r *Runner) exec(ctx context.Context, s Step, env map[string]string) (detail, shot string, err error) {
	page := r.Page
	switch s.Command {
	case "launchApp":
		if r.Launch != nil {
			return "", "", r.Launch(ctx)
		}
		return "application already attached", "", nil
	case "tapOn", "doubleTapOn":
		if s.Point != "" {
			x, y, err := r.point(Expand(s.Point, env))
			if err != nil {
				return "", "", err
			}
			if err := page.Mouse.MoveTo(proto.Point{X: x, Y: y}); err != nil {
				return "", "", err
			}
			if err := page.Mouse.Click(proto.InputMouseButtonLeft, max(s.Count, 1)); err != nil {
				return "", "", err
			}
			r.settle()
			return fmt.Sprintf("tapped %.0f,%.0f", x, y), "", nil
		}
		target := Expand(s.Target, env)
		el, err := r.find(target, r.lookup(s))
		if err != nil {
			return "", "", err
		}
		if err := engine.EnsureActionable(el, r.lookup(s)); err != nil {
			return "", "", fmt.Errorf("%s: %w", target, err)
		}
		if s.Count == 2 {
			box, err := el.Shape()
			if err != nil {
				return "", "", err
			}
			rc := box.Box()
			if rc == nil {
				return "", "", fmt.Errorf("%s has no visible box", target)
			}
			if err := page.Mouse.MoveTo(proto.Point{X: rc.X + rc.Width/2, Y: rc.Y + rc.Height/2}); err != nil {
				return "", "", err
			}
			if err := page.Mouse.Click(proto.InputMouseButtonLeft, 2); err != nil {
				return "", "", err
			}
		} else if err := engine.ClickElementWithButton(page, el, proto.InputMouseButtonLeft); err != nil {
			return "", "", err
		}
		r.settle()
		return target, "", nil
	case "assertVisible":
		target := Expand(s.Target, env)
		if _, err := r.find(target, r.lookup(s)); err != nil {
			return "", "", err
		}
		return target, "", nil
	case "assertNotVisible":
		target := Expand(s.Target, env)
		el, err := engine.WaitForTarget(page, target, r.Browser.Snapshot(page), engine.StateHidden, r.lookup(s))
		if err != nil {
			return "", "", err
		}
		if el != nil {
			return "", "", fmt.Errorf("%s is still visible", target)
		}
		return target + " not visible", "", nil
	case "inputText":
		if err := engine.TypeFocused(page, Expand(s.Text, env)); err != nil {
			return "", "", err
		}
		r.settle()
		return "", "", nil
	case "eraseText":
		for i := 0; i < s.Count; i++ {
			if err := page.Keyboard.Type(input("Backspace")); err != nil {
				return "", "", err
			}
		}
		return "", "", nil
	case "pressKey":
		if r.Press == nil {
			return "", "", fmt.Errorf("pressKey is not available")
		}
		if err := r.Press(page, Expand(s.Key, env)); err != nil {
			return "", "", err
		}
		r.settle()
		return "", "", nil
	case "scroll":
		dy := r.scrollStep()
		if s.Direction == "UP" {
			dy = -dy
		}
		_, err := engine.ScrollBy(page, dy)
		return "", "", err
	case "scrollUntilVisible":
		return r.scrollUntil(ctx, s, env)
	case "waitForAnimationToEnd":
		if err := engine.WaitForPage(page, "stable"); err != nil {
			return "", "", err
		}
		return "settled", "", nil
	case "extendedWaitUntil":
		if s.Visible != "" {
			target := Expand(s.Visible, env)
			if _, err := r.find(target, r.lookup(s)); err != nil {
				return "", "", err
			}
			return target, "", nil
		}
		target := Expand(s.NotVisible, env)
		el, err := engine.WaitForTarget(page, target, r.Browser.Snapshot(page), engine.StateHidden, r.lookup(s))
		if err != nil {
			return "", "", err
		}
		if el != nil {
			return "", "", fmt.Errorf("%s is still visible", target)
		}
		return "", "", nil
	case "takeScreenshot":
		p, err := r.screenshot(Expand(s.Text, env))
		return "", p, err
	case "evalJs", "assertJs":
		v, err := engine.EvalJSTimeout(page, Expand(s.Text, env), "", nil, 8*time.Second)
		if err != nil {
			return "", "", err
		}
		if s.Command == "assertJs" && !truthy(v) {
			return v, "", fmt.Errorf("assertion is falsy: %s", v)
		}
		return v, "", nil
	case "evalMain", "assertMain":
		if r.Main == nil {
			return "", "", fmt.Errorf("%s needs the main process inspector (launch with inspect: true)", s.Command)
		}
		v, err := r.Main(ctx, Expand(s.Text, env))
		if err != nil {
			return "", "", err
		}
		if s.Command == "assertMain" && !truthy(v) {
			return v, "", fmt.Errorf("assertion is falsy: %s", v)
		}
		return v, "", nil
	}
	return "", "", fmt.Errorf("unsupported command %s", s.Command)
}

func (r *Runner) scrollStep() int {
	v, err := r.Page.Eval(`() => Math.max(100, Math.floor(window.innerHeight / 2))`)
	if err != nil {
		return 300
	}
	return v.Value.Int()
}

func (r *Runner) scrollUntil(ctx context.Context, s Step, env map[string]string) (string, string, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	target := Expand(s.Target, env)
	deadline := time.Now().Add(timeout)
	prev := -1.0
	for {
		if el, err := engine.WaitForTarget(r.Page, target, r.Browser.Snapshot(r.Page), engine.StateVisible, 0); err == nil && el != nil {
			return target, "", nil
		}
		pos, err := r.Page.Eval(`() => window.scrollY + window.scrollX`)
		if err != nil {
			return "", "", err
		}
		if pos.Value.Num() == prev {
			return "", "", fmt.Errorf("reached the end of the scrollable area without finding %s", target)
		}
		prev = pos.Value.Num()
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return "", "", fmt.Errorf("%s did not become visible within %s", target, timeout)
		}
		step := r.scrollStep()
		var js string
		switch s.Direction {
		case "UP":
			js = fmt.Sprintf(`() => window.scrollBy(0, -%d)`, step)
		case "RIGHT":
			js = fmt.Sprintf(`() => window.scrollBy(%d, 0)`, step)
		case "LEFT":
			js = fmt.Sprintf(`() => window.scrollBy(-%d, 0)`, step)
		default:
			js = fmt.Sprintf(`() => window.scrollBy(0, %d)`, step)
		}
		if _, err := r.Page.Eval(js); err != nil {
			return "", "", err
		}
		_ = engine.WaitForImminentDOM(r.Page, 200*time.Millisecond)
	}
}

// point resolves "x,y" or "x%,y%" against the viewport.
func (r *Runner) point(p string) (float64, float64, error) {
	parts := strings.Split(p, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("point must be \"x,y\" or \"x%%,y%%\", got %q", p)
	}
	var w, h float64
	if strings.Contains(p, "%") {
		v, err := r.Page.Eval(`() => [window.innerWidth, window.innerHeight]`)
		if err != nil {
			return 0, 0, err
		}
		arr := v.Value.Arr()
		if len(arr) == 2 {
			w, h = arr[0].Num(), arr[1].Num()
		}
	}
	var xy [2]float64
	for i, s := range parts {
		s = strings.TrimSpace(s)
		pct := strings.HasSuffix(s, "%")
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			return 0, 0, fmt.Errorf("bad point %q", p)
		}
		if pct {
			if v < 0 || v > 100 {
				return 0, 0, fmt.Errorf("percentage out of range in %q", p)
			}
			if i == 0 {
				v = w * v / 100
			} else {
				v = h * v / 100
			}
		}
		xy[i] = v
	}
	return xy[0], xy[1], nil
}

func (r *Runner) screenshot(name string) (string, error) {
	if r.OutDir == "" {
		return "", fmt.Errorf("no output directory for screenshots")
	}
	data, err := engine.TakeScreenshotFormat(r.Page, false, "", "png", 0, nil)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(name, ".png") {
		name += ".png"
	}
	path := filepath.Join(r.OutDir, filepath.Clean(name))
	if !strings.HasPrefix(path, filepath.Clean(r.OutDir)+string(filepath.Separator)) {
		return "", fmt.Errorf("screenshot path escapes the output directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o600)
}

func truthy(v string) bool {
	switch strings.TrimSpace(v) {
	case "", "false", "null", "undefined", "0", "NaN", `""`:
		return false
	}
	return true
}
