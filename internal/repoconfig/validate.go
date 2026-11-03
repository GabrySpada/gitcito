package repoconfig

// Validation for `.gitcito.json` — src/shared/repoConfig.ts.
//
// The governing rule is that this file is **untrusted repository content**, no
// different from a commit message:
//   - Every field is inert data or an added restriction. There is no field
//     that runs a command, and none that can switch a guard off.
//   - Every list and every string is capped. A hostile repo should not be able
//     to paste a wall of text into a confirmation dialog or a thousand chips
//     into the sidebar.
//   - Anything that becomes a path is checked for escape, and anything that
//     becomes a link is checked for scheme.
//
// Validation never fails the whole file for one bad field: offending entries
// are dropped, an issue is recorded, and the rest applies. A config that fails
// wholesale would take a working repository's guards with it, which is the
// wrong failure direction.
//
// It works on the generic JSON value (what JSON.parse produced in TS) so the
// type checks, the issue paths and their order are the TypeScript ones.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

const (
	File    = ".gitcito.json"
	Version = 1
)

// Caps. Generous for any honest repository, small enough that the worst a
// hostile one achieves is a slightly crowded panel.
const (
	limitProtect   = 50
	limitScopes    = 100
	limitTickets   = 10
	limitTrailers  = 10
	limitFiles     = 20
	limitChecklist = 20
	limitLine      = 200 // free text shown in a dialog
	limitShort     = 200 // anything that becomes a chip, a path or a value
	limitPattern   = 120 // a regular expression source
)

type issueList []RepoConfigIssue

func (is *issueList) push(field, code string) {
	*is = append(*is, RepoConfigIssue{Field: field, Code: code})
}

func isRecord(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

var driveRe = regexp.MustCompile(`^[a-zA-Z]:`)
var pathSegRe = regexp.MustCompile(`[/\\]`)

// IsSafeRepoRelPath accepts a repo-relative path that cannot leave the
// repository: no absolute paths, `..` or `.git` segments, `~`, drive letters,
// UNC roots or NUL bytes. Everything the config can name is joined onto the
// repo root, so this is the only thing standing between a config field and
// the rest of the filesystem.
func IsSafeRepoRelPath(value string) bool {
	if value == "" || gitx.UTF16Len(value) > limitShort || strings.Contains(value, "\x00") {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) || strings.HasPrefix(value, "~") {
		return false
	}
	if driveRe.MatchString(value) {
		return false
	}
	for _, seg := range pathSegRe.Split(value, -1) {
		if seg == ".." || seg == ".git" {
			return false
		}
	}
	return true
}

var linkURLRe = regexp.MustCompile(`(?i)^https?://[^` + jsSpace + `]+$`)

// IsSafeLinkURL: only http(s) links are ever handed to the shell's opener.
func IsSafeLinkURL(value string) bool {
	return value != "" && gitx.UTF16Len(value) <= limitShort && linkURLRe.MatchString(value)
}

var (
	lookaroundRe = regexp.MustCompile(`\(\?(?:<=|<!|=|!)`)
	backrefRe    = regexp.MustCompile(`\\[1-9][0-9]*`)
	goOnlyRe     = regexp.MustCompile(`\(\?(?:P<|[imsU-]+[:)])`)
)

// IsSafePattern is whether a pattern is a regular expression this build will
// run — `new RegExp(source)` not throwing. Go's RE2 is the judge, adjusted for
// the two dialects' main differences: JS lookarounds and back-references are
// valid (RE2 refuses them), Go's inline flag groups and `(?P<` are not (JS
// refuses them). Corner cases beyond that (RE2's 1000-repeat cap, Annex B
// escapes) can disagree; the renderer re-validates with the real engine.
func IsSafePattern(source string) bool {
	if source == "" || gitx.UTF16Len(source) > limitPattern {
		return false
	}
	if goOnlyRe.MatchString(source) {
		return false
	}
	if _, err := syntax.Parse(source, syntax.Perl); err == nil {
		return true
	}
	relaxed := backrefRe.ReplaceAllString(lookaroundRe.ReplaceAllString(source, "(?:"), "x")
	_, err := syntax.Parse(relaxed, syntax.Perl)
	return err == nil
}

func readStrings(value any, present bool, field string, limit int, is *issueList, maxLen int) []string {
	if !present {
		return nil
	}
	arr, ok := value.([]any)
	if !ok {
		is.push(field, "type")
		return nil
	}
	if len(arr) > limit {
		is.push(field, "limit")
		arr = arr[:limit]
	}
	var out []string
	for i, raw := range arr {
		s, ok := raw.(string)
		if !ok {
			is.push(fmt.Sprintf("%s[%d]", field, i), "type")
			continue
		}
		s = gitx.TrimJS(s)
		if s == "" {
			continue
		}
		if gitx.UTF16Len(s) > maxLen {
			is.push(fmt.Sprintf("%s[%d]", field, i), "limit")
			continue
		}
		out = append(out, s)
	}
	return out
}

func readBool(value any, present bool, field string, is *issueList) *bool {
	if !present {
		return nil
	}
	b, ok := value.(bool)
	if !ok {
		is.push(field, "type")
		return nil
	}
	return &b
}

func readShortString(value any, present bool, field string, is *issueList) string {
	if !present {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		is.push(field, "type")
		return ""
	}
	s = gitx.TrimJS(s)
	if s == "" {
		return ""
	}
	if gitx.UTF16Len(s) > limitShort {
		is.push(field, "limit")
		return ""
	}
	return s
}

func get(m map[string]any, key string) (any, bool) {
	v, ok := m[key]
	return v, ok
}

func readLinks(value any, present bool, is *issueList) []RepoConfigLink {
	if !present {
		return nil
	}
	arr, ok := value.([]any)
	if !ok {
		is.push("links.tickets", "type")
		return nil
	}
	if len(arr) > limitTickets {
		is.push("links.tickets", "limit")
		arr = arr[:limitTickets]
	}
	var out []RepoConfigLink
	for i, raw := range arr {
		at := fmt.Sprintf("links.tickets[%d]", i)
		rec, ok := isRecord(raw)
		if !ok {
			is.push(at, "type")
			continue
		}
		mv, mp := get(rec, "match")
		uv, up := get(rec, "url")
		lv, lp := get(rec, "label")
		match := readShortString(mv, mp, at+".match", is)
		url := readShortString(uv, up, at+".url", is)
		label := readShortString(lv, lp, at+".label", is)
		if match == "" || url == "" {
			continue
		}
		if !IsSafePattern(match) {
			is.push(at+".match", "regex")
			continue
		}
		if !IsSafeLinkURL(url) {
			is.push(at+".url", "url")
			continue
		}
		out = append(out, RepoConfigLink{Match: match, URL: url, Label: label})
	}
	return out
}

func readFileReqs(value any, present bool, is *issueList) []RepoConfigFileReq {
	if !present {
		return nil
	}
	arr, ok := value.([]any)
	if !ok {
		is.push("requires.files", "type")
		return nil
	}
	if len(arr) > limitFiles {
		is.push("requires.files", "limit")
		arr = arr[:limitFiles]
	}
	var out []RepoConfigFileReq
	for i, raw := range arr {
		at := fmt.Sprintf("requires.files[%d]", i)
		// A bare string is the common case ("this file must exist"), so accept it.
		var rec map[string]any
		switch v := raw.(type) {
		case string:
			rec = map[string]any{"path": v}
		case map[string]any:
			rec = v
		default:
			is.push(at, "type")
			continue
		}
		pv, pp := get(rec, "path")
		fv, fp := get(rec, "from")
		wv, wp := get(rec, "why")
		path := readShortString(pv, pp, at+".path", is)
		from := readShortString(fv, fp, at+".from", is)
		why := readShortString(wv, wp, at+".why", is)
		if path == "" {
			continue
		}
		if !IsSafeRepoRelPath(path) {
			is.push(at+".path", "unsafe")
			continue
		}
		if from != "" && !IsSafeRepoRelPath(from) {
			is.push(at+".from", "unsafe")
			continue
		}
		out = append(out, RepoConfigFileReq{Path: path, From: from, Why: why})
	}
	return out
}

var topLevel = map[string]bool{"version": true, "protect": true, "links": true, "commit": true, "requires": true, "checklist": true}

// Validate validates a parsed JSON value against the v1 schema.
// keys are the top-level keys in JavaScript's Object.keys order. It returns
// the config with every rejected field dropped, plus what was rejected; the
// config is nil only when the document is not an object or carries a version
// this build does not understand — a version bump must not make an old Gitcito
// guess at fields it has never seen.
func Validate(value any, keys []string) (*RepoConfig, []RepoConfigIssue) {
	is := issueList{}
	rec, ok := isRecord(value)
	if !ok {
		is.push("version", "type")
		return nil, is
	}
	if v, ok := rec["version"].(float64); !ok || v != Version {
		is.push("version", "version")
		return nil, is
	}
	for _, k := range keys {
		if !topLevel[k] {
			is.push(k, "unknown")
		}
	}
	config := &RepoConfig{Version: Version}

	pv, pp := get(rec, "protect")
	config.Protect = readStrings(pv, pp, "protect", limitProtect, &is, limitShort)

	if lv, ok := get(rec, "links"); ok {
		if links, ok := isRecord(lv); !ok {
			is.push("links", "type")
		} else {
			tv, tp := get(links, "tickets")
			if tickets := readLinks(tv, tp, &is); len(tickets) > 0 {
				config.Links = &RepoConfigLinks{Tickets: tickets}
			}
		}
	}

	if cv, ok := get(rec, "commit"); ok {
		if c, ok := isRecord(cv); !ok {
			is.push("commit", "type")
		} else {
			sv, sp := get(c, "scopes")
			tv, tp := get(c, "trailers")
			bv, bp := get(c, "ticketFromBranch")
			commit := RepoConfigCommit{
				Scopes:   readStrings(sv, sp, "commit.scopes", limitScopes, &is, limitShort),
				Trailers: readStrings(tv, tp, "commit.trailers", limitTrailers, &is, limitShort),
			}
			commit.TicketFromBranch = readBool(bv, bp, "commit.ticketFromBranch", &is)
			if len(commit.Scopes) > 0 || len(commit.Trailers) > 0 || commit.TicketFromBranch != nil {
				config.Commit = &commit
			}
		}
	}

	if rv, ok := get(rec, "requires"); ok {
		if r, ok := isRecord(rv); !ok {
			is.push("requires", "type")
		} else {
			req := RepoConfigRequires{}
			nv, np := get(r, "node")
			req.Node = readShortString(nv, np, "requires.node", &is)
			sv, sp := get(r, "submodules")
			req.Submodules = readBool(sv, sp, "requires.submodules", &is)
			lv, lp := get(r, "lfs")
			req.Lfs = readBool(lv, lp, "requires.lfs", &is)
			hv, hp := get(r, "hooksPath")
			if hooks := readShortString(hv, hp, "requires.hooksPath", &is); hooks != "" {
				if IsSafeRepoRelPath(hooks) {
					req.HooksPath = hooks
				} else {
					is.push("requires.hooksPath", "unsafe")
				}
			}
			fv, fp := get(r, "files")
			req.Files = readFileReqs(fv, fp, &is)
			if req.Node != "" || req.Submodules != nil || req.Lfs != nil || req.HooksPath != "" || len(req.Files) > 0 {
				config.Requires = &req
			}
		}
	}

	if cv, ok := get(rec, "checklist"); ok {
		if c, ok := isRecord(cv); !ok {
			is.push("checklist", "type")
		} else {
			pv, pp := get(c, "push")
			if push := readStrings(pv, pp, "checklist.push", limitChecklist, &is, limitLine); len(push) > 0 {
				config.Checklist = &RepoConfigChecklist{Push: push}
			}
		}
	}
	return config, is
}

// Parse parses and validates raw file contents. Invalid JSON yields
// a single issue.
func Parse(raw string) (*RepoConfig, []RepoConfigIssue) {
	value, keys, err := ParseJSONOrdered([]byte(raw))
	if err != nil {
		return nil, []RepoConfigIssue{{Field: File, Code: "json"}}
	}
	return Validate(value, keys)
}

// ParseJSONOrdered is JSON.parse plus the top-level object's keys in
// Object.keys order: array-index keys first in ascending numeric order, then
// the rest in first-insertion order (a repeated key keeps its first place and
// its last value, as JSON.parse does).
func ParseJSONOrdered(raw []byte) (any, []string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return value, nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	var keys []string
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, _ := t.(string)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, nil, err
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	var index, named []string
	for _, k := range keys {
		if isArrayIndex(k) {
			index = append(index, k)
		} else {
			named = append(named, k)
		}
	}
	sort.SliceStable(index, func(i, j int) bool {
		a, _ := strconv.ParseUint(index[i], 10, 64)
		b, _ := strconv.ParseUint(index[j], 10, 64)
		return a < b
	})
	return value, append(index, named...), nil
}

// isArrayIndex: a canonical decimal integer below 2^32-1.
func isArrayIndex(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 1<<32-1
}

var majorRe = regexp.MustCompile(`(\d+)`)

// SatisfiesNodeRange says whether an installed Node version satisfies a
// `requires.node` spec. Deliberately a fraction of semver: the specs that
// appear in a `.nvmrc` or `engines.node` in practice — `20`, `20.x`,
// `v20.11.1`, `>=20`, `^20.1`, `~20.1`. An unrecognised spec passes rather
// than inventing a failure the reader cannot act on.
func SatisfiesNodeRange(actual, spec string) bool {
	have, ok := major(actual)
	if !ok {
		return false
	}
	s := gitx.TrimJS(spec)
	wanted, ok := major(s)
	if !ok {
		return true // unparseable spec — say nothing rather than lie
	}
	switch {
	case strings.HasPrefix(s, ">=") || strings.HasPrefix(s, "^") || strings.HasPrefix(s, "~"):
		return have >= wanted
	case strings.HasPrefix(s, ">"):
		return have > wanted
	case strings.HasPrefix(s, "<="):
		return have <= wanted
	case strings.HasPrefix(s, "<"):
		return have < wanted
	}
	return have == wanted
}

func major(v string) (float64, bool) {
	m := majorRe.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	return n, err == nil
}

// Empty is an empty, valid config — what the editor starts from on a
// repository without one.
func Empty() RepoConfig {
	return RepoConfig{Version: Version}
}

// IsEmpty is isRepoConfigEmpty: no config at all, or one that sets nothing
// but its version.
func IsEmpty(c *RepoConfig) bool {
	if c == nil {
		return true
	}
	return len(c.Protect) == 0 && c.Links == nil && c.Commit == nil && c.Requires == nil && c.Checklist == nil
}
