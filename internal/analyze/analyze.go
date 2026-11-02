// Package analyze ports src/main/analyze.ts: Problems — the project's own
// analyzer output, normalised.
//
// Every analyzer worth running already prints machine-readable diagnostics, so
// this is an adapter table rather than a protocol: detect what the repo asks
// for from the files it carries, run those tools, parse them into one shape.
// No language server, no daemon, nothing running in the background.
//
// It runs **only when the user asks** — `tsc --noEmit` on a large repo is tens
// of seconds, and these commands execute the repository's own toolchain, which
// is not something to do behind someone's back on open.
package analyze

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

const (
	// maxProblems: past this the panel is a wall of text and the renderer
	// starts to hurt.
	maxProblems = 5000
	// analyzerTimeout: an analyzer that has not answered by now is not going
	// to be useful.
	analyzerTimeout = 120 * time.Second
	// gitTimeout: `git check-ignore` on a few thousand paths is one fast
	// process, not a sweep.
	gitTimeout = 20 * time.Second
)

// AnalyzeResult is the analyze:run result.
type AnalyzeResult struct {
	Problems []Problem `json:"problems"`
	// Ran lists the analyzers that ran, in the order they finished.
	Ran []string `json:"ran"`
	// Missing lists analyzers the project asks for but the machine could not run.
	Missing []string `json:"missing"`
	// Truncated is true when the list hit its cap.
	Truncated bool `json:"truncated"`
	// Ms is the sweep's wall-clock.
	Ms int64 `json:"ms"`
}

type analyzer struct {
	id      string
	detect  func(dir string) bool
	command func(dir string) (string, []string)
	parse   func(stdout, stderr, dir string) []Problem
}

// localBin: a project-local binary beats whatever is on PATH; npx is the last
// resort.
func localBin(dir, name string) (string, []string) {
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".cmd"
	}
	local := filepath.Join(dir, "node_modules", ".bin", exe)
	if exists(local) {
		return local, nil
	}
	if runtime.GOOS == "windows" {
		return "npx.cmd", []string{"--no-install", name}
	}
	return "npx", []string{"--no-install", name}
}

var reEslintConfig = regexp.MustCompile(`^(eslint\.config\.[cm]?[jt]s|\.eslintrc(\.[a-z]+)?)$`)

func hasEslintConfig(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if reEslintConfig.MatchString(e.Name()) {
			return true
		}
	}
	return false
}

func marker(names ...string) func(string) bool {
	return func(dir string) bool {
		for _, n := range names {
			if exists(filepath.Join(dir, n)) {
				return true
			}
		}
		return false
	}
}

var analyzers = []analyzer{
	{
		id:      "dart",
		detect:  marker("pubspec.yaml"),
		command: func(string) (string, []string) { return "dart", []string{"analyze", "--format=machine", "."} },
		parse:   func(stdout, stderr, dir string) []Problem { return ParseDartAnalyze(stdout+stderr, dir) },
	},
	{
		id:     "tsc",
		detect: marker("tsconfig.json"),
		command: func(dir string) (string, []string) {
			cmd, args := localBin(dir, "tsc")
			return cmd, append(args, "--noEmit", "--pretty", "false")
		},
		parse: func(stdout, stderr, dir string) []Problem { return ParseTsc(stdout+stderr, dir) },
	},
	{
		id:     "eslint",
		detect: hasEslintConfig,
		command: func(dir string) (string, []string) {
			cmd, args := localBin(dir, "eslint")
			return cmd, append(args, ".", "-f", "json", "--no-error-on-unmatched-pattern")
		},
		parse: func(stdout, _, dir string) []Problem { return ParseEslint(stdout, dir) },
	},
	{
		id:     "clippy",
		detect: marker("Cargo.toml"),
		command: func(string) (string, []string) {
			return "cargo", []string{"clippy", "--message-format=short", "--quiet"}
		},
		parse: func(_, stderr, dir string) []Problem { return ParseLineDiagnostics(stderr, dir, "clippy") },
	},
	{
		id:      "go vet",
		detect:  marker("go.mod"),
		command: func(string) (string, []string) { return "go", []string{"vet", "./..."} },
		parse:   func(_, stderr, dir string) []Problem { return ParseLineDiagnostics(stderr, dir, "go vet") },
	},
	{
		id:      "ruff",
		detect:  marker("ruff.toml", "pyproject.toml"),
		command: func(string) (string, []string) { return "ruff", []string{"check", "--output-format=json", "."} },
		parse:   func(stdout, _, dir string) []Problem { return ParseRuff(stdout, dir) },
	},
}

// ─── Where the projects are ──────────────────────────────────────────────────
// A repository is not always one project sitting at its own root: a Flutter app
// under mobile/, a web package under apps/web, a Rust crate under crates/. The
// markers are looked for a few levels down, not only at the top.

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "build": true, "dist": true, "out": true, "target": true,
	".dart_tool": true, "vendor": true, "Pods": true, ".next": true, ".venv": true, "venv": true,
	"__pycache__": true, "coverage": true,
}

const (
	// maxDepth: deep enough for an app under apps/web, shallow enough to stay
	// instant.
	maxDepth = 3
	// maxRoots: a monorepo with fifty packages must not spawn fifty compilers.
	maxRoots = 12
)

func walkRoots(dir string, depth int, acc *[]string) {
	if len(*acc) >= maxRoots {
		return
	}
	for _, a := range analyzers {
		if a.detect(dir) {
			*acc = append(*acc, dir)
			break
		}
	}
	if depth >= maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || skipDirs[e.Name()] {
			continue
		}
		walkRoots(filepath.Join(dir, e.Name()), depth+1, acc)
	}
}

type job struct {
	a   analyzer
	dir string
}

// analyzerJobs is one (analyzer, directory) pair per project to sweep.
//
// A nested project of the *same* kind is skipped when an ancestor already has
// that analyzer: a root `tsconfig.json` is how a TypeScript project says it
// covers what is beneath it, and running the compiler twice over the same files
// only produces the same diagnostics twice.
func analyzerJobs(repoPath string) []job {
	var roots []string
	walkRoots(repoPath, 0, &roots)
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i]) < len(roots[j]) })
	var jobs []job
	for _, a := range analyzers {
		var kept []string
		for _, dir := range roots {
			if !a.detect(dir) {
				continue
			}
			covered := false
			for _, k := range kept {
				if strings.HasPrefix(dir, k+string(filepath.Separator)) {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
			kept = append(kept, dir)
			jobs = append(jobs, job{a, dir})
		}
	}
	return jobs
}

// PlanEntry is one step of the sweep: an analyzer and the directory it runs in.
type PlanEntry struct {
	ID  string `json:"id"`
	Dir string `json:"dir"`
}

// AnalyzerPlan is the sweep Gitcito would run: one entry per analyzer per
// project directory.
func AnalyzerPlan(repoPath string) []PlanEntry {
	out := []PlanEntry{}
	for _, j := range analyzerJobs(repoPath) {
		out = append(out, PlanEntry{j.a.id, j.dir})
	}
	return out
}

// DetectAnalyzers lists which analyzers this repo asks for — the panel names
// them before running.
func DetectAnalyzers(repoPath string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, j := range AnalyzerPlan(repoPath) {
		if !seen[j.ID] {
			seen[j.ID] = true
			out = append(out, j.ID)
		}
	}
	return out
}

// DropIgnored drops diagnostics about files git ignores.
//
// A tool pointed at the project root will happily lint whatever it finds, and
// what it finds includes generated output — `.next/build/chunks`, a bundled
// `dist`, a vendored copy. Hundreds of complaints about machine-written code
// bury the handful about yours, and the repository has already stated which
// files it does not care about. Tracked files are never dropped, even when a
// pattern matches them: committing generated output is a choice, and `git
// check-ignore` respects it.
func DropIgnored(ctx context.Context, repoPath string, problems []Problem) []Problem {
	// Nothing in a dependency tree is ever the user's problem, ignored or not.
	candidates := []Problem{}
	for _, p := range problems {
		inDeps := false
		for _, seg := range strings.Split(p.File, "/") {
			if seg == "node_modules" {
				inDeps = true
				break
			}
		}
		if !inDeps {
			candidates = append(candidates, p)
		}
	}
	var files []string
	seenFile := map[string]bool{}
	for _, p := range candidates {
		if !seenFile[p.File] {
			seenFile[p.File] = true
			files = append(files, p.File)
		}
	}
	if len(files) == 0 {
		return candidates
	}
	res, err := gitx.Exec(ctx, repoPath, "git", []string{"check-ignore", "--stdin", "-z"}, gitx.RunOpts{
		PlainEnv: true, Timeout: gitTimeout, MaxBuffer: 16 * 1024 * 1024, Stdin: []byte(strings.Join(files, "\x00")),
	})
	// Exit 1 simply means "none of them are ignored"; anything worse (no git,
	// not a repo) means we cannot tell, and we keep everything.
	if err != nil {
		if code, ok := gitx.ExitCode(err); !ok || (code != 0 && code != 1) {
			return candidates
		}
	}
	ignored := map[string]bool{}
	for _, f := range strings.Split(res.Stdout, "\x00") {
		if f != "" {
			ignored[f] = true
		}
	}
	if len(ignored) == 0 {
		return candidates
	}
	out := []Problem{}
	for _, p := range candidates {
		if !ignored[p.File] {
			out = append(out, p)
		}
	}
	return out
}

// runTool runs one analyzer; ok is false only when it could not start. A
// non-zero exit is the *normal* outcome here: every one of these tools exits
// non-zero precisely when it found something.
func runTool(ctx context.Context, dir, cmd string, args []string) (stdout, stderr string, ok bool) {
	res, err := gitx.Exec(ctx, dir, cmd, args, gitx.RunOpts{PlainEnv: true, Timeout: analyzerTimeout, MaxBuffer: 32 * 1024 * 1024})
	var ee *gitx.ExecError
	if errors.As(err, &ee) && !ee.Started() &&
		(errors.Is(ee.Err, exec.ErrNotFound) || errors.Is(ee.Err, fs.ErrNotExist) || errors.Is(ee.Err, fs.ErrPermission)) {
		return "", "", false
	}
	return strings.ToValidUTF8(res.Stdout, "�"), strings.ToValidUTF8(res.Stderr, "�"), true
}

// Runner owns the in-flight sweeps, so a second click cancels rather than
// piling up.
type Runner struct {
	mu      sync.Mutex
	running map[string]*sweep
}

type sweep struct{ cancel context.CancelFunc }

// NewRunner builds an idle Runner.
func NewRunner() *Runner { return &Runner{running: map[string]*sweep{}} }

// Analyze runs every analyzer the repo asks for, in parallel, and folds the
// results into one sorted list.
func (r *Runner) Analyze(ctx context.Context, repoPath string) AnalyzeResult {
	started := time.Now()
	r.Cancel(repoPath)
	toolCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	me := &sweep{cancel: cancel}
	r.mu.Lock()
	r.running[repoPath] = me
	r.mu.Unlock()

	jobs := analyzerJobs(repoPath)
	var mu sync.Mutex
	ran, missing, collected := []string{}, []string{}, []Problem{}
	add := func(list *[]string, id string) {
		for _, x := range *list {
			if x == id {
				return
			}
		}
		*list = append(*list, id)
	}
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd, args := j.a.command(j.dir)
			// Each tool runs in its own project directory — that is where its
			// config lives and what its relative paths are measured from.
			stdout, stderr, ok := runTool(toolCtx, j.dir, cmd, args)
			mu.Lock()
			defer mu.Unlock()
			if !ok {
				add(&missing, j.a.id)
				return
			}
			add(&ran, j.a.id)
			// Parsed against the project, then re-anchored to the repository
			// root so the panel groups a monorepo's files under one tree.
			for _, p := range j.a.parse(stdout, stderr, j.dir) {
				p.File = rel(repoPath, resolve(j.dir, p.File))
				collected = append(collected, p)
			}
		}()
	}
	wg.Wait()
	r.mu.Lock()
	if r.running[repoPath] == me {
		delete(r.running, repoPath)
	}
	r.mu.Unlock()

	problems := NormaliseProblems(DropIgnored(ctx, repoPath, collected))
	truncated := len(problems) > maxProblems
	if truncated {
		problems = problems[:maxProblems]
	}
	return AnalyzeResult{Problems: problems, Ran: ran, Missing: missing, Truncated: truncated, Ms: time.Since(started).Milliseconds()}
}

// Cancel kills the repo's running analyzers (their process groups). The sweep
// still returns whatever they printed, as TS did.
func (r *Runner) Cancel(repoPath string) {
	r.mu.Lock()
	s := r.running[repoPath]
	delete(r.running, repoPath)
	r.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

// CancelAll stops every sweep (app shutdown).
func (r *Runner) CancelAll() {
	r.mu.Lock()
	all := r.running
	r.running = map[string]*sweep{}
	r.mu.Unlock()
	for _, s := range all {
		s.cancel()
	}
}
