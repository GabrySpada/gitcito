package shared

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func readVectors(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// vectorConfig maps the RepoConfig of a vector to the slice the check reads.
type vectorConfig struct {
	Commit *struct {
		Scopes           []string `json:"scopes"`
		Trailers         []string `json:"trailers"`
		TicketFromBranch bool     `json:"ticketFromBranch"`
	} `json:"commit"`
}

func (v *vectorConfig) toCheck() *CommitCheckConfig {
	if v == nil {
		return nil
	}
	c := &CommitCheckConfig{}
	if v.Commit != nil {
		c.Scopes, c.Trailers, c.TicketFromBranch = v.Commit.Scopes, v.Commit.Trailers, v.Commit.TicketFromBranch
	}
	return c
}

// Replays testdata/vectors/commitCheck.json (test/port/cli.test.ts).
func TestCommitCheckVectors(t *testing.T) {
	var v struct {
		Check []struct {
			Input struct {
				Raw    string        `json:"raw"`
				Config *vectorConfig `json:"config"`
				Branch string        `json:"branch"`
			} `json:"input"`
			Out []CommitCheckIssue `json:"out"`
		} `json:"check"`
		Strip []struct {
			Input string `json:"input"`
			Out   string `json:"out"`
		} `json:"strip"`
	}
	readVectors(t, "commitCheck.json", &v)
	if len(v.Check) == 0 || len(v.Strip) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.Check {
		got := CheckCommitMessage(c.Input.Raw, CommitCheckOptions{Config: c.Input.Config.toCheck(), Branch: c.Input.Branch})
		if !reflect.DeepEqual(got, c.Out) {
			t.Errorf("%q: got %+v, want %+v", c.Input.Raw, got, c.Out)
		}
	}
	for _, c := range v.Strip {
		if got := StripCommitComments(c.Input); got != c.Out {
			t.Errorf("strip %q: got %q, want %q", c.Input, got, c.Out)
		}
	}
}

// Replays testdata/vectors/cliRepos.json (test/port/cli.test.ts).
func TestCliReposVectors(t *testing.T) {
	var v []struct {
		Settings any          `json:"settings"`
		Repos    []KnownRepo  `json:"repos"`
		Matches  [][]bool     `json:"matches"`
		Resolved []*KnownRepo `json:"resolved"`
	}
	readVectors(t, "cliRepos.json", &v)
	needles := []string{"API", "api", "we", "nothing", "/a/", "OLD", "", "slash"}
	for _, c := range v {
		repos := KnownRepos(c.Settings)
		if !reflect.DeepEqual(repos, c.Repos) {
			t.Errorf("settings %v: got %+v, want %+v", c.Settings, repos, c.Repos)
			continue
		}
		for i, n := range needles {
			for j, r := range repos {
				if MatchesRepo(r, n) != c.Matches[i][j] {
					t.Errorf("MatchesRepo(%+v, %q) != %v", r, n, c.Matches[i][j])
				}
			}
			if got := ResolveRepo(repos, n); !reflect.DeepEqual(got, c.Resolved[i]) {
				t.Errorf("ResolveRepo(%q) = %+v, want %+v", n, got, c.Resolved[i])
			}
		}
	}
}

// ─── Ports of test/cli.test.ts ──────────────────────────────────────────────

// parseCliOpenArgs
func TestParseCliOpenArgsCases(t *testing.T) {
	t.Run("returns null without --open", func(t *testing.T) {
		if ParseCliOpenArgs([]string{"/Applications/Gitcito.app", "--name=x"}) != nil {
			t.Fatal("want nil")
		}
	})
	t.Run("reads every flag the shim can pass", func(t *testing.T) {
		got := ParseCliOpenArgs([]string{"--open=/repo", "--name=API", "--group=Work", "--view=blame", "--arg=src/x.ts", "--line=42"})
		want := &CliOpenPayload{Path: "/repo", Name: "API", Group: "Work", View: "blame", Arg: "src/x.ts", Line: 42}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})
	// Chromium hoisted switches ahead of positionals; order must not matter.
	t.Run("is order-independent", func(t *testing.T) {
		got := ParseCliOpenArgs([]string{"--view=graph", "--open=/repo", "/some/positional"})
		if got == nil || got.Path != "/repo" || got.View != "graph" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("drops a view this build does not know instead of failing the open", func(t *testing.T) {
		got := ParseCliOpenArgs([]string{"--open=/repo", "--view=teleport"})
		if got == nil || got.Path != "/repo" || got.View != "" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("ignores a line number that is not one", func(t *testing.T) {
		for _, l := range []string{"--line=abc", "--line=-3"} {
			if got := ParseCliOpenArgs([]string{"--open=/r", l}); got.Line != 0 {
				t.Fatalf("%s: got %d", l, got.Line)
			}
		}
	})
	t.Run("carries an edit request and its sentinel", func(t *testing.T) {
		got := ParseCliOpenArgs([]string{"--open=/repo", "--edit=/repo/.git/COMMIT_EDITMSG", "--wait=/tmp/gitcito-wait.aB"})
		if got.Edit != "/repo/.git/COMMIT_EDITMSG" || got.Wait != "/tmp/gitcito-wait.aB" {
			t.Fatalf("got %+v", got)
		}
	})
}

// cliViewAction › agrees with isCliView on the vocabulary
func TestIsCliViewVocabulary(t *testing.T) {
	if !IsCliView("graph") || IsCliView("nope") {
		t.Fatal("IsCliView disagrees")
	}
}

func knownReposSettings() any {
	var v any
	_ = json.Unmarshal([]byte(`{
	  "tabs": [
	    {"kind": "repo", "repos": [{"path": "/a/api", "name": "API"}]},
	    {"kind": "group", "name": "Work", "repos": [{"path": "/a/web", "name": "Web"}, {"path": "/a/api", "name": "API"}]},
	    {"kind": "page"}
	  ],
	  "recentRepos": [{"path": "/a/old", "name": "Old"}, {"path": "/a/api"}]
	}`), &v)
	return v
}

// knownRepos
func TestKnownRepos(t *testing.T) {
	settings := knownReposSettings()
	t.Run("collects tabs then recents, without duplicates", func(t *testing.T) {
		var got []string
		for _, r := range KnownRepos(settings) {
			got = append(got, r.Path)
		}
		if !slices.Equal(got, []string{"/a/api", "/a/web", "/a/old"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("labels only the ones inside a group", func(t *testing.T) {
		by := map[string]KnownRepo{}
		for _, r := range KnownRepos(settings) {
			by[r.Path] = r
		}
		// First writer wins, so the standalone tab keeps this one ungrouped.
		if by["/a/web"].Group != "Work" || by["/a/api"].Group != "" {
			t.Fatalf("got %+v", by)
		}
	})
	t.Run("survives a settings file it does not recognise", func(t *testing.T) {
		if got := KnownRepos(nil); len(got) != 0 || got == nil {
			t.Fatalf("nil: %v", got)
		}
		var junk, one any
		_ = json.Unmarshal([]byte(`{"tabs": "nope", "recentRepos": 7}`), &junk)
		_ = json.Unmarshal([]byte(`{"tabs": [{"repos": [{"path": "/x"}]}]}`), &one)
		if got := KnownRepos(junk); len(got) != 0 {
			t.Fatalf("junk: %v", got)
		}
		if got := KnownRepos(one); !reflect.DeepEqual(got, []KnownRepo{{Path: "/x", Name: "x"}}) {
			t.Fatalf("one: %v", got)
		}
	})
	t.Run("prefers an exact name over a substring", func(t *testing.T) {
		repos := KnownRepos(settings)
		if r := ResolveRepo(repos, "API"); r == nil || r.Path != "/a/api" {
			t.Fatalf("API: %v", r)
		}
		if r := ResolveRepo(repos, "we"); r == nil || r.Path != "/a/web" {
			t.Fatalf("we: %v", r)
		}
		if ResolveRepo(repos, "nothing") != nil {
			t.Fatal("nothing matched")
		}
		if !MatchesRepo(KnownRepo{Path: "/a/api", Name: "API"}, "/a/") {
			t.Fatal("path match")
		}
	})
}

func codes(raw string, opts CommitCheckOptions) []string {
	out := []string{}
	for _, i := range CheckCommitMessage(raw, opts) {
		out = append(out, i.Code)
	}
	return out
}

// checkCommitMessage
func TestCheckCommitMessage(t *testing.T) {
	none := CommitCheckOptions{}
	scoped := &CommitCheckConfig{Scopes: []string{"api", "ui"}, Trailers: []string{"Refs: {ticket}"}}
	ticket := &CommitCheckConfig{TicketFromBranch: true}
	t.Run("strips what git strips", func(t *testing.T) {
		if got := StripCommitComments("feat: x\n# a comment\n\nbody"); got != "feat: x\n\nbody" {
			t.Fatalf("got %q", got)
		}
		if got := StripCommitComments("subject\n# ------------------------ >8 ------------------------\ndiff"); got != "subject" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("fails an empty message", func(t *testing.T) {
		if got := codes("# only comments\n", none); !slices.Equal(got, []string{"empty"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("accepts a well-formed message", func(t *testing.T) {
		if got := codes("feat: add range-diff\n\nA body that is comfortably short.", none); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("requires a blank line under the subject", func(t *testing.T) {
		if !slices.Contains(codes("feat: x\nbody right away", none), "no-blank-line") {
			t.Fatal("missing no-blank-line")
		}
	})
	t.Run("warns on style and errors on length", func(t *testing.T) {
		if got := codes("feat: "+strings.Repeat("x", 60)+".", none); !slices.Equal(got, []string{"subject-long", "trailing-period"}) {
			t.Fatalf("got %v", got)
		}
		if got := codes("feat: "+strings.Repeat("x", 80), none); !slices.Equal(got, []string{"subject-too-long"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("leaves a long URL in the body alone", func(t *testing.T) {
		if got := codes("feat: x\n\nhttps://example.com/"+strings.Repeat("y", 80), none); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("turns conventional commits into a rule once the repo declares scopes", func(t *testing.T) {
		o := CommitCheckOptions{Config: scoped}
		if !slices.Contains(codes("just some text", o), "not-conventional") ||
			!slices.Contains(codes("feat(db): x", o), "unknown-scope") ||
			slices.Contains(codes("feat(api): x", o), "unknown-scope") {
			t.Fatal("scope rules")
		}
	})
	t.Run("notices a declared trailer that is missing", func(t *testing.T) {
		o := CommitCheckOptions{Config: scoped}
		if !slices.Contains(codes("feat(api): x", o), "missing-trailer") ||
			slices.Contains(codes("feat(api): x\n\nRefs: ABC-1", o), "missing-trailer") {
			t.Fatal("trailer rules")
		}
	})
	t.Run("only mentions a ticket when the branch has one and the repo asked", func(t *testing.T) {
		if !slices.Contains(codes("fix: thing", CommitCheckOptions{Config: ticket, Branch: "feature/ABC-12-thing"}), "missing-ticket") ||
			slices.Contains(codes("fix: ABC-12 thing", CommitCheckOptions{Config: ticket, Branch: "feature/ABC-12-thing"}), "missing-ticket") ||
			slices.Contains(codes("fix: thing", CommitCheckOptions{Config: ticket, Branch: "feature/no-ticket"}), "missing-ticket") {
			t.Fatal("ticket rules")
		}
	})
}
