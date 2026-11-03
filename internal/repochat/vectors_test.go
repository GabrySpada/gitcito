package repochat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/GabrySpada/gitcito/internal/gitsvc"
	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/grounding/porttest"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Replays testdata/vectors/repoChat.json and testdata/payloads.json, written
// by test/port/repochat.test.ts from src/main/repoChat.ts.

type fnCase struct {
	Fn     string            `json:"fn"`
	Args   []json.RawMessage `json:"args"`
	Out    json.RawMessage   `json:"out"`
	Throws *string           `json:"throws"`
}

func loadCases(t *testing.T) []fnCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "repoChat.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []fnCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file.Cases
}

func arg[T any](t *testing.T, c fnCase, i int) T {
	t.Helper()
	var v T
	if i < len(c.Args) {
		if err := json.Unmarshal(c.Args[i], &v); err != nil {
			t.Fatalf("%s arg %d: %v", c.Fn, i, err)
		}
	}
	return v
}

func anyArg(t *testing.T, c fnCase, i int) any {
	t.Helper()
	v, err := shared.DecodeJSON(c.Args[i])
	if err != nil {
		t.Fatalf("%s arg %d: %v", c.Fn, i, err)
	}
	return v
}

func canon(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func setOf(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[s] = true
	}
	return m
}

// capturedSchemas serves the aiSchemas output the TS captured.
type capturedSchemas struct{ ask, askRemote, repoChat, repoChatRemote string }

func (s capturedSchemas) AskActionsSchema(remote bool) json.RawMessage {
	if remote {
		return json.RawMessage(s.askRemote)
	}
	return json.RawMessage(s.ask)
}

func (s capturedSchemas) RepoChatActionsSchema(remote bool) json.RawMessage {
	if remote {
		return json.RawMessage(s.repoChatRemote)
	}
	return json.RawMessage(s.repoChat)
}

func loadSchemas(t *testing.T) capturedSchemas {
	t.Helper()
	for _, c := range loadCases(t) {
		if c.Fn != "actionSchemas" {
			continue
		}
		var out struct{ Ask, AskRemote, RepoChat, RepoChatRemote string }
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		return capturedSchemas{out.Ask, out.AskRemote, out.RepoChat, out.RepoChatRemote}
	}
	t.Fatal("no actionSchemas vector")
	return capturedSchemas{}
}

func TestRepoChatVectors(t *testing.T) {
	schemas := loadSchemas(t)
	for i, c := range loadCases(t) {
		var got any
		var err error
		switch c.Fn {
		case "actionSchemas":
			continue
		case "chatAnswerSchema":
			allowActions := arg[bool](t, c, 0)
			allowFile := allowActions
			if len(c.Args) > 1 {
				allowFile = arg[bool](t, c, 1)
			}
			got = string(ChatAnswerSchema(schemas, allowActions, allowFile, arg[bool](t, c, 2), arg[bool](t, c, 3)))
		case "repoChatActionRules":
			got = ActionRules(arg[bool](t, c, 0), arg[bool](t, c, 1), arg[bool](t, c, 2))
		case "contentContainsActionPayload":
			got = ContentContainsActionPayload(arg[string](t, c, 0))
		case "selectSearchEvidence":
			got = SelectSearchEvidence(arg[[]gitsvc.CodeSearchHit](t, c, 0))
		case "normalizeRepoChatMessages":
			got, err = NormalizeMessages(anyArg(t, c, 0), arg[bool](t, c, 1))
		case "normalizeRepoChatAttachments":
			got, err = NormalizeAttachments(anyArg(t, c, 0), arg[string](t, c, 1))
		case "attachmentLabel":
			got = AttachmentLabel(arg[Attachment](t, c, 0))
		case "filterRepoChatPaths":
			got = FilterPaths(arg[[]string](t, c, 0), setOf(arg[[]string](t, c, 1)))
		case "validateChatSelection":
			got = ValidateSelection(anyArg(t, c, 0), setOf(arg[[]string](t, c, 1)))
		case "validateChatAnswer":
			var actx *g.RepoChatActionContext
			if cx := arg[*struct {
				WorkingTreePaths []string `json:"workingTreePaths"`
				EvidencePaths    []string `json:"evidencePaths"`
				CompletePaths    []string `json:"completePaths"`
			}](t, c, 2); cx != nil {
				actx = &g.RepoChatActionContext{
					WorkingTreePaths: setOf(cx.WorkingTreePaths),
					EvidencePaths:    setOf(cx.EvidencePaths),
					CompletePaths:    setOf(cx.CompletePaths),
				}
			}
			got = ValidateAnswer(anyArg(t, c, 0), setOf(arg[[]string](t, c, 1)), actx)
		case "finalizationMessages":
			exec, perr := normalizeExecutionResult(anyArg(t, c, 2))
			if perr != nil {
				t.Fatal(perr)
			}
			got = FinalizationMessages(arg[[]Message](t, c, 0), arg[gitsvc.RepoStatus](t, c, 1), exec)
		case "packRepoChatEvidence":
			max := ContextBytes
			if p := arg[*int](t, c, 1); p != nil {
				max = *p
			}
			got = PackEvidence(arg[[]Evidence](t, c, 0), max)
		case "clipEvidenceBudget":
			got = ClipEvidenceBudget(arg[[]Evidence](t, c, 0), arg[int](t, c, 1))
		default:
			t.Fatalf("unknown fn %q", c.Fn)
		}
		label := c.Fn + "#" + strconv.Itoa(i)
		if c.Throws != nil {
			if err == nil || err.Error() != *c.Throws {
				t.Errorf("%s: error = %v, want %q", label, err, *c.Throws)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", label, err)
			continue
		}
		if gv, w := canon(t, got), canon(t, c.Out); !reflect.DeepEqual(gv, w) {
			gj, _ := json.Marshal(gv)
			wj, _ := json.Marshal(w)
			t.Errorf("%s:\n got  %s\n want %s", label, gj, wj)
		}
	}
}

type payloadFile struct {
	GitEnv    map[string]string `json:"gitEnv"`
	Recipe    []porttest.Step   `json:"recipe"`
	Scenarios []struct {
		Name        string                       `json:"name"`
		Fn          string                       `json:"fn"`
		RepoPath    json.RawMessage              `json:"repoPath"`
		Messages    json.RawMessage              `json:"messages"`
		Cfg         json.RawMessage              `json:"cfg"`
		Attachments json.RawMessage              `json:"attachments"`
		Execution   json.RawMessage              `json:"execution"`
		Replies     map[string][]json.RawMessage `json:"replies"`
		Calls       json.RawMessage              `json:"calls"`
		Result      json.RawMessage              `json:"result"`
		Error       *string                      `json:"error"`
	} `json:"scenarios"`
}

func decode(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	v, err := shared.DecodeJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestRepoChatPayloads replays every scripted conversation: the requests the
// model receives (system prompt, user message, schema bytes, name, strict,
// temperature), the validation lines each scripted reply earns, and the reply
// or error the renderer gets.
func TestRepoChatPayloads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "payloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file payloadFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	b := porttest.BuildRepo(t, file.Recipe, file.GitEnv)
	git := gitsvc.New(nil, nil)
	schemas := loadSchemas(t)

	for _, sc := range file.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			replies := map[string][]json.RawMessage{}
			for k, list := range sc.Replies {
				for _, r := range list {
					replies[k] = append(replies[k], b.Bind(t, r))
				}
			}
			model := porttest.NewModel(replies)
			chat := &Chat{Git: git, Model: model, Schemas: schemas}
			cfg, _ := decode(t, sc.Cfg).(map[string]any)
			repoPath := decode(t, b.Bind(t, sc.RepoPath))
			messages := decode(t, b.Bind(t, sc.Messages))
			var out Reply
			var callErr error
			if sc.Fn == "answer" {
				var attachments any
				if string(sc.Attachments) != "null" {
					attachments = decode(t, b.Bind(t, sc.Attachments))
				}
				out, callErr = chat.Answer(context.Background(), repoPath, messages, cfg, attachments)
			} else {
				out, callErr = chat.Finalize(context.Background(), repoPath, messages, decode(t, b.Bind(t, sc.Execution)), cfg)
			}

			gotCalls := b.Canon(t, model.Calls)
			if model.Calls == nil {
				gotCalls = []any{}
			}
			wantCalls := b.Canon(t, sc.Calls)
			if !reflect.DeepEqual(gotCalls, wantCalls) {
				diffCalls(t, gotCalls, wantCalls)
			}
			if sc.Error != nil {
				if callErr == nil || b.Unbind(t, callErr.Error()) != *sc.Error {
					t.Errorf("error = %v, want %q", callErr, *sc.Error)
				}
				return
			}
			if callErr != nil {
				t.Fatalf("unexpected error: %v", callErr)
			}
			if got, want := b.Canon(t, out), b.Canon(t, sc.Result); !reflect.DeepEqual(got, want) {
				gj, _ := json.MarshalIndent(got, "", " ")
				wj, _ := json.MarshalIndent(want, "", " ")
				t.Errorf("result:\n got  %s\n want %s", gj, wj)
			}
		})
	}
}

// diffCalls reports the first differing field of the first differing call.
func diffCalls(t *testing.T, got, want any) {
	t.Helper()
	gl, _ := got.([]any)
	wl, _ := want.([]any)
	if len(gl) != len(wl) {
		gj, _ := json.MarshalIndent(got, "", " ")
		t.Errorf("calls: got %d, want %d\n%s", len(gl), len(wl), gj)
		return
	}
	for i := range gl {
		gm, _ := gl[i].(map[string]any)
		wm, _ := wl[i].(map[string]any)
		for k := range wm {
			if !reflect.DeepEqual(gm[k], wm[k]) {
				gj, _ := json.MarshalIndent(gm[k], "", " ")
				wj, _ := json.MarshalIndent(wm[k], "", " ")
				t.Errorf("call %d (%v) field %q:\n got  %s\n want %s", i, wm["feature"], k, gj, wj)
				return
			}
		}
	}
}
