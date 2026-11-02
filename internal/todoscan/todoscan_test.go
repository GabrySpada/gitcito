package todoscan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

func canon(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func sameJSON(t *testing.T, label string, got, want any) {
	t.Helper()
	if g, w := canon(t, got), canon(t, want); !reflect.DeepEqual(g, w) {
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		t.Errorf("%s:\n got %s\nwant %s", label, gb, wb)
	}
}

// Replays testdata/vectors/todoScan.json, written from src/main/todoScan.ts by
// test/port/processes.test.ts.
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "todoScan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Tags          []string `json:"tags"`
		GrepPattern   string   `json:"grepPattern"`
		ParseTodoLine []struct {
			File string          `json:"file"`
			Line int             `json:"line"`
			Text string          `json:"text"`
			Out  json.RawMessage `json:"out"`
		} `json:"parseTodoLine"`
		ParseGrepOutput []struct {
			Input string          `json:"input"`
			Out   json.RawMessage `json:"out"`
		} `json:"parseGrepOutput"`
		SortTodos []struct {
			Input []CodeTodo      `json:"input"`
			Out   json.RawMessage `json:"out"`
		} `json:"sortTodos"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Tags, TodoTags) {
		t.Errorf("tags = %v, want %v", TodoTags, v.Tags)
	}
	if GrepPattern() != v.GrepPattern {
		t.Errorf("grepPattern = %q, want %q", GrepPattern(), v.GrepPattern)
	}
	if len(v.ParseTodoLine) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.ParseTodoLine {
		got := ParseTodoLine(c.File, c.Line, c.Text)
		if got == nil {
			sameJSON(t, c.Text, nil, c.Out)
		} else {
			sameJSON(t, c.Text, got, c.Out)
		}
	}
	for _, c := range v.ParseGrepOutput {
		sameJSON(t, "parseGrepOutput("+c.Input+")", ParseGrepOutput(c.Input), c.Out)
	}
	for _, c := range v.SortTodos {
		sameJSON(t, "sortTodos", SortTodos(c.Input), c.Out)
	}
}

// ─── test/todoScan.test.ts ──────────────────────────────────────────────────

func TestParseTodoLine(t *testing.T) {
	t.Run("reads a tag whatever the comment leader and the case", func(t *testing.T) {
		for _, line := range []string{"// TODO: ship it", "//TODO ship it", "// todo ship it", "# TODO: ship it", "  * todo: ship it"} {
			td := ParseTodoLine("a.ts", 3, line)
			if td == nil || td.Tag != "TODO" || td.Message != "ship it" || td.Owner != "" {
				t.Errorf("%q → %+v", line, td)
			}
		}
	})
	t.Run("unites the owner whether the parens hug the tag or not", func(t *testing.T) {
		for _, line := range []string{"// TODO(cgm): ship it", "// TODO (cgm) ship it", "// todo[CGM] ship it", "// TODO: @cgm ship it"} {
			td := ParseTodoLine("a.ts", 1, line)
			if td == nil || td.Owner != "cgm" || td.Message != "ship it" {
				t.Errorf("%q → %+v", line, td)
			}
		}
	})
	t.Run("keeps a parenthesised sentence as the message, not as an owner", func(t *testing.T) {
		td := ParseTodoLine("a.ts", 1, "// TODO (see the issue) revisit")
		if td == nil || td.Owner != "" || td.Message != "(see the issue) revisit" {
			t.Errorf("got %+v", td)
		}
	})
	t.Run("records the column of the tag so the file opens on the marker", func(t *testing.T) {
		td := ParseTodoLine("a.ts", 9, "const x = 1 // FIXME later")
		if td == nil || td.Tag != "FIXME" || td.Col != len("const x = 1 // ")+1 {
			t.Errorf("got %+v", td)
		}
	})
	t.Run("ignores a tag that is not in a comment", func(t *testing.T) {
		for _, c := range [][2]string{
			{"a.ts", "const todos = ['todo']"},
			{"a.ts", "function reviewNote() {}"},
			// The one that started it: a Python variable named `todo`, indented,
			// is code. Without a comment leader in front there is no marker.
			{"a.py", "        todo = [l for l in lines if UNCHECKED.match(l)]"},
			{"NOTES.md", "TODO: write the docs"},
		} {
			if td := ParseTodoLine(c[0], 1, c[1]); td != nil {
				t.Errorf("%q → %+v", c[1], td)
			}
		}
	})
	t.Run("drops the closer of a block or markup comment", func(t *testing.T) {
		if td := ParseTodoLine("a.ts", 1, "/* TODO: wire it up */"); td == nil || td.Message != "wire it up" {
			t.Errorf("got %+v", td)
		}
		if td := ParseTodoLine("a.html", 1, "<!-- FIXME: escape this -->"); td == nil || td.Message != "escape this" {
			t.Errorf("got %+v", td)
		}
	})
	t.Run("never splits FIXME into FIX plus a message", func(t *testing.T) {
		if td := ParseTodoLine("a.ts", 1, "// FIXME"); td == nil || td.Tag != "FIXME" || td.Message != "" {
			t.Errorf("got %+v", td)
		}
	})
	t.Run("knows every tag it advertises", func(t *testing.T) {
		for _, tag := range TodoTags {
			if td := ParseTodoLine("a.ts", 1, "// "+tag+": x"); td == nil || td.Tag != tag {
				t.Errorf("%s → %+v", tag, td)
			}
		}
	})
}

func TestParseGrepOutput(t *testing.T) {
	t.Run("parses git grep lines and skips dependency trees", func(t *testing.T) {
		out := ParseGrepOutput(strings.Join([]string{"src/a.ts:12:// TODO(cgm): ship", "node_modules/x/b.js:1:// TODO: theirs", "src/b.ts:4:const x = 1"}, "\n"))
		if len(out) != 1 || out[0].File != "src/a.ts" || out[0].Line != 12 || out[0].Owner != "cgm" {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("survives a path that holds a colon", func(t *testing.T) {
		out := ParseGrepOutput("src/we:ird.ts:7:// FIXME: odd name")
		if len(out) != 1 || out[0].File != "src/we:ird.ts" || out[0].Line != 7 || out[0].Tag != "FIXME" {
			t.Errorf("got %+v", out)
		}
	})
}

func TestSortTodos(t *testing.T) {
	// sorts by file then line
	td := func(file string, line int) CodeTodo { return CodeTodo{File: file, Line: line, Col: 1, Tag: "TODO"} }
	got := SortTodos([]CodeTodo{td("b.ts", 1), td("a.ts", 9), td("a.ts", 2)})
	var keys []string
	for _, x := range got {
		keys = append(keys, x.File+":"+string(rune('0'+x.Line)))
	}
	if !reflect.DeepEqual(keys, []string{"a.ts:2", "a.ts:9", "b.ts:1"}) {
		t.Errorf("got %v", keys)
	}
}

// The scan is `git grep`, so the only honest test runs one against a real repo.
func TestScanTodos(t *testing.T) {
	// finds markers in tracked and untracked files and skips ignored ones
	testutil.IsolateGitEnv(t)
	dir := t.TempDir()
	testutil.Git(t, dir, "init", "-q")
	testutil.WriteFile(t, dir, ".gitignore", "build/\n")
	testutil.WriteFile(t, dir, "src/tracked.ts", "const a = 1 // TODO(cgm): ship this\n// fixme lowercase counts\n")
	testutil.Git(t, dir, "add", "-A")
	testutil.Git(t, dir, "-c", "user.email=a@b.c", "-c", "user.name=t", "commit", "-qm", "init")
	testutil.WriteFile(t, dir, "src/untracked.py", "# TODO: not committed yet\n")
	testutil.WriteFile(t, dir, "build/out.js", "// TODO: generated, not mine\n")

	res := NewScanner().Scan(context.Background(), dir)
	files := map[string]bool{}
	var owned, fixme *CodeTodo
	for i, x := range res.Todos {
		files[x.File] = true
		if x.Owner == "cgm" {
			owned = &res.Todos[i]
		}
		if x.Tag == "FIXME" {
			fixme = &res.Todos[i]
		}
	}
	if !files["src/tracked.ts"] || !files["src/untracked.py"] || files["build/out.js"] {
		t.Errorf("files = %v", files)
	}
	if owned == nil || owned.Message != "ship this" || fixme == nil || fixme.Message != "lowercase counts" || res.Truncated {
		t.Errorf("result = %+v", res)
	}
}

func TestScanOutsideARepoIsEmpty(t *testing.T) {
	testutil.IsolateGitEnv(t)
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	testutil.WriteFile(t, dir, "a.ts", "// TODO: x\n")
	s := NewScanner()
	res := s.Scan(context.Background(), dir)
	if len(res.Todos) != 0 || res.Todos == nil {
		t.Errorf("result = %+v", res)
	}
	s.Cancel(dir) // nothing running: a no-op
}
