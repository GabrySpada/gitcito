// Package registry is the Repositories page's index — src/main/repoRegistry.ts
// with its helpers repoMeta.ts (metadata read from .git without spawning git)
// and repoScan.ts (finding repositories under configured folders).
//
// Every repository Gitcito knows about, whether or not it is open, lives in its
// own file (gitcito-repos.json in the data directory) rather than in settings:
// it is a cache of what is on *this* disk, while settings are preferences you
// would carry to another machine. Losing this file costs a rescan; losing
// settings costs information.
package registry

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/hosting"
)

// Repository metadata read straight from the files in .git, never by spawning
// git. A row on the Repositories page costs two file reads; spawning `git
// rev-parse` per row would cost a process, and the page lists everything the
// user has ever opened.

// jsSpace is JavaScript's `\s`.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	gitdirRe = regexp.MustCompile(`(?m)^gitdir:[` + jsSpace + `]*(.+)$`)
	headRe   = regexp.MustCompile(`(?m)^ref:[` + jsSpace + `]*refs/heads/(.+)$`)
	urlRe    = regexp.MustCompile(`(?m)^[` + jsSpace + `]*url[` + jsSpace + `]*=[` + jsSpace + `]*(.+)$`)
	// scp-style: git@host:namespace/repo.git
	scpRe = regexp.MustCompile(`^[^@` + jsSpace + `]+@[^:` + jsSpace + `]+:([^\n\r\x{2028}\x{2029}]+)$`)
	// URL form: scheme://[user@]host/namespace/repo.git
	schemeRe = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://(?:[^@/]+@)?[^/]+/([^\n\r\x{2028}\x{2029}]+)$`)
)

// GitDirOf is the real .git directory: a plain repository has a directory, a
// worktree or submodule has a file containing `gitdir: <path>`. "" when
// neither.
func GitDirOf(repoPath string) string {
	dot := filepath.Join(repoPath, ".git")
	info, err := os.Stat(dot)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return dot
	}
	text, err := os.ReadFile(dot)
	if err != nil {
		return ""
	}
	m := gitdirRe.FindStringSubmatch(string(text))
	if m == nil {
		return ""
	}
	target := gitx.TrimJS(m[1])
	if filepath.IsAbs(target) {
		return target
	}
	abs, err := filepath.Abs(filepath.Join(repoPath, target))
	if err != nil {
		return ""
	}
	return abs
}

// ReadHeadBranch is the checked-out branch, or nil when detached or
// unreadable.
func ReadHeadBranch(repoPath string) *string {
	gitDir := GitDirOf(repoPath)
	if gitDir == "" {
		return nil
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return nil
	}
	m := headRe.FindStringSubmatch(string(head))
	if m == nil {
		return nil // a raw sha means detached HEAD
	}
	branch := gitx.TrimJS(m[1])
	return &branch
}

// OwnerFromRemoteURL is the owning namespace of a remote URL: an org, a user,
// or the top level of a GitLab group path.
//
// hosting.ParseRemoteURL handles the hosts Gitcito integrates with, but
// returns nil for a self-hosted GitLab or Gitea — which is exactly where an
// owner column earns its keep. The fallback takes the first path segment
// after the host.
func OwnerFromRemoteURL(url string) *string {
	if url == "" {
		return nil
	}
	known, err := hosting.ParseRemoteURL(url)
	if err != nil {
		// The TS threw here (a broken percent-escape in an Azure URL) and its
		// only caller turned that into null.
		return nil
	}
	if known != nil {
		owner := strings.Split(known.Owner, "/")[0]
		if owner == "" {
			return nil
		}
		return &owner
	}
	trimmed := gitx.TrimJS(url)
	// Not scp-style when a scheme is present, or `user@host:port/namespace/repo`
	// greedily matches this first and the port number gets reported as owner.
	var m []string
	if !strings.Contains(url, "://") {
		m = scpRe.FindStringSubmatch(trimmed)
	}
	if m == nil {
		m = schemeRe.FindStringSubmatch(trimmed)
	}
	if m == nil {
		return nil
	}
	var segments []string
	for _, s := range strings.Split(m[1], "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}
	// One segment is just the repo — there is no namespace to report.
	if len(segments) < 2 {
		return nil
	}
	return &segments[0]
}

// ReadOriginOwner is the owner of `origin`, read from .git/config.
func ReadOriginOwner(repoPath string) *string {
	gitDir := GitDirOf(repoPath)
	if gitDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return nil
	}
	config := string(data)
	const header = `[remote "origin"]`
	i := strings.Index(config, header)
	if i < 0 {
		return nil
	}
	section := config[i+len(header):]
	if end := strings.Index(section, "\n["); end >= 0 {
		section = section[:end]
	}
	m := urlRe.FindStringSubmatch(section)
	if m == nil {
		return nil
	}
	return OwnerFromRemoteURL(gitx.TrimJS(m[1]))
}
