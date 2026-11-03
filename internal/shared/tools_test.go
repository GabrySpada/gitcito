package shared

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Ports of test/pureLogic.test.ts: "external editor argv", "ssh key parsing"
// and "external diff tool listing".

func TestExternalEditorArgv(t *testing.T) {
	vscode := EditorSetting{ID: "vscode", Name: "Visual Studio Code", Command: "/usr/local/bin/code", Source: "cli"}
	eq := func(t *testing.T, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}

	t.Run("substitutes path, line and column", func(t *testing.T) {
		eq(t, EditorArgs("-g {path}:{line}:{col}", EditorVars{Path: "/r/a.ts", Line: 12, Col: 3}), []string{"-g", "/r/a.ts:12:3"})
	})
	t.Run("defaults the column to 1 when a line is given without one", func(t *testing.T) {
		eq(t, EditorArgs("{path}:{line}:{col}", EditorVars{Path: "/r/a.ts", Line: 9}), []string{"/r/a.ts:9:1"})
	})
	t.Run("drops the line suffix entirely when there is no line", func(t *testing.T) {
		eq(t, EditorArgs("{path}:{line}:{col}", EditorVars{Path: "/r/a.ts"}), []string{"/r/a.ts"})
	})
	t.Run("drops a flag whose placeholder resolved to nothing", func(t *testing.T) {
		// `--line` left behind would swallow the path as its value.
		eq(t, EditorArgs("--line {line} --column {col} {path}", EditorVars{Path: "/r/a.ts"}), []string{"/r/a.ts"})
		eq(t, EditorArgs("--line {line} --column {col} {path}", EditorVars{Path: "/r/a.ts", Line: 4}),
			[]string{"--line", "4", "--column", "1", "/r/a.ts"})
	})
	t.Run("keeps a quoted path as a single argument", func(t *testing.T) {
		eq(t, EditorArgs(`"{path}"`, EditorVars{Path: "/r/my dir/a.ts"}), []string{"/r/my dir/a.ts"})
		eq(t, EditorArgs("{path}", EditorVars{Path: "/r/my dir/a.ts"}), []string{"/r/my dir/a.ts"})
	})
	t.Run("builds a launch plan from the preset for the chosen editor", func(t *testing.T) {
		eq(t, EditorLaunch(vscode, EditorTarget{Path: "/r/a.ts", Line: 7}),
			&EditorLaunchPlan{Command: "/usr/local/bin/code", Args: []string{"-g", "/r/a.ts:7:1"}, Source: "cli"})
	})
	t.Run("uses the folder template for a directory", func(t *testing.T) {
		eq(t, EditorLaunch(vscode, EditorTarget{Path: "/r", IsDir: true}).Args, []string{"/r"})
	})
	t.Run("ignores the line for an editor found as a macOS bundle", func(t *testing.T) {
		bundle := vscode
		bundle.Command, bundle.Source = "/Applications/Visual Studio Code.app", "app"
		eq(t, SupportsLine(bundle), false)
		eq(t, EditorLaunch(bundle, EditorTarget{Path: "/r/a.ts", Line: 7}).Args, []string{"-g", "/r/a.ts"})
	})
	t.Run("falls back to {path} when a custom command carries no template", func(t *testing.T) {
		custom := EditorSetting{ID: "custom", Name: "nano", Command: "/usr/bin/nano", Source: "cli"}
		eq(t, SupportsLine(custom), false)
		eq(t, EditorLaunch(custom, EditorTarget{Path: "/r/a.ts", Line: 7}).Args, []string{"/r/a.ts"})
	})
	t.Run("reports no plan at all when nothing is configured", func(t *testing.T) {
		if p := EditorLaunch(EditorSetting{ID: "custom", Source: "cli"}, EditorTarget{Path: "/r/a.ts"}); p != nil {
			t.Fatalf("got %#v, want nil", p)
		}
	})
}

func TestSshKeyParsing(t *testing.T) {
	t.Run("reads an ssh-keygen -l line, type and all", func(t *testing.T) {
		got := ParseKeygenLine("256 SHA256:abc123 you@example.com (ED25519)")
		want := &KeygenLine{Bits: 256, Fingerprint: "SHA256:abc123", Comment: "you@example.com", Type: "ed25519"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v", got)
		}
		if c := ParseKeygenLine("4096 SHA256:xyz old laptop key (RSA)").Comment; c != "old laptop key" {
			t.Fatalf("comment %q", c)
		}
		if c := ParseKeygenLine("256 SHA256:def no comment (ED25519)").Comment; c != "" {
			t.Fatalf("comment %q", c)
		}
	})
	t.Run("treats an unknown or missing algorithm as unknown rather than guessing", func(t *testing.T) {
		if ty := ParseKeygenLine("256 SHA256:abc you@example.com").Type; ty != "unknown" {
			t.Fatalf("type %q", ty)
		}
		if ty := ParseKeygenLine("521 SHA256:abc key (ECDSA-SK)").Type; ty != "ecdsa-sk" {
			t.Fatalf("type %q", ty)
		}
	})
	t.Run("rejects lines that are not key lines", func(t *testing.T) {
		// What ssh-add prints when there is nothing loaded, or no agent at all.
		for _, line := range []string{"The agent has no identities.", "Error connecting to agent: No such file or directory", ""} {
			if got := ParseKeygenLine(line); got != nil {
				t.Fatalf("%q parsed as %#v", line, got)
			}
		}
	})
	t.Run("collects only real fingerprints out of ssh-add output", func(t *testing.T) {
		out := "256 SHA256:aaa a@b (ED25519)\n4096 SHA256:bbb c@d (RSA)\nThe agent has no identities."
		if got := AgentFingerprints(out); !reflect.DeepEqual(got, []string{"SHA256:aaa", "SHA256:bbb"}) {
			t.Fatalf("got %v", got)
		}
		if got := AgentFingerprints("Could not open a connection to your authentication agent."); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("picks key files out of a ~/.ssh listing and leaves the config alone", func(t *testing.T) {
		entries := []string{"config", "known_hosts", "id_rsa", "id_rsa.pub", "id_ed25519", "id_ed25519.pub", "authorized_keys"}
		if got := PublicKeyFiles(entries); !reflect.DeepEqual(got, []string{"id_ed25519.pub", "id_rsa.pub"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("maps a public key back to its private half", func(t *testing.T) {
		if PrivateKeyName("id_ed25519.pub") != "id_ed25519" || PrivateKeyName("/home/me/.ssh/id_rsa.pub") != "/home/me/.ssh/id_rsa" {
			t.Fatal("wrong private name")
		}
	})
	t.Run("reads the verdict out of ssh -T, whose exit code lies", func(t *testing.T) {
		// GitHub authenticates you and then exits 1.
		cases := map[string]string{
			"Hi octocat! You've successfully authenticated, but GitHub does not provide shell access.": "ok",
			"Welcome to GitLab, @octocat!":                   "ok",
			"git@github.com: Permission denied (publickey).": "denied",
			"ssh: Could not resolve hostname github.com":     "unreachable",
			"something else entirely":                        "unknown",
		}
		for in, want := range cases {
			if got := ReadSshTest(in); got != want {
				t.Errorf("%q → %q, want %q", in, got, want)
			}
		}
	})
}

func TestExternalDiffToolListing(t *testing.T) {
	// Trimmed from a real `git difftool --tool-help`.
	help := strings.Join([]string{
		"'git difftool --tool=<tool>' may be set to one of the following:",
		"\t\taraxis           Use Araxis Merge (requires a graphical session)",
		"\t\topendiff         Use FileMerge (requires a graphical session)",
		"\t\tvimdiff          Use Vim",
		"",
		"The following tools are valid, but not currently available:",
		"\t\tbc               Use Beyond Compare (requires a graphical session)",
		"\t\tmeld             Use Meld (requires a graphical session)",
		"",
		"Some of the tools listed above only work in a windowed",
		"environment. If run in a terminal-only session, they will fail.",
	}, "\n")
	ids := func(tools []DiffToolInfo, keep func(DiffToolInfo) bool) []string {
		out := []string{}
		for _, t := range tools {
			if keep(t) {
				out = append(out, t.ID)
			}
		}
		return out
	}
	all := func(DiffToolInfo) bool { return true }
	find := func(id string) DiffToolInfo {
		for _, t := range ParseToolHelp(help) {
			if t.ID == id {
				return t
			}
		}
		return DiffToolInfo{}
	}

	t.Run("splits the two lists git prints", func(t *testing.T) {
		tools := ParseToolHelp(help)
		if got := ids(tools, func(t DiffToolInfo) bool { return t.Available }); !reflect.DeepEqual(got, []string{"araxis", "opendiff", "vimdiff"}) {
			t.Fatalf("available %v", got)
		}
		if got := ids(tools, func(t DiffToolInfo) bool { return !t.Available }); !reflect.DeepEqual(got, []string{"bc", "meld"}) {
			t.Fatalf("unavailable %v", got)
		}
	})
	t.Run("keeps git’s own description for each tool", func(t *testing.T) {
		if d := find("meld").Description; d != "Use Meld (requires a graphical session)" {
			t.Fatalf("meld %q", d)
		}
		if d := find("vimdiff").Description; d != "Use Vim" {
			t.Fatalf("vimdiff %q", d)
		}
	})
	t.Run("ignores the headings and the closing prose", func(t *testing.T) {
		got := ids(ParseToolHelp(help), all)
		idRe := regexp.MustCompile(`^[\w.+-]+$`)
		for _, id := range got {
			if id == "Some" || id == "The" || !idRe.MatchString(id) {
				t.Fatalf("unexpected id %q in %v", id, got)
			}
		}
	})
	t.Run("survives empty or unexpected output instead of inventing tools", func(t *testing.T) {
		if got := ParseToolHelp(""); len(got) != 0 || got == nil {
			t.Fatalf("empty → %#v", got)
		}
		if got := ParseToolHelp("fatal: not a git repository"); len(got) != 0 {
			t.Fatalf("fatal → %#v", got)
		}
	})
	t.Run("sorts installed tools first, then alphabetically", func(t *testing.T) {
		got := ids(SortTools(ParseToolHelp(help)), all)
		if !reflect.DeepEqual(got, []string{"araxis", "opendiff", "vimdiff", "bc", "meld"}) {
			t.Fatalf("got %v", got)
		}
	})
}
