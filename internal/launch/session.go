package launch

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/GabrySpada/gitcito/internal/terminal"
)

// Emitter sends a named event to the window.
type Emitter interface {
	Emit(name string, data any)
}

// ─── Session execution (pty-backed, like the integrated terminal) ────────────

// backlogMax is enough to cover a compiler's banner; a watcher's endless ticks
// are not the point of a backlog, only the lines that arrived before anyone
// was looking. Counted in UTF-16 units, as the TS string was.
const backlogMax = 256 * 1024

// exitedGrace is how long an exited session stays around so a late attach
// still sees why.
const exitedGrace = 30 * time.Second

// proc is what a running launch session needs from its process.
type proc interface {
	Write(data string) error
	Resize(cols, rows int) error
	Kill()
	Signal(pause bool)
}

// spawnSpec is one launch to start: the shell and its command line.
type spawnSpec struct {
	Name string
	Args []string
	Dir  string
	// Env is the full environment: the process's own plus the config's.
	Env        []string
	Cols, Rows int
	OnData     func(string)
	OnExit     func(int)
}

// starter starts a launch process; the default tries a pty and falls back to
// pipes.
type starter func(spec spawnSpec) (proc, error)

func defaultStarter(spec spawnSpec) (proc, error) {
	ts := terminal.Spec{
		Name: spec.Name, Args: spec.Args, Dir: spec.Dir, Cols: spec.Cols, Rows: spec.Rows,
		// node-pty sets TERM to the terminal name, over whatever env says.
		Env:    gitx.MergeEnv(spec.Env, "TERM=xterm-256color"),
		OnData: spec.OnData, OnExit: spec.OnExit,
	}
	p, err := terminal.StartPTY(ts)
	if err == nil {
		return p, nil
	}
	// No pty: stream a plain child process (no colors, no resize).
	ts.Env = spec.Env
	ts.CRLF = true
	return terminal.StartPipe(ts)
}

// sink holds back what a program printed before its terminal attached.
//
// The pty starts the moment `launch:run` returns, but the renderer only
// subscribes once the terminal panel has mounted its xterm. Everything printed
// in between — for a fast program, its whole banner — used to be dropped on the
// floor. Output is buffered until the renderer attaches, and the exit code with
// it, so a program that finishes first still gets to explain itself. Live
// output is sent too, before and after attaching, exactly as TS did.
type sink struct {
	mu       sync.Mutex
	emit     Emitter
	dataEv   string
	exitEv   string
	backlog  string
	attached bool
	exitCode *int
}

func (s *sink) data(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.attached {
		b := s.backlog + text
		if len(b) > backlogMax { // bytes ≥ UTF-16 units, so only then can it be over
			b = shared.TailUTF16(b, backlogMax)
		}
		s.backlog = b
	}
	s.emit.Emit(s.dataEv, text)
}

func (s *sink) exit(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exitCode = &code
	s.emit.Emit(s.exitEv, code)
}

func (s *sink) attach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached {
		return
	}
	s.attached = true
	if s.backlog != "" {
		s.emit.Emit(s.dataEv, s.backlog)
	}
	if s.exitCode != nil {
		s.emit.Emit(s.exitEv, *s.exitCode)
	}
	s.backlog = ""
}

type session struct {
	sink *sink
	mu   sync.Mutex
	proc proc
}

func (s *session) withProc(f func(p proc)) {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		f(p)
	}
}

// Options configure a Manager. The zero value is production behaviour except
// OpenURL, which main.go must pass for serverReadyAction to open anything.
type Options struct {
	// OpenURL opens an http(s) URL in the system browser.
	OpenURL func(url string) error
	// start, h and grace are test seams.
	start starter
	h     *host
	grace time.Duration
}

// Manager owns the launch sessions.
type Manager struct {
	emit  Emitter
	open  func(string)
	start starter
	h     host
	grace time.Duration

	mu       sync.Mutex
	nextID   int
	sessions map[int]*session
}

// NewManager builds a launch manager that emits on e.
func NewManager(e Emitter, opts Options) *Manager {
	m := &Manager{emit: e, start: opts.start, grace: opts.grace, nextID: 1, sessions: map[int]*session{}}
	if m.start == nil {
		m.start = defaultStarter
	}
	if opts.h != nil {
		m.h = *opts.h
	} else {
		m.h = defaultHost()
	}
	if m.grace == 0 {
		m.grace = exitedGrace
	}
	if opts.OpenURL != nil {
		m.open = func(u string) { _ = opts.OpenURL(u) }
	}
	return m
}

func (m *Manager) defaultShell() string {
	if m.h.windows() {
		if s := m.h.getenv("COMSPEC"); s != "" {
			return s
		}
		return "powershell.exe"
	}
	if s := m.h.getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/zsh"
}

// spawnSession runs commandLine through a login shell so output renders with
// colors exactly like VS Code's debug terminal. The command line already
// `exec`s into the program (after any preLaunchTask) so the pty's pid IS the
// program — making pause/resume/stop signals hit it directly rather than an
// intermediate shell.
func (m *Manager) spawnSession(cwd, commandLine, display string, env map[string]string, cols, rows int,
	tap func(string), id int) *session {
	sk := &sink{
		emit:   m.emit,
		dataEv: "launch:data:" + strconv.Itoa(id),
		exitEv: "launch:exit:" + strconv.Itoa(id),
	}
	s := &session{sink: sk}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	args := []string{"-lic", commandLine}
	if m.h.windows() {
		args = []string{"-NoLogo", "-Command", commandLine}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	overrides := make([]string, 0, len(keys))
	for _, k := range keys {
		overrides = append(overrides, k+"="+env[k])
	}
	// The banner goes out before the process can print anything.
	sk.data("\x1b[90m> " + display + "\x1b[0m\r\n")
	p, err := m.start(spawnSpec{
		Name: m.defaultShell(), Args: args, Dir: cwd, Cols: cols, Rows: rows,
		Env: gitx.MergeEnv(os.Environ(), overrides...),
		OnData: func(d string) {
			if tap != nil {
				tap(d)
			}
			sk.data(d)
		},
		OnExit: func(code int) {
			sk.exit(code)
			// Keep the session (and its backlog) briefly: a terminal that mounts
			// after a fast failure must still be able to show it.
			time.AfterFunc(m.grace, func() { m.remove(id, s) })
		},
	})
	if err != nil {
		// Even the pipe fallback could not start the shell: say so in the
		// terminal and end the session, rather than leaving it silent.
		sk.data("\x1b[31m" + err.Error() + "\x1b[0m\r\n")
		sk.exit(1)
		time.AfterFunc(m.grace, func() { m.remove(id, s) })
		return s
	}
	s.mu.Lock()
	s.proc = p
	s.mu.Unlock()
	return s
}

func (m *Manager) remove(id int, s *session) {
	m.mu.Lock()
	if m.sessions[id] == s {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
}

func (m *Manager) get(id int) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *Manager) newID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	return id
}

func orDefault(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

// Discover is launch:discover.
func (m *Manager) Discover(repoPath string) []LaunchGroup { return DiscoverLaunch(repoPath) }

// Run starts a launch config (its preLaunchTask chain, then the program) in
// its own session.
func (m *Manager) Run(p RunPayload) RunResult {
	h := m.h
	dir := p.Dir
	values := p.InputValues
	if values == nil {
		values = map[string]string{}
	}
	// Substitute the user's `${input:id}` answers, then apply platform overrides.
	config := h.applyPlatform(resolveConfig(p.Config, values))
	if config == nil {
		config = LaunchConfig{}
	}
	tasks := resolveList(p.Tasks, values)
	siblings := p.Configs
	if siblings == nil {
		siblings = []LaunchConfig{config}
	}
	siblings = resolveList(siblings, values)
	folder := dir
	if truthy(config["cwd"]) {
		folder = h.subAll(config["cwd"], dir)
	}

	// Bail clearly on configs that reference editor-only variables we can't
	// resolve headlessly (interactive ${input:…}, ${command:…}, ${file…}…),
	// rather than running a half-substituted command line.
	check := []any{config["program"], config["cwd"]}
	if c, ok := config["command"].(string); ok {
		check = append(check, c)
	}
	if a, ok := list(config["args"]); ok {
		check = append(check, a...)
	}
	if a, ok := list(config["runtimeArgs"]); ok {
		check = append(check, a...)
	}
	name := "undefined"
	if v, has := config["name"]; has {
		name = jsString(v)
	}
	if hasUnresolvableVars(check...) {
		return RunResult{Error: `"` + name + `" uses a VS Code variable Gitcito can't resolve outside the editor (e.g. ${input:…}, ${command:…} or ${file}). Run it from VS Code instead.`}
	}

	env := map[string]string{}
	parts := h.configSegments(config, siblings, tasks, dir, env, map[string]bool{}, p.SkipTasks)
	if len(parts) == 0 {
		typ := "?"
		if config["type"] != nil {
			typ = jsString(config["type"])
		}
		return RunResult{Error: `"` + name + `" isn't directly runnable from Gitcito (type "` + typ + `"). It likely needs a debugger we can't launch.`}
	}

	// Run preLaunchTask(s) (and their dependsOn chain) first, then the program.
	// The *last* segment is `exec`'d (on unix) so the pty's pid becomes the
	// program itself and pause/resume/stop signals hit it directly. We skip
	// exec when the final segment is a subshell `( … )`, since `exec` only
	// takes a simple command.
	display := strings.Join(parts, " && ")
	if !h.windows() && !strings.HasPrefix(parts[len(parts)-1], "(") {
		parts[len(parts)-1] = "exec " + parts[len(parts)-1]
	}
	commandLine := strings.Join(parts, " && ")

	id := m.newID()
	// Two watchers on the same stream: the config's own serverReadyAction, and
	// the DevTools address Flutter announces on its own.
	ready := h.serverReadyTap(config, folder, m.open)
	devEv := "launch:devtools:" + strconv.Itoa(id)
	devtools := devToolsTap(func(f DevToolsFound) { m.emit.Emit(devEv, f) })
	tap := func(chunk string) {
		if ready != nil {
			ready(chunk)
		}
		devtools(chunk)
	}
	m.spawnSession(folder, commandLine, display, env, orDefault(p.Cols, 80), orDefault(p.Rows, 24), tap, id)
	return RunResult{ID: id}
}

// RunTasks runs a plain task chain (no program) in its own session — the
// up-front, run-once pane a compound uses for tasks shared by several members.
// Exit code is the chain's own (`&&`), so the renderer can gate the member
// launches on 0.
func (m *Manager) RunTasks(p RunTasksPayload) RunResult {
	values := p.InputValues
	if values == nil {
		values = map[string]string{}
	}
	tasks := resolveList(p.Tasks, values)
	seen := map[string]bool{}
	var parts []string
	for _, label := range p.Labels {
		parts = append(parts, m.h.resolveTaskCommands(label, tasks, p.Dir, seen)...)
	}
	if len(parts) == 0 {
		return RunResult{Error: "No runnable tasks among: " + strings.Join(p.Labels, ", ")}
	}
	commandLine := strings.Join(parts, " && ")
	id := m.newID()
	m.spawnSession(p.Dir, commandLine, commandLine, map[string]string{}, orDefault(p.Cols, 80), orDefault(p.Rows, 24), nil, id)
	return RunResult{ID: id}
}

// Attach is "my terminal is listening now": the backlog is replayed.
func (m *Manager) Attach(id int) {
	if s := m.get(id); s != nil {
		s.sink.attach()
	}
}

// Input writes to a session (errors of a dying session are dropped).
func (m *Manager) Input(id int, data string) {
	if s := m.get(id); s != nil {
		s.withProc(func(p proc) { _ = p.Write(data) })
	}
}

// Resize resizes a session. Sessions stay in the map for the exit grace so a
// late-mounting terminal can still read the backlog, and xterm keeps firing
// resize in that window; a dead pty's error is dropped.
func (m *Manager) Resize(id, cols, rows int) {
	if s := m.get(id); s != nil {
		s.withProc(func(p proc) { _ = p.Resize(cols, rows) })
	}
}

// Signal pauses ("pause") or resumes (anything else) a session.
func (m *Manager) Signal(id int, action string) {
	if s := m.get(id); s != nil {
		s.withProc(func(p proc) { p.Signal(action == "pause") })
	}
}

// Stop kills a session and forgets it.
func (m *Manager) Stop(id int) {
	s := m.get(id)
	if s == nil {
		return
	}
	s.withProc(func(p proc) { p.Kill() })
	m.remove(id, s)
}

// StopAll kills every session (app shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]int, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}
