package launch

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// host is the slice of the machine command building depends on — injectable
// so tests can pin it.
type host struct {
	goos   string
	sep    string
	home   string
	getenv func(string) string
	read   func(string) (string, bool)
}

func defaultHost() host {
	home, _ := os.UserHomeDir()
	return host{
		goos:   runtime.GOOS,
		sep:    string(filepath.Separator),
		home:   home,
		getenv: os.Getenv,
		read:   readText,
	}
}

func (h host) windows() bool { return h.goos == "windows" }

// ─── Variable substitution ──────────────────────────────────────────────────
// A pragmatic subset of VS Code's variables, enough to run typical configs.

var (
	reWorkspaceFolderBasename = regexp.MustCompile(`\$\{workspaceFolderBasename\}`)
	reWorkspaceFolder         = regexp.MustCompile(`\$\{workspaceFolder\}`)
	reWorkspaceRoot           = regexp.MustCompile(`\$\{workspaceRoot\}`)
	reCwd                     = regexp.MustCompile(`\$\{cwd\}`)
	reUserHome                = regexp.MustCompile(`\$\{userHome\}`)
	rePathSeparator           = regexp.MustCompile(`\$\{pathSeparator\}`)
	reSlash                   = regexp.MustCompile(`\$\{/\}`)
	reEnvVar                  = regexp.MustCompile(`\$\{env:([^}]+)\}`)
	reConfigVar               = regexp.MustCompile(`\$\{config:[^}]+\}`)
	reInputToken              = regexp.MustCompile(`\$\{input:([^}]+)\}`)
)

// substitute replaces the supported variables. String replacements go through
// JS replacement-pattern expansion, as `.replace(re, string)` did: a folder
// holding `$&` really does expand.
func (h host) substitute(value, folder string) string {
	parts := strings.Split(folder, h.sep)
	base := parts[len(parts)-1]
	v := shared.JSReplaceAll(reWorkspaceFolderBasename, value, base)
	v = shared.JSReplaceAll(reWorkspaceFolder, v, folder)
	v = shared.JSReplaceAll(reWorkspaceRoot, v, folder) // legacy alias
	v = shared.JSReplaceAll(reCwd, v, folder)
	v = shared.JSReplaceAll(reUserHome, v, h.home)
	v = shared.JSReplaceAll(rePathSeparator, v, h.sep)
	v = shared.JSReplaceAll(reSlash, v, h.sep) // shorthand for ${pathSeparator}
	v = reEnvVar.ReplaceAllStringFunc(v, func(m string) string {
		return h.getenv(reEnvVar.FindStringSubmatch(m)[1])
	})
	return reConfigVar.ReplaceAllLiteralString(v, "") // editor settings — not available headless
}

// subAll is `value ? substitute(value, folder) : ”` for a loosely typed value.
func (h host) subAll(v any, folder string) string {
	if !truthy(v) {
		return ""
	}
	return h.substitute(jsString(v), folder)
}

// subList maps a JSON array through subAll; anything else is empty.
func (h host) subList(v any, folder string) []string {
	arr, _ := list(v)
	out := make([]string, 0, len(arr))
	for _, a := range arr {
		out = append(out, h.subAll(a, folder))
	}
	return out
}

// ─── Per-platform overrides ──────────────────────────────────────────────────
// VS Code lets a launch config or task carry `windows` / `osx` / `linux` blocks
// that override the top-level keys on that OS. We merge the matching block in
// before doing anything else.

func (h host) platformKey() string {
	switch h.goos {
	case "windows":
		return "windows"
	case "darwin":
		return "osx"
	}
	return "linux"
}

func (h host) applyPlatform(o map[string]any) map[string]any {
	override, ok := obj(o[h.platformKey()])
	if !ok {
		return o
	}
	out := make(map[string]any, len(o)+len(override))
	for k, v := range o {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// ─── ${input:id} resolution ──────────────────────────────────────────────────

// ResolveInputTokens deep-replaces `${input:id}` in every string of a decoded
// JSON value with the user's answers, leaving unknown ids intact. The renderer
// prompts for each id and sends the answers; this runs before the usual
// variable substitution.
func ResolveInputTokens(value any, values map[string]string) any {
	switch v := value.(type) {
	case string:
		return reInputToken.ReplaceAllStringFunc(v, func(m string) string {
			if ans, ok := values[reInputToken.FindStringSubmatch(m)[1]]; ok {
				return ans
			}
			return m
		})
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = ResolveInputTokens(e, values)
		}
		return out
	case map[string]any:
		if v == nil {
			return v
		}
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = ResolveInputTokens(e, values)
		}
		return out
	}
	return value
}

func resolveConfig(c LaunchConfig, values map[string]string) LaunchConfig {
	m, _ := ResolveInputTokens(map[string]any(c), values).(map[string]any)
	return m
}

func resolveList(cs []map[string]any, values map[string]string) []map[string]any {
	out := make([]map[string]any, len(cs))
	for i, c := range cs {
		out[i] = resolveConfig(c, values)
	}
	return out
}

// unresolvableVar detects launch variables we can't resolve outside the editor
// (`${input:…}`, `${command:…}`, `${file…}`, `${selectedText}` …). Used to warn
// instead of silently running a half-substituted command line.
var unresolvableVar = regexp.MustCompile(`\$\{(input:|command:|file|relativeFile|lineNumber|selectedText|fileBasename|fileDirname|fileExtname|defaultBuildTask)`)

func hasUnresolvableVars(values ...any) bool {
	for _, v := range values {
		if s, ok := v.(string); ok && unresolvableVar.MatchString(s) {
			return true
		}
	}
	return false
}

// ─── env files ───────────────────────────────────────────────────────────────

var (
	reCRLFSplit  = regexp.MustCompile(`\r?\n`)
	reExportWord = shared.MustJSRegexp(`^export\s+`, false)
)

// parseDotenv parses a dotenv-style file into KEY=VALUE pairs (best-effort,
// like VS Code).
func parseDotenv(text string) map[string]string {
	out := map[string]string{}
	for _, raw := range reCRLFSplit.Split(text, -1) {
		line := gitx.TrimJS(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := reExportWord.ReplaceAllLiteralString(gitx.TrimJS(line[:eq]), "")
		val := gitx.TrimJS(line[eq+1:])
		// Strip matching surrounding quotes.
		if (strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`)) ||
			(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			if len(val) >= 2 {
				val = val[1 : len(val)-1]
			} else {
				val = ""
			}
		}
		out[key] = val
	}
	return out
}

// applyEnvFile reads and merges a config's `envFile` into env (config `env`
// still wins).
func (h host) applyEnvFile(config LaunchConfig, folder string, env map[string]string) {
	if !truthy(config["envFile"]) {
		return
	}
	text, ok := h.read(h.subAll(config["envFile"], folder))
	if !ok {
		return
	}
	for k, v := range parseDotenv(text) {
		if _, has := env[k]; !has {
			env[k] = v
		}
	}
}

// ─── Shell-quoting ──────────────────────────────────────────────────────────

var winNeedsQuote = shared.MustJSRegexp(`[\s"]`, false)

func (h host) shQuote(s string) string {
	if h.windows() {
		if winNeedsQuote.MatchString(s) {
			return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
		}
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (h host) quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = h.shQuote(s)
	}
	return out
}

func lower(v any) string {
	if v == nil {
		return ""
	}
	return strings.ToLower(jsString(v))
}

// taskCommandSelf builds the shell command line for a single task's own
// command (no deps).
func (h host) taskCommandSelf(task LaunchTask, folder string) string {
	typ := lower(task["type"])
	var line string
	switch {
	case typ == "npm" && truthy(task["script"]):
		// `{ "type": "npm", "script": "build" }` → `npm run build`.
		parts := append([]string{"npm", "run", h.shQuote(h.subAll(task["script"], folder))}, h.quoteAll(h.subList(task["args"], folder))...)
		line = strings.Join(parts, " ")
	case truthy(task["command"]):
		args := h.quoteAll(h.subList(task["args"], folder))
		// VS Code treats a `shell` task's bare command string as a full command
		// line (pipes, `&&`, redirects); quoting it would exec it as one word.
		if typ == "shell" && len(args) == 0 {
			line = h.subAll(task["command"], folder)
		} else {
			line = strings.Join(append([]string{h.shQuote(h.subAll(task["command"], folder))}, args...), " ")
		}
	default:
		return "" // dependsOn-only task: nothing of its own to run.
	}
	// Inline any per-task env (`options.env`) ahead of the command (posix only;
	// on Windows the launch env already carries config env and we keep it simple).
	// Keys are sorted: Go maps keep no insertion order, and the order of
	// `A=1 B=2 cmd` assignments does not change what runs.
	options, _ := obj(task["options"])
	if !h.windows() {
		envPairs, _ := obj(options["env"])
		keys := make([]string, 0, len(envPairs))
		for k := range envPairs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, k+"="+h.shQuote(h.subAll(envPairs[k], folder)))
		}
		if prefix := strings.Join(pairs, " "); prefix != "" {
			line = prefix + " " + line
		}
	}
	// Always a subshell: VS Code runs each task in its own shell, so a task body
	// that ends in `exit` (a common way to swallow a failing step) must not take
	// the launch chain down with it. The subshell also scopes `options.cwd`.
	if truthy(options["cwd"]) {
		return "( cd " + h.shQuote(h.subAll(options["cwd"], folder)) + " && " + line + " )"
	}
	return "( " + line + " )"
}

// resolveTaskCommands resolves a task into the ordered list of shell commands
// to run, expanding any `dependsOn` chain first (depth-first, deduped,
// cycle-safe). VS Code runs `dependsOn` before the task itself; we serialise
// everything into one terminal.
func (h host) resolveTaskCommands(label any, tasks []LaunchTask, folder string, seen map[string]bool) []string {
	key, isStr := label.(string)
	if !isStr {
		// A non-string label never equals a task's (string) label.
		return nil
	}
	if seen[key] {
		return nil
	}
	seen[key] = true
	var found LaunchTask
	for _, t := range tasks {
		if l, ok := t["label"].(string); ok && l == key {
			found = t
			break
		}
	}
	if found == nil {
		return nil
	}
	task := h.applyPlatform(found)
	var out []string
	if truthy(task["dependsOn"]) {
		deps := []any{task["dependsOn"]}
		if arr, ok := list(task["dependsOn"]); ok {
			deps = arr
		}
		for _, dep := range deps {
			out = append(out, h.resolveTaskCommands(dep, tasks, folder, seen)...)
		}
	}
	if self := h.taskCommandSelf(task, folder); self != "" {
		// A background/watch task (`isBackground: true`, e.g. `tsc -w`, `vite`)
		// never exits — chaining it with `&&` would hang the launch forever. Run
		// it detached so the next segment can start. We can't do VS Code's
		// problem-matcher "ready" detection, so this is a best-effort head start.
		if truthy(task["isBackground"]) {
			self = h.backgrounded(self)
		}
		out = append(out, self)
	}
	return out
}

// backgrounded wraps a command so it runs detached and the launch chain can
// proceed.
func (h host) backgrounded(cmd string) string {
	if h.windows() {
		return "start /b " + cmd
	}
	return "( " + cmd + " ) &"
}

func seedSet(labels []string) map[string]bool {
	s := make(map[string]bool, len(labels))
	for _, l := range labels {
		s[l] = true
	}
	return s
}

// configSegments turns a launch config into the ordered shell command segments
// to run: its preLaunchTask chain (dependsOn expanded) followed by its program.
// A `compounds` entry expands into all its member configs, in order. Collects
// each config's `env` (and `envFile`) into env.
func (h host) configSegments(config LaunchConfig, configs []LaunchConfig, tasks []LaunchTask, dir string,
	env map[string]string, seen map[string]bool, skipTasks []string) []string {
	config = h.applyPlatform(config)
	if members, ok := list(config["compound"]); ok {
		name := jsString(config["name"])
		if seen[name] {
			return nil
		}
		seen[name] = true
		var out []string
		for _, n := range members {
			for _, c := range configs {
				if cn, ok := c["name"].(string); ok && n == any(cn) {
					out = append(out, h.configSegments(c, configs, tasks, dir, env, seen, skipTasks)...)
					break
				}
			}
		}
		return out
	}
	// Config `env` wins over `envFile`; both are merged into the pty environment.
	if e, ok := obj(config["env"]); ok {
		for k, v := range e {
			env[k] = h.subAll(v, dir)
		}
	}
	h.applyEnvFile(config, dir, env)
	var out []string
	// Pre-seeding the dedupe set with `skipTasks` drops tasks (and their whole
	// dependsOn subtree) that a compound launch already ran once up front.
	if truthy(config["preLaunchTask"]) {
		out = append(out, h.resolveTaskCommands(config["preLaunchTask"], tasks, dir, seedSet(skipTasks))...)
	}
	if program := h.launchCommand(config, dir); program != "" {
		// Honour a per-config cwd for compound members (the top-level config's
		// cwd is applied to the pty directly, so it needs no wrapping).
		cwd := ""
		if truthy(config["cwd"]) {
			cwd = h.subAll(config["cwd"], dir)
		}
		if cwd != "" && len(seen) > 0 {
			out = append(out, "( cd "+h.shQuote(cwd)+" && "+program+" )")
		} else {
			out = append(out, program)
		}
	}
	// A `postDebugTask` runs once the program exits (we chain it after).
	if post, ok := config["postDebugTask"].(string); ok && post != "" {
		out = append(out, h.resolveTaskCommands(post, tasks, dir, seedSet(skipTasks))...)
	}
	return out
}

var (
	reExtensionHost = regexp.MustCompile(`extensionhost`)
	reBrowserType   = regexp.MustCompile(`^(pwa-)?(chrome|msedge|edge)$`)
	reLibDir        = regexp.MustCompile(`(^|/)lib/`)
)

// launchCommand builds the shell command line that launches a config's program.
func (h host) launchCommand(config LaunchConfig, folder string) string {
	typ := lower(config["type"])
	request := "launch"
	if config["request"] != nil {
		request = lower(config["request"])
	}

	// 1. `node-terminal` (and anything with a raw `command`) — run it verbatim;
	//    it's already a full shell command line, not a program path.
	if c, ok := config["command"].(string); ok {
		if command := h.subAll(c, folder); command != "" {
			return command
		}
	}

	// 2. Attach configs have no program of their own. If one carries a build via
	//    preLaunchTask, the task is the work (handled by the caller); there's
	//    nothing else for us to spawn directly.
	_, urlIsString := config["url"].(string)
	if request == "attach" && !truthy(config["runtimeExecutable"]) && !urlIsString {
		return ""
	}

	// 3. The VS Code Extension Host can't be launched outside VS Code itself.
	if reExtensionHost.MatchString(typ) {
		return ""
	}

	// 4. Browser debug configs — we can't drive a debugger, so just open the URL.
	if reBrowserType.MatchString(typ) {
		url := ""
		if u, ok := config["url"].(string); ok {
			url = h.subAll(u, folder)
		}
		if url == "" {
			return ""
		}
		return h.openCmd() + " " + h.shQuote(url)
	}

	// 5. Dart / Flutter — run through the flutter (or dart) CLI.
	if typ == "dart" || typ == "flutter" {
		return h.dartCommand(config, folder)
	}

	program := h.subAll(config["program"], folder)
	args := h.quoteAll(h.subList(config["args"], folder))

	// 6. Go (`dlv`-based delve adapter) — `go run` the package/file.
	if typ == "go" {
		target := program
		if target == "" {
			if c, ok := config["cwd"].(string); ok {
				target = h.subAll(c, folder)
			} else {
				target = "."
			}
		}
		return strings.Join(append([]string{"go", "run", h.shQuote(target)}, args...), " ")
	}

	// 7. .NET (coreclr / clr) — the `program` is a built .dll, run via `dotnet`.
	if typ == "coreclr" || typ == "clr" || typ == "dotnet" {
		if program == "" {
			return ""
		}
		if strings.HasSuffix(strings.ToLower(program), ".dll") {
			return strings.Join(append([]string{"dotnet", h.shQuote(program)}, args...), " ")
		}
		return strings.Join(append([]string{h.shQuote(program)}, args...), " ")
	}

	// 8. PHP (XDebug `php` adapter) — run the script through the php CLI.
	if typ == "php" {
		if program == "" {
			return ""
		}
		return strings.Join(append([]string{"php", h.shQuote(program)}, args...), " ")
	}

	// 9. Python launching a *module* (`"module": "uvicorn"` → `python -m uvicorn`).
	if typ == "python" || typ == "debugpy" {
		if mod, ok := config["module"].(string); ok && mod != "" {
			return strings.Join(append([]string{h.python(), "-m", h.shQuote(h.subAll(mod, folder))}, args...), " ")
		}
	}

	// Pick the executable: an explicit runtimeExecutable wins; otherwise infer an
	// interpreter from the debug `type` / file extension so a bare `program`
	// (e.g. a .js or .py file) is run *through* its runtime instead of exec'd
	// directly (which would fail with "permission denied").
	var exe string
	if truthy(config["runtimeExecutable"]) {
		exe = h.subAll(config["runtimeExecutable"], folder)
	} else if exe = h.interpreterFor(config, program); exe == "" {
		exe = program
	}
	if exe == "" {
		return ""
	}
	runtimeArgs := h.quoteAll(h.subList(config["runtimeArgs"], folder))
	// When the executable is a runtime (runtimeExecutable or an inferred
	// interpreter), `program` becomes its first argument.
	parts := append([]string{h.shQuote(exe)}, runtimeArgs...)
	if program != "" && exe != program {
		parts = append(parts, h.shQuote(program))
	}
	return strings.Join(append(parts, args...), " ")
}

func (h host) python() string {
	if h.windows() {
		return "python"
	}
	return "python3"
}

// dartCommand builds a `flutter run` / `dart run` command for a Dart-Code
// launch config.
func (h host) dartCommand(config LaunchConfig, folder string) string {
	program := h.subAll(config["program"], folder)
	mode := ""
	if m, ok := config["flutterMode"].(string); ok {
		mode = strings.ToLower(m)
	}
	args := h.quoteAll(h.subList(config["args"], folder))
	// A `lib/*.dart` entrypoint (or no program) is almost always a Flutter app;
	// a bare script path is a plain Dart program.
	if program == "" || reLibDir.MatchString(program) {
		parts := []string{"flutter", "run"}
		if program != "" {
			parts = append(parts, "-t", h.shQuote(program))
		}
		switch mode {
		case "profile":
			parts = append(parts, "--profile")
		case "release":
			parts = append(parts, "--release")
		}
		return strings.Join(append(parts, args...), " ")
	}
	return strings.Join(append([]string{"dart", "run", h.shQuote(program)}, args...), " ")
}

// openCmd is the platform command that opens a URL/file in the default handler.
func (h host) openCmd() string {
	switch h.goos {
	case "windows":
		return `start ""`
	case "darwin":
		return "open"
	}
	return "xdg-open"
}

// interpreterFor infers the interpreter for a config that has a `program` but
// no runtimeExecutable.
func (h host) interpreterFor(config LaunchConfig, program string) string {
	switch lower(config["type"]) {
	case "node", "node2", "pwa-node":
		return "node"
	case "python", "debugpy":
		return h.python()
	case "ruby", "rdbg":
		return "ruby"
	}
	// program.slice(program.lastIndexOf('.')): with no dot, the last character.
	ext := ""
	if i := strings.LastIndex(program, "."); i >= 0 {
		ext = program[i:]
	} else if program != "" {
		r := []rune(program)
		ext = string(r[len(r)-1])
	}
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".cjs":
		return "node"
	case ".ts", ".mts", ".cts":
		return "tsx"
	case ".py":
		return h.python()
	case ".rb":
		return "ruby"
	case ".sh":
		return "bash"
	case ".php":
		return "php"
	case ".pl":
		return "perl"
	}
	return ""
}
