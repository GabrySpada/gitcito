// Command gitcito-cli is the `gitcito` command line: the headless verbs
// (doctor, status, repos, commit-check, config, editor, completions), the
// git textconv filter, and the launcher that opens a repository — or a file git
// is waiting on — in the Gitcito window.
//
// It replaces three Electron-era pieces: src/main/cliEntry.ts (run as plain
// Node through ELECTRON_RUN_AS_NODE), src/main/textconvCli.ts, and the
// launcher half of the resources/cli/gitcito shell script. It is pure Go with
// no Wails import and builds with CGO_ENABLED=0, so a verb never turns into a
// second app instance and a CI job can run `gitcito doctor` without a display.
//
// Inside the app bundle the binary is named gitcito-cli, not gitcito: macOS
// file systems are case-insensitive, so `gitcito` would collide with the
// `Gitcito` executable it sits next to. The PATH symlink the app installs is
// still called `gitcito` (internal/cli). Invoked through a link named
// `gitcito-textconv`, it is the textconv filter.
package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/GabrySpada/gitcito/internal/textconv"
)

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// headlessVerbs answer in the terminal; everything else opens the window.
// Kept in step with the case list of the Electron-era shim.
var headlessVerbs = map[string]bool{
	"doctor": true, "status": true, "repos": true, "commit-check": true, "config": true, "editor": true,
	"completions": true, "help": true, "--help": true, "-h": true, "--version": true, "-V": true,
	"--resolve": true,
}

func run(argv []string, stdout, stderr io.Writer) int {
	name := ""
	if len(argv) > 0 {
		name = strings.TrimSuffix(strings.ToLower(filepath.Base(argv[0])), ".exe")
	}
	var args []string
	if len(argv) > 1 {
		args = argv[1:]
	}
	// git runs the textconv filter with one argument: a path to (a temp copy
	// of) the file. Reached either through a `gitcito-textconv` link or the
	// one-line shim in resources/cli, which calls the `textconv` verb.
	if name == "gitcito-textconv" {
		return textconv.RunCLI(args, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "textconv" {
		return textconv.RunCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && headlessVerbs[args[0]] {
		return newTerm(stdout, stderr).main(args)
	}
	return newLauncher(stdout, stderr).main(args)
}
