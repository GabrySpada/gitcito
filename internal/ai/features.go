package ai

// The AI features (src/main/ai.ts). Prompt wording is product behaviour: every
// string below is the TS template literal verbatim, and
// internal/ai/testdata/features.json pins the request bodies byte-for-byte.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
)

// CommitMessage is AICommitMessage.
type CommitMessage struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
}

// CommitContext is AICommitContext.
type CommitContext struct {
	Branch string `json:"branch"`
}

var ticketRE = regexp.MustCompile(`([A-Z][A-Z0-9]+-\d+)`)

func styleGuidance(cfg Config, branch string) string {
	ticket := ""
	if m := ticketRE.FindStringSubmatch(branch); m != nil {
		ticket = m[1]
	}
	conventional := "Format the summary using Conventional Commits: a prefix like feat:, fix:, refactor:, chore:, docs:, test:, perf: followed by an imperative description."
	ticketRule := ""
	if ticket != "" {
		ticketRule = `The current branch is "` + branch + `" and references ticket ` + ticket + `. Prefix the summary with "` + ticket +
			`: " (e.g. "` + ticket + `: add login validation"). Do not use any other prefix.`
	}

	var rule string
	switch cfg.CommitStyle {
	case "conventional":
		rule = conventional
	case "gitmoji":
		rule = "Start the summary with the most fitting gitmoji (✨ feature, 🐛 fix, ♻️ refactor, 📝 docs, ✅ tests, 🔧 config, ⚡️ perf) followed by a space and an imperative description. No other prefix."
	case "ticket":
		rule = ticketRule
		if rule == "" {
			rule = `No ticket reference found in the branch name ("` + branch + `"). Fall back to a plain imperative summary without prefixes.`
		}
	case "plain":
		rule = "Write a plain imperative summary with no prefixes, no emoji, no ticket references."
	case "caveman":
		rule = `Write the summary in exaggerated caveman speak: short, broken sentences in ALL CAPS, e.g. "ME ADD LOGIN. CODE GOOD.". No prefixes or emoji. Keep it understandable.`
	case "haiku":
		rule = `Write the summary as a single-line haiku (5-7-5 syllables) describing the change, separating the three parts with " / ". No prefixes or emoji.`
	default:
		rule = ticketRule
		if rule == "" {
			rule = conventional
		}
	}
	if custom := jsTrim(cfg.CustomInstructions); custom != "" {
		return rule + "\nAdditional user rules (highest priority): " + custom
	}
	return rule
}

// explainStyleGuidance is the tone instruction for code explanations.
func explainStyleGuidance(style string) string {
	switch style {
	case "concise":
		return "Be extremely concise: a one-line summary and at most two short bullets."
	case "detailed":
		return "Be thorough: walk through the logic step by step, including edge cases, complexity, and potential bugs."
	case "eli5":
		return "Explain it like I am five: very simple words and everyday analogies, no jargon."
	case "caveman":
		return `Use exaggerated caveman speak: short, broken sentences and ALL CAPS for emphasis (e.g. "CODE TAKE NUMBER. CODE ADD. CODE GIVE BACK."). Stay accurate and understandable.`
	case "pirate":
		return `Speak like a salty pirate, with nautical slang and the odd "Arr". Stay accurate and understandable.`
	case "formal":
		return "Use a formal, academic tone suitable for technical documentation."
	}
	return "Use a clear, friendly, professional tone."
}

// conflictStyleGuidance shapes the output of AI merge-conflict resolution.
func conflictStyleGuidance(style string) string {
	switch style {
	case "commented":
		return `Where you combine or choose between the conflicting sides, add a brief inline comment using the file's comment syntax noting what was done (e.g. "// merged: kept both validations"). Keep comments short and only at resolved spots.`
	case "conservative":
		return "Be conservative: make the smallest possible change. When the two sides cannot be safely combined, prefer keeping BOTH behaviours over dropping either."
	}
	return "Produce a clean result with no extra comments about the merge."
}

func buildCommitSystemPrompt(cfg Config, c CommitContext) string {
	descRule := `- "description": 1-4 short bullet lines explaining the why/what, or empty string for trivial changes.`
	if cfg.GenerateDescription != nil && !*cfg.GenerateDescription {
		descRule = `- "description": always null. Do not write a body; put everything meaningful in the summary.`
	}
	return "You are an expert software engineer writing git commit messages.\n" +
		`Given a staged diff, reply ONLY with a JSON object: {"summary": "...", "description": "..."}.` + "\n" +
		`- "summary": max 72 chars, imperative mood. ` + styleGuidance(cfg, c.Branch) + "\n" +
		descRule + "\n" +
		"No markdown fences, no extra text."
}

// splitFallback reads a rejected reply as plain text: the first line is the
// subject, the rest the body.
func splitFallback(reply string) (string, string) {
	lines := strings.Split(jsTrim(reply), "\n")
	return jsTrim(lines[0]), jsTrim(strings.Join(lines[1:], "\n"))
}

func (c *Client) generateCommitMessage(ctx context.Context, diff string, cfg Config, cc CommitContext) (CommitMessage, error) {
	// Honour the toggle even if the model ignores the instruction and returns
	// a body anyway.
	omitDesc := cfg.GenerateDescription != nil && !*cfg.GenerateDescription
	messages := []ChatMessage{
		{Role: "system", Content: buildCommitSystemPrompt(cfg, cc)},
		{Role: "user", Content: "Branch: " + cc.Branch + "\n\nStaged diff:\n\n" + clip(diff, 16000)},
	}
	v, err := c.ChatCompleteJSON(ctx, cfg, messages, "commitMessage", JSONSpec{
		Name: "commit_message", Schema: commitSchema(), Validate: validateCommitMessage,
	}, 0.2)
	if err != nil {
		var inv *InvalidAIResponse
		if !errors.As(err, &inv) || jsTrim(inv.LastReply) == "" {
			return CommitMessage{}, err
		}
		// Rather than fail the commit outright, fall back to reading the reply
		// as plain text — the first line is the subject, the rest the body.
		summary, body := splitFallback(inv.LastReply)
		if omitDesc {
			body = ""
		}
		return CommitMessage{Summary: summary, Description: body}, nil
	}
	o := asObj(v)
	desc := ""
	if !omitDesc {
		if d, ok := o.V("description").(string); ok {
			desc = jsTrim(d)
		}
	}
	return CommitMessage{Summary: jsTrim(o.V("summary").(string)), Description: desc}, nil
}

// explainCode is a plain-language explanation of a code file or snippet.
func (c *Client) explainCode(ctx context.Context, code, lang string, cfg Config) (string, error) {
	if lang == "" {
		lang = "source"
	}
	system := "You are an expert software engineer explaining code to a colleague.\n" +
		"Explain what the given " + lang + " code does in clear, plain language.\n" +
		"Lead with a one-sentence summary, then short bullet points for the key parts and any\n" +
		"notable side effects, edge cases, or risks. Be concise. Do not restate the code line by\n" +
		"line. Use markdown, but no code fences unless quoting a short identifier.\n" +
		"Tone: " + explainStyleGuidance(cfg.ExplainStyle)
	text, err := c.ChatComplete(ctx, cfg, []ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: clip(code, 16000)},
	}, "explainCode", 0.2, ModelCallOptions{})
	if err != nil {
		return "", err
	}
	return jsTrim(text), nil
}

// HoverExplainRequest is one hovered token and the lines the viewer has — a
// whole file, or the hunks of a diff.
type HoverExplainRequest struct {
	Path  string         `json:"path"`
	Lang  string         `json:"lang"`
	Token string         `json:"token"`
	Line  float64        `json:"line"`
	Lines []NumberedLine `json:"lines"`
}

// HoverExplainResult is the explanation; Lines are the lines it drew on,
// resolved to `path:line` by the caller.
type HoverExplainResult struct {
	Summary   string    `json:"summary"`
	Bullets   []string  `json:"bullets"`
	Lines     []float64 `json:"lines"`
	StartLine float64   `json:"startLine"`
	EndLine   float64   `json:"endLine"`
}

func hoverSchema() *Obj {
	return newObj("type", "object", "additionalProperties", false,
		"required", anyStrings([]string{"summary", "bullets", "lines"}),
		"properties", newObj(
			"summary", strType(),
			"bullets", strArray(),
			"lines", newObj("type", "array", "items", newObj("type", "integer")),
		))
}

// Hovering is cheap to trigger and expensive to answer, so identical asks are
// served from memory. The key covers the window text, so editing the file (or
// switching model) misses and re-asks. Bounded; oldest entry goes first.
const hoverCacheLimit = 200

func (c *Client) rememberHover(key string, value HoverExplainResult) {
	c.hoverMu.Lock()
	defer c.hoverMu.Unlock()
	if _, ok := c.hoverCache[key]; !ok {
		c.hoverOrder = append(c.hoverOrder, key)
	}
	c.hoverCache[key] = value
	if len(c.hoverOrder) > hoverCacheLimit {
		oldest := c.hoverOrder[0]
		c.hoverOrder = c.hoverOrder[1:]
		delete(c.hoverCache, oldest)
	}
}

func cloneHover(r HoverExplainResult) HoverExplainResult {
	r.Bullets = append([]string{}, r.Bullets...)
	r.Lines = append([]float64{}, r.Lines...)
	return r
}

// hoverExplain explains one token, using only a numbered window of the file
// around it. The model cites line numbers from that window and never sees —
// or writes — a file path, so a citation that isn't in front of it is a
// validation error.
func (c *Client) hoverExplain(ctx context.Context, req HoverExplainRequest, cfg Config) (HoverExplainResult, error) {
	window := BuildWindowFromLines(req.Lines, req.Line, WindowOptions{})
	if window.Text == "" {
		return HoverExplainResult{}, &InvalidAIResponse{Message: "There is no code around that token to read."}
	}
	style := cfg.ExplainStyle
	if style == "" {
		style = "normal"
	}
	sum := sha256.Sum256([]byte(jsonStringify([]any{"hover.v1", cfg.Model, style, req.Path, req.Token, req.Line, window.Text})))
	key := hex.EncodeToString(sum[:])
	c.hoverMu.Lock()
	hit, ok := c.hoverCache[key]
	c.hoverMu.Unlock()
	if ok {
		return cloneHover(hit), nil
	}

	lang := req.Lang
	if lang == "" {
		lang = "source"
	}
	system := "You explain a single token of source code to a developer reading the file.\n\n" +
		"You are given a numbered window of a " + lang + " file. Rules:\n" +
		"- Answer only from the window. If it does not show what the token is (it is imported or defined elsewhere), say exactly that in the summary instead of guessing.\n" +
		"- The numbering may skip lines — a diff view shows only the changed hunks. Never assume anything about the gaps.\n" +
		`- Never write file paths. Cite line numbers only from the window, in "lines" — the app turns them into links.` + "\n" +
		`- Be brief: one sentence in "summary", at most 2 short bullets in "bullets". Plain text, no markdown, no code fences.` + "\n" +
		"Tone: " + explainStyleGuidance(cfg.ExplainStyle) + "\n\n" +
		`Reply ONLY with valid JSON: {"summary":"...","bullets":["..."],"lines":[` + jsNumberString(window.StartLine) + `]}`

	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: "Token: " + req.Token + "\nOn line: " + jsNumberString(req.Line) + "\n\nFile window:\n" + window.Text},
	}, "hoverExplain", JSONSpec{
		Name: "hover_explain", Schema: hoverSchema(), Validate: func(v any) []string { return ValidateHoverExplain(v, window) },
	}, 0.2)
	if err != nil {
		return HoverExplainResult{}, err
	}
	o := asObj(v)
	bullets := []string{}
	if list, ok := o.V("bullets").([]any); ok {
		for _, b := range list {
			if s := jsTrim(jsString(b)); s != "" && len(bullets) < 3 {
				bullets = append(bullets, s)
			}
		}
	}
	lines := []float64{}
	if list, ok := o.V("lines").([]any); ok {
		for _, n := range list {
			// Number.isInteger
			if f, isNum := n.(float64); isNum && !math.IsInf(f, 0) && f == math.Trunc(f) {
				lines = append(lines, f)
			}
		}
	}
	result := HoverExplainResult{
		Summary:   jsTrim(o.V("summary").(string)),
		Bullets:   bullets,
		Lines:     lines,
		StartLine: window.StartLine,
		EndLine:   window.EndLine,
	}
	c.rememberHover(key, cloneHover(result))
	return result, nil
}

var (
	leadingFenceRE = regexp.MustCompile("^```[^\n]*\n?")
)

// unfence strips a stray ```lang fence if the model added one despite
// instructions: `/^```[^\n]*\n?/`, `/\n?```\s*$/`, `/\s+$/`.
func unfence(text string) string {
	text = leadingFenceRE.ReplaceAllLiteralString(text, "")
	if t := jsTrimEnd(text); strings.HasSuffix(t, "```") {
		cut := len(t) - 3
		if cut > 0 && text[cut-1] == '\n' {
			cut--
		}
		text = text[:cut]
	}
	return jsTrimEnd(text)
}

// resolveConflict proposes a merged file from raw content containing git
// conflict markers.
func (c *Client) resolveConflict(ctx context.Context, file, content string, cfg Config) (string, error) {
	system := `You are resolving a git merge conflict in "` + file + `".` + "\n" +
		"The input contains conflict markers: <<<<<<< (ours), ======= , >>>>>>> (theirs), and\n" +
		"optionally ||||||| (base). Produce the correct merged file that preserves the intent of\n" +
		"BOTH sides where compatible. Keep all non-conflicting content unchanged.\n" +
		conflictStyleGuidance(cfg.ConflictStyle) + "\n" +
		"Reply with ONLY the full resolved file content. No conflict markers, no markdown fences,\n" +
		"no commentary, no explanations."
	out, err := c.chatCompleteChecked(ctx, cfg, []ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: clip(content, 24000)},
	}, "resolveConflict",
		// A "resolution" that still has conflict markers would be pasted
		// straight into the editor, so it is worth one more try.
		func(text string) []string { return validateResolvedFile(unfence(text)) }, 0.1)
	if err != nil {
		return "", err
	}
	return unfence(out), nil
}

// ArtifactRequest is one config file the wizard asks for.
type ArtifactRequest struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// GeneratedFile is one generated config file.
type GeneratedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// GeneratedFiles is ai:generateConfig's result.
type GeneratedFiles struct {
	Files []GeneratedFile `json:"files"`
}

// ArtifactSuggestion is one suggested extra config file.
type ArtifactSuggestion struct {
	Path        string `json:"path"`
	Description string `json:"description"`
	Reason      string `json:"reason"`
}

// ArtifactSuggestions is ai:suggestArtifacts's result.
type ArtifactSuggestions struct {
	Suggestions []ArtifactSuggestion `json:"suggestions"`
}

func (c *Client) generateProjectConfig(ctx context.Context, repoName string, artifacts []ArtifactRequest, context_ string, cfg Config) (GeneratedFiles, error) {
	system := "You are a developer productivity expert generating AI tool configuration files for a software project.\n" +
		`Reply ONLY with valid JSON: {"files": [{"path": "...", "content": "..."}]}` + "\n" +
		"Rules:\n" +
		`- Paths are relative to the repo root (e.g. "CLAUDE.md", ".cursor/rules/project.mdc")` + "\n" +
		"- Content must be complete and production-ready — no placeholders, no TODOs\n" +
		"- For shell scripts (.git/hooks/*), output executable sh scripts with a proper shebang line\n" +
		"- No markdown fences or commentary outside the JSON object"

	list := make([]string, len(artifacts))
	requested := make([]string, len(artifacts))
	for i, a := range artifacts {
		list[i] = "- " + a.Path + ": " + a.Description
		requested[i] = jsTrim(a.Path)
	}
	desc := ""
	if t := jsTrim(context_); t != "" {
		desc = "\nProject description: " + t
	}
	user := "Project name: " + repoName + desc + "\n\nGenerate these configuration files:\n" + strings.Join(list, "\n")

	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, "generateConfig", JSONSpec{
		Name: "project_config", Schema: configFilesSchema(),
		Validate: func(v any) []string { return validateGeneratedFiles(v, requested) },
	}, 0.3)
	if err != nil {
		return GeneratedFiles{}, err
	}
	files := []GeneratedFile{}
	for _, f := range asObj(v).V("files").([]any) {
		o := asObj(f)
		files = append(files, GeneratedFile{Path: o.V("path").(string), Content: o.V("content").(string)})
	}
	return GeneratedFiles{Files: files}, nil
}

// SmartStageFile is one changed file offered to smart staging.
type SmartStageFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// SmartStageResult is ai:smartStage's result.
type SmartStageResult struct {
	ToStage []string `json:"toStage"`
	Reason  string   `json:"reason"`
}

const smartStageSystem = `You are a git expert deciding which changed files should be staged for a commit.

STAGE these kinds of files:
- Source code changes (.ts, .tsx, .js, .jsx, .py, .go, .rs, .java, .rb, .php, .cs, .cpp, .c, .h, .swift, .kt)
- Tests, documentation, migrations, SQL
- Intentional config changes (tsconfig, vite.config, package.json changes that aren't just lockfile, .eslintrc, etc.)
- Assets, styles, templates that were deliberately edited

DO NOT STAGE:
- Lock files: package-lock.json, yarn.lock, pnpm-lock.yaml, Cargo.lock, poetry.lock, Gemfile.lock, composer.lock
- Build/compile output: dist/, build/, out/, .next/, .nuxt/, __pycache__/, *.pyc, *.class, *.o, *.d.ts in dist
- Environment & secrets: .env, .env.local, .env.production, .env.development, *.pem, *.key, secrets.*
- OS & editor garbage: .DS_Store, Thumbs.db, desktop.ini, .idea/, *.swp, *.swo, *~
- Log files: *.log, npm-debug.log, yarn-error.log
- Coverage & cache: coverage/, .nyc_output/, .cache/, .parcel-cache/

Reply ONLY with valid JSON (no markdown fences):
{"toStage": ["path/to/file.ts", ...], "reason": "one sentence explaining what you staged and what you excluded"}`

func (c *Client) smartStageFiles(ctx context.Context, files []SmartStageFile, cfg Config) (SmartStageResult, error) {
	list := make([]string, len(files))
	known := map[string]bool{}
	for i, f := range files {
		list[i] = f.Status + ": " + f.Path
		known[f.Path] = true
	}
	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: smartStageSystem},
		{Role: "user", Content: "Changed files:\n" + strings.Join(list, "\n")},
	}, "smartStage", JSONSpec{
		Name: "smart_stage", Schema: smartStageSchema(), Validate: func(v any) []string { return validateSmartStage(v, known) },
	}, 0.2)
	if err != nil {
		var inv *InvalidAIResponse
		if !errors.As(err, &inv) {
			return SmartStageResult{}, err
		}
		// Staging nothing is the safe failure: the old fallback staged
		// everything, which is exactly what the user asked the AI to avoid.
		return SmartStageResult{ToStage: []string{}, Reason: "The AI response could not be used, so nothing was selected. Stage manually."}, nil
	}
	o := asObj(v)
	reason, _ := o.V("reason").(string)
	return SmartStageResult{ToStage: stringList(o.V("toStage")), Reason: reason}, nil
}

const suggestArtifactsSystem = `You are a developer productivity expert. Given a project description and the AI tools a developer is using, suggest ADDITIONAL configuration files that would be valuable — beyond what they have already selected.
Reply ONLY with valid JSON: {"suggestions": [{"path": "...", "description": "...", "reason": "..."}]}
Rules:
- path: file path relative to repo root
- description: one sentence — what the file does/contains
- reason: one sentence — why this specific project would benefit from it
- Suggest only files not already in the "already selected" list
- Limit to 6–10 high-value suggestions
- Paths must be real, recognised config file paths for the tools listed
- No markdown fences or commentary outside the JSON`

func (c *Client) suggestArtifacts(ctx context.Context, repoName string, selectedTools []string, context_ string, already []ArtifactRequest, cfg Config) (ArtifactSuggestions, error) {
	alreadyLines := make([]string, len(already))
	alreadyPaths := make([]string, len(already))
	for i, a := range already {
		alreadyLines[i] = "- " + a.Path
		alreadyPaths[i] = jsTrim(a.Path)
	}
	alreadyList := strings.Join(alreadyLines, "\n")
	if alreadyList == "" {
		alreadyList = "(none)"
	}
	desc := ""
	if t := jsTrim(context_); t != "" {
		desc = "Project description: " + t + "\n"
	}
	user := "Project name: " + repoName + "\n" + desc + "Selected tools: " + strings.Join(selectedTools, ", ") +
		"\n\nAlready selected files (do NOT suggest these again):\n" + alreadyList +
		"\n\nSuggest additional configuration files that would be valuable for this project."

	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: suggestArtifactsSystem},
		{Role: "user", Content: user},
	}, "suggestArtifacts", JSONSpec{
		Name: "artifact_suggestions", Schema: artifactSuggestionsSchema(),
		Validate: func(v any) []string { return validateArtifactSuggestions(v, alreadyPaths) },
	}, 0.4)
	if err != nil {
		// Suggestions are a bonus on top of the wizard; an unusable reply just
		// means none to show.
		var inv *InvalidAIResponse
		if !errors.As(err, &inv) {
			return ArtifactSuggestions{}, err
		}
		return ArtifactSuggestions{Suggestions: []ArtifactSuggestion{}}, nil
	}
	out := []ArtifactSuggestion{}
	for _, s := range asObj(v).V("suggestions").([]any) {
		o := asObj(s)
		out = append(out, ArtifactSuggestion{Path: o.V("path").(string), Description: o.V("description").(string), Reason: o.V("reason").(string)})
	}
	return ArtifactSuggestions{Suggestions: out}, nil
}

const appThemeSystem = `You are a UI color palette expert. Generate a complete app color theme with DISTINCT light and dark variants.

Reply ONLY with valid JSON (no markdown fences):
{
  "name": "Theme Name",
  "light": { "bg0":"#hex","bg1":"#hex","bg2":"#hex","bg3":"#hex","bg4":"#hex","border":"#hex","borderSoft":"#hex","text0":"#hex","text1":"#hex","text2":"#hex","accent":"#hex","green":"#hex","red":"#hex","yellow":"#hex","purple":"#hex" },
  "dark":  { "bg0":"#hex","bg1":"#hex","bg2":"#hex","bg3":"#hex","bg4":"#hex","border":"#hex","borderSoft":"#hex","text0":"#hex","text1":"#hex","text2":"#hex","accent":"#hex","green":"#hex","red":"#hex","yellow":"#hex","purple":"#hex" }
}

LIGHT mode rules (bg values must be LIGHT, text must be DARK):
- bg0: the main window background — very light (e.g. #f5f5f5, #ffffff, #f0ebe3). Luminance > 85%.
- bg1–bg4: progressively slightly darker panels/surfaces, still clearly light
- text0: near-black or very dark (e.g. #1a1a1a, #111827). Luminance < 20%.
- text1: medium dark (e.g. #374151). text2: muted (e.g. #6b7280)
- border: subtle light gray (e.g. #e5e7eb). borderSoft: even subtler (e.g. #f0f0f0)

DARK mode rules (bg values must be DARK, text must be LIGHT):
- bg0: the main window background — very dark (e.g. #0f1117, #1a1a2e, #1e1e1e). Luminance < 15%.
- bg1–bg4: progressively slightly lighter dark panels, but still clearly dark
- text0: near-white or very light (e.g. #f0f0f0, #e2e8f0). Luminance > 85%.
- text1: lighter gray (e.g. #94a3b8). text2: muted (e.g. #64748b)
- border: dark gray (e.g. #2d2d2d). borderSoft: subtler dark (e.g. #252525)

Shared rules:
- accent: the theme's signature color (buttons, links, highlights) — keep hue consistent across both modes, adjust lightness
- green/red/yellow/purple: semantic status colors — keep recognizable in both modes
- bg0 in light and bg0 in dark must look COMPLETELY DIFFERENT — one clearly light, one clearly dark
- All values must be valid 6-digit hex colors`

const codeThemeSystem = `You are a syntax highlighting color theme expert. Generate a complete code editor theme with DISTINCT light and dark variants.

Reply ONLY with valid JSON (no markdown fences):
{
  "name": "Theme Name",
  "light": { "bg":"#hex","text":"#hex","comment":"#hex","keyword":"#hex","string":"#hex","number":"#hex","function":"#hex","title":"#hex","variable":"#hex","type":"#hex","builtin":"#hex","attr":"#hex","tag":"#hex","operator":"#hex","meta":"#hex" },
  "dark":  { "bg":"#hex","text":"#hex","comment":"#hex","keyword":"#hex","string":"#hex","number":"#hex","function":"#hex","title":"#hex","variable":"#hex","type":"#hex","builtin":"#hex","attr":"#hex","tag":"#hex","operator":"#hex","meta":"#hex" }
}

LIGHT mode rules (editor background must be LIGHT):
- bg: very light editor background (e.g. #ffffff, #fafafa, #f8f4f0). Luminance > 90%.
- text: near-black default code color (e.g. #1a1a1a, #24292e). High contrast on light bg.
- comment: muted medium tone (e.g. #6a737d, #998866) — readable but de-emphasized
- All token colors must be dark enough to read clearly on the light bg

DARK mode rules (editor background must be DARK):
- bg: very dark editor background (e.g. #1e1e1e, #0d1117, #1a1b26). Luminance < 15%.
- text: light default code color (e.g. #d4d4d4, #abb2bf). High contrast on dark bg.
- comment: muted mid-tone (e.g. #6a737d, #5c6370) — readable but de-emphasized
- All token colors must be light enough to read clearly on the dark bg

Shared rules:
- bg in light and bg in dark must look COMPLETELY DIFFERENT — one clearly light, one clearly dark
- Each token type should use a distinct hue to maximize visual differentiation
- Keep keyword/string/function hues thematically consistent with the prompt's color palette
- All values must be valid 6-digit hex colors`

const graphPaletteSystem = `You are a data-visualization color expert. Generate a palette of branch-lane colors for a git commit graph.

Reply ONLY with valid JSON (no markdown fences):
{
  "name": "Palette Name",
  "colors": ["#hex","#hex","#hex","#hex","#hex","#hex","#hex","#hex"]
}

Rules:
- Exactly 8 colors, all valid 6-digit hex.
- These are LANE colors drawn as thin lines/dots over BOTH light and dark app backgrounds, so pick mid-to-vivid tones that stay legible on either — avoid near-white, near-black, and very pale pastels unless the prompt explicitly asks for them.
- Adjacent colors in the array must be clearly distinguishable from each other (different hue or strong lightness gap) — they often sit side by side.
- Keep the set harmonious and on-theme for the prompt.`

// Theme is a generated theme: a name and a light and dark colour map keyed by
// AppThemeKeys or CodeThemeKeys. Keys the model added beyond those are
// dropped (TS passed the model's object through whole).
type Theme struct {
	Name  string            `json:"name"`
	Light map[string]string `json:"light"`
	Dark  map[string]string `json:"dark"`
}

// GraphPalette is a generated set of 8 lane colours.
type GraphPalette struct {
	Name   string   `json:"name"`
	Colors []string `json:"colors"`
}

func themeColors(v any, keys []string) map[string]string {
	o := asObj(v)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = jsString(o.V(k))
	}
	return out
}

func (c *Client) generateTheme(ctx context.Context, prompt string, cfg Config, system, feature, name string, keys []string) (Theme, error) {
	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: "Theme description: " + prompt},
	}, feature, JSONSpec{
		Name: name, Schema: themeSchema(keys), Validate: func(v any) []string { return validateTheme(v, keys) },
	}, 0.7)
	if err != nil {
		return Theme{}, err
	}
	o := asObj(v)
	return Theme{Name: o.V("name").(string), Light: themeColors(o.V("light"), keys), Dark: themeColors(o.V("dark"), keys)}, nil
}

func (c *Client) generateGraphPalette(ctx context.Context, prompt string, cfg Config) (GraphPalette, error) {
	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: graphPaletteSystem},
		{Role: "user", Content: "Palette description: " + prompt},
	}, "generateGraphPalette", JSONSpec{
		Name: "graph_palette", Schema: graphPaletteSchema(), Validate: validateGraphPalette,
	}, 0.7)
	if err != nil {
		return GraphPalette{}, err
	}
	o := asObj(v)
	colors := []string{}
	for _, x := range o.V("colors").([]any) {
		colors = append(colors, jsString(x))
	}
	return GraphPalette{Name: o.V("name").(string), Colors: colors}, nil
}

var nonSlugRE = regexp.MustCompile(`[^a-z0-9]`)

func branchStyleGuidance(style, username string) string {
	name := nonSlugRE.ReplaceAllLiteralString(strings.ToLower(strings.Split(username, " ")[0]), "")
	if name == "" {
		name = "dev"
	}
	switch style {
	case "prefix/ticket-description":
		return `Use the format "prefix/TICKET-slug". Extract ticket/issue numbers (e.g. CMS-123, JIRA-456) if present. Pick prefix from: feature, fix, chore, docs, test, refactor, hotfix. If no ticket found, fall back to "prefix/slug". Example: feature/CMS-123-add-login-form`
	case "username/prefix/description":
		return `Use the format "` + name + `/prefix/slug". Username is always "` + name + `". Pick prefix from: feature, fix, chore, docs, test, refactor, hotfix. Example: ` + name + `/feature/add-login-form`
	case "plain":
		return `Plain slug only, no prefix, no slashes. Example: add-login-form`
	}
	return `Use the format "prefix/slug". Pick prefix from: feature, fix, chore, docs, test, refactor, hotfix. Example: feature/add-login-form`
}

// BranchNameContext is ai:generateBranchName's third argument.
type BranchNameContext struct {
	Username string `json:"username,omitempty"`
}

func (c *Client) generateBranchName(ctx context.Context, description string, cfg Config, bc BranchNameContext) (string, error) {
	system := "You are a git branch naming expert.\n" +
		"Given a description of work, generate a short, valid branch name.\n" +
		"Convention: " + branchStyleGuidance(cfg.BranchNamingStyle, bc.Username) + "\n" +
		"Rules:\n" +
		"- Lowercase only, hyphens instead of spaces or special chars\n" +
		"- Keep slug concise (3–6 words max after the prefix)\n" +
		`- The name must be a valid git ref: no spaces, no "..", no trailing "/" or ".lock".` + "\n" +
		`Reply ONLY with valid JSON: {"name": "..."}. No markdown fences.`
	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: description},
	}, "generateBranchName", JSONSpec{Name: "branch_name", Schema: branchNameSchema(), Validate: validateBranchName}, 0.3)
	if err != nil {
		return "", err
	}
	return jsTrim(asObj(v).V("name").(string)), nil
}

func reviewSchema() *Obj {
	finding := newObj("type", "object", "additionalProperties", false,
		"required", anyStrings([]string{"kind", "severity", "evidenceId", "claim", "suggestion"}),
		"properties", newObj(
			"kind", newObj("type", "string", "enum", anyStrings([]string{"risk", "suggestion"})),
			"severity", newObj("type", "string", "enum", anyStrings([]string{"high", "medium", "low"})),
			"evidenceId", strType(),
			"claim", strType(),
			"suggestion", strType(),
		))
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"summary", "findings"}),
		"properties", newObj("summary", strType(), "findings", newObj("type", "array", "items", finding)))
}

// PRReviewResult mirrors PRReviewResult in src/shared/types.ts.
type PRReviewResult struct {
	Summary string `json:"summary"`
	// Findings rendered as markdown, kept for plain-text consumers.
	Risks       string            `json:"risks"`
	Suggestions string            `json:"suggestions"`
	Findings    []GroundedFinding `json:"findings"`
}

const reviewSystem = `You are an expert software engineer reviewing a pull request.

You are given the changed hunks of a git diff. Each hunk is labelled with an EvidenceID like [E3].

Rules:
- Ground every finding in exactly one hunk and cite it with "evidenceId".
- Only cite EvidenceIDs from the list. Never invent one.
- Never write file paths, line numbers or code excerpts — the app resolves those from the EvidenceID you cite.
- If a hunk does not actually show the problem, omit the finding rather than guess.
- At most 8 findings, most important first. Return an empty array when the change looks fine.

Reply ONLY with valid JSON (no markdown fences):
{"summary":"2-4 sentences on what this PR does and its overall quality","findings":[{"kind":"risk","severity":"high","evidenceId":"E1","claim":"one sentence describing the problem","suggestion":"one sentence with the fix, or an empty string"}]}
- "kind": "risk" for bugs, security, performance or breaking changes; "suggestion" for improvements.
- "severity": "high", "medium" or "low".`

// reviewPR reviews a diff hunk by hunk. The model only ever cites
// EvidenceIDs; the real paths and line numbers are resolved here, so it cannot
// invent a location.
func (c *Client) reviewPR(ctx context.Context, diff string, cfg Config) (PRReviewResult, error) {
	evidence := BuildDiffEvidence(diff, EvidenceOptions{})
	if len(evidence.Items) == 0 {
		return PRReviewResult{Summary: "No reviewable code changes found in this diff.", Findings: []GroundedFinding{}}, nil
	}
	index := EvidenceIndex(evidence)
	allowed := evidenceIDs(evidence)
	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: reviewSystem},
		{Role: "user", Content: "Changed hunks:\n\n" + SerializeEvidence(evidence)},
	}, "reviewPR", JSONSpec{
		Name: "pr_review", Schema: reviewSchema(), Validate: func(v any) []string { return ValidateReview(v, allowed) },
	}, 0.2)
	if err != nil {
		return PRReviewResult{}, err
	}
	o := asObj(v)
	raw, _ := o.V("findings").([]any)
	findings := GroundFindings(raw, index)
	return PRReviewResult{
		Summary:     jsTrim(o.V("summary").(string)),
		Risks:       RenderFindings(findings, "risk"),
		Suggestions: RenderFindings(findings, "suggestion"),
		Findings:    findings,
	}, nil
}

// PRDescriptionResult is a drafted PR title and Markdown body.
type PRDescriptionResult struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

const prDescriptionSystem = `You write clear, concise pull request descriptions.
Given a branch's commit subjects and its diff, return a JSON object:
- "title": a single-line PR title (imperative, no trailing period, ≤ 70 chars).
- "body": GitHub-flavored Markdown — a short summary paragraph, then a "## Changes" bullet list of the notable changes, and a "## Notes" section only if useful.
Reply ONLY with valid JSON: {"title": "...", "body": "..."}. No markdown fences.`

// prDescription drafts a PR title + Markdown body from a branch's commit
// subjects and diff.
func (c *Client) prDescription(ctx context.Context, commits, diff string, cfg Config) (PRDescriptionResult, error) {
	user := "Commit subjects:\n" + clip(commits, 4000) + "\n\nDiff:\n" + clip(diff, 20000)
	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: prDescriptionSystem},
		{Role: "user", Content: user},
	}, "prDescription", JSONSpec{Name: "pr_description", Schema: prDescriptionSchema(), Validate: validatePRDescription}, 0.3)
	if err != nil {
		var inv *InvalidAIResponse
		if !errors.As(err, &inv) || jsTrim(inv.LastReply) == "" {
			return PRDescriptionResult{}, err
		}
		// Fall back to the first line as title, rest as body.
		title, body := splitFallback(inv.LastReply)
		return PRDescriptionResult{Title: title, Body: body}, nil
	}
	o := asObj(v)
	return PRDescriptionResult{Title: jsTrim(o.V("title").(string)), Body: jsTrim(o.V("body").(string))}, nil
}

// StatusFile is the part of a status entry the planner reads.
type StatusFile struct {
	Path string `json:"path"`
}

// RepoStatus is the part of RepoStatus the planner reads.
type RepoStatus struct {
	Current    string       `json:"current"`
	Staged     []StatusFile `json:"staged"`
	Unstaged   []StatusFile `json:"unstaged"`
	Conflicted []StatusFile `json:"conflicted"`
}

// AskPlan mirrors AskPlan. Actions pass through as the model wrote them (the
// AskAction union, validated against the working tree); Note likewise.
type AskPlan struct {
	Summary string            `json:"summary"`
	Actions []json.RawMessage `json:"actions"`
	Note    json.RawMessage   `json:"note,omitempty"`
}

// askPlanSchema interprets a free-form instruction ("ignore all *.tsx files",
// "commit the unstaged .md files") against the working tree: the model
// resolves globs/intents to literal paths and patterns, the renderer applies
// the actions.
func askPlanSchema() *Obj {
	return newObj("type", "object", "required", anyStrings([]string{"summary", "actions"}),
		"properties", newObj("summary", strType(), "note", strType(), "actions", askActionsSchema()))
}

const planSystem = `You translate a user's plain-language git instruction into a concrete plan of actions for the Gitcito desktop app to execute.

You are given the repository's current working-tree state. Resolve any globs or descriptions (e.g. "*.tsx", "the markdown files", "everything unstaged") to LITERAL repo-relative file paths drawn ONLY from the lists provided. Never invent paths that aren't listed (except .gitignore patterns, which are literal glob strings the user wants ignored).

Reply ONLY with valid JSON (no markdown fences) matching:
{
  "summary": "one short sentence describing the plan",
  "actions": [ ...zero or more actions... ],
  "note": "optional — set only when you cannot fulfill the request; then actions must be []"
}

Each action is one of:
- {"type":"gitignore","patterns":["*.tsx"],"description":"Ignore all .tsx files"}
- {"type":"stage","files":["a.ts"],"description":"Stage a.ts"}
- {"type":"unstage","files":["a.ts"],"description":"Unstage a.ts"}
- {"type":"commit","message":"...","files":["README.md"],"description":"Commit the .md files"}
- {"type":"stash","files":["a.ts"],"message":"optional label","description":"Stash a.ts"}
- {"type":"discard","files":["a.ts"],"description":"Discard uncommitted changes to a.ts"}
- {"type":"branch","name":"feature/x","at":"main","checkout":true,"description":"Create branch feature/x"}
- {"type":"checkout","ref":"main","description":"Switch to main"}
- {"type":"tag","name":"v1.2.0","message":"optional annotation","description":"Tag the current commit v1.2.0"}

Rules:
- For a commit, set "files" to the paths to include; they will be staged before committing. Omit "files" to commit what is already staged.
- For a stash, set "files" to the specific paths to stash; omit "files" to stash all changes. "message" is an optional label.
- "message" (commit) must be a concise, conventional commit message.
- "discard" permanently throws away uncommitted changes to the listed files — only use it when the user clearly asks to discard / revert / throw away local changes.
- For "branch", "at" is the start point (default: current branch); "checkout" switches to it after creating (default true).
- ONLY use the action types listed above. If the instruction needs anything else (push, pull, fetch, reset, rebase, revert, merge, delete a branch, force operations, etc.), return actions: [] and explain in "note" that it must be done from the dedicated UI.
- If nothing matches (e.g. the user asks to commit .md files but none exist), return actions: [] and explain in "note".
- Keep the plan minimal — only the actions needed to satisfy the instruction.`

func (c *Client) planRepoActions(ctx context.Context, prompt string, status RepoStatus, cfg Config) (AskPlan, error) {
	list := func(files []StatusFile) string {
		p := make([]string, len(files))
		for i, f := range files {
			p[i] = f.Path
		}
		if s := strings.Join(p, "\n"); s != "" {
			return s
		}
		return "(none)"
	}
	stateBlock := "Current branch: " + status.Current + "\nStaged files:\n" + list(status.Staged) +
		"\nUnstaged/untracked files:\n" + list(status.Unstaged) + "\nConflicted files:\n" + list(status.Conflicted)
	known := map[string]bool{}
	for _, group := range [][]StatusFile{status.Staged, status.Unstaged, status.Conflicted} {
		for _, f := range group {
			known[f.Path] = true
		}
	}
	strict := false
	v, err := c.ChatCompleteJSON(ctx, cfg, []ChatMessage{
		{Role: "system", Content: planSystem},
		{Role: "user", Content: stateBlock + "\n\nInstruction: " + clip(prompt, 4000)},
	}, "planActions", JSONSpec{
		Name: "ask_plan", Schema: askPlanSchema(), Validate: func(v any) []string { return ValidateAskPlan(v, known) }, Strict: &strict,
	}, 0.1)
	if err != nil {
		// A malformed plan is reported in the panel; transport errors still fail.
		var inv *InvalidAIResponse
		if !errors.As(err, &inv) {
			return AskPlan{}, err
		}
		return AskPlan{Summary: "", Actions: []json.RawMessage{}, Note: json.RawMessage(jsonStringify(inv.Message))}, nil
	}
	o := asObj(v)
	actions := []json.RawMessage{}
	for _, a := range o.V("actions").([]any) {
		actions = append(actions, json.RawMessage(jsonStringify(a)))
	}
	plan := AskPlan{Summary: o.V("summary").(string), Actions: actions}
	if note, has := o.Get("note"); has {
		plan.Note = json.RawMessage(jsonStringify(note))
	}
	return plan, nil
}
