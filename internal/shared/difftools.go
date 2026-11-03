package shared

import (
	"sort"
	"strings"
)

// External diff and merge tools — port of src/shared/diffTools.ts
// (testdata/vectors/diffTools.json holds both to the same answers).
//
// Gitcito does not keep its own table of Kaleidoscope/Beyond Compare/Meld
// invocations: git already ships one, knows which of them are installed, and
// lets a user add their own with `difftool.<name>.cmd`. So the whole feature is
// a reader for `git difftool --tool-help` plus two commands. Anything the user
// has already configured for the CLI works here unchanged.

// DiffToolInfo is one tool git offers.
type DiffToolInfo struct {
	// ID is the `--tool=` value, e.g. `kdiff3`.
	ID string `json:"id"`
	// Description is git's own one-line description.
	Description string `json:"description"`
	// Available is true when git found the tool on this machine.
	Available bool `json:"available"`
}

// DiffToolConfig is what the settings UI and the per-file actions need to know.
type DiffToolConfig struct {
	// DiffTool is `diff.tool`, empty when unset.
	DiffTool string `json:"diffTool"`
	// MergeTool is `merge.tool`, empty when unset.
	MergeTool string `json:"mergeTool"`
	// DiffTools are the tools offered for diffing.
	DiffTools []DiffToolInfo `json:"diffTools"`
	// MergeTools are the tools offered for merging — git's two lists differ.
	MergeTools []DiffToolInfo `json:"mergeTools"`
	// KeepBackup is `mergetool.keepBackup`; when true a `.orig` file is left behind.
	KeepBackup bool `json:"keepBackup"`
}

var (
	notAvailableRe = MustJSRegexp(`not currently available`, true)
	toolIndentRe   = MustJSRegexp(`^\s+\S`, false)
	toolTrailRe    = MustJSRegexp(`\s+$`, false)
	toolRowRe      = MustJSRegexp(`^\s+(\S+)\s*(.*)$`, false)
	toolIDRe       = MustJSRegexp(`^[\w.+-]+$`, false)
)

// ParseToolHelp parses `git difftool --tool-help` / `git mergetool --tool-help`:
//
//	'git difftool --tool=<tool>' may be set to one of the following:
//	        opendiff         Use FileMerge (requires a graphical session)
//
//	The following tools are valid, but not currently available:
//	        meld             Use Meld (requires a graphical session)
//
// Everything before the "not currently available" heading is installed; what
// follows is a catalogue git knows but cannot find. Trailing prose after the
// lists is not indented, which is what separates it from a tool row.
func ParseToolHelp(stdout string) []DiffToolInfo {
	tools := []DiffToolInfo{}
	available := true
	for _, raw := range strings.Split(stdout, "\n") {
		if notAvailableRe.MatchString(raw) {
			available = false
			continue
		}
		// Tool rows are indented; headings and the closing prose are not.
		if !toolIndentRe.MatchString(raw) {
			continue
		}
		m := toolRowRe.FindStringSubmatch(toolTrailRe.ReplaceAllString(raw, ""))
		if m == nil {
			continue
		}
		id, description := m[1], m[2]
		// The closing prose is indented on some git builds ("Some of the tools…").
		if !toolIDRe.MatchString(id) {
			continue
		}
		tools = append(tools, DiffToolInfo{ID: id, Description: JSTrim(description), Available: available})
	}
	return tools
}

// SortTools puts installed tools first, then the rest, each alphabetically
// (localeCompare; a stable sort, like V8's).
func SortTools(tools []DiffToolInfo) []DiffToolInfo {
	out := append([]DiffToolInfo{}, tools...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Available == b.Available {
			return LocaleCompare(a.ID, b.ID) < 0
		}
		return a.Available
	})
	return out
}
