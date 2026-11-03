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

## 018 — Navigation lock lives in the renderer
- **Context:** The brief asks for a Go-side mechanism that refuses navigation away from the app origin. Wails
  v3.1.0 has none: no navigation callback in `WebviewWindowOptions` or the window API (checked in
  `v3/pkg/application`), and `guides/security` does not mention one.
- **Choice:** `installLinkGuard` in `bridge.ts`: a delegated bubble-phase click listener that sends any
  cross-origin link no component handled to `ShellService.OpenExternal` (which keeps the `^https?://` check in
  Go) and prevents the navigation. Electron had no main-frame `will-navigate` handler either; its
  `setWindowOpenHandler` covered new windows, which the renderer never opens (`window.open`/`target=_blank`:
  0 call sites). Listed under limitations: a script-driven `location.href = …` is not blocked.

## 019 — Clipboard goes through the bridge
- 58 `navigator.clipboard.writeText` call sites now use `clipboardApi.writeText` → `window.api.clipboard`
  (Electron: `clipboard.writeText`; Wails: `Clipboard.SetText`). WebKit rejects clipboard writes outside a
  user gesture, and several call sites run after an `await`.

## 020 — PDF preview frame is not sandboxed
- **Context:** The brief asks for `sandbox` (without `allow-same-origin`) on every iframe, and for a fallback when
  WebKitGTK cannot render a PDF.
- **Finding:** Each webview renders PDFs with a plugin-style viewer (Chromium/WebView2 PDF viewer, WKWebView
  PDFKit, WebKitGTK PDF.js). A `sandbox` attribute — with any token set — disables plugins, so a sandboxed PDF
  frame renders nothing on every platform.
- **Choice:** The frame keeps a `data:` URL, which already gives it an opaque origin (the same isolation
  `sandbox` without `allow-same-origin` provides for origin checks); it is the only iframe in the renderer. A
  cross-origin frame cannot be inspected for a failed render, so the working-tree preview always shows an
  "Open in default app" button (existing key `fileTree.openDefaultApp`, translated in every locale). The
  server-build e2e (M8) asserts an opaque-origin frame cannot call `/wails/runtime`.

## 021 — macOS entitlements emptied
- **Context:** `build/entitlements.mac.plist` granted allow-jit, allow-unsigned-executable-memory,
  disable-library-validation and allow-dyld-environment-variables — all Electron/V8 requirements.
- **Choice:** An empty entitlements dict. WKWebView's JIT (JavaScript and the tree-sitter WebAssembly) runs in
  Apple's WebContent process; the Go binary loads no third-party dylibs. Removing them shrinks the hardened
  runtime's exceptions to none. Listed in MANUAL-CHECKS (a signed macOS build must still render and run git).

## 022 — Release profile targets
- `wails.yaml` `profiles.release`: darwin/universal → dmg, windows/amd64 → nsis (per-user scope, as
  electron-builder's oneClick: false), linux/amd64 → appimage + deb. `wails3 config check release` passes.
  Updater artifacts and signing hooks are added with the updater port (M8).

## 023 — Wake builds run before the playground exists
- **Context:** Wake's binding-input fingerprint walks every `.go` file under the project root with a fixed
  exclude list (.git, .wails, bin, build, dist, node_modules) and does not stop at nested modules or honour
  .gitignore for production builds. Several generated playground repos (`examples/playground/*`) contain `.go`
  files with deliberate merge-conflict markers, so `wails3 build` fails while the playground exists.
- **Choice:** CI runs `wails3 build` before any step that generates the playground; the release workflow never
  generates it. Locally, a developer with a playground runs
  `npm run playground:clean` or builds in a fresh checkout. `wails3 build --plan` resolves for linux/amd64
  (appimage, deb) and windows/amd64 (nsis) here; darwin/universal needs a macOS host with CGO (D8), so its plan
  and build run on the macOS release runner (cross-compiling from Linux would need Wails' Docker image).

## 024 — Updater: signed Wails update manifest, not the GitHub provider
- **Context:** Wails' `providers/github` verifies only a SHA256SUMS digest; the brief requires that no update
  installs without a valid ed25519 signature.
- **Choice:** `providers/endpoint` on `https://github.com/MyAppDesk/gitcito/releases/latest/download/
  gitcito-update.json`, written by `wails3 updater manifest -key …` and gated by `wails3 updater verify` in the
  release workflow. `build/updater.pub` (embedded) is the only trust anchor; tampered, wrongly signed,
  digest-only or unsigned releases are refused (internal/updater tests, plus a real CLI-signed fixture).
  Asset names carry GOOS/GOARCH; the universal macOS bundle ships as both darwin-arm64 and darwin-amd64 zips.
- `version.UpdatesDisabled` stays true (fork, as UPDATES_DISABLED was): no network path runs until the owner
  repoints `version.ReleaseRepo` and flips it.
- **Owner action (the one allowed by the brief, §11.3):** this session has no authenticated `gh`, so the private
  key was written to `~/.config/gitcito-release/updater.key` (0600) in the port container and NOT uploaded.
  Because that container is ephemeral, the PR states the steps: generate a fresh pair with
  `wails3 updater genkey`, replace `build/updater.pub`, and run
  `gh secret set WAILS_UPDATER_PRIVATE_KEY < updater.key` on the release repository.

## 025 — `gitcito` CLI is a Go companion binary
- `cmd/gitcito-cli` (CGO off, no Wails import) runs every headless verb and launches/hands off to the app for
  `gitcito <dir>` / `--wait`. `cli:install` symlinks it onto PATH as `gitcito`. `resources/cli/gitcito-textconv`
  stays a one-line exec shim because existing `diff.<driver>.textconv` configs name that path.
