package secure

// Sharing files as encrypted .gitcito bundles — the I/O half of
// src/main/secureShare.ts. All filesystem and crypto work stays in Go; the
// renderer only ever sees relative paths, sizes and error codes — never key
// material, file contents, secret values or note bodies.

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/vault"
)

// Directories that are all bulk/derived content — walking them would drown the
// picker in noise (and node_modules alone can be 100k files).
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "out": true, "build": true, "coverage": true,
	"target": true, "vendor": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true,
	".turbo": true, ".cache": true, "DerivedData": true, "Pods": true,
}

const (
	maxCandidates = 5000
	// maxFileBytes is the per-file cap that keeps bundles sane (JSON+base64).
	maxFileBytes = 16 * 1024 * 1024
	// gitMaxBuffer is gitOut's maxBuffer.
	gitMaxBuffer = 16 * 1024 * 1024
)

// The dialog copy, verbatim from the TS (main-process strings, never translated).
const (
	exportTitle = "Export secure bundle"
	importTitle = "Import secure bundle"
)

var (
	bundleFilter = FileFilter{Name: "Gitcito bundle", Extensions: []string{"gitcito"}}
	allFilter    = FileFilter{Name: "All files", Extensions: []string{"*"}}
	unsafeName   = regexp.MustCompile(`[^\w.-]+`)
	noteSha      = regexp.MustCompile(`^[0-9a-f]{4,64}$`)
)

// VaultAccess is the global vault as secure share uses it: *vault.Store.
type VaultAccess interface {
	ExportGlobalEntries(ctx context.Context) ([]vault.BundleEntry, error)
	ImportGlobalEntries(ctx context.Context, entries []vault.BundleEntry) (int, error)
}

// Options configures a Share.
type Options struct {
	// Vault is required.
	Vault VaultAccess
	// Dialogs defaults to WailsDialogs.
	Dialogs Dialogs
}

// Share is the secure-share core. It holds no mutable state; safe for
// concurrent use.
type Share struct {
	vault   VaultAccess
	dialogs Dialogs
	// pack is PackSections; tests pin its randomness.
	pack func(project string, sections []Section, password string) (string, error)
}

// New builds a Share.
func New(opts Options) *Share {
	if opts.Vault == nil {
		panic("secure: Options.Vault is required")
	}
	s := &Share{vault: opts.Vault, dialogs: opts.Dialogs, pack: PackSections}
	if s.dialogs == nil {
		s.dialogs = WailsDialogs{}
	}
	return s
}

// gitOut is pexecFile('git', ['-C', repo, ...args]) with the inherited
// environment and a 16 MiB buffer, stdout decoded as UTF-8.
func gitOut(ctx context.Context, repoPath string, args ...string) (string, error) {
	res, err := gitx.Exec(ctx, "", "git", append([]string{"-C", repoPath}, args...),
		gitx.RunOpts{PlainEnv: true, MaxBuffer: gitMaxBuffer})
	if err != nil {
		return "", err
	}
	return decodeUTF8(res.Stdout), nil
}

func trimOneNewline(s string) string { return strings.TrimSuffix(s, "\n") }

// ReadRepoNotes is readRepoNotes: every note in the shared notes ref of a repo,
// bodies included.
func ReadRepoNotes(ctx context.Context, repoPath string) []BundleNote {
	raw, err := gitOut(ctx, repoPath, "notes", "--ref="+NotesRef, "list")
	if err != nil {
		raw = ""
	}
	notes := []BundleNote{}
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.Split(gitx.TrimJS(line), " ")
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		sha := parts[1]
		body, err := gitOut(ctx, repoPath, "notes", "--ref="+NotesRef, "show", sha)
		if err == nil {
			notes = append(notes, BundleNote{Sha: sha, Body: trimOneNewline(body)})
		}
	}
	return notes
}

// resolveInside is the repo-relative resolution that refuses to land outside
// the repo root.
func resolveInside(repoPath, relPath string) (string, bool) {
	if !IsSafeRelPath(relPath) {
		return "", false
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return "", false
	}
	full := filepath.Join(root, relPath)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

// Candidates is listCandidates: every file under the repo (bulk directories and
// symlinks skipped, files over 16 MiB left out, at most 5000), secrets first
// and then by localeCompare.
func (s *Share) Candidates(repoPath string) []Candidate {
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return []Candidate{}
	}
	found := []Candidate{}
	var walk func(dir string)
	walk = func(dir string) {
		if len(found) >= maxCandidates {
			return
		}
		entries, err := os.ReadDir(dir) // sorted by name, like libuv's scandir
		if err != nil {
			return
		}
		for _, entry := range entries {
			if len(found) >= maxCandidates {
				return
			}
			full := filepath.Join(dir, entry.Name())
			mode := entry.Type()
			switch {
			case mode&os.ModeSymlink != 0:
				continue // never follow links out of the repo
			case entry.IsDir():
				if !skipDirs[entry.Name()] {
					walk(full)
				}
			case mode.IsRegular():
				st, err := os.Stat(full)
				if err != nil {
					continue // unreadable file — skip
				}
				if st.Size() > maxFileBytes {
					continue
				}
				rel, err := filepath.Rel(root, full)
				if err != nil {
					continue
				}
				rel = filepath.ToSlash(rel)
				found = append(found, Candidate{Path: rel, Size: st.Size(), Secret: isSecretFile(rel)})
			}
		}
	}
	walk(root)
	// Secrets first (they're what this feature exists for), then alphabetical.
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.Secret != b.Secret {
			return a.Secret
		}
		return localeCompare(a.Path, b.Path) < 0
	})
	return found
}

// readRepoFile reads one selected file for export: content and executable bit.
func readRepoFile(repoPath, rel string) (BundleFile, string) {
	full, ok := resolveInside(repoPath, rel)
	if !ok {
		return BundleFile{}, ErrInvalid
	}
	content, err := os.ReadFile(full)
	if err != nil {
		return BundleFile{}, ErrReadFailed
	}
	st, err := os.Stat(full)
	if err != nil {
		return BundleFile{}, ErrReadFailed
	}
	return BundleFile{
		Path:       strings.ReplaceAll(rel, `\`, "/"),
		Content:    content,
		Executable: st.Mode().Perm()&0o111 != 0,
	}, ""
}

func safeFileName(project, fallback string) string {
	if project == "" {
		project = fallback
	}
	return unsafeName.ReplaceAllString(project, "-") + ".gitcito"
}

// saveBundle asks where to save, then packs and writes owner-only.
func (s *Share) saveBundle(project, fallbackName string, sections []Section, password string) (ExportResult, error) {
	filePath, err := s.dialogs.SaveFile(exportTitle, safeFileName(project, fallbackName), []FileFilter{bundleFilter})
	if err != nil {
		return ExportResult{}, err
	}
	if filePath == "" {
		return ExportResult{Canceled: true}, nil
	}
	text, err := s.pack(project, sections, password)
	if err != nil {
		return ExportResult{Error: ErrWriteFailed}, nil
	}
	if err := os.WriteFile(filePath, []byte(text), 0o600); err != nil {
		return ExportResult{Error: ErrWriteFailed}, nil
	}
	return ExportResult{Path: filePath}, nil
}

// Export is exportBundle (secure:export): one repo's selected files as a bundle.
func (s *Share) Export(repoPath, project string, paths []string, password string) (ExportResult, error) {
	files := []BundleFile{}
	for _, rel := range paths {
		f, code := readRepoFile(repoPath, rel)
		if code != "" {
			return ExportResult{Error: code}, nil
		}
		files = append(files, f)
	}
	return s.saveBundle(project, "project",
		[]Section{{Kind: KindRepo, Project: project, Folder: project, Files: files}}, password)
}

func readText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return decodeUTF8(string(raw)), nil
}

func readSections(bundlePath, password string) ([]Section, error) {
	text, err := readText(bundlePath)
	if err != nil {
		return nil, err
	}
	return UnpackSections(text, password)
}

// Pick is pickBundle (secure:pick): choose a bundle file and read its envelope.
// nil when the dialog is cancelled.
func (s *Share) Pick() (*PickResult, error) {
	path, err := s.dialogs.OpenFile(importTitle, []FileFilter{bundleFilter, allFilter})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	text, err := readText(path)
	if err != nil {
		return &PickResult{Error: errCode(err)}, nil
	}
	header, err := ReadBundleHeader(text)
	if err != nil {
		return &PickResult{Error: errCode(err)}, nil
	}
	if header == nil {
		return &PickResult{Error: ErrInvalid}, nil
	}
	return &PickResult{Path: path, Header: header}, nil
}

func previewFiles(files []BundleFile, repoPath string) []PreviewEntry {
	entries := []PreviewEntry{}
	for _, f := range files {
		full, ok := resolveInside(repoPath, f.Path)
		exists := false
		if ok {
			_, err := os.Stat(full)
			exists = err == nil
		}
		entries = append(entries, PreviewEntry{Path: f.Path, Size: len(f.Content), Exists: exists, Safe: ok})
	}
	return entries
}

// Preview is previewBundle (secure:preview): per-file exists/safe against a repo.
func (s *Share) Preview(bundlePath, password, repoPath string) PreviewResult {
	text, err := readText(bundlePath)
	if err != nil {
		return PreviewResult{Error: errCode(err)}
	}
	files, err := UnpackBundle(text, password)
	if err != nil {
		return PreviewResult{Error: errCode(err)}
	}
	return PreviewResult{Files: previewFiles(files, repoPath)}
}

// writeBundleFile writes one file into a repo. Secrets land owner-only;
// everything else keeps normal perms.
func writeBundleFile(full string, f BundleFile) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
		return err
	}
	var mode os.FileMode = 0o644
	switch {
	case f.Executable:
		mode = 0o755
	case isSecretFile(f.Path):
		mode = 0o600
	}
	if err := os.WriteFile(full, f.Content, mode); err != nil {
		return err
	}
	return os.Chmod(full, mode) // WriteFile's mode is ignored when the file exists
}

// Apply is applyBundle (secure:apply): writes the selected files into a repo.
func (s *Share) Apply(bundlePath, password, repoPath string, selected []string) WrittenResult {
	text, err := readText(bundlePath)
	if err != nil {
		return WrittenResult{Error: errCode(err)}
	}
	files, err := UnpackBundle(text, password)
	if err != nil {
		return WrittenResult{Error: errCode(err)}
	}
	wanted := map[string]bool{}
	for _, p := range selected {
		wanted[p] = true
	}
	written := []string{}
	for _, f := range files {
		if !wanted[f.Path] {
			continue
		}
		full, ok := resolveInside(repoPath, f.Path)
		if !ok {
			continue // unsafe path — silently dropped, preview already flagged it
		}
		if err := writeBundleFile(full, f); err != nil {
			return WrittenResult{Error: ErrWriteFailed}
		}
		written = append(written, f.Path)
	}
	return WrittenResult{Written: written}
}

// ─── v2 multi-section export/import ──────────────────────────────────────────

// ExportV2 is exportSectionsV2 (secure:exportV2): builds a bundle from export
// specs and writes it. Repo files are read here; the vault section pulls the
// global vault directly (values never touch the renderer). Empty vault and
// notes sections are dropped; an all-empty request is read-failed.
func (s *Share) ExportV2(ctx context.Context, specs []ExportSpec, project, password string) (ExportResult, error) {
	sections := []Section{}
	for _, spec := range specs {
		switch spec.Kind {
		case KindVault:
			entries, err := s.vault.ExportGlobalEntries(ctx)
			if err != nil {
				return ExportResult{}, err
			}
			if len(entries) > 0 {
				sections = append(sections, Section{Kind: KindVault, Entries: entries})
			}
			continue
		case KindWorkspace:
			// The renderer owns the settings store, so it hands over the already
			// portable tab shape; only sanity-shape it here.
			name, _ := parseRaw(spec.Name)
			tabs, _ := parseRaw(spec.Tabs)
			nameStr, okName := name.(string)
			tabList, okTabs := tabs.([]any)
			if okName && okTabs {
				sections = append(sections, Section{Kind: KindWorkspace, Name: nameStr, Tabs: tabList})
			}
			continue
		case KindNotes:
			notes := ReadRepoNotes(ctx, spec.RepoPath)
			if len(notes) > 0 {
				sections = append(sections, Section{
					Kind: KindNotes, Folder: spec.Folder, Remote: optRemote(spec.Remote), Ref: NotesRef, Notes: notes,
				})
			}
			continue
		}
		files := []BundleFile{}
		for _, rel := range spec.Paths {
			f, code := readRepoFile(spec.RepoPath, rel)
			if code != "" {
				return ExportResult{Error: code}, nil
			}
			files = append(files, f)
		}
		if len(files) > 0 {
			sections = append(sections, Section{
				Kind: KindRepo, Project: spec.Project, Folder: spec.Folder, Remote: optRemote(spec.Remote), Files: files,
			})
		}
	}
	if len(sections) == 0 {
		return ExportResult{Error: ErrReadFailed}, nil
	}
	return s.saveBundle(project, "workspace", sections, password)
}

func optRemote(r string) *string {
	if r == "" {
		return nil
	}
	return &r
}

// openedSections is the renderer-safe shape of decrypted sections.
func openedSections(sections []Section) []any {
	out := make([]any, 0, len(sections))
	for _, s := range sections {
		switch s.Kind {
		case KindVault:
			entries := make([]any, 0, len(s.Entries))
			for _, e := range s.Entries {
				entries = append(entries, noteField(jsObject{{"key", e.Key}}, e.Note))
			}
			out = append(out, jsObject{{"kind", KindVault}, {"entries", entries}})
		case KindWorkspace:
			out = append(out, jsObject{{"kind", KindWorkspace}, {"name", s.Name}, {"tabs", tabsValue(s.Tabs)}})
		case KindNotes:
			// Note bodies stay in Go until apply; the renderer needs only counts.
			o := remoteFields(jsObject{{"kind", KindNotes}, {"folder", s.Folder}}, s.Remote)
			out = append(out, append(o, jsField{"ref", s.Ref}, jsField{"noteCount", len(s.Notes)}))
		default:
			files := make([]any, 0, len(s.Files))
			for _, f := range s.Files {
				fo := jsObject{{"path", f.Path}, {"size", len(f.Content)}}
				if f.Executable {
					fo = append(fo, jsField{"executable", true})
				}
				files = append(files, fo)
			}
			o := remoteFields(jsObject{{"kind", KindRepo}, {"project", s.Project}, {"folder", s.Folder}}, s.Remote)
			out = append(out, append(o, jsField{"files", files}))
		}
	}
	return out
}

// OpenV2 is openBundleV2 (secure:openV2): decrypts a bundle to a renderer-safe
// shape — repo file lists (path/size) and vault keys, never file contents or
// secret values.
func (s *Share) OpenV2(bundlePath, password string) OpenedBundle {
	sections, err := readSections(bundlePath, password)
	if err != nil {
		return OpenedBundle{Error: errCode(err)}
	}
	return OpenedBundle{Version: 2, Sections: openedSections(sections)}
}

// sectionAt is `sections[index]`: undefined for a negative, fractional or
// out-of-range index.
func sectionAt(sections []Section, index float64) (Section, bool) {
	if index < 0 || index != math.Trunc(index) || index >= float64(len(sections)) {
		return Section{}, false
	}
	return sections[int(index)], true
}

// PreviewRepoV2 is previewRepoSectionV2 (secure:previewRepoV2): per-file
// exists/safe check for one repo section against a chosen target repo.
func (s *Share) PreviewRepoV2(bundlePath, password string, sectionIndex float64, repoPath string) PreviewResult {
	sections, err := readSections(bundlePath, password)
	if err != nil {
		return PreviewResult{Error: errCode(err)}
	}
	section, ok := sectionAt(sections, sectionIndex)
	if !ok || section.Kind != KindRepo {
		return PreviewResult{Error: ErrInvalid}
	}
	return PreviewResult{Files: previewFiles(section.Files, repoPath)}
}

// commitExists is `git cat-file -e <sha>^{commit}` succeeding. A sha that
// would read as an option is refused up front; git would have rejected it
// anyway, so the answer is the same.
func commitExists(ctx context.Context, repoPath, sha string) bool {
	if strings.HasPrefix(sha, "-") {
		return false
	}
	_, err := gitOut(ctx, repoPath, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// existingNote is the target's note on sha (one trailing newline dropped), or
// false when there is none.
func existingNote(ctx context.Context, repoPath, ref, sha string) (string, bool) {
	out, err := gitOut(ctx, repoPath, "notes", "--ref="+ref, "show", sha)
	if err != nil {
		return "", false
	}
	return trimOneNewline(out), true
}

// PreviewNotesV2 is previewNotesSectionV2 (secure:previewNotesV2): per-note
// new/same/different check for one notes section against a target repo.
func (s *Share) PreviewNotesV2(ctx context.Context, bundlePath, password string, sectionIndex float64, repoPath string) NotesPreviewResult {
	sections, err := readSections(bundlePath, password)
	if err != nil {
		return NotesPreviewResult{Error: errCode(err)}
	}
	section, ok := sectionAt(sections, sectionIndex)
	if !ok || section.Kind != KindNotes {
		return NotesPreviewResult{Error: ErrInvalid}
	}
	entries := []NotePreviewEntry{}
	for _, n := range section.Notes {
		exists := commitExists(ctx, repoPath, n.Sha)
		state := "new"
		if exists {
			if existing, ok := existingNote(ctx, repoPath, section.Ref, n.Sha); ok {
				state = "different"
				if existing == n.Body {
					state = "same"
				}
			}
		}
		entries = append(entries, NotePreviewEntry{Sha: n.Sha, CommitExists: exists, State: state})
	}
	return NotesPreviewResult{Notes: entries}
}

// ApplyV2 is applyV2 (secure:applyV2): applies a decrypted bundle per the
// plan — repo sections write files into their chosen target repos, a vault
// section merges into the global vault (asking for keychain access if needed),
// a notes section writes git notes.
func (s *Share) ApplyV2(ctx context.Context, bundlePath, password string, plan []ApplyPlan) (ApplyResult, error) {
	sections, err := readSections(bundlePath, password)
	if err != nil {
		return ApplyResult{Error: errCode(err)}, nil
	}
	var res ApplyResult
	for _, step := range plan {
		section, ok := sectionAt(sections, step.SectionIndex)
		if !ok {
			return ApplyResult{Error: ErrInvalid}, nil
		}
		switch step.Kind {
		case KindVault:
			if section.Kind != KindVault {
				return ApplyResult{Error: ErrInvalid}, nil
			}
			wanted := map[string]bool{}
			for _, k := range step.Keys {
				wanted[k] = true
			}
			picked := []BundleVaultEntry{}
			for _, e := range section.Entries {
				if wanted[e.Key] {
					picked = append(picked, e)
				}
			}
			n, err := s.vault.ImportGlobalEntries(ctx, picked)
			if err != nil {
				return ApplyResult{}, err
			}
			res.SecretsWritten += n
		case KindNotes:
			if section.Kind != KindNotes {
				return ApplyResult{Error: ErrInvalid}, nil
			}
			for _, n := range section.Notes {
				if !noteSha.MatchString(n.Sha) {
					continue
				}
				if !commitExists(ctx, step.TargetRepoPath, n.Sha) {
					res.NotesSkipped++
					continue
				}
				if existing, ok := existingNote(ctx, step.TargetRepoPath, section.Ref, n.Sha); ok {
					if existing == n.Body {
						continue // identical — nothing to do, nothing skipped
					}
					if !step.Overwrite {
						res.NotesSkipped++
						continue
					}
				}
				if _, err := gitOut(ctx, step.TargetRepoPath, "notes", "--ref="+section.Ref, "add", "-f", "-m", n.Body, n.Sha); err != nil {
					return ApplyResult{Error: ErrWriteFailed}, nil
				}
				res.NotesWritten++
			}
		default:
			if section.Kind != KindRepo {
				return ApplyResult{Error: ErrInvalid}, nil
			}
			wanted := map[string]bool{}
			for _, p := range step.Paths {
				wanted[p] = true
			}
			for _, f := range section.Files {
				if !wanted[f.Path] {
					continue
				}
				full, ok := resolveInside(step.TargetRepoPath, f.Path)
				if !ok {
					continue // unsafe path — silently dropped, preview flagged it
				}
				if err := writeBundleFile(full, f); err != nil {
					return ApplyResult{Error: ErrWriteFailed}, nil
				}
				res.FilesWritten++
			}
		}
	}
	return res, nil
}
