package gitsvc

// Attributes, diff drivers and clean/smudge filters.
//
// `.gitattributes` is how a repository teaches git about its own files — which
// are binary, which should concatenate on merge, which never leave the repo in
// an archive. It travels with the clone, so one person fixing it fixes it for
// everyone.
//
// A filter runs on every checkout of every matching file, and a wrong one
// corrupts a working tree quietly. So configuring one here is gated on a dry
// run against real repository files, and SetFilterDriver returns what it
// overwrote so the store can hang an undo entry on it.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/textconv"
)

// AttributeFile is AttributeFile in src/shared/types.ts.
type AttributeFile struct {
	// Path is repo-relative, or `.git/info/attributes` for the private one.
	Path    string `json:"path"`
	Content string `json:"content"`
	// Local is true for `.git/info/attributes`: never committed, never shared.
	Local bool `json:"local"`
}

// AttributeCheck is AttributeCheck in src/shared/types.ts.
type AttributeCheck struct {
	Path string `json:"path"`
	// Attrs maps attribute name → value: `set`, `unset`, `unspecified`, or a string.
	Attrs map[string]string `json:"attrs"`
}

// DiffDriverInfo is DiffDriverInfo in src/shared/types.ts.
type DiffDriverInfo struct {
	Name     string `json:"name"`
	Textconv string `json:"textconv"`
	// Available is true when the converter command can actually run.
	Available bool `json:"available"`
	// Scope: 'repo' | 'global'.
	Scope string `json:"scope"`
}

// DiffDriverSuggestion is DiffDriverSuggestion in src/shared/types.ts.
type DiffDriverSuggestion struct {
	Name     string   `json:"name"`
	Patterns []string `json:"patterns"`
	Textconv string   `json:"textconv"`
	// Binary is what it needs ('' when Gitcito ships the converter itself).
	Binary string `json:"binary"`
	// Bundled is true when the converter is the one Gitcito ships.
	Bundled   bool `json:"bundled,omitempty"`
	Available bool `json:"available"`
}

// FilterDriverInfo is FilterDriverInfo in src/shared/types.ts.
type FilterDriverInfo struct {
	Name string `json:"name"`
	// Clean runs on staging (working tree → repository). '' when unset.
	Clean string `json:"clean"`
	// Smudge runs on checkout (repository → working tree). '' when unset.
	Smudge string `json:"smudge"`
	// Required makes a failing filter abort instead of passing bytes through.
	Required        bool   `json:"required"`
	Scope           string `json:"scope"`
	CleanAvailable  bool   `json:"cleanAvailable"`
	SmudgeAvailable bool   `json:"smudgeAvailable"`
}

// FilterDryRunFile is FilterDryRunFile in src/shared/types.ts.
type FilterDryRunFile struct {
	File string `json:"file"`
	// OK: the clean command ran and exited 0.
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Roundtrip: 'ok' | 'different' | 'skipped' — did smudge(clean(file)) give the original back.
	Roundtrip string `json:"roundtrip"`
	// Preview is the first bytes of the cleaned output: what git would store.
	Preview string `json:"preview"`
}

// FilterDryRunResult is FilterDryRunResult in src/shared/types.ts.
type FilterDryRunResult struct {
	Matched int                `json:"matched"`
	Tested  []FilterDryRunFile `json:"tested"`
}

// FilterDriverPrevious is FilterDriverPrevious in src/shared/types.ts: what
// `filter.<name>` held before SetFilterDriver overwrote it — the undo payload.
type FilterDriverPrevious struct {
	Clean    string `json:"clean"`
	Smudge   string `json:"smudge"`
	Required bool   `json:"required"`
}

// FilterDriverInput is setFilterDriver's `driver` argument.
type FilterDriverInput struct {
	Clean    string `json:"clean"`
	Smudge   string `json:"smudge"`
	Required bool   `json:"required"`
}

// Dry-run bounds: enough to judge a filter, cheap enough to run on a click.
const (
	dFilterDryRunFiles    = 5
	dFilterDryRunMaxBytes = 5 * 1024 * 1024
	dFilterPreviewBytes   = 400
)

// AttributeFiles returns every `.gitattributes` in the repository, plus the
// private local one (`.git/info/attributes`), with their text.
//
// gitsvc: attributeFiles
func (s *GitService) AttributeFiles(ctx context.Context, repoPath string) ([]AttributeFile, error) {
	return locked(s, ctx, "attributeFiles", repoPath, func(ctx context.Context) ([]AttributeFile, error) {
		tracked, err := gitx.RunGit(ctx, repoPath, []string{"ls-files", "-z", "--", ".gitattributes", "**/.gitattributes"}, gitx.RunOpts{})
		if err != nil {
			tracked = ""
		}
		set := map[string]bool{}
		var paths []string
		add := func(p string) {
			if p != "" && !set[p] {
				set[p] = true
				paths = append(paths, p)
			}
		}
		for _, p := range strings.Split(tracked, "\x00") {
			add(p)
		}
		// An untracked root file is the common case right after someone creates one.
		if dExists(filepath.Join(repoPath, ".gitattributes")) {
			add(".gitattributes")
		}
		dSortJS(paths)
		files := []AttributeFile{}
		for _, p := range paths {
			content, err := dReadUTF8(filepath.Join(repoPath, p))
			if err != nil {
				content = ""
			}
			files = append(files, AttributeFile{Path: p, Content: content, Local: false})
		}
		infoPath := filepath.Join(dGitDir(ctx, repoPath), "info", "attributes")
		if dExists(infoPath) {
			content, err := dReadUTF8(infoPath)
			if err != nil {
				content = ""
			}
			files = append(files, AttributeFile{Path: ".git/info/attributes", Content: content, Local: true})
		}
		return files, nil
	})
}

// AttributeWrite writes one attributes file whole.
//
// The editing is done in the renderer against the file's text, so comments and
// ordering survive; this only has to refuse to write anywhere else.
//
// gitsvc: attributeWrite
func (s *GitService) AttributeWrite(ctx context.Context, repoPath, path, content string) error {
	return lockedErr(s, ctx, "attributeWrite", repoPath, func(ctx context.Context) error {
		gitDir := dGitDir(ctx, repoPath)
		target := dResolve(repoPath, path)
		if path == ".git/info/attributes" {
			target = filepath.Join(gitDir, "info", "attributes")
		}
		root := dResolve(repoPath)
		if base := filepath.Base(target); base != ".gitattributes" && base != "attributes" {
			return dErrf("Not an attributes file: %s", path)
		}
		sep := string(filepath.Separator)
		if !strings.HasPrefix(target, root+sep) && !strings.HasPrefix(target, gitDir+sep) {
			return dErrf("Refusing to write outside the repository: %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
			return dNodeFSError("mkdir", filepath.Dir(target), "", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o666); err != nil {
			return dNodeFSError("open", target, "", err)
		}
		return nil
	})
}

// CheckAttributes asks git what applies to these paths — the answer no
// hand-reading of the files gives.
//
// gitsvc: checkAttributes
func (s *GitService) CheckAttributes(ctx context.Context, repoPath string, paths []string) ([]AttributeCheck, error) {
	return locked(s, ctx, "checkAttributes", repoPath, func(ctx context.Context) ([]AttributeCheck, error) {
		var wanted []string
		for _, p := range paths {
			if p = gitx.TrimJS(p); p != "" {
				wanted = append(wanted, p)
			}
		}
		if len(wanted) == 0 {
			return []AttributeCheck{}, nil
		}
		// `-a` asks for every attribute git knows about for the path, `-z` keeps
		// paths with spaces intact: <path>\0<attr>\0<value>\0…
		out, err := gitx.RunGit(ctx, repoPath, append([]string{"check-attr", "-a", "-z", "--"}, wanted...), gitx.RunOpts{})
		if err != nil {
			out = ""
		}
		byPath := map[string]map[string]string{}
		for _, p := range wanted {
			byPath[p] = map[string]string{}
		}
		fields := strings.Split(out, "\x00")
		for i := 0; i+2 < len(fields); i += 3 {
			path, attr, value := fields[i], fields[i+1], fields[i+2]
			if path == "" {
				continue
			}
			entry, ok := byPath[path]
			if !ok {
				entry = map[string]string{}
				byPath[path] = entry
			}
			entry[attr] = value
		}
		checks := make([]AttributeCheck, 0, len(wanted))
		for _, p := range wanted {
			checks = append(checks, AttributeCheck{Path: p, Attrs: byPath[p]})
		}
		return checks, nil
	})
}

var dDiffKeyRe = regexp.MustCompile(`^diff\.(.*)\.textconv$`)

// DiffDrivers lists the diff drivers configured here (repo, then global), and
// whether their converter is installed.
//
// gitsvc: diffDrivers
func (s *GitService) DiffDrivers(ctx context.Context, repoPath string) ([]DiffDriverInfo, error) {
	return locked(s, ctx, "diffDrivers", repoPath, func(ctx context.Context) ([]DiffDriverInfo, error) {
		drivers := []DiffDriverInfo{}
		for _, scope := range []string{"repo", "global"} {
			flag := "--local"
			if scope == "global" {
				flag = "--global"
			}
			out, err := gitx.RunGit(ctx, repoPath, []string{"config", flag, "--get-regexp", `^diff\..*\.textconv`}, gitx.RunOpts{})
			if err != nil {
				out = ""
			}
			for _, line := range dLines(out) {
				space := strings.IndexByte(line, ' ')
				if space < 0 {
					continue
				}
				m := dDiffKeyRe.FindStringSubmatch(line[:space])
				textconv := gitx.TrimJS(line[space+1:])
				if m == nil || m[1] == "" || textconv == "" {
					continue
				}
				drivers = append(drivers, DiffDriverInfo{Name: m[1], Textconv: textconv, Available: gitx.ConverterAvailable(textconv), Scope: scope})
			}
		}
		return drivers, nil
	})
}

// DiffDriverSuggestions lists the converters Gitcito can wire up for the
// formats it already previews. It takes the repo path it never uses: every
// repo-scoped method does, and the dispatcher locks on it.
//
// gitsvc: diffDriverSuggestions
func (s *GitService) DiffDriverSuggestions(ctx context.Context, repoPath string) ([]DiffDriverSuggestion, error) {
	return locked(s, ctx, "diffDriverSuggestions", repoPath, func(ctx context.Context) ([]DiffDriverSuggestion, error) {
		// Word, Excel and JSON ride the converter Gitcito ships, so they work
		// with nothing installed. The rest still need a real tool on PATH, and
		// the UI says plainly which are missing rather than writing a driver
		// that fails at the first diff.
		shim := textconv.BundledPath()
		shimConv := `"` + shim + `"`
		shimOK := gitx.CanExecute(shim)
		return []DiffDriverSuggestion{
			{Name: "word", Patterns: []string{"*.docx"}, Textconv: shimConv, Binary: "", Bundled: true, Available: shimOK},
			{Name: "excel", Patterns: []string{"*.xlsx", "*.xls"}, Textconv: shimConv, Binary: "", Bundled: true, Available: shimOK},
			{Name: "json", Patterns: []string{"*.json", "*.xcstrings"}, Textconv: shimConv, Binary: "", Bundled: true, Available: shimOK},
			// Git calls a UTF-16 .strings binary and shows no diff at all, which is
			// the one file where seeing which string moved matters most.
			{Name: "strings", Patterns: []string{"*.strings"}, Textconv: shimConv, Binary: "", Bundled: true, Available: shimOK},
			{Name: "pdf", Patterns: []string{"*.pdf"}, Textconv: "pdftotext -layout - -", Binary: "pdftotext", Available: gitx.HasBinary("pdftotext")},
			{Name: "exif", Patterns: []string{"*.jpg", "*.jpeg", "*.png"}, Textconv: "exiftool", Binary: "exiftool", Available: gitx.HasBinary("exiftool")},
		}, nil
	})
}

// SetDiffDriver sets (or, with an empty textconv, clears) `diff.<name>.textconv`
// in the repository or, with global, the user's config.
//
// gitsvc: setDiffDriver
func (s *GitService) SetDiffDriver(ctx context.Context, repoPath, name, textconv string, global bool) error {
	return lockedErr(s, ctx, "setDiffDriver", repoPath, func(ctx context.Context) error {
		scope := "--local"
		if global {
			scope = "--global"
		}
		key := "diff." + name + ".textconv"
		if t := gitx.TrimJS(textconv); t != "" {
			_, err := gitx.RunGit(ctx, repoPath, []string{"config", scope, key, t}, gitx.RunOpts{})
			return err
		}
		_, _ = gitx.RunGit(ctx, repoPath, []string{"config", scope, "--unset", key}, gitx.RunOpts{})
		return nil
	})
}

var dFilterKeyRe = regexp.MustCompile(`^filter\.(.+)\.(clean|smudge|required)$`)

// FilterDrivers lists the configured `filter.<name>.clean/smudge` drivers,
// repo then global.
//
// gitsvc: filterDrivers
func (s *GitService) FilterDrivers(ctx context.Context, repoPath string) ([]FilterDriverInfo, error) {
	return locked(s, ctx, "filterDrivers", repoPath, func(ctx context.Context) ([]FilterDriverInfo, error) {
		drivers := []FilterDriverInfo{}
		for _, scope := range []string{"repo", "global"} {
			flag := "--local"
			if scope == "global" {
				flag = "--global"
			}
			out, err := gitx.RunGit(ctx, repoPath, []string{"config", flag, "--get-regexp", `^filter\..*\.(clean|smudge|required)$`}, gitx.RunOpts{})
			if err != nil {
				out = ""
			}
			var order []string
			byName := map[string]*FilterDriverInfo{}
			for _, line := range dLines(out) {
				space := strings.IndexByte(line, ' ')
				if space < 0 {
					continue
				}
				m := dFilterKeyRe.FindStringSubmatch(line[:space])
				if m == nil {
					continue
				}
				value := gitx.TrimJS(line[space+1:])
				info, ok := byName[m[1]]
				if !ok {
					info = &FilterDriverInfo{Name: m[1], Scope: scope, CleanAvailable: true, SmudgeAvailable: true}
					byName[m[1]] = info
					order = append(order, m[1])
				}
				switch m[2] {
				case "clean":
					info.Clean = value
					info.CleanAvailable = gitx.ConverterAvailable(value)
				case "smudge":
					info.Smudge = value
					info.SmudgeAvailable = gitx.ConverterAvailable(value)
				default:
					info.Required = value == "true"
				}
			}
			for _, n := range order {
				drivers = append(drivers, *byName[n])
			}
		}
		return drivers, nil
	})
}

// FilterDryRun tries a clean/smudge pair against real matching files WITHOUT
// configuring anything: run `clean` on a copy of each file's bytes, then
// `smudge` on the result, and report whether the roundtrip reproduces the
// original. Nothing in the repository or its config is touched.
//
// gitsvc: filterDryRun
func (s *GitService) FilterDryRun(ctx context.Context, repoPath, pattern, clean, smudge string) (FilterDryRunResult, error) {
	return locked(s, ctx, "filterDryRun", repoPath, func(ctx context.Context) (FilterDryRunResult, error) {
		listed, err := gitx.RunGit(ctx, repoPath, []string{"ls-files", "-z", "--", pattern}, gitx.RunOpts{})
		if err != nil {
			listed = ""
		}
		var matches []string
		for _, p := range strings.Split(listed, "\x00") {
			if p != "" {
				matches = append(matches, p)
			}
		}
		tested := []FilterDryRunFile{}
		failed := func(file string, err error) {
			tested = append(tested, FilterDryRunFile{File: file, OK: false, Error: err.Error(), Roundtrip: "skipped", Preview: ""})
		}
		for i, file := range matches {
			if i >= dFilterDryRunFiles {
				break
			}
			full := filepath.Join(repoPath, file)
			original, err := os.ReadFile(full)
			if err != nil {
				failed(file, dNodeFSError("open", full, "", err))
				continue
			}
			if len(original) > dFilterDryRunMaxBytes {
				tested = append(tested, FilterDryRunFile{File: file, OK: false, Error: "file too large for a dry run", Roundtrip: "skipped", Preview: ""})
				continue
			}
			cleaned, err := gitx.RunFilterCommand(ctx, clean, repoPath, file, original)
			if err != nil {
				failed(file, err)
				continue
			}
			roundtrip := "skipped"
			if gitx.TrimJS(smudge) != "" {
				smudged, err := gitx.RunFilterCommand(ctx, smudge, repoPath, file, cleaned)
				if err != nil {
					failed(file, err)
					continue
				}
				roundtrip = "different"
				if bytes.Equal(smudged, original) {
					roundtrip = "ok"
				}
			}
			preview := cleaned
			if len(preview) > dFilterPreviewBytes {
				preview = preview[:dFilterPreviewBytes]
			}
			tested = append(tested, FilterDryRunFile{File: file, OK: true, Roundtrip: roundtrip, Preview: dDecodeUTF8(preview)})
		}
		return FilterDryRunResult{Matched: len(matches), Tested: tested}, nil
	})
}

// SetFilterDriver writes (or clears, when both commands are empty) a
// `filter.<name>` driver, and returns the values that were there before, for
// the undo entry.
//
// gitsvc: setFilterDriver
func (s *GitService) SetFilterDriver(ctx context.Context, repoPath, name string, driver FilterDriverInput, global bool) (FilterDriverPrevious, error) {
	return locked(s, ctx, "setFilterDriver", repoPath, func(ctx context.Context) (FilterDriverPrevious, error) {
		scope := "--local"
		if global {
			scope = "--global"
		}
		get := func(key string) string { return dGitTrim(ctx, repoPath, "config", scope, "--get", key) }
		previous := FilterDriverPrevious{
			Clean:    get("filter." + name + ".clean"),
			Smudge:   get("filter." + name + ".smudge"),
			Required: get("filter."+name+".required") == "true",
		}
		set := func(key, value string) error {
			if value != "" {
				_, err := gitx.RunGit(ctx, repoPath, []string{"config", scope, key, value}, gitx.RunOpts{})
				return err
			}
			_, _ = gitx.RunGit(ctx, repoPath, []string{"config", scope, "--unset", key}, gitx.RunOpts{})
			return nil
		}
		clean, smudge := gitx.TrimJS(driver.Clean), gitx.TrimJS(driver.Smudge)
		if err := set("filter."+name+".clean", clean); err != nil {
			return FilterDriverPrevious{}, err
		}
		if err := set("filter."+name+".smudge", smudge); err != nil {
			return FilterDriverPrevious{}, err
		}
		required := ""
		if driver.Required && (clean != "" || smudge != "") {
			required = "true"
		}
		if err := set("filter."+name+".required", required); err != nil {
			return FilterDriverPrevious{}, err
		}
		return previous, nil
	})
}
