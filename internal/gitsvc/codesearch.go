package gitsvc

// Code search: the commit panel's file filter, line-level matches in the
// working tree or a commit's tree, `git grep`, and the history pickaxe.
//
// Renderer channels served (through the 'git' dispatcher):
// searchFileContents, searchFileMatches, searchCommitMatches,
// grepWorkingTree, searchHistory.

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// SearchOptions is the `opts` object the search methods take. Every field is
// optional in TS; Max and MaxPerFile are pointers so an absent value takes the
// method's default while an explicit 0 stays 0.
type SearchOptions struct {
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
	WholeWord     bool     `json:"wholeWord,omitempty"`
	Regex         bool     `json:"regex,omitempty"`
	Max           *int     `json:"max,omitempty"`
	MaxPerFile    *int     `json:"maxPerFile,omitempty"`
	Paths         []string `json:"paths,omitempty"`
}

func (o *SearchOptions) orZero() SearchOptions {
	if o == nil {
		return SearchOptions{}
	}
	return *o
}

// CodeSearchHit is CodeSearchHit in src/shared/types.ts.
type CodeSearchHit struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// bHitTextMax caps a hit's text (in UTF-16 units, like `.slice(0, 400)`).
const bHitTextMax = 400

// bReadAll reads each repo-relative file concurrently (Promise.all in TS) and
// hands back the decoded texts in input order; ok is false for a file that
// could not be read.
func bReadAll(repoPath string, files []string) (texts []string, ok []bool) {
	texts = make([]string, len(files))
	ok = make([]bool, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, f := range files {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			text, err := bReadText(filepath.Join(repoPath, f))
			texts[i], ok[i] = text, err == nil
		}(i, f)
	}
	wg.Wait()
	return texts, ok
}

// gitsvc: searchFileContents
//
// Filter the given changed-file paths down to those whose working-tree
// content matches `query`. Used by the commit panel's search bar. Reads files
// directly (only the handful of changed files), so it covers tracked +
// untracked alike. Returns `files` unchanged when the query is empty; `[]` for
// an invalid regex.
func (s *GitService) SearchFileContents(ctx context.Context, repoPath string, files []string, query string, opts *SearchOptions) ([]string, error) {
	return locked(s, ctx, "searchFileContents", repoPath, func(ctx context.Context) ([]string, error) {
		if query == "" {
			return bNonNil(files), nil
		}
		o := opts.orZero()
		pattern, err := bSearchPattern(query, o.CaseSensitive, o.WholeWord, o.Regex)
		if err != nil {
			return []string{}, nil
		}
		texts, ok := bReadAll(repoPath, files)
		matched := []string{}
		for i, f := range files {
			// binary, deleted, or unreadable files simply do not match
			if ok[i] && pattern.MatchString(texts[i]) {
				matched = append(matched, f)
			}
		}
		return matched, nil
	})
}

// gitsvc: searchFileMatches
//
// Like searchFileContents, but returns every matching LINE (file/line/text)
// instead of only the file paths — the data a VSCode-style expandable results
// tree needs. Reads the working-tree copy of each candidate, so it covers
// tracked, staged and untracked files alike.
//
// Capped twice: `maxPerFile` keeps one huge file from burying the rest, `max`
// bounds the whole payload crossing the bridge.
func (s *GitService) SearchFileMatches(ctx context.Context, repoPath string, files []string, query string, opts *SearchOptions) ([]CodeSearchHit, error) {
	return locked(s, ctx, "searchFileMatches", repoPath, func(ctx context.Context) ([]CodeSearchHit, error) {
		if gitx.TrimJS(query) == "" {
			return []CodeSearchHit{}, nil
		}
		o := opts.orZero()
		max := bIntOr(o.Max, 2000)
		maxPerFile := bIntOr(o.MaxPerFile, 100)
		pattern, err := bSearchPattern(query, o.CaseSensitive, o.WholeWord, o.Regex)
		if err != nil {
			return []CodeSearchHit{}, nil
		}
		texts, ok := bReadAll(repoPath, files)
		all := []CodeSearchHit{}
		for i, f := range files {
			if !ok[i] || strings.Contains(texts[i], "\x00") {
				continue // binary, deleted, or unreadable
			}
			lines := strings.Split(texts[i], "\n")
			n := 0
			for li := 0; li < len(lines) && n < maxPerFile; li++ {
				if pattern.MatchString(lines[li]) {
					all = append(all, CodeSearchHit{File: f, Line: li + 1, Text: gitx.SliceUTF16(lines[li], bHitTextMax)})
					n++
				}
			}
		}
		return all[:bSliceEnd(len(all), max)], nil
	})
}

// bGrepRowRe is /^(.*?):(\d+):(.*)$/ — JS `.` stops at \r as well, so a CRLF
// line does not match in either language.
var bGrepRowRe = regexp.MustCompile(`^(` + bJSDot + `*?):(\d+):(` + bJSDot + `*)$`)

func bGrepArgs(base []string, o SearchOptions) []string {
	args := append([]string{}, base...)
	if !o.CaseSensitive {
		args = append(args, "-i")
	}
	if o.WholeWord {
		args = append(args, "-w")
	}
	if o.Regex {
		return append(args, "-E")
	}
	return append(args, "-F")
}

// gitsvc: searchCommitMatches
//
// Same line-level search, but against the tree of a commit/stash (`rev`)
// instead of the working tree — `git grep <pattern> <rev> -- <paths>`. Output
// rows are `rev:path:line:text`, so the rev prefix is stripped off.
func (s *GitService) SearchCommitMatches(ctx context.Context, repoPath, rev, query string, opts *SearchOptions) ([]CodeSearchHit, error) {
	return locked(s, ctx, "searchCommitMatches", repoPath, func(ctx context.Context) ([]CodeSearchHit, error) {
		if gitx.TrimJS(query) == "" {
			return []CodeSearchHit{}, nil
		}
		o := opts.orZero()
		max := bIntOr(o.Max, 2000)
		maxPerFile := bIntOr(o.MaxPerFile, 100)
		args := bGrepArgs([]string{"grep", "-n", "-I", "--no-color", "--full-name"}, o)
		args = append(append(args, "-e", query, rev, "--"), o.Paths...)
		raw, err := bRaw(ctx, repoPath, args...)
		if err != nil {
			return []CodeSearchHit{}, nil // no matches (exit 1) or invalid pattern
		}
		hits := []CodeSearchHit{}
		perFile := map[string]int{}
		prefix := rev + ":"
		for _, line := range strings.Split(raw, "\n") {
			if line == "" || len(hits) >= max {
				break
			}
			body := strings.TrimPrefix(line, prefix)
			m := bGrepRowRe.FindStringSubmatch(body)
			if m == nil {
				continue
			}
			file := m[1]
			if perFile[file] >= maxPerFile {
				continue
			}
			perFile[file]++
			hits = append(hits, CodeSearchHit{File: file, Line: bAtoi(m[2]), Text: gitx.SliceUTF16(m[3], bHitTextMax)})
		}
		return hits, nil
	})
}

func bAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return int(bJSInt(s))
	}
	return n
}

// gitsvc: grepWorkingTree
//
// Working-tree code search via `git grep -n` (tracked + untracked, honouring
// .gitignore). Returns up to `max` file:line:text hits. git grep exits 1 when
// nothing matches — that's a clean empty result.
func (s *GitService) GrepWorkingTree(ctx context.Context, repoPath, query string, opts *SearchOptions) ([]CodeSearchHit, error) {
	return locked(s, ctx, "grepWorkingTree", repoPath, func(ctx context.Context) ([]CodeSearchHit, error) {
		if gitx.TrimJS(query) == "" {
			return []CodeSearchHit{}, nil
		}
		o := opts.orZero()
		max := bIntOr(o.Max, 500)
		args := bGrepArgs([]string{"grep", "-n", "-I", "--no-color", "--untracked", "--full-name"}, o)
		raw, err := bRaw(ctx, repoPath, append(args, "-e", query, "--")...)
		if err != nil {
			return []CodeSearchHit{}, nil // no matches (exit 1) or invalid pattern
		}
		hits := []CodeSearchHit{}
		for _, line := range strings.Split(raw, "\n") {
			if line == "" {
				continue
			}
			m := bGrepRowRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			hits = append(hits, CodeSearchHit{File: m[1], Line: bAtoi(m[2]), Text: gitx.SliceUTF16(m[3], bHitTextMax)})
			// Checked after the push, as in TS: a max of 0 still returns one hit.
			if len(hits) >= max {
				break
			}
		}
		return hits, nil
	})
}

// HistorySearchHit is HistorySearchHit in src/shared/types.ts.
type HistorySearchHit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    int64  `json:"date"`
	Subject string `json:"subject"`
}

// HistorySearchOptions is searchHistory's `opts`.
type HistorySearchOptions struct {
	CaseSensitive bool `json:"caseSensitive,omitempty"`
	Regex         bool `json:"regex,omitempty"`
	Max           *int `json:"max,omitempty"`
}

// gitsvc: searchHistory
//
// History pickaxe: commits that changed the number of occurrences of `query`
// (`-S`, literal) or whose diff matches it (`-G`, regex). The "who introduced
// / removed this string" search.
func (s *GitService) SearchHistory(ctx context.Context, repoPath, query string, opts *HistorySearchOptions) ([]HistorySearchHit, error) {
	return locked(s, ctx, "searchHistory", repoPath, func(ctx context.Context) ([]HistorySearchHit, error) {
		if gitx.TrimJS(query) == "" {
			return []HistorySearchHit{}, nil
		}
		var o HistorySearchOptions
		if opts != nil {
			o = *opts
		}
		args := []string{"log", "--max-count=" + strconv.Itoa(bIntOr(o.Max, 200)),
			"--pretty=format:%H" + gitx.SEP + "%an" + gitx.SEP + "%at" + gitx.SEP + "%s" + gitx.REC}
		if !o.CaseSensitive {
			args = append(args, "--regexp-ignore-case")
		}
		if o.Regex {
			args = append(args, "-G"+query)
		} else {
			args = append(args, "-S"+query)
		}
		raw, err := bRaw(ctx, repoPath, args...)
		if err != nil {
			return []HistorySearchHit{}, nil
		}
		hits := []HistorySearchHit{}
		for _, rec := range strings.Split(raw, gitx.REC) {
			rec = gitx.TrimJS(rec)
			if rec == "" {
				continue
			}
			f := bFields(rec, gitx.SEP, 4)
			hits = append(hits, HistorySearchHit{Hash: f[0], Author: f[1], Date: bJSInt(f[2]), Subject: f[3]})
		}
		return hits, nil
	})
}
