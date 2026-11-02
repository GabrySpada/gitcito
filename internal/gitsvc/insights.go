package gitsvc

// Insights and stats: activity per day, hotspots and churn, contributors, the
// per-commit streams behind RepoCosmos and the timelapse, and the
// conventional-commit changelog.
//
// Renderer channels served (through the 'git' dispatcher): repoStats,
// repoInsights, contributors, cosmosData, timelapseData, generateChangelog.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// RepoStatsDay is one RepoStats.perDay entry.
type RepoStatsDay struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// RepoStatsAuthor is one RepoStats.authors entry.
type RepoStatsAuthor struct {
	Name    string `json:"name"`
	Commits int    `json:"commits"`
}

// RepoStats is RepoStats in src/shared/types.ts.
type RepoStats struct {
	TotalCommits int `json:"totalCommits"`
	// First is unix seconds of the oldest commit in range, 0 if none.
	First int64 `json:"first"`
	// Last is unix seconds of the newest commit, 0 if none.
	Last    int64             `json:"last"`
	PerDay  []RepoStatsDay    `json:"perDay"`
	Authors []RepoStatsAuthor `json:"authors"`
}

// bLocalDay is the YYYY-MM-DD key repoStats builds from local time
// (`getFullYear()` is not padded, month and day are).
func bLocalDay(ts int64) string {
	d := time.Unix(ts, 0).In(time.Local)
	return fmt.Sprintf("%d-%02d-%02d", d.Year(), int(d.Month()), d.Day())
}

// gitsvc: repoStats
//
// Per-day commit counts and author tallies from the repo's history.
// sinceDays 0 = whole history.
func (s *GitService) RepoStats(ctx context.Context, repoPath string, sinceDays int) (RepoStats, error) {
	return locked(s, ctx, "repoStats", repoPath, func(ctx context.Context) (RepoStats, error) {
		args := []string{"log", "--no-merges", "--pretty=format:%at" + gitx.SEP + "%an"}
		if sinceDays > 0 {
			args = append(args, "--since="+strconv.Itoa(sinceDays)+".days.ago")
		}
		out := bRawOr(ctx, repoPath, "", args...)
		perDay := map[string]int{}
		var days []string
		authors := map[string]int{}
		var names []string
		var stats RepoStats
		for _, line := range strings.Split(out, "\n") {
			at, name, _ := strings.Cut(line, gitx.SEP)
			ts := bJSInt(at)
			if ts == 0 {
				continue
			}
			if name = gitx.TrimJS(name); name == "" {
				name = "Unknown"
			}
			stats.TotalCommits++
			if stats.Last == 0 || ts > stats.Last {
				stats.Last = ts
			}
			if stats.First == 0 || ts < stats.First {
				stats.First = ts
			}
			key := bLocalDay(ts)
			if _, ok := perDay[key]; !ok {
				days = append(days, key)
			}
			perDay[key]++
			if _, ok := authors[name]; !ok {
				names = append(names, name)
			}
			authors[name]++
		}
		stats.PerDay = make([]RepoStatsDay, 0, len(days))
		for _, d := range days {
			stats.PerDay = append(stats.PerDay, RepoStatsDay{Date: d, Count: perDay[d]})
		}
		bStableSortBy(stats.PerDay, func(a, b RepoStatsDay) bool { return bLocaleCompare(a.Date, b.Date) < 0 })
		stats.Authors = make([]RepoStatsAuthor, 0, len(names))
		for _, n := range names {
			stats.Authors = append(stats.Authors, RepoStatsAuthor{Name: n, Commits: authors[n]})
		}
		bStableSortBy(stats.Authors, func(a, b RepoStatsAuthor) bool { return a.Commits > b.Commits })
		return stats, nil
	})
}

// Contributor is one entry of contributors(): a distinct commit author.
type Contributor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// gitsvc: contributors
//
// Distinct commit authors (name + email), most-frequent first — for the
// composer's co-author picker. max nil (undefined) is 100.
func (s *GitService) Contributors(ctx context.Context, repoPath string, max *int) ([]Contributor, error) {
	return locked(s, ctx, "contributors", repoPath, func(ctx context.Context) ([]Contributor, error) {
		out := bRawOr(ctx, repoPath, "", "log", "--no-merges", "-5000", "--pretty=format:%an"+gitx.SEP+"%ae")
		type counted struct {
			c Contributor
			n int
		}
		var list []*counted
		byEmail := map[string]*counted{}
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(line, gitx.SEP)
			if len(f) < 2 || f[0] == "" || f[1] == "" {
				continue
			}
			key := strings.ToLower(f[1])
			if e := byEmail[key]; e != nil {
				e.n++
				continue
			}
			e := &counted{c: Contributor{Name: f[0], Email: f[1]}, n: 1}
			byEmail[key] = e
			list = append(list, e)
		}
		bStableSortBy(list, func(a, b *counted) bool { return a.n > b.n })
		list = list[:bSliceEnd(len(list), bIntOr(max, 100))]
		people := make([]Contributor, 0, len(list))
		for _, e := range list {
			people = append(people, e.c)
		}
		return people, nil
	})
}

// FileHotspot is FileHotspot in src/shared/types.ts.
type FileHotspot struct {
	Path    string `json:"path"`
	Commits int    `json:"commits"`
	Added   int64  `json:"added"`
	Removed int64  `json:"removed"`
}

// AuthorStat is AuthorStat in src/shared/types.ts.
type AuthorStat struct {
	Name    string `json:"name"`
	Commits int    `json:"commits"`
	Added   int64  `json:"added"`
	Removed int64  `json:"removed"`
}

// ChurnPoint is ChurnPoint in src/shared/types.ts: a weekly bucket.
type ChurnPoint struct {
	// Week is the ISO date of the week's Monday.
	Week    string `json:"week"`
	Added   int64  `json:"added"`
	Removed int64  `json:"removed"`
	Commits int    `json:"commits"`
}

// RepoInsights is RepoInsights in src/shared/types.ts.
type RepoInsights struct {
	TotalCommits int           `json:"totalCommits"`
	First        int64         `json:"first"`
	Last         int64         `json:"last"`
	FilesTouched int           `json:"filesTouched"`
	Authors      []AuthorStat  `json:"authors"`
	Hotspots     []FileHotspot `json:"hotspots"`
	Churn        []ChurnPoint  `json:"churn"`
}

var (
	bRenameBraceRe = regexp.MustCompile(`\{[^}]*=>[` + bJSSpace + `]*([^}]*)\}`)
	bRenameArrowRe = regexp.MustCompile(bJSDot + `*=>[` + bJSSpace + `]*`)
)

// bJSSpace is the body of a character class matching JavaScript's \s.
const bJSSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// bNumstatRow parses "<added>\t<removed>\t<path>" ("-" for binary, counted
// as 0). For renames numstat shows "old => new" (or "{a => b}/c"); the new
// path is kept. ok is false for a row that is not a numstat row.
func bNumstatRow(line string) (path string, added, removed int64, ok bool) {
	parts := strings.Split(line, "\t")
	if len(parts) < 3 {
		return "", 0, 0, false
	}
	if parts[0] != "-" {
		added = bJSInt(parts[0])
	}
	if parts[1] != "-" {
		removed = bJSInt(parts[1])
	}
	path = strings.Join(parts[2:], "\t")
	if strings.Contains(path, "=>") {
		path = bRenameBraceRe.ReplaceAllString(path, "${1}")
		path = gitx.TrimJS(bReplaceFirst(bRenameArrowRe, path, ""))
	}
	return path, added, removed, true
}

// bWeekOf is the ISO Monday (UTC) of the week containing ts, as YYYY-MM-DD.
func bWeekOf(ts int64) string {
	d := time.Unix(ts, 0).UTC()
	day := (int(d.Weekday()) + 6) % 7 // 0 = Monday
	d = d.AddDate(0, 0, -day)
	return fmt.Sprintf("%d-%02d-%02d", d.Year(), int(d.Month()), d.Day())
}

// gitsvc: repoInsights
//
// Rich repository insights from one `git log --numstat` pass: per-file change
// frequency + churn (hotspots), per-author contribution totals, and a weekly
// churn timeline. Renames are followed; binary files (numstat "-") count as a
// touch but contribute no line counts.
func (s *GitService) RepoInsights(ctx context.Context, repoPath string, sinceDays int) (RepoInsights, error) {
	return locked(s, ctx, "repoInsights", repoPath, func(ctx context.Context) (RepoInsights, error) {
		// \x01 prefixes each commit header so it's distinguishable from numstat rows.
		args := []string{"log", "--no-merges", "--numstat", "-M", "--pretty=format:\x01%at" + gitx.SEP + "%an"}
		if sinceDays > 0 {
			args = append(args, "--since="+strconv.Itoa(sinceDays)+".days.ago")
		}
		out := bRawOr(ctx, repoPath, "", args...)

		var ins RepoInsights
		var authors []*AuthorStat
		authorBy := map[string]*AuthorStat{}
		var files []*FileHotspot
		fileBy := map[string]*FileHotspot{}
		var weeks []*ChurnPoint
		weekBy := map[string]*ChurnPoint{}
		curAuthor, curWeek := "Unknown", ""

		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "\x01") {
				f := bFields(line[1:], gitx.SEP, 2)
				curTs := bJSInt(f[0])
				if curAuthor = gitx.TrimJS(f[1]); curAuthor == "" {
					curAuthor = "Unknown"
				}
				curWeek = bWeekOf(curTs)
				ins.TotalCommits++
				if ins.Last == 0 || curTs > ins.Last {
					ins.Last = curTs
				}
				if ins.First == 0 || curTs < ins.First {
					ins.First = curTs
				}
				a := authorBy[curAuthor]
				if a == nil {
					a = &AuthorStat{Name: curAuthor}
					authorBy[curAuthor] = a
					authors = append(authors, a)
				}
				a.Commits++
				w := weekBy[curWeek]
				if w == nil {
					w = &ChurnPoint{Week: curWeek}
					weekBy[curWeek] = w
					weeks = append(weeks, w)
				}
				w.Commits++
				continue
			}
			if gitx.TrimJS(line) == "" {
				continue
			}
			path, added, removed, ok := bNumstatRow(line)
			if !ok {
				continue
			}
			f := fileBy[path]
			if f == nil {
				f = &FileHotspot{Path: path}
				fileBy[path] = f
				files = append(files, f)
			}
			f.Commits++
			f.Added += added
			f.Removed += removed
			if a := authorBy[curAuthor]; a != nil {
				a.Added += added
				a.Removed += removed
			}
			if w := weekBy[curWeek]; w != nil {
				w.Added += added
				w.Removed += removed
			}
		}

		ins.FilesTouched = len(files)
		ins.Authors = make([]AuthorStat, 0, len(authors))
		for _, a := range authors {
			ins.Authors = append(ins.Authors, *a)
		}
		bStableSortBy(ins.Authors, func(a, b AuthorStat) bool { return a.Commits > b.Commits })
		hot := make([]FileHotspot, 0, len(files))
		for _, f := range files {
			hot = append(hot, *f)
		}
		bStableSortBy(hot, func(a, b FileHotspot) bool { return a.Commits > b.Commits })
		ins.Hotspots = hot[:bSliceEnd(len(hot), 30)]
		ins.Churn = make([]ChurnPoint, 0, len(weeks))
		for _, w := range weeks {
			ins.Churn = append(ins.Churn, *w)
		}
		bStableSortBy(ins.Churn, func(a, b ChurnPoint) bool { return bLocaleCompare(a.Week, b.Week) < 0 })
		return ins, nil
	})
}

// CosmosFileTouch is CosmosFileTouch in src/shared/types.ts.
type CosmosFileTouch struct {
	Path    string `json:"path"`
	Added   int64  `json:"added"`
	Removed int64  `json:"removed"`
}

// CosmosCommit is CosmosCommit in src/shared/types.ts.
type CosmosCommit struct {
	Hash        string   `json:"hash"`
	Parents     []string `json:"parents"`
	AuthorName  string   `json:"authorName"`
	AuthorEmail string   `json:"authorEmail"`
	// Timestamp is unix seconds.
	Timestamp int64  `json:"timestamp"`
	Subject   string `json:"subject"`
	// Files is empty for merge commits.
	Files []CosmosFileTouch `json:"files"`
}

// gitsvc: cosmosData
//
// Per-commit history with touched files, for the "RepoCosmos" 3D
// visualization easter egg. Same single `git log --numstat` pass as
// repoInsights, but per-commit records instead of aggregates — capped to the
// most recent `limit` commits (nil = 2000) for render performance.
func (s *GitService) CosmosData(ctx context.Context, repoPath string, limit *int) ([]CosmosCommit, error) {
	return locked(s, ctx, "cosmosData", repoPath, func(ctx context.Context) ([]CosmosCommit, error) {
		format := "--pretty=format:\x01%H" + gitx.SEP + "%P" + gitx.SEP + "%an" + gitx.SEP + "%ae" + gitx.SEP + "%at" + gitx.SEP + "%s"
		out := bRawOr(ctx, repoPath, "", "log", "-M", "-"+strconv.Itoa(bIntOr(limit, 2000)), format, "--numstat")
		commits := []CosmosCommit{}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "\x01") {
				f := bFields(line[1:], gitx.SEP, 6)
				parents := []string{}
				for _, p := range strings.Split(f[1], " ") {
					if p != "" {
						parents = append(parents, p)
					}
				}
				name := gitx.TrimJS(f[2])
				if name == "" {
					name = "Unknown"
				}
				commits = append(commits, CosmosCommit{
					Hash: f[0], Parents: parents, AuthorName: name, AuthorEmail: gitx.TrimJS(f[3]),
					Timestamp: bJSInt(f[4]), Subject: f[5], Files: []CosmosFileTouch{},
				})
				continue
			}
			if len(commits) == 0 || gitx.TrimJS(line) == "" {
				continue
			}
			path, added, removed, ok := bNumstatRow(line)
			if !ok {
				continue
			}
			cur := &commits[len(commits)-1]
			cur.Files = append(cur.Files, CosmosFileTouch{Path: path, Added: added, Removed: removed})
		}
		return commits, nil
	})
}

// TimelapseFile is one file a TimelapseCommit touched.
type TimelapseFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// TimelapseCommit is TimelapseCommit in src/shared/types.ts.
type TimelapseCommit struct {
	Hash    string          `json:"hash"`
	Author  string          `json:"author"`
	Date    int64           `json:"date"`
	Subject string          `json:"subject"`
	Files   []TimelapseFile `json:"files"`
}

// gitsvc: timelapseData
//
// The whole history as one flat stream of "commit + the files it touched",
// oldest first — the input for the timelapse animation. One `git log` does
// the lot: asking per commit would be hundreds of processes for a repo worth
// watching. max nil (undefined) is 2000.
func (s *GitService) TimelapseData(ctx context.Context, repoPath string, max *int) ([]TimelapseCommit, error) {
	return locked(s, ctx, "timelapseData", repoPath, func(ctx context.Context) ([]TimelapseCommit, error) {
		out := bGitOr(ctx, repoPath, "", "log", "--reverse", "--first-parent",
			"--max-count="+strconv.Itoa(bIntOr(max, 2000)), "--name-status", "-M", "--date=unix",
			"--format="+gitx.REC+"%H"+gitx.SEP+"%an"+gitx.SEP+"%ct"+gitx.SEP+"%s")
		commits := []TimelapseCommit{}
		for _, chunk := range strings.Split(out, gitx.REC) {
			if gitx.TrimJS(chunk) == "" {
				continue
			}
			lines := strings.Split(chunk, "\n")
			f := bFields(lines[0], gitx.SEP, 4)
			if f[0] == "" {
				continue
			}
			files := []TimelapseFile{}
			for _, line := range lines[1:] {
				if gitx.TrimJS(line) == "" {
					continue
				}
				code, path := bNameStatus(line)
				if len(strings.Split(line, "\t")) == 1 {
					path = line // a row with no tab is its own last field
				}
				if path == "" {
					continue
				}
				files = append(files, TimelapseFile{Path: path, Status: gitx.MapStatusCode(code)})
			}
			commits = append(commits, TimelapseCommit{Hash: f[0], Author: f[1], Date: bJSInt(f[2]), Subject: f[3], Files: files})
		}
		return commits, nil
	})
}

// ChangelogOptions is generateChangelog's `opts`.
type ChangelogOptions struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Version string `json:"version,omitempty"`
}

// ChangelogResult is ChangelogResult in src/shared/types.ts.
type ChangelogResult struct {
	Markdown string `json:"markdown"`
	// Count is the number of commits included.
	Count int `json:"count"`
}

var bChangelogGroups = []struct{ key, title string }{
	{"feat", "✨ Features"},
	{"fix", "🐛 Bug Fixes"},
	{"perf", "⚡ Performance"},
	{"refactor", "♻️ Refactoring"},
	{"docs", "📝 Documentation"},
	{"test", "✅ Tests"},
	{"build", "📦 Build"},
	{"ci", "🤖 CI"},
	{"style", "💄 Styles"},
	{"chore", "🔧 Chores"},
	{"revert", "⏪ Reverts"},
}

var (
	bConventionalRe = regexp.MustCompile(`^(\w+)(?:\(([^)]*)\))?(!)?:[` + bJSSpace + `]*(` + bJSDot + `+)$`)
	bBreakingRe     = regexp.MustCompile(`BREAKING[ -]CHANGE`)
)

// gitsvc: generateChangelog
//
// Build a Conventional-Commits changelog for the range `from..to` (defaults
// to the latest tag → HEAD). Commits are parsed as `type(scope)!: subject`,
// grouped by type with breaking changes surfaced first.
func (s *GitService) GenerateChangelog(ctx context.Context, repoPath string, opts *ChangelogOptions) (ChangelogResult, error) {
	return locked(s, ctx, "generateChangelog", repoPath, func(ctx context.Context) (ChangelogResult, error) {
		var o ChangelogOptions
		if opts != nil {
			o = *opts
		}
		to := gitx.TrimJS(o.To)
		if to == "" {
			to = "HEAD"
		}
		// Default `from` to the most recent tag reachable from `to`, if any.
		from := gitx.TrimJS(o.From)
		if from == "" {
			from = gitx.TrimJS(bRawOr(ctx, repoPath, "", "describe", "--tags", "--abbrev=0", to))
		}
		rng := to
		if from != "" {
			rng = from + ".." + to
		}
		raw := bRawOr(ctx, repoPath, "", "log", rng, "--no-merges", "--pretty=format:%h"+gitx.SEP+"%s"+gitx.SEP+"%b"+gitx.REC)

		buckets := map[string][]string{}
		var breaking, other []string
		count := 0
		for _, rec := range strings.Split(raw, gitx.REC) {
			t := gitx.TrimJS(rec)
			if t == "" {
				continue
			}
			parts := strings.Split(t, gitx.SEP)
			if len(parts) < 2 || parts[1] == "" {
				continue
			}
			hash, subject := parts[0], parts[1]
			body := ""
			if len(parts) > 2 {
				body = parts[2]
			}
			count++
			m := bConventionalRe.FindStringSubmatch(gitx.TrimJS(subject))
			isBreaking := m != nil && m[3] != "" || bBreakingRe.MatchString(body)
			if m != nil {
				typ, scope, desc := m[1], m[2], m[4]
				line := "- "
				if scope != "" {
					line += "**" + scope + ":** "
				}
				line += desc + " (`" + hash + "`)"
				if isBreaking {
					breaking = append(breaking, line)
				}
				key := strings.ToLower(typ)
				if bIsChangelogGroup(key) {
					buckets[key] = append(buckets[key], line)
				} else {
					other = append(other, line)
				}
			} else {
				line := "- " + gitx.TrimJS(subject) + " (`" + hash + "`)"
				if isBreaking {
					breaking = append(breaking, line)
				}
				other = append(other, line)
			}
		}

		date := time.Now().UTC().Format("2006-01-02")
		heading := gitx.TrimJS(o.Version)
		if heading == "" {
			heading = rng
		}
		out := []string{"## " + heading + " (" + date + ")", ""}
		if len(breaking) > 0 {
			out = append(append(out, "### ⚠ BREAKING CHANGES", ""), breaking...)
			out = append(out, "")
		}
		for _, g := range bChangelogGroups {
			if arr := buckets[g.key]; len(arr) > 0 {
				out = append(append(out, "### "+g.title, ""), arr...)
				out = append(out, "")
			}
		}
		if len(other) > 0 {
			out = append(append(out, "### Other", ""), other...)
			out = append(out, "")
		}
		if count == 0 {
			out = append(out, "_No commits in this range._", "")
		}
		return ChangelogResult{Markdown: gitx.TrimEndJS(strings.Join(out, "\n")) + "\n", Count: count}, nil
	})
}

func bIsChangelogGroup(key string) bool {
	for _, g := range bChangelogGroups {
		if g.key == key {
			return true
		}
	}
	return false
}
