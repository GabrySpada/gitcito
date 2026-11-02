# Port plan — Electron → Wails v3

Milestones from the brief (§10). Each ends with a green gate for what exists, a commit, a push and a tick here.

- [ ] **M0** Notes files, Wails docs, reference project, inventory, goldens + vectors, starting test count,
      baseline Electron bundle size
- [ ] **M1** Toolchain + skeleton: go.mod, wails.yaml, main.go, vite.config.ts, bindings, bridge.ts skeleton,
      PreloadApi → src/shared/bridgeApi.ts
- [ ] **M2** Wave 0 foundation: paths, gitx, logbook, keychain, settings, app (window, single instance, menu,
      dialogs, shell, zoom, bootstrap), lock.go + GitService skeleton
- [ ] **M3** Wave 1: git service (A–D) + hosting/credentials; git goldens green; e2e core flows
- [ ] **M4** Wave 2: ai, repochat/grounding/wiki, processes, secure/vault/analytics/info, registry/watcher/textconv
- [ ] **M5** Wave 3: tools, CLI binary, updater, renderer adaptations, semantic diff in renderer
- [ ] **M6** Review round + fixes
- [ ] **M7** Electron removal
- [ ] **M8** Build, release, CI, tooling, screenshots harness
- [ ] **M9** Docs, handbook, site, CHANGELOG, screenshots
- [ ] **M10** Final reviews, draft PR
