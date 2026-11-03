// Package localci ports src/main/localCi.ts: run the repository's GitHub
// Actions workflows on this machine via nektos/act — deliberately an *optional
// integration*, not a bundled runtime. Gitcito only orchestrates: it detects
// act and Docker, lists workflow files, spawns `act` and streams its output.
// Installing the tools is the user's explicit choice, guided by the UI.
package localci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Emitter sends a named event to the window.
type Emitter interface {
	Emit(name string, data any)
}

// LocalCiStatus is what the local-CI runner found on this machine.
type LocalCiStatus struct {
	// Act is `act --version` output, or null when the binary is missing.
	Act *string `json:"act"`
	// Docker: daemon reachable (act cannot run without it).
	Docker bool `json:"docker"`
}

// LocalCiWorkflow is one file under .github/workflows.
type LocalCiWorkflow struct {
	File string `json:"file"`
	Name string `json:"name"`
}

// LocalCiVerdict is a recorded local-CI result for one commit (a git note).
type LocalCiVerdict struct {
	OK       bool   `json:"ok"`
	Workflow string `json:"workflow"`
	At       int64  `json:"at"` // unix ms
}

// SweepEntry is one commit of a sweep.
type SweepEntry struct {
	SHA  string `json:"sha"`
	Exit *int   `json:"exit"`
	OK   bool   `json:"ok"`
}

// LocalCiSweepResult is the outcome of a "test this range" sweep.
type LocalCiSweepResult struct {
	Results []SweepEntry `json:"results"`
	// Aborted is true when cancel stopped the loop before every commit ran.
	Aborted bool `json:"aborted"`
}

// RecordResult is localci:record's answer.
type RecordResult struct {
	Recorded bool   `json:"recorded"`
	SHA      string `json:"sha"`
}

// RunAtResult is localci:runAt's answer.
type RunAtResult struct {
	SHA      string `json:"sha"`
	Exit     *int   `json:"exit"`
	OK       bool   `json:"ok"`
	Recorded bool   `json:"recorded"`
}

// RangeResult is localci:resolveRange's answer.
type RangeResult struct {
	Total    int               `json:"total"`
	SHAs     []string          `json:"shas"`
	Subjects map[string]string `json:"subjects"`
}

// dataEvent is the `localci:data` payload, keyed by the repo.
type dataEvent struct {
	RepoPath string `json:"repoPath"`
	SHA      string `json:"sha,omitempty"`
	Chunk    string `json:"chunk"`
}

type sweepStart struct {
	RepoPath string `json:"repoPath"`
	SHA      string `json:"sha"`
	Index    int    `json:"index"`
	Total    int    `json:"total"`
	Phase    string `json:"phase"`
}

type sweepDone struct {
	RepoPath string `json:"repoPath"`
	SHA      string `json:"sha"`
	Index    int    `json:"index"`
	Total    int    `json:"total"`
	Phase    string `json:"phase"`
	OK       bool   `json:"ok"`
	Exit     *int   `json:"exit"`
}

// actBin is overridable so tests (and users with act outside PATH) can point
// at a different runner binary. Tests substitute a tiny script for the real act.
func actBin() string {
	if b := os.Getenv("GITCITO_ACT_BIN"); b != "" {
		return b
	}
	return "act"
}

// killGrace is how long act gets to stop its containers after SIGTERM.
const killGrace = 5 * time.Second

type child struct {
	pid  int
	done chan struct{}
}

func (c *child) kill() {
	select {
	case <-c.done:
		return
	default:
	}
	termGroup(c.pid)
	go func() {
		select {
		case <-c.done:
		case <-time.After(killGrace):
			killGroup(c.pid)
		}
	}()
}

type sweepState struct {
	mu    sync.Mutex
	abort bool
}

func (s *sweepState) aborted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abort
}

// Manager runs act: one run per repo at a time (a second Run press means
// "restart"), plus the active range sweeps, so cancel can stop the loop, not
// just the current child.
type Manager struct {
	emit Emitter
	now  func() time.Time

	mu       sync.Mutex
	running  map[string]*child
	sweeping map[string]*sweepState
}

// NewManager builds an idle Manager.
func NewManager(e Emitter) *Manager {
	return &Manager{emit: e, now: time.Now, running: map[string]*child{}, sweeping: map[string]*sweepState{}}
}

func git(ctx context.Context, repoPath string, args ...string) (string, error) {
	res, err := gitx.Run(ctx, repoPath, args, gitx.RunOpts{PlainEnv: true})
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

// Status reports whether act and a reachable Docker daemon are present.
func (m *Manager) Status(ctx context.Context) LocalCiStatus {
	var st LocalCiStatus
	if res, err := gitx.Exec(ctx, "", actBin(), []string{"--version"}, gitx.RunOpts{PlainEnv: true}); err == nil {
		v := gitx.TrimJS(res.Stdout)
		st.Act = &v
	}
	// `docker info` answers only when the daemon is actually reachable — the
	// binary existing is not enough for act to work.
	_, err := gitx.Exec(ctx, "", "docker", []string{"info", "--format", "{{.ServerVersion}}"}, gitx.RunOpts{PlainEnv: true})
	st.Docker = err == nil
	return st
}

var reWorkflowFile = regexp.MustCompile(`\.ya?ml$`)

// Workflows lists .github/workflows/*.y(a)ml with their names, the filename
// standing in for a missing `name:`.
func (m *Manager) Workflows(repoPath string) []LocalCiWorkflow {
	dir := filepath.Join(repoPath, ".github", "workflows")
	out := []LocalCiWorkflow{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.SliceStable(names, func(i, j int) bool { return shared.CompareUTF16(names[i], names[j]) < 0 })
	for _, file := range names {
		if !reWorkflowFile.MatchString(file) {
			continue
		}
		text := ""
		if b, err := os.ReadFile(filepath.Join(dir, file)); err == nil {
			text = strings.ToValidUTF8(string(b), "�")
		}
		name, ok := shared.ParseWorkflowName(text)
		if !ok {
			name = file
		}
		out = append(out, LocalCiWorkflow{File: file, Name: name})
	}
	return out
}

// Cancel stops the repo's sweep loop and kills the act run in flight.
func (m *Manager) Cancel(repoPath string) {
	m.mu.Lock()
	s := m.sweeping[repoPath]
	c := m.running[repoPath]
	delete(m.running, repoPath)
	m.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		s.abort = true
		s.mu.Unlock()
	}
	if c != nil {
		c.kill()
	}
}

// assertWorkflowFilename keeps the path inside .github/workflows: the name
// comes from our own listing, but the channel takes any string.
func assertWorkflowFilename(workflowFile string) error {
	if strings.Contains(workflowFile, "/") || strings.Contains(workflowFile, "..") {
		return errors.New("Not a workflow filename: " + workflowFile)
	}
	return nil
}

// spawnAct runs act in cwd, streaming output over `localci:data` keyed by the
// repo. The exit code is nil when a signal ended it (Node's `close` code null).
func (m *Manager) spawnAct(ctx context.Context, cwd, repoPath, workflowFile, sha string) (*int, error) {
	bin := actBin()
	cmd := exec.Command(bin, "--workflows", filepath.Join(".github", "workflows", workflowFile), "--rm")
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	configureGroup(cmd)
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	// One pipe for stdout and stderr keeps their relative order.
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, errors.New("spawn " + bin + " ENOENT")
		}
		return nil, errors.New("spawn " + bin + " " + err.Error())
	}
	_ = w.Close()
	c := &child{pid: cmd.Process.Pid, done: make(chan struct{})}
	m.mu.Lock()
	m.running[repoPath] = c
	m.mu.Unlock()
	stop := context.AfterFunc(ctx, c.kill)
	defer stop()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var dec shared.UTF8Stream
		buf := make([]byte, 32*1024)
		emit := func(s string) {
			if s != "" {
				m.emit.Emit("localci:data", dataEvent{RepoPath: repoPath, SHA: sha, Chunk: s})
			}
		}
		for {
			n, err := r.Read(buf)
			if n > 0 {
				emit(dec.Decode(buf[:n]))
			}
			if err != nil {
				break
			}
		}
		emit(dec.Flush())
	}()
	_ = cmd.Wait()
	close(c.done)
	// A container helper that inherited the pipe must not hold the run open.
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		_ = r.Close()
		<-readerDone
	}
	_ = r.Close()
	m.mu.Lock()
	if m.running[repoPath] == c {
		delete(m.running, repoPath)
	}
	m.mu.Unlock()
	if signaled(cmd.ProcessState) {
		return nil, nil
	}
	code := cmd.ProcessState.ExitCode()
	return &code, nil
}

// Run spawns act for one workflow file and streams its output. It returns
// the exit code (nil when killed). The workflow name is taken from our own
// listing, so the path never leaves `.github/workflows/`.
func (m *Manager) Run(ctx context.Context, repoPath, workflowFile string) (*int, error) {
	if err := assertWorkflowFilename(workflowFile); err != nil {
		return nil, err
	}
	m.Cancel(repoPath)
	m.mu.Lock()
	delete(m.sweeping, repoPath)
	m.mu.Unlock()
	return m.spawnAct(ctx, repoPath, repoPath, workflowFile, "")
}

// RunAt runs one workflow against a commit you are NOT on: the commit is
// checked out detached into a throwaway worktree under the OS temp dir, act
// runs there, and the verdict pins to that sha. The main working tree is never
// touched, and the worktree is removed however the run ends.
func (m *Manager) RunAt(ctx context.Context, repoPath, workflowFile, sha string) (RunAtResult, error) {
	if err := assertWorkflowFilename(workflowFile); err != nil {
		return RunAtResult{}, err
	}
	out, err := git(ctx, repoPath, "rev-parse", "--verify", sha+"^{commit}")
	if err != nil {
		return RunAtResult{}, err
	}
	full := gitx.TrimJS(out)
	dir, err := os.MkdirTemp("", "gitcito-ci-")
	if err != nil {
		return RunAtResult{}, err
	}
	// Bypasses gitsvc's repo lock on purpose, like the rest of this module: a
	// CI run can hold the repo for minutes, and the worktree add/remove pair
	// only touches worktree metadata.
	if _, err := git(ctx, repoPath, "worktree", "add", "--detach", dir, full); err != nil {
		_ = os.RemoveAll(dir)
		return RunAtResult{}, err
	}
	defer func() {
		// Cleanup must run even when the caller's context is gone.
		bg := context.WithoutCancel(ctx)
		_, _ = git(bg, repoPath, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
	}()
	exit, err := m.spawnAct(ctx, dir, repoPath, workflowFile, full)
	if err != nil {
		return RunAtResult{}, err
	}
	res := RunAtResult{SHA: full, Exit: exit, OK: exit != nil && *exit == 0}
	if exit != nil {
		// The worktree is a pristine checkout of `full` by construction, so the
		// clean-tree honesty rule holds without re-checking.
		if err := m.recordAt(ctx, repoPath, workflowFile, res.OK, full); err != nil {
			return RunAtResult{}, err
		}
		res.Recorded = true
	}
	return res, nil
}

var reRangeLine = shared.MustJSRegexp(`^([0-9a-f]{40}) (.*)$`, false)

// ResolveRange resolves a revision or range spec to the commits a sweep would
// run, newest first: the total the spec matches plus the capped head of the
// list, so the UI can state the real cost before anything runs.
func (m *Manager) ResolveRange(ctx context.Context, repoPath, spec string, limit int) (RangeResult, error) {
	clean := gitx.TrimJS(spec)
	// A spec starting with "-" would be read as an option by rev-list.
	if clean == "" || strings.HasPrefix(clean, "-") {
		return RangeResult{}, errors.New("Not a revision spec: " + spec)
	}
	limitCap := max(1, min(limit, 50))
	countOut, err := git(ctx, repoPath, "rev-list", "--count", clean, "--")
	if err != nil {
		return RangeResult{}, err
	}
	total := parseIntPrefix(gitx.TrimJS(countOut))
	out, err := git(ctx, repoPath, "rev-list", "--max-count="+strconv.Itoa(limitCap), "--format=%H %s", "--no-commit-header", clean, "--")
	if err != nil {
		return RangeResult{}, err
	}
	res := RangeResult{Total: total, SHAs: []string{}, Subjects: map[string]string{}}
	for _, line := range shared.SplitNL(out) {
		mm := reRangeLine.FindStringSubmatch(gitx.TrimJS(line))
		if mm == nil {
			continue
		}
		res.SHAs = append(res.SHAs, mm[1])
		res.Subjects[mm[1]] = mm[2]
	}
	return res, nil
}

// parseIntPrefix is `parseInt(s, 10) || 0`.
func parseIntPrefix(s string) int {
	n, i := 0, 0
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		i++
	}
	if i == start {
		return 0
	}
	if neg {
		return -n
	}
	return n
}

// Sweep runs one workflow across several commits, sequentially — act plus
// Docker is heavy enough that parallel runs would fight for the machine.
// Progress lands on `localci:sweep-progress` per commit; Cancel aborts between
// commits and kills the one in flight.
func (m *Manager) Sweep(ctx context.Context, repoPath, workflowFile string, shas []string) (LocalCiSweepResult, error) {
	if err := assertWorkflowFilename(workflowFile); err != nil {
		return LocalCiSweepResult{}, err
	}
	m.Cancel(repoPath)
	state := &sweepState{}
	m.mu.Lock()
	m.sweeping[repoPath] = state
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.sweeping[repoPath] == state {
			delete(m.sweeping, repoPath)
		}
		m.mu.Unlock()
	}()
	results := []SweepEntry{}
	for i, sha := range shas {
		if state.aborted() {
			break
		}
		m.emit.Emit("localci:sweep-progress", sweepStart{RepoPath: repoPath, SHA: sha, Index: i, Total: len(shas), Phase: "start"})
		r, err := m.RunAt(ctx, repoPath, workflowFile, sha)
		if err != nil {
			r = RunAtResult{SHA: sha}
		}
		results = append(results, SweepEntry{SHA: r.SHA, Exit: r.Exit, OK: r.OK})
		m.emit.Emit("localci:sweep-progress", sweepDone{
			RepoPath: repoPath, SHA: r.SHA, Index: i, Total: len(shas), Phase: "done", OK: r.OK, Exit: r.Exit,
		})
	}
	return LocalCiSweepResult{Results: results, Aborted: state.aborted()}, nil
}

// noteJSON is `JSON.stringify({ ok, workflow, at })` — no HTML escaping.
func noteJSON(ok bool, workflow string, at int64) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct {
		OK       bool   `json:"ok"`
		Workflow string `json:"workflow"`
		At       int64  `json:"at"`
	}{ok, workflow, at})
	return strings.TrimSuffix(buf.String(), "\n")
}

// Record pins a finished run's verdict to the commit it tested, as a git note
// under refs/notes/gitcito-ci. Only recorded when the working tree is CLEAN — a
// dirty tree means the run tested something no commit contains, and a verdict
// pinned to HEAD would lie. Notes are local and never pushed by default.
func (m *Manager) Record(ctx context.Context, repoPath, workflowFile string, ok bool) (RecordResult, error) {
	status, err := git(ctx, repoPath, "status", "--porcelain")
	if err != nil {
		return RecordResult{}, err
	}
	head, err := git(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		return RecordResult{}, err
	}
	sha := gitx.TrimJS(head)
	if gitx.TrimJS(status) != "" {
		return RecordResult{Recorded: false, SHA: sha}, nil
	}
	if err := m.recordAt(ctx, repoPath, workflowFile, ok, sha); err != nil {
		return RecordResult{}, err
	}
	return RecordResult{Recorded: true, SHA: sha}, nil
}

// recordAt pins a verdict to an explicit sha — used by worktree runs, which are
// clean by construction.
func (m *Manager) recordAt(ctx context.Context, repoPath, workflowFile string, ok bool, sha string) error {
	note := noteJSON(ok, workflowFile, m.now().UnixMilli())
	_, err := git(ctx, repoPath, "notes", "--ref=gitcito-ci", "add", "-f", "-m", note, sha)
	return err
}

// Verdicts is every recorded verdict, keyed by commit sha — it feeds the
// graph's ✓/✗ badges.
func (m *Manager) Verdicts(ctx context.Context, repoPath string) map[string]LocalCiVerdict {
	raw, _ := git(ctx, repoPath, "notes", "--ref=gitcito-ci", "list")
	out := map[string]LocalCiVerdict{}
	for _, line := range shared.SplitNL(raw) {
		fields := strings.Split(gitx.TrimJS(line), " ")
		if len(fields) < 2 || fields[1] == "" {
			continue
		}
		sha := fields[1]
		body, _ := git(ctx, repoPath, "notes", "--ref=gitcito-ci", "show", sha)
		var parsed struct {
			OK       *bool `json:"ok"`
			Workflow any   `json:"workflow"`
			At       any   `json:"at"`
		}
		// An unreadable note is just skipped.
		if json.Unmarshal([]byte(body), &parsed) != nil || parsed.OK == nil {
			continue
		}
		v := LocalCiVerdict{OK: *parsed.OK}
		v.Workflow, _ = parsed.Workflow.(string)
		if at, ok := parsed.At.(float64); ok {
			v.At = int64(at)
		}
		out[sha] = v
	}
	return out
}

// CancelAll stops every run and sweep (app shutdown).
func (m *Manager) CancelAll() {
	m.mu.Lock()
	repos := map[string]bool{}
	for r := range m.running {
		repos[r] = true
	}
	for r := range m.sweeping {
		repos[r] = true
	}
	m.mu.Unlock()
	for r := range repos {
		m.Cancel(r)
	}
}
