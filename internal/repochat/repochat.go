// Package repochat is src/main/repoChat.ts: repository chat — a grounded
// question/answer loop over the selected repository, which may propose (never
// run) file and Git actions, plus the factual report after the renderer ran
// them. Prompt wording, schemas and validation messages are product behaviour
// and match the TypeScript byte for byte (see testdata/payloads.json).
package repochat

//lint:file-ignore ST1005 error strings are shown to the user verbatim and match the TypeScript byte for byte

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitsvc"
	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/GabrySpada/gitcito/internal/wiki"
)

// Limits, exported in TS for tests and the renderer's expectations.
const (
	MaxMessages    = 12
	MaxPaths       = 8
	MaxSearches    = 5
	MaxSearchPaths = 48
	ContextBytes   = 32_000
	MaxAttachments = 8
	// MaxEvidenceRounds is how many extra evidence rounds an answer may ask
	// for before it must answer with what it has. One selection pass guesses;
	// a second look at what arrived is where "the caller is in the other file"
	// gets resolved. Bounded, because each round is another model call the
	// user waits for.
	MaxEvidenceRounds = 2
	// PinnedBytes is how much of the budget pinned context may take before the
	// model's picks.
	PinnedBytes = 20_000
)

const (
	// maxExternalBytes: a file dragged in from outside the repository is read
	// whole, up to this.
	maxExternalBytes     = 512_000
	maxMessageChars      = 8_000
	maxHistoryChars      = 16_000
	maxPathListChars     = 16_000
	maxFileChars         = 6_000
	maxSearchHitsPerFile = 2
)

var reGeneratedPath = regexp.MustCompile(`(?i)(^|/)(generated|gen)(/|$)|\.generated\.[^/]+$`)

func ustr(s string) int { return utf16Len(s) }

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func trim(s string) string { return shared.JSTrim(s) }

// ─── Schemas ────────────────────────────────────────────────────────────────

// ActionSchemas is the part of src/main/aiSchemas.ts repository chat composes
// into its answer schema; internal/ai owns those schemas. Each method returns
// the schema exactly as JSON.stringify wrote it.
type ActionSchemas interface {
	// AskActionsSchema is askActionsSchema(remote).
	AskActionsSchema(remote bool) json.RawMessage
	// RepoChatActionsSchema is repoChatActionsSchema(remote).
	RepoChatActionsSchema(remote bool) json.RawMessage
}

func arrayOf(maxItems int) g.Obj {
	return g.Obj{{K: "type", V: "array"}, {K: "maxItems", V: maxItems}, {K: "items", V: g.Obj{{K: "type", V: "string"}}}}
}

var chatSelectionSchema = g.Raw(g.Obj{
	{K: "type", V: "object"},
	{K: "additionalProperties", V: false},
	{K: "required", V: []string{"paths", "searches"}},
	{K: "properties", V: g.Obj{
		{K: "paths", V: arrayOf(MaxPaths)},
		{K: "searches", V: arrayOf(MaxSearches)},
	}},
})

// orderedRaw splits a raw JSON object into its properties, in order, with each
// value's bytes untouched — so `{ ...schema, key }` can be rebuilt exactly.
func orderedRaw(raw json.RawMessage) (g.Obj, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("schema is not a JSON object")
	}
	out := g.Obj{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = out.With(tok.(string), v)
	}
	return out, nil
}

// ChatAnswerSchema is chatAnswerSchema: the answer contract — `actions` exists
// only when the chat-actions setting allows proposals, so a disabled surface
// cannot even be described. schemas may be nil when allowActions is false.
func ChatAnswerSchema(schemas ActionSchemas, allowActions, allowFileActions, allowRemoteActions, allowNeedMore bool) json.RawMessage {
	props := g.Obj{
		{K: "content", V: g.Obj{{K: "type", V: "string"}}},
		{K: "sourceIds", V: g.Obj{{K: "type", V: "array"}, {K: "items", V: g.Obj{{K: "type", V: "string"}}}}},
	}
	if allowNeedMore {
		props = append(props, g.KV{K: "needMore", V: g.Obj{
			{K: "type", V: "object"},
			{K: "additionalProperties", V: false},
			{K: "properties", V: g.Obj{
				{K: "paths", V: arrayOf(MaxPaths)},
				{K: "searches", V: arrayOf(MaxSearches)},
				{K: "commits", V: arrayOf(4)},
				{K: "reason", V: g.Obj{{K: "type", V: "string"}}},
			}},
		}})
	}
	if allowActions {
		var actions any
		if allowFileActions {
			actions = schemas.RepoChatActionsSchema(allowRemoteActions)
		} else {
			ask, err := orderedRaw(schemas.AskActionsSchema(allowRemoteActions))
			if err != nil {
				ask = g.Obj{}
			}
			actions = ask.With("maxItems", 12)
		}
		props = append(props, g.KV{K: "actions", V: actions})
	}
	return g.Raw(g.Obj{
		{K: "type", V: "object"},
		{K: "additionalProperties", V: false},
		{K: "required", V: []string{"content", "sourceIds"}},
		{K: "properties", V: props},
	})
}

// ─── Prompt rules ───────────────────────────────────────────────────────────

const bt = "`"

// repoConfigRule is the shape of `.gitcito.json`, for the surface that can
// write one.
//
// Repository config is the one file in a repository whose schema Gitcito owns,
// and "set this up for me" is a reasonable thing to ask an assistant that can
// already write files. Without the shape it invents plausible keys the loader
// then rejects, so the shape is supplied rather than guessed.
const repoConfigRule = `- ` + bt + `.gitcito.json` + bt + ` is this app's own per-repository config, and you may create or edit it with a file action. Its shape (every field optional except "version", which is 1):
  {"version":1,"protect":["main","release/*"],"links":{"tickets":[{"match":"([A-Z]+-\\d+)","url":"https://tracker/browse/$1","label":"Jira"}]},"commit":{"scopes":["ui","api"],"ticketFromBranch":true,"trailers":["Refs: {ticket}"]},"requires":{"node":">=20","submodules":true,"lfs":false,"hooksPath":".husky","files":[{"path":".env","from":".env.example","why":"local secrets"}]},"checklist":{"push":["Run the tests"]}}
- Use only those keys — an unknown key is rejected by the loader, not ignored. "match" is a JavaScript regular expression source. Read the existing file first when one is present, and edit it rather than replacing it.`

// ActionRules is repoChatActionRules: the system-prompt lines describing which
// proposals the model may make.
func ActionRules(actionsEnabled, fileActionsEnabled, remoteActionsEnabled bool) string {
	if !actionsEnabled {
		return "- Do not propose that you executed, edited, staged, committed, or otherwise changed anything."
	}

	fileRules := "- File creation, editing, replacement, and deletion are disabled by file read-only mode. Git actions remain available."
	if fileActionsEnabled {
		fileRules = `  {"type":"edit_file","path":"LICENSE","oldText":"exact text from evidence","newText":"replacement","description":"…"} (set "replaceAll":true only when every exact occurrence should change)
  {"type":"write_file","path":"new.txt","content":"complete content","mode":"create","description":"…"}
  {"type":"write_file","path":"README.md","content":"complete content","mode":"replace","description":"…"} (replace only evidence explicitly marked complete file)
  {"type":"delete_file","path":"obsolete.txt","description":"…"}
- Put every file action before every Git action. Existing file targets must be literal repo-relative paths from evidence; use create only for a genuinely new path. Prefer exact edit_file over whole-file replacement.
` + repoConfigRule
	}

	remoteRules := "- Fetching, pulling, pushing, opening pull requests and submitting a stack are disabled by settings. Local actions remain available."
	if remoteActionsEnabled {
		remoteRules = `  {"type":"fetch","remote":"origin","description":"…"} / {"type":"pull","mode":"rebase","description":"…"}
  {"type":"push","branch":"feature/x","remote":"origin","description":"…"} (never force; a rejected push must be reconciled from the UI)
  {"type":"open_pr","title":"…","body":"optional Markdown","source":"feature/x","target":"main","draft":false,"description":"…"}
  {"type":"stack_submit","leaf":"top-branch","description":"…"} (GitHub only — pushes every level of the stack and opens or retargets one pull request per level)
- A remote action publishes work outside this machine. Propose one only when the user asked for it in this turn, and name the remote and branch in "description".`
	}

	return `- When the user asks for a repository change, propose it in "actions" — never claim you already did anything. Each action is one of:
` + fileRules + `
  {"type":"gitignore","patterns":["*.log"],"description":"…"}
  {"type":"stage","files":["a.ts"],"description":"…"} / {"type":"unstage","files":["a.ts"],"description":"…"}
  {"type":"commit","message":"…","files":["a.ts"],"description":"…"} (omit "files" to commit what is staged)
  {"type":"stash","files":["a.ts"],"message":"optional","description":"…"} (omit "files" to stash everything)
  {"type":"discard","files":["a.ts"],"description":"…"} (only when the user clearly asks to throw changes away)
  {"type":"branch","name":"feature/x","at":"main","checkout":true,"description":"…"}
  {"type":"checkout","ref":"main","description":"…"} / {"type":"tag","name":"v1.0.0","message":"optional","description":"…"}
  {"type":"merge","ref":"feature/x","noFf":false,"description":"…"} / {"type":"rebase","onto":"main","description":"…"}
  {"type":"revert","hashes":["abc1234"],"description":"…"} / {"type":"cherry_pick","hashes":["abc1234"],"description":"…"}
` + remoteRules + `
- Every "files" entry must be a LITERAL repo-relative path copied from the working-tree state above; resolve globs and descriptions yourself. Never invent paths.
- Every "hashes" entry must be a commit hash copied from the history above, newest-first order for a cherry-pick.
- A merge or rebase can stop on a conflict; say so when you propose one, and never propose one while the repository is already conflicted.
- Anything outside that list (reset, filter-branch, deleting branches, force pushing, any force operation) cannot be proposed — say it must be done from the dedicated UI, with an empty "actions".
- Proposals only run after the app's configured approval check; "content" must describe the proposal, not a result. Put executable actions only in the top-level "actions" field. "content" is never empty — always include at least one short sentence alongside any actions. Omit "actions" for pure questions.`
}

// ─── Action payloads hidden in content ──────────────────────────────────────

// actionTypes is REPO_CHAT_ACTION_TYPES, in its TS order (the JSON-like
// detector builds an alternation from it).
var actionTypes = []string{
	"edit_file", "write_file", "delete_file", "gitignore", "stage", "unstage", "commit", "stash", "discard",
	"branch", "checkout", "tag", "merge", "rebase", "revert", "cherry_pick", "fetch", "pull", "push", "open_pr",
	"stack_submit",
}

func isActionType(s string) bool {
	for _, t := range actionTypes {
		if t == s {
			return true
		}
	}
	return false
}

func parsedValueContainsAction(value any) bool {
	if list, ok := value.([]any); ok {
		for _, v := range list {
			if parsedValueContainsAction(v) {
				return true
			}
		}
		return false
	}
	item, ok := g.AsObject(value)
	if !ok {
		return false
	}
	t, isStr := item["type"].(string)
	return isStr && isActionType(t)
}

func isFileAction(value any) bool {
	item, ok := g.AsObject(value)
	if !ok {
		return false
	}
	t := item["type"]
	return t == "edit_file" || t == "write_file" || t == "delete_file"
}

var (
	reStartsContainer = regexp.MustCompile(`^[\[{]`)
	// /```(?:json)?(?=\s|$)/i — the lookahead only decides whether there is a
	// match, so consuming it is equivalent for a test.
	reFenceOpen  = regexp.MustCompile(`(?i)` + "```" + `(?:json)?(?:` + shared.JSSpace + `|$)`)
	reTypeField  = regexp.MustCompile(`"type"` + shared.JSSpace + `*:` + shared.JSSpace + `*"(?:` + strings.Join(actionTypes, "|") + `)"`)
	reDescField  = regexp.MustCompile(`"description"` + shared.JSSpace + `*:`)
	reJSONFences = regexp.MustCompile(`(?i)` + "```" + `(?:json)?` + shared.JSSpace + `*\n?((?s:.*?))\n?` + "```")
)

// contentContainsJSONLikeAction catches action-shaped JSON that cannot be
// parsed because its text embeds a Markdown fence.
func contentContainsJSONLikeAction(content string) bool {
	if !reStartsContainer.MatchString(trim(content)) && !reFenceOpen.MatchString(content) {
		return false
	}
	// Any type field followed somewhere by a description: the first match has
	// the longest remainder, so it decides.
	loc := reTypeField.FindStringIndex(content)
	return loc != nil && reDescField.MatchString(content[loc[1]:])
}

// ContentContainsActionPayload detects action JSON placed in Markdown instead
// of the validated actions field.
func ContentContainsActionPayload(content string) bool {
	var candidates []string
	if trimmed := trim(content); reStartsContainer.MatchString(trimmed) {
		candidates = append(candidates, trimmed)
	}
	for _, m := range reJSONFences.FindAllStringSubmatch(content, -1) {
		candidates = append(candidates, trim(m[1]))
	}
	for _, c := range candidates {
		if v, err := shared.DecodeJSON([]byte(c)); err == nil && parsedValueContainsAction(v) {
			return true
		}
	}
	return contentContainsJSONLikeAction(content)
}

// ─── Search evidence ────────────────────────────────────────────────────────

// SelectSearchEvidence keeps broad search evidence fair: one hit per file
// before any second hit.
func SelectSearchEvidence(hits []gitsvc.CodeSearchHit) []gitsvc.CodeSearchHit {
	var order []string
	groups := map[string][]gitsvc.CodeSearchHit{}
	for _, hit := range hits {
		if _, ok := groups[hit.File]; !ok {
			if len(order) >= MaxSearchPaths {
				continue
			}
			order = append(order, hit.File)
		}
		groups[hit.File] = append(groups[hit.File], hit)
	}
	rounds := 0
	for _, f := range order {
		rounds = max(rounds, len(groups[f]))
	}
	selected := []gitsvc.CodeSearchHit{}
	for round := 0; round < rounds; round++ {
		for _, f := range order {
			if round < len(groups[f]) {
				selected = append(selected, groups[f][round])
			}
		}
	}
	return selected
}

// ─── Transcript and attachments ─────────────────────────────────────────────

// Message is RepoChatMessage as the main process reads it.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// NormalizeMessages normalizes the untrusted IPC transcript and keeps only
// the bounded tail.
func NormalizeMessages(value any, requireFinalUser bool) ([]Message, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, errors.New("Invalid repository chat transcript.")
	}
	if len(list) > MaxMessages {
		list = list[len(list)-MaxMessages:]
	}
	messages := []Message{}
	for _, raw := range list {
		item, ok := g.AsObject(raw)
		role, _ := item["role"].(string)
		content, isStr := item["content"].(string)
		if !ok || (role != "user" && role != "assistant") || !isStr {
			return nil, errors.New("Invalid repository chat message.")
		}
		if c := g.SliceJS(trim(content), maxMessageChars); c != "" {
			messages = append(messages, Message{Role: role, Content: c})
		}
	}
	if len(messages) == 0 || (requireFinalUser && messages[len(messages)-1].Role != "user") {
		return nil, errors.New("A repository chat request must end with a user message.")
	}
	return messages, nil
}

// ExecutionResult is the part of RepoChatExecutionResult the finalization
// prompt reads.
type ExecutionResult struct {
	Applied     int
	FailedIndex *int
	FailedType  string
	ErrorCode   *string
	Remaining   int
}

// jsInteger is Number.isInteger for a decoded JSON value.
func jsInteger(v any) (int, bool) {
	var f float64
	switch x := v.(type) {
	case json.Number:
		n, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			return 0, false
		}
		f = n
	case float64:
		f = x
	case int:
		return x, true
	default:
		return 0, false
	}
	if math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

func normalizeExecutionResult(value any) (ExecutionResult, error) {
	bad := errors.New("Invalid repository chat execution result.")
	root, ok := g.AsObject(value)
	if !ok {
		return ExecutionResult{}, bad
	}
	applied, ok := jsInteger(root["applied"])
	if !ok || applied < 0 {
		return ExecutionResult{}, bad
	}
	remaining, ok := jsInteger(root["remaining"])
	results, isArr := root["actionResults"].([]any)
	if !ok || remaining < 0 || !isArr {
		return ExecutionResult{}, bad
	}
	for _, raw := range results {
		item, ok := g.AsObject(raw)
		index, isInt := jsInteger(item["index"])
		typ, isStr := item["type"].(string)
		status := item["status"]
		if !ok || !isInt || index < 0 || !isStr || !isActionType(typ) ||
			(status != "done" && status != "failed" && status != "skipped") {
			return ExecutionResult{}, errors.New("Invalid repository chat action result.")
		}
	}
	errObj, errIsObj := g.AsObject(root["error"])
	if _, present := root["error"]; present {
		if _, isStr := errObj["code"].(string); !errIsObj || !isStr {
			return ExecutionResult{}, errors.New("Invalid repository chat execution error.")
		}
	}
	out := ExecutionResult{Applied: applied, Remaining: remaining}
	if n, ok := jsInteger(root["failedIndex"]); ok {
		out.FailedIndex = &n
	}
	if t, ok := root["failedType"].(string); ok && isActionType(t) {
		out.FailedType = t
	}
	if errIsObj {
		code := errObj["code"].(string)
		out.ErrorCode = &code
	}
	return out, nil
}

// Attachment is RepoChatAttachment: a file, a commit, or a file from outside
// the repository that the user pinned to a turn.
type Attachment struct {
	Kind string `json:"kind"` // 'file' | 'commit' | 'external'
	Path string `json:"path,omitempty"`
	Hash string `json:"hash,omitempty"`
}

// AttachmentLabel is how a pinned item reads in the panel, and in the
// "skipped" notice.
func AttachmentLabel(item Attachment) string {
	if item.Kind == "commit" {
		return g.SliceJS(item.Hash, 7)
	}
	return item.Path
}

var (
	reLineBreak  = regexp.MustCompile(`[\r\n]`)
	reLineOrNul  = regexp.MustCompile("[\r\n\x00]")
	reCommitHash = regexp.MustCompile(`^[0-9a-fA-F]{4,40}$`)
)

// NormalizeAttachments normalizes the pinned-context list arriving over IPC.
// Unlike the model's selection this may name any file on disk — the user
// pointed at it — but the shape is still untrusted, and a path that actually
// lives inside the repository is rewritten as a repo file so the ignore and
// secret rules apply.
func NormalizeAttachments(value any, repoPath string) ([]Attachment, error) {
	out := []Attachment{}
	if value == nil {
		return out, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, errors.New("Invalid repository chat context.")
	}
	if len(list) > MaxAttachments {
		list = list[:MaxAttachments]
	}
	seen := map[string]bool{}
	for _, raw := range list {
		item, _ := g.AsObject(raw)
		path, _ := item["path"].(string)
		hash, _ := item["hash"].(string)
		var next *Attachment
		switch {
		case item["kind"] == "file" && shared.IsSafeRepoPath(path) && !reLineBreak.MatchString(path):
			next = &Attachment{Kind: "file", Path: path}
		case item["kind"] == "commit" && reCommitHash.MatchString(hash):
			next = &Attachment{Kind: "commit", Hash: strings.ToLower(hash)}
		case item["kind"] == "external" && filepath.IsAbs(path) && !reLineOrNul.MatchString(path):
			resolved := filepath.Clean(path)
			inside := ""
			if repoPath != "" {
				if rel, err := filepath.Rel(repoPath, resolved); err == nil && rel != "." {
					inside = rel
				}
			}
			if inside != "" && !strings.HasPrefix(inside, "..") && !filepath.IsAbs(inside) {
				next = &Attachment{Kind: "file", Path: strings.ReplaceAll(inside, `\`, "/")}
			} else {
				next = &Attachment{Kind: "external", Path: resolved}
			}
		}
		if next == nil {
			return nil, errors.New("Invalid repository chat context item.")
		}
		key := next.Kind + "\x00" + AttachmentLabel(*next)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, *next)
	}
	return out, nil
}

// FilterPaths keeps the candidate paths a chat may read. Candidates are always
// derived from Git, never from the model.
func FilterPaths(paths []string, ignored map[string]bool) []string {
	out := []string{}
	for _, p := range paths {
		if shared.IsSafeRepoPath(p) && !reLineBreak.MatchString(p) && !ignored[p] && !shared.IsSecretFile(p) &&
			!reGeneratedPath.MatchString(p) && wiki.IsReadableSource(p) {
			out = append(out, p)
		}
	}
	return out
}

// ─── Validators ─────────────────────────────────────────────────────────────

// ValidateSelection is validateChatSelection.
func ValidateSelection(value any, allowed map[string]bool) []string {
	root, ok := g.AsObject(value)
	if !ok {
		return []string{"The response must be a JSON object."}
	}
	errs := []string{}
	paths, pathsOK := root["paths"].([]any)
	if !pathsOK {
		errs = append(errs, `"paths" must be an array.`)
	} else {
		if len(paths) > MaxPaths {
			errs = append(errs, `"paths" may contain at most `+strconv.Itoa(MaxPaths)+` items.`)
		}
		for _, p := range paths {
			if s, ok := p.(string); !ok || !allowed[s] {
				errs = append(errs, "Path "+g.JSONStringify(p)+" is not in the tracked-file list.")
			}
		}
	}
	searches, searchesOK := root["searches"].([]any)
	if !searchesOK {
		errs = append(errs, `"searches" must be an array.`)
	} else {
		if len(searches) > MaxSearches {
			errs = append(errs, `"searches" may contain at most `+strconv.Itoa(MaxSearches)+` items.`)
		}
		for _, q := range searches {
			if s, ok := q.(string); !ok || trim(s) == "" || ustr(s) > 120 {
				errs = append(errs, "Every search must be a non-empty literal string of at most 120 characters.")
			}
		}
	}
	if pathsOK && searchesOK && len(paths) == 0 && len(searches) == 0 {
		errs = append(errs, "Select at least one path or search term.")
	}
	return errs
}

// ValidateAnswer is validateChatAnswer. actionContext nil means proposals are
// disabled.
func ValidateAnswer(value any, allowed map[string]bool, actionContext *g.RepoChatActionContext) []string {
	root, ok := g.AsObject(value)
	if !ok {
		return []string{"The response must be a JSON object."}
	}
	errs := []string{}
	// Small models sometimes put the whole reply into "actions" and leave
	// "content" blank — with a usable proposal that is salvageable (the caller
	// falls back to the action descriptions), so only reject an empty content
	// when there is nothing else to show.
	proposals := 0
	if actions, isArr := root["actions"].([]any); actionContext != nil && isArr {
		proposals = len(actions)
	}
	// A turn that asks for more evidence is not an answer yet, so it is
	// allowed to arrive without one.
	_, asking := g.AsObject(root["needMore"])
	content, isStr := root["content"].(string)
	switch {
	case !isStr:
		errs = append(errs, `"content" must be a string answer.`)
	case trim(content) == "" && proposals == 0 && !asking:
		errs = append(errs, `"content" must be a non-empty answer.`)
	case ContentContainsActionPayload(content):
		errs = append(errs, "Executable action JSON must be returned in the top-level actions field, not in content.")
	}
	if ids, ok := root["sourceIds"].([]any); !ok {
		errs = append(errs, `"sourceIds" must be an array.`)
	} else {
		if len(ids) > 12 {
			errs = append(errs, `"sourceIds" may contain at most 12 items.`)
		}
		for _, id := range ids {
			if s, ok := id.(string); !ok || !allowed[s] {
				errs = append(errs, "Source "+g.JSONStringify(id)+" was not provided in the evidence.")
			}
		}
	}
	if actions, present := root["actions"]; present {
		if actionContext == nil {
			errs = append(errs, `Action proposals are disabled — omit "actions" entirely.`)
		} else {
			errs = append(errs, g.ValidateRepoChatActions(actions, *actionContext)...)
		}
	}
	return errs
}

// ─── Prompt text ────────────────────────────────────────────────────────────

func serializeHistory(messages []Message) string {
	var blocks []string
	used := 0
	for i := len(messages) - 1; i >= 0; i-- {
		who := "Assistant"
		if messages[i].Role == "user" {
			who = "User"
		}
		block := who + ": " + messages[i].Content
		if len(blocks) > 0 && used+ustr(block) > maxHistoryChars {
			break
		}
		clipped := g.SliceJS(block, max(0, maxHistoryChars-used))
		blocks = append([]string{clipped}, blocks...)
		used += ustr(clipped)
	}
	return strings.Join(blocks, "\n\n")
}

func serializePaths(paths []string) string {
	var out []string
	used := 0
	for _, p := range paths {
		if used+ustr(p)+1 > maxPathListChars {
			break
		}
		out = append(out, p)
		used += ustr(p) + 1
	}
	return strings.Join(out, "\n")
}

func orDetached(current string) string {
	if current == "" {
		return "(detached)"
	}
	return current
}

func statusSummary(status gitsvc.RepoStatus, allowed map[string]bool) string {
	paths := func(items []gitsvc.FileEntry) string {
		var out []string
		for _, it := range items {
			if allowed[it.Path] {
				out = append(out, it.Path)
			}
		}
		if len(out) == 0 {
			return "(none)"
		}
		return strings.Join(out, ", ")
	}
	var unstaged []gitsvc.FileEntry
	for _, f := range status.Unstaged {
		if !f.Untracked {
			unstaged = append(unstaged, f)
		}
	}
	return "Current branch: " + orDetached(status.Current) +
		"\nAhead/behind upstream: " + strconv.Itoa(status.Ahead) + "/" + strconv.Itoa(status.Behind) +
		"\nStaged tracked files: " + paths(status.Staged) +
		"\nUnstaged tracked files: " + paths(unstaged) +
		"\nConflicted tracked files: " + paths(status.Conflicted)
}

// FinalizationMessages is finalizationMessages: the provider prompt for a
// factual post-execution report with actions disabled.
func FinalizationMessages(messages []Message, status gitsvc.RepoStatus, result ExecutionResult) []g.ChatMessage {
	failed := "(none)"
	if result.FailedType != "" {
		failed = result.FailedType
	}
	code := "(none)"
	if result.ErrorCode != nil {
		code = *result.ErrorCode
	}
	refreshed := "Current branch: " + orDetached(status.Current) +
		"\nAhead/behind upstream: " + strconv.Itoa(status.Ahead) + "/" + strconv.Itoa(status.Behind) +
		"\nStaged files: " + strconv.Itoa(len(status.Staged)) +
		"\nUnstaged files: " + strconv.Itoa(len(status.Unstaged)) +
		"\nConflicted files: " + strconv.Itoa(len(status.Conflicted))
	return []g.ChatMessage{
		{
			Role: "system",
			Content: `Write a concise, factual completion report for repository actions that the app already attempted.
Do not propose any actions. Do not claim that skipped or failed work succeeded. Do not include secrets or credentials.
Return JSON with "content" and an empty "sourceIds" array.`,
		},
		{
			Role: "user",
			Content: "Conversation:\n" + serializeHistory(messages) +
				"\n\nExecution result:\nApplied actions: " + strconv.Itoa(result.Applied) +
				"\nFailed action: " + failed +
				"\nError code: " + code +
				"\nRemaining actions: " + strconv.Itoa(result.Remaining) +
				"\n\nRefreshed repository status:\n" + refreshed,
		},
	}
}

// ─── Evidence ───────────────────────────────────────────────────────────────

// Evidence is RepoChatEvidence: an excerpt the model may cite by ID.
type Evidence struct {
	ID        string `json:"id,omitempty"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	// External marks a file pinned from outside the repository: Path is absolute.
	External bool   `json:"external,omitempty"`
	Text     string `json:"text"`
	// Complete is set (true or false) only on whole-file reads; true when the
	// evidence contains the complete repository file.
	Complete *bool `json:"complete,omitempty"`
	// Pinned marks context the user chose rather than the model.
	Pinned bool `json:"pinned,omitempty"`
}

func lineWindow(content string, line int) Evidence {
	const radius = 18
	lines := strings.Split(content, "\n")
	target := max(1, min(line, max(len(lines), 1)))
	start := max(1, target-radius)
	end := min(len(lines), target+radius)
	text := ""
	if start-1 < end {
		text = strings.Join(lines[start-1:end], "\n")
	}
	return Evidence{StartLine: start, EndLine: end, Text: text}
}

func firstWindow(content string) Evidence {
	clipped := g.SliceJS(content, maxFileChars)
	complete := ustr(content) <= maxFileChars
	return Evidence{StartLine: 1, EndLine: max(1, strings.Count(clipped, "\n")+1), Text: clipped, Complete: &complete}
}

// PackEvidence is packRepoChatEvidence: packs evidence deterministically under
// the outbound context budget, numbering it E1, E2, …
func PackEvidence(items []Evidence, maxChars int) []Evidence {
	packed := []Evidence{}
	used := 0
	for _, item := range items {
		remaining := maxChars - used
		if remaining <= 0 {
			break
		}
		text := item.Text
		if ustr(text) > remaining {
			text = g.SliceJS(text, remaining)
		}
		if trim(text) == "" {
			continue
		}
		lines := strings.Count(text, "\n") + 1
		next := item
		next.ID = "E" + strconv.Itoa(len(packed)+1)
		next.Text = text
		if item.Complete != nil {
			c := *item.Complete && ustr(text) == ustr(item.Text)
			next.Complete = &c
		}
		next.EndLine = min(item.EndLine, item.StartLine+max(0, lines-1))
		packed = append(packed, next)
		used += ustr(text)
	}
	return packed
}

// ClipEvidenceBudget keeps pinned context inside its own slice of the outbound
// budget.
func ClipEvidenceBudget(items []Evidence, maxChars int) []Evidence {
	out := []Evidence{}
	used := 0
	for _, item := range items {
		remaining := maxChars - used
		if remaining <= 0 {
			break
		}
		text := item.Text
		if ustr(text) > remaining {
			text = g.SliceJS(text, remaining)
		}
		if trim(text) == "" {
			continue
		}
		next := item
		next.Text = text
		if item.Complete != nil {
			c := *item.Complete && ustr(text) == ustr(item.Text)
			next.Complete = &c
		}
		out = append(out, next)
		used += ustr(text)
	}
	return out
}

func serializeEvidence(items []Evidence) string {
	blocks := make([]string, 0, len(items))
	for _, item := range items {
		head := "[" + item.ID + "]"
		if item.Pinned {
			head += " (pinned by the user)"
		}
		if item.Complete != nil && *item.Complete {
			head += " (complete file)"
		}
		blocks = append(blocks, head+" "+item.Path+":"+strconv.Itoa(item.StartLine)+"-"+strconv.Itoa(item.EndLine)+"\n"+item.Text)
	}
	return strings.Join(blocks, "\n\n")
}
