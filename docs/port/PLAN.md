# Port plan — Electron → Wails v3

Milestones from the brief (§10). Each ends with a green gate for what exists, a commit, a push and a tick here.

- [x] **M0** Notes files, Wails docs, reference project, inventory, goldens + vectors, starting test count, (e.g. 0dd3fd7, 283395c)
      baseline Electron bundle size
- [x] **M1** Toolchain + skeleton: go.mod, wails.yaml, main.go, vite.config.ts, bindings, bridge.ts skeleton, (5c3ed66, c7c56b2, e15f672)
      PreloadApi → src/shared/bridgeApi.ts
- [x] **M2** Wave 0 foundation: paths, gitx, logbook, keychain, settings, app (window, single instance, menu, (dfde30e, 34b4021)
      dialogs, shell, zoom, bootstrap), lock.go + GitService skeleton
- [x] **M3** Wave 1: git service (A–D) + hosting/credentials; git goldens green; e2e core flows (slices A–D + hosting; e2e e74b36e)
- [x] **M4** Wave 2: ai, repochat/grounding/wiki, processes, secure/vault/analytics/info, registry/watcher/textconv (wave 2 merges f4d9436, 7526c9a, 528eb7b, e0e1600) — PTY on macOS/Windows unverified: CI has never run
- [x] **M5** Wave 3: tools, CLI binary, updater, renderer adaptations, semantic diff in renderer (e632710, acc0b60; renderer adaptations, semantic diff c0f697f, D6 10a0b3e — D6 handbook pending)
- [ ] **M6** Review round + fixes
- [ ] **M7** Electron removal
- [ ] **M8** Build, release, CI, tooling, screenshots harness
- [ ] **M9** Docs, handbook, site, CHANGELOG, screenshots
- [ ] **M10** Final reviews, draft PR
