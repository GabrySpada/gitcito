package gitsvc

// Repository maintenance, fsck, bundles and archives.
//
// git keeps working whatever state its object database is in, which is why
// nobody ever runs gc until a clone takes ten minutes. These read the numbers
// that would otherwise stay invisible, and run the jobs that act on them.
//
// Bundles and archives are two ways to put a repository into a single file. A
// bundle is git history — it clones and fetches like a remote, and is how work
// crosses an air gap. An archive is just the files at one commit, with no
// history at all.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// MaintenanceStats is MaintenanceStats in src/shared/types.ts.
type MaintenanceStats struct {
	LooseObjects  int64 `json:"looseObjects"`
	LooseBytes    int64 `json:"looseBytes"`
	PackedObjects int64 `json:"packedObjects"`
	Packs         int64 `json:"packs"`
	PackBytes     int64 `json:"packBytes"`
	PrunePackable int64 `json:"prunePackable"`
	GarbageFiles  int64 `json:"garbageFiles"`
	GarbageBytes  int64 `json:"garbageBytes"`
	Prunable      int   `json:"prunable"`
	PrunableBytes int64 `json:"prunableBytes"`
	GitBytes      int64 `json:"gitBytes"`
	// LastPack is when the newest packfile was written (ISO 8601), or null.
	LastPack  *string `json:"lastPack"`
	Scheduled bool    `json:"scheduled"`
	GcLog     string  `json:"gcLog"`
}

// MaintenanceResult is MaintenanceResult in src/shared/types.ts.
type MaintenanceResult struct {
	Before int64  `json:"before"`
	After  int64  `json:"after"`
	Output string `json:"output"`
}

// FsckReport is FsckReport in src/shared/types.ts.
type FsckReport struct {
	OK       bool   `json:"ok"`
	Dangling int    `json:"dangling"`
	Missing  int    `json:"missing"`
	Output   string `json:"output"`
}

// BundleScope is the BundleScope union in src/shared/types.ts:
// {kind:'all'} | {kind:'ref', ref} | {kind:'range', from, to}.
type BundleScope struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// BundleResult is BundleResult in src/shared/types.ts.
type BundleResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Refs  int    `json:"refs"`
}

// BundleRef is BundleRef in src/shared/types.ts.
type BundleRef struct {
	Name string `json:"name"`
	Sha  string `json:"sha"`
}

// BundleInfo is BundleInfo in src/shared/types.ts.
type BundleInfo struct {
	Refs          []BundleRef `json:"refs"`
	Prerequisites []string    `json:"prerequisites"`
	Usable        bool        `json:"usable"`
	Message       string      `json:"message"`
}

// ArchiveResult is ArchiveResult in src/shared/types.ts.
type ArchiveResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// dGitWalkBudget bounds the walk that sizes a whole .git directory.
const dGitWalkBudget = 200000

func dGitDirBytes(ctx context.Context, gitDir string) int64 {
	budget := dGitWalkBudget
	return gitx.DirBytes(ctx, gitDir, &budget)
}

var dObjectShaRe = regexp.MustCompile(`^([0-9a-f]{40,64})\b`)

// MaintenanceStats says where the disk went, and how much of it maintenance
// could get back.
//
// gitsvc: maintenanceStats
func (s *GitService) MaintenanceStats(ctx context.Context, repoPath string) (MaintenanceStats, error) {
	return locked(s, ctx, "maintenanceStats", repoPath, func(ctx context.Context) (MaintenanceStats, error) {
		counts, err := gitx.RunGit(ctx, repoPath, []string{"count-objects", "-v"}, gitx.RunOpts{})
		if err != nil {
			counts = ""
		}
		num := func(key string) int64 {
			m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `: (\d+)$`).FindStringSubmatch(counts)
			if m == nil {
				return 0
			}
			n, _ := strconv.ParseInt(m[1], 10, 64)
			return n
		}
		gitDir := dGitDir(ctx, repoPath)

		// `prune -n` is a full reachability walk: the honest number, and the slow
		// one. It is also the only way to say what "unreachable" costs here. Only
		// loose objects are listed, which is exactly what prune removes — so each
		// one is a file to stat rather than an object to ask git about.
		prunable, err := gitx.RunGit(ctx, repoPath, []string{"prune", "-n", "--expire=now"}, gitx.RunOpts{})
		if err != nil {
			prunable = ""
		}
		var shas []string
		for _, line := range dLines(prunable) {
			if m := dObjectShaRe.FindStringSubmatch(gitx.TrimJS(line)); m != nil {
				shas = append(shas, m[1])
			}
		}
		var prunableBytes int64
		for _, sha := range shas {
			if st, err := os.Stat(filepath.Join(gitDir, "objects", sha[:2], sha[2:])); err == nil {
				prunableBytes += gitx.DiskBytes(st)
			}
		}
		gitBytes := dGitDirBytes(ctx, gitDir)

		// The newest packfile's mtime is when gc last did something worth doing.
		var lastPack *string
		packDir := filepath.Join(gitDir, "objects", "pack")
		if entries, err := os.ReadDir(packDir); err == nil {
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".pack") {
					continue
				}
				st, err := os.Stat(filepath.Join(packDir, e.Name()))
				if err != nil {
					continue
				}
				when := st.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")
				if lastPack == nil || when > *lastPack {
					lastPack = &when
				}
			}
		}

		registered, err := gitx.RunGit(ctx, repoPath, []string{"config", "--global", "--get-all", "maintenance.repo"}, gitx.RunOpts{})
		if err != nil {
			registered = ""
		}
		real := dResolve(repoPath)
		cwd, _ := os.Getwd()
		scheduled := false
		for _, line := range dLines(registered) {
			p := gitx.TrimJS(line)
			if p == "" {
				p = "."
			}
			if dResolve(cwd, p) == real {
				scheduled = true
			}
		}
		gcLog, err := dReadUTF8(filepath.Join(gitDir, "gc.log"))
		if err != nil {
			gcLog = ""
		}
		return MaintenanceStats{
			LooseObjects:  num("count"),
			LooseBytes:    num("size") * 1024,
			PackedObjects: num("in-pack"),
			Packs:         num("packs"),
			PackBytes:     num("size-pack") * 1024,
			PrunePackable: num("prune-packable"),
			GarbageFiles:  num("garbage"),
			GarbageBytes:  num("size-garbage") * 1024,
			Prunable:      len(shas),
			PrunableBytes: prunableBytes,
			GitBytes:      gitBytes,
			LastPack:      lastPack,
			Scheduled:     scheduled,
			GcLog:         gcLog,
		}, nil
	})
}

// dMaintenanceArgs maps a MaintenanceTask to its git command.
var dMaintenanceArgs = map[string][]string{
	"gc":          {"gc"},
	"aggressive":  {"gc", "--aggressive"},
	"commitGraph": {"commit-graph", "write", "--reachable"},
	"prune":       {"gc", "--prune=now"},
}

// MaintenanceRun runs one maintenance job ('gc' | 'aggressive' |
// 'commitGraph' | 'prune'), reporting the size on either side of it.
//
// `prune` is the only one that destroys anything: gc keeps unreachable objects
// for two weeks precisely so a bad reset stays recoverable, and pruning now
// throws that safety net away.
//
// gitsvc: maintenanceRun
func (s *GitService) MaintenanceRun(ctx context.Context, repoPath, task string) (MaintenanceResult, error) {
	return locked(s, ctx, "maintenanceRun", repoPath, func(ctx context.Context) (MaintenanceResult, error) {
		gitDir := dGitDir(ctx, repoPath)
		before := dGitDirBytes(ctx, gitDir)
		args, ok := dMaintenanceArgs[task]
		if !ok {
			return MaintenanceResult{}, dErrf("Unknown maintenance task: %s", task)
		}
		output, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
		if err != nil {
			return MaintenanceResult{}, err
		}
		return MaintenanceResult{Before: before, After: dGitDirBytes(ctx, gitDir), Output: gitx.TrimJS(output)}, nil
	})
}

// MaintenanceSchedule registers (on) or unregisters this repository with `git
// maintenance`, and returns on.
//
// gitsvc: maintenanceSchedule
func (s *GitService) MaintenanceSchedule(ctx context.Context, repoPath string, on bool) (bool, error) {
	return locked(s, ctx, "maintenanceSchedule", repoPath, func(ctx context.Context) (bool, error) {
		// `start` writes the OS schedule (launchd / systemd / Task Scheduler) as
		// well as registering; `unregister` leaves the schedule alone for the
		// other repositories still in it.
		sub := "unregister"
		if on {
			sub = "start"
		}
		if _, err := gitx.RunGit(ctx, repoPath, []string{"maintenance", sub}, gitx.RunOpts{}); err != nil {
			return false, err
		}
		return on, nil
	})
}

var dMissingLineRe = regexp.MustCompile(`^missing |^broken link`)

// dRunBoth runs git and returns stdout+stderr, ok=false on failure with the
// output `${stdout}${stderr || message}` — the shape fsck and bundle verify
// share, since both say everything useful on stderr and a non-zero exit there
// is a finding, not an error.
func dRunBoth(ctx context.Context, repoPath string, args []string, maxBuffer int) (string, bool) {
	res, err := gitx.Run(ctx, repoPath, args, gitx.RunOpts{MaxBuffer: maxBuffer})
	if err == nil {
		return res.Stdout + res.Stderr, true
	}
	var ee *gitx.ExecError
	tail := res.Stderr
	if tail == "" {
		tail = err.Error()
		if errors.As(err, &ee) {
			tail = ee.Error()
		}
	}
	return res.Stdout + tail, false
}

// Fsck checks the object database. Dangling is normal; missing is damage.
//
// gitsvc: fsck
func (s *GitService) Fsck(ctx context.Context, repoPath string) (FsckReport, error) {
	return locked(s, ctx, "fsck", repoPath, func(ctx context.Context) (FsckReport, error) {
		output, ok := dRunBoth(ctx, repoPath, []string{"fsck", "--no-progress", "--dangling"}, 16*1024*1024)
		r := FsckReport{OK: ok}
		for _, l := range dLines(output) {
			if strings.HasPrefix(l, "dangling ") {
				r.Dangling++
			}
			if dMissingLineRe.MatchString(l) {
				r.Missing++
			}
		}
		r.Output = gitx.SliceUTF16(gitx.TrimJS(output), 20000)
		return r, nil
	})
}

// BundleCreate writes a bundle of scope to file, and reports what it ended up
// holding. Writing a bundle reads the object database and touches nothing in
// the repository.
//
// gitsvc: bundleCreate
func (s *GitService) BundleCreate(ctx context.Context, repoPath, file string, scope BundleScope) (BundleResult, error) {
	return locked(s, ctx, "bundleCreate", repoPath, func(ctx context.Context) (BundleResult, error) {
		args := []string{"bundle", "create", file}
		switch scope.Kind {
		case "all":
			args = append(args, "--all")
		case "ref":
			args = append(args, scope.Ref)
		default:
			// A range bundle records the far end as a prerequisite: it is a patch
			// of history, useless to anyone who does not already have `from`.
			args = append(args, scope.From+".."+scope.To)
		}
		if _, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{}); err != nil {
			return BundleResult{}, err
		}
		heads, err := gitx.RunGit(ctx, repoPath, []string{"bundle", "list-heads", file}, gitx.RunOpts{})
		if err != nil {
			heads = ""
		}
		r := BundleResult{Path: file}
		if st, err := os.Stat(file); err == nil {
			r.Bytes = st.Size()
		}
		for _, l := range dLines(heads) {
			if gitx.TrimJS(l) != "" {
				r.Refs++
			}
		}
		return r, nil
	})
}

var (
	dShortShaRe     = regexp.MustCompile(`^[0-9a-f]{7,}$`)
	dErrorPrefixRe  = regexp.MustCompile(`^error:[` + dJSSpace + `]*`)
	dRequiresHeadRe = regexp.MustCompile(`(?i)requires (this|these)` + dJSDot + `*ref|lacks these prerequisite`)
)

// BundleInspect says what a bundle holds, and whether this repository has
// what it needs to use it.
//
// gitsvc: bundleInspect
func (s *GitService) BundleInspect(ctx context.Context, repoPath, file string) (BundleInfo, error) {
	return locked(s, ctx, "bundleInspect", repoPath, func(ctx context.Context) (BundleInfo, error) {
		heads, err := gitx.RunGit(ctx, repoPath, []string{"bundle", "list-heads", file}, gitx.RunOpts{})
		if err != nil {
			heads = ""
		}
		info := BundleInfo{Refs: []BundleRef{}, Prerequisites: []string{}}
		for _, line := range dLines(heads) {
			f := dSplitWS(gitx.TrimJS(line))
			if dShortShaRe.MatchString(f[0]) && len(f) > 1 {
				info.Refs = append(info.Refs, BundleRef{Sha: f[0], Name: strings.Join(f[1:], " ")})
			}
		}

		// `git bundle verify` says everything useful on stderr and exits non-zero
		// when a prerequisite is missing — which is a fact to report, not a failure.
		output, ok := dRunBoth(ctx, repoPath, []string{"bundle", "verify", file}, 0)
		// verify prints shas twice over — the refs the bundle *contains*, and the
		// ones it *requires* — so the prerequisites are only the shas under the
		// requires heading (or, when git could not read them, its `error:` lines).
		inRequires := false
		for _, raw := range dLines(output) {
			line := dErrorPrefixRe.ReplaceAllString(gitx.TrimJS(raw), "")
			if dRequiresHeadRe.MatchString(line) {
				inRequires = true
				continue
			}
			if m := dObjectShaRe.FindStringSubmatch(line); m != nil {
				if inRequires {
					info.Prerequisites = append(info.Prerequisites, m[1])
				}
			} else if line != "" {
				inRequires = false
			}
		}
		info.Usable = ok
		info.Message = gitx.TrimJS(output)
		return info, nil
	})
}

// dBundleShort is a ref's name under refs/remotes/bundle/.
func dBundleShort(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/")
}

// BundleFetch fetches refs out of a bundle into `refs/remotes/bundle/*` and
// returns their new short names.
//
// Deliberately not onto the local branches: a bundle is someone else's work
// arriving, and landing it on `main` behind the user's back is how you lose
// commits. Under a remote-shaped namespace it shows up in the branch list and
// merges (or does not) on their terms.
//
// gitsvc: bundleFetch
func (s *GitService) BundleFetch(ctx context.Context, repoPath, file string, refs []string) ([]string, error) {
	return locked(s, ctx, "bundleFetch", repoPath, func(ctx context.Context) ([]string, error) {
		var wanted []string
		for _, r := range refs {
			if r != "" {
				wanted = append(wanted, r)
			}
		}
		if len(wanted) == 0 {
			return nil, dErr("Choose at least one ref to import.")
		}
		args := []string{"fetch", file}
		created := make([]string, 0, len(wanted))
		for _, ref := range wanted {
			short := dBundleShort(ref)
			args = append(args, "+"+ref+":refs/remotes/bundle/"+short)
			created = append(created, "bundle/"+short)
		}
		if _, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{}); err != nil {
			return nil, err
		}
		return created, nil
	})
}

// ArchiveCreate exports one tree as a zip or tarball (format 'zip' | 'tar.gz'
// | 'tar'), optionally wrapped in prefix and limited to subdir. `git archive`
// honours `export-ignore` in .gitattributes, so a repository can keep its own
// CI config and fixtures out of the file it hands to someone else.
//
// gitsvc: archiveCreate
func (s *GitService) ArchiveCreate(ctx context.Context, repoPath, file, ref, format, prefix, subdir string) (ArchiveResult, error) {
	return locked(s, ctx, "archiveCreate", repoPath, func(ctx context.Context) (ArchiveResult, error) {
		args := []string{"archive", "--format=" + format, "-o", file}
		// Without a prefix every path lands at the root of the archive, which
		// turns an unzip in the wrong directory into a mess of loose files.
		if p := gitx.TrimJS(prefix); p != "" {
			args = append(args, "--prefix="+strings.TrimRight(p, "/")+"/")
		}
		r := gitx.TrimJS(ref)
		if r == "" {
			r = "HEAD"
		}
		args = append(args, r)
		if d := gitx.TrimJS(subdir); d != "" {
			args = append(args, "--", d)
		}
		if _, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{}); err != nil {
			return ArchiveResult{}, err
		}
		res := ArchiveResult{Path: file}
		if st, err := os.Stat(file); err == nil {
			res.Bytes = st.Size()
		}
		return res, nil
	})
}
