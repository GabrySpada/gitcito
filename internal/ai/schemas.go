package ai

// JSON contracts for the AI features (src/main/aiSchemas.ts): the schema sent
// to providers that support structured output, and a validator that decides
// whether a reply is usable. Validators return one correction line per
// problem — those lines are what the model is shown when it is asked to try
// again.
//
// Schemas are built as ordered objects: their serialization is part of the
// request body, so key order matters.

import (
	"regexp"
	"strconv"
	"strings"
)

var hexColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func strType() *Obj { return newObj("type", "string") }

func strArray() *Obj { return newObj("type", "array", "items", strType()) }

func anyStrings(keys []string) []any {
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out
}

// stringMapSchema builds an object schema whose properties are all required
// strings.
func stringMapSchema(keys []string) *Obj {
	props := newObj()
	for _, k := range keys {
		props.Set(k, strType())
	}
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings(keys), "properties", props)
}

// ─── Commit messages ────────────────────────────────────────────────────────

func commitSchema() *Obj {
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"summary", "description"}),
		"properties", newObj("summary", strType(), "description", strType()))
}

func validateCommitMessage(value any) []string {
	root := asObj(value)
	if root == nil {
		return []string{"The response must be a JSON object."}
	}
	errs := []string{}
	summary := root.V("summary")
	if !nonEmptyString(summary) {
		errs = append(errs, `"summary" must be a non-empty one-line commit subject.`)
	} else if strings.Contains(summary.(string), "\n") {
		errs = append(errs, `"summary" must be a single line.`)
	}
	if d, has := root.Get("description"); has && d != nil {
		if _, ok := d.(string); !ok {
			errs = append(errs, `"description" must be a string (use "" when there is no body).`)
		}
	}
	return errs
}

// ─── Generated project config files ─────────────────────────────────────────

var driveLetterRE = regexp.MustCompile(`^[a-zA-Z]:`)

// IsSafeRepoPath reports whether a model-supplied path stays inside the repo.
// Paths come back from the LLM and are joined onto the repo root before
// writing, so `..` segments, absolute paths and Windows drive letters have to
// be refused.
//
// This is the TS check exactly (`typeof` guard, JS trim, UTF-16 length);
// shared.IsSafeRepoPath trims with strings.TrimSpace, which differs on U+FEFF
// and U+0085, so model-facing validation here does not use it.
func IsSafeRepoPath(path any) bool {
	s, ok := path.(string)
	if !ok {
		return false
	}
	p := jsTrim(s)
	if p == "" || utf16Len(p) > 400 {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || driveLetterRE.MatchString(p) {
		return false
	}
	if strings.Contains(p, "\x00") {
		return false
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	return true
}

func configFilesSchema() *Obj {
	file := newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"path", "content"}),
		"properties", newObj("path", strType(), "content", strType()))
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"files"}),
		"properties", newObj("files", newObj("type", "array", "items", file)))
}

func validateGeneratedFiles(value any, requested []string) []string {
	root := asObj(value)
	if root == nil {
		return []string{"The response must be a JSON object."}
	}
	files, ok := root.V("files").([]any)
	if !ok {
		return []string{`"files" must be an array of {path, content} objects.`}
	}
	if len(files) == 0 {
		return []string{`"files" is empty — generate the files that were asked for.`}
	}
	errs := []string{}
	wanted := uniqueStrings(requested)
	for i, raw := range files {
		file := asObj(raw)
		at := "files[" + strconv.Itoa(i) + "]"
		if file == nil {
			errs = append(errs, at+` must be an object with "path" and "content".`)
			continue
		}
		path, hasPath := file.Get("path")
		if !IsSafeRepoPath(path) {
			shown := path
			if !hasPath {
				shown = nil
			}
			errs = append(errs, at+".path "+jsonStringify(shown)+` must be a relative path inside the repo — no leading "/" and no ".." segments.`)
		} else if len(wanted) > 0 && !inList(wanted, jsTrim(path.(string))) {
			errs = append(errs, at+".path "+jsonStringify(path)+" was not requested. Generate only: "+strings.Join(wanted, ", ")+".")
		}
		if !nonEmptyString(file.V("content")) {
			errs = append(errs, at+".content must be the complete file contents.")
		}
	}
	return errs
}

// uniqueStrings is `[...new Set(list)]`.
func uniqueStrings(list []string) []string {
	out := []string{}
	for _, s := range list {
		if !inList(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// ─── "Ask" actions ──────────────────────────────────────────────────────────

// Working-tree and ref bookkeeping — what every action surface may propose.
var localActionTypes = []string{"gitignore", "stage", "unstage", "commit", "stash", "discard", "branch", "checkout", "tag"}

// AskActionSchema is one entry of the Git-only AskAction union, as loose JSON
// Schema. History actions need branch and commit context, so only repository
// chat offers them; remote ones are offered only when the remote-actions
// setting is on — the model cannot describe what it is not given, so a
// disabled surface is unreachable rather than merely discouraged.
func AskActionSchema(allow ActionAllow) *Obj {
	types := append([]string{}, localActionTypes...)
	if allow.History {
		types = append(types, historyActionTypes...)
	}
	if allow.Remote {
		types = append(types, remoteAskActionTypes...)
	}
	boolType := func() *Obj { return newObj("type", "boolean") }
	return newObj(
		"type", "object",
		"required", anyStrings([]string{"type", "description"}),
		"properties", newObj(
			"type", newObj("type", "string", "enum", anyStrings(types)),
			"description", strType(),
			"files", strArray(),
			"patterns", strArray(),
			"hashes", strArray(),
			"message", strType(),
			"name", strType(),
			"at", strType(),
			"ref", strType(),
			"onto", strType(),
			"checkout", boolType(),
			"noFf", boolType(),
			"mode", newObj("type", "string", "enum", anyStrings([]string{"default", "ff-only", "rebase"})),
			"remote", strType(),
			"branch", strType(),
			"title", strType(),
			"body", strType(),
			"source", strType(),
			"target", strType(),
			"draft", boolType(),
			"leaf", strType(),
		),
	)
}

// askActionsSchema is ASK_ACTIONS_SCHEMA: the Ask planner remains local, it
// has no pull-request context to work from.
func askActionsSchema() *Obj { return newObj("type", "array", "items", AskActionSchema(ActionAllow{})) }

// AskActionsSchemaFor is askActionsSchema(remote): history allowed.
func AskActionsSchemaFor(remote bool) *Obj {
	return newObj("type", "array", "items", AskActionSchema(ActionAllow{History: true, Remote: remote}))
}

// fileActionSchemas are the three file mutations, shared by every
// repository-chat action schema.
func fileActionSchemas() []any {
	return []any{
		newObj("type", "object", "additionalProperties", false,
			"required", anyStrings([]string{"type", "path", "oldText", "newText", "description"}),
			"properties", newObj("type", newObj("const", "edit_file"), "path", strType(), "oldText", strType(),
				"newText", strType(), "replaceAll", newObj("type", "boolean"), "description", strType())),
		newObj("type", "object", "additionalProperties", false,
			"required", anyStrings([]string{"type", "path", "content", "mode", "description"}),
			"properties", newObj("type", newObj("const", "write_file"), "path", strType(), "content", strType(),
				"mode", newObj("type", "string", "enum", anyStrings([]string{"create", "replace"})), "description", strType())),
		newObj("type", "object", "additionalProperties", false,
			"required", anyStrings([]string{"type", "path", "description"}),
			"properties", newObj("type", newObj("const", "delete_file"), "path", strType(), "description", strType())),
	}
}

// RepoChatActionsSchema is repoChatActionsSchema(remote): repository chat may
// propose a file-action prefix followed by Git actions.
func RepoChatActionsSchema(remote bool) *Obj {
	anyOf := append([]any{AskActionSchema(ActionAllow{History: true, Remote: remote})}, fileActionSchemas()...)
	return newObj("type", "array", "maxItems", 64.0, "items", newObj("anyOf", anyOf))
}

// ─── Smart staging ──────────────────────────────────────────────────────────

func smartStageSchema() *Obj {
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"toStage", "reason"}),
		"properties", newObj("toStage", strArray(), "reason", strType()))
}

func validateSmartStage(value any, known map[string]bool) []string {
	root := asObj(value)
	if root == nil {
		return []string{"The response must be a JSON object."}
	}
	toStage, ok := root.V("toStage").([]any)
	if !ok {
		return []string{`"toStage" must be an array of paths (use [] to stage nothing).`}
	}
	errs := []string{}
	for _, p := range toStage {
		if s, isStr := p.(string); !isStr || !known[s] {
			errs = append(errs, `"toStage" contains `+jsonStringify(p)+", which is not one of the changed files you were given.")
		}
	}
	if r, has := root.Get("reason"); has {
		if _, isStr := r.(string); !isStr {
			errs = append(errs, `"reason" must be a string.`)
		}
	}
	return errs
}

// ─── Suggested config artifacts ─────────────────────────────────────────────

func artifactSuggestionsSchema() *Obj {
	item := newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"path", "description", "reason"}),
		"properties", newObj("path", strType(), "description", strType(), "reason", strType()))
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"suggestions"}),
		"properties", newObj("suggestions", newObj("type", "array", "items", item)))
}

func validateArtifactSuggestions(value any, alreadySelected []string) []string {
	root := asObj(value)
	if root == nil {
		return []string{"The response must be a JSON object."}
	}
	suggestions, ok := root.V("suggestions").([]any)
	if !ok {
		return []string{`"suggestions" must be an array (use [] when there is nothing to add).`}
	}
	errs := []string{}
	for i, raw := range suggestions {
		item := asObj(raw)
		at := "suggestions[" + strconv.Itoa(i) + "]"
		if item == nil {
			errs = append(errs, at+" must be an object.")
			continue
		}
		path := item.V("path")
		if !IsSafeRepoPath(path) {
			errs = append(errs, at+".path must be a relative path inside the repo.")
		} else if inList(alreadySelected, jsTrim(path.(string))) {
			errs = append(errs, at+".path "+jsonStringify(path)+" is already selected — suggest something else.")
		}
		if !nonEmptyString(item.V("description")) {
			errs = append(errs, at+".description must say what the file is for.")
		}
		if !nonEmptyString(item.V("reason")) {
			errs = append(errs, at+".reason must say why this project benefits from it.")
		}
	}
	return errs
}

// ─── Themes and palettes ────────────────────────────────────────────────────

// AppThemeKeys and CodeThemeKeys are the colours each theme variant carries.
var (
	AppThemeKeys = []string{
		"bg0", "bg1", "bg2", "bg3", "bg4",
		"border", "borderSoft",
		"text0", "text1", "text2",
		"accent", "green", "red", "yellow", "purple",
	}
	CodeThemeKeys = []string{
		"bg", "text", "comment", "keyword", "string", "number", "function", "title",
		"variable", "type", "builtin", "attr", "tag", "operator", "meta",
	}
)

func themeSchema(keys []string) *Obj {
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"name", "light", "dark"}),
		"properties", newObj("name", strType(), "light", stringMapSchema(keys), "dark", stringMapSchema(keys)))
}

// validateTheme: both variants must carry every colour, as a 6-digit hex value.
func validateTheme(value any, keys []string) []string {
	root := asObj(value)
	if root == nil {
		return []string{"The response must be a JSON object."}
	}
	errs := []string{}
	if !nonEmptyString(root.V("name")) {
		errs = append(errs, `"name" must be the theme name.`)
	}
	for _, variant := range []string{"light", "dark"} {
		colors := asObj(root.vals[variant])
		if colors == nil {
			errs = append(errs, `"`+variant+`" must be an object with all `+strconv.Itoa(len(keys))+" colours.")
			continue
		}
		var missing, bad []string
		for _, k := range keys {
			v, has := colors.Get(k)
			if !has {
				missing = append(missing, k)
			} else if !hexColorRE.MatchString(jsString(v)) {
				bad = append(bad, k)
			}
		}
		if len(missing) > 0 {
			errs = append(errs, `"`+variant+`" is missing: `+strings.Join(missing, ", ")+".")
		}
		if len(bad) > 0 {
			errs = append(errs, `"`+variant+`" must use 6-digit hex colours; these are not: `+strings.Join(bad, ", ")+".")
		}
	}
	return errs
}

func graphPaletteSchema() *Obj {
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"name", "colors"}),
		"properties", newObj("name", strType(), "colors", strArray()))
}

func validateGraphPalette(value any) []string {
	root := asObj(value)
	if root == nil {
		return []string{"The response must be a JSON object."}
	}
	errs := []string{}
	if !nonEmptyString(root.V("name")) {
		errs = append(errs, `"name" must be the palette name.`)
	}
	colors, ok := root.V("colors").([]any)
	if !ok {
		return append(errs, `"colors" must be an array of 8 hex colours.`)
	}
	if len(colors) != 8 {
		errs = append(errs, `"colors" must have exactly 8 entries, not `+strconv.Itoa(len(colors))+".")
	}
	var bad []any
	for _, c := range colors {
		if !hexColorRE.MatchString(jsString(c)) {
			bad = append(bad, c)
		}
	}
	if len(bad) > 0 {
		errs = append(errs, `"colors" must be 6-digit hex values; these are not: `+jsJoin(bad, ", ")+".")
	}
	return errs
}

// ─── Pull request description ───────────────────────────────────────────────

func prDescriptionSchema() *Obj {
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"title", "body"}),
		"properties", newObj("title", strType(), "body", strType()))
}

func validatePRDescription(value any) []string {
	root := asObj(value)
	if root == nil {
		return []string{"The response must be a JSON object."}
	}
	errs := []string{}
	title := root.V("title")
	if !nonEmptyString(title) {
		errs = append(errs, `"title" must be a one-line pull request title.`)
	} else if strings.Contains(title.(string), "\n") {
		errs = append(errs, `"title" must be a single line.`)
	}
	if !nonEmptyString(root.V("body")) {
		errs = append(errs, `"body" must be the Markdown description.`)
	}
	return errs
}

// ─── Branch names ───────────────────────────────────────────────────────────

func branchNameSchema() *Obj {
	return newObj("type", "object", "additionalProperties", false, "required", anyStrings([]string{"name"}),
		"properties", newObj("name", strType()))
}

var branchCharsRE = regexp.MustCompile(`^[A-Za-z0-9._\-/]+$`)

// validateBranchName: git's rules, plus the house style — lowercase, hyphens,
// no exotic characters.
func validateBranchName(value any) []string {
	root := asObj(value)
	if root == nil {
		return []string{"The response must be a JSON object."}
	}
	name := root.V("name")
	if !nonEmptyString(name) {
		return []string{`"name" must be the branch name.`}
	}
	n := jsTrim(name.(string))
	errs := []string{}
	if strings.IndexFunc(n, isJSSpace) >= 0 {
		errs = append(errs, `"name" must not contain spaces — use hyphens.`)
	}
	if !branchCharsRE.MatchString(n) {
		errs = append(errs, `"name" may only use letters, digits, ".", "_", "-" and "/".`)
	}
	if strings.HasPrefix(n, "/") || strings.HasSuffix(n, "/") || strings.Contains(n, "//") {
		errs = append(errs, `"name" must not start or end with "/" or contain "//".`)
	}
	if strings.HasSuffix(n, ".lock") || strings.Contains(n, "..") {
		errs = append(errs, `"name" must not contain ".." or end with ".lock".`)
	}
	if utf16Len(n) > 100 {
		errs = append(errs, `"name" must be shorter than 100 characters.`)
	}
	return errs
}

// ─── Merge conflict resolution (plain text, not JSON) ───────────────────────

// hasConflictMarker is /^(<{7}|={7}|>{7}|\|{7})/m: seven of the same marker
// at the start of any line (JS lines also break on \r, U+2028 and U+2029).
func hasConflictMarker(text string) bool {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c != '<' && c != '=' && c != '>' && c != '|') || !isLineTerminatorBefore(text, i) {
			continue
		}
		if i+7 <= len(text) && text[i:i+7] == strings.Repeat(string(c), 7) {
			return true
		}
	}
	return false
}

// validateResolvedFile: a resolved file must not still contain conflict
// markers.
func validateResolvedFile(text string) []string {
	if hasConflictMarker(text) {
		return []string{"The result still contains git conflict markers (<<<<<<<, =======, >>>>>>>). Return the merged file with none of them."}
	}
	if jsTrim(text) == "" {
		return []string{"The result is empty. Return the full merged file contents."}
	}
	return []string{}
}
