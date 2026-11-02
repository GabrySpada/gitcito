// Package gitx runs git (and the few helper processes git-backed features
// need) the way the Electron main process did in src/main/git.ts and
// src/main/fix-path.ts: explicit environment, bounded output, error strings
// byte-identical to what Node produced — the renderer parses some of them.
//
// Every runner here mirrors one TypeScript call shape:
//
//	Exec      child_process.execFile / spawn of any binary (the primitive)
//	Run       pexecFile('git', ['-C', repo, ...args], opts) — raw Node error kept
//	RunGit    runGit(): non-interactive, stderr becomes the error message
//	Raw       simpleGit(repo).raw(args): simple-git's very different exit rules
//	WithStdin gitWithStdin(): stdin in, stdout out, exit status ignored
//	ReadBlob  readBlobBuffer(): `git show <spec>` as raw bytes
//
// git stays a CLI subprocess (never go-git). Processes run in their own
// process group on Unix so cancelling a context kills hooks and helpers git
// started, not just git itself.
package gitx
