package gitsvc

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// The simple-git task parsers slice A's TS called through `gitFor(repo)`:
// status(), getRemotes(true), checkIsRepo(), version(). Their quirks are
// kept, because the renderer saw their output.

// ─── status() ───────────────────────────────────────────────────────────────

// aFileStatus is simple-git's FileStatusSummary.
type aFileStatus struct {
	Path       string
	From       string
	Index      string
	WorkingDir string
}

// aStatusSummary is the part of simple-git's StatusResult git.ts read.
type aStatusSummary struct {
	Files      []aFileStatus
	Conflicted []string
	Ahead      int
	Behind     int
	Current    *string
	Tracking   *string
	Detached   bool
}

// aConflictCodes are the XY pairs simple-git files under `conflicted`.
var aConflictCodes = map[string]bool{
	"AA": true, "AU": true, "DD": true, "DU": true, "UA": true, "UD": true, "UU": true,
}

var (
	aSGAheadRe  = regexp.MustCompile(`ahead (\d+)`)
	aSGBehindRe = regexp.MustCompile(`behind (\d+)`)
)

// aParseStatusSummary is simple-git's parseStatusSummary over
// `status --porcelain -b -u --null` output.
func aParseStatusSummary(text string) aStatusSummary {
	var st aStatusSummary
	lines := strings.Split(text, "\x00")
	for i := 0; i < len(lines); {
		line := gitx.TrimJS(lines[i])
		i++
		if line == "" {
			continue
		}
		// Only an index-side rename carries its source path in the next record
		// (simple-git checks this one case).
		if line[0] == 'R' {
			next := ""
			if i < len(lines) {
				next = lines[i]
			}
			i++
			line += "\x00" + next
		}
		aSplitStatusLine(&st, line)
	}
	return st
}

func aSplitStatusLine(st *aStatusSummary, lineStr string) {
	t := gitx.TrimJS(lineStr)
	at := func(i int) byte {
		if i < len(t) {
			return t[i]
		}
		return 0
	}
	var index, work, path string
	switch {
	case at(2) == ' ':
		index, work, path = string(t[0]), string(t[1]), t[3:]
	case at(1) == ' ':
		index, work, path = " ", string(t[0]), t[2:]
	default:
		return
	}
	raw := index + work
	switch {
	case raw == "##":
		aStatusBranchLine(st, path)
	case aConflictCodes[raw]:
		st.Conflicted = append(st.Conflicted, path)
	}
	if raw != "##" && raw != "!!" {
		st.Files = append(st.Files, aNewFileStatus(path, index, work))
	}
}

// aNewFileStatus is FileStatusSummary's constructor: a rename's "to\0from"
// path is split (regex `^(.+)\0(.+)$`, `.` stopping at line terminators).
func aNewFileStatus(path, index, work string) aFileStatus {
	f := aFileStatus{Path: path, Index: index, WorkingDir: work}
	if index == "R" || work == "R" {
		f.From, f.Path = path, path
		if i := strings.LastIndexByte(path, 0); i > 0 && i < len(path)-1 &&
			!strings.ContainsAny(path, "\n\r  ") {
			f.Path, f.From = path[:i], path[i+1:]
		}
	}
	return f
}

// aStatusBranchLine is simple-git's `##` parser.
func aStatusBranchLine(st *aStatusSummary, line string) {
	if m := aSGAheadRe.FindStringSubmatch(line); m != nil {
		st.Ahead, _ = strconv.Atoi(m[1])
	}
	if m := aSGBehindRe.FindStringSubmatch(line); m != nil {
		st.Behind, _ = strconv.Atoi(m[1])
	}
	// /^(.+?(?=(?:\.{3}|\s|$)))/ — the shortest non-empty prefix followed by
	// "...", whitespace or the end.
	st.Current = nil
	for i := 1; i <= len(line); i++ {
		if i == len(line) || strings.HasPrefix(line[i:], "...") || aStartsWithJSSpace(line[i:]) {
			cur := line[:i]
			st.Current = &cur
			break
		}
	}
	// /\.{3}(\S*)/
	st.Tracking = nil
	if i := strings.Index(line, "..."); i >= 0 {
		rest := line[i+3:]
		end := strings.IndexFunc(rest, gitx.IsJSSpace)
		if end < 0 {
			end = len(rest)
		}
		tr := rest[:end]
		st.Tracking = &tr
	}
	// /\son\s(\S+?)(?=\.{3}|$)/ — "No commits yet on main".
	if b, ok := aOnEmptyBranch(line); ok {
		st.Current = &b
	}
	st.Detached = strings.Contains(line, "(no branch)")
}

func aStartsWithJSSpace(s string) bool {
	for _, r := range s {
		return gitx.IsJSSpace(r)
	}
	return false
}

// aOnEmptyBranch searches like the regex engine would: the first position
// where `\son\s` is followed by a run of non-space characters ending at "..."
// or at the end of the line, with the shortest such run.
func aOnEmptyBranch(line string) (string, bool) {
	runes := []rune(line)
	for s := 0; s+3 < len(runes); s++ {
		if !gitx.IsJSSpace(runes[s]) || runes[s+1] != 'o' || runes[s+2] != 'n' || !gitx.IsJSSpace(runes[s+3]) {
			continue
		}
		start := s + 4
		for e := start + 1; e <= len(runes); e++ {
			if gitx.IsJSSpace(runes[e-1]) {
				break
			}
			if e == len(runes) || strings.HasPrefix(string(runes[e:]), "...") {
				return string(runes[start:e]), true
			}
		}
	}
	return "", false
}

// aSimpleStatus is `gitFor(repo).status(extra)`: porcelain v1 with
// `-b -u --null`, parsed the simple-git way.
func aSimpleStatus(ctx context.Context, repoPath string, extra ...string) (aStatusSummary, error) {
	args := append([]string{"status", "--porcelain", "-b", "-u", "--null"}, extra...)
	out, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{})
	if err != nil {
		return aStatusSummary{}, err
	}
	return aParseStatusSummary(out), nil
}

// ─── checkIsRepo() ──────────────────────────────────────────────────────────

var aWhitespaceRe = regexp.MustCompile(`[` + aJSSpace + `]+`)

var aNotRepoRe = regexp.MustCompile(`(?i)(Not a git repository|Kein Git-Repository)`)

// aCheckIsRepo is simple-git's checkIsRepo(): `rev-parse
// --is-inside-work-tree` is "true", and exit 128 with a not-a-repository
// message is a plain false rather than an error.
func aCheckIsRepo(ctx context.Context, repoPath string) (bool, error) {
	out, err := gitx.Raw(ctx, repoPath, []string{"rev-parse", "--is-inside-work-tree"}, gitx.RunOpts{})
	if err != nil {
		var ge *gitx.GitError
		if errors.As(err, &ge) && ge.Exec != nil {
			if code, ok := gitx.ExitCode(ge.Exec); ok && code == 128 && aNotRepoRe.MatchString(ge.Message) {
				return false, nil
			}
		}
		return false, err
	}
	return gitx.TrimJS(out) == "true", nil
}

// ─── getRemotes(true) ───────────────────────────────────────────────────────

type aRemoteRefs struct {
	name        string
	fetch, push string
}

// aParseRemotesVerbose is parseGetRemotesVerbose over `git remote -v`.
// Remotes come out in JS object key order: integer-like names first,
// ascending, then the rest in insertion order.
func aParseRemotesVerbose(text string) []aRemoteRefs {
	byName := map[string]*aRemoteRefs{}
	var order []string
	for _, l := range strings.Split(text, "\n") {
		line := gitx.TrimJS(l)
		if line == "" {
			continue
		}
		parts := aWhitespaceRe.Split(line, -1)
		name, url, purpose := aField(parts, 0), aField(parts, 1), aField(parts, 2)
		r, ok := byName[name]
		if !ok {
			r = &aRemoteRefs{name: name}
			byName[name] = r
			order = append(order, name)
		}
		if purpose != "" && url != "" {
			switch strings.Map(func(c rune) rune {
				if c >= 'a' && c <= 'z' {
					return c
				}
				return -1
			}, purpose) {
			case "fetch":
				r.fetch = url
			case "push":
				r.push = url
			}
		}
	}
	var ints, rest []string
	for _, n := range order {
		if aIsArrayIndex(n) {
			ints = append(ints, n)
		} else {
			rest = append(rest, n)
		}
	}
	sort.Slice(ints, func(i, j int) bool {
		a, _ := strconv.ParseUint(ints[i], 10, 64)
		b, _ := strconv.ParseUint(ints[j], 10, 64)
		return a < b
	})
	out := make([]aRemoteRefs, 0, len(order))
	for _, n := range append(ints, rest...) {
		out = append(out, *byName[n])
	}
	return out
}

// aIsArrayIndex reports whether a JS object key is a canonical array index
// (those are enumerated first, in numeric order).
func aIsArrayIndex(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n < 1<<32-1
}

// ─── version() ──────────────────────────────────────────────────────────────

var (
	aVersionRe    = regexp.MustCompile(`version (\d+)\.(\d+)\.(\d+)(?:[` + aJSSpace + `]*\((` + aJSDot + `+)\))?`)
	aVersionAltRe = regexp.MustCompile(`version (\d+)\.(\d+)\.(\D+)(` + aJSDot + `+)?$`)
)

// aParseVersion is simple-git's version parser: "git version 2.43.0" →
// "2.43.0". Later lines override earlier ones, as its line parsers do.
func aParseVersion(out string) string {
	major, minor, patch := "0", "0", "0"
	num := func(s string) string {
		n, err := strconv.Atoi(s)
		if err != nil {
			return "0"
		}
		return strconv.Itoa(n)
	}
	for _, line := range strings.Split(out, "\n") {
		if m := aVersionRe.FindStringSubmatch(line); m != nil {
			major, minor, patch = num(m[1]), num(m[2]), num(m[3])
		} else if m := aVersionAltRe.FindStringSubmatch(line); m != nil {
			major, minor, patch = num(m[1]), num(m[2]), m[3]
		}
	}
	return major + "." + minor + "." + patch
}
