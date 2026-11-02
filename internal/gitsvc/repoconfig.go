package gitsvc

// `.gitcito.json` on disk: reading it, writing it, checking what it requires,
// and proposing one for a repository that has none — src/main/repoConfig.ts,
// exposed as gitService methods so the calls take the same per-repository
// lock as git itself and land in the operation log alongside them.
//
// The doctor's repairs are a closed set (see DoctorFix). The config supplies
// data for them — a path to copy, a value for `core.hooksPath` — and never a
// command, so opening a hostile repository cannot make Gitcito run anything it
// was not already prepared to run.
//
// The validation (repoconfigvalidate.go) is src/shared/repoConfig.ts. Both
// belong in internal/repoconfig once it exists; they live here, slice-prefixed,
// because this slice owns the five methods.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// RepoConfigLink is RepoConfigLink in src/shared/types.ts.
type RepoConfigLink struct {
	Match string `json:"match"`
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

// RepoConfigLinks is RepoConfig['links'].
type RepoConfigLinks struct {
	Tickets []RepoConfigLink `json:"tickets,omitempty"`
}

// RepoConfigCommit is RepoConfig['commit']. Field order is the order the
// validator writes them in, which is the order they land in the file.
type RepoConfigCommit struct {
	Scopes           []string `json:"scopes,omitempty"`
	Trailers         []string `json:"trailers,omitempty"`
	TicketFromBranch *bool    `json:"ticketFromBranch,omitempty"`
}

// RepoConfigFileReq is RepoConfigFileReq in src/shared/types.ts.
type RepoConfigFileReq struct {
	Path string `json:"path"`
	From string `json:"from,omitempty"`
	Why  string `json:"why,omitempty"`
}

// RepoConfigRequires is RepoConfig['requires'].
type RepoConfigRequires struct {
	Node       string              `json:"node,omitempty"`
	Submodules *bool               `json:"submodules,omitempty"`
	Lfs        *bool               `json:"lfs,omitempty"`
	HooksPath  string              `json:"hooksPath,omitempty"`
	Files      []RepoConfigFileReq `json:"files,omitempty"`
}

// RepoConfigChecklist is RepoConfig['checklist'].
type RepoConfigChecklist struct {
	Push []string `json:"push,omitempty"`
}

// RepoConfig is RepoConfig in src/shared/types.ts — the validated
// `.gitcito.json`.
type RepoConfig struct {
	Version   float64              `json:"version"`
	Protect   []string             `json:"protect,omitempty"`
	Links     *RepoConfigLinks     `json:"links,omitempty"`
	Commit    *RepoConfigCommit    `json:"commit,omitempty"`
	Requires  *RepoConfigRequires  `json:"requires,omitempty"`
	Checklist *RepoConfigChecklist `json:"checklist,omitempty"`
}

// RepoConfigIssue is RepoConfigIssue in src/shared/types.ts.
type RepoConfigIssue struct {
	// Field is the dotted path of the offending field, e.g. `links.tickets[0].url`.
	Field string `json:"field"`
	// Code: 'type' | 'limit' | 'unknown' | 'version' | 'unsafe' | 'regex' | 'url' | 'json'.
	Code string `json:"code"`
}

// RepoConfigResult is RepoConfigResult in src/shared/types.ts.
type RepoConfigResult struct {
	Path   string            `json:"path"`
	Exists bool              `json:"exists"`
	Config *RepoConfig       `json:"config"`
	Issues []RepoConfigIssue `json:"issues"`
}

// DoctorFix is the DoctorFix union in src/shared/types.ts:
// {kind:'submodules'} | {kind:'lfsPull'} | {kind:'hooksPath', value} |
// {kind:'copyFile', from, to}. Deliberately closed: data, never a command.
type DoctorFix struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// DoctorCheck is DoctorCheck in src/shared/types.ts.
type DoctorCheck struct {
	// ID is the row's stable identity: `node`, `lfs`, `file:.env`…
	ID string `json:"id"`
	// Kind: 'node' | 'submodules' | 'lfs' | 'hooks' | 'file'.
	Kind string `json:"kind"`
	// Status: 'ok' | 'warn' | 'fail'.
	Status   string     `json:"status"`
	Expected string     `json:"expected,omitempty"`
	Actual   string     `json:"actual,omitempty"`
	Why      string     `json:"why,omitempty"`
	Fix      *DoctorFix `json:"fix,omitempty"`
}

// dRepoConfigTimeout: short-lived commands only — a doctor row is not worth
// hanging the panel over.
const dRepoConfigTimeout = 15 * time.Second

// dRun is repoConfig.ts' run(): execFile with a timeout, the process
// environment, a 4 MiB cap, trimmed stdout.
func dRun(ctx context.Context, cwd, file string, args ...string) (string, error) {
	res, err := gitx.Exec(ctx, cwd, file, args, gitx.RunOpts{PlainEnv: true, Timeout: dRepoConfigTimeout, MaxBuffer: 4 * 1024 * 1024})
	if err != nil {
		return "", err
	}
	return gitx.TrimJS(res.Stdout), nil
}

func dRepoGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	return dRun(ctx, repoPath, "git", args...)
}

// dInside resolves a repo-relative path, refusing anything that escapes the
// repository. The validator already rejected these shapes; this is the second
// lock on the same door, because this is the point where a string becomes a
// real path.
func dInside(repoPath, rel string) (string, error) {
	if !dIsSafeRepoRelPath(rel) {
		return "", dErrf("Unsafe path in %s: %s", dRepoConfigFile, rel)
	}
	root := dResolve(repoPath)
	full := dResolve(root, rel)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", dErrf("Unsafe path in %s: %s", dRepoConfigFile, rel)
	}
	return full, nil
}

// dRelativeInside expresses a configured path relative to the repository, or
// "" when it points outside it — an absolute path from someone else's machine
// is not something to write into a shared file.
func dRelativeInside(repoPath, value string) string {
	root := dResolve(repoPath)
	full := dResolve(root, value)
	if full == root || !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return ""
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || !dIsSafeRepoRelPath(rel) {
		return ""
	}
	return rel
}

func dReadRepoConfig(repoPath string) RepoConfigResult {
	path := filepath.Join(repoPath, dRepoConfigFile)
	raw, err := dReadUTF8(path)
	if err != nil {
		return RepoConfigResult{Path: path, Exists: false, Config: nil, Issues: []RepoConfigIssue{}}
	}
	config, issues := dParseRepoConfig(raw)
	return RepoConfigResult{Path: path, Exists: true, Config: config, Issues: issues}
}

// RepoConfig reads and validates the repository's `.gitcito.json`.
//
// gitsvc: repoConfig
func (s *GitService) RepoConfig(ctx context.Context, repoPath string) (RepoConfigResult, error) {
	return locked(s, ctx, "repoConfig", repoPath, func(ctx context.Context) (RepoConfigResult, error) {
		return dReadRepoConfig(repoPath), nil
	})
}

// dSerializeRepoConfig renders a config back to the file's text: two spaces
// and a trailing newline, because this file is committed and diffed by humans.
func dSerializeRepoConfig(c *RepoConfig) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// SetRepoConfig writes `.gitcito.json`. The config is validated again here
// rather than trusted from the renderer: the editor is one caller, and the
// next one should not be able to introduce a field the loader would reject.
//
// gitsvc: setRepoConfig
func (s *GitService) SetRepoConfig(ctx context.Context, repoPath string, config RepoConfig) (RepoConfigResult, error) {
	return locked(s, ctx, "setRepoConfig", repoPath, func(ctx context.Context) (RepoConfigResult, error) {
		raw, err := json.Marshal(config)
		if err != nil {
			return RepoConfigResult{}, err
		}
		value, keys, err := dParseJSONOrdered(raw)
		if err != nil {
			return RepoConfigResult{}, err
		}
		clean, issues := dValidateRepoConfig(value, keys)
		if clean == nil {
			return RepoConfigResult{}, dErrf("Invalid %s", dRepoConfigFile)
		}
		path := filepath.Join(repoPath, dRepoConfigFile)
		text, err := dSerializeRepoConfig(clean)
		if err != nil {
			return RepoConfigResult{}, err
		}
		if err := os.WriteFile(path, []byte(text), 0o666); err != nil {
			return RepoConfigResult{}, dNodeFSError("open", path, "", err)
		}
		return RepoConfigResult{Path: path, Exists: true, Config: clean, Issues: issues}, nil
	})
}

// ─── The doctor ─────────────────────────────────────────────────────────────

var dLeadingVRe = regexp.MustCompile(`^v`)

func dNodeCheck(ctx context.Context, spec string) DoctorCheck {
	c := DoctorCheck{ID: "node", Kind: "node", Expected: spec}
	// PATH was repaired at startup (gitx.FixPath), so this sees the same node
	// the user's terminal does rather than the one Finder handed us.
	cwd, _ := os.Getwd()
	out, err := dRun(ctx, cwd, "node", "--version")
	if err != nil {
		c.Status = "fail"
		return c
	}
	c.Actual = dLeadingVRe.ReplaceAllString(out, "")
	c.Status = "warn"
	if dSatisfiesNodeRange(c.Actual, spec) {
		c.Status = "ok"
	}
	return c
}

func dSubmodulesCheck(ctx context.Context, repoPath string) *DoctorCheck {
	if !dExists(filepath.Join(repoPath, ".gitmodules")) {
		return nil
	}
	out, err := dRepoGit(ctx, repoPath, "submodule", "status", "--recursive")
	if err != nil {
		out = ""
	}
	// `git submodule status` prefixes a line with `-` for a submodule that has
	// never been checked out — the state that makes a fresh clone fail to build.
	missing := 0
	for _, l := range dLines(out) {
		if strings.HasPrefix(l, "-") {
			missing++
		}
	}
	if missing == 0 {
		return &DoctorCheck{ID: "submodules", Kind: "submodules", Status: "ok"}
	}
	return &DoctorCheck{ID: "submodules", Kind: "submodules", Status: "fail", Actual: strconv.Itoa(missing), Fix: &DoctorFix{Kind: "submodules"}}
}

func dLfsCheck(ctx context.Context, repoPath string) DoctorCheck {
	c := DoctorCheck{ID: "lfs", Kind: "lfs"}
	if _, err := dRun(ctx, repoPath, "git", "lfs", "version"); err != nil {
		// git-lfs is not installed at all — nothing Gitcito can repair from here.
		c.Status = "fail"
		return c
	}
	out, err := dRepoGit(ctx, repoPath, "lfs", "ls-files", "-n")
	if err != nil {
		out = ""
	}
	var names []string
	for _, l := range dLines(out) {
		if l != "" {
			names = append(names, l)
		}
	}
	c.Status = "ok"
	if len(names) == 0 {
		return c
	}
	// A tracked file still holding its pointer text was cloned without the
	// objects: the build reads 130 bytes of metadata where an asset should be.
	first := names[0]
	if full, err := dInside(repoPath, first); err == nil {
		if head, err := dReadUTF8(full); err == nil && strings.HasPrefix(head, "version https://git-lfs") {
			return DoctorCheck{ID: "lfs", Kind: "lfs", Status: "warn", Actual: first, Fix: &DoctorFix{Kind: "lfsPull"}}
		}
	}
	return c
}

func dHooksCheck(ctx context.Context, repoPath, expected string) DoctorCheck {
	raw, err := dRepoGit(ctx, repoPath, "config", "--get", "core.hooksPath")
	if err != nil {
		raw = ""
	}
	raw = gitx.TrimJS(raw)
	// husky's installer writes an absolute path; the config can only say a
	// relative one. Compare them in the same terms, or a perfectly wired clone
	// wears a red row forever.
	actual := ""
	if raw != "" {
		actual = dRelativeInside(repoPath, raw)
		if actual == "" {
			actual = raw
		}
	}
	c := DoctorCheck{ID: "hooks", Kind: "hooks", Expected: expected, Actual: actual, Status: "ok"}
	if actual != expected {
		c.Status = "fail"
		c.Fix = &DoctorFix{Kind: "hooksPath", Value: expected}
	}
	return c
}

// dFileCheck returns nil where the TS check threw (an unsafe `from`), which
// dropped the row.
func dFileCheck(repoPath string, req RepoConfigFileReq) *DoctorCheck {
	c := DoctorCheck{ID: "file:" + req.Path, Kind: "file", Expected: req.Path, Why: req.Why}
	if full, err := dInside(repoPath, req.Path); err == nil && dExists(full) {
		c.Status = "ok"
		return &c
	}
	canCopy := false
	if req.From != "" {
		from, err := dInside(repoPath, req.From)
		if err != nil {
			return nil
		}
		canCopy = dExists(from)
	}
	c.Status = "fail"
	if canCopy {
		c.Fix = &DoctorFix{Kind: "copyFile", From: req.From, To: req.Path}
	}
	return &c
}

// RepoDoctor runs every check the repository's config asks for. It reads the
// config itself rather than taking it from the caller, so a doctor run always
// reflects what is on disk right now — the file is a tracked file and a branch
// switch can change it underneath the panel.
//
// gitsvc: repoDoctor
func (s *GitService) RepoDoctor(ctx context.Context, repoPath string) ([]DoctorCheck, error) {
	return locked(s, ctx, "repoDoctor", repoPath, func(ctx context.Context) ([]DoctorCheck, error) {
		checks := []DoctorCheck{}
		cfg := dReadRepoConfig(repoPath).Config
		if cfg == nil || cfg.Requires == nil {
			return checks, nil
		}
		req := cfg.Requires
		// One check that fails must not take the panel down with it.
		if req.Node != "" {
			checks = append(checks, dNodeCheck(ctx, req.Node))
		}
		if req.Submodules != nil && *req.Submodules {
			if c := dSubmodulesCheck(ctx, repoPath); c != nil {
				checks = append(checks, *c)
			}
		}
		if req.Lfs != nil && *req.Lfs {
			checks = append(checks, dLfsCheck(ctx, repoPath))
		}
		if req.HooksPath != "" {
			checks = append(checks, dHooksCheck(ctx, repoPath, req.HooksPath))
		}
		for _, f := range req.Files {
			if c := dFileCheck(repoPath, f); c != nil {
				checks = append(checks, *c)
			}
		}
		return checks, nil
	})
}

// dCopyFile is fs.copyFile: the destination is overwritten and takes the
// source's permissions.
func dCopyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return dNodeFSError("copyfile", from, to, err)
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return dNodeFSError("copyfile", from, to, err)
	}
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return dNodeFSError("copyfile", from, to, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return dNodeFSError("copyfile", from, to, err)
	}
	if err := dst.Close(); err != nil {
		return dNodeFSError("copyfile", from, to, err)
	}
	return os.Chmod(to, st.Mode().Perm())
}

// ApplyDoctorFix performs one of the doctor's repairs. The union is closed on
// purpose; an unknown kind does nothing.
//
// gitsvc: applyDoctorFix
func (s *GitService) ApplyDoctorFix(ctx context.Context, repoPath string, fix DoctorFix) error {
	return lockedErr(s, ctx, "applyDoctorFix", repoPath, func(ctx context.Context) error {
		switch fix.Kind {
		case "submodules":
			_, err := dRepoGit(ctx, repoPath, "submodule", "update", "--init", "--recursive")
			return err
		case "lfsPull":
			_, err := dRepoGit(ctx, repoPath, "lfs", "pull")
			return err
		case "hooksPath":
			// Re-validated: this value came from a file in the repository.
			if !dIsSafeRepoRelPath(fix.Value) {
				return dErrf("Unsafe hooksPath: %s", fix.Value)
			}
			_, err := dRepoGit(ctx, repoPath, "config", "core.hooksPath", fix.Value)
			return err
		case "copyFile":
			from, err := dInside(repoPath, fix.From)
			if err != nil {
				return err
			}
			to, err := dInside(repoPath, fix.To)
			if err != nil {
				return err
			}
			// Never clobber: the file being absent is the whole reason we are here,
			// and a config that could overwrite one would be a way to destroy work.
			if dExists(to) {
				return dErrf("%s already exists", fix.To)
			}
			return dCopyFile(from, to)
		}
		return nil
	})
}

// ─── Proposing a config ─────────────────────────────────────────────────────

var (
	dScopeRe  = regexp.MustCompile(`^\w+\(([^)]{1,40})\)!?:`)
	dTicketRe = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b`)
)

// SuggestRepoConfig reads a repository and proposes the config it evidently
// wants.
//
// The point is that nobody writes this file from a blank page. Everything here
// is inferred from something already in the repository — `.gitmodules`,
// `.nvmrc`, a husky directory, an `.env.example` sitting next to no `.env`. It
// deliberately proposes nothing it cannot see evidence for: ticket links need
// a tracker URL only a human knows, so they are left for the editor.
//
// gitsvc: suggestRepoConfig
func (s *GitService) SuggestRepoConfig(ctx context.Context, repoPath string) (RepoConfig, error) {
	return locked(s, ctx, "suggestRepoConfig", repoPath, func(ctx context.Context) (RepoConfig, error) {
		config := RepoConfig{Version: dRepoConfigVersion}
		yes := true

		branches, err := dRepoGit(ctx, repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads")
		if err != nil {
			branches = ""
		}
		locals := map[string]bool{}
		for _, b := range dLines(branches) {
			if b != "" {
				locals[b] = true
			}
		}
		var protect []string
		seen := map[string]bool{}
		add := func(b string) {
			if !seen[b] {
				seen[b] = true
				protect = append(protect, b)
			}
		}
		for _, b := range []string{"main", "master", "develop"} {
			if locals[b] {
				add(b)
			}
		}
		// Existing protection is per-clone (git config); lifting it into the file
		// is how it stops being something each teammate has to rediscover.
		configured, err := dRepoGit(ctx, repoPath, "config", "--get", "gitcito.protectedbranches")
		if err != nil {
			configured = ""
		}
		for _, b := range strings.Split(configured, ",") {
			if b = gitx.TrimJS(b); b != "" {
				add(b)
			}
		}
		config.Protect = protect

		req := &RepoConfigRequires{}
		hasReq := false
		if dExists(filepath.Join(repoPath, ".gitmodules")) {
			req.Submodules, hasReq = &yes, true
		}
		if attrs, err := dReadUTF8(filepath.Join(repoPath, ".gitattributes")); err == nil && strings.Contains(attrs, "filter=lfs") {
			req.Lfs, hasReq = &yes, true
		}
		nvmrc, err := dReadUTF8(filepath.Join(repoPath, ".nvmrc"))
		if err != nil {
			nvmrc = ""
		}
		if nvmrc = gitx.TrimJS(nvmrc); nvmrc != "" {
			req.Node, hasReq = dLeadingVRe.ReplaceAllString(nvmrc, ""), true
		} else if pkg, err := dReadUTF8(filepath.Join(repoPath, "package.json")); err == nil && pkg != "" {
			var parsed struct {
				Engines struct {
					Node any `json:"node"`
				} `json:"engines"`
			}
			if err := json.Unmarshal([]byte(pkg), &parsed); err != nil {
				return RepoConfig{}, err
			}
			if node, ok := parsed.Engines.Node.(string); ok && node != "" {
				req.Node, hasReq = node, true
			}
		}

		// Prefer what the clone is actually configured with over what the
		// directory layout suggests: husky v9 points core.hooksPath at
		// `.husky/_`, and its installer writes an absolute path. Proposing
		// `.husky` from the directory alone would hand every teammate a doctor
		// row that "fixes" a working setup into a broken one.
		hooksPath, err := dRepoGit(ctx, repoPath, "config", "--get", "core.hooksPath")
		if err != nil {
			hooksPath = ""
		}
		hooksPath = gitx.TrimJS(hooksPath)
		relHooks := ""
		if hooksPath != "" {
			relHooks = dRelativeInside(repoPath, hooksPath)
		}
		if relHooks != "" {
			req.HooksPath, hasReq = relHooks, true
		} else if hooksPath == "" && dExists(filepath.Join(repoPath, ".husky")) {
			req.HooksPath, hasReq = ".husky", true
		}

		// An example file with no counterpart is the single most common reason a
		// fresh clone does not run.
		for _, example := range []string{".env.example", ".env.sample", ".env.template"} {
			if dExists(filepath.Join(repoPath, example)) && !dExists(filepath.Join(repoPath, ".env")) {
				req.Files, hasReq = []RepoConfigFileReq{{Path: ".env", From: example}}, true
				break
			}
		}
		if hasReq {
			config.Requires = req
		}

		// Scopes: the ones this repository's own history has been using.
		subjects, err := dRepoGit(ctx, repoPath, "log", "-500", "--format=%s")
		if err != nil {
			subjects = ""
		}
		type scopeCount struct {
			scope string
			n     int
		}
		var scopes []scopeCount
		index := map[string]int{}
		tickets := 0
		for _, line := range dLines(subjects) {
			if m := dScopeRe.FindStringSubmatch(line); m != nil {
				if i, ok := index[m[1]]; ok {
					scopes[i].n++
				} else {
					index[m[1]] = len(scopes)
					scopes = append(scopes, scopeCount{m[1], 1})
				}
			}
			if dTicketRe.MatchString(line) {
				tickets++
			}
		}
		var common []scopeCount
		for _, sc := range scopes {
			if sc.n >= 2 {
				common = append(common, sc)
			}
		}
		sort.SliceStable(common, func(i, j int) bool { return common[i].n > common[j].n })
		if len(common) > 20 {
			common = common[:20]
		}
		commit := &RepoConfigCommit{}
		hasCommit := false
		if len(common) > 0 {
			for _, sc := range common {
				commit.Scopes = append(commit.Scopes, sc.scope)
			}
			dSortJS(commit.Scopes)
			hasCommit = true
		}
		// A tenth of recent commits carrying a ticket key is a convention, not a coincidence.
		if tickets >= 5 {
			commit.TicketFromBranch, hasCommit = &yes, true
		}
		if hasCommit {
			config.Commit = commit
		}
		return config, nil
	})
}
