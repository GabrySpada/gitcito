// Package porttest holds test support shared by the repochat and wiki port
// tests: the scripted model test/port/repochat.test.ts used when it captured
// the payload vectors, and the repository recipe both sides build. It is
// imported only from _test.go files.
package porttest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Spec is the recorded part of a JSONSpec.
type Spec struct {
	Name                   string `json:"name"`
	Schema                 string `json:"schema"`
	Strict                 *bool  `json:"strict"`
	MaxTokens              *int   `json:"maxTokens"`
	NativeStructuredOutput *bool  `json:"nativeStructuredOutput"`
	Normalize              bool   `json:"normalize"`
}

// Call is one recorded chatCompleteJson request.
type Call struct {
	Feature     string          `json:"feature"`
	Model       string          `json:"model"`
	Messages    []g.ChatMessage `json:"messages"`
	Spec        Spec            `json:"spec"`
	Temperature float64         `json:"temperature"`
	Reply       json.RawMessage `json:"reply"`
	Errors      []string        `json:"errors"`
}

var rePageLine = regexp.MustCompile(`^Page: (.*) \(`)

// Model answers chatCompleteJson from a script, the way the TS mock did: it
// runs the spec's normalize and validate on the scripted reply, records the
// request, and fails with InvalidAIResponse on the first validation error.
type Model struct {
	mu      sync.Mutex
	replies map[string][]json.RawMessage
	Calls   []Call
}

// NewModel scripts replies by key: the feature, or `wikiPage:<title>` for a
// wiki page (pages are written concurrently, so order cannot pick them).
func NewModel(replies map[string][]json.RawMessage) *Model {
	return &Model{replies: replies}
}

func replyKey(feature string, messages []g.ChatMessage) string {
	if feature != "wikiPage" {
		return feature
	}
	title := ""
	if len(messages) > 1 {
		if m := rePageLine.FindStringSubmatch(messages[1].Content); m != nil {
			title = m[1]
		}
	}
	return "wikiPage:" + title
}

// ChatCompleteJSON implements grounding.Completer.
func (m *Model) ChatCompleteJSON(ctx context.Context, cfg shared.Object, messages []g.ChatMessage, feature string, spec g.JSONSpec, temperature float64) (any, error) {
	key := replyKey(feature, messages)
	m.mu.Lock()
	queue := m.replies[key]
	if len(queue) == 0 {
		m.mu.Unlock()
		return nil, &g.InvalidAIResponse{Message: "no scripted reply for " + key}
	}
	reply := queue[0]
	m.replies[key] = queue[1:]
	m.mu.Unlock()

	value, err := shared.DecodeJSON(reply)
	if err != nil {
		return nil, err
	}
	if spec.Normalize != nil {
		value = spec.Normalize(value)
	}
	errs, err := spec.Validate(ctx, value)
	if err != nil {
		return nil, err
	}
	if errs == nil {
		errs = []string{}
	}
	model, _ := cfg["model"].(string)
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{
		Feature:  feature,
		Model:    model,
		Messages: messages,
		Spec: Spec{
			Name: spec.Name, Schema: string(spec.Schema), Strict: spec.Strict, MaxTokens: spec.MaxTokens,
			NativeStructuredOutput: spec.NativeStructuredOutput, Normalize: spec.Normalize != nil,
		},
		Temperature: temperature,
		Reply:       reply,
		Errors:      errs,
	})
	m.mu.Unlock()
	if len(errs) > 0 {
		return nil, &g.InvalidAIResponse{Message: "The AI returned an invalid response: " + errs[0], LastReply: string(reply)}
	}
	return value, nil
}

// Step is one recipe step: write a file (relative to the repo, or under the
// temp root with an @TMP/ prefix) or run git with an optional fixed date.
type Step struct {
	Write   *string  `json:"write"`
	Content string   `json:"content"`
	Git     []string `json:"git"`
	Date    string   `json:"date"`
}

// Built is a repository built from a recipe.
type Built struct {
	Tmp, Repo string
	env       []string
}

// BuildRepo builds <tmp>/demo-repo from the recipe with the fixed identity
// and dates the vectors were captured with, so commit hashes match.
func BuildRepo(t *testing.T, recipe []Step, gitEnv map[string]string) *Built {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &Built{Tmp: tmp, Repo: filepath.Join(tmp, "demo-repo")}
	if err := os.MkdirAll(b.Repo, 0o755); err != nil {
		t.Fatal(err)
	}
	b.env = os.Environ()
	for k, v := range gitEnv {
		b.env = append(b.env, k+"="+v)
		t.Setenv(k, v)
	}
	for _, s := range recipe {
		if s.Write != nil {
			target := filepath.Join(b.Repo, *s.Write)
			if rest, ok := strings.CutPrefix(*s.Write, "@TMP/"); ok {
				target = filepath.Join(tmp, rest)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(s.Content), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		env := b.env
		if s.Date != "" {
			env = append(append([]string{}, env...), "GIT_AUTHOR_DATE="+s.Date, "GIT_COMMITTER_DATE="+s.Date)
		}
		b.Git(t, env, s.Git...)
	}
	return b
}

// Git runs git in the built repository.
func (b *Built) Git(t *testing.T, env []string, args ...string) string {
	t.Helper()
	if env == nil {
		env = b.env
	}
	cmd := exec.Command("git", append([]string{"-C", b.Repo}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Bind replaces the @HEAD~1 / @HEAD / @REPO / @TMP tokens in raw JSON.
func (b *Built) Bind(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	s := string(raw)
	s = strings.ReplaceAll(s, "@HEAD~1", b.Git(t, nil, "rev-parse", "HEAD~1"))
	s = strings.ReplaceAll(s, "@HEAD", b.Git(t, nil, "rev-parse", "HEAD"))
	s = strings.ReplaceAll(s, "@REPO", b.Repo)
	s = strings.ReplaceAll(s, "@TMP", b.Tmp)
	return json.RawMessage(s)
}

// Unbind turns paths and hashes back into tokens, in the order the TS did.
func (b *Built) Unbind(t *testing.T, s string) string {
	t.Helper()
	head := b.Git(t, nil, "rev-parse", "HEAD")
	prev := b.Git(t, nil, "rev-parse", "HEAD~1")
	s = strings.ReplaceAll(s, b.Repo, "@REPO")
	s = strings.ReplaceAll(s, b.Tmp, "@TMP")
	s = strings.ReplaceAll(s, head, "@HEAD")
	return strings.ReplaceAll(s, prev, "@HEAD~1")
}

// Canon turns any value into plain decoded JSON (float64 numbers), after
// unbinding, so results compare by JSON meaning.
func (b *Built) Canon(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal([]byte(b.Unbind(t, string(raw))), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// Plain decodes recorded JSON the same way, without unbinding.
func Plain(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}
