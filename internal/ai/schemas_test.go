package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

type validatorCase struct {
	Input     json.RawMessage `json:"input"`
	Requested []string        `json:"requested"`
	Known     []string        `json:"known"`
	Already   []string        `json:"already"`
	Keys      string          `json:"keys"`
	Out       json.RawMessage `json:"out"`
}

// Replays testdata/vectors/aiSchemas.json: every schema byte-for-byte and
// every validator on a spread of inputs.
func TestSchemaVectors(t *testing.T) {
	var v struct {
		Schemas       map[string]string          `json:"schemas"`
		AppThemeKeys  []string                   `json:"appThemeKeys"`
		CodeThemeKeys []string                   `json:"codeThemeKeys"`
		Validators    map[string][]validatorCase `json:"-"`
	}
	readVectors(t, "testdata/vectors/aiSchemas.json", &v)
	var all map[string]json.RawMessage
	readVectors(t, "testdata/vectors/aiSchemas.json", &all)

	built := map[string]*Obj{
		"COMMIT_SCHEMA":                   commitSchema(),
		"CONFIG_FILES_SCHEMA":             configFilesSchema(),
		"ASK_ACTION_SCHEMA":               AskActionSchema(ActionAllow{}),
		"ASK_ACTIONS_SCHEMA":              askActionsSchema(),
		"askActionSchema()":               AskActionSchema(ActionAllow{}),
		"askActionSchema(history)":        AskActionSchema(ActionAllow{History: true}),
		"askActionSchema(remote)":         AskActionSchema(ActionAllow{Remote: true}),
		"askActionSchema(history,remote)": AskActionSchema(ActionAllow{History: true, Remote: true}),
		"askActionsSchema(false)":         AskActionsSchemaFor(false),
		"askActionsSchema(true)":          AskActionsSchemaFor(true),
		"repoChatActionsSchema(false)":    RepoChatActionsSchema(false),
		"repoChatActionsSchema(true)":     RepoChatActionsSchema(true),
		"REPO_CHAT_ACTIONS_SCHEMA":        RepoChatActionsSchema(false),
		"SMART_STAGE_SCHEMA":              smartStageSchema(),
		"ARTIFACT_SUGGESTIONS_SCHEMA":     artifactSuggestionsSchema(),
		"APP_THEME_SCHEMA":                themeSchema(AppThemeKeys),
		"CODE_THEME_SCHEMA":               themeSchema(CodeThemeKeys),
		"GRAPH_PALETTE_SCHEMA":            graphPaletteSchema(),
		"PR_DESCRIPTION_SCHEMA":           prDescriptionSchema(),
		"BRANCH_NAME_SCHEMA":              branchNameSchema(),
	}
	if len(built) != len(v.Schemas) {
		t.Errorf("TS has %d schemas, Go builds %d", len(v.Schemas), len(built))
	}
	for name, want := range v.Schemas {
		got, ok := built[name]
		if !ok {
			t.Errorf("schema %s not ported", name)
			continue
		}
		if s := jsonStringify(got); s != want {
			t.Errorf("%s:\n got %s\nwant %s", name, s, want)
		}
	}
	if !equalStrings(v.AppThemeKeys, AppThemeKeys) || !equalStrings(v.CodeThemeKeys, CodeThemeKeys) {
		t.Error("theme keys differ")
	}

	set := func(list []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range list {
			m[s] = true
		}
		return m
	}
	run := map[string]func(c validatorCase) any{
		"validateCommitMessage": func(c validatorCase) any { return validateCommitMessage(decodeJS(t, c.Input)) },
		"validateGeneratedFiles": func(c validatorCase) any {
			return validateGeneratedFiles(decodeJS(t, c.Input), c.Requested)
		},
		"validateSmartStage": func(c validatorCase) any { return validateSmartStage(decodeJS(t, c.Input), set(c.Known)) },
		"validateArtifactSuggestions": func(c validatorCase) any {
			return validateArtifactSuggestions(decodeJS(t, c.Input), c.Already)
		},
		"validateTheme": func(c validatorCase) any {
			keys := AppThemeKeys
			if c.Keys == "code" {
				keys = CodeThemeKeys
			}
			return validateTheme(decodeJS(t, c.Input), keys)
		},
		"validateGraphPalette":  func(c validatorCase) any { return validateGraphPalette(decodeJS(t, c.Input)) },
		"validatePRDescription": func(c validatorCase) any { return validatePRDescription(decodeJS(t, c.Input)) },
		"validateBranchName":    func(c validatorCase) any { return validateBranchName(decodeJS(t, c.Input)) },
		"validateResolvedFile": func(c validatorCase) any {
			var s string
			_ = json.Unmarshal(c.Input, &s)
			return validateResolvedFile(s)
		},
		"isSafeRepoPath": func(c validatorCase) any { return IsSafeRepoPath(decodeJS(t, c.Input)) },
	}
	for name, fn := range run {
		var cases []validatorCase
		if err := json.Unmarshal(all[name], &cases); err != nil || len(cases) == 0 {
			t.Fatalf("%s: no vectors (%v)", name, err)
		}
		for _, c := range cases {
			if got := fn(c); !sameJSON(t, got, c.Out) {
				t.Errorf("%s(%s) = %s, want %s", name, c.Input, asJSON(got), c.Out)
			}
		}
	}
}

// Ported from test/pureLogic.test.ts, "AI contracts — …".
func TestAIContracts(t *testing.T) {
	obj := func(s string) any {
		v, err := jsonParse(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := func(errs []string) string {
		if len(errs) == 0 {
			return ""
		}
		return errs[0]
	}
	contains := func(name string, errs []string, want string) {
		t.Helper()
		if !strings.Contains(first(errs), want) {
			t.Errorf("%s: first error %q does not contain %q", name, first(errs), want)
		}
	}
	empty := func(name string, errs []string) {
		t.Helper()
		if len(errs) != 0 {
			t.Errorf("%s: unexpected errors %q", name, errs)
		}
	}

	t.Run("commit messages: accepts a one-line subject with or without a body", func(t *testing.T) {
		empty("a", validateCommitMessage(obj(`{"summary":"feat: add login","description":""}`)))
		empty("b", validateCommitMessage(obj(`{"summary":"fix: guard null","description":"- checks the token"}`)))
	})
	t.Run("commit messages: rejects an empty or multi-line subject", func(t *testing.T) {
		contains("a", validateCommitMessage(obj(`{"summary":"  ","description":""}`)), "summary")
		contains("b", validateCommitMessage(obj(`{"summary":"one\ntwo","description":""}`)), "single line")
	})
	t.Run("commit messages: rejects prose instead of an object", func(t *testing.T) {
		contains("a", validateCommitMessage("Sure! Here is your commit"), "JSON object")
	})

	requested := []string{"CLAUDE.md", ".cursor/rules/project.mdc"}
	t.Run("generated config files: accepts the files that were asked for", func(t *testing.T) {
		empty("a", validateGeneratedFiles(obj(`{"files":[{"path":"CLAUDE.md","content":"# Project"}]}`), requested))
	})
	t.Run("generated config files: refuses paths that escape the repo", func(t *testing.T) {
		for _, p := range []string{"../../etc/passwd", "/etc/passwd", `C:\Windows\system.ini`, "a/../../b"} {
			v := newObj("files", []any{newObj("path", p, "content", "x")})
			contains(p, validateGeneratedFiles(v, requested), "inside the repo")
		}
	})
	t.Run("generated config files: accepts ordinary nested paths", func(t *testing.T) {
		for _, p := range []string{".cursor/rules/project.mdc", ".git/hooks/pre-commit", "a/..b/c"} {
			if !IsSafeRepoPath(p) {
				t.Errorf("%s refused", p)
			}
		}
	})
	t.Run("generated config files: refuses files nobody asked for, and empty content", func(t *testing.T) {
		contains("a", validateGeneratedFiles(obj(`{"files":[{"path":"sneaky.sh","content":"x"}]}`), requested), "was not requested")
		contains("b", validateGeneratedFiles(obj(`{"files":[{"path":"CLAUDE.md","content":""}]}`), requested), "content")
	})
	t.Run("generated config files: rejects an empty file list", func(t *testing.T) {
		contains("a", validateGeneratedFiles(obj(`{"files":[]}`), requested), "empty")
	})

	known := map[string]bool{"src/app.ts": true, "package-lock.json": true}
	t.Run("smart staging: accepts a subset of the files it was shown", func(t *testing.T) {
		empty("a", validateSmartStage(obj(`{"toStage":["src/app.ts"],"reason":"source only"}`), known))
		empty("b", validateSmartStage(obj(`{"toStage":[],"reason":"nothing worth staging"}`), known))
	})
	t.Run("smart staging: rejects a path that was never offered", func(t *testing.T) {
		contains("a", validateSmartStage(obj(`{"toStage":["src/ghost.ts"],"reason":""}`), known), "ghost")
	})

	colors := func(hex string) *Obj {
		o := newObj()
		for _, k := range AppThemeKeys {
			o.Set(k, hex)
		}
		return o
	}
	t.Run("themes and palettes: accepts a theme with every colour present and hex", func(t *testing.T) {
		empty("a", validateTheme(newObj("name", "Dusk", "light", colors("#ffffff"), "dark", colors("#101010")), AppThemeKeys))
	})
	t.Run("themes and palettes: names the missing keys and the malformed colours", func(t *testing.T) {
		partial := colors("#ffffff")
		partial.Delete("accent")
		contains("a", validateTheme(newObj("name", "X", "light", partial, "dark", colors("#101010")), AppThemeKeys), "accent")
		bad := colors("#ffffff")
		bad.Set("red", "crimson")
		contains("b", validateTheme(newObj("name", "X", "light", bad, "dark", colors("#101010")), AppThemeKeys), "red")
	})
	t.Run("themes and palettes: requires exactly 8 hex lane colours in a graph palette", func(t *testing.T) {
		eight := []any{}
		for i := 0; i < 8; i++ {
			eight = append(eight, "#123456")
		}
		empty("a", validateGraphPalette(newObj("name", "Neon", "colors", eight)))
		contains("b", validateGraphPalette(newObj("name", "Neon", "colors", eight[:6])), "exactly 8")
		contains("c", validateGraphPalette(newObj("name", "Neon", "colors", append(append([]any{}, eight[1:]...), "red"))), "hex")
	})

	t.Run("branch names and PR descriptions: accepts a conventional branch name", func(t *testing.T) {
		empty("a", validateBranchName(obj(`{"name":"feature/CMS-12-add-login"}`)))
	})
	t.Run("branch names and PR descriptions: rejects names git would refuse", func(t *testing.T) {
		contains("a", validateBranchName(obj(`{"name":"my branch"}`)), "spaces")
		contains("b", validateBranchName(obj(`{"name":"feature//x"}`)), "/")
		contains("c", validateBranchName(obj(`{"name":"feature/x.lock"}`)), ".lock")
		contains("d", validateBranchName(obj(`{"name":"a..b"}`)), "..")
		contains("e", validateBranchName(obj(`{"name":"feature/¡hola!"}`)), "may only use")
	})
	t.Run("branch names and PR descriptions: requires a single-line title and a body for a PR description", func(t *testing.T) {
		empty("a", validatePRDescription(obj(`{"title":"Add login","body":"## Changes\n- x"}`)))
		contains("b", validatePRDescription(obj(`{"title":"a\nb","body":"x"}`)), "single line")
		contains("c", validatePRDescription(obj(`{"title":"Add login","body":""}`)), "body")
	})

	t.Run("resolved merge conflicts: accepts a clean merged file", func(t *testing.T) {
		empty("a", validateResolvedFile("const a = 1\nconst b = 2\n"))
	})
	t.Run("resolved merge conflicts: rejects a result that still has conflict markers", func(t *testing.T) {
		contains("a", validateResolvedFile("a\n<<<<<<< HEAD\nb\n=======\nc\n>>>>>>> theirs\n"), "conflict markers")
	})
	t.Run("resolved merge conflicts: rejects an empty result", func(t *testing.T) {
		contains("a", validateResolvedFile("   "), "empty")
	})

	already := []string{"CLAUDE.md"}
	t.Run("suggested artifacts: accepts well-formed suggestions", func(t *testing.T) {
		empty("a", validateArtifactSuggestions(obj(`{"suggestions":[{"path":".editorconfig","description":"Editor defaults","reason":"Mixed indentation in the repo"}]}`), already))
	})
	t.Run("suggested artifacts: rejects re-suggesting something already selected", func(t *testing.T) {
		contains("a", validateArtifactSuggestions(obj(`{"suggestions":[{"path":"CLAUDE.md","description":"d","reason":"r"}]}`), already), "already selected")
	})
	t.Run("suggested artifacts: rejects escaping paths and missing prose", func(t *testing.T) {
		contains("a", validateArtifactSuggestions(obj(`{"suggestions":[{"path":"../x","description":"d","reason":"r"}]}`), already), "inside the repo")
		if n := len(validateArtifactSuggestions(obj(`{"suggestions":[{"path":"a.md","description":"","reason":""}]}`), already)); n != 2 {
			t.Errorf("got %d errors, want 2", n)
		}
	})
	t.Run("suggested artifacts: treats no suggestions as valid", func(t *testing.T) {
		empty("a", validateArtifactSuggestions(obj(`{"suggestions":[]}`), already))
	})
}

// Ported from the schema halves of test/chatActions.test.ts.
func TestRepoChatActionSchemas(t *testing.T) {
	t.Run("validates grounded file actions followed by Git actions (schema half)", func(t *testing.T) {
		s := RepoChatActionsSchema(false)
		if typ, _ := s.Get("type"); typ != "array" {
			t.Errorf("type = %v", typ)
		}
		if n, _ := s.Get("maxItems"); n != 64.0 {
			t.Errorf("maxItems = %v", n)
		}
	})
	t.Run("never offers force as a field a proposal could carry", func(t *testing.T) {
		schema := jsonStringify(RepoChatActionsSchema(true))
		if !strings.Contains(schema, `"push"`) {
			t.Error(`missing "push"`)
		}
		if strings.Contains(schema, "force") {
			t.Error("schema offers force")
		}
	})
}
