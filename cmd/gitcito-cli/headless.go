package main

// The headless half of the `gitcito` command — src/main/cliEntry.ts.
//
// The contract with a caller is git's contract: text on stdout, diagnostics on
// stderr, and an exit code that means something. `gitcito doctor` in a CI job
// is the whole point — the guard rails a repository declares in `.gitcito.json`
// are worth little if only the person with the GUI open ever sees them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/paths"
	"github.com/GabrySpada/gitcito/internal/repoconfig"
	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/GabrySpada/gitcito/internal/version"
)

// settingsFileName is the file the app writes its (secret-free) settings to.
const settingsFileName = "gitcito-settings.json"

// exitCode ends a verb early with a status, the way process.exit did deep
// inside repoRoot().
type exitCode int

func (e exitCode) Error() string { return "exit " + strconv.Itoa(int(e)) }

// term is one headless invocation: where its output goes and whether colour
// is on.
type term struct {
	stdout, stderr io.Writer
	tty            bool
	ctx            context.Context
}

func newTerm(stdout, stderr io.Writer) *term {
	// Colour only when someone is actually looking. Piped into a file, a log
	// collector or `grep`, escape codes are noise at best and corruption at worst.
	tty := false
	if f, ok := stdout.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			tty = os.Getenv("NO_COLOR") == ""
		}
	}
	return &term{stdout: stdout, stderr: stderr, tty: tty, ctx: context.Background()}
}

// ─── Terminal output ────────────────────────────────────────────────────────

func (t *term) paint(code, s string) string {
	if t.tty {
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	return s
}
func (t *term) bold(s string) string   { return t.paint("1", s) }
func (t *term) dim(s string) string    { return t.paint("2", s) }
func (t *term) red(s string) string    { return t.paint("31", s) }
func (t *term) green(s string) string  { return t.paint("32", s) }
func (t *term) yellow(s string) string { return t.paint("33", s) }
func (t *term) cyan(s string) string   { return t.paint("36", s) }

func (t *term) okMark() string   { return t.green("✓") }
func (t *term) warnMark() string { return t.yellow("!") }
func (t *term) failMark() string { return t.red("✗") }

func (t *term) out(line string) { _, _ = io.WriteString(t.stdout, line+"\n") }
func (t *term) err(line string) { _, _ = io.WriteString(t.stderr, line+"\n") }

// ─── Repository resolution ──────────────────────────────────────────────────

// gitError mirrors the message Node's execFile puts on a failed command.
type gitError struct {
	args   []string
	stderr string
	cause  error
}

func (e *gitError) Error() string {
	if _, ok := e.cause.(*exec.ExitError); ok {
		return "Command failed: git " + strings.Join(e.args, " ") + "\n" + e.stderr
	}
	return e.cause.Error()
}

// git runs git in cwd with the caller's environment (a CLI inherits the
// terminal's, unlike the app), a 20 s timeout and an 8 MiB cap, and returns
// trimmed stdout.
func (t *term) git(cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(t.ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	var stdout, stderr capBuffer
	stdout.max, stderr.max = 8*1024*1024, 8*1024*1024
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", &gitError{args: args, stderr: stderr.String(), cause: err}
	}
	if stdout.overflow {
		return "", errors.New("stdout maxBuffer length exceeded")
	}
	return gitx.TrimJS(stdout.String()), nil
}

func (t *term) gitOr(cwd, fallback string, args ...string) string {
	out, err := t.git(cwd, args...)
	if err != nil {
		return fallback
	}
	return out
}

// capBuffer keeps at most max bytes and remembers that it dropped some.
type capBuffer struct {
	bytes.Buffer
	max      int
	overflow bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func cwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// resolvePath is Node's path.resolve(p) against the working directory.
func resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(cwd(), p)
}

// repoRoot is the repository containing dir (default: cwd). Resolving to the
// top level rather than accepting the cwd verbatim is what lets every verb be
// run from a subdirectory, the way git itself can.
func (t *term) repoRoot(dir *string) (string, error) {
	start := cwd()
	if dir != nil {
		start = resolvePath(*dir)
	}
	if _, err := os.Stat(start); err != nil {
		t.err("gitcito: no such directory: " + start)
		return "", exitCode(2)
	}
	top := t.gitOr(start, "", "rev-parse", "--show-toplevel")
	if top == "" {
		t.err("gitcito: not a git repository: " + start)
		return "", exitCode(2)
	}
	return top, nil
}

// ─── Flags ──────────────────────────────────────────────────────────────────

// args is a deliberately small parse: long flags, `--flag` or `--flag=value`,
// plus a handful of single-letter aliases. Everything else is positional.
type args struct {
	rest  []string
	flags map[string]*string
}

func parseArgs(argv []string, aliases map[string]string) args {
	a := args{rest: []string{}, flags: map[string]*string{}}
	for _, token := range argv {
		switch {
		case strings.HasPrefix(token, "--"):
			if eq := strings.Index(token, "="); eq == -1 {
				a.flags[token[2:]] = nil
			} else {
				v := token[eq+1:]
				a.flags[token[2:eq]] = &v
			}
		case strings.HasPrefix(token, "-") && len(token) > 1 && aliases[token[1:]] != "":
			a.flags[aliases[token[1:]]] = nil
		default:
			a.rest = append(a.rest, token)
		}
	}
	return a
}

func (a args) has(flag string) bool {
	_, ok := a.flags[flag]
	return ok
}

func (a args) value(flag string) *string { return a.flags[flag] }

func (a args) arg(i int) *string {
	if i < len(a.rest) {
		return &a.rest[i]
	}
	return nil
}

// readConfig is readRepoConfig(root).config.
func readConfig(root string) *repoconfig.RepoConfig {
	return repoconfig.Read(root).Config
}

// readUTF8 is readFile(p, 'utf8'), with an unreadable file read as empty.
func readUTF8(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return repoconfig.DecodeUTF8(b)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ─── gitcito doctor ─────────────────────────────────────────────────────────

func (t *term) doctorLine(c repoconfig.DoctorCheck) string {
	mark := t.failMark()
	switch c.Status {
	case "ok":
		mark = t.okMark()
	case "warn":
		mark = t.warnMark()
	}
	want := ""
	if c.Expected != "" {
		want = t.dim(" wants " + c.Expected)
	}
	got := ""
	if c.Actual != "" {
		got = t.dim(" · found " + c.Actual)
	} else if c.Status != "ok" {
		got = t.dim(" · missing")
	}
	return "  " + mark + " " + t.bold(c.ID) + want + got
}

func (t *term) cmdDoctor(argv []string) (int, error) {
	a := parseArgs(argv, nil)
	root, err := t.repoRoot(a.arg(0))
	if err != nil {
		return 0, err
	}
	config := readConfig(root)
	if config == nil || config.Requires == nil {
		t.out(t.dim("No requirements declared in") + " " + repoconfig.File + t.dim("."))
		t.out(t.dim("Run `gitcito config init` to propose one from what this repository already contains."))
		return 0, nil
	}

	checks := repoconfig.Doctor(t.ctx, root)
	if a.has("fix") {
		for _, c := range checks {
			if c.Status == "ok" || c.Fix == nil {
				continue
			}
			if err := repoconfig.ApplyFix(t.ctx, root, *c.Fix); err != nil {
				t.out("  " + t.failMark() + " could not fix " + t.bold(c.ID) + ": " + err.Error())
			} else {
				t.out("  " + t.okMark() + " fixed " + t.bold(c.ID))
			}
		}
		checks = repoconfig.Doctor(t.ctx, root)
	}

	t.out(t.bold("Doctor · " + root))
	failed, warned := 0, 0
	for _, c := range checks {
		t.out(t.doctorLine(c))
		if c.Why != "" {
			t.out("      " + t.dim(c.Why))
		}
		switch c.Status {
		case "fail":
			failed++
		case "warn":
			warned++
		}
	}
	t.out("")
	if failed == 0 && warned == 0 {
		t.out(fmt.Sprintf("%s %d check%s passed.", t.okMark(), len(checks), plural(len(checks))))
	} else {
		t.out(fmt.Sprintf("%d failing, %d warning, %d total.", failed, warned, len(checks)))
	}
	// Warnings are things the doctor could not determine, not things that are
	// wrong — failing a build on them would make the file too costly to adopt.
	if failed > 0 {
		return 1, nil
	}
	return 0, nil
}

// ─── gitcito status ─────────────────────────────────────────────────────────

type statusCounts struct{ staged, unstaged, untracked, conflicted int }

// charAt is JS `line[i]` for the ASCII status columns: "" past the end.
func charAt(s string, i int) string {
	r := []rune(s)
	if i < len(r) {
		return string(r[i])
	}
	return ""
}

func countPorcelain(porcelain string) statusCounts {
	var c statusCounts
	for _, line := range strings.Split(porcelain, "\n") {
		if line == "" {
			continue
		}
		x, y := charAt(line, 0), charAt(line, 1)
		switch {
		case x == "?":
			c.untracked++
		case x == "U" || y == "U" || (x == "A" && y == "A") || (x == "D" && y == "D"):
			c.conflicted++
		default:
			if x != " " {
				c.staged++
			}
			if y != " " {
				c.unstaged++
			}
		}
	}
	return c
}

// jsNumberTruthy is `Number(s)` used as a condition: non-zero and not NaN.
func jsNumberTruthy(s string) bool {
	s = gitx.TrimJS(s)
	if s == "" {
		return false
	}
	f, err := strconv.ParseFloat(s, 64)
	return err == nil && f != 0 && !math.IsNaN(f)
}

func (t *term) cmdStatus(argv []string) (int, error) {
	a := parseArgs(argv, nil)
	root, err := t.repoRoot(a.arg(0))
	if err != nil {
		return 0, err
	}
	branch := t.gitOr(root, "HEAD", "rev-parse", "--abbrev-ref", "HEAD")
	upstream := t.gitOr(root, "", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	// gitOr trims, which eats the first line's leading space (" M a" reads as
	// staged). Kept: this is what the shipped command printed.
	porcelain := t.gitOr(root, "", "status", "--porcelain")
	stashes := t.gitOr(root, "", "stash", "list")
	lastCommit := t.gitOr(root, "", "log", "-1", "--format=%h %s %cr")
	counts := countPorcelain(porcelain)
	ahead, behind := "0", "0"
	if upstream != "" {
		ahead = t.gitOr(root, "0", "rev-list", "--count", upstream+"..HEAD")
		behind = t.gitOr(root, "0", "rev-list", "--count", "HEAD.."+upstream)
	}
	config := readConfig(root)

	tracking := t.dim("no upstream")
	if upstream != "" {
		tracking = t.dim("→") + " " + upstream
		if jsNumberTruthy(ahead) {
			tracking += " " + t.green("↑"+ahead)
		}
		if jsNumberTruthy(behind) {
			tracking += " " + t.yellow("↓"+behind)
		}
	}

	t.out(t.bold(root[strings.LastIndex(root, "/")+1:]) + "  " + t.dim(root))
	t.out("  " + t.cyan(branch) + " " + tracking)
	if lastCommit != "" {
		t.out("  " + t.dim(lastCommit))
	}

	parts := []string{}
	if counts.conflicted > 0 {
		parts = append(parts, t.red(fmt.Sprintf("%d conflicted", counts.conflicted)))
	}
	if counts.staged > 0 {
		parts = append(parts, t.green(fmt.Sprintf("%d staged", counts.staged)))
	}
	if counts.unstaged > 0 {
		parts = append(parts, t.yellow(fmt.Sprintf("%d modified", counts.unstaged)))
	}
	if counts.untracked > 0 {
		parts = append(parts, t.dim(fmt.Sprintf("%d untracked", counts.untracked)))
	}
	stashCount := 0
	for _, l := range strings.Split(stashes, "\n") {
		if l != "" {
			stashCount++
		}
	}
	if stashCount > 0 {
		parts = append(parts, t.dim(fmt.Sprintf("%d stashed", stashCount)))
	}
	if len(parts) > 0 {
		t.out("  " + strings.Join(parts, t.dim(" · ")))
	} else {
		t.out("  " + t.green("clean"))
	}

	if config != nil {
		for _, p := range config.Protect {
			if branchMatchesGlob(p, branch) {
				t.out("  " + t.warnMark() + " " + branch + " is protected by " + repoconfig.File)
				break
			}
		}
		var checklist []string
		if config.Checklist != nil {
			checklist = config.Checklist.Push
		}
		if len(checklist) > 0 {
			t.out("")
			t.out(t.bold("Before you push"))
			for _, item := range checklist {
				t.out("  " + t.dim("•") + " " + item)
			}
		}
	}
	if counts.conflicted > 0 {
		return 1, nil
	}
	return 0, nil
}

// branchMatchesGlob matches `protect` entries, which are globs, the way the
// app matches them.
func branchMatchesGlob(pattern, branch string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == branch
	}
	pieces := strings.Split(pattern, "*")
	for i, p := range pieces {
		pieces[i] = regexp.QuoteMeta(p)
	}
	rx, err := regexp.Compile("^" + strings.Join(pieces, "[^\n\r  ]*") + "$")
	return err == nil && rx.MatchString(branch)
}

// ─── gitcito commit-check ───────────────────────────────────────────────────

func (t *term) cmdCommitCheck(argv []string) (int, error) {
	// `-m "text"` reads better in a shell than `--message=text`, and a hook is
	// handed a file path, so both shapes have to work.
	inlineIdx := -1
	for i, a := range argv {
		if a == "-m" || a == "--message" {
			inlineIdx = i
			break
		}
	}
	filtered := []string{}
	for i, a := range argv {
		if inlineIdx < 0 || (i != inlineIdx && i != inlineIdx+1) {
			filtered = append(filtered, a)
		}
	}
	a := parseArgs(filtered, nil)
	var inline *string
	if inlineIdx >= 0 {
		inline = nil
		if inlineIdx+1 < len(argv) {
			inline = &argv[inlineIdx+1]
		}
	} else {
		inline = a.value("message")
	}

	wd := cwd()
	root, err := t.repoRoot(a.value("repo"))
	if err != nil {
		return 0, err
	}
	var raw string
	if inline != nil {
		raw = *inline
	} else {
		file := filepath.Join(root, ".git", "COMMIT_EDITMSG")
		if len(a.rest) > 0 {
			file = a.rest[0]
		}
		p := file
		if !filepath.IsAbs(file) {
			p = filepath.Join(wd, file)
		}
		raw = readUTF8(p)
		if raw == "" {
			t.err("gitcito: nothing to check — no message given and " + p + " is empty")
			return 2, nil
		}
	}

	config := readConfig(root)
	branch := t.gitOr(root, "", "rev-parse", "--abbrev-ref", "HEAD")
	issues := shared.CheckCommitMessage(raw, shared.CommitCheckOptions{Config: commitCheckConfig(config), Branch: branch})
	return t.reportCommitIssues(issues), nil
}

func commitCheckConfig(c *repoconfig.RepoConfig) *shared.CommitCheckConfig {
	if c == nil {
		return nil
	}
	out := &shared.CommitCheckConfig{}
	if c.Commit != nil {
		out.Scopes = c.Commit.Scopes
		out.Trailers = c.Commit.Trailers
		out.TicketFromBranch = c.Commit.TicketFromBranch != nil && *c.Commit.TicketFromBranch
	}
	return out
}

func (t *term) reportCommitIssues(issues []shared.CommitCheckIssue) int {
	if len(issues) == 0 {
		t.out(t.okMark() + " Commit message looks good.")
		return 0
	}
	errs := 0
	for _, i := range issues {
		mark := t.warnMark()
		if i.Level == "error" {
			mark = t.failMark()
			errs++
		}
		t.out("  " + mark + " " + i.Message + " " + t.dim("("+i.Code+")"))
	}
	if errs > 0 {
		return 1
	}
	return 0
}

// ─── gitcito config ─────────────────────────────────────────────────────────

func (t *term) cmdConfig(argv []string) (int, error) {
	a := parseArgs(argv, nil)
	sub, dir := a.arg(0), a.arg(1)
	root, err := t.repoRoot(dir)
	if err != nil {
		return 0, err
	}
	file := filepath.Join(root, repoconfig.File)

	s := ""
	if sub != nil {
		s = *sub
	}
	switch {
	case sub == nil || s == "show":
		raw := readUTF8(file)
		if raw == "" {
			t.out(t.dim("No " + repoconfig.File + " in " + root + "."))
			return 0, nil
		}
		t.out(gitx.TrimEndJS(raw))
		return 0, nil
	case s == "check":
		raw := readUTF8(file)
		if raw == "" {
			t.err("gitcito: no " + repoconfig.File + " in " + root)
			return 2, nil
		}
		config, issues := repoconfig.Parse(raw)
		if config == nil {
			t.out(t.failMark() + " " + repoconfig.File + " could not be read as a v1 config.")
			for _, i := range issues {
				t.out("  " + t.failMark() + " " + i.Field + ": " + i.Code)
			}
			return 1, nil
		}
		for _, i := range issues {
			t.out("  " + t.warnMark() + " " + i.Field + ": " + i.Code + " " + t.dim("(dropped)"))
		}
		if len(issues) > 0 {
			t.out(fmt.Sprintf("%s %d field(s) dropped; the rest applies.", t.warnMark(), len(issues)))
		} else {
			t.out(t.okMark() + " " + repoconfig.File + " is valid.")
		}
		return 0, nil
	case s == "init":
		if _, err := os.Stat(file); err == nil && !a.has("force") {
			t.err("gitcito: " + repoconfig.File + " already exists — pass --force to overwrite")
			return 2, nil
		}
		suggested, err := repoconfig.Suggest(t.ctx, root)
		if err != nil {
			return 0, err
		}
		if repoconfig.IsEmpty(&suggested) {
			t.out(t.dim("Nothing to propose: no .nvmrc, submodules, LFS, hooks path or consistent commit scopes found."))
			return 0, nil
		}
		text, err := serializeSuggested(&suggested)
		if err != nil {
			return 0, err
		}
		if a.has("dry-run") {
			t.out(text)
			return 0, nil
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			return 0, err
		}
		t.out(t.okMark() + " Wrote " + repoconfig.File)
		t.out(t.dim("Review it before committing — it becomes every teammate’s rules."))
		t.summarizeConfig(&suggested)
		return 0, nil
	default:
		t.err("gitcito config: unknown subcommand \"" + s + "\" (expected init, show or check)")
		return 2, nil
	}
}

func (t *term) summarizeConfig(c *repoconfig.RepoConfig) {
	if len(c.Protect) > 0 {
		t.out("  " + t.dim("protect") + " " + strings.Join(c.Protect, ", "))
	}
	if c.Requires != nil && c.Requires.Node != "" {
		t.out("  " + t.dim("node") + " " + c.Requires.Node)
	}
	if c.Commit != nil && len(c.Commit.Scopes) > 0 {
		t.out("  " + t.dim("scopes") + " " + strings.Join(c.Commit.Scopes, ", "))
	}
}

// ─── gitcito repos ──────────────────────────────────────────────────────────

// readSettings reads the settings file the app writes — in the app's own data
// directory (GITCITO_DATA_DIR honoured), never the Electron build's.
func readSettings() any {
	file, err := paths.File(settingsFileName)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	return v
}

// padEnd is String.prototype.padEnd(n) with spaces.
func padEnd(s string, n int) string {
	if l := gitx.UTF16Len(s); l < n {
		return s + strings.Repeat(" ", n-l)
	}
	return s
}

func (t *term) cmdRepos(argv []string) (int, error) {
	a := parseArgs(argv, nil)
	repos := shared.KnownRepos(readSettings())
	if len(repos) == 0 {
		t.out(t.dim("No repositories yet — open one in Gitcito, or run `gitcito .` in a checkout."))
		return 0, nil
	}
	shown := repos
	if len(a.rest) > 0 && a.rest[0] != "" {
		filter := strings.ToLower(a.rest[0])
		shown = []shared.KnownRepo{}
		for _, r := range repos {
			if shared.MatchesRepo(r, filter) {
				shown = append(shown, r)
			}
		}
	}
	status := 1
	if len(shown) > 0 {
		status = 0
	}
	if a.has("paths") {
		// Machine-readable, for `cd "$(gitcito repos --paths api | head -1)"`.
		for _, r := range shown {
			t.out(r.Path)
		}
		return status, nil
	}
	// Pad name and group separately: the paths only line up if the two columns
	// before them are each fixed width.
	nameWidth, groupWidth := 0, 0
	for _, r := range shown {
		nameWidth = max(nameWidth, gitx.UTF16Len(r.Name))
		if r.Group != "" {
			groupWidth = max(groupWidth, gitx.UTF16Len(r.Group)+2)
		}
	}
	for _, r := range shown {
		group := ""
		if r.Group != "" {
			group = "[" + r.Group + "]"
		}
		t.out("  " + t.bold(padEnd(r.Name, nameWidth)) + "  " + t.dim(padEnd(group, groupWidth)) + "  " + t.dim(r.Path))
	}
	return status, nil
}

// cmdResolve resolves `gitcito open <name>` against the known repositories,
// for the launcher's by-name mode.
func (t *term) cmdResolve(argv []string) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return 2, nil
	}
	hit := shared.ResolveRepo(shared.KnownRepos(readSettings()), strings.ToLower(argv[0]))
	if hit == nil {
		return 1, nil
	}
	t.out(hit.Path)
	return 0, nil
}

// ─── gitcito editor ─────────────────────────────────────────────────────────

// cmdEditor wires (or unwires) Gitcito as git's message editor. `--wait` is
// what makes it usable there: git blocks until the file comes back, so the
// command has to block too.
func (t *term) cmdEditor(argv []string) (int, error) {
	a := parseArgs(argv, nil)
	scope := "--global"
	dir := cwd()
	if a.has("local") {
		scope = "--local"
		root, err := t.repoRoot(nil)
		if err != nil {
			return 0, err
		}
		dir = root
	}
	sub := "install"
	if len(a.rest) > 0 {
		sub = a.rest[0]
	}

	set := func(key string, value *string) error {
		if value == nil {
			_, _ = t.git(dir, "config", scope, "--unset", key)
			return nil
		}
		_, err := t.git(dir, "config", scope, key, *value)
		return err
	}

	if sub == "uninstall" {
		if err := set("core.editor", nil); err != nil {
			return 0, err
		}
		if err := set("sequence.editor", nil); err != nil {
			return 0, err
		}
		t.out(t.okMark() + " Gitcito is no longer git’s editor (" + scope[2:] + ").")
		return 0, nil
	}
	if sub != "install" {
		t.err("gitcito editor: unknown subcommand \"" + sub + "\" (expected install or uninstall)")
		return 2, nil
	}
	editor := "gitcito --wait"
	if err := set("core.editor", &editor); err != nil {
		return 0, err
	}
	if err := set("sequence.editor", &editor); err != nil {
		return 0, err
	}
	t.out(t.okMark() + " git will now open commit messages and rebase todos in Gitcito (" + scope[2:] + ").")
	t.out(t.dim("Undo with `gitcito editor uninstall`."))
	return 0, nil
}

// ─── gitcito completions ────────────────────────────────────────────────────

var headlessVerbList = []string{"doctor", "status", "repos", "commit-check", "config", "editor", "completions", "help"}

var windowVerbList = []string{
	"open", "diff", "graph", "blame", "show", "search", "conflicts", "stack", "stash", "reflog", "todos", "chat",
	"settings", "terminal", "insights", "history", "ci", "clean", "bisect", "absorb", "snapshots", "timelapse",
	"time-machine", "maintenance", "export", "objects", "hooks", "lfs", "sparse", "subtree", "gitflow", "purge",
	"changelog",
}

func allVerbs() []string {
	all := append(append([]string{}, headlessVerbList...), windowVerbList...)
	// Array.prototype.sort() compares UTF-16 code units; the verbs are ASCII.
	sort.Strings(all)
	return all
}

func (t *term) completions(shell string) int {
	verbs := strings.Join(allVerbs(), " ")
	switch shell {
	case "bash":
		t.out(`# gitcito bash completion — add to ~/.bashrc:
#   eval "$(gitcito completions bash)"
_gitcito() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "` + verbs + `" -- "$cur") )
  else
    COMPREPLY=( $(compgen -d -- "$cur") )
  fi
}
complete -F _gitcito gitcito`)
		return 0
	case "zsh":
		t.out(`# gitcito zsh completion — add to ~/.zshrc:
#   eval "$(gitcito completions zsh)"
_gitcito() {
  if (( CURRENT == 2 )); then
    compadd ` + verbs + `
  else
    _files -/
  fi
}
compdef _gitcito gitcito`)
		return 0
	case "fish":
		t.out(`# gitcito fish completion — write to ~/.config/fish/completions/gitcito.fish:
#   gitcito completions fish > ~/.config/fish/completions/gitcito.fish
complete -c gitcito -f
complete -c gitcito -n "__fish_use_subcommand" -a "` + verbs + `"
complete -c gitcito -n "not __fish_use_subcommand" -a "(__fish_complete_directories)"`)
		return 0
	default:
		t.err("gitcito completions: expected bash, zsh or fish")
		return 2
	}
}

// ─── Help & version ─────────────────────────────────────────────────────────

func (t *term) usage() {
	h := t.bold
	f := repoconfig.File
	t.out(h("gitcito") + ` — Gitcito from the command line

` + h("OPEN") + `
  gitcito [path]                 Open a repository (default: the current directory)
  gitcito open <name|path>       Open by tab name — ` + "`gitcito open api`" + ` finds it
  gitcito [path] -n <name>       …with a display name
  gitcito [path] -g <group>      …inside a group tab

` + h("JUMP STRAIGHT TO A SURFACE") + `
  gitcito diff [path]            Working changes
  gitcito graph [path]           The commit graph
  gitcito show <ref>             One commit, in detail
  gitcito blame <file> [-l N]    Blame, scrolled to a line
  gitcito search <query>         Search the repository's code
  gitcito conflicts | stack | stash | reflog | todos | chat | settings
  gitcito ci | clean | bisect | absorb | snapshots | insights | terminal
                                 …and the rest — ` + "`gitcito help verbs`" + ` lists them

` + h("ANSWER IN THE TERMINAL") + `
  gitcito status [path]          Branch, tracking, working tree, push checklist
  gitcito doctor [path] [--fix]  Check what ` + f + ` requires. Exit 1 on failure
  gitcito commit-check [file]    Lint a commit message. Exit 1 on an error
  gitcito config init|show|check Propose, print or validate ` + f + `
  gitcito repos [filter]         Repositories Gitcito knows about (--paths for scripts)

` + h("SET UP") + `
  gitcito editor install         Use Gitcito for commit messages and rebase todos
  gitcito completions <shell>    bash · zsh · fish

` + h("OPTIONS") + `
  -w, --wait                     Block until the opened file is closed (for git)
  -n, --name <name>              Display name for the tab
  -g, --group <group>            Group tab to open in
  -l, --line <n>                 Line to scroll to
  -h, --help                     This text
  -V, --version                  Version

` + t.dim("Docs: https://myappdesk.github.io/gitcito/help/cli.html"))
}

func (t *term) verbList() {
	t.out(t.bold("In the terminal"))
	t.out("  " + strings.Join(headlessVerbList, ", "))
	t.out("")
	t.out(t.bold("In the window"))
	t.out("  " + strings.Join(windowVerbList, ", "))
}

// ─── Entry ──────────────────────────────────────────────────────────────────

func (t *term) main(argv []string) int {
	verb := argv[0]
	rest := argv[1:]
	code, err := t.dispatch(verb, rest)
	if err != nil {
		var ec exitCode
		if errors.As(err, &ec) {
			return int(ec)
		}
		t.err("gitcito: " + err.Error())
		return 2
	}
	return code
}

func (t *term) dispatch(verb string, rest []string) (int, error) {
	switch verb {
	case "doctor":
		return t.cmdDoctor(rest)
	case "status":
		return t.cmdStatus(rest)
	case "commit-check":
		return t.cmdCommitCheck(rest)
	case "config":
		return t.cmdConfig(rest)
	case "repos":
		return t.cmdRepos(rest)
	case "--resolve":
		return t.cmdResolve(rest)
	case "editor":
		return t.cmdEditor(rest)
	case "completions":
		shell := ""
		if len(rest) > 0 {
			shell = rest[0]
		}
		return t.completions(shell), nil
	case "help", "--help", "-h":
		if len(rest) > 0 && rest[0] == "verbs" {
			t.verbList()
		} else {
			t.usage()
		}
		return 0, nil
	case "--version", "-V":
		t.out(version.Version)
		return 0, nil
	}
	t.err("gitcito: unknown command \"" + verb + "\"")
	t.usage()
	return 2, nil
}
