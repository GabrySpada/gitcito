package fileactions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/GabrySpada/gitcito/internal/testutil"
)

var ctx = context.Background()

func set(paths ...string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func ctxFor(paths ...string) Context {
	return Context{EvidencePaths: set(paths...), CompletePaths: set(paths...), IgnoredPaths: set()}
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
	return fe
}

// ─── vectors ────────────────────────────────────────────────────────────────

type vecError struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
}

type scenario struct {
	Name     string                     `json:"name"`
	Files    map[string]json.RawMessage `json:"files"`
	Actions  []Action                   `json:"actions"`
	Evidence []string                   `json:"evidence"`
	Complete *[]string                  `json:"complete"`
	Ignored  []string                   `json:"ignored"`
	Apply    *struct {
		Mutate map[string]*string `json:"mutate"`
	} `json:"apply"`
	Prepared     json.RawMessage        `json:"prepared"`
	PrepareError *vecError              `json:"prepareError"`
	Applied      *struct{ Applied int } `json:"applied"`
	ApplyError   *vecError              `json:"applyError"`
	After        map[string]string      `json:"after"`
}

func asVecError(err error) *vecError {
	var fe *Error
	if errors.As(err, &fe) {
		p := fe.Paths
		if p == nil {
			p = []string{}
		}
		return &vecError{Code: fe.Code, Message: fe.Message, Paths: p}
	}
	return &vecError{Code: "thrown", Message: err.Error(), Paths: []string{}}
}

func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = read(t, dir, rel)
		return nil
	})
	return out
}

func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func TestVectors(t *testing.T) {
	testutil.IsolateGitEnv(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "fileActions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Generated []struct {
			In  string `json:"in"`
			Out bool   `json:"out"`
		} `json:"generated"`
		Secret []struct {
			In  string `json:"in"`
			Out bool   `json:"out"`
		} `json:"secret"`
		Scenarios []scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Scenarios) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.Generated {
		if got := IsGeneratedRepoPath(c.In); got != c.Out {
			t.Errorf("IsGeneratedRepoPath(%q) = %v, want %v", c.In, got, c.Out)
		}
	}
	for _, c := range v.Secret {
		if got := shared.IsSecretFile(c.In); got != c.Out {
			t.Errorf("IsSecretFile(%q) = %v, want %v", c.In, got, c.Out)
		}
	}
	for _, sc := range v.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			repo := t.TempDir()
			for rel, content := range sc.Files {
				var text string
				if json.Unmarshal(content, &text) != nil {
					var b struct{ B64 string }
					_ = json.Unmarshal(content, &b)
					data, _ := base64.StdEncoding.DecodeString(b.B64)
					text = string(data)
				}
				write(t, repo, rel, text)
			}
			complete := sc.Evidence
			if sc.Complete != nil {
				complete = *sc.Complete
			}
			c := Context{EvidencePaths: set(sc.Evidence...), CompletePaths: set(complete...), IgnoredPaths: set(sc.Ignored...)}
			prepared, err := Prepare(ctx, repo, sc.Actions, c)
			if sc.PrepareError != nil {
				if err == nil || !reflect.DeepEqual(asVecError(err), sc.PrepareError) {
					t.Fatalf("prepare err = %v, want %+v", err, sc.PrepareError)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			got, _ := json.Marshal(prepared)
			if canonical(t, got) != canonical(t, sc.Prepared) {
				t.Fatalf("prepared =\n%s\nwant\n%s", got, sc.Prepared)
			}
			if sc.Apply == nil {
				return
			}
			for rel, content := range sc.Apply.Mutate {
				if content == nil {
					_ = os.Remove(filepath.Join(repo, rel))
				} else {
					write(t, repo, rel, *content)
				}
			}
			// Round-trip through JSON, as the batch travels through the bridge.
			var back []PreparedRepoChatFileAction
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatal(err)
			}
			n, err := Apply(repo, back, set(sc.Ignored...), nil)
			if sc.ApplyError != nil {
				if err == nil || !reflect.DeepEqual(asVecError(err), sc.ApplyError) {
					t.Fatalf("apply err = %v, want %+v", err, sc.ApplyError)
				}
			} else if err != nil || sc.Applied == nil || n != sc.Applied.Applied {
				t.Fatalf("apply = %d, %v; want %+v", n, err, sc.Applied)
			}
			if after := tree(t, repo); !reflect.DeepEqual(after, sc.After) {
				t.Fatalf("tree = %v, want %v", after, sc.After)
			}
		})
	}
}

// ─── test/repoFileActions.test.ts ───────────────────────────────────────────

// repository file action preparation > prepares an exact edit with a local preview and hash
func TestPrepareExactEdit(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "LICENSE", "MIT License\nCopyright MyAppDesk\n")
	out, err := Prepare(ctx, repo, []Action{{Type: "edit_file", Path: "LICENSE", OldText: "MIT License",
		NewText: "Apache License, Version 2.0", Description: "Replace the license heading"}}, ctxFor("LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	a := out[0]
	if a.ExpectedHash == nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(*a.ExpectedHash) {
		t.Fatalf("hash = %v", a.ExpectedHash)
	}
	if a.ExpectedOccurrences == nil || *a.ExpectedOccurrences != 1 {
		t.Fatal("expectedOccurrences != 1")
	}
	for _, want := range []string{"--- a/LICENSE", "+++ b/LICENSE", "-MIT License", "+Apache License, Version 2.0"} {
		if !strings.Contains(a.Preview, want) {
			t.Errorf("preview lacks %q:\n%s", want, a.Preview)
		}
	}
}

// repository file action preparation > rejects ambiguous edits and incomplete whole-file replacement
func TestPrepareRejectsAmbiguousAndIncomplete(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.txt", "same\nsame\n")
	incomplete := Context{EvidencePaths: set("a.txt"), CompletePaths: set(), IgnoredPaths: set()}
	_, err := Prepare(ctx, repo, []Action{{Type: "edit_file", Path: "a.txt", OldText: "same", NewText: "next", Description: "Edit"}}, incomplete)
	wantCode(t, err, "ambiguous_edit")
	_, err = Prepare(ctx, repo, []Action{{Type: "write_file", Path: "a.txt", Content: "next", Mode: "replace", Description: "Replace"}}, incomplete)
	wantCode(t, err, "incomplete_evidence")
}

// repository file action preparation > prepares create, replace-all, delete, and sequential edits in memory
func TestPrepareSequentialInMemory(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.txt", "same same\n")
	write(t, repo, "gone.txt", "gone\n")
	out, err := Prepare(ctx, repo, []Action{
		{Type: "write_file", Path: "new.txt", Content: "created\n", Mode: "create", Description: "Create"},
		{Type: "edit_file", Path: "a.txt", OldText: "same", NewText: "next", ReplaceAll: true, Description: "Replace all"},
		{Type: "edit_file", Path: "a.txt", OldText: "next next", NewText: "done", Description: "Edit again"},
		{Type: "delete_file", Path: "gone.txt", Description: "Delete"},
	}, ctxFor("a.txt", "gone.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var nulls []bool
	for _, a := range out {
		nulls = append(nulls, a.ExpectedHash == nil)
	}
	if !reflect.DeepEqual(nulls, []bool{true, false, false, false}) {
		t.Fatalf("hash nulls = %v", nulls)
	}
	if *out[1].ExpectedOccurrences != 2 || !strings.Contains(out[2].Preview, "+done") ||
		!strings.Contains(out[0].Preview, "--- /dev/null") || !strings.Contains(out[3].Preview, "+++ /dev/null") {
		t.Fatalf("prepared = %+v", out)
	}
}

// repository file action preparation > rejects protected target …
func TestPrepareRejectsProtectedTargets(t *testing.T) {
	for _, c := range []struct{ path, code string }{
		{".git/config", "git_internal_path"},
		{"nested/.git/config", "git_internal_path"},
		{".env", "secret_file"},
		{"src/generated/client.ts", "generated_path"},
		{"../outside.txt", "unsafe_path"},
	} {
		t.Run(c.path, func(t *testing.T) {
			_, err := Prepare(ctx, t.TempDir(), []Action{{Type: "write_file", Path: c.path, Content: "x", Mode: "create", Description: "Create"}}, ctxFor())
			wantCode(t, err, c.code)
		})
	}
}

// repository file action preparation > rejects ignored targets and binary content
func TestPrepareRejectsIgnoredAndBinary(t *testing.T) {
	repo := t.TempDir()
	c := ctxFor()
	c.IgnoredPaths = set("dist/app.js")
	_, err := Prepare(ctx, repo, []Action{{Type: "write_file", Path: "dist/app.js", Content: "x", Mode: "create", Description: "Create"}}, c)
	wantCode(t, err, "ignored_path")
	_, err = Prepare(ctx, repo, []Action{{Type: "write_file", Path: "data.txt", Content: "a\x00b", Mode: "create", Description: "Create"}}, ctxFor())
	wantCode(t, err, "binary_file")
}

// repository file action preparation > enforces per-file and batch UTF-8 limits
func TestPrepareEnforcesLimits(t *testing.T) {
	repo := t.TempDir()
	oversized := strings.Repeat("é", MaxBytes/2+1)
	_, err := Prepare(ctx, repo, []Action{{Type: "write_file", Path: "large.txt", Content: oversized, Mode: "create", Description: "Create"}}, ctxFor())
	wantCode(t, err, "file_too_large")

	content := strings.Repeat("x", MaxBytes)
	var actions []Action
	for i := 0; i < BatchMaxBytes/MaxBytes+1; i++ {
		actions = append(actions, Action{Type: "write_file", Path: "file-" + string(rune('a'+i)) + ".txt", Content: content, Mode: "create", Description: "Create"})
	}
	_, err = Prepare(ctx, repo, actions, ctxFor())
	wantCode(t, err, "batch_too_large")
}

// repository file action preparation > rejects a symlink target and a symlinked parent
func TestPrepareRejectsSymlinks(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	write(t, outside, "target.txt", "outside\n")
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(repo, "link.txt")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(ctx, repo, []Action{{Type: "delete_file", Path: "link.txt", Description: "Delete"}}, ctxFor("link.txt"))
	wantCode(t, err, "symlink_path")
	_, err = Prepare(ctx, repo, []Action{{Type: "write_file", Path: "linked-dir/new.txt", Content: "x", Mode: "create", Description: "Create"}}, ctxFor())
	wantCode(t, err, "symlink_path")
}

// repository file action preparation > exposes stable preparation errors
func TestErrorShape(t *testing.T) {
	err := error(&Error{Code: "not_found", Message: "missing", Paths: []string{"a.txt"}})
	fe := wantCode(t, err, "not_found")
	if err.Error() != "missing" || !reflect.DeepEqual(fe.Paths, []string{"a.txt"}) {
		t.Fatalf("err = %+v", fe)
	}
}

func mixedBatch(t *testing.T, repo string) []PreparedRepoChatFileAction {
	t.Helper()
	out, err := Prepare(ctx, repo, []Action{
		{Type: "edit_file", Path: "a.txt", OldText: "old", NewText: "new", Description: "Edit"},
		{Type: "write_file", Path: "new.txt", Content: "created\n", Mode: "create", Description: "Create"},
		{Type: "delete_file", Path: "gone.txt", Description: "Delete"},
	}, ctxFor("a.txt", "gone.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// repository file action application > applies a prepared create, edit, and delete batch
func TestApplyMixedBatch(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.txt", "old\n")
	write(t, repo, "gone.txt", "remove\n")
	n, err := Apply(repo, mixedBatch(t, repo), set(), nil)
	if err != nil || n != 3 {
		t.Fatalf("apply = %d, %v", n, err)
	}
	if read(t, repo, "a.txt") != "new\n" || read(t, repo, "new.txt") != "created\n" {
		t.Fatal("contents wrong")
	}
	if _, err := os.Stat(filepath.Join(repo, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("gone.txt still there")
	}
}

// repository file action application > refuses a stale file before the first write
func TestApplyRefusesStale(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.txt", "old\n")
	prepared, err := Prepare(ctx, repo, []Action{{Type: "edit_file", Path: "a.txt", OldText: "old", NewText: "new", Description: "Edit"}}, ctxFor("a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, repo, "a.txt", "user changed it\n")
	_, err = Apply(repo, prepared, set(), nil)
	wantCode(t, err, "stale_file")
	if read(t, repo, "a.txt") != "user changed it\n" {
		t.Fatal("file touched")
	}
}

// failingFS injects rename failures.
type failingFS struct {
	FS
	fail func(from, to string) error
}

func (f failingFS) Rename(from, to string) error {
	if err := f.fail(from, to); err != nil {
		return err
	}
	return f.FS.Rename(from, to)
}

// repository file action application > rolls back edits, creations, and deletions when an install rename fails
func TestApplyRollsBack(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.txt", "old\n")
	write(t, repo, "gone.txt", "remove\n")
	prepared := mixedBatch(t, repo)
	io := failingFS{OS(), func(from, to string) error {
		if to == filepath.Join(repo, "new.txt") && strings.Contains(from, ".tmp-") {
			return errors.New("injected rename failure")
		}
		return nil
	}}
	_, err := Apply(repo, prepared, set(), io)
	wantCode(t, err, "unknown")
	if read(t, repo, "a.txt") != "old\n" || read(t, repo, "gone.txt") != "remove\n" {
		t.Fatal("not restored")
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("new.txt left behind")
	}
	entries, _ := os.ReadDir(repo)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".gitcito-") {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

// repository file action application > reports every path whose rollback restoration fails
func TestApplyReportsFailedRollback(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.txt", "old\n")
	write(t, repo, "gone.txt", "remove\n")
	prepared := mixedBatch(t, repo)
	io := failingFS{OS(), func(from, to string) error {
		if to == filepath.Join(repo, "new.txt") && strings.Contains(from, ".tmp-") {
			return errors.New("primary failure")
		}
		if to == filepath.Join(repo, "gone.txt") && strings.Contains(from, ".bak-") {
			return errors.New("restore failure")
		}
		return nil
	}}
	_, err := Apply(repo, prepared, set(), io)
	fe := wantCode(t, err, "rollback_failed")
	if !reflect.DeepEqual(fe.Paths, []string{"gone.txt"}) {
		t.Fatalf("paths = %v", fe.Paths)
	}
}

// repository file action application > rejects an unknown IPC action type without treating it as delete
func TestApplyRejectsUnknownType(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.txt", "old\n")
	prepared, err := Prepare(ctx, repo, []Action{{Type: "delete_file", Path: "a.txt", Description: "Delete"}}, ctxFor("a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	prepared[0].Type = "unknown_action"
	_, err = Apply(repo, prepared, set(), nil)
	wantCode(t, err, "unknown")
	if read(t, repo, "a.txt") != "old\n" {
		t.Fatal("file touched")
	}
}

// The created parent directories of a failed batch go again (empty ones only).
func TestApplyRemovesCreatedDirectoriesOnRollback(t *testing.T) {
	repo := t.TempDir()
	prepared, err := Prepare(ctx, repo, []Action{{Type: "write_file", Path: "deep/er/x.txt", Content: "x", Mode: "create", Description: "c"}}, ctxFor())
	if err != nil {
		t.Fatal(err)
	}
	io := failingFS{OS(), func(from, to string) error { return errors.New("no") }}
	_, err = Apply(repo, prepared, set(), io)
	wantCode(t, err, "unknown")
	entries, _ := os.ReadDir(repo)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 0 {
		t.Fatalf("left behind %v", names)
	}
}

// An edit that deletes text still says `newText: ""` on the wire.
func TestPreparedJSONKeepsEmptyVariantFields(t *testing.T) {
	b, _ := json.Marshal(PreparedRepoChatFileAction{Type: "edit_file", Path: "a", OldText: "x", Preview: "p"})
	if !strings.Contains(string(b), `"newText":""`) || !strings.Contains(string(b), `"expectedHash":null`) {
		t.Fatalf("json = %s", b)
	}
	b, _ = json.Marshal(PreparedRepoChatFileAction{Type: "write_file", Path: "a", Mode: "create"})
	if !strings.Contains(string(b), `"content":""`) || strings.Contains(string(b), "oldText") {
		t.Fatalf("json = %s", b)
	}
}
