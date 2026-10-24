package electron

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dev-toolings/mcp-electron/internal/engine"
)

// pidBridgeJS builds a CDP endpoint inside the main process (see the file).
//
//go:embed pidbridge.js
var pidBridgeJS string

var (
	argRemotePort  = regexp.MustCompile(`(?:^|\s)--remote-debugging-port=(\d+)`)
	argInspect     = regexp.MustCompile(`(?:^|\s)--inspect(?:-brk)?(?:=(?:[^\s:]+:)?(\d+))?(?:\s|$)`)
	argInspectPort = regexp.MustCompile(`(?:^|\s)--inspect-port=(?:[^\s:]+:)?(\d+)`)
)

// defaultInspectPort is where Node opens its inspector on SIGUSR1 without --inspect-port.
const defaultInspectPort = 9229

// pidPorts reads the debugging ports a process was started with: its
// --remote-debugging-port (0 when absent), whether --inspect was given, and
// the port its inspector uses or would use on SIGUSR1.
func pidPorts(cmd string) (cdp int, inspectGiven bool, inspect int) {
	inspect = defaultInspectPort
	if m := argRemotePort.FindStringSubmatch(cmd); m != nil {
		cdp, _ = strconv.Atoi(m[1])
	}
	if m := argInspectPort.FindStringSubmatch(cmd); m != nil {
		inspect, _ = strconv.Atoi(m[1])
	}
	if m := argInspect.FindStringSubmatch(cmd); m != nil {
		inspectGiven = true
		if m[1] != "" {
			inspect, _ = strconv.Atoi(m[1])
		}
	}
	return cdp, inspectGiven, inspect
}

// attachPID attaches to a running Electron main process by its pid, whatever
// it was started with:
//
//   - started with --remote-debugging-port: a plain attach to that port;
//   - otherwise its Node inspector is reached (already open with --inspect,
//     or opened with SIGUSR1, refused by a packaged app whose
//     EnableNodeCliInspectArguments fuse is off), checked to belong to that
//     pid, and used to start the CDP bridge of pidbridge.js inside the app.
//
// An inspector this call opened is closed again once the bridge runs, unless
// keepInspector (eval_main) is set: an open inspector gives any local process
// the whole main process. The bridge stops by itself after the session ends.
func (m *Manager) attachPID(ctx context.Context, spec AttachSpec) (*Session, error) {
	if runtime.GOOS == "windows" {
		return nil, &UsageError{"attach by pid needs SIGUSR1 (macOS, Linux); on Windows start the app with --remote-debugging-port and attach to its endpoint"}
	}
	cmd := rootCommand(spec.PID)
	if cmd == "" {
		return nil, &UsageError{fmt.Sprintf("no process %d", spec.PID)}
	}
	if strings.Contains(cmd, " --type=") {
		return nil, &UsageError{fmt.Sprintf("process %d is an Electron helper (%s), not the main process: give its parent's pid", spec.PID, helperType(cmd))}
	}
	cdp, inspectGiven, inspectPort := pidPorts(cmd)
	if cdp > 0 {
		attach := AttachSpec{Endpoint: strconv.Itoa(cdp), Name: spec.Name}
		if inspectGiven {
			attach.Inspector = loopbackEndpoint(inspectPort)
		}
		s, err := m.Attach(ctx, attach)
		if err != nil {
			return nil, err
		}
		s.PID = spec.PID
		s.Note = fmt.Sprintf("process %d was started with --remote-debugging-port=%d: attached to it directly, nothing was opened in the app", spec.PID, cdp)
		return s, nil
	}

	if err := m.admit(); err != nil {
		return nil, err
	}
	inspector := loopbackEndpoint(inspectPort)
	opened := false
	mc, err := connectInspectorOf(ctx, inspector, spec.PID)
	if err != nil {
		if probeInspector(ctx, inspector) != "" {
			return nil, err // the port answers for another process
		}
		if inspectGiven {
			return nil, fmt.Errorf("process %d was started with --inspect but its inspector does not answer at %s", spec.PID, inspector)
		}
		if err := syscall.Kill(spec.PID, syscall.SIGUSR1); err != nil {
			return nil, fmt.Errorf("SIGUSR1 to %d: %w", spec.PID, err)
		}
		opened = true
		deadline := time.Now().Add(5 * time.Second)
		for {
			if mc, err = connectInspectorOf(ctx, inspector, spec.PID); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("process %d opened no Node inspector at %s after SIGUSR1: a packaged app with the EnableNodeCliInspectArguments fuse off refuses it (start it with --remote-debugging-port instead), or the port is taken (%v)", spec.PID, inspector, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	keep := !opened || spec.KeepInspector
	release := func() {
		if !keep {
			// Deferred: inspector.close() waits for the open connections, ours included, which ends right after.
			_, _ = mc.Eval(ctx, `setTimeout(() => { try { require('inspector').close(); } catch (e) {} }, 300); 'closing'`, 5*time.Second)
		}
		mc.Close()
	}

	raw, err := mc.Eval(ctx, pidBridgeJS, 20*time.Second)
	if err != nil {
		release()
		return nil, fmt.Errorf("starting the CDP bridge in process %d: %w", spec.PID, err)
	}
	var started struct {
		Port   int    `json:"port"`
		Token  string `json:"token"`
		Reused bool   `json:"reused"`
	}
	if err := json.Unmarshal([]byte(raw), &started); err != nil || started.Port == 0 || started.Token == "" {
		release()
		return nil, fmt.Errorf("the CDP bridge in process %d answered %q", spec.PID, raw)
	}
	endpoint := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/%s", started.Port, started.Token)
	b, err := engine.Connect(ctx, endpoint, 30*time.Second)
	if err != nil {
		release()
		return nil, err
	}
	s := &Session{Info: Info{Name: firstNonEmpty(spec.Name, appName(cmd)), Mode: "attach", Endpoint: endpoint, PID: spec.PID}, browser: b, generation: 1}
	switch {
	case !opened:
		s.Inspector = inspector
		s.main = mc
		s.Note = fmt.Sprintf("attached through a CDP bridge started inside process %d by its own inspector (%s, left as it was)", spec.PID, inspector)
	case spec.KeepInspector:
		s.Inspector = inspector
		s.main = mc
		s.Note = fmt.Sprintf("attached through a CDP bridge started inside process %d; its Node inspector, opened with SIGUSR1, stays open at %s for eval_main until the app quits: any local process can reach the main process there", spec.PID, inspector)
	default:
		release()
		s.Note = fmt.Sprintf("attached through a CDP bridge started inside process %d; the Node inspector opened with SIGUSR1 was closed again (eval_main unavailable: attach with inspector: true to keep it); the bridge stops a few seconds after stop", spec.PID)
	}
	if started.Reused {
		s.Note += "; reused the bridge an earlier session left running"
	}
	return m.add(s)
}

// connectInspectorOf connects to the inspector at endpoint and checks it is
// pid's: a port answering for another process is never used.
func connectInspectorOf(ctx context.Context, endpoint string, pid int) (*MainClient, error) {
	mc, err := ConnectMain(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	got, err := mc.Eval(ctx, "process.pid", 3*time.Second)
	if err != nil {
		mc.Close()
		return nil, err
	}
	if strings.TrimSpace(got) != strconv.Itoa(pid) {
		mc.Close()
		return nil, fmt.Errorf("the inspector at %s belongs to process %s, not %d", endpoint, strings.TrimSpace(got), pid)
	}
	return mc, nil
}

func helperType(cmd string) string {
	_, rest, _ := strings.Cut(cmd, " --type=")
	t, _, _ := strings.Cut(rest, " ")
	return t
}

// appName is the executable's base name, e.g. "Electron" or "Escape (Alpha)".
func appName(cmd string) string {
	exe := cmd
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
		exe = exe[:i]
		if j := strings.LastIndex(exe, "/"); j >= 0 {
			exe = exe[j+1:]
		}
		return exe
	}
	exe, _, _ = strings.Cut(exe, " ")
	if j := strings.LastIndex(exe, "/"); j >= 0 {
		exe = exe[j+1:]
	}
	return exe
}
