package gitsvc

// Clone and init: the app-level methods that run before a repository exists
// locally (no repo lock), plus `initHere` for a folder the user already opened.
// Clone streams `git clone --progress` to the renderer as `clone:progress`
// events, in the order git prints them.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// CloneOptions is CloneOptions in src/shared/types.ts. Every field is
// optional; the zero value is a plain full clone of the default branch.
type CloneOptions struct {
	// Host the URL belongs to, for credential injection.
	Host string `json:"host,omitempty"`
	// Token for that host — used for this clone only, never written to config.
	Token string `json:"token,omitempty"`
	// Filter is a partial clone spec for `--filter`, e.g. `blob:none`.
	Filter string `json:"filter,omitempty"`
	// Depth keeps only this many commits of history (0 = full).
	Depth float64 `json:"depth,omitempty"`
	// SingleBranch fetches only the checked-out branch. `false` alongside Depth
	// restores the other branches that `--depth` would otherwise drop; nil says
	// nothing either way.
	SingleBranch *bool `json:"singleBranch,omitempty"`
	// Branch or tag to check out instead of the remote's default HEAD.
	Branch string `json:"branch,omitempty"`
	// RecurseSubmodules also clones submodules, recursively.
	RecurseSubmodules bool `json:"recurseSubmodules,omitempty"`
}

// dSchemeRe recognises a URL scheme (`https:`, `ssh:`, `file:`…).
var dSchemeRe = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)

// dProgressRe is simple-git's progress-monitor pattern, applied to the start
// of each stderr chunk: `Receiving objects:  45% (9/20)`.
var dProgressRe = regexp.MustCompile(`^((?s:.)+?):[` + dJSSpace + `]*(\d+)% \((\d+)/(\d+)\)`)

// dParseCloneProgress turns one stderr chunk into a progress event, the way
// simple-git did: only a chunk that starts with a progress line counts, and the
// stage is the first word of its label, lower-cased.
func dParseCloneProgress(chunk string) (CloneProgress, bool) {
	m := dProgressRe.FindStringSubmatch(chunk)
	if m == nil {
		return CloneProgress{}, false
	}
	stage := strings.SplitN(strings.ToLower(m[1]), " ", 2)[0]
	if stage == "" {
		stage = "unknown"
	}
	num := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0
		}
		return n
	}
	return CloneProgress{Stage: stage, Progress: num(m[2]), Processed: num(m[3]), Total: num(m[4])}, true
}

// dBasename is Node's posix path.basename: trailing slashes ignored, ” for ”.
func dBasename(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// dFileURL is Node's url.pathToFileURL(path).href for an absolute path:
// `file://` plus the path, percent-encoded with the WHATWG path set (and `%`,
// `\`, which Node encodes on top).
func dFileURL(p string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.WriteString("file://")
	for _, c := range []byte(filepath.ToSlash(p)) {
		switch {
		case c <= 0x20 || c >= 0x7f || strings.IndexByte("\"#%<>?`{}\\", c) >= 0:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Clone clones url into parentDir/name (name defaults to the URL's last path
// segment, then 'repository') and returns the new repository's path. Progress
// is streamed as `clone:progress` events.
//
// gitsvc: clone
func (s *GitService) Clone(ctx context.Context, parentDir, url, name string, opts CloneOptions) (string, error) {
	return locked(s, ctx, "clone", parentDir, func(ctx context.Context) (string, error) {
		return s.dClone(ctx, parentDir, url, name, opts, s.cloneProgress())
	})
}

func (s *GitService) dClone(ctx context.Context, parentDir, url, name string, opts CloneOptions, onProgress func(CloneProgress)) (string, error) {
	folder := gitx.TrimJS(name)
	if folder == "" {
		folder = strings.TrimSuffix(dBasename(url), ".git")
	}
	if folder == "" {
		folder = "repository"
	}
	target := filepath.Join(parentDir, folder)
	if dExists(target) {
		return "", dErrf("A folder named \"%s\" already exists here.", folder)
	}
	// `git clone --depth` is silently ignored for a local path — a local clone
	// hardlinks the object store instead of fetching — so a shallow request on a
	// local repository only takes effect through a file:// URL.
	localShallow := opts.Depth != 0 && !dSchemeRe.MatchString(url) && !strings.Contains(url, "@") && dExists(url)
	cloneURL := gitx.AuthedCloneURL(url, opts.Host, opts.Token)
	if localShallow {
		abs, err := filepath.Abs(url)
		if err != nil {
			abs = url
		}
		cloneURL = dFileURL(abs)
	}
	args := []string{"clone"}
	// A blob filter (e.g. "blob:none") makes this a partial clone — history
	// without file blobs, fetched on demand. Great for very large repos.
	if opts.Filter != "" {
		args = append(args, "--filter="+opts.Filter)
	}
	// `--depth` implies --single-branch in git, so the flag pair below is not
	// contradictory: asking for full branches back is the case worth spelling out.
	if opts.Depth > 0 {
		args = append(args, "--depth="+strconv.FormatInt(int64(opts.Depth), 10))
	}
	if opts.SingleBranch != nil && *opts.SingleBranch {
		args = append(args, "--single-branch")
	} else if opts.SingleBranch != nil && opts.Depth != 0 {
		args = append(args, "--no-single-branch")
	}
	if b := gitx.TrimJS(opts.Branch); b != "" {
		args = append(args, "--branch", b)
	}
	if opts.RecurseSubmodules {
		args = append(args, "--recurse-submodules")
	}
	// simple-git appended --progress (a progress handler was always attached)
	// and moved the URL and folder behind `--`.
	args = append(args, "--progress", "--", cloneURL, folder)
	_, err := gitx.Raw(ctx, parentDir, args, gitx.RunOpts{OnStderr: func(chunk []byte) {
		if p, ok := dParseCloneProgress(string(chunk)); ok && onProgress != nil {
			onProgress(p)
		}
	}})
	if err != nil {
		return "", err
	}
	// Reset the origin URL back to the token-free version so the PAT is not
	// persisted on disk.
	if cloneURL != url {
		_, _ = gitx.Raw(ctx, target, []string{"remote", "set-url", "origin", url}, gitx.RunOpts{})
	}
	return target, nil
}

// RemoteBranches lists the branch names a remote advertises, sorted, for the
// clone dialog's branch picker. Never touches disk — `ls-remote` is a network
// read with no local repo. host and token authenticate an https URL for this
// call only.
//
// gitsvc: remoteBranches
func (s *GitService) RemoteBranches(ctx context.Context, url, host, token string) ([]string, error) {
	return locked(s, ctx, "remoteBranches", url, func(ctx context.Context) ([]string, error) {
		authed := gitx.AuthedCloneURL(url, host, token)
		out, err := gitx.RunGitFallback(ctx, "", []string{"ls-remote", "--heads", authed}, gitx.RunOpts{}, "git ls-remote failed")
		if err != nil {
			return nil, err
		}
		names := []string{}
		for _, line := range dLines(out) {
			parts := strings.Split(line, "\t")
			if len(parts) < 2 {
				continue
			}
			if n := strings.TrimPrefix(parts[1], "refs/heads/"); n != "" {
				names = append(names, n)
			}
		}
		dSortJS(names)
		return names, nil
	})
}

// Init creates parentDir/name (default 'my-repo') and runs `git init` in it.
//
// gitsvc: init
func (s *GitService) Init(ctx context.Context, parentDir, name string) (string, error) {
	return locked(s, ctx, "init", parentDir, func(ctx context.Context) (string, error) {
		folder := gitx.TrimJS(name)
		if folder == "" {
			folder = "my-repo"
		}
		target := filepath.Join(parentDir, folder)
		if dExists(target) {
			return "", dErrf("A folder named \"%s\" already exists here.", folder)
		}
		if err := os.MkdirAll(target, 0o777); err != nil {
			return "", dNodeFSError("mkdir", target, "", err)
		}
		if _, err := gitx.Raw(ctx, target, []string{"init"}, gitx.RunOpts{}); err != nil {
			return "", err
		}
		return target, nil
	})
}

// InitHere runs `git init` in an existing folder the user already opened.
//
// gitsvc: initHere
func (s *GitService) InitHere(ctx context.Context, repoPath string) error {
	return lockedErr(s, ctx, "initHere", repoPath, func(ctx context.Context) error {
		_, err := gitx.Raw(ctx, repoPath, []string{"init"}, gitx.RunOpts{})
		return err
	})
}
