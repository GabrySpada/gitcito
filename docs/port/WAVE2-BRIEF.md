# Wave 2/3 porting brief (non-git main-process modules)

You port one group of `src/main/*.ts` modules to Go, behaviour-identical. Read first:
`docs/port/BRIEF.md` (approved design), `docs/port/FOUNDATION-API.md`, `CLAUDE.md` §2/§4/§5/§6, the rows of
`docs/port/INVENTORY.md` for your channels (grep the channel prefixes), and the TS sources and their tests.

## Environment
- You run in your own git worktree. `ln -s /home/user/gitcito/node_modules node_modules` in the worktree root
  (gitignored; do not commit it). Generate the playground if you need it (`bash examples/setup-playground.sh`).
- Go: always `export GOTOOLCHAIN=go1.26.10`. Module `github.com/GabrySpada/gitcito`.
- Available Go packages: `internal/gitx`, `internal/gitsvc` (do not edit), `internal/paths`, `internal/logbook`,
  `internal/testutil`, `internal/shared` (add new files only), `internal/keychain` + `internal/settings`
  (if present in your worktree — read their exported API; if not yet merged, define a small interface for what you
  need, e.g. `type TokenSource interface{ … }`, and list it in your report so the lead wires the real one).

## Shape
- One package per domain under `internal/<pkg>`. Core logic is plain Go with injected dependencies
  (emitter `interface{ Emit(name string, data any) }`, settings/keychain accessors, an `*http.Client` /
  base URL for HTTP, a clock where time matters) so it is testable without Wails.
- The page-facing part is a **thin facade** `Service` struct (in `service.go`) whose exported methods are
  exactly the renderer channels of your modules — one method per channel, `ctx context.Context` first where the
  method does I/O or spawns. Every exported method on a registered service is callable from the page, so
  **nothing else may be an exported method on the facade**. Above each facade method: `// channel: <ipc channel>`.
  Start `service.go` with a comment listing the channels it serves.
- Fire-and-forget channels (`ipcRenderer.send`) become methods that return nothing (or an error that the bridge
  only logs). Events keep their Electron channel names exactly (including `term:data:<id>` patterns).
  Ordered high-rate output (terminal/launch data, localci data) keeps order: emit from one goroutine per
  session, in arrival order.
- JSON contract: tags match `src/shared/types.ts` exactly; nil slices → `[]`; `?:` → `omitempty`/pointer;
  `T | null` → pointer without omitempty; `[]byte` is base64 on the wire. Error strings byte-identical to TS.
- Port the TS why-comments with the logic. No package-level mutable state without a mutex.
- Security (BRIEF §8): validate every path/URL/argument as TS does (`shared.IsSafeRepoPath` for model/CLI paths);
  never log tokens; argument injection into spawned commands (`--` separators, refuse `-`-prefixed values where TS
  does); spawned processes are killed with their process group on cancel/stop.

## Contract capture first (goldens/vectors from TypeScript)
Before writing Go, write `test/port/<group>.test.ts` (vitest) that runs the TS code on fixed inputs and writes
JSON under `testdata/vectors/<module>.json` (pure logic) or `internal/<pkg>/testdata/` (I/O behaviour):
- **Pure logic** (parsers, schemas, prompt builders, crypto): input → output vectors.
- **Model-facing payloads** (AI): the exact request body each feature builds from fixed inputs (system prompt,
  messages, schema, model id, params) — Go must produce identical JSON, because prompt wording is product
  behaviour. Same for hosting requests (method, URL, headers minus auth, body).
- **Crypto/file formats**: TS encrypts fixed plaintexts with fixed salts/IVs → Go must decrypt and re-encrypt
  byte-for-byte.
Writing is gated on `PORT_WRITE_VECTORS=1`; otherwise the vitest file asserts TS still matches the files, so the
files guard both implementations permanently. Go tests replay the same files.

## Tests
- Port **every** existing vitest case that covers your modules (grep `test/*.test.ts` for the module/function
  names) to Go table-driven tests; report a TEST-MAP fragment (vitest file + full test name → Go test name).
- HTTP: `httptest.Server` fakes, never live network. Processes: real subprocesses against temp dirs.
- `-race` where there is concurrency (terminal, launch, watcher, localci, analyze).
- Tests never touch the real OS keychain (`keyring.MockInit()`), real data dir (`GITCITO_DATA_DIR=t.TempDir()`),
  `~/.ssh` or global git config (`testutil.IsolateGitEnv`).

## Gate
`gofmt -l internal` empty; `go vet` and `go test -race` on your packages; staticcheck
(`go run honnef.co/go/tools/cmd/staticcheck@2026.2.2 ./internal/<pkg>/...`); `npx vitest run test/port/<group>.test.ts`.

## Commits (in your worktree branch; the lead merges)
1. `test(port): capture <group> vectors from typescript`
2. `feat(port): port <group> to go`
Lowercase subject, no attribution trailers. Do not push.

## Constraints
- Edit only your packages, your `test/port/<group>.test.ts`, your vector/testdata files, and new files in
  `internal/shared`. Never edit `main.go`, `internal/app`, the renderer, `go.mod`/`go.sum` (if you need a
  dependency, run `go get` in your worktree and say so in the report — the lead reconciles), or TS sources.
- Do not ask questions. Decide, and list decisions in your report.

## Report (final message)
Files created; every facade method (Go signature, channel); events emitted; dependencies your constructor needs
(what main.go must pass); behaviours you could not reproduce exactly and why; new go.mod deps; test counts;
TEST-MAP fragment; branch name and commit SHAs.
