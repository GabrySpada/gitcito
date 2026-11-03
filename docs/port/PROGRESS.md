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
  - keychain + settings ported and bound (docs: subagent report in git log 34b4021).
  - Go services bound: AppService (bootstrap, version, releases, ready, finishEdit), DialogService, ShellService,
    menu.Service, logbook.Service. Editor `--wait` flow ported (Core, unbound).
  - Server build (`-tags server`, loopback-guarded) + e2e harness (`npm run test:e2e`, 8 tests: boot, settings,
    log, origin isolation). Wails debug log scrubbed of call args/results. ci.yml written.
  - Git slices A, B, C, D merged: **264/264 git methods** in Go, 954 goldens. Hook names validated
    (traversal fix). Snapshot guard reads settings via SetSnapshotGuardSetting.
  - (was) Git slices A, B, C merged (184 of 264 git methods), hosting merged and bound (30 channels).
    `bridge.git` dispatches through the generated GitService bindings (arity-safe). Semantic diff runs in the
    renderer (lib/semantic.ts; grammars under /tree-sitter; CSP + 'wasm-unsafe-eval').
  - Renderer adaptations: clipboard (58 sites), `--wails-draggable`, link guard, native file drop
    (`droppedFilePaths`), zoom, timelapse format, PDF fallback button.

## In flight
- Nothing. **All 163 IPC channels / 184 preload members reach Go; the bridge has no stubs left.**
  425 bound methods, all tagged (SURFACE.md). Native smoke OK (scripts/port/smoke.sh), e2e 17/17,
  vitest 2708 passed / 2 skipped, Go tests green.

## Next
- M6 review round (§9.4): code review, silent failures, parity auditor (INVENTORY row by row), security
  (SURFACE.md + §8), then fixes. Known items to fold in: grounding↔ai duplicates (+ jsonrepair dep),
  LocaleCompare/isSecretFile copies, a*/b*/c*/d* helper hoisting, two order-sensitive goldens.
- M7 Electron removal (round-trip crypto test already exists: TS opens Go bundles).
- M8 rest: CI cannot run (Actions off); screenshots harness on the server build; gen-licenses for Go modules.
- M9 docs: CLAUDE.md, skills, README, CONTRIBUTING, handbook in 16 locales (devtools.md!), site, ROADMAP,
  CHANGELOG 5.0.0, screenshots.
- M10 final reviews + draft PR. Owner action for the updater key (DECISIONS 024).

## Merged since the last update
- M3 exit: e2e core git flows + terminal streaming (e2e/git.e2e.ts; suite 17/17 on the server build).
- M8 started: wails.yaml release profile (config check OK; --plan OK for linux/amd64 + windows/amd64, darwin
  needs a macOS host), empty macOS entitlements, `npm run wails:*` / `bindings` scripts (scripts/wails.mjs),
  husky runs gofmt + go vet, frontend-dist/.gitkeep so a fresh clone builds, ci.yml builds before the
  playground exists (DECISIONS 021–023). nsis installed in the container for planning.
- Wave 2 complete: ai, repochat + grounding + wiki, processes (terminal/launch/devices/localci/analyze/todoscan),
  secure/vault/analytics/info, registry/watcher/repoconfig/fileactions/textconv. All bound in main.go and the
  bridge (408 bound methods, all tagged). Only difftool/ssh/editor/update/cli channels remain `notPorted`.
- Hardening during merges: secure-bundle writes refuse symlink escapes; `shared.IsSafeRepoPath` uses JS trim.
- Cleanup owed in M6: internal/grounding duplicates of internal/ai (ParseLooseJSON + jsonrepair dep, review/
  hover/ask-plan validators — see merge 528eb7b report); several `*LocaleCompare`/`isSecretFile` copies in
  gitsvc/secure; a*/b*/c*/d* helper hoisting; two gitsvc goldens (teammateRadar one-branch, notedCommits
  cherry-pick-two) may be order-flaky on a freshly generated playground.

## Next
- Merge each slice branch: `git merge <branch>`, `go build ./... && go test ./internal/gitsvc/`, regenerate
  bindings, wire `bridge.git(method, ...args)` → GitService by name, full gate, commit, push.
- After all slices: hoist duplicated helpers (a*/b*/c*/d* — see slice reports in git log) into gitx; wire
  analytics as gitsvc ActivityRecorder; the snapshot guard reads settings through internal/settings.
- Wave 2 (WAVE2-BRIEF.md): ai; repochat+grounding+wiki; processes; secure/vault/analytics/info; registry.
- Wave 3: editor, difftool, ssh, cli + cmd/gitcito-cli, updater; semantic diff → renderer (needs slice B contract).
- D6 code done (10a0b3e): DevToolsPage + 'devtools' page type removed, button → openExternal via
  src/shared/loopback.ts. **Still to do (M9 docs agent):** rewrite docs/help/devtools.md in all 16 locale dirs
  (keep the "which tools" table and the limits; it opens in the browser now) and regenerate/remove
  docs/screenshots/devtools.webp.
- Bound-surface test + SURFACE.md generator (scan `// channel:` / `// gitsvc:` tags; assert every exported
  method of every registered service is tagged).

## Blockers
- CI: `.github/workflows/ci.yml` pushed (99e588a), but the repository shows **zero** workflow runs of any
  workflow (MCP actions_list). Actions is probably disabled on GabrySpada/gitcito. Re-check after later pushes;
  if still zero, the PR lists it and local gate output stands in as evidence.
