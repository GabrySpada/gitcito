package gitx

import (
	"regexp"
	"strings"
)

// CheckUnsafe ports simple-git's blockUnsafeOperationsPlugin (argv half,
// @simple-git/argv-parser 'vulnerabilityCheck'), which ran in front of every
// `gitFor(repo).raw(...)` in TS. It refuses arguments that make git execute a
// program of the caller's choosing — `-c core.editor=…`, `--upload-pack`,
// `config core.hooksPath …` — unless the call site enabled that category.
// The first vulnerability whose category is not allowed is returned, with
// simple-git's message, exactly as GitPluginError carried it.
//
// The env half of the guard only inspected an env object handed to `.env()`,
// which git.ts never did (it passed `-c` args instead), so it is not ported.
func CheckUnsafe(args []string, allow ...string) error {
	allowed := make(map[string]bool, len(allow))
	for _, a := range allow {
		allowed[a] = true
	}
	for _, v := range vulnerabilities(args) {
		if !allowed[v.category] {
			return &GitError{Message: v.message}
		}
	}
	return nil
}

type vulnerability struct{ category, message string }

type flagSpec struct {
	short map[byte]bool // flag letter → takes a value
	long  map[string]bool
}

type parsedFlag struct {
	name     string
	value    string
	hasValue bool
	global   bool
}

var globalFlags = flagSpec{
	short: map[byte]bool{'C': true, 'P': false, 'h': false, 'p': false, 'v': false, 'c': true},
	long: set("attr-source", "config-env", "exec-path", "git-dir", "list-cmds", "namespace",
		"super-prefix", "work-tree"),
}

var taskFlags = map[string]flagSpec{
	"clone": {
		short: map[byte]bool{'b': true, 'j': true, 'l': false, 'n': false, 'o': true, 'q': false, 's': false, 'u': true},
		long:  set("branch", "config", "jobs", "origin", "upload-pack", "u", "template"),
	},
	"commit": {
		short: map[byte]bool{'C': true, 'F': true, 'c': true, 'm': true, 't': true},
		long:  set("file", "message", "reedit-message", "reuse-message", "template"),
	},
	"config": {
		short: map[byte]bool{'e': false, 'f': true, 'l': false},
		long:  set("blob", "comment", "default", "file", "type", "value"),
	},
	"fetch": {long: set("upload-pack")},
	"init":  {long: set("template")},
	"pull":  {long: set("upload-pack")},
	"push":  {long: set("exec", "receive-pack")},
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// specFor merges `-c` (always a value-taking short flag) with the task's own.
func specFor(task string) flagSpec {
	t := taskFlags[task]
	short := map[byte]bool{'c': true}
	for k, v := range t.short {
		short[k] = v
	}
	long := t.long
	if long == nil {
		long = map[string]bool{}
	}
	return flagSpec{short: short, long: long}
}

type flagToken struct {
	name      string
	value     string
	hasValue  bool
	needsNext bool
}

// splitFlag parses one `-x` / `--xx[=v]` / `-abc` token.
func splitFlag(arg string, spec flagSpec) []flagToken {
	if strings.HasPrefix(arg, "--") {
		if n := strings.IndexByte(arg, '='); n > 2 {
			return []flagToken{{name: arg[:n], value: arg[n+1:], hasValue: true}}
		}
		return []flagToken{{name: arg, needsNext: spec.long[arg[2:]]}}
	}
	if len(arg) == 2 {
		return []flagToken{{name: arg, needsNext: spec.short[arg[1]]}}
	}
	return splitShortCluster(arg, spec.short)
}

func splitShortCluster(arg string, short map[byte]bool) []flagToken {
	chars := arg[1:]
	var out []flagToken
	for i := 0; i < len(chars); i++ {
		r := chars[i]
		takes, known := short[r]
		if !known {
			return []flagToken{{name: arg}}
		}
		if takes {
			rest := chars[i+1:]
			allKnown := true
			for j := 0; j < len(rest); j++ {
				if _, ok := short[rest[j]]; !ok {
					allKnown = false
					break
				}
			}
			if rest != "" && !allKnown {
				return append(out, flagToken{name: "-" + string(r), value: rest, hasValue: true})
			}
		}
		out = append(out, flagToken{name: "-" + string(r), needsNext: takes})
	}
	return out
}

func vulnerabilities(args []string) []vulnerability {
	// Global flags, before the task name.
	var flags []parsedFlag
	n := 0
	for n < len(args) {
		a := args[n]
		if !strings.HasPrefix(a, "-") || len(a) < 2 {
			break
		}
		next := n + 1
		for _, tok := range splitFlag(a, globalFlags) {
			f := parsedFlag{name: tok.name, value: tok.value, hasValue: tok.hasValue, global: true}
			if tok.needsNext && !f.hasValue && next < len(args) {
				f.value, f.hasValue = args[next], true
				next++
			}
			flags = append(flags, f)
		}
		n = next
	}
	task := ""
	hasTask := n < len(args)
	var rest []string
	if hasTask {
		task = strings.ToLower(args[n])
		rest = args[n+1:]
	}

	// Task flags and positionals.
	spec := specFor(task)
	var positionals []string
	for a := 0; a < len(rest); {
		f := rest[a]
		if f == "--" {
			break
		}
		if !strings.HasPrefix(f, "-") || len(f) < 2 {
			positionals = append(positionals, f)
			a++
			continue
		}
		next := a + 1
		for _, tok := range splitFlag(f, spec) {
			pf := parsedFlag{name: tok.name, value: tok.value, hasValue: tok.hasValue}
			if tok.needsNext && !pf.hasValue && next < len(rest) {
				pf.value, pf.hasValue = rest[next], true
				next++
			}
			flags = append(flags, pf)
		}
		a = next
	}

	// Config writes: inline `-c k=v` / `--config-env=k=v` anywhere, plus a
	// `git config` invocation that writes.
	var writes []string
	for _, f := range flags {
		if f.name != "-c" && f.name != "--config" && f.name != "--config-env" {
			continue
		}
		if !f.hasValue {
			continue
		}
		// `e?.indexOf("=") || -1`: a '=' at index 0 counts as absent.
		eq := strings.IndexByte(f.value, '=')
		if f.value == "" || eq <= 0 {
			continue
		}
		writes = append(writes, strings.ToLower(TrimJS(f.value[:eq])))
	}
	if task == "config" {
		if key, isWrite, ok := configAccess(flags, positionals); ok && isWrite {
			writes = append(writes, key)
		}
	}

	var out []vulnerability
	if hasTask || len(flags) > 0 {
		for _, f := range flags {
			for _, rule := range argRules {
				if (rule.task == "" || (hasTask && task == rule.task)) && rule.re.MatchString(f.name) {
					out = append(out, vulnerability{rule.category, rule.message})
				}
			}
		}
	}
	for _, key := range writes {
		for _, rule := range configRules {
			if rule.re.MatchString(key) {
				out = append(out, vulnerability{rule.category, rule.message})
			}
		}
	}
	return out
}

var (
	configWriteFlags = set("--add", "--edit", "--remove-section", "--rename-section", "--replace-all", "--unset", "--unset-all", "-e")
	configReadFlags  = set("--get", "--get-all", "--get-color", "--get-colorbool", "--get-regexp", "--get-urlmatch", "--list", "-l")
	configWriteVerbs = set("edit", "remove-section", "rename-section", "set", "unset")
	configReadVerbs  = set("get", "get-color", "get-colorbool", "list")
)

// configAccess decides whether `git config …` reads or writes, and which key.
func configAccess(flags []parsedFlag, pos []string) (key string, isWrite, ok bool) {
	access := func(write bool, p []string) (string, bool, bool) {
		if len(p) == 0 {
			return "", false, false
		}
		return strings.ToLower(p[0]), write, true
	}
	for _, f := range flags {
		if f.global {
			continue
		}
		if configWriteFlags[f.name] {
			return access(true, pos)
		}
		if configReadFlags[f.name] {
			return access(false, pos)
		}
	}
	if len(pos) == 0 {
		return "", false, false
	}
	verb := strings.ToLower(pos[0])
	switch {
	case configWriteVerbs[verb]:
		return access(true, pos[1:])
	case configReadVerbs[verb]:
		return access(false, pos[1:])
	case len(pos) == 1:
		return access(false, pos)
	default:
		return access(true, pos)
	}
}

type rule struct {
	task     string
	re       *regexp.Regexp
	category string
	message  string
}

// configRule mirrors argv-parser's `c(...)`: an unanchored `\s*<key>` match
// (so "alias" catches any key containing it, and '.' is a regex any-char).
func configRule(key, category string) rule {
	return rule{re: regexp.MustCompile(`[` + jsSpaceClass + `]*` + strings.ToLower(key)), category: category,
		message: "Configuring " + key + " is not permitted without enabling " + category}
}

// configRuleSub mirrors `i(...)`: every '.' may have a subsection in between,
// so `credential.helper` also catches `credential.https://x.helper`.
func configRuleSub(key, category string) rule {
	pat := strings.ReplaceAll(strings.ToLower(key), ".", "(..+)?.")
	return rule{re: regexp.MustCompile(`[` + jsSpaceClass + `]*` + pat), category: category,
		message: "Configuring " + key + " is not permitted without enabling " + category}
}

var configRules = []rule{
	configRule("alias", "allowUnsafeAlias"),
	configRule("core.askPass", "allowUnsafeAskPass"),
	configRule("core.editor", "allowUnsafeEditor"),
	configRule("core.fsmonitor", "allowUnsafeFsMonitor"),
	configRule("core.gitProxy", "allowUnsafeGitProxy"),
	configRule("core.hooksPath", "allowUnsafeHooksPath"),
	configRule("core.pager", "allowUnsafePager"),
	configRule("core.sshCommand", "allowUnsafeSshCommand"),
	configRuleSub("credential.helper", "allowUnsafeCredentialHelper"),
	configRuleSub("diff.command", "allowUnsafeDiffExternal"),
	configRule("diff.external", "allowUnsafeDiffExternal"),
	configRuleSub("diff.textconv", "allowUnsafeDiffTextConv"),
	configRuleSub("filter.clean", "allowUnsafeFilter"),
	configRuleSub("filter.smudge", "allowUnsafeFilter"),
	configRuleSub("gpg.program", "allowUnsafeGpgProgram"),
	configRule("init.templateDir", "allowUnsafeTemplateDir"),
	configRuleSub("merge.driver", "allowUnsafeMergeDriver"),
	configRuleSub("mergetool.path", "allowUnsafeMergeDriver"),
	configRuleSub("mergetool.cmd", "allowUnsafeMergeDriver"),
	configRuleSub("protocol.allow", "allowUnsafeProtocolOverride"),
	configRuleSub("remote.receivepack", "allowUnsafePack"),
	configRuleSub("remote.uploadpack", "allowUnsafePack"),
	configRule("sequence.editor", "allowUnsafeEditor"),
}

// argRule mirrors `w(task, pattern, category, label)`.
func argRule(task string, re *regexp.Regexp, category, label string) rule {
	prefix := ""
	if task != "" {
		prefix = task + " with option "
	}
	return rule{task: task, re: re, category: category,
		message: "Use of " + prefix + label + " is not permitted without enabling " + category}
}

func argRuleLit(task, lit, category string) rule {
	return argRule(task, regexp.MustCompile(`[`+jsSpaceClass+`]*`+regexp.QuoteMeta(strings.ToLower(lit))), category, lit)
}

var argRules = []rule{
	argRule("", regexp.MustCompile(`--(upload|receive)-pack`), "allowUnsafePack", "--upload-pack or --receive-pack"),
	argRule("clone", regexp.MustCompile(`^-\w*u`), "allowUnsafePack", `/^-\w*u/`),
	argRuleLit("clone", "--u", "allowUnsafePack"),
	argRuleLit("push", "--exec", "allowUnsafePack"),
	argRuleLit("", "--template", "allowUnsafeTemplateDir"),
}
