# Port progress

Re-read this file, PLAN.md, DECISIONS.md and BRIEF.md after every context compaction.

## Where we are
- M0/M1/M2 running in parallel.
- Baseline: 1686 vitest tests (1684 pass, 2 skipped) in 42 files — `docs/port/baseline-tests.json`.
  Gate green at start (typecheck, lint:i18n, lint:docs 77 pages, build).
- Toolchain: wails3 v3.1.0 at ~/go/bin, go1.26.10 via GOTOOLCHAIN, GTK4/WebKitGTK 6.0 installed.
- Written by me: go.mod, docs/port/{BRIEF,PLAN,PROGRESS,DECISIONS}.md, scripts/port/testmap.mjs → TEST-MAP.md,
  internal/shared/safepath.go, internal/app/{shell,dialogs,detach_*}.go.

## In flight (subagents, main tree, no worktree)
- Inventory: scripts/port/inventory.mjs → docs/port/INVENTORY.md + inventory.json.
- Go foundation: internal/{gitx,paths,logbook,testutil}, internal/gitsvc/{lock,classify,service,oplog}.go.
- Renderer bridge skeleton: src/shared/bridgeApi.ts, infrastructure/bridge.ts, main.tsx, vite.config.ts.

## Next
- internal/app: AppService (bootstrap, version, releases, ready/cli queue), window options, single instance,
  main.go, wails.yaml → first native smoke (M1 exit).
- Wave 1 git slices A–D + hosting (goldens captured by each slice agent from TS first).

## Blockers
- None. (Push was blocked by a 403 until the owner reconnected GitHub; resolved.)
