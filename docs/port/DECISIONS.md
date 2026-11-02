# Port decisions

Every decision the brief did not dictate. One entry each: context, options, choice, why.

## 001 — Working branch
- **Context:** The brief says `feat/wails-v3-port`. The session harness pins development to
  `claude/wizardly-hypatia-gttsk8` and forbids pushing elsewhere without explicit permission.
- **Options:** (a) `feat/wails-v3-port`; (b) the harness branch.
- **Choice:** (b) `claude/wizardly-hypatia-gttsk8`. The tree was clean, so no worktree was needed.
- **Why:** The harness rule is a hard constraint of this environment. The branch name has no effect on
  the code; the draft PR targets `main` from it all the same.

## 002 — No `gh` CLI in this environment
- **Context:** The brief uses `gh release list`, `gh run watch`, `gh secret set`, `gh pr create`.
  This session has no authenticated `gh` (search only) and uses the GitHub MCP tools instead.
- **Choice:** Wails releases were read with `go list -m -versions` and `git ls-remote`; the Wails docs
  were read from a shallow clone of `wailsapp/wails@v3.1.0`. CI runs are read with the MCP `actions_*`
  tools. The PR is opened with the MCP `create_pull_request` tool (draft). `gh secret set` is not
  possible: see the updater-key entry.

## 003 — Wails version: v3.1.0
- **Context:** `go list -m -versions github.com/wailsapp/wails/v3` on 2026-11-02 lists `v3.1.0` and
  `v3.2.0-nightly.20261101`. Nightlies are forbidden.
- **Choice:** `v3.1.0` everywhere: `wails3` CLI, `go.mod`, `@wailsio/runtime` (exact, no caret).

## 004 — Go toolchain
- **Context:** The container ships go1.24.7 (not 1.26.1 as the brief assumed). Wails needs ≥ 1.26.
- **Choice:** `go 1.26.0` + `toolchain go1.26.10` (newest 1.26.x on the module proxy) in `go.mod`;
  the go command fetches it. The system Go is untouched.

## 005 — Reference template name
- `wails3 init -l` lists `react` as "React + TypeScript + Vite"; there is no `react-ts`. Used `react`.

## 006 — Go module path
- `git remote -v` → `https://github.com/GabrySpada/gitcito`. Module: `github.com/GabrySpada/gitcito`.

## 007 — Updater release source
- `package.json` `build.publish` is `{ provider: github, owner: MyAppDesk, repo: gitcito }`. The Wails
  updater points at the same `MyAppDesk/gitcito`, so 5.x users follow the same release feed.

## 008 — Commit trailers
- The owner's rule (brief §3.2): no `Co-Authored-By: Claude`, no "Generated with" line. Commits carry none.

## 009 — Linux build dependencies in the container
- Installed `libgtk-4-dev`, `libwebkitgtk-6.0-dev`, `xvfb` with apt so `wails3 build` (CGO, GTK4) runs
  here. `wails3 doctor` reports the system ready.

## 010 — Baseline bundle size measured on Linux, not macOS
- **Context:** The brief asks for `electron-builder --mac --dir`. This container is Linux; macOS packaging
  of an Electron app cannot run here.
- **Choice:** `electron-builder --linux --dir` → `linux-unpacked` = **543 MB**. The Wails Linux binary is
  compared against that number; the macOS comparison is listed in MANUAL-CHECKS.md.

## 011 — Git goldens are captured by each slice's porting agent, before its Go code
- **Context:** §4.2 wants goldens committed in M0, before any Go. Capturing ~274 methods needs the same
  deep reading of `git.ts` that porting them does.
- **Choice:** Each Wave 1 slice agent first writes its TS capture test and goldens (committed as their own
  `test(port): …` commit), then ports, then replays. The rule "goldens come from TS and are fixed before the
  Go code exists" holds per slice; only the calendar order differs from the brief.
- **Why:** Halves the reading cost and puts the golden author next to the code that must satisfy it.

## 012 — Wave 0 delegated in parts
- **Context:** §9.3 says Wave 0 is done by the lead. The foundation is large (gitx alone mirrors ~1.5k TS lines).
- **Choice:** The lead writes `internal/app`, `main.go`, `wails.yaml` and integrates; a subagent writes
  `gitx`/`paths`/`logbook`/`testutil`/lock skeleton, another the renderer bridge skeleton, in the main tree on
  disjoint files. Shared files remain the lead's.

## 013 — Bound services are facades over unbound cores
- **Context:** Wails binds every exported method of a registered service. The first `AppService` exported
  `HandleSecondInstance`, which would have let page script forge a CLI hand-off (`--edit=<any file>`) and then
  overwrite that file through `cli:finishEdit`.
- **Choice:** Each registered type exposes exactly its renderer channels. State and lifecycle hooks live on an
  unregistered type (`app.Core`, `logbook.Logbook`); the registered one (`AppService`, `logbook.Service`) wraps it.
  Wave briefs require `// channel: <name>` above each facade method; SURFACE.md is generated from those.

## 014 — CLI ready gate is the first cli:open-path subscription
- **Context:** Electron delivered `cli:open-path` after `did-finish-load`. Wails events emitted before a listener
  exists are lost.
- **Choice:** Go queues CLI deliveries until `AppService.Ready()`. The bridge calls it right after subscribing the
  `cli:open-path` listener (App.tsx subscribes `onEdit` in the same commit phase, before the call resolves), so
  no extra renderer change is needed.

## 015 — Single instance off when Linux has no D-Bus session bus
- **Context:** Wails' Linux single-instance uses D-Bus; without a session bus or `dbus-launch` it aborts startup.
- **Choice:** `app.SingleInstanceAvailable()` skips the option in that case and logs a warning. A second launch
  then opens a second window instead of the app refusing to start. Listed in MANUAL-CHECKS.

## 016 — Go module fence for examples/
- The generated playground repos contain sample `.go` files. `examples/go.mod` makes `examples/` its own module
  so `go build ./...` / `go vet ./...` / `go test ./...` skip them.

## 017 — Bindings are committed under src/renderer/src/bindings
- Generated with `wails3 generate bindings -ts -i -clean -d src/renderer/src/bindings` (TypeScript interfaces).
  CI regenerates and fails on drift (M8).
