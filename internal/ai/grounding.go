package ai

// The slice of src/main/grounding.ts that ai.ts calls: diff evidence for the
// PR review, line windows for hover explain, the Ask-plan validator, and the
// loose JSON parser every JSON feature goes through. grounding.ts belongs to
// the repochat group; these are ported here because internal/ai cannot import
// that package (repochat imports ai), and they are exported so repochat can
// reuse them rather than keep a second copy.
//
// Grounding helpers for AI features: the model never writes file paths, line
// numbers or code. It is given a list of evidence items with opaque IDs ("E1",
// "E2", …) and may only cite those IDs. The app resolves each ID back to the
// real path/line, so a hallucinated reference is a validation error instead of
// a plausible-looking lie.

import (
	"regexp"
	"strconv"
	"strings"
)

// DiffEvidence is one citable hunk of a unified diff.
type DiffEvidence struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// First line of the hunk, in the new file (old file for pure deletions).
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine"`
	// Hunk text as sent to the model, including its `@@` header.
	Text string `json:"text"`
}

// DiffEvidenceSet is the evidence list plus how many hunks were dropped to
// stay inside the byte budget.
type DiffEvidenceSet struct {
	Items   []DiffEvidence `json:"items"`
	Omitted int            `json:"omitted"`
}

// EvidenceOptions bounds BuildDiffEvidence; zero fields take the defaults.
type EvidenceOptions struct {
	MaxBytes     int
	MaxHunks     int
	MaxHunkBytes int
}

var (
	// JS `.` stops at \r, \u2028 and \u2029 as well as \n, so a CRLF line
	// does not match; RE2 `.` would.
	fileRE = regexp.MustCompile(`^diff --git a/([^\r\n\x{2028}\x{2029}]+?) b/([^\r\n\x{2028}\x{2029}]+)$`)
	hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

func clipHunk(text string, max int) string {
	if utf16Len(text) > max {
		return sliceUTF16(text, max) + "\n…(hunk truncated)"
	}
	return text
}

// jsNumber is Number(s) for a run of digits.
func digitsNumber(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// BuildDiffEvidence splits a unified diff into per-hunk evidence items,
// oldest-first, stopping once MaxBytes of hunk text has been collected.
func BuildDiffEvidence(diff string, opts EvidenceOptions) DiffEvidenceSet {
	maxBytes, maxHunks, maxHunkBytes := 24000, 40, 4000
	if opts.MaxBytes != 0 {
		maxBytes = opts.MaxBytes
	}
	if opts.MaxHunks != 0 {
		maxHunks = opts.MaxHunks
	}
	if opts.MaxHunkBytes != 0 {
		maxHunkBytes = opts.MaxHunkBytes
	}

	items := []DiffEvidence{}
	omitted, used := 0, 0
	path := ""

	// Hunk currently being accumulated.
	var head *string
	var body []string
	startLine, endLine, newLine, oldLine := 0, 0, 0, 0
	sawNew := false

	flush := func() {
		if head == nil {
			return
		}
		text := clipHunk(strings.Join(append([]string{*head}, body...), "\n"), maxHunkBytes)
		head = nil
		body = nil
		n := utf16Len(text)
		if len(items) >= maxHunks || used+n > maxBytes {
			omitted++
			return
		}
		used += n
		items = append(items, DiffEvidence{
			ID: "E" + strconv.Itoa(len(items)+1), Path: path, StartLine: startLine, EndLine: max(startLine, endLine), Text: text,
		})
	}

	for _, line := range strings.Split(diff, "\n") {
		if m := fileRE.FindStringSubmatch(line); m != nil {
			flush()
			path = m[2]
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			// A deleted file has `+++ /dev/null`; keep the a-side path then.
			to := jsTrim(line[4:])
			if to != "/dev/null" {
				path = strings.TrimPrefix(to, "b/")
			}
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			from := jsTrim(line[4:])
			if path == "" && from != "/dev/null" {
				path = strings.TrimPrefix(from, "a/")
			}
			continue
		}
		if m := hunkRE.FindStringSubmatch(line); m != nil {
			flush()
			oldLine = digitsNumber(m[1])
			newLine = digitsNumber(m[3])
			newCount := "1"
			if m[4] != "" {
				newCount = m[4]
			}
			sawNew = digitsNumber(newCount) > 0
			if sawNew {
				startLine = newLine
			} else {
				startLine = oldLine
			}
			endLine = startLine
			l := line
			head = &l
			continue
		}
		if head == nil {
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
		rng := ""
		if e.EndLine > e.StartLine {
			rng = "-" + strconv.Itoa(e.EndLine)
		}
		blocks = append(blocks, "["+e.ID+"] "+e.Path+":"+strconv.Itoa(e.StartLine)+rng+"\n"+e.Text)
	}
	if set.Omitted > 0 {
		blocks = append(blocks, "("+strconv.Itoa(set.Omitted)+" further hunk(s) omitted — do not reference them.)")
	}
	return strings.Join(blocks, "\n\n")
}

// EvidenceIndex maps each ID to its item.
func EvidenceIndex(set DiffEvidenceSet) map[string]DiffEvidence {
	m := make(map[string]DiffEvidence, len(set.Items))
	for _, e := range set.Items {
		m[e.ID] = e
	}
	return m
}

// evidenceIDs lists the IDs in order, for the "Cite one of" correction.
func evidenceIDs(set DiffEvidenceSet) []string {
	ids := make([]string, len(set.Items))
	for i, e := range set.Items {
		ids[i] = e.ID
	}
	return ids
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ValidateReview checks a model review against the evidence it was given.
// Returns one correction line per problem; empty means the output is usable.
// allowed is the evidence IDs in order.
func ValidateReview(value any, allowed []string) []string {
	errs := []string{}
	root := asObj(value)
	if root == nil {
		// `!root || typeof root !== 'object'`: an array passes this check in
		// TS and then fails the field checks below.
		arr, isArr := value.([]any)
		if !isArr {
			return []string{"The response must be a JSON object."}
		}
		_ = arr
		root = &Obj{}
	}
	summary, _ := root.Get("summary")
	if !nonEmptyString(summary) {
		errs = append(errs, `"summary" must be a non-empty string.`)
	}
	findingsV, _ := root.Get("findings")
	findings, ok := findingsV.([]any)
	if !ok {
		return append(errs, `"findings" must be an array (use [] when there is nothing to report).`)
	}
	ids := strings.Join(allowed, ", ")
	if ids == "" {
		ids = "(none)"
	}
	for i, raw := range findings {
		at := "findings[" + strconv.Itoa(i) + "]"
		f, isObj := raw.(*Obj)
		if _, isArr := raw.([]any); isArr {
			f, isObj = &Obj{}, true
		}
		if !isObj || f == nil {
			errs = append(errs, at+" must be an object.")
			continue
		}
		ev, has := f.Get("evidenceId")
		if s, ok := ev.(string); !ok || !inList(allowed, s) {
			shown := ev
			if !has {
				shown = nil
			}
			errs = append(errs, at+".evidenceId "+jsonStringify(shown)+" is not in the evidence list. Cite one of: "+ids+".")
		}
		if k, ok := f.V("kind").(string); !ok || (k != "risk" && k != "suggestion") {
			errs = append(errs, at+`.kind must be "risk" or "suggestion".`)
		}
		if s, ok := f.V("severity").(string); !ok || (s != "high" && s != "medium" && s != "low") {
			errs = append(errs, at+`.severity must be "high", "medium" or "low".`)
		}
		if !nonEmptyString(f.V("claim")) {
			errs = append(errs, at+".claim must be a non-empty sentence.")
		}
	}
	return errs
}

// GroundedFinding is a finding after its EvidenceID has been resolved to a
// real location.
type GroundedFinding struct {
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Claim      string `json:"claim"`
	Suggestion string `json:"suggestion"`
}

// GroundFindings resolves cited EvidenceIDs to paths/lines, dropping anything
// unresolvable. Each raw finding is a decoded model object.
func GroundFindings(findings []any, index map[string]DiffEvidence) []GroundedFinding {
	out := []GroundedFinding{}
	for _, raw := range findings {
		f := asObj(raw)
		if f == nil {
			continue
		}
		id, ok := f.V("evidenceId").(string)
		if !ok {
			continue
		}
		evidence, ok := index[id]
		if !ok {
			continue
		}
		kind := "risk"
		if f.V("kind") == "suggestion" {
			kind = "suggestion"
		}
		severity := "medium"
		if s, _ := f.V("severity").(string); s == "high" || s == "low" {
			severity = s
		}
		claimV, has := f.Get("claim")
		claim := ""
		if has && claimV != nil {
			claim = jsTrim(jsString(claimV))
		}
		suggestion := ""
		if s, ok := f.V("suggestion").(string); ok {
			suggestion = jsTrim(s)
		}
		out = append(out, GroundedFinding{
			Kind: kind, Severity: severity, Path: evidence.Path, Line: evidence.StartLine, Claim: claim, Suggestion: suggestion,
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
		if kind != "risk" {
			tail = ""
		}
		lines = append(lines, "- `"+f.Path+":"+strconv.Itoa(f.Line)+"` "+body+tail)
	}
	return strings.Join(lines, "\n")
}

// NumberedLine is one line of source as the viewer has it, with its real file
// line number.
type NumberedLine struct {
	No   float64 `json:"no"`
	Text string  `json:"text"`
}

// LineWindow is a numbered slice of a file, the only thing a hover
// explanation may draw on.
type LineWindow struct {
	StartLine float64 `json:"startLine"`
	EndLine   float64 `json:"endLine"`
	// The line numbers actually in the window — a diff view has gaps.
	Numbers []float64 `json:"numbers"`
	// Lines prefixed with their number, ready to paste into a prompt.
	Text string `json:"text"`
}

// WindowOptions bounds BuildWindowFromLines; zero fields take the defaults.
type WindowOptions struct {
	Radius   int
	MaxBytes int
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// BuildWindowFromLines builds the context window around a line: Radius lines
// either side of the hovered one, numbered so the model can cite locations
// without being told any file paths. It takes the lines the viewer actually
// has — a whole file, or just the hunks of a diff — and trims from the far end
// first if it overruns the byte budget, always keeping the hovered line.
func BuildWindowFromLines(lines []NumberedLine, line float64, opts WindowOptions) LineWindow {
	radius, maxBytes := 30, 6000
	if opts.Radius != 0 {
		radius = opts.Radius
	}
	if opts.MaxBytes != 0 {
		maxBytes = opts.MaxBytes
	}
	if len(lines) == 0 {
		return LineWindow{StartLine: line, EndLine: line, Numbers: []float64{}, Text: ""}
	}
	// Nearest entry to the hovered line — in a diff the exact number may be on
	// the other side of the hunk.
	target := 0
	for i := 1; i < len(lines); i++ {
		if absf(lines[i].No-line) < absf(lines[target].No-line) {
			target = i
		}
	}
	from := max(0, target-radius)
	to := min(len(lines)-1, target+radius)
	render := func(a, b int) string {
		parts := make([]string, 0, b-a+1)
		for _, l := range lines[a : b+1] {
			parts = append(parts, jsNumberString(l.No)+" | "+l.Text)
		}
		return strings.Join(parts, "\n")
	}
	text := render(from, to)
	for utf16Len(text) > maxBytes && (from < target || to > target) {
		if to-target >= target-from && to > target {
			to--
		} else if from < target {
			from++
		} else {
			break
		}
		text = render(from, to)
	}
	slice := lines[from : to+1]
	nums := make([]float64, len(slice))
	for i, l := range slice {
		nums[i] = l.No
	}
	return LineWindow{StartLine: slice[0].No, EndLine: slice[len(slice)-1].No, Numbers: nums, Text: text}
}

// ValidateHoverExplain checks a hover explanation: brief, and citing only
// lines that were actually in the window.
func ValidateHoverExplain(value any, window LineWindow) []string {
	errs := []string{}
	root := asObj(value)
	if root == nil {
		if _, isArr := value.([]any); !isArr {
			return []string{"The response must be a JSON object."}
		}
		root = &Obj{}
	}
	if !nonEmptyString(root.V("summary")) {
		errs = append(errs, `"summary" must be a single sentence.`)
	}
	if b, has := root.Get("bullets"); has {
		if arr, ok := b.([]any); !ok {
			errs = append(errs, `"bullets" must be an array of short strings.`)
		} else if len(arr) > 3 {
			errs = append(errs, `"bullets" must have at most 3 entries.`)
		}
	}
	if l, has := root.Get("lines"); has {
		arr, ok := l.([]any)
		if !ok {
			errs = append(errs, `"lines" must be an array of line numbers from the window.`)
		} else {
			for _, n := range arr {
				f, isNum := n.(float64)
				allowed := false
				if isNum {
					for _, w := range window.Numbers {
						if w == f {
							allowed = true
							break
						}
					}
				}
				if !allowed {
					errs = append(errs, `"lines" contains `+jsonStringify(n)+`, which is not a line you were shown. Cite only lines between `+
						jsNumberString(window.StartLine)+` and `+jsNumberString(window.EndLine)+` that appear in the window.`)
				}
			}
		}
	}
	return errs
}

// Working-tree and ref bookkeeping — what the Ask planner has always run.
var askActionTypes = []string{"gitignore", "stage", "unstage", "commit", "stash", "discard", "branch", "checkout", "tag"}

// Local actions that move history. Allowed only for a surface that shows the
// model the branches and commit hashes they must name.
var historyActionTypes = []string{"merge", "rebase", "revert", "cherry_pick"}

// Action types that reach a remote or the hosting provider. Allowed only when
// the caller says the remote-actions setting is on.
var remoteAskActionTypes = []string{"fetch", "pull", "push", "open_pr", "stack_submit"}

var pullModes = []string{"default", "ff-only", "rebase"}

var hashRE = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// ActionAllow says which optional action families a surface offers.
type ActionAllow struct {
	History bool
	Remote  bool
}

func trimmedField(raw *Obj, key string) string {
	if s, ok := raw.vals[key].(string); ok {
		return jsTrim(s)
	}
	return ""
}

// refActionErrors checks the actions that name refs or commits rather than
// files.
func refActionErrors(typ string, raw *Obj, at string) []string {
	errs := []string{}
	if typ == "merge" && trimmedField(raw, "ref") == "" {
		errs = append(errs, at+".ref must be the branch or commit to merge in.")
	}
	if typ == "rebase" && trimmedField(raw, "onto") == "" {
		errs = append(errs, at+".onto must be the branch to rebase onto.")
	}
	if typ == "revert" || typ == "cherry_pick" {
		hashes, ok := raw.V("hashes").([]any)
		if !ok || len(hashes) == 0 {
			errs = append(errs, at+".hashes must be a non-empty array of commit hashes copied from the history you were shown.")
		} else {
			for _, h := range hashes {
				s, isStr := h.(string)
				if !isStr || !hashRE.MatchString(s) {
					errs = append(errs, at+".hashes must contain only commit hashes; never invent one.")
					break
				}
			}
		}
	}
	if typ == "pull" {
		if mode, has := raw.Get("mode"); has && !inList(pullModes, jsString(mode)) {
			errs = append(errs, at+".mode must be one of: "+strings.Join(pullModes, ", ")+".")
		}
	}
	if typ == "open_pr" {
		if trimmedField(raw, "title") == "" {
			errs = append(errs, at+".title must be the pull request title.")
		}
		if trimmedField(raw, "target") == "" {
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
	root := asObj(value)
	if root == nil {
		if _, isArr := value.([]any); !isArr {
			return []string{"The response must be a JSON object."}
		}
		root = &Obj{}
	}
	if _, ok := root.V("summary").(string); !ok {
		errs = append(errs, `"summary" must be a string.`)
	}
	actions, _ := root.Get("actions")
	return append(errs, ValidateAskActions(actions, knownPaths, ActionAllow{})...)
}

// ValidateAskActions is the action-list half of the plan check, shared with
// repository chat: chat replies carry the same action union, grounded against
// the same working-tree path list.
func ValidateAskActions(value any, knownPaths map[string]bool, allow ActionAllow) []string {
	list, ok := value.([]any)
	if !ok {
		return []string{`"actions" must be an array (use [] when nothing can be done).`}
	}
	errs := []string{}
	runnable := append([]string{}, askActionTypes...)
	if allow.History {
		runnable = append(runnable, historyActionTypes...)
	}
	if allow.Remote {
		runnable = append(runnable, remoteAskActionTypes...)
	}
	for i, rawV := range list {
		at := "actions[" + strconv.Itoa(i) + "]"
		raw, isObj := rawV.(*Obj)
		if _, isArr := rawV.([]any); isArr {
			raw, isObj = &Obj{}, true
		}
		if !isObj || raw == nil {
			errs = append(errs, at+" must be an object.")
			continue
		}
		typeV, hasType := raw.Get("type")
		typ, isStr := typeV.(string)
		if !isStr || !inList(runnable, typ) {
			// Naming the remote set explicitly is the difference between "the
			// app cannot do this" and "this repository's settings do not allow it".
			if isStr && inList(remoteAskActionTypes, typ) {
				errs = append(errs, at+".type "+jsonStringify(typ)+" needs remote actions, which are off in Settings. Propose only local actions.")
				continue
			}
			if isStr && inList(historyActionTypes, typ) {
				errs = append(errs, at+".type "+jsonStringify(typ)+" is not available on this surface. Use repository chat for merge, rebase, revert and cherry-pick.")
				continue
			}
			shown := typeV
			if !hasType {
				shown = nil
			}
			errs = append(errs, at+".type "+jsonStringify(shown)+" is not an action this app can run. Use one of: "+strings.Join(runnable, ", ")+".")
			continue
		}
		errs = append(errs, refActionErrors(typ, raw, at)...)
		if !nonEmptyString(raw.V("description")) {
			errs = append(errs, at+".description must be a short sentence.")
		}
		if typ == "commit" && !nonEmptyString(raw.V("message")) {
			errs = append(errs, at+".message must be a commit message.")
		}
		if (typ == "branch" || typ == "tag") && !nonEmptyString(raw.V("name")) {
			errs = append(errs, at+".name must be a "+typ+" name.")
		}
		if typ == "checkout" && !nonEmptyString(raw.V("ref")) {
			errs = append(errs, at+".ref must be a branch, tag or commit to switch to.")
		}
		if typ == "gitignore" {
			// Patterns are literal globs the user wants ignored, not existing paths.
			if p, ok := raw.V("patterns").([]any); !ok || len(p) == 0 {
				errs = append(errs, at+".patterns must be a non-empty array of gitignore patterns.")
			}
			continue
		}
		files, has := raw.Get("files")
		if !has {
			continue
		}
		arr, ok := files.([]any)
		if !ok {
			errs = append(errs, at+".files must be an array of repo-relative paths.")
			continue
		}
		for _, f := range arr {
			s, isStr := f.(string)
			if !isStr || !knownPaths[s] {
				errs = append(errs, at+".files contains "+jsonStringify(f)+", which is not one of the files listed in the working-tree state. Use only paths from those lists.")
			}
		}
	}
	return errs
}
