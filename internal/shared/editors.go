package shared

import (
	"math"
	"regexp"
	"strings"
)

// External editors — the table of editors "Open in editor" knows how to launch,
// and the pure argv building that turns a template into a spawn argument list.
// Port of src/shared/editors.ts (the renderer keeps the TS copy to label its
// menus; testdata/vectors/editors.json holds both to the same answers).
//
// Jumping to a line needs the editor's own CLI (`code`, `subl`, `zed`, …). A
// macOS `.app` bundle can only be handed a path, so a bundle-launched editor
// opens the file at the top — the UI hides the "at line" wording in that case.

// EditorID is the TS EditorId union: a preset id or "custom".
type EditorID = string

// EditorSource says how an editor was located, which decides whether line
// jumping is possible: "cli" or "app".
type EditorSource = string

// EditorPreset is one editor Gitcito knows how to find and launch.
type EditorPreset struct {
	ID EditorID `json:"id"`
	// Name is the product name — never translated.
	Name string `json:"name"`
	// Bins are CLI executables to look for on PATH, best first.
	Bins []string `json:"bins"`
	// MacApps are macOS application bundles to fall back to, as found under /Applications.
	MacApps []string `json:"macApps"`
	// WinPaths are Windows executables, relative to a Program Files-style root.
	WinPaths []string `json:"winPaths"`
	// FileArgs is the argv template for a file. `{path}` `{line}` `{col}` are substituted.
	FileArgs string `json:"fileArgs"`
	// FolderArgs is the argv template for a directory.
	FolderArgs string `json:"folderArgs"`
}

// DetectedEditor is an editor found on this machine.
type DetectedEditor struct {
	ID   EditorID `json:"id"`
	Name string   `json:"name"`
	// Command is the absolute path to the executable, or to the `.app` bundle
	// when Source is "app".
	Command string       `json:"command"`
	Source  EditorSource `json:"source"`
}

// EditorSetting is the user's choice, persisted in settings.
type EditorSetting struct {
	ID EditorID `json:"id"`
	// Command is the executable or `.app` bundle. Resolved by detection for
	// presets, typed by the user for `custom`.
	Command string `json:"command"`
	// Name is the display name — the preset's product name, or whatever the
	// user called their custom command.
	Name   string       `json:"name"`
	Source EditorSource `json:"source"`
	// FileArgs (custom only) is the argv template for a file. Falls back to `{path}`.
	FileArgs string `json:"fileArgs,omitempty"`
	// FolderArgs (custom only) is the argv template for a directory. Falls back to `{path}`.
	FolderArgs string `json:"folderArgs,omitempty"`
}

// EditorTarget is what to open. Path is absolute; Line/Col are 1-based and
// ignored when the editor cannot accept them (zero means unset, as in TS where
// 0 and undefined are both falsy).
type EditorTarget struct {
	Path  string  `json:"path"`
	Line  float64 `json:"line,omitempty"`
	Col   float64 `json:"col,omitempty"`
	IsDir bool    `json:"isDir,omitempty"`
	// Repo is the repository root, for a custom template that wants the workspace too.
	Repo string `json:"repo,omitempty"`
}

// EditorVars are the values substituted into an argv template.
type EditorVars struct {
	Path string  `json:"path"`
	Line float64 `json:"line,omitempty"`
	Col  float64 `json:"col,omitempty"`
	// Repo is the repository root — useful in a custom template that opens a workspace.
	Repo string `json:"repo,omitempty"`
}

// EditorLaunchPlan is which executable to run with which arguments.
type EditorLaunchPlan struct {
	Command string       `json:"command"`
	Args    []string     `json:"args"`
	Source  EditorSource `json:"source"`
}

// EditorPresets is EDITOR_PRESETS, in the same order (detection reports in it).
var EditorPresets = []EditorPreset{
	{
		ID:       "vscode",
		Name:     "Visual Studio Code",
		Bins:     []string{"code", "code-insiders"},
		MacApps:  []string{"Visual Studio Code.app"},
		WinPaths: []string{`Microsoft VS Code\Code.exe`},
		// `-g` is VS Code's goto form; without it the `path:line` suffix is read
		// as part of the filename.
		FileArgs:   "-g {path}:{line}:{col}",
		FolderArgs: "{path}",
	},
	{
		ID:         "cursor",
		Name:       "Cursor",
		Bins:       []string{"cursor"},
		MacApps:    []string{"Cursor.app"},
		WinPaths:   []string{`cursor\Cursor.exe`},
		FileArgs:   "-g {path}:{line}:{col}",
		FolderArgs: "{path}",
	},
	{
		ID:         "windsurf",
		Name:       "Windsurf",
		Bins:       []string{"windsurf"},
		MacApps:    []string{"Windsurf.app"},
		WinPaths:   []string{`Windsurf\Windsurf.exe`},
		FileArgs:   "-g {path}:{line}:{col}",
		FolderArgs: "{path}",
	},
	{
		ID:         "zed",
		Name:       "Zed",
		Bins:       []string{"zed"},
		MacApps:    []string{"Zed.app"},
		WinPaths:   []string{`Zed\zed.exe`},
		FileArgs:   "{path}:{line}:{col}",
		FolderArgs: "{path}",
	},
	{
		ID:         "sublime",
		Name:       "Sublime Text",
		Bins:       []string{"subl"},
		MacApps:    []string{"Sublime Text.app"},
		WinPaths:   []string{`Sublime Text\subl.exe`},
		FileArgs:   "{path}:{line}:{col}",
		FolderArgs: "{path}",
	},
	{
		ID:         "jetbrains",
		Name:       "JetBrains IDE",
		Bins:       []string{"idea", "webstorm", "pycharm", "rustrover", "goland", "clion", "rider", "phpstorm"},
		MacApps:    []string{"IntelliJ IDEA.app", "WebStorm.app", "PyCharm.app", "RustRover.app", "GoLand.app", "CLion.app"},
		WinPaths:   []string{},
		FileArgs:   "--line {line} --column {col} {path}",
		FolderArgs: "{path}",
	},
	{
		ID:       "xcode",
		Name:     "Xcode",
		Bins:     []string{"xed"},
		MacApps:  []string{"Xcode.app"},
		WinPaths: []string{},
		// `xed --line N file` opens the file at that line in Xcode.
		FileArgs:   "--line {line} {path}",
		FolderArgs: "{path}",
	},
}

// PresetFor is the preset with that id, or nil (custom, or unknown).
func PresetFor(id EditorID) *EditorPreset {
	for i := range EditorPresets {
		if EditorPresets[i].ID == id {
			return &EditorPresets[i]
		}
	}
	return nil
}

// SupportsLine says whether this editor can be told which line to land on.
// Only when it was found as a CLI — an `.app` bundle is launched through
// `open`, which forwards a path and nothing else.
func SupportsLine(setting EditorSetting) bool {
	if setting.Source != "cli" {
		return false
	}
	template := ""
	if setting.ID == "custom" {
		template = setting.FileArgs
	} else if p := PresetFor(setting.ID); p != nil {
		template = p.FileArgs
	}
	return template != "" && strings.Contains(template, "{line}")
}

// tokenize splits an argv template on whitespace, keeping "quoted runs" together.
func tokenize(template string) []string {
	out := []string{}
	var current strings.Builder
	var quote rune
	started := false
	for _, ch := range template {
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				current.WriteRune(ch)
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			started = true
			continue
		}
		if jsIsSpace(ch) {
			if started {
				out = append(out, current.String())
			}
			current.Reset()
			started = false
			continue
		}
		current.WriteRune(ch)
		started = true
	}
	if started {
		out = append(out, current.String())
	}
	return out
}

var (
	editorPlaceholderRe = regexp.MustCompile(`\{(path|line|col|repo)\}`)
	editorPathRe        = regexp.MustCompile(`\{path\}`)
	editorLineRe        = regexp.MustCompile(`\{line\}`)
	editorColRe         = regexp.MustCompile(`\{col\}`)
	editorRepoRe        = regexp.MustCompile(`\{repo\}`)
	trailingColonsRe    = regexp.MustCompile(`:+$`)
)

// jsPositive is TS's `n && n > 0` on a number.
func jsPositive(n float64) bool { return n > 0 && !math.IsNaN(n) }

// EditorArgs turns an argv template into a concrete argument list. Tokens are
// substituted, never re-split afterwards, so a path containing spaces stays
// one argument. A token whose placeholders all resolve to nothing is dropped —
// that is what keeps `--line {line}` from degrading into a bare `--line`.
//
// The substitution is `String.prototype.replace` with a string replacement, so
// `$&`, `$$`, … inside a value expand exactly as they did in TypeScript (the
// vectors pin it).
func EditorArgs(template string, vars EditorVars) []string {
	line := ""
	if jsPositive(vars.Line) {
		line = jsNumberString(vars.Line)
	}
	// A column without a line is meaningless — and `path::3` is a filename no
	// editor recognises — so the column only survives when the line does.
	col := ""
	if line != "" {
		col = "1"
		if jsPositive(vars.Col) {
			col = jsNumberString(vars.Col)
		}
	}
	args := []string{}
	for _, token := range tokenize(template) {
		hadPlaceholder := editorPlaceholderRe.MatchString(token)
		value := JSReplaceAll(editorPathRe, token, vars.Path)
		value = JSReplaceAll(editorLineRe, value, line)
		value = JSReplaceAll(editorColRe, value, col)
		value = JSReplaceAll(editorRepoRe, value, vars.Repo)
		// A bare `{line}` that resolved to nothing takes its flag with it:
		// leaving `--line` behind makes the CLI read the following path as the
		// flag's value.
		if hadPlaceholder && JSTrim(value) == "" {
			if n := len(args); n > 0 && strings.HasPrefix(args[n-1], "-") {
				args = args[:n-1]
			}
			continue
		}
		// `{path}:{line}:{col}` with no line collapses to a trailing `::`.
		args = append(args, trailingColonsRe.ReplaceAllString(value, ""))
	}
	return args
}

// EditorLaunch is the full launch plan for a target: which executable, which
// arguments. Nil when the setting carries no command, i.e. nothing is
// configured yet.
func EditorLaunch(setting EditorSetting, target EditorTarget) *EditorLaunchPlan {
	if setting.Command == "" {
		return nil
	}
	preset := PresetFor(setting.ID)
	fileArgs, folderArgs := "{path}", "{path}"
	if setting.ID == "custom" {
		if setting.FileArgs != "" {
			fileArgs = setting.FileArgs
		}
		if setting.FolderArgs != "" {
			folderArgs = setting.FolderArgs
		}
	} else if preset != nil {
		fileArgs, folderArgs = preset.FileArgs, preset.FolderArgs
	}
	template := fileArgs
	if target.IsDir {
		template = folderArgs
	}
	vars := EditorVars{Path: target.Path, Col: target.Col, Repo: target.Repo}
	if SupportsLine(setting) {
		vars.Line = target.Line
	}
	return &EditorLaunchPlan{Command: setting.Command, Args: EditorArgs(template, vars), Source: setting.Source}
}
