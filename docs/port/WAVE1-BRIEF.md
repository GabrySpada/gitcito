# Wave 1 porting brief (git service slices)

You port one slice of `src/main/git.ts`'s `gitService` to Go, behaviour-identical. Read first:
`docs/port/BRIEF.md` (approved design), `CLAUDE.md` §2/§4/§5/§6, `docs/port/WAVE1.md` (your method list),
`.claude/skills/git-operation/SKILL.md` and `.claude/skills/playground-fixture/SKILL.md` (intent),
and the foundation packages you build on — read their code, it is the API:
`internal/gitx` (runners, NoPromptEnv, FixPath, parsers, LRU…), `internal/gitsvc/{service,lock,classify,oplog}.go`,
`internal/testutil` (Playground, CloneFixture, NoNulls, git helpers), `internal/paths`, `internal/logbook`.
**Read `docs/port/FOUNDATION-API.md` first: it fixes naming, classification and the lock helpers.**

## Environment
- You run in your own git worktree. `node_modules` is not there: run `ln -s /home/user/gitcito/node_modules node_modules`
  in the worktree root (do not commit it — it is gitignored). Generate the playground once in your worktree with
  `bash examples/setup-playground.sh` (or let the vitest global setup / testutil do it).
- Go: always `export GOTOOLCHAIN=go1.26.10`. Module `github.com/GabrySpada/gitcito`.

## Steps
1. **Goldens first, from TypeScript.** Write `test/port/goldens-<slice>.test.ts` (vitest; the existing electron stub
   and `test/fixtures.ts` work there). For every method in your slice: call the TS `gitService` method against
   playground fixtures with fixed arguments — READ methods on the shared playground (read-only), WRITE methods on a
   `cloneFixture()` copy followed by a repo-state snapshot (`git status --porcelain=v2`, `for-each-ref`,
   `log --format=%H%x00%P%x00%s --all`, `stash list`, index via `ls-files -s`, plus the return value) — and write
   `internal/gitsvc/testdata/goldens/<method>/<fixture>-<case>.json` = `{method, fixture, args, result | error, state?}`.
   Normalize absolute paths to `<REPO>`, `<HOME>`, `<TMP>`. Prefer deterministic scenarios (most pin GIT_*_DATE; check
   `examples/scenarios/*.sh`); normalize wall-clock values only where unavoidable, and say which. Writing is gated on
   `PORT_WRITE_GOLDENS=1`; without it the test re-runs TS and asserts it still matches the files (so vitest guards
   the goldens permanently while TS exists). Cover error paths too (error message strings must match exactly).
   Cover every method; where a method cannot be exercised offline (network remotes, interactive), use a local bare
   remote fixture (`file://`) or document precisely why not in your report.
2. **Port.** Go code in `internal/gitsvc/<domain>.go` files named for your slice's domains. Each `GitService`
   method: exported, `ctx context.Context` first, then the TS args in order (repo path first — it is the lock key),
   wrapped by `locked`/`lockedErr`/`lockedNoRepo` (classification is already complete in `classify.go`; do not
   edit it). Above each exported method put `// gitsvc: <tsMethodName>` (the inventory scanner uses it).
   Return structs whose JSON tags match `src/shared/types.ts` exactly; nil slices → `[]`; optional TS fields
   (`?:`) → `omitempty`/pointer; `T | null` → pointer without omitempty. Error strings byte-identical to TS
   (renderer parses `*_PREFIX` markers). Port the TS why-comments with the logic. Activity/op-log recording and
   events (`clone:progress`, `bisect:output`) go through the foundation's emitter/oplog.
   **Helper naming:** other slices write into the same package in parallel. Any unexported helper you add that is
   not a method on a type you own must be prefixed with your slice letter in lowercase (`aParseStatus`,
   `bRangeArgs`), unless it already exists in `internal/gitx` (use that). If you believe a helper belongs in
   `gitx`, put it in your slice prefix anyway and list it in your report for the lead to hoist.
3. **Replay.** `internal/gitsvc/<slice>_golden_test.go` loads every golden for your methods, runs the Go method
   on the same fixture (clone for writes), applies the same normalization, and compares JSON semantically
   (and the state snapshot for writes). Also run `testutil.NoNulls` on every result. Port every existing vitest
   case that covers your methods (grep `test/*.test.ts` for each method name) as Go tests.
4. **Gate:** `gofmt -l internal` empty, `go vet ./internal/gitsvc/`, `go test -race ./internal/gitsvc/`,
   staticcheck (`go run honnef.co/go/tools/cmd/staticcheck@2026.2.2 ./internal/gitsvc/` or the version the
   foundation used — see go.mod/notes), and `npx vitest run test/port/goldens-<slice>.test.ts` all green.
5. **Commit in your worktree branch** in two commits: `test(port): capture slice <x> git goldens from typescript`
   then `feat(port): port git slice <x> to go`. Lowercase subject, no Co-Authored-By/attribution trailers.
   Do not push. The lead merges your branch.

## Constraints
- Edit only: `internal/gitsvc/*` files you create for your slice, `internal/gitsvc/testdata/goldens/<your methods>/`,
  `test/port/goldens-<slice>.test.ts`. Never edit the foundation files, another slice's files, the renderer,
  `main.go`, `go.mod` (if you truly need a dependency, list it in the report instead), or TS sources.
- Never mutate the shared playground; tests use clones. Tests never touch real global git config or `~/.ssh`.
- Do not ask questions. Decide and list decisions in your report.

## Report (final message)
Files created; every exported method signature with its TS name; which methods are read/write; behaviours you
could not reproduce exactly and why; helpers you'd hoist to gitx; test counts (goldens, Go tests); a TEST-MAP
fragment (vitest file + full test name → Go test name) for every existing TS test case you ported; the branch name
and the two commit SHAs.
