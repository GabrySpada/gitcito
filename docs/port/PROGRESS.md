# Port progress

Re-read this file, PLAN.md, DECISIONS.md, BRIEF.md, FOUNDATION-API.md and the wave briefs after every context
compaction.

## Where we are
- Baseline: 1686 vitest tests (1684 pass, 2 skipped) in 42 files — `docs/port/baseline-tests.json`.
  Gate green at start. Linux Electron bundle: 543 MB unpacked.
- Toolchain: wails3 v3.1.0 at ~/go/bin, `GOTOOLCHAIN=go1.26.10`, GTK4/WebKitGTK 6.0 + xvfb installed.
- Native smoke works: `go build -o /tmp/claude-0/gitcito-bin .` then
  `GITCITO_DATA_DIR=$(mktemp -d) GITCITO_KEYCHAIN_SUFFIX=smoke timeout 20 xvfb-run -a /tmp/claude-0/gitcito-bin`
  → "window ready" logged, App chunk loads. (Do not `pkill -f gitcito` — it matches the shell itself.)
- Done and pushed:
  - M0: notes, BRIEF.md copy, inventory (184 preload members, 264 git methods), TEST-MAP generator, baseline.
  - M1: go.mod, wails.yaml (valid), main.go, vite.config.ts, bridgeApi.ts, bridge.ts, bindings committed.
  - Foundation: gitx, gitsvc lock/classify/service/oplog, paths, logbook, testutil (FOUNDATION-API.md).
  - Go services bound: AppService (bootstrap, version, releases, ready, finishEdit), DialogService, ShellService,
    menu.Service, logbook.Service. Editor `--wait` flow ported (Core, unbound).
  - Renderer adaptations: clipboard (58 sites), `--wails-draggable`, link guard, native file drop
    (`droppedFilePaths`), zoom, timelapse format, PDF fallback button.

## In flight (subagents)
- keychain + settings (main tree, no worktree): internal/keychain, internal/settings, internal/shared additions,
  test/port/vectors-settings.test.ts, testdata/vectors/{secrets,aiAccounts,settings}.json.
- Git slices A, B, C, D (worktrees, WAVE1-BRIEF.md): each commits goldens then the port in its worktree branch.

## Next
- Merge each slice branch: `git merge <branch>`, `go build ./... && go test ./internal/gitsvc/`, regenerate
  bindings, wire `bridge.git(method, ...args)` → GitService by name, full gate, commit, push.
- Hosting + credentials (Wave 1 remainder) when a slot frees.
- Wave 2 (WAVE2-BRIEF.md): ai; repochat+grounding+wiki; processes; secure/vault/analytics/info; registry.
- Wave 3: editor, difftool, ssh, cli + cmd/gitcito-cli, updater; semantic diff → renderer (needs slice B contract).
- Renderer subagent: D6 (remove DevToolsPage + 'devtools' page type, DebugToolbar button → openExternal gated by
  a shared isLoopbackUrl, i18n key `devtools.openExternal` reuse, handbook devtools.md in 16 locales, docs-map,
  screenshot), plus anything else left from §5.3.
- Bound-surface test + SURFACE.md generator (scan `// channel:` / `// gitsvc:` tags; assert every exported
  method of every registered service is tagged).

## Blockers
- None.
