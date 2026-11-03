package repochat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/ai"
	"github.com/GabrySpada/gitcito/internal/gitsvc"
	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/grounding/aicompleter"
	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/GabrySpada/gitcito/internal/testutil"
)

// Ports of the main-process cases in test/repoChat.test.ts and
// test/chatActions.test.ts. The session-store, panel, safety-ladder, executor
// and drag/drop cases test renderer code and stay in vitest.

func jv(t *testing.T, s string) any {
	t.Helper()
	v, err := shared.DecodeJSON([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func joined(errs []string) string { return strings.Join(errs, " ") }

// dig follows object keys through a decoded JSON tree.
func dig(v any, keys ...string) any {
	for _, k := range keys {
		v, _ = g.Prop(v, k)
	}
	return v
}

// schemas are internal/ai's builders, which the TS-captured bytes pin.
var schemas = aicompleter.Schemas{}

// ─── test/repoChat.test.ts › repository chat grounding ─────────────────────

func TestRepoChatGrounding(t *testing.T) {
	t.Run("defaults repository chat to file read-only while preserving an explicit CRUD opt-in", func(t *testing.T) {
		if shared.MigrateAIConfig(map[string]any{})["repoChatReadOnly"] != true {
			t.Fatal("default")
		}
		if shared.MigrateAIConfig(map[string]any{"repoChatReadOnly": false})["repoChatReadOnly"] != false {
			t.Fatal("opt-in")
		}
	})
	t.Run("tells a file-read-only agent about Git actions without advertising file mutations", func(t *testing.T) {
		rules := ActionRules(true, false, false)
		if !strings.Contains(rules, `{"type":"stage"`) || !strings.Contains(rules, "File creation, editing, replacement, and deletion are disabled") {
			t.Fatal(rules)
		}
		for _, banned := range []string{"edit_file", "write_file", "delete_file"} {
			if strings.Contains(rules, banned) {
				t.Fatal(banned)
			}
		}
	})
	t.Run("builds a factual action finalization prompt with refreshed status", func(t *testing.T) {
		tracking := "origin/main"
		status := gitsvc.RepoStatus{Current: "main", Tracking: &tracking, Ahead: 1, Staged: []gitsvc.FileEntry{{Path: "LICENSE", Status: "M"}}}
		exec, err := normalizeExecutionResult(jv(t, `{"applied":1,"failedIndex":1,"failedType":"commit","error":{"code":"hook_failed"},"remaining":2,
			"actionResults":[{"index":0,"type":"edit_file","status":"done"},{"index":1,"type":"commit","status":"failed"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		var prompt []string
		for _, m := range FinalizationMessages([]Message{{"user", "Change the license."}}, status, exec) {
			prompt = append(prompt, m.Content)
		}
		text := strings.Join(prompt, "\n")
		for _, want := range []string{"Do not propose any actions", "Applied actions: 1", "Failed action: commit", "Remaining actions: 2", "Current branch: main"} {
			if !strings.Contains(text, want) {
				t.Fatal(want)
			}
		}
		if strings.Contains(string(ChatAnswerSchema(nil, false, false, false, false)), `"actions"`) {
			t.Fatal("actions in a disabled schema")
		}
	})
	t.Run("keeps compact search evidence from more than eight files", func(t *testing.T) {
		var hits []gitsvc.CodeSearchHit
		for i := 0; i < 17; i++ {
			hits = append(hits, gitsvc.CodeSearchHit{File: "src/i18n/l" + string(rune('a'+i)) + ".ts", Line: i + 1, Text: "MIT"})
		}
		files := map[string]bool{}
		for _, h := range SelectSearchEvidence(hits) {
			files[h.File] = true
		}
		if len(files) != 17 {
			t.Fatal(len(files))
		}
	})
	t.Run("packs one match per file before second matches", func(t *testing.T) {
		got := SelectSearchEvidence([]gitsvc.CodeSearchHit{{File: "a.ts", Line: 1}, {File: "a.ts", Line: 20}, {File: "b.ts", Line: 2}})
		if got[0].File != "a.ts" || got[0].Line != 1 || got[1].File != "b.ts" || got[2].Line != 20 {
			t.Fatal(got)
		}
	})
	t.Run("detects executable action JSON hidden in answer content", func(t *testing.T) {
		if !ContentContainsActionPayload("```json\n[{\"type\":\"stage\",\"files\":[\"a.ts\"]}]\n```") || ContentContainsActionPayload(`Example: {"kind":"stage"}`) {
			t.Fatal("detection")
		}
	})
	t.Run("detects an edit_file payload whose newText contains a nested Markdown fence", func(t *testing.T) {
		content := strings.Join([]string{
			"I will add a Quick build section to README.md.", "```json", "[", "  {",
			`    "type": "edit_file",`, `    "path": "README.md",`,
			`    "oldText": "## Install\nInstall the app.",`,
			`    "newText": "## Install\nInstall the app.\n\n## Quick build\n` + "```" + `sh\nnpm run build\n` + "```" + `",`,
			`    "description": "Add a Quick build section"`, "  }", "]", "```",
		}, "\n")
		if !ContentContainsActionPayload(content) {
			t.Fatal("not detected")
		}
		if e := joined(ValidateAnswer(map[string]any{"content": content, "sourceIds": []any{}}, nil, nil)); !strings.Contains(e, "top-level actions field") {
			t.Fatal(e)
		}
		if ContentContainsActionPayload("The field `\"type\": \"edit_file\"` names the action.") {
			t.Fatal("false positive")
		}
	})
	t.Run("allows only readable tracked paths outside privacy filters", func(t *testing.T) {
		got := FilterPaths([]string{"README.md", "src/app.ts", ".env", "config/secrets.json", "assets/logo.png",
			"node_modules/pkg/index.js", "src/generated/client.ts", "package-lock.json", "../outside.ts"}, map[string]bool{"src/app.ts": true})
		if !reflect.DeepEqual(got, []string{"README.md"}) {
			t.Fatal(got)
		}
	})
	t.Run("honours ignore rules even for paths checked as tracked context", func(t *testing.T) {
		// The vitest case asked the checkout itself; a throwaway repo with the
		// same rule keeps the case independent of how node_modules is linked.
		testutil.IsolateGitEnv(t)
		root := testutil.InitRepo(t)
		testutil.WriteFile(t, root, ".gitignore", "node_modules\n")
		testutil.WriteFile(t, root, "src/main/index.ts", "export {}\n")
		got, err := gitsvc.New(nil, nil).IgnoredTrackedFiles(context.Background(), root, []string{"node_modules/example.js", "src/main/index.ts"})
		if err != nil || !reflect.DeepEqual(got, []string{"node_modules/example.js"}) {
			t.Fatal(got, err)
		}
	})
	t.Run("bounds and validates the untrusted IPC transcript", func(t *testing.T) {
		raw := []any{}
		for i := 0; i < 14; i++ {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			raw = append(raw, map[string]any{"role": role, "content": "message " + string(rune('0'+i%10)), "sources": []any{}})
		}
		raw[0].(map[string]any)["content"] = "message 0"
		raw[2].(map[string]any)["content"] = "message 2"
		raw[13] = map[string]any{"role": "user", "content": " latest question ", "sources": []any{}}
		got, err := NormalizeMessages(raw, true)
		if err != nil || len(got) != 12 || got[0].Content != "message 2" || got[11] != (Message{"user", "latest question"}) {
			t.Fatal(got, err)
		}
		if _, err := NormalizeMessages([]any{map[string]any{"role": "assistant", "content": "answer"}}, true); err == nil {
			t.Fatal("accepted a transcript ending with the assistant")
		}
	})
	t.Run("rejects model-invented paths and evidence ids", func(t *testing.T) {
		paths := map[string]bool{"README.md": true, "src/app.ts": true}
		if e := ValidateSelection(jv(t, `{"paths":["src/app.ts"],"searches":["createApp"]}`), paths); len(e) != 0 {
			t.Fatal(e)
		}
		if e := joined(ValidateSelection(jv(t, `{"paths":["../../secret"],"searches":[]}`), paths)); !strings.Contains(e, "not in the tracked-file list") {
			t.Fatal(e)
		}
		ids := map[string]bool{"E1": true}
		if e := ValidateAnswer(jv(t, `{"content":"Grounded answer","sourceIds":["E1"]}`), ids, nil); len(e) != 0 {
			t.Fatal(e)
		}
		if e := joined(ValidateAnswer(jv(t, `{"content":"Invented","sourceIds":["E2"]}`), ids, nil)); !strings.Contains(e, "was not provided") {
			t.Fatal(e)
		}
	})
	t.Run("packs numbered evidence under the shared context budget", func(t *testing.T) {
		got := PackEvidence([]Evidence{{Path: "a.ts", StartLine: 10, EndLine: 20, Text: "one\ntwo\nthree"}, {Path: "b.ts", StartLine: 1, EndLine: 10, Text: "abcdefghij"}}, 7)
		if !reflect.DeepEqual(got, []Evidence{{ID: "E1", Path: "a.ts", StartLine: 10, EndLine: 11, Text: "one\ntwo"}}) {
			t.Fatal(got)
		}
	})
}

// ─── test/repoChat.test.ts › repository chat pinned context ────────────────

func TestRepoChatPinnedContext(t *testing.T) {
	empty := gitsvc.RepoStatus{Current: "main", Staged: []gitsvc.FileEntry{}, Unstaged: []gitsvc.FileEntry{}, Conflicted: []gitsvc.FileEntry{}}
	chat := &Chat{Git: gitsvc.New(nil, nil)}
	ctx := context.Background()

	t.Run("accepts only well-formed items and folds an in-repo path back to a repo file", func(t *testing.T) {
		got, err := NormalizeAttachments(jv(t, `[{"kind":"file","path":"src/main/git.ts"},{"kind":"commit","hash":"AbC1234"},
			{"kind":"external","path":"/tmp/notes.md"},{"kind":"external","path":"/repo/src/app.ts"},{"kind":"file","path":"src/main/git.ts"}]`), "/repo")
		want := []Attachment{{Kind: "file", Path: "src/main/git.ts"}, {Kind: "commit", Hash: "abc1234"}, {Kind: "external", Path: "/tmp/notes.md"}, {Kind: "file", Path: "src/app.ts"}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(got, err)
		}
		if got, err := NormalizeAttachments(nil, ""); err != nil || len(got) != 0 {
			t.Fatal(got, err)
		}
		for _, bad := range []string{`[{"kind":"file","path":"../escape.ts"}]`, `[{"kind":"commit","hash":"not-a-hash"}]`, `[{"kind":"external","path":"relative.md"}]`} {
			if _, err := NormalizeAttachments(jv(t, bad), ""); err == nil {
				t.Error(bad)
			}
		}
	})
	t.Run("refuses a secret-looking pinned file and says why instead of reading it", func(t *testing.T) {
		skipped := []Skipped{}
		evidence, _ := chat.CollectAttachmentEvidence(ctx, testutil.MustRepoRoot(t), []Attachment{{Kind: "file", Path: ".env"}}, nil, empty, &skipped, false)
		if len(evidence) != 0 || !reflect.DeepEqual(skipped, []Skipped{{".env", "secret"}}) {
			t.Fatal(evidence, skipped)
		}
	})
	t.Run("refuses a pinned file outside the repository when it is a secret or missing", func(t *testing.T) {
		skipped := []Skipped{}
		chat.CollectAttachmentEvidence(ctx, testutil.MustRepoRoot(t), []Attachment{{Kind: "external", Path: "/definitely/not/here.md"}, {Kind: "external", Path: "/tmp/id_rsa"}}, nil, empty, &skipped, false)
		if len(skipped) != 2 || skipped[0].Reason != "unreadable" || skipped[1].Reason != "secret" {
			t.Fatal(skipped)
		}
	})
	t.Run("reads a pinned commit as diff hunks plus its message", func(t *testing.T) {
		dir := testutil.CloneFixture(t, "cherry-pick")
		five := 5
		log, err := gitsvc.New(nil, nil).Log(ctx, dir, &five, 0)
		if err != nil || len(log) == 0 {
			t.Fatal(err)
		}
		skipped := []Skipped{}
		evidence, notes := chat.CollectAttachmentEvidence(ctx, dir, []Attachment{{Kind: "commit", Hash: log[0].Hash}}, nil, empty, &skipped, false)
		if len(skipped) != 0 || len(notes) == 0 || !strings.Contains(notes[0], log[0].Subject) || len(evidence) == 0 {
			t.Fatal(skipped, notes, evidence)
		}
		for _, e := range evidence {
			if !e.Pinned {
				t.Fatal(e)
			}
		}
	})
}

// ─── test/chatActions.test.ts (main-process cases) ──────────────────────────

const editFile = `{"type":"edit_file","path":"LICENSE","oldText":"MIT License","newText":"Apache License","description":"Replace the license heading"}`

func ctxOf(working, evidence, complete []string) g.RepoChatActionContext {
	set := func(l []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range l {
			m[s] = true
		}
		return m
	}
	return g.RepoChatActionContext{WorkingTreePaths: set(working), EvidencePaths: set(evidence), CompletePaths: set(complete)}
}

func TestRepoChatFileActionContract(t *testing.T) {
	base := ctxOf([]string{"dirty.ts"}, []string{"LICENSE"}, []string{"LICENSE"})
	t.Run("keeps Git actions but rejects file mutations in file read-only mode", func(t *testing.T) {
		ro := ctxOf([]string{"a.ts"}, []string{"LICENSE"}, []string{"LICENSE"})
		ro.AllowFileActions = g.Bool(false)
		if e := g.ValidateRepoChatActions(jv(t, `[{"type":"stage","files":["a.ts"],"description":"Stage a.ts"}]`), ro); len(e) != 0 {
			t.Fatal(e)
		}
		if e := joined(g.ValidateRepoChatActions(jv(t, `[`+editFile+`]`), ro)); !strings.Contains(e, "file read-only mode") {
			t.Fatal(e)
		}
		items := dig(jv(t, string(ChatAnswerSchema(schemas, true, false, false, false))), "properties", "actions", "items")
		if _, hasAnyOf := g.Prop(items, "anyOf"); hasAnyOf {
			t.Fatal("anyOf in the ask-only schema")
		}
		enum := g.JSONStringify(dig(items, "properties", "type", "enum"))
		if !strings.Contains(enum, `"stage"`) || !strings.Contains(enum, `"commit"`) || strings.Contains(enum, "_file") {
			t.Fatal(enum)
		}
	})
	t.Run("validates grounded file actions followed by Git actions", func(t *testing.T) {
		if e := g.ValidateRepoChatActions(jv(t, `[`+editFile+`,{"type":"stage","files":["LICENSE"],"description":"Stage LICENSE"}]`), base); len(e) != 0 {
			t.Fatal(e)
		}
		s := jv(t, string(schemas.RepoChatActionsSchema(false)))
		if v, _ := g.Prop(s, "type"); v != "array" {
			t.Fatal(s)
		}
		if v, _ := g.Prop(s, "maxItems"); g.JSONStringify(v) != "64" {
			t.Fatal(s)
		}
	})
	t.Run("keeps replacement grounded and file actions before Git actions", func(t *testing.T) {
		if e := joined(g.ValidateRepoChatActions(jv(t, `[{"type":"delete_file","path":"ghost.ts","description":"Delete it"}]`), base)); !strings.Contains(e, "evidence") {
			t.Fatal(e)
		}
		if e := joined(g.ValidateRepoChatActions(jv(t, `[{"type":"stage","files":["dirty.ts"],"description":"Stage"},`+editFile+`]`), base)); !strings.Contains(e, "before Git actions") {
			t.Fatal(e)
		}
		noComplete := ctxOf([]string{"dirty.ts"}, []string{"LICENSE"}, nil)
		if e := joined(g.ValidateRepoChatActions(jv(t, `[{"type":"write_file","path":"LICENSE","content":"Apache","mode":"replace","description":"Replace"}]`), noComplete)); !strings.Contains(e, "complete evidence") {
			t.Fatal(e)
		}
	})
	t.Run("rejects plans over the file, Git, and total action limits", func(t *testing.T) {
		var items []string
		for i := 0; i < 49; i++ {
			items = append(items, `{"type":"write_file","path":"new-`+string(rune('a'+i%26))+string(rune('a'+i/26))+`.txt","content":"x","mode":"create","description":"Create file"}`)
		}
		if e := joined(g.ValidateRepoChatActions(jv(t, "["+strings.Join(items, ",")+"]"), base)); !strings.Contains(e, "48 file actions") {
			t.Fatal(e)
		}
	})
}

func TestRepoChatAnswerContractWithActions(t *testing.T) {
	ids := map[string]bool{"E1": true}
	paths := ctxOf([]string{"a.ts"}, nil, nil)
	stage := `{"type":"stage","files":["a.ts"],"description":"Stage a.ts"}`
	t.Run("only offers the actions field when the setting allows it", func(t *testing.T) {
		if !strings.Contains(string(ChatAnswerSchema(schemas, true, true, false, false)), `"actions"`) ||
			strings.Contains(string(ChatAnswerSchema(schemas, false, false, false, false)), `"actions"`) {
			t.Fatal("actions field")
		}
	})
	t.Run("accepts a grounded proposal and rejects invented paths", func(t *testing.T) {
		if e := ValidateAnswer(jv(t, `{"content":"plan","sourceIds":[],"actions":[`+stage+`]}`), ids, &paths); len(e) != 0 {
			t.Fatal(e)
		}
		if e := joined(ValidateAnswer(jv(t, `{"content":"plan","sourceIds":[],"actions":[{"type":"stage","files":["ghost.ts"],"description":"Stage a.ts"}]}`), ids, &paths)); !strings.Contains(e, "ghost.ts") {
			t.Fatal(e)
		}
	})
	t.Run("tolerates a blank content only when a proposal can stand in for it", func(t *testing.T) {
		if e := ValidateAnswer(jv(t, `{"content":"","sourceIds":[],"actions":[`+stage+`]}`), ids, &paths); len(e) != 0 {
			t.Fatal(e)
		}
		for _, v := range []string{`{"content":"","sourceIds":[]}`, `{"content":"","sourceIds":[],"actions":[]}`} {
			if e := joined(ValidateAnswer(jv(t, v), ids, &paths)); !strings.Contains(e, "non-empty") {
				t.Fatal(e)
			}
		}
		// Proposals disabled: an empty answer stays an error even with actions.
		if e := joined(ValidateAnswer(jv(t, `{"content":"","sourceIds":[],"actions":[`+stage+`]}`), ids, nil)); !strings.Contains(e, "non-empty") {
			t.Fatal(e)
		}
	})
	t.Run("rejects any actions at all when proposals are disabled", func(t *testing.T) {
		if e := joined(ValidateAnswer(jv(t, `{"content":"plan","sourceIds":[],"actions":[`+stage+`]}`), ids, nil)); !strings.Contains(e, "disabled") {
			t.Fatal(e)
		}
		if e := ValidateAnswer(jv(t, `{"content":"answer","sourceIds":[]}`), ids, nil); len(e) != 0 {
			t.Fatal(e)
		}
	})
	t.Run("rejects action payloads hidden in content instead of the actions field", func(t *testing.T) {
		v := map[string]any{"content": "```json\n[" + stage + "]\n```", "sourceIds": []any{}}
		if e := joined(ValidateAnswer(v, ids, &paths)); !strings.Contains(e, "actions field") {
			t.Fatal(e)
		}
	})
}

func TestRepoChatRemoteAndHistoryActions(t *testing.T) {
	c := ctxOf([]string{"dirty.ts"}, []string{"LICENSE"}, []string{"LICENSE"})
	c.AllowFileActions = g.Bool(true)
	remote := c
	remote.AllowRemoteActions = true
	t.Run("refuses remote actions until the setting turns them on, and says which setting", func(t *testing.T) {
		push := `[{"type":"push","branch":"feature/x","description":"Publish the branch"}]`
		if e := joined(g.ValidateRepoChatActions(jv(t, push), c)); !strings.Contains(e, "remote actions") || !strings.Contains(e, "Settings") {
			t.Fatal(e)
		}
		if e := g.ValidateRepoChatActions(jv(t, push), remote); len(e) != 0 {
			t.Fatal(e)
		}
	})
	t.Run("lets chat move history, and keeps that out of the Ask planner", func(t *testing.T) {
		rebase := `[{"type":"rebase","onto":"main","description":"Rebase onto main"}]`
		if e := g.ValidateRepoChatActions(jv(t, rebase), c); len(e) != 0 {
			t.Fatal(e)
		}
		if e := joined(g.ValidateAskActions(jv(t, rebase), map[string]bool{}, g.AskAllow{})); !strings.Contains(e, "repository chat") {
			t.Fatal(e)
		}
	})
	t.Run("refuses a commit hash that is not a hash, and a pull request with no base", func(t *testing.T) {
		if e := joined(g.ValidateRepoChatActions(jv(t, `[{"type":"revert","hashes":["the last one"],"description":"Revert"}]`), c)); !strings.Contains(e, "hashes") {
			t.Fatal(e)
		}
		if e := joined(g.ValidateRepoChatActions(jv(t, `[{"type":"open_pr","title":"feat: x","description":"Open it"}]`), remote)); !strings.Contains(e, "target") {
			t.Fatal(e)
		}
	})
	t.Run("never offers force as a field a proposal could carry", func(t *testing.T) {
		s := string(schemas.RepoChatActionsSchema(true))
		if !strings.Contains(s, `"push"`) || strings.Contains(s, "force") {
			t.Fatal(s)
		}
	})
	t.Run("describes remote actions in the prompt only while they are allowed", func(t *testing.T) {
		if !strings.Contains(ActionRules(true, true, true), "stack_submit") {
			t.Fatal("missing")
		}
		off := ActionRules(true, true, false)
		if strings.Contains(off, "stack_submit") || !strings.Contains(off, "disabled by settings") {
			t.Fatal(off)
		}
	})
	t.Run("offers a second look only while a round remains", func(t *testing.T) {
		if !strings.Contains(string(ChatAnswerSchema(schemas, true, true, false, true)), `"needMore"`) ||
			strings.Contains(string(ChatAnswerSchema(schemas, true, true, false, false)), `"needMore"`) {
			t.Fatal("needMore")
		}
	})
	t.Run("accepts an evidence request that arrives without an answer yet", func(t *testing.T) {
		if e := ValidateAnswer(jv(t, `{"content":"","sourceIds":[],"needMore":{"paths":["a.ts"],"reason":"need the caller"}}`), map[string]bool{}, nil); len(e) != 0 {
			t.Fatal(e)
		}
		if e := joined(ValidateAnswer(jv(t, `{"content":"","sourceIds":[]}`), map[string]bool{}, nil)); !strings.Contains(e, "non-empty") {
			t.Fatal(e)
		}
	})
	t.Run("hands the model the shape of .gitcito.json rather than letting it invent one", func(t *testing.T) {
		rules := ActionRules(true, true, false)
		if !strings.Contains(rules, ".gitcito.json") || !strings.Contains(rules, `"version":1`) || strings.Contains(ActionRules(true, false, false), ".gitcito.json") {
			t.Fatal(rules)
		}
	})
}

// ─── test/repoChatLive.test.ts ─────────────────────────────────────────────

// TestRepoChatLive is opt-in, like the vitest file: GITCITO_LIVE_AI=1 plus
// GITCITO_LIVE_ENDPOINT, GITCITO_LIVE_API_KEY and GITCITO_LIVE_MODEL.
func TestRepoChatLive(t *testing.T) {
	if os.Getenv("GITCITO_LIVE_AI") != "1" {
		t.Skip("set GITCITO_LIVE_AI=1 to probe a live OpenAI-compatible provider")
	}
	endpoint, apiKey, model := os.Getenv("GITCITO_LIVE_ENDPOINT"), os.Getenv("GITCITO_LIVE_API_KEY"), os.Getenv("GITCITO_LIVE_MODEL")
	if endpoint == "" || apiKey == "" || model == "" {
		t.Fatal("GITCITO_LIVE_ENDPOINT, GITCITO_LIVE_API_KEY and GITCITO_LIVE_MODEL are required")
	}
	testutil.IsolateGitEnv(t)
	repo := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.name", "Gitcito Test")
	run("config", "user.email", "gitcito@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "LICENSE"), []byte("MIT License\n\nCopyright MyAppDesk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "LICENSE")
	run("commit", "-m", "test: seed license")

	cfg := shared.DefaultAIConfig()
	for k, v := range map[string]any{"enabled": true, "provider": "openai", "endpoint": endpoint, "apiKey": apiKey, "model": model,
		"repoChatActions": true, "repoChatReadOnly": false} {
		cfg[k] = v
	}
	client := ai.NewClient(ai.Options{DataDir: func() (string, error) { return t.TempDir(), nil }})
	chat := &Chat{Git: gitsvc.New(nil, nil), Model: aicompleter.New(client), Schemas: schemas}
	reply, err := chat.Answer(context.Background(), repo, []any{map[string]any{"role": "user",
		"content": "Change LICENSE to Apache 2.0. Propose the file change without committing."}}, cfg, nil)
	if err != nil {
		// Never echo the provider's error: it can carry the endpoint or key.
		t.Fatal("Live provider probe failed without exposing its configuration.")
	}
	for _, a := range reply.Actions {
		if a["type"] == "edit_file" || a["type"] == "write_file" {
			return
		}
	}
	t.Fatal("no file action proposed")
}
