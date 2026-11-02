package gitx

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// fixPathTimeout bounds each login-shell probe.
const fixPathTimeout = 5 * time.Second

// Unique markers so PATH can be extracted even if rc files print banners.
const (
	pathBegin = "__GITCITO_PATH_BEGIN__"
	pathEnd   = "__GITCITO_PATH_END__"
)

var pathMarkerRe = regexp.MustCompile(pathBegin + `(` + jsDot + `*)` + pathEnd)

var fixPathState struct {
	mu   sync.Mutex
	done bool
}

// FixPath restores the user's login-shell PATH into this process's
// environment, once.
//
// macOS/Linux GUI apps launched from Finder/Dock/Spotlight (not a terminal)
// inherit a minimal PATH — typically just `/usr/bin:/bin:/usr/sbin:/sbin`.
// That breaks anything relying on tools installed via nvm / Homebrew / asdf,
// most visibly git hooks (husky → `npm`/`node`) which then fail with
// `command not found` (exit 127).
//
// Fix: at startup, before any git spawn, ask the user's *login* shell for its
// real PATH and merge it into the process PATH, so every child process (git,
// and therefore its hooks) sees the same PATH the user gets in their terminal.
// Best-effort: never fails. Idempotent and safe to call concurrently: the
// first call does the work, later calls return at once. No-op on Windows,
// whose GUI apps already inherit the full system/user PATH.
func FixPath(ctx context.Context) {
	fixPathState.mu.Lock()
	defer fixPathState.mu.Unlock()
	if fixPathState.done {
		return
	}
	fixPathState.done = true
	if runtime.GOOS == "windows" {
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	if merged, ok := loginShellPath(ctx, shell, os.Getenv("PATH")); ok {
		_ = os.Setenv("PATH", merged)
	}
}

// loginShellPath asks shell for its PATH and merges it in front of current.
func loginShellPath(ctx context.Context, shell, current string) (string, bool) {
	// `${PATH}` must be brace-wrapped — `$PATH__GITCITO…` would glue the marker
	// into the variable name, yielding no PATH at all.
	script := `echo "` + pathBegin + `${PATH}` + pathEnd + `"`

	ask := func(args ...string) string {
		res, err := Exec(ctx, "", shell, append(args, script), RunOpts{PlainEnv: true, Timeout: fixPathTimeout})
		if err != nil {
			// Non-zero exit, timeout, missing shell: like execFileSync, any
			// failure discards the output, marker or not.
			return ""
		}
		// A login shell with no tty can emit job-control noise on stderr;
		// it is captured and dropped.
		m := pathMarkerRe.FindStringSubmatch(res.Stdout)
		if m == nil {
			return ""
		}
		return TrimJS(m[1])
	}

	// `-ilc`: interactive login shell so ~/.zprofile, ~/.zshrc, nvm, etc.
	// load. Interactive rc files can hang without a tty (prompt frameworks,
	// compinit), so fall back to a plain login shell rather than losing PATH.
	resolved := ask("-ilc")
	if resolved == "" {
		resolved = ask("-lc")
	}
	if resolved == "" {
		return "", false
	}
	return mergePath(resolved, current), true
}

// mergePath puts the shell PATH first (its tools win), then anything already
// there, de-duplicated so the variable doesn't grow unbounded across launches.
func mergePath(resolved, current string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range append(strings.Split(resolved, ":"), strings.Split(current, ":")...) {
		p = TrimJS(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return strings.Join(out, ":")
}
