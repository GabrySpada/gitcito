package launch

// LaunchConfig is one entry from a `.vscode/launch.json` `configurations`
// array (src/shared/types.ts LaunchConfig). It stays the decoded JSON object:
// the TS type is open (`[key: string]: unknown`), discovery hands every field
// through untouched, and platform overrides and `${input:…}` substitution work
// on arbitrary keys.
type LaunchConfig = map[string]any

// LaunchTask is one entry from a `.vscode/tasks.json` `tasks` array — open for
// the same reasons as LaunchConfig.
type LaunchTask = map[string]any

// LaunchInput is one `${input:id}` definition from a launch.json `inputs`
// array, passed through as written.
type LaunchInput = map[string]any

// LaunchGroup is all launch configs discovered under one `.vscode/` folder.
type LaunchGroup struct {
	// ID is the absolute workspace-folder path (parent of `.vscode`).
	ID  string `json:"id"`
	Dir string `json:"dir"`
	// Label is "Workspace" for the repo root, else the relative path.
	Label   string            `json:"label"`
	IsRoot  bool              `json:"isRoot"`
	Configs []LaunchConfig    `json:"configs"`
	Tasks   []LaunchTask      `json:"tasks"`
	Inputs  []LaunchInput     `json:"inputs"`
	Scripts map[string]string `json:"scripts"`
}

// RunPayload is the `launch:run` request.
type RunPayload struct {
	Dir    string       `json:"dir"`
	Config LaunchConfig `json:"config"`
	// Configs are the sibling configs a compound resolves members from; absent
	// means "just this config".
	Configs     []LaunchConfig    `json:"configs,omitempty"`
	Tasks       []LaunchTask      `json:"tasks"`
	InputValues map[string]string `json:"inputValues,omitempty"`
	// SkipTasks are task labels a compound already ran once up front.
	SkipTasks []string `json:"skipTasks,omitempty"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
}

// RunTasksPayload is the `launch:runTasks` request.
type RunTasksPayload struct {
	Dir         string            `json:"dir"`
	Tasks       []LaunchTask      `json:"tasks"`
	Labels      []string          `json:"labels"`
	InputValues map[string]string `json:"inputValues,omitempty"`
	Cols        int               `json:"cols"`
	Rows        int               `json:"rows"`
}

// RunResult is `{ id } | { error }`.
type RunResult struct {
	ID    int    `json:"id,omitempty"`
	Error string `json:"error,omitempty"`
}

// DevToolsFound is the payload of `launch:devtools:<id>`: the address a dev
// tool announced and the tool's name.
type DevToolsFound struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}
