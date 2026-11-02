/**
 * The Wails host's implementation of `window.api` (see docs/port/BRIEF.md §5.2).
 *
 * RULE: this is the ONLY renderer file that may import `@wailsio/runtime` or the generated bindings under
 * `src/renderer/src/bindings/`. Everything else keeps talking to `window.api` through
 * `infrastructure/api.ts`, exactly as it does under Electron — so the two hosts stay interchangeable and the
 * Electron build never pulls in the Wails runtime (main.tsx imports this module dynamically, only when no
 * preload installed `window.api`).
 *
 * Milestone M1 state: the shape is complete, the behaviour is not. Every request member rejects with
 * `not ported: <channel>`, every listener is already a real `Events.On` subscription on the Electron channel
 * name (Go emits on identical names), and fire-and-forget members log once and do nothing. Porting a
 * channel means replacing its stub with `invoke(() => SomeService.Method(...))`.
 */
import { Clipboard, Events, Window } from '@wailsio/runtime'
import * as AppService from '../bindings/github.com/GabrySpada/gitcito/internal/app/appservice'
import * as DialogService from '../bindings/github.com/GabrySpada/gitcito/internal/app/dialogservice'
import * as ShellService from '../bindings/github.com/GabrySpada/gitcito/internal/app/shellservice'
import * as KeychainService from '../bindings/github.com/GabrySpada/gitcito/internal/keychain/service'
import * as LogService from '../bindings/github.com/GabrySpada/gitcito/internal/logbook/service'
import * as MenuService from '../bindings/github.com/GabrySpada/gitcito/internal/menu/service'
import * as SettingsService from '../bindings/github.com/GabrySpada/gitcito/internal/settings/service'
import type { BootstrapInfo, PreloadApi } from '../../../shared/bridgeApi'
import type { KeychainConsent } from '../../../shared/types'

/**
 * A Wails bound call rejects with a RuntimeError (the Go method returned an error), a TypeError (argument
 * count/shape), a ReferenceError (unknown method) or a plain Error (transport). For a RuntimeError the
 * message is the Go error string verbatim (`errors.Join(...).Error()` on the Go side). The renderer parses
 * that string for `*_PREFIX` markers, so it is passed through untouched; only the class is flattened to a
 * plain `Error`, which is what the Electron path delivered.
 */
export function normalizeError(e: unknown): Error {
  if (e instanceof Error) {
    const err = new Error(e.message, e.cause === undefined ? undefined : { cause: e.cause })
    if (e.stack) err.stack = e.stack
    return err
  }
  if (typeof e === 'string') return new Error(e)
  if (typeof e === 'object' && e !== null && 'message' in e && typeof e.message === 'string') {
    return new Error(e.message)
  }
  return new Error(String(e))
}

/** Run a bound call, rejecting with a normalized `Error` instead of the runtime's error classes. */
export function invoke<T>(fn: () => Promise<T>): Promise<T> {
  return fn().catch((e: unknown) => {
    throw normalizeError(e)
  })
}


function guessPlatform(): string {
  const ua = typeof navigator === 'undefined' ? '' : navigator.userAgent
  if (/Mac/i.test(ua)) return 'darwin'
  if (/Win/i.test(ua)) return 'win32'
  return 'linux'
}

function defaultBootstrap(): BootstrapInfo {
  return { platform: guessPlatform(), shotMode: false, version: '0.0.0', dev: import.meta.env.DEV, zoom: 1 }
}

/** Overlay whatever well-typed fields Go sent onto the defaults; a malformed payload cannot break startup. */
function mergeBootstrap(base: BootstrapInfo, got: unknown): BootstrapInfo {
  if (typeof got !== 'object' || got === null) return base
  const out = { ...base }
  if ('platform' in got && typeof got.platform === 'string') out.platform = got.platform
  if ('shotMode' in got && typeof got.shotMode === 'boolean') out.shotMode = got.shotMode
  if ('version' in got && typeof got.version === 'string') out.version = got.version
  if ('dev' in got && typeof got.dev === 'boolean') out.dev = got.dev
  if ('zoom' in got && typeof got.zoom === 'number' && got.zoom > 0) out.zoom = got.zoom
  return out
}

/**
 * The one bound call made before the first render. Falls back to defaults so a missing or failing Go side
 * still yields a page that renders (and reports `not ported` per action) rather than a blank window.
 */
export async function loadBootstrap(): Promise<BootstrapInfo> {
  const base = defaultBootstrap()
  try {
    return mergeBootstrap(base, await invoke(() => AppService.Bootstrap()))
  } catch (e) {
    console.warn('[bridge] bootstrap failed:', normalizeError(e).message)
    return base
  }
}

const notPorted =
  (channel: string) =>
  (): Promise<never> =>
    Promise.reject(new Error(`not ported: ${channel}`))

// Fire-and-forget members can fire per keystroke (term:input); warn once per channel, not per call.
const warned = new Set<string>()
const sendNoop =
  (channel: string) =>
  (..._args: unknown[]): void => {
    if (warned.has(channel)) return
    warned.add(channel)
    console.warn(`[bridge] not ported: ${channel}`)
  }

/** Fire-and-forget calls never reject into the caller; a failure is logged, as Electron's send would drop it. */
const logSendError =
  (channel: string) =>
  (e: unknown): void => {
    console.warn(`[bridge] ${channel} failed:`, normalizeError(e).message)
  }

function toBase64(data: Uint8Array): string {
  let bin = ''
  const CHUNK = 0x8000
  for (let i = 0; i < data.length; i += CHUNK) {
    bin += String.fromCharCode(...data.subarray(i, i + CHUNK))
  }
  return btoa(bin)
}

/** Subscribe to a Go-emitted event; the payload is the event's data, as webContents.send delivered it. */
function listen<T>(channel: string, cb: (data: T) => void): () => void {
  return Events.On(channel, (ev) => cb(ev.data))
}

/**
 * Electron's setWindowOpenHandler sent every new-window request to the system browser, and nothing in the app
 * ever navigates the main frame away from the bundle. Wails has no Go-side navigation policy, so this one
 * delegated listener is the lock: an http(s) link to another origin that no component handled itself opens in
 * the browser, and the page stays where it is. Runs in the bubble phase so components that already route their
 * own links (HelpPage, RepoChatPanel) keep doing so.
 */
export function installLinkGuard(openExternal: (url: string) => Promise<void>): void {
  document.addEventListener('click', (e) => {
    if (e.defaultPrevented || !(e.target instanceof Element)) return
    const a = e.target.closest('a')
    const href = a?.getAttribute('href')
    if (!a || !href) return
    let url: URL
    try {
      url = new URL(href, window.location.href)
    } catch {
      return
    }
    if (url.origin === window.location.origin) return
    e.preventDefault()
    if (url.protocol === 'http:' || url.protocol === 'https:') void openExternal(url.href)
  })
}

function toConsent(c: string): KeychainConsent {
  return c === 'granted' || c === 'declined' ? c : 'unset'
}

const NATIVE_DROP_TIMEOUT_MS = 1500

function nextNativeDrop(): Promise<string[]> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      off()
      resolve([])
    }, NATIVE_DROP_TIMEOUT_MS)
    const off = listen<{ paths: string[] }>('files:dropped', (p) => {
      clearTimeout(timer)
      off()
      resolve(p.paths ?? [])
    })
  })
}

export function createBridge(boot: BootstrapInfo): PreloadApi {
  let zoom = boot.zoom

  const bridge: PreloadApi = {
    platform: boot.platform,
    shotMode: boot.shotMode,

    git: (method: string): Promise<never> => Promise.reject(new Error(`not ported: git:${method}`)),
    onCloneProgress: (cb) => listen('clone:progress', cb),
    onBisectOutput: (cb) => listen('bisect:output', cb),

    localci: {
      status: notPorted('localci:status'),
      workflows: notPorted('localci:workflows'),
      run: notPorted('localci:run'),
      cancel: notPorted('localci:cancel'),
      record: notPorted('localci:record'),
      verdicts: notPorted('localci:verdicts'),
      runAt: notPorted('localci:runAt'),
      resolveRange: notPorted('localci:resolveRange'),
      sweep: notPorted('localci:sweep'),
      onData: (cb) => listen('localci:data', cb),
      onSweepProgress: (cb) => listen('localci:sweep-progress', cb)
    },

    // A DOM File carries no path in a webview. Go emits `files:dropped` for the native drop, after the DOM
    // drop handler ran; wait for it (briefly — a drop outside a marked target never produces one).
    droppedFilePaths: (files) => (files.length ? nextNativeDrop() : Promise.resolve([])),

    keychain: {
      onAsk: (cb) => listen('keychain:ask', cb),
      answer: (granted) => invoke(() => KeychainService.Answer(granted)),
      // Go's Consent is generated as a TS enum; the renderer's contract is the string union. Narrow by value.
      status: () =>
        invoke(() => KeychainService.Status()).then((st) => ({
          consent: toConsent(st.consent),
          explained: st.explained,
          available: st.available
        })),
      set: (granted) => invoke(() => KeychainService.Set(granted))
    },

    selectDirectory: (title) => invoke(() => DialogService.SelectDirectory(title ?? '')),
    savePatch: (defaultName, content) => invoke(() => DialogService.SavePatch(defaultName, content)),
    openPatch: () => invoke(() => DialogService.OpenPatch()),
    // JSON has no bytes: Go's []byte decodes from base64.
    saveBinary: (defaultName, data, filters) =>
      invoke(() => DialogService.SaveBinary(defaultName, toBase64(data), filters ?? null)),
    choosePath: (title, defaultName, filters) =>
      invoke(() => DialogService.ChoosePath(title, defaultName, filters ?? null)),
    openFilePath: (title, filters) => invoke(() => DialogService.OpenFilePath(title, filters ?? null)),
    openExternal: (url) => invoke(() => ShellService.OpenExternal(url)),
    clipboard: {
      writeText: (text) => invoke(() => Clipboard.SetText(text))
    },
    appVersion: () => invoke(() => AppService.Version()),
    appReleases: () => invoke(() => AppService.Releases()).then((r) => r ?? []),

    shell: {
      showItemInFolder: (p) => invoke(() => ShellService.ShowItemInFolder(p)),
      openPath: (p) => invoke(() => ShellService.OpenPath(p)),
      openWithPicker: (p) => invoke(() => ShellService.OpenWithPicker(p)),
      pickApplication: () => invoke(() => ShellService.PickApplication()),
      openWithApp: (t, a) => invoke(() => ShellService.OpenWithApp(t, a)),
      writeFiles: (repoPath, files) =>
        invoke(() => ShellService.WriteFiles(repoPath, files as { path: string; content: string }[]))
    },

    difftool: {
      config: notPorted('difftool:config'),
      set: notPorted('difftool:set'),
      diff: notPorted('difftool:diff'),
      merge: notPorted('difftool:merge')
    },

    ssh: {
      status: notPorted('ssh:status'),
      generate: notPorted('ssh:generate'),
      addToAgent: notPorted('ssh:addToAgent'),
      test: notPorted('ssh:test')
    },

    editor: {
      detect: notPorted('editor:detect'),
      open: notPorted('editor:open')
    },

    settings: {
      get: () => invoke(() => SettingsService.Get()),
      unlock: () => invoke(() => SettingsService.Unlock()),
      set: (settings) => invoke(() => SettingsService.Set(settings)),
      importFile: () => invoke(() => SettingsService.ImportFile()),
      exportFile: (settings) => invoke(() => SettingsService.ExportFile(settings))
    },

    ai: {
      commitMessage: notPorted('ai:commitMessage'),
      listModels: notPorted('ai:listModels'),
      detectCli: notPorted('ai:detectCli'),
      explainCode: notPorted('ai:explainCode'),
      hoverExplain: notPorted('ai:hoverExplain'),
      resolveConflict: notPorted('ai:resolveConflict'),
      generateConfig: notPorted('ai:generateConfig'),
      suggestArtifacts: notPorted('ai:suggestArtifacts'),
      smartStage: notPorted('ai:smartStage'),
      generateAppTheme: notPorted('ai:generateAppTheme'),
      generateCodeTheme: notPorted('ai:generateCodeTheme'),
      generateGraphPalette: notPorted('ai:generateGraphPalette'),
      generateBranchName: notPorted('ai:generateBranchName'),
      reviewPR: notPorted('ai:reviewPR'),
      prDescription: notPorted('ai:prDescription'),
      planActions: notPorted('ai:planActions'),
      repoChat: notPorted('ai:repoChat'),
      repoChatFinalize: notPorted('ai:repoChatFinalize')
    },

    wiki: {
      facts: notPorted('wiki:facts'),
      imports: notPorted('wiki:imports'),
      get: notPorted('wiki:get'),
      generate: notPorted('wiki:generate'),
      export: notPorted('wiki:export'),
      clear: notPorted('wiki:clear'),
      onProgress: (cb) => listen('wiki:progress', cb)
    },

    analytics: {
      get: notPorted('analytics:get'),
      clear: notPorted('analytics:clear'),
      setRetention: notPorted('analytics:setRetention')
    },

    vault: {
      list: notPorted('vault:list'),
      upsert: notPorted('vault:upsert'),
      remove: notPorted('vault:remove'),
      exportAll: notPorted('vault:exportAll'),
      importAll: notPorted('vault:importAll')
    },

    repos: {
      list: notPorted('repos:list'),
      remember: notPorted('repos:remember'),
      forget: notPorted('repos:forget'),
      scan: notPorted('repos:scan'),
      locate: notPorted('repos:locate'),
      refresh: notPorted('repos:refresh')
    },

    secureShare: {
      candidates: notPorted('secure:candidates'),
      export: notPorted('secure:export'),
      pick: notPorted('secure:pick'),
      preview: notPorted('secure:preview'),
      apply: notPorted('secure:apply'),
      exportV2: notPorted('secure:exportV2'),
      openV2: notPorted('secure:openV2'),
      previewRepoV2: notPorted('secure:previewRepoV2'),
      applyV2: notPorted('secure:applyV2'),
      previewNotesV2: notPorted('secure:previewNotesV2')
    },

    info: {
      list: notPorted('info:list'),
      upsert: notPorted('info:upsert'),
      remove: notPorted('info:remove'),
      reorder: notPorted('info:reorder'),
      exportAll: notPorted('info:exportAll'),
      importAll: notPorted('info:importAll')
    },

    log: {
      get: () => invoke(() => LogService.Get()).then((r) => r ?? []),
      clear: () => invoke(() => LogService.Clear()).then((r) => r ?? [])
    },

    hosting: {
      listRepos: notPorted('hosting:listRepos'),
      uploadSshKey: notPorted('hosting:uploadSshKey'),
      listOwners: notPorted('hosting:listOwners'),
      whoAmI: notPorted('hosting:whoAmI'),
      createRepo: notPorted('hosting:createRepo'),
      listPRs: notPorted('hosting:listPRs'),
      listReleases: notPorted('hosting:listReleases'),
      ciStatuses: notPorted('hosting:ciStatuses'),
      openCreatePR: notPorted('hosting:openCreatePR'),
      createPR: notPorted('hosting:createPR'),
      prDetail: notPorted('hosting:prDetail'),
      prComment: notPorted('hosting:prComment'),
      prReplyReviewComment: notPorted('hosting:prReplyReviewComment'),
      prChecks: notPorted('hosting:prChecks'),
      prFiles: notPorted('hosting:prFiles'),
      prReview: notPorted('hosting:prReview'),
      prMerge: notPorted('hosting:prMerge'),
      listNotifications: notPorted('hosting:listNotifications'),
      markNotificationRead: notPorted('hosting:markNotificationRead'),
      markAllNotificationsRead: notPorted('hosting:markAllNotificationsRead'),
      listIssues: notPorted('hosting:listIssues'),
      issueDetail: notPorted('hosting:issueDetail'),
      setIssueState: notPorted('hosting:setIssueState'),
      createIssue: notPorted('hosting:createIssue'),
      applyPrMeta: notPorted('hosting:applyPrMeta'),
      updatePR: notPorted('hosting:updatePR'),
      mergedPrHeads: notPorted('hosting:mergedPrHeads'),
      ensureStack: notPorted('hosting:ensureStack'),
      listMilestones: notPorted('hosting:listMilestones'),
      milestoneIssues: notPorted('hosting:milestoneIssues')
    },

    term: {
      create: notPorted('term:create'),
      input: sendNoop('term:input'),
      resize: sendNoop('term:resize'),
      kill: sendNoop('term:kill'),
      procName: notPorted('term:process'),
      onData: (id, cb) => listen(`term:data:${id}`, cb),
      onExit: (id, cb) => listen(`term:exit:${id}`, () => cb())
    },

    launch: {
      discover: notPorted('launch:discover'),
      run: notPorted('launch:run'),
      runTasks: notPorted('launch:runTasks'),
      attach: sendNoop('launch:attach'),
      input: sendNoop('launch:input'),
      resize: sendNoop('launch:resize'),
      signal: sendNoop('launch:signal'),
      stop: sendNoop('launch:stop'),
      onData: (id, cb) => listen(`launch:data:${id}`, cb),
      onDevTools: (id, cb) => listen(`launch:devtools:${id}`, cb),
      onExit: (id, cb) => listen(`launch:exit:${id}`, cb)
    },

    analyze: {
      run: notPorted('analyze:run'),
      detect: notPorted('analyze:detect'),
      cancel: sendNoop('analyze:cancel')
    },

    todoScan: {
      run: notPorted('todoscan:run'),
      cancel: sendNoop('todoscan:cancel')
    },

    devices: {
      list: notPorted('devices:list'),
      boot: notPorted('devices:boot')
    },

    menu: {
      // The spec is built by lib/appMenu.ts; the Go Item model mirrors MenuSpecItem field for field.
      set: (spec) => invoke(() => MenuService.Set(spec as Parameters<typeof MenuService.Set>[0])),
      onCommand: (cb) => listen('menu:command', cb)
    },

    window: {
      minimize: () => void Window.Minimise().catch(logSendError('window:minimize')),
      maximize: () => void Window.ToggleMaximise().catch(logSendError('window:maximize')),
      close: () => void Window.Close().catch(logSendError('window:close'))
    },

    // Electron's webFrame answers the getter synchronously; mirror the last value set so it still does.
    zoom: {
      get: (): number => zoom,
      // The getter must answer synchronously (webFrame did), so the last value set is cached here.
      set: (factor: number): void => {
        zoom = factor
        void Window.SetZoom(factor).catch(logSendError('zoom:set'))
      }
    },

    watch: {
      repo: notPorted('watch:repo'),
      mute: notPorted('watch:mute'),
      onChange: (cb) => listen('repo:changed', cb)
    },

    updates: {
      getState: notPorted('update:getState'),
      check: notPorted('update:check'),
      download: notPorted('update:download'),
      install: sendNoop('update:install'),
      onEvent: (cb) => listen('update:event', cb)
    },

    cli: {
      isInstalled: notPorted('cli:isInstalled'),
      install: notPorted('cli:install'),
      uninstall: notPorted('cli:uninstall'),
      // Go holds CLI deliveries until the page can receive them (Electron's did-finish-load gate). The
      // listener existing is the moment it can: subscribe first, then release the queue.
      onOpenPath: (cb) => {
        const off = listen('cli:open-path', cb)
        void AppService.Ready().catch(logSendError('app:ready'))
        return off
      },
      onEdit: (cb) => listen('cli:edit', cb),
      finishEdit: (sentinel, content) => invoke(() => AppService.FinishEdit(sentinel, content))
    }
  }

  return bridge
}
