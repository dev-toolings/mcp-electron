package electron

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dev-toolings/mcp-electron/internal/engine"
	"github.com/go-rod/rod"
)

// State of a session's application.
type State string

const (
	StateRunning    State = "running"
	StateRestarting State = "restarting" // the debugging endpoint stopped answering; waiting for it to come back
	StateExited     State = "exited"     // the owned process ended
	StateStopped    State = "stopped"
)

// keepPaintingArgs are Chromium switches for an Electron app driven in the
// background: a window covered by other windows then stays "visible" and
// keeps running requestAnimationFrame and timers.
var keepPaintingArgs = []string{"--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding"}

// LaunchSpec describes what to start.
type LaunchSpec struct {
	// Target is an Electron project directory, a .app bundle or an Electron
	// executable. Ignored when Command is set.
	Target string
	// Command starts the app through a development command (["bun","run",
	// "dev"]); it must hand the ports in MCP_ELECTRON_CDP_PORT and
	// MCP_ELECTRON_INSPECT_PORT (env or ${…} placeholders) to electron.
	Command []string
	// Args are extra application arguments (Target launches only).
	Args []string
	Env  map[string]string
	// Dir is the working directory (defaults to the project or target dir).
	Dir string
	// Isolate gives a Target launch a private --user-data-dir (default true).
	Isolate bool
	// Inspect opens the main process's Node inspector (default true).
	Inspect        bool
	StartupTimeout time.Duration
	// Name overrides the display name.
	Name string
}

// AttachSpec selects a running application.
type AttachSpec struct {
	// Endpoint is the remote debugging endpoint (http://127.0.0.1:PORT).
	Endpoint string
	// Inspector is the main process inspector endpoint, when known.
	Inspector string
	Name      string
}

// Info is the identity of a session: what the agent reads back.
type Info struct {
	ID   string `json:"session_id"`
	Name string `json:"name"`
	// Mode is "target" (electron binary started here), "command" (dev
	// command started here) or "attach".
	Mode string `json:"mode"`
	// Owned is true when the session started the application and will stop it.
	Owned bool `json:"owned"`
	// Dir holds this session's artifacts (screenshots, flow outputs).
	Dir       string    `json:"artifacts_dir"`
	Started   time.Time `json:"started"`
	Endpoint  string    `json:"cdp_endpoint"`
	Inspector string    `json:"inspector_endpoint,omitempty"`
	PID       int       `json:"pid,omitempty"`
	Profile   string    `json:"profile_dir,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// Session is one driven application.
type Session struct {
	Info

	// lastUse is the last tool or flow access (Manager.Get); idleAfter and
	// onIdle stop an owned app nobody uses.
	lastUse   time.Time
	idleAfter time.Duration
	onIdle    func()
	idleFired bool
	ticks     int
	usage     Usage
	usageAt   time.Time

	mu         sync.Mutex
	browser    *engine.Browser
	main       *MainClient
	proc       *ownedProc
	ring       *Ring
	root       string // private temp root (profile), removed on stop
	state      State
	since      time.Time // when the current state began
	generation int
	restarts   []time.Time
	lastErr    string
	stopped    bool
	dialogs    *engine.DialogAutoPolicy
	hubbed     map[string]bool
	stopWatch  chan struct{}
	actions    sync.Mutex
}

// Actions serialises UI actions on this session (page tools take it; status
// and logs do not).
func (s *Session) Actions() *sync.Mutex { return &s.actions }

// Status is the agent-facing view of a session.
type Status struct {
	Info
	State      State       `json:"state"`
	StateSince time.Time   `json:"state_since"`
	Generation int         `json:"generation"`
	Restarts   []time.Time `json:"restarts,omitempty"`
	LastError  string      `json:"last_error,omitempty"`
	MainProc   bool        `json:"main_inspector_connected"`
	Descendant int         `json:"descendant_processes,omitempty"`
	// Usage is the live process tree (owned sessions only; measured at
	// most once per second).
	Usage *Usage `json:"usage,omitempty"`
	// IdleFor is the time since the last tool access; IdleStopIn is the
	// remaining time before an unused owned app is stopped (0: never).
	IdleFor    time.Duration `json:"idle_for"`
	IdleStopIn time.Duration `json:"idle_stop_in,omitempty"`
}

// Status snapshots the session state without touching the application.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Info: s.Info, State: s.state, StateSince: s.since, Generation: s.generation, Restarts: append([]time.Time(nil), s.restarts...), LastError: s.lastErr, MainProc: s.main != nil}
	if s.proc != nil {
		st.Descendant = len(s.proc.descendants())
		if !s.stopped && s.state != StateExited {
			if time.Since(s.usageAt) > time.Second {
				s.usage, s.usageAt = s.proc.usage(), time.Now()
			}
			u := s.usage
			st.Usage = &u
		}
	}
	st.IdleFor = time.Since(s.lastUse)
	if s.idleAfter > 0 && !s.stopped {
		st.IdleStopIn = max(s.idleAfter-st.IdleFor, 0)
	}
	return st
}

// Touch records a use of the session, deferring the idle stop.
func (s *Session) Touch() {
	s.mu.Lock()
	s.lastUse = time.Now()
	s.mu.Unlock()
}

// State returns the current state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Browser returns the live connection, reconnecting once if the endpoint
// came back since the last probe. It fails with the session state when the
// application is restarting, exited or unreachable.
func (s *Session) Browser(ctx context.Context) (*engine.Browser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, fmt.Errorf("session %s is stopped", s.ID)
	}
	if s.browser != nil && s.browser.Alive(2*time.Second) {
		return s.browser, nil
	}
	s.tickLocked(ctx)
	if s.browser != nil {
		return s.browser, nil
	}
	return nil, s.unavailableLocked()
}

func (s *Session) unavailableLocked() error {
	switch s.state {
	case StateRestarting:
		return fmt.Errorf("%s is restarting since %s (its debugging endpoint %s stopped answering: a dev loop recompile, a crash or a relaunch). Retry in a moment, or read logs for the cause", s.Name, s.since.Format("15:04:05"), s.Endpoint)
	case StateExited:
		tail := ""
		if s.ring != nil {
			tail = "\nlast output:\n" + s.ring.Tail(15)
		}
		return fmt.Errorf("%s exited (%s)%s", s.Name, s.lastErr, tail)
	case StateStopped:
		return fmt.Errorf("session %s is stopped", s.ID)
	}
	return fmt.Errorf("%s is not reachable at %s: %s", s.Name, s.Endpoint, s.lastErr)
}

// Main returns the main process inspector client, if connected.
func (s *Session) Main() *MainClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.main
}

// Dialogs returns the JavaScript dialog policy applied to every window.
func (s *Session) Dialogs() *engine.DialogAutoPolicy { return s.dialogs }

// Page returns the page for a window index through a live connection.
func (s *Session) Page(ctx context.Context, window int) (*engine.Browser, *rod.Page, error) {
	b, err := s.Browser(ctx)
	if err != nil {
		return nil, nil, err
	}
	page, err := b.Page(window)
	if err != nil {
		return nil, nil, err
	}
	s.preparePage(b, page)
	// The caller's context bounds every CDP call on the returned page, so a
	// cancelled tool call or an interrupted run-flow stops waiting at once;
	// the event hubs stay on the session-scoped page.
	return b, page.Context(ctx), nil
}

// preparePage starts the event hub (console, exceptions, network) and the
// dialog handler on a page the first time it is used.
func (s *Session) preparePage(b *engine.Browser, page *rod.Page) {
	key := string(page.TargetID)
	s.mu.Lock()
	if s.hubbed == nil {
		s.hubbed = map[string]bool{}
	}
	done := s.hubbed[key]
	s.hubbed[key] = true
	s.mu.Unlock()
	if done {
		return
	}
	_, _ = engine.StartEventHub(page)
	engine.StartDialogAutoHandler(page, s.dialogs)
}

func (s *Session) ensureHubsLocked() {
	if s.browser == nil {
		return
	}
	wins, err := s.browser.Windows()
	if err != nil {
		return
	}
	b := s.browser
	s.mu.Unlock()
	for _, w := range wins {
		if page, err := b.Page(w.Index); err == nil {
			s.preparePage(b, page)
		}
	}
	s.mu.Lock()
}

// tickLocked is one step of the watch loop: it notices an exited process, a
// vanished endpoint, and reconnects when the endpoint answers again.
func (s *Session) tickLocked(ctx context.Context) {
	if s.stopped {
		return
	}
	if s.proc != nil && s.proc.exited() && s.state != StateExited {
		s.setStateLocked(StateExited)
		s.lastErr = s.proc.exitError()
		s.detachLocked()
		return
	}
	if s.state == StateExited {
		return
	}
	_, ok := probeCDP(ctx, s.Endpoint)
	if !ok {
		if s.state == StateRunning {
			s.setStateLocked(StateRestarting)
			s.detachLocked()
		}
		return
	}
	if s.browser == nil {
		b, err := engine.Connect(ctx, s.Endpoint, 30*time.Second)
		if err != nil {
			s.lastErr = err.Error()
			return
		}
		s.browser = b
		s.generation++
		s.lastErr = ""
		if s.state == StateRestarting {
			s.restarts = append(s.restarts, time.Now())
		}
		s.setStateLocked(StateRunning)
	}
	if s.main == nil && s.Inspector != "" {
		if m, err := ConnectMain(ctx, s.Inspector); err == nil {
			s.main = m
		}
	}
	s.ensureHubsLocked()
}

func (s *Session) setStateLocked(st State) {
	if s.state != st {
		s.state = st
		s.since = time.Now()
	}
}

// detachLocked drops the CDP and inspector connections (the app may be gone).
func (s *Session) detachLocked() {
	if s.browser != nil {
		s.browser.Close()
		s.browser = nil
	}
	if s.main != nil {
		s.main.Close()
		s.main = nil
	}
	s.hubbed = nil
}

func (s *Session) watch() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stopWatch:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.mu.Lock()
		s.ticks++
		// A healthy app is probed every third tick; a restarting one every
		// tick so it is picked up quickly.
		if s.state != StateRunning || s.ticks%3 == 0 || (s.proc != nil && s.proc.exited()) {
			s.tickLocked(ctx)
		}
		idle := s.idleAfter > 0 && !s.idleFired && !s.stopped && time.Since(s.lastUse) > s.idleAfter
		if idle {
			s.idleFired = true
		}
		if idle && s.ring != nil {
			s.ring.Add("mcp-electron", fmt.Sprintf("stopped after %s without any tool access (idle timeout, --idle-timeout)", s.idleAfter))
		}
		onIdle := s.onIdle
		s.mu.Unlock()
		cancel()
		if idle && onIdle != nil {
			go onIdle()
			return
		}
	}
}

// Logs merges renderer console messages, exceptions and failed requests of
// every window, the main process console, and the process output, oldest
// first.
func (s *Session) Logs() []LogEntry {
	var out []LogEntry
	s.mu.Lock()
	b, m, ring := s.browser, s.main, s.ring
	s.mu.Unlock()
	if ring != nil {
		out = append(out, ring.Entries()...)
	}
	if m != nil {
		out = append(out, m.Logs()...)
	}
	if b != nil {
		if wins, err := b.Windows(); err == nil {
			for _, w := range wins {
				page, err := b.Page(w.Index)
				if err != nil {
					continue
				}
				hub := engine.HubForPage(page)
				if hub == nil || hub.Observer() == nil {
					continue
				}
				for _, e := range hub.Observer().Drain(0) {
					if le, ok := logFromEvent(e, w.Index); ok {
						out = append(out, le)
					}
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func logFromEvent(e engine.ObserverEvent, window int) (LogEntry, bool) {
	t := time.UnixMilli(e.TS)
	switch e.Kind {
	case engine.KindConsole:
		return LogEntry{Time: t, Source: "console", Level: e.Level, Text: e.Text, Location: e.Source, Window: window}, true
	case engine.KindError:
		return LogEntry{Time: t, Source: "exception", Level: "error", Text: e.Text, Location: e.Source, Window: window}, true
	case engine.KindNet:
		if e.Failed == "" && e.Status < 400 {
			return LogEntry{}, false
		}
		text := fmt.Sprintf("%s %s → %d", e.Method, engine.ElideDataURL(e.URL), e.Status)
		if e.Failed != "" {
			text = fmt.Sprintf("%s %s failed: %s", e.Method, engine.ElideDataURL(e.URL), e.Failed)
		}
		return LogEntry{Time: t, Source: "network", Level: "error", Text: text, Window: window}, true
	case engine.KindPage:
		text := e.Event
		if e.URL != "" {
			text += " " + e.URL
		}
		return LogEntry{Time: t, Source: "page", Level: "info", Text: text, Window: window}, true
	}
	return LogEntry{}, false
}

// Manager holds the sessions of one server.
type Manager struct {
	root string
	// MaxSessions caps the open sessions (launched or attached); 0 means
	// DefaultMaxSessions. Every session is a whole Electron process tree.
	MaxSessions int
	// IdleTimeout stops a launched application nobody has used for that
	// long (tools, flows). 0 means DefaultIdleTimeout; negative disables.
	IdleTimeout time.Duration

	mu       sync.Mutex
	sessions map[string]*Session
	seq      int
}

const (
	DefaultMaxSessions = 3
	DefaultIdleTimeout = 30 * time.Minute
)

func (m *Manager) maxSessions() int {
	if m.MaxSessions > 0 {
		return m.MaxSessions
	}
	return DefaultMaxSessions
}

func (m *Manager) idleTimeout() time.Duration {
	switch {
	case m.IdleTimeout < 0:
		return 0
	case m.IdleTimeout == 0:
		return DefaultIdleTimeout
	}
	return m.IdleTimeout
}

// admit reserves a slot for a new session or explains which to stop.
func (m *Manager) admit() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) < m.maxSessions() {
		return nil
	}
	ids := make([]string, 0, len(m.sessions))
	for id, s := range m.sessions {
		ids = append(ids, fmt.Sprintf("%s (%s)", id, s.Name))
	}
	sort.Strings(ids)
	return &UsageError{fmt.Sprintf("%d sessions are already open (%s); each one is a whole Electron process tree, so stop one first (limit %d, --max-sessions)", len(ids), strings.Join(ids, ", "), m.maxSessions())}
}

// NewManager creates a manager whose artifacts live under root (created).
func NewManager(root string) (*Manager, error) {
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil || cache == "" {
			cache = os.TempDir()
		}
		root = filepath.Join(cache, "mcp-electron", "sessions")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	sweepStaleArtifacts(root, time.Now().Add(-StaleArtifactsAge))
	return &Manager{root: root, sessions: map[string]*Session{}}, nil
}

// Get returns a session by id; an empty id selects the only session.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" {
		if len(m.sessions) == 1 {
			for _, s := range m.sessions {
				s.Touch()
				return s, nil
			}
		}
		if len(m.sessions) == 0 {
			return nil, &UsageError{"no application session: call launch or attach first"}
		}
		return nil, &UsageError{"several sessions are open: pass session"}
	}
	s, ok := m.sessions[id]
	if !ok {
		return nil, &UsageError{fmt.Sprintf("unknown session %q", id)}
	}
	s.Touch()
	return s, nil
}

// List returns the open sessions.
func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) add(s *Session) (*Session, error) {
	m.mu.Lock()
	m.seq++
	s.ID = "s" + strconv.Itoa(m.seq)
	m.sessions[s.ID] = s
	m.mu.Unlock()
	s.Dir = filepath.Join(m.root, time.Now().Format("20060102-150405")+"-"+s.ID)
	s.Started = time.Now()
	s.since = s.Started
	s.lastUse = s.Started
	s.state = StateRunning
	s.stopWatch = make(chan struct{})
	if s.Owned {
		if idle := m.idleTimeout(); idle > 0 {
			s.idleAfter = idle
			id := s.ID
			s.onIdle = func() { _, _ = m.Stop(id) }
		}
	}
	if s.dialogs == nil {
		s.dialogs = &engine.DialogAutoPolicy{Accept: true}
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	go s.watch()
	return s, nil
}

const (
	defaultTargetStartup  = 60 * time.Second
	defaultCommandStartup = 180 * time.Second
)

// tempRoot is where private profiles live. macOS limits Unix socket paths to
// 104 bytes and apps put sockets in their profile, so /tmp beats $TMPDIR.
func tempRoot() string {
	if runtime.GOOS == "darwin" {
		return "/tmp"
	}
	return os.TempDir()
}

// Launch starts an application and attaches to it. On any failure, whatever
// was started is stopped before returning.
func (m *Manager) Launch(ctx context.Context, spec LaunchSpec) (*Session, error) {
	if len(spec.Command) > 0 {
		return m.launchCommand(ctx, spec)
	}
	if spec.Target == "" {
		return nil, &UsageError{"give target (an Electron project, a .app or an Electron executable) or command (a dev command)"}
	}
	t, err := Resolve(spec.Target)
	if err != nil {
		return nil, err
	}
	if err := m.admit(); err != nil {
		return nil, err
	}
	timeout := spec.StartupTimeout
	if timeout <= 0 {
		timeout = defaultTargetStartup
	}
	cdpPort, err := freePort()
	if err != nil {
		return nil, err
	}
	inspPort := 0
	if spec.Inspect {
		if inspPort, err = freePort(); err != nil {
			return nil, err
		}
	}
	s := &Session{Info: Info{Name: firstNonEmpty(spec.Name, t.Name), Mode: "target", Owned: true, Endpoint: loopbackEndpoint(cdpPort)}, ring: NewRing(3000)}
	args := []string{"--remote-debugging-port=" + strconv.Itoa(cdpPort)}
	if spec.Inspect {
		args = append(args, "--inspect=127.0.0.1:"+strconv.Itoa(inspPort))
		s.Inspector = loopbackEndpoint(inspPort)
	}
	marker := ""
	if spec.Isolate && !hasUserDataDir(spec.Args) {
		root, err := os.MkdirTemp(tempRoot(), "mcp-electron-")
		if err != nil {
			return nil, fmt.Errorf("create private profile: %w", err)
		}
		profile := filepath.Join(root, "profile")
		if err := os.Mkdir(profile, 0o700); err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("create private profile: %w", err)
		}
		s.root, s.Profile, marker = root, profile, root
		args = append(args, "--user-data-dir="+profile)
	}
	args = append(args, keepPaintingArgs...)
	args = append(args, t.Args...)
	args = append(args, spec.Args...)
	dir := spec.Dir
	if dir == "" {
		dir = t.Dir
	}
	if dir == "" {
		dir = filepath.Dir(t.Executable)
	}
	env := mergeEnv(spec.Env, cdpPort, inspPort)
	p, err := startOwned(t.Executable, args, env, dir, s.ring, marker)
	if err != nil {
		if s.root != "" {
			_ = os.RemoveAll(s.root)
		}
		return nil, err
	}
	s.proc, s.PID = p, p.pid
	if err := s.waitReady(ctx, timeout); err != nil {
		s.stop(false)
		return nil, err
	}
	if t.Packaged {
		s.Note = "packaged app: a production build may ignore --inspect (EnableNodeCliInspectArguments fuse); eval_main then stays unavailable"
	}
	return m.add(s)
}

func hasUserDataDir(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--user-data-dir") {
			return true
		}
	}
	return false
}

func (m *Manager) launchCommand(ctx context.Context, spec LaunchSpec) (*Session, error) {
	timeout := spec.StartupTimeout
	if timeout <= 0 {
		timeout = defaultCommandStartup
	}
	if err := m.admit(); err != nil {
		return nil, err
	}
	cdpPort, err := freePort()
	if err != nil {
		return nil, err
	}
	inspPort := 0
	if spec.Inspect {
		if inspPort, err = freePort(); err != nil {
			return nil, err
		}
	}
	argv := make([]string, len(spec.Command))
	for i, a := range spec.Command {
		argv[i] = expandPorts(a, cdpPort, inspPort)
	}
	extra := map[string]string{}
	for k, v := range spec.Env {
		extra[k] = expandPorts(v, cdpPort, inspPort)
	}
	exe, err := lookPath(argv[0], spec.Dir)
	if err != nil {
		return nil, &UsageError{fmt.Sprintf("command %q not found: %v", argv[0], err)}
	}
	s := &Session{Info: Info{Name: firstNonEmpty(spec.Name, filepath.Base(argv[0])+" "+strings.Join(argv[1:], " ")), Mode: "command", Owned: true, Endpoint: loopbackEndpoint(cdpPort)}, ring: NewRing(5000)}
	if spec.Inspect {
		s.Inspector = loopbackEndpoint(inspPort)
	}
	s.Note = fmt.Sprintf("dev command: the script must start electron with --remote-debugging-port=$%s%s; the session re-attaches automatically after every restart", EnvCDPPort, map[bool]string{true: " and --inspect=127.0.0.1:$" + EnvInspectPort, false: ""}[spec.Inspect])
	p, err := startOwned(exe, argv[1:], mergeEnv(extra, cdpPort, inspPort), spec.Dir, s.ring, "")
	if err != nil {
		return nil, err
	}
	s.proc, s.PID = p, p.pid
	if err := s.waitReady(ctx, timeout); err != nil {
		s.stop(false)
		return nil, err
	}
	return m.add(s)
}

func lookPath(name, dir string) (string, error) {
	if strings.Contains(name, string(filepath.Separator)) {
		if !filepath.IsAbs(name) && dir != "" {
			name = filepath.Join(dir, name)
		}
		if _, err := os.Stat(name); err != nil {
			return "", err
		}
		return name, nil
	}
	return execLookPath(name)
}

// waitReady polls the debugging endpoint until it answers, then connects
// and (when requested) reaches the inspector.
func (s *Session) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if s.proc.exited() {
			return fmt.Errorf("%s exited before opening its debugging port (%s)\noutput:\n%s", s.Name, s.proc.exitError(), s.ring.Tail(30))
		}
		if _, ok := probeCDP(ctx, s.Endpoint); ok {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not open its debugging port %s within %s\noutput:\n%s", s.Name, s.Endpoint, timeout, s.ring.Tail(30))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	b, err := engine.Connect(ctx, s.Endpoint, 30*time.Second)
	if err != nil {
		return fmt.Errorf("attach to %s: %w", s.Name, err)
	}
	s.browser = b
	s.generation = 1
	// The first window can take a moment after the port opens.
	winDeadline := time.Now().Add(min(timeout, 30*time.Second))
	for {
		if wins, err := b.Windows(); err == nil && len(wins) > 0 {
			break
		}
		if time.Now().After(winDeadline) || s.proc.exited() {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	s.waitFirstPaint(b, 4*time.Second)
	if s.Inspector != "" {
		ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
		for {
			if m, err := ConnectMain(ictx, s.Inspector); err == nil {
				s.main = m
				break
			}
			if ictx.Err() != nil {
				s.Note = strings.TrimSpace(s.Note + " The main process inspector did not answer at " + s.Inspector + "; eval_main is unavailable until it does.")
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		cancel()
	}
	return nil
}

// waitFirstPaint gives the renderer a bounded moment to mount its UI so the
// first snapshot is not an empty document: it returns as soon as the first
// window has a body with text or an interactive element, or after d.
func (s *Session) waitFirstPaint(b *engine.Browser, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		page, err := b.Page(0)
		if err != nil {
			return
		}
		res, err := page.Timeout(time.Second).Eval(`() => document.readyState === "complete" && !!document.body && (document.body.innerText.trim().length > 0 || !!document.querySelector("button,a,input,textarea,select,[role],[contenteditable]"))`)
		if err == nil && res.Value.Bool() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Attach connects to a running application without owning it.
func (m *Manager) Attach(ctx context.Context, spec AttachSpec) (*Session, error) {
	if spec.Endpoint == "" {
		return nil, &UsageError{"give endpoint (http://127.0.0.1:PORT of an Electron app started with --remote-debugging-port)"}
	}
	if err := m.admit(); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(spec.Endpoint, "/")
	if !strings.Contains(endpoint, "://") {
		if p, err := strconv.Atoi(endpoint); err == nil {
			endpoint = loopbackEndpoint(p)
		} else {
			endpoint = "http://" + endpoint
		}
	}
	v, ok := probeCDP(ctx, endpoint)
	if !ok {
		if v.Browser != "" {
			return nil, &UsageError{fmt.Sprintf("%s is a Node inspector (%s), not a remote debugging endpoint: pass it as inspector_endpoint", endpoint, v.Browser)}
		}
		return nil, fmt.Errorf("no remote debugging endpoint answers at %s", endpoint)
	}
	b, err := engine.Connect(ctx, endpoint, 30*time.Second)
	if err != nil {
		return nil, err
	}
	s := &Session{Info: Info{Name: firstNonEmpty(spec.Name, v.Browser), Mode: "attach", Endpoint: endpoint}, browser: b, generation: 1}
	if spec.Inspector != "" {
		insp := strings.TrimRight(spec.Inspector, "/")
		if !strings.Contains(insp, "://") {
			if p, err := strconv.Atoi(insp); err == nil {
				insp = loopbackEndpoint(p)
			} else {
				insp = "http://" + insp
			}
		}
		s.Inspector = insp
		if mc, err := ConnectMain(ctx, insp); err == nil {
			s.main = mc
		} else {
			s.Note = err.Error()
		}
	}
	return m.add(s)
}

// StaleArtifactsAge is the age past which a session artifacts directory left
// by a server that died without stop is removed when a new server starts.
// Several servers may share the root (one per client), so a younger
// directory may still belong to a live session and is kept.
const StaleArtifactsAge = 24 * time.Hour

// sweepStaleArtifacts removes the session directories under root last
// modified before cutoff. Errors are ignored: the sweep is housekeeping and
// must never stop the server from starting.
func sweepStaleArtifacts(root string, cutoff time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// StopReport states what was released.
type StopReport struct {
	SessionID  string `json:"session_id"`
	Terminated bool   `json:"terminated"`
	// ArtifactsDir is the session's screenshots and flow outputs directory;
	// ArtifactsKept says it was left on disk at the caller's request.
	ArtifactsDir  string `json:"artifacts_dir,omitempty"`
	ArtifactsKept bool   `json:"artifacts_kept,omitempty"`
	// Survivors lists pids that could not be terminated.
	Survivors []int  `json:"survivors,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Stop closes a session; an owned application is terminated together with
// every process it spawned, and its private profile removed. The session's
// artifacts (screenshots, flow outputs) are removed too, launched or attached.
func (m *Manager) Stop(id string) (*StopReport, error) {
	return m.stopSession(id, false)
}

// StopKeepArtifacts is Stop leaving the artifacts directory on disk, for a
// caller that still needs its screenshots as evidence.
func (m *Manager) StopKeepArtifacts(id string) (*StopReport, error) {
	return m.stopSession(id, true)
}

func (m *Manager) stopSession(id string, keepArtifacts bool) (*StopReport, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.sessions, s.ID)
	m.mu.Unlock()
	return s.stop(keepArtifacts), nil
}

// Close stops every session (server shutdown).
func (m *Manager) Close() {
	for _, s := range m.List() {
		_, _ = m.Stop(s.ID)
	}
}

func (s *Session) stop(keepArtifacts bool) *StopReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := &StopReport{SessionID: s.ID}
	if s.stopped {
		rep.Note = "already stopped"
		return rep
	}
	s.stopped = true
	s.setStateLocked(StateStopped)
	if s.stopWatch != nil {
		close(s.stopWatch)
	}
	s.detachLocked()
	defer s.releaseArtifactsLocked(rep, keepArtifacts)
	if !s.Owned {
		rep.Note = "attached session: detached, the application keeps running"
		return rep
	}
	if s.proc != nil {
		rep.Survivors = s.proc.terminate()
	}
	rep.Terminated = len(rep.Survivors) == 0
	if s.root != "" {
		var rmErr error
		for i := 0; i < 5; i++ {
			if rmErr = os.RemoveAll(s.root); rmErr == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if rmErr != nil {
			rep.Note = "private profile not removed: " + rmErr.Error()
		}
	}
	return rep
}

// releaseArtifactsLocked removes the session's artifacts directory unless
// keep is set; a failure is reported in the note, never hidden.
func (s *Session) releaseArtifactsLocked(rep *StopReport, keep bool) {
	if s.Dir == "" {
		return
	}
	rep.ArtifactsDir = s.Dir
	if keep {
		rep.ArtifactsKept = true
		return
	}
	var err error
	for i := 0; i < 5; i++ {
		if err = os.RemoveAll(s.Dir); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	msg := "artifacts not removed: " + err.Error()
	if rep.Note != "" {
		msg = rep.Note + "; " + msg
	}
	rep.Note = msg
}

// mergeEnv builds the child environment: this server's environment (the
// developer's shell, which toolchains depend on), minus ELECTRON_RUN_AS_NODE,
// plus the caller's variables and the chosen ports.
func mergeEnv(extra map[string]string, cdpPort, inspectPort int) []string {
	drop := map[string]bool{"ELECTRON_RUN_AS_NODE": true, EnvCDPPort: true, EnvInspectPort: true}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, override := extra[k]; !drop[k] && !override {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+extra[k])
	}
	out = append(out, EnvCDPPort+"="+strconv.Itoa(cdpPort))
	if inspectPort > 0 {
		out = append(out, EnvInspectPort+"="+strconv.Itoa(inspectPort))
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
