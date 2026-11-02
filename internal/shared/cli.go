package shared

import (
	"regexp"
	"strconv"
	"strings"
)

// CliViews mirrors CLI_VIEWS in src/shared/cli.ts: the surfaces the CLI can ask
// the window to open once the repository is on screen.
var CliViews = []string{
	"absorb", "bisect", "blame", "changelog", "chat", "clean", "conflicts", "diff", "export", "file",
	"gitflow", "graph", "history", "hooks", "insights", "lfs", "ci", "maintenance", "objects", "purge",
	"reflog", "search", "settings", "show", "snapshots", "sparse", "stack", "stash", "subtree",
	"terminal", "timelapse", "time-machine", "todos",
}

// IsCliView reports whether v is one of CliViews.
func IsCliView(v string) bool {
	for _, c := range CliViews {
		if c == v {
			return true
		}
	}
	return false
}

// CliOpenPayload mirrors CliOpenPayload in src/shared/cli.ts. Optional fields
// are omitted from JSON so they arrive as `undefined`, not `null`.
type CliOpenPayload struct {
	Path  string `json:"path"`
	Name  string `json:"name,omitempty"`
	Group string `json:"group,omitempty"`
	View  string `json:"view,omitempty"`
	Arg   string `json:"arg,omitempty"`
	Line  int    `json:"line,omitempty"`
	Edit  string `json:"edit,omitempty"`
	Wait  string `json:"wait,omitempty"`
}

var lineDigits = regexp.MustCompile(`^\d{1,9}$`)

// ParseCliOpenArgs extracts the `--flag=value` switches the bundled shim passes
// on launch. Flags use the single-token `--flag=value` form so they survive any
// argv reordering between the shim and the running instance. Mirrors
// parseCliOpenArgs in src/shared/cli.ts; returns nil when there is no --open.
func ParseCliOpenArgs(argv []string) *CliOpenPayload {
	read := func(flag string) string {
		prefix := flag + "="
		for _, a := range argv {
			if strings.HasPrefix(a, prefix) {
				return a[len(prefix):]
			}
		}
		return ""
	}
	path := read("--open")
	if path == "" {
		return nil
	}
	p := &CliOpenPayload{
		Path:  path,
		Name:  read("--name"),
		Group: read("--group"),
		Arg:   read("--arg"),
		Edit:  read("--edit"),
		Wait:  read("--wait"),
	}
	// An unknown verb is dropped rather than passed on: a future shim talking to
	// an older app should still open the repository.
	if v := read("--view"); IsCliView(v) {
		p.View = v
	}
	if l := read("--line"); lineDigits.MatchString(l) {
		p.Line, _ = strconv.Atoi(l)
	}
	return p
}
