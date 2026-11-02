package secure

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
	"github.com/GabrySpada/gitcito/internal/vault"
)

// Port of test/secureShare.test.ts and the notes half of
// test/secureTeam.test.ts, plus the channels those files could not reach
// (dialogs, the vault section): real directories, real git, fake dialogs and
// a fake vault.

const sharePW = "correct horse battery"

type fakeDialogs struct {
	open, save string
	err        error
	saveCalls  []string // default names offered
	titles     []string
	filters    [][]FileFilter
}

func (d *fakeDialogs) OpenFile(title string, filters []FileFilter) (string, error) {
	d.titles = append(d.titles, title)
	d.filters = append(d.filters, filters)
	return d.open, d.err
}

func (d *fakeDialogs) SaveFile(title, defaultName string, filters []FileFilter) (string, error) {
	d.titles = append(d.titles, title)
	d.filters = append(d.filters, filters)
	d.saveCalls = append(d.saveCalls, defaultName)
	return d.save, d.err
}

type fakeVault struct {
	global   []vault.BundleEntry
	imported []vault.BundleEntry
	err      error
}

func (v *fakeVault) ExportGlobalEntries(context.Context) ([]vault.BundleEntry, error) {
	return v.global, v.err
}

func (v *fakeVault) ImportGlobalEntries(_ context.Context, e []vault.BundleEntry) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	if len(e) == 0 {
		return 0, nil
	}
	v.imported = append(v.imported, e...)
	return len(e), nil
}

func newShare(d *fakeDialogs, v *fakeVault) *Share {
	if d == nil {
		d = &fakeDialogs{}
	}
	if v == nil {
		v = &fakeVault{}
	}
	return New(Options{Vault: v, Dialogs: d})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func shareRepo(t *testing.T) string {
	repo := filepath.Join(t.TempDir(), "repo")
	writeFile(t, filepath.Join(repo, ".env"), "API_KEY=abc\n")
	writeFile(t, filepath.Join(repo, "app", ".env.prod"), "NODE_ENV=production\n")
	writeFile(t, filepath.Join(repo, "index.ts"), "export {}\n")
	writeFile(t, filepath.Join(repo, "node_modules", "pkg", "index.js"), "x")
	return repo
}

func TestListCandidatesFindsSecretsFirstAndSkipsBulkDirs(t *testing.T) {
	list := newShare(nil, nil).Candidates(shareRepo(t))
	var paths []string
	for _, c := range list {
		paths = append(paths, c.Path)
	}
	if !reflect.DeepEqual(paths, []string{".env", "app/.env.prod", "index.ts"}) {
		t.Fatalf("paths = %v", paths)
	}
	if !list[0].Secret || !list[1].Secret || list[2].Secret {
		t.Errorf("secret flags = %+v", list)
	}
}

func TestCandidatesVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "candidates.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Files []struct {
			Path    string `json:"path"`
			Size    *int64 `json:"size"`
			Content string `json:"content"`
		} `json:"files"`
		Symlinks []struct{ Path, Target string } `json:"symlinks"`
		Expected []Candidate                     `json:"expected"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, f := range v.Files {
		full := filepath.Join(root, filepath.FromSlash(f.Path))
		writeFile(t, full, f.Content)
		if f.Size != nil {
			if err := os.Truncate(full, *f.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, l := range v.Symlinks {
		if err := os.Symlink(l.Target, filepath.Join(root, l.Path)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	got := newShare(nil, nil).Candidates(root)
	if !reflect.DeepEqual(got, v.Expected) {
		t.Errorf("candidates\n got %+v\nwant %+v", got, v.Expected)
	}
}

func TestCandidatesStopAtTheCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxCandidates+20; i++ {
		writeFile(t, filepath.Join(root, "d"+string(rune('a'+i%26)), "f"+strings.Repeat("x", i%7)+string(rune('A'+i/26%26))+string(rune('a'+i/676))), "")
	}
	if n := len(newShare(nil, nil).Candidates(root)); n != maxCandidates {
		t.Errorf("got %d candidates, want %d", n, maxCandidates)
	}
}

// demoBundle is the hand-crafted bundle of the TS test, traversal entry included.
func demoBundle(t *testing.T) string {
	t.Helper()
	text, err := PackBundle("demo", []BundleFile{
		{Path: "app/.env.prod", Content: []byte("NODE_ENV=staging\n")},
		{Path: "fresh/new.env", Content: []byte("NEW=1\n")},
		{Path: "../escape.txt", Content: []byte("pwned")}, // hand-crafted traversal
	}, sharePW)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "demo.gitcito")
	writeFile(t, path, text)
	return path
}

func TestPreviewFlagsExistingAndUnsafe(t *testing.T) {
	repo := shareRepo(t)
	res := newShare(nil, nil).Preview(demoBundle(t), sharePW, repo)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	byPath := map[string]PreviewEntry{}
	for _, f := range res.Files {
		byPath[f.Path] = f
	}
	if e := byPath["app/.env.prod"]; !e.Exists || !e.Safe {
		t.Errorf("app/.env.prod = %+v", e)
	}
	if e := byPath["fresh/new.env"]; e.Exists || !e.Safe {
		t.Errorf("fresh/new.env = %+v", e)
	}
	if e := byPath["../escape.txt"]; e.Safe {
		t.Errorf("../escape.txt = %+v", e)
	}
}

func TestPreviewRejectsWrongPassword(t *testing.T) {
	res := newShare(nil, nil).Preview(demoBundle(t), "nope-nope-nope", shareRepo(t))
	out, _ := json.Marshal(res)
	if string(out) != `{"error":"bad-password"}` {
		t.Errorf("got %s", out)
	}
}

func TestApplyWritesSelectedAndDropsUnsafe(t *testing.T) {
	repo := shareRepo(t)
	bundle := demoBundle(t)
	res := newShare(nil, nil).Apply(bundle, sharePW, repo, []string{"app/.env.prod", "fresh/new.env", "../escape.txt"})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	sort.Strings(res.Written)
	if !reflect.DeepEqual(res.Written, []string{"app/.env.prod", "fresh/new.env"}) {
		t.Errorf("written = %v", res.Written)
	}
	if got := readFile(t, filepath.Join(repo, "app", ".env.prod")); got != "NODE_ENV=staging\n" {
		t.Errorf("app/.env.prod = %q", got)
	}
	if got := readFile(t, filepath.Join(repo, "fresh", "new.env")); got != "NEW=1\n" {
		t.Errorf("fresh/new.env = %q", got)
	}
	// The traversal entry must never land outside the repo.
	if _, err := os.Stat(filepath.Join(repo, "..", "escape.txt")); err == nil {
		t.Error("escape.txt written outside the repo")
	}
	// Secrets land owner-only, even over an existing file.
	if st, _ := os.Stat(filepath.Join(repo, "app", ".env.prod")); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
}

func TestApplyOnlyWritesSelected(t *testing.T) {
	res := newShare(nil, nil).Apply(demoBundle(t), sharePW, shareRepo(t), []string{"app/.env.prod"})
	if !reflect.DeepEqual(res.Written, []string{"app/.env.prod"}) {
		t.Errorf("written = %v", res.Written)
	}
}

func TestExportReadsFilesAndWritesOwnerOnlyBundle(t *testing.T) {
	repo := shareRepo(t)
	if err := os.Chmod(filepath.Join(repo, "index.ts"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.gitcito")
	d := &fakeDialogs{save: out}
	res, err := newShare(d, nil).Export(repo, "my repo/x", []string{".env", "index.ts"}, sharePW)
	if err != nil || res.Path != out {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if !reflect.DeepEqual(d.saveCalls, []string{"my-repo-x.gitcito"}) || d.titles[0] != exportTitle {
		t.Errorf("dialog = %v %v", d.saveCalls, d.titles)
	}
	if !reflect.DeepEqual(d.filters[0], []FileFilter{bundleFilter}) {
		t.Errorf("filters = %+v", d.filters[0])
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode = %v", st.Mode().Perm())
	}
	files, err := UnpackBundle(readFile(t, out), sharePW)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Executable || !files[1].Executable || string(files[0].Content) != "API_KEY=abc\n" {
		t.Errorf("files = %+v", files)
	}
}

func TestExportErrorsAndCancel(t *testing.T) {
	repo := shareRepo(t)
	s := newShare(&fakeDialogs{}, nil)
	for _, c := range []struct {
		paths []string
		want  string
	}{
		{[]string{"../x"}, `{"error":"invalid"}`},
		{[]string{"missing.txt"}, `{"error":"read-failed"}`},
		{[]string{".env"}, `{"canceled":true}`},
	} {
		res, err := s.Export(repo, "", c.paths, sharePW)
		if err != nil {
			t.Fatal(err)
		}
		if out, _ := json.Marshal(res); string(out) != c.want {
			t.Errorf("%v → %s, want %s", c.paths, out, c.want)
		}
	}
	// A dialog failure propagates, like the TS's rejected promise.
	if _, err := newShare(&fakeDialogs{err: errors.New("boom")}, nil).Export(repo, "", []string{".env"}, sharePW); err == nil {
		t.Error("want the dialog error")
	}
	// Unwritable destination.
	res, _ := newShare(&fakeDialogs{save: filepath.Join(t.TempDir(), "no", "such", "dir", "b.gitcito")}, nil).
		Export(repo, "", []string{".env"}, sharePW)
	if res.Error != ErrWriteFailed {
		t.Errorf("res = %+v", res)
	}
}

func TestPick(t *testing.T) {
	bundle := demoBundle(t)
	d := &fakeDialogs{}
	s := newShare(d, nil)
	if res, err := s.Pick(); res != nil || err != nil {
		t.Errorf("cancel = %+v, %v", res, err)
	}
	if d.titles[0] != importTitle || !reflect.DeepEqual(d.filters[0], []FileFilter{bundleFilter, allFilter}) {
		t.Errorf("dialog = %v %+v", d.titles, d.filters)
	}
	d.open = bundle
	res, err := s.Pick()
	if err != nil || res.Path != bundle || res.Header == nil || res.Header.Project != "demo" {
		t.Fatalf("pick = %+v, %v", res, err)
	}
	other := filepath.Join(t.TempDir(), "x.txt")
	writeFile(t, other, "hello")
	d.open = other
	if res, _ := s.Pick(); res.Error != ErrInvalid {
		t.Errorf("non-bundle = %+v", res)
	}
	writeFile(t, other, `{"format":"gitcito-secure-bundle","version":9}`)
	if res, _ := s.Pick(); res.Error != ErrUnsupportedVersion {
		t.Errorf("future = %+v", res)
	}
	d.open = filepath.Join(t.TempDir(), "missing")
	if res, _ := s.Pick(); res.Error != ErrReadFailed {
		t.Errorf("missing = %+v", res)
	}
}

func writeSections(t *testing.T, project string, sections []Section) string {
	t.Helper()
	text, err := PackSections(project, sections, sharePW)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.gitcito")
	writeFile(t, path, text)
	return path
}

func TestExportV2BuildsSections(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := shareRepo(t)
	out := filepath.Join(t.TempDir(), "ws.gitcito")
	d := &fakeDialogs{save: out}
	v := &fakeVault{global: []vault.BundleEntry{{Key: "K", Value: "secret", Note: str("n")}}}
	s := newShare(d, v)
	specs := []ExportSpec{
		{Kind: KindRepo, RepoPath: repo, Project: "api", Folder: "api", Remote: "git@x:a.git", Paths: []string{".env"}},
		{Kind: KindRepo, RepoPath: repo, Project: "empty", Folder: "empty"}, // no files → dropped
		{Kind: KindVault},
		{Kind: KindWorkspace, Name: json.RawMessage(`"WS"`), Tabs: json.RawMessage(`[{"kind":"repo","repo":{"name":"a","folder":"a"}}]`)},
		{Kind: KindWorkspace, Name: json.RawMessage(`7`), Tabs: json.RawMessage(`[]`)}, // not a string → skipped
		{Kind: KindNotes, RepoPath: repo, Folder: "api"},                               // not a repo → no notes → dropped
	}
	res, err := s.ExportV2(context.Background(), specs, "", sharePW)
	if err != nil || res.Path != out {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if d.saveCalls[0] != "workspace.gitcito" {
		t.Errorf("default name = %q", d.saveCalls[0])
	}
	sections, err := UnpackSections(readFile(t, out), sharePW)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, sec := range sections {
		kinds = append(kinds, sec.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{KindRepo, KindVault, KindWorkspace}) {
		t.Fatalf("kinds = %v", kinds)
	}
	if *sections[0].Remote != "git@x:a.git" || sections[1].Entries[0].Value != "secret" {
		t.Errorf("sections = %+v", sections)
	}
	// Nothing to pack at all.
	res, _ = s.ExportV2(context.Background(), []ExportSpec{{Kind: KindRepo, RepoPath: repo}}, "p", sharePW)
	if res.Error != ErrReadFailed {
		t.Errorf("empty = %+v", res)
	}
	res, _ = s.ExportV2(context.Background(), []ExportSpec{{Kind: KindRepo, RepoPath: repo, Paths: []string{"/etc/passwd"}}}, "p", sharePW)
	if res.Error != ErrInvalid {
		t.Errorf("unsafe = %+v", res)
	}
}

func TestOpenV2IsRendererSafe(t *testing.T) {
	path := writeSections(t, "p", []Section{
		{Kind: KindRepo, Project: "api", Folder: "api", Files: []BundleFile{{Path: "run.sh", Content: []byte("abc"), Executable: true}}},
		{Kind: KindVault, Entries: []BundleVaultEntry{{Key: "K", Value: "top-secret", Note: str("n")}, {Key: "L", Value: "v"}}},
		{Kind: KindNotes, Folder: "api", Ref: NotesRef, Notes: []BundleNote{{Sha: "abcd", Body: "private body"}}},
	})
	out, _ := json.Marshal(newShare(nil, nil).OpenV2(path, sharePW))
	want := `{"version":2,"sections":[` +
		`{"kind":"repo","project":"api","folder":"api","files":[{"path":"run.sh","size":3,"executable":true}]},` +
		`{"kind":"vault","entries":[{"key":"K","note":"n"},{"key":"L"}]},` +
		`{"kind":"notes","folder":"api","ref":"refs/notes/commits","noteCount":1}]}`
	if string(out) != want {
		t.Errorf("got  %s\nwant %s", out, want)
	}
	if strings.Contains(string(out), "top-secret") || strings.Contains(string(out), "private body") {
		t.Error("secret reached the renderer shape")
	}
}

func TestPreviewRepoV2(t *testing.T) {
	repo := shareRepo(t)
	path := writeSections(t, "p", []Section{
		{Kind: KindVault, Entries: []BundleVaultEntry{{Key: "K", Value: "v"}}},
		{Kind: KindRepo, Project: "a", Folder: "a", Files: []BundleFile{{Path: ".env", Content: []byte("x")}, {Path: "../x", Content: nil}}},
	})
	s := newShare(nil, nil)
	res := s.PreviewRepoV2(path, sharePW, 1, repo)
	out, _ := json.Marshal(res)
	if string(out) != `{"files":[{"path":".env","size":1,"exists":true,"safe":true},{"path":"../x","size":0,"exists":false,"safe":false}]}` {
		t.Errorf("got %s", out)
	}
	for _, idx := range []float64{0, 2, -1, 0.5} {
		if res := s.PreviewRepoV2(path, sharePW, idx, repo); res.Error != ErrInvalid {
			t.Errorf("index %v = %+v", idx, res)
		}
	}
}

func TestApplyV2RepoAndVault(t *testing.T) {
	repo := shareRepo(t)
	v := &fakeVault{}
	s := newShare(nil, v)
	path := writeSections(t, "p", []Section{
		{Kind: KindRepo, Project: "a", Folder: "a", Files: []BundleFile{
			{Path: "bin/run", Content: []byte("#!/bin/sh\n"), Executable: true},
			{Path: "plain.txt", Content: []byte("p")},
			{Path: "../escape", Content: []byte("x")},
		}},
		{Kind: KindVault, Entries: []BundleVaultEntry{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}}},
	})
	res, err := s.ApplyV2(context.Background(), path, sharePW, []ApplyPlan{
		{Kind: KindRepo, SectionIndex: 0, TargetRepoPath: repo, Paths: []string{"bin/run", "plain.txt", "../escape"}},
		{Kind: KindVault, SectionIndex: 1, Keys: []string{"B"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(res)
	if string(out) != `{"filesWritten":2,"secretsWritten":1,"notesWritten":0,"notesSkipped":0}` {
		t.Errorf("got %s", out)
	}
	if st, _ := os.Stat(filepath.Join(repo, "bin", "run")); st.Mode().Perm() != 0o755 {
		t.Errorf("exec mode = %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Join(repo, "plain.txt")); st.Mode().Perm() != 0o644 {
		t.Errorf("plain mode = %v", st.Mode().Perm())
	}
	if len(v.imported) != 1 || v.imported[0].Key != "B" {
		t.Errorf("imported = %+v", v.imported)
	}
	// Kind mismatches and bad indexes are invalid; a bad password is reported.
	for _, plan := range [][]ApplyPlan{
		{{Kind: KindVault, SectionIndex: 0}},
		{{Kind: KindRepo, SectionIndex: 1}},
		{{Kind: KindNotes, SectionIndex: 0}},
		{{Kind: KindRepo, SectionIndex: 5}},
	} {
		if res, _ := s.ApplyV2(context.Background(), path, sharePW, plan); res.Error != ErrInvalid {
			t.Errorf("%+v → %+v", plan, res)
		}
	}
	if res, _ := s.ApplyV2(context.Background(), path, "wrong", nil); res.Error != ErrBadPassword {
		t.Errorf("wrong password → %+v", res)
	}
}

// makeRepoPair is a tiny repo with two commits, and a clone of it (shared history).
func makeRepoPair(t *testing.T) (src, dst string, shas []string) {
	t.Helper()
	testutil.IsolateGitEnv(t)
	base := t.TempDir()
	src = filepath.Join(base, "src")
	testutil.Git(t, base, "init", "-q", src)
	writeFile(t, filepath.Join(src, "a.txt"), "one\n")
	testutil.Git(t, src, "add", ".")
	testutil.Git(t, src, "commit", "-qm", "one")
	writeFile(t, filepath.Join(src, "a.txt"), "two\n")
	testutil.Git(t, src, "commit", "-qam", "two")
	revs := strings.Split(testutil.Git(t, src, "rev-list", "HEAD"), "\n")
	for i := len(revs) - 1; i >= 0; i-- {
		shas = append(shas, revs[i])
	}
	dst = filepath.Join(base, "dst")
	testutil.Git(t, base, "clone", "-q", src, dst)
	return src, dst, shas
}

func TestReadRepoNotes(t *testing.T) {
	src, _, shas := makeRepoPair(t)
	testutil.Git(t, src, "notes", "add", "-m", "first note", shas[0])
	testutil.Git(t, src, "notes", "add", "-m", "second note", shas[1])
	notes := ReadRepoNotes(context.Background(), src)
	if len(notes) != 2 {
		t.Fatalf("notes = %+v", notes)
	}
	for _, n := range notes {
		if n.Sha == shas[0] && n.Body != "first note" {
			t.Errorf("body = %q", n.Body)
		}
	}
	if got := ReadRepoNotes(context.Background(), t.TempDir()); len(got) != 0 {
		t.Errorf("non-repo = %+v", got)
	}
}

func TestPreviewNotesNewSameDifferent(t *testing.T) {
	src, dst, shas := makeRepoPair(t)
	testutil.Git(t, src, "notes", "add", "-m", "shared", shas[0])
	testutil.Git(t, src, "notes", "add", "-m", "fresh", shas[1])
	// Target already has one identical note and no second note.
	testutil.Git(t, dst, "notes", "add", "-m", "shared", shas[0])
	notes := ReadRepoNotes(context.Background(), src)
	notes = append(notes, BundleNote{Sha: "-p", Body: "option-shaped"}, BundleNote{Sha: strings.Repeat("f", 40), Body: "x"})
	path := writeSections(t, "src", []Section{{Kind: KindNotes, Folder: "src", Ref: NotesRef, Notes: notes}})
	res := newShare(nil, nil).PreviewNotesV2(context.Background(), path, sharePW, 0, dst)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	bySha := map[string]NotePreviewEntry{}
	for _, n := range res.Notes {
		bySha[n.Sha] = n
	}
	if bySha[shas[0]].State != "same" || bySha[shas[1]].State != "new" || !bySha[shas[1]].CommitExists {
		t.Errorf("notes = %+v", res.Notes)
	}
	if e := bySha["-p"]; e.CommitExists || e.State != "new" {
		t.Errorf("option-shaped sha = %+v", e)
	}
	if e := bySha[strings.Repeat("f", 40)]; e.CommitExists || e.State != "new" {
		t.Errorf("missing commit = %+v", e)
	}
	// Different note.
	testutil.Git(t, dst, "notes", "add", "-f", "-m", "mine", shas[0])
	res = newShare(nil, nil).PreviewNotesV2(context.Background(), path, sharePW, 0, dst)
	if res.Notes[0].Sha == shas[0] && res.Notes[0].State != "different" {
		t.Errorf("notes = %+v", res.Notes)
	}
	if res := newShare(nil, nil).PreviewNotesV2(context.Background(), path, sharePW, 1, dst); res.Error != ErrInvalid {
		t.Errorf("bad index = %+v", res)
	}
}

func TestApplyNotesHonoursOverwrite(t *testing.T) {
	src, dst, shas := makeRepoPair(t)
	testutil.Git(t, src, "notes", "add", "-m", "theirs", shas[0])
	testutil.Git(t, src, "notes", "add", "-m", "brand new", shas[1])
	testutil.Git(t, dst, "notes", "add", "-m", "mine, different", shas[0])
	notes := ReadRepoNotes(context.Background(), src)
	notes = append(notes, BundleNote{Sha: strings.Repeat("f", 40), Body: "no such commit"}, BundleNote{Sha: "--force", Body: "never run"})
	path := writeSections(t, "src", []Section{{Kind: KindNotes, Folder: "src", Ref: NotesRef, Notes: notes}})
	s := newShare(nil, nil)

	// Without overwrite: the differing note survives untouched.
	r1, err := s.ApplyV2(context.Background(), path, sharePW, []ApplyPlan{{Kind: KindNotes, SectionIndex: 0, TargetRepoPath: dst}})
	if err != nil || r1.Error != "" {
		t.Fatalf("r1 = %+v, %v", r1, err)
	}
	if r1.NotesWritten != 1 || r1.NotesSkipped != 2 { // brand-new written; differing + missing commit skipped
		t.Errorf("r1 = %+v", r1)
	}
	if got := testutil.Git(t, dst, "notes", "show", shas[0]); got != "mine, different" {
		t.Errorf("note 0 = %q", got)
	}
	if got := testutil.Git(t, dst, "notes", "show", shas[1]); got != "brand new" {
		t.Errorf("note 1 = %q", got)
	}

	// With overwrite: the differing note is replaced.
	r2, err := s.ApplyV2(context.Background(), path, sharePW, []ApplyPlan{{Kind: KindNotes, SectionIndex: 0, TargetRepoPath: dst, Overwrite: true}})
	if err != nil || r2.Error != "" {
		t.Fatalf("r2 = %+v, %v", r2, err)
	}
	if got := testutil.Git(t, dst, "notes", "show", shas[0]); got != "theirs" {
		t.Errorf("note 0 = %q", got)
	}
}

func TestResolveInside(t *testing.T) {
	root := t.TempDir()
	if full, ok := resolveInside(root, "a/b"); !ok || full != filepath.Join(root, "a", "b") {
		t.Errorf("a/b = %q %v", full, ok)
	}
	for _, rel := range []string{"../a", "/abs", "a/../../b", ""} {
		if _, ok := resolveInside(root, rel); ok {
			t.Errorf("%q should be refused", rel)
		}
	}
}

func TestResultJSONShapes(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{ExportResult{Path: "/x"}, `{"path":"/x"}`},
		{ExportResult{Canceled: true}, `{"canceled":true}`},
		{ExportResult{Error: ErrWriteFailed}, `{"error":"write-failed"}`},
		{PreviewResult{}, `{"files":[]}`},
		{WrittenResult{}, `{"written":[]}`},
		{WrittenResult{Error: ErrInvalid}, `{"error":"invalid"}`},
		{NotesPreviewResult{}, `{"notes":[]}`},
		{ApplyResult{Error: ErrInvalid}, `{"error":"invalid"}`},
		{OpenedBundle{Version: 2}, `{"version":2,"sections":[]}`},
		{(*PickResult)(nil), `null`},
		{PickResult{Error: ErrInvalid}, `{"error":"invalid"}`},
	} {
		out, err := json.Marshal(c.v)
		if err != nil || string(out) != c.want {
			t.Errorf("%T: %s (%v), want %s", c.v, out, err, c.want)
		}
	}
}
