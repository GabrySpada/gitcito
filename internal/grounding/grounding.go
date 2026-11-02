// Package grounding is src/main/grounding.ts: grounding helpers for AI
// features, plus the narrow model contract (contract.go) that repository chat
// and the wiki call.
//
// The model never writes file paths, line numbers or code: it is given a list
// of evidence items with opaque IDs ("E1", "E2", …) and may only cite those
// IDs. The app resolves each ID back to the real path/line, so a hallucinated
// reference is a validation error instead of a plausible-looking lie.
package grounding

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// DiffEvidence is one citable hunk of a unified diff.
type DiffEvidence struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// StartLine is the first line of the hunk, in the new file (old file for
	// pure deletions).
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine"`
	// Text is the hunk as sent to the model, including its `@@` header.
	Text string `json:"text"`
}

// DiffEvidenceSet is the evidence plus how many hunks were dropped to stay
// inside the byte budget.
type DiffEvidenceSet struct {
	Items   []DiffEvidence `json:"items"`
	Omitted int            `json:"omitted"`
}

// DiffEvidenceOpts are buildDiffEvidence's options; nil means the TS default
// (24000 bytes, 40 hunks, 4000 bytes per hunk). Sizes are UTF-16 units, like
// the TS `.length`.
type DiffEvidenceOpts struct {
	MaxBytes     *int `json:"maxBytes,omitempty"`
	MaxHunks     *int `json:"maxHunks,omitempty"`
	MaxHunkBytes *int `json:"maxHunkBytes,omitempty"`
}

func orDefault(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

var (
	reDiffFile = regexp.MustCompile(`^diff --git a/(` + shared.JSDot + `+?) b/(` + shared.JSDot + `+)$`)
	reDiffHunk = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	reBPrefix  = regexp.MustCompile(`^b/`)
	reAPrefix  = regexp.MustCompile(`^a/`)
)

func clipHunk(text string, max int) string {
	if gitx.UTF16Len(text) > max {
		return SliceJS(text, max) + "\n…(hunk truncated)"
	}
	return text
}

// atoi is Number() for the all-digit captures of the hunk header.
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		f, _ := strconv.ParseFloat(s, 64)
		return int(f)
	}
	return n
}

// BuildDiffEvidence splits a unified diff into per-hunk evidence items,
// oldest-first, stopping once maxBytes of hunk text has been collected.
func BuildDiffEvidence(diff string, opts DiffEvidenceOpts) DiffEvidenceSet {
	maxBytes := orDefault(opts.MaxBytes, 24000)
	maxHunks := orDefault(opts.MaxHunks, 40)
	maxHunkBytes := orDefault(opts.MaxHunkBytes, 4000)

	items := []DiffEvidence{}
	omitted, used := 0, 0
	path := ""

	// Hunk currently being accumulated.
	inHunk := false
	head := ""
	var body []string
	startLine, endLine, newLine, oldLine := 0, 0, 0, 0
	sawNew := false

	flush := func() {
		if !inHunk {
			return
		}
		text := clipHunk(strings.Join(append([]string{head}, body...), "\n"), maxHunkBytes)
		inHunk = false
		body = nil
		n := gitx.UTF16Len(text)
		if len(items) >= maxHunks || used+n > maxBytes {
			omitted++
			return
		}
		used += n
		items = append(items, DiffEvidence{
			ID:        "E" + strconv.Itoa(len(items)+1),
			Path:      path,
			StartLine: startLine,
			EndLine:   max(startLine, endLine),
			Text:      text,
		})
	}

	for _, line := range strings.Split(diff, "\n") {
		if file := reDiffFile.FindStringSubmatch(line); file != nil {
			flush()
			path = file[2]
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			// A deleted file has `+++ /dev/null`; keep the a-side path in that case.
			to := trim(line[4:])
			if to != "/dev/null" {
				path = reBPrefix.ReplaceAllString(to, "")
			}
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			from := trim(line[4:])
			if path == "" && from != "/dev/null" {
				path = reAPrefix.ReplaceAllString(from, "")
			}
			continue
		}

		if hunk := reDiffHunk.FindStringSubmatch(line); hunk != nil {
			flush()
			oldLine = atoi(hunk[1])
			newLine = atoi(hunk[3])
			newCount := hunk[4]
			if newCount == "" {
				newCount = "1"
			}
			sawNew = atoi(newCount) > 0
			if sawNew {
				startLine = newLine
			} else {
				startLine = oldLine
			}
			endLine = startLine
			head = line
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}

		body = append(body, line)
		switch {
		case strings.HasPrefix(line, "+"):
			endLine = newLine
			newLine++
		case strings.HasPrefix(line, "-"):
			if !sawNew {
				endLine = oldLine
			}
			oldLine++
		case strings.HasPrefix(line, " ") || line == "":
			if sawNew {
				endLine = newLine
			} else {
				endLine = oldLine
			}
			newLine++
			oldLine++
		}
	}
	flush()
	return DiffEvidenceSet{Items: items, Omitted: omitted}
}

// SerializeEvidence renders the evidence list for the prompt: an ID header
// plus the hunk text.
func SerializeEvidence(set DiffEvidenceSet) string {
	blocks := make([]string, 0, len(set.Items)+1)
	for _, e := range set.Items {
		span := ""
		if e.EndLine > e.StartLine {
			span = "-" + strconv.Itoa(e.EndLine)
		}
		blocks = append(blocks, "["+e.ID+"] "+e.Path+":"+strconv.Itoa(e.StartLine)+span+"\n"+e.Text)
	}
	if set.Omitted > 0 {
		blocks = append(blocks, "("+strconv.Itoa(set.Omitted)+" further hunk(s) omitted — do not reference them.)")
	}
	return strings.Join(blocks, "\n\n")
}

// EvidenceIndex maps each evidence ID to its item.
func EvidenceIndex(set DiffEvidenceSet) map[string]DiffEvidence {
	out := make(map[string]DiffEvidence, len(set.Items))
	for _, e := range set.Items {
		out[e.ID] = e
	}
	return out
}

// EvidenceIDs lists the IDs in order — the `allowed` set ValidateReview names.
func EvidenceIDs(set DiffEvidenceSet) []string {
	out := make([]string, 0, len(set.Items))
	for _, e := range set.Items {
		out = append(out, e.ID)
	}
	return out
}

var (
	findingKinds      = map[string]bool{"risk": true, "suggestion": true}
	findingSeverities = map[string]bool{"high": true, "medium": true, "low": true}
)

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ValidateReview checks a model review against the evidence it was given.
// Returns one correction line per problem; an empty slice means the output is
// usable. allowed is ordered: the message lists the IDs in that order.
func ValidateReview(value any, allowed []string) []string {
	errs := []string{}
	if !isObjectLike(value) {
		return []string{"The response must be a JSON object."}
	}
	if s, ok := str(get(value, "summary")); !ok || trim(s) == "" {
		errs = append(errs, `"summary" must be a non-empty string.`)
	}
	findings, ok := arr(get(value, "findings"))
	if !ok {
		return append(errs, `"findings" must be an array (use [] when there is nothing to report).`)
	}

	ids := strings.Join(allowed, ", ")
	if ids == "" {
		ids = "(none)"
	}
	for i, raw := range findings {
		at := "findings[" + strconv.Itoa(i) + "]"
		if !isObjectLike(raw) {
			errs = append(errs, at+" must be an object.")
			continue
		}
		id := get(raw, "evidenceId")
		if s, ok := str(id); !ok || !contains(allowed, s) {
			errs = append(errs, at+".evidenceId "+JSONStringify(id)+" is not in the evidence list. Cite one of: "+ids+".")
		}
		if s, ok := str(get(raw, "kind")); !ok || !findingKinds[s] {
			errs = append(errs, at+`.kind must be "risk" or "suggestion".`)
		}
		if s, ok := str(get(raw, "severity")); !ok || !findingSeverities[s] {
			errs = append(errs, at+`.severity must be "high", "medium" or "low".`)
		}
		if s, ok := str(get(raw, "claim")); !ok || trim(s) == "" {
			errs = append(errs, at+".claim must be a non-empty sentence.")
		}
	}
	return errs
}

// GroundedFinding is a finding after its evidence ID was resolved to a real
// location.
type GroundedFinding struct {
	Kind       string `json:"kind"`     // 'risk' | 'suggestion'
	Severity   string `json:"severity"` // 'high' | 'medium' | 'low'
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Claim      string `json:"claim"`
	Suggestion string `json:"suggestion"`
}

// GroundFindings resolves cited evidence IDs to paths/lines, dropping anything
// unresolvable. findings are raw model objects.
func GroundFindings(findings []any, index map[string]DiffEvidence) []GroundedFinding {
	out := []GroundedFinding{}
	for _, raw := range findings {
		id, ok := str(get(raw, "evidenceId"))
		if !ok {
			continue
		}
		evidence, found := index[id]
		if !found {
			continue
		}
		kind := "risk"
		if s, _ := str(get(raw, "kind")); s == "suggestion" {
			kind = "suggestion"
		}
		severity := "medium"
		if s, _ := str(get(raw, "severity")); s == "high" || s == "low" {
			severity = s
		}
		claim := ""
		if c := get(raw, "claim"); c != nil {
			claim = shared.JSString(c)
		}
		suggestion := ""
		if s, ok := str(get(raw, "suggestion")); ok {
			suggestion = trim(s)
		}
		out = append(out, GroundedFinding{
			Kind:       kind,
			Severity:   severity,
			Path:       evidence.Path,
			Line:       evidence.StartLine,
			Claim:      trim(claim),
			Suggestion: suggestion,
		})
	}
	return out
}

// RenderFindings is a Markdown bullet list of one finding kind, with
// app-resolved locations.
func RenderFindings(findings []GroundedFinding, kind string) string {
	lines := []string{}
	for _, f := range findings {
		if f.Kind != kind {
			continue
		}
		tail := ""
		if f.Suggestion != "" && kind == "risk" {
			tail = " — " + f.Suggestion
		}
		body := f.Claim
		if kind == "suggestion" {
			body = f.Suggestion
			if body == "" {
				body = f.Claim
			}
		}
		line := "- `" + f.Path + ":" + strconv.Itoa(f.Line) + "` " + body
		if kind == "risk" {
			line += tail
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// NumberedLine is one line of source as the viewer has it, with its real file
// line number.
type NumberedLine struct {
	No   int    `json:"no"`
	Text string `json:"text"`
}

// LineWindow is a numbered slice of a file, the only thing a hover
// explanation may draw on.
type LineWindow struct {
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine"`
	// Numbers are the line numbers actually in the window — a diff view has gaps.
	Numbers []int `json:"numbers"`
	// Text holds the lines prefixed with their number, ready for a prompt.
	Text string `json:"text"`
}

// WindowOpts are the window options; nil means the TS default (radius 30,
// 6000 UTF-16 units).
type WindowOpts struct {
	Radius   *int `json:"radius,omitempty"`
	MaxBytes *int `json:"maxBytes,omitempty"`
}

// BuildWindowFromLines builds the context window around a line: radius lines
// either side of the hovered one, numbered so the model can cite locations
// without being told any file paths. Takes the lines the viewer actually has —
// a whole file, or just the hunks of a diff — and trims from the far end first
// if it overruns the byte budget, always keeping the hovered line.
func BuildWindowFromLines(lines []NumberedLine, line int, opts WindowOpts) LineWindow {
	radius := orDefault(opts.Radius, 30)
	maxBytes := orDefault(opts.MaxBytes, 6000)
	if len(lines) == 0 {
		return LineWindow{StartLine: line, EndLine: line, Numbers: []int{}, Text: ""}
	}

	// Nearest entry to the hovered line — in a diff the exact number may be on
	// the other side of the hunk.
	targetIdx := 0
	for i := 1; i < len(lines); i++ {
		if math.Abs(float64(lines[i].No-line)) < math.Abs(float64(lines[targetIdx].No-line)) {
			targetIdx = i
		}
	}

	from := max(0, targetIdx-radius)
	to := min(len(lines)-1, targetIdx+radius)
	render := func(a, b int) string {
		parts := make([]string, 0, b-a+1)
		for _, l := range lines[a : b+1] {
			parts = append(parts, strconv.Itoa(l.No)+" | "+l.Text)
		}
		return strings.Join(parts, "\n")
	}

	text := render(from, to)
	for gitx.UTF16Len(text) > maxBytes && (from < targetIdx || to > targetIdx) {
		if to-targetIdx >= targetIdx-from && to > targetIdx {
			to--
		} else if from < targetIdx {
			from++
		} else {
			break
		}
		text = render(from, to)
	}

	slice := lines[from : to+1]
	numbers := make([]int, 0, len(slice))
	for _, l := range slice {
		numbers = append(numbers, l.No)
	}
	return LineWindow{StartLine: slice[0].No, EndLine: slice[len(slice)-1].No, Numbers: numbers, Text: text}
}

// BuildLineWindow is the window around a line of a whole file.
func BuildLineWindow(content string, line int, opts WindowOpts) LineWindow {
	split := strings.Split(content, "\n")
	lines := make([]NumberedLine, len(split))
	for i, text := range split {
		lines[i] = NumberedLine{No: i + 1, Text: text}
	}
	return BuildWindowFromLines(lines, min(max(line, 1), len(lines)), opts)
}

// ValidateHoverExplain checks a hover explanation: it must be brief and may
// only cite lines that were actually in the window.
func ValidateHoverExplain(value any, window LineWindow) []string {
	errs := []string{}
	if !isObjectLike(value) {
		return []string{"The response must be a JSON object."}
	}
	if s, ok := str(get(value, "summary")); !ok || trim(s) == "" {
		errs = append(errs, `"summary" must be a single sentence.`)
	}
	if bullets, present := Prop(value, "bullets"); present {
		if list, ok := arr(bullets); !ok {
			errs = append(errs, `"bullets" must be an array of short strings.`)
		} else if len(list) > 3 {
			errs = append(errs, `"bullets" must have at most 3 entries.`)
		}
	}
	if lines, present := Prop(value, "lines"); present {
		list, ok := arr(lines)
		if !ok {
			errs = append(errs, `"lines" must be an array of line numbers from the window.`)
		} else {
			for _, n := range list {
				f, isNum := toFloat(n)
				if !isNumber(n) || !isNum || !windowHas(window, f) {
					errs = append(errs, `"lines" contains `+JSONStringify(n)+`, which is not a line you were shown. Cite only lines between `+
						strconv.Itoa(window.StartLine)+" and "+strconv.Itoa(window.EndLine)+" that appear in the window.")
				}
			}
		}
	}
	return errs
}

func windowHas(w LineWindow, f float64) bool {
	for _, n := range w.Numbers {
		if float64(n) == f {
			return true
		}
	}
	return false
}

// askActionTypes is working-tree and ref bookkeeping — what the Ask planner
// has always run. Ordered: the "Use one of" message lists them in this order.
var askActionTypes = []string{"gitignore", "stage", "unstage", "commit", "stash", "discard", "branch", "checkout", "tag"}

// historyActionTypes are local actions that move history. Allowed only for a
// surface that shows the model the branches and commit hashes they must name.
var historyActionTypes = []string{"merge", "rebase", "revert", "cherry_pick"}

// remoteActionTypes reach a remote or the hosting provider. Allowed only when
// the caller says the remote-actions setting is on.
var remoteActionTypes = []string{"fetch", "pull", "push", "open_pr", "stack_submit"}

var pullModes = []string{"default", "ff-only", "rebase"}

var reHash = regexp.MustCompile(`(?i)^[0-9a-f]{7,40}$`)

// refActionErrors checks the fields of actions that name refs or commits
// rather than files.
func refActionErrors(typ string, raw any, at string) []string {
	errs := []string{}
	text := func(key string) string {
		s, _ := str(get(raw, key))
		return trim(s)
	}
	if typ == "merge" && text("ref") == "" {
		errs = append(errs, at+".ref must be the branch or commit to merge in.")
	}
	if typ == "rebase" && text("onto") == "" {
		errs = append(errs, at+".onto must be the branch to rebase onto.")
	}
	if typ == "revert" || typ == "cherry_pick" {
		hashes, ok := arr(get(raw, "hashes"))
		if !ok || len(hashes) == 0 {
			errs = append(errs, at+".hashes must be a non-empty array of commit hashes copied from the history you were shown.")
		} else {
			for _, h := range hashes {
				if s, ok := str(h); !ok || !reHash.MatchString(s) {
					errs = append(errs, at+".hashes must contain only commit hashes; never invent one.")
					break
				}
			}
		}
	}
	if mode, present := Prop(raw, "mode"); typ == "pull" && present && !contains(pullModes, shared.JSString(mode)) {
		errs = append(errs, at+".mode must be one of: "+strings.Join(pullModes, ", ")+".")
	}
	if typ == "open_pr" {
		if text("title") == "" {
			errs = append(errs, at+".title must be the pull request title.")
		}
		if text("target") == "" {
			errs = append(errs, at+".target must be the branch the pull request merges into.")
		}
	}
	return errs
}

// ValidateAskPlan checks an "Ask" plan against the working tree it was built
// for: every file an action touches must be a path the model was shown, and
// every action must be a type the app can execute.
func ValidateAskPlan(value any, knownPaths map[string]bool) []string {
	errs := []string{}
	if !isObjectLike(value) {
		return []string{"The response must be a JSON object."}
	}
	if _, ok := str(get(value, "summary")); !ok {
		errs = append(errs, `"summary" must be a string.`)
	}
	return append(errs, ValidateAskActions(get(value, "actions"), knownPaths, AskAllow{})...)
}

// AskAllow widens the Ask action set: History for merge/rebase/revert/
// cherry-pick, Remote for fetch/pull/push/PRs/stacks.
type AskAllow struct {
	History bool `json:"history,omitempty"`
	Remote  bool `json:"remote,omitempty"`
}

// ValidateAskActions is the action-list half of the plan check, shared with
// repository chat: chat replies carry the same action union, grounded against
// the same working-tree path list.
func ValidateAskActions(value any, knownPaths map[string]bool, allow AskAllow) []string {
	list, ok := arr(value)
	if !ok {
		return []string{`"actions" must be an array (use [] when nothing can be done).`}
	}
	errs := []string{}
	runnable := append([]string{}, askActionTypes...)
	if allow.History {
		runnable = append(runnable, historyActionTypes...)
	}
	if allow.Remote {
		runnable = append(runnable, remoteActionTypes...)
	}
	for i, raw := range list {
		at := "actions[" + strconv.Itoa(i) + "]"
		if !isObjectLike(raw) {
			errs = append(errs, at+" must be an object.")
			continue
		}
		typeValue := get(raw, "type")
		typ, isStr := str(typeValue)
		if !isStr || !contains(runnable, typ) {
			// Naming the remote set explicitly is the difference between "the
			// app cannot do this" and "this repository's settings do not allow it".
			if isStr && contains(remoteActionTypes, typ) {
				errs = append(errs, at+".type "+JSONStringify(typ)+" needs remote actions, which are off in Settings. Propose only local actions.")
				continue
			}
			if isStr && contains(historyActionTypes, typ) {
				errs = append(errs, at+".type "+JSONStringify(typ)+" is not available on this surface. Use repository chat for merge, rebase, revert and cherry-pick.")
				continue
			}
			errs = append(errs, at+".type "+JSONStringify(typeValue)+" is not an action this app can run. Use one of: "+strings.Join(runnable, ", ")+".")
			continue
		}
		errs = append(errs, refActionErrors(typ, raw, at)...)
		if s, ok := str(get(raw, "description")); !ok || trim(s) == "" {
			errs = append(errs, at+".description must be a short sentence.")
		}
		if s, ok := str(get(raw, "message")); typ == "commit" && (!ok || trim(s) == "") {
			errs = append(errs, at+".message must be a commit message.")
		}
		if s, ok := str(get(raw, "name")); (typ == "branch" || typ == "tag") && (!ok || trim(s) == "") {
			errs = append(errs, at+".name must be a "+typ+" name.")
		}
		if s, ok := str(get(raw, "ref")); typ == "checkout" && (!ok || trim(s) == "") {
			errs = append(errs, at+".ref must be a branch, tag or commit to switch to.")
		}
		if typ == "gitignore" {
			// Patterns are literal globs the user wants ignored, not existing paths.
			if patterns, ok := arr(get(raw, "patterns")); !ok || len(patterns) == 0 {
				errs = append(errs, at+".patterns must be a non-empty array of gitignore patterns.")
			}
			continue
		}
		files, present := Prop(raw, "files")
		if !present {
			continue
		}
		fileList, ok := arr(files)
		if !ok {
			errs = append(errs, at+".files must be an array of repo-relative paths.")
			continue
		}
		for _, file := range fileList {
			if s, ok := str(file); !ok || !knownPaths[s] {
				errs = append(errs, at+".files contains "+JSONStringify(file)+", which is not one of the files listed in the working-tree state. Use only paths from those lists.")
			}
		}
	}
	return errs
}

var repoChatFileActionTypes = map[string]bool{"edit_file": true, "write_file": true, "delete_file": true}

// RepoChatActionContext is what a repository-chat plan is grounded against.
type RepoChatActionContext struct {
	WorkingTreePaths map[string]bool
	EvidencePaths    map[string]bool
	CompletePaths    map[string]bool
	// AllowFileActions false refuses file mutations (file read-only mode); nil
	// allows them, like an absent TS field.
	AllowFileActions *bool
	// AllowRemoteActions says whether the plan may reach a remote or the
	// hosting provider.
	AllowRemoteActions bool
}

// ValidateRepoChatActions validates repository-chat plans without broadening
// the Ask planner. File mutations form a prefix, and their outputs become
// valid paths for later Git actions such as stage and commit.
func ValidateRepoChatActions(value any, ctx RepoChatActionContext) []string {
	list, ok := arr(value)
	if !ok {
		return []string{`"actions" must be an array (use [] when nothing can be done).`}
	}

	errs := []string{}
	gitActions := []any{}
	knownGitPaths := map[string]bool{}
	for p := range ctx.WorkingTreePaths {
		knownGitPaths[p] = true
	}
	fileActions := 0
	sawGitAction := false

	if len(list) > 64 {
		errs = append(errs, "Repository chat may propose at most 64 total actions.")
	}

	for i, raw := range list {
		at := "actions[" + strconv.Itoa(i) + "]"
		action, isObj := AsObject(raw)
		if !isObj {
			gitActions = append(gitActions, raw)
			sawGitAction = true
			continue
		}
		typ, _ := str(action["type"])
		if !repoChatFileActionTypes[typ] {
			gitActions = append(gitActions, action)
			sawGitAction = true
			continue
		}
		if ctx.AllowFileActions != nil && !*ctx.AllowFileActions {
			errs = append(errs, at+" file actions are disabled by file read-only mode.")
			continue
		}

		fileActions++
		if sawGitAction {
			errs = append(errs, at+" file actions must appear before Git actions.")
		}
		if s, ok := str(action["description"]); !ok || trim(s) == "" {
			errs = append(errs, at+".description must be a short sentence.")
		}
		rawPath, isStr := str(action["path"])
		if !isStr || !shared.IsSafeRepoPath(rawPath) {
			errs = append(errs, at+".path must be a relative path inside the repository.")
			continue
		}

		path := trim(rawPath)
		mode, hasMode := action["mode"]
		if typ != "write_file" || (hasMode && mode == "replace") {
			if !ctx.EvidencePaths[path] {
				errs = append(errs, at+".path "+JSONStringify(path)+" requires repository evidence.")
			}
		}

		switch typ {
		case "edit_file":
			if s, ok := str(action["oldText"]); !ok || s == "" {
				errs = append(errs, at+".oldText must be a non-empty exact excerpt.")
			}
			if _, ok := str(action["newText"]); !ok {
				errs = append(errs, at+".newText must be a string.")
			}
			if ra, present := action["replaceAll"]; present {
				if _, ok := ra.(bool); !ok {
					errs = append(errs, at+".replaceAll must be a boolean when provided.")
				}
			}
		case "write_file":
			if mode != "create" && mode != "replace" {
				errs = append(errs, at+`.mode must be either "create" or "replace".`)
			}
			if _, ok := str(action["content"]); !ok {
				errs = append(errs, at+".content must be a string.")
			}
			if mode == "replace" && !ctx.CompletePaths[path] {
				errs = append(errs, at+".path "+JSONStringify(path)+" requires complete evidence before replacement.")
			}
		}

		knownGitPaths[path] = true
	}

	if fileActions > 48 {
		errs = append(errs, "Repository chat may propose at most 48 file actions.")
	}
	if len(gitActions) > 12 {
		errs = append(errs, "Repository chat may propose at most 12 Git actions.")
	}
	return append(errs, ValidateAskActions(gitActions, knownGitPaths, AskAllow{History: true, Remote: ctx.AllowRemoteActions})...)
}
