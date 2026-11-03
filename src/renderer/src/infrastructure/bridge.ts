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
import * as LocalCiService from '../bindings/github.com/GabrySpada/gitcito/internal/localci/service'
import * as RepoChatService from '../bindings/github.com/GabrySpada/gitcito/internal/repochat/service'
import * as WikiService from '../bindings/github.com/GabrySpada/gitcito/internal/wiki/service'
import * as TerminalService from '../bindings/github.com/GabrySpada/gitcito/internal/terminal/service'
import * as LaunchService from '../bindings/github.com/GabrySpada/gitcito/internal/launch/service'
import * as AnalyzeService from '../bindings/github.com/GabrySpada/gitcito/internal/analyze/service'
import * as TodoScanService from '../bindings/github.com/GabrySpada/gitcito/internal/todoscan/service'
import * as DevicesService from '../bindings/github.com/GabrySpada/gitcito/internal/devices/service'
import * as RegistryService from '../bindings/github.com/GabrySpada/gitcito/internal/registry/service'
import * as WatcherService from '../bindings/github.com/GabrySpada/gitcito/internal/watcher/service'
import * as AIService from '../bindings/github.com/GabrySpada/gitcito/internal/ai/service'
import * as AnalyticsService from '../bindings/github.com/GabrySpada/gitcito/internal/analytics/service'
import * as InfoService from '../bindings/github.com/GabrySpada/gitcito/internal/info/service'
import * as SecureService from '../bindings/github.com/GabrySpada/gitcito/internal/secure/service'
import * as VaultService from '../bindings/github.com/GabrySpada/gitcito/internal/vault/service'
import * as GitService from '../bindings/github.com/GabrySpada/gitcito/internal/gitsvc/gitservice'
import * as HostingService from '../bindings/github.com/GabrySpada/gitcito/internal/hosting/service'
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

type AnyCall = (...args: unknown[]) => Promise<unknown>

/** Most channels carry loosely typed payloads in the contract; forward them as-is to the generated Go
 *  function of the same name (which sends every parameter, missing ones as null). */
/** What a forwarded call resolves to, as far as tsc knows: the contract's own type. The Go models mirror the
 *  types in src/shared (field for field, checked by the TS-captured vectors each Go package replays), so the
 *  value is what the Electron channel returned; Promise<never> lets each member keep its declared type. */
type Forwarded = (...args: unknown[]) => Promise<never>

const forwarder =
  (mod: object) =>
  (name: string): Forwarded => {
    const fn = (mod as Record<string, AnyCall | undefined>)[name]
    if (!fn) throw new Error(`bridge: no bound method ${name}`)
    return (...args) => invoke(() => fn(...args)) as Promise<never>
  }
const hostingCall = forwarder(HostingService)
const vaultCall = forwarder(VaultService)
const secureCall = forwarder(SecureService)
const analyticsCall = forwarder(AnalyticsService)
const infoCall = forwarder(InfoService)
const aiCall = forwarder(AIService)
const localCiCall = forwarder(LocalCiService)
const repoChatCall = forwarder(RepoChatService)
const wikiCall = forwarder(WikiService)
const termCall = forwarder(TerminalService)
const launchCall = forwarder(LaunchService)
const analyzeCall = forwarder(AnalyzeService)
const todoScanCall = forwarder(TodoScanService)
const devicesCall = forwarder(DevicesService)
const reposCall = forwarder(RegistryService)
const watchCall = forwarder(WatcherService)

/** Fire-and-forget channels (Electron's ipcRenderer.send): never awaited, failures only logged. */
const sendTo =
  (call: Forwarded, channel: string) =>
  (...args: unknown[]): void => {
    void call(...args).catch(logSendError(channel))
  }

// Looked up by name at call time; the generated module is the authoritative list of ported git methods.
const gitMethods = GitService as unknown as Record<string, ((...args: unknown[]) => Promise<unknown>) | undefined>

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

    // The single Electron 'git' channel becomes one bound Go method per gitService method, named with the
    // first letter upper-cased (GitService.Status for 'status'). Calls go through the generated functions, not
    // Call.ByName: Wails rejects a call whose argument count differs from the Go signature, and callers here
    // omit optional trailing arguments — a generated function always sends every parameter (missing → null).
    git: (method: string, ...args: unknown[]): Promise<unknown> => {
      const fn = gitMethods[`${method.charAt(0).toUpperCase()}${method.slice(1)}`]
      if (!fn) return Promise.reject(new Error(`not ported: git:${method}`))
      return invoke(() => fn(...args))
    },
    onCloneProgress: (cb) => listen('clone:progress', cb),
    onBisectOutput: (cb) => listen('bisect:output', cb),

    localci: {
      status: localCiCall('Status'),
      workflows: localCiCall('Workflows'),
      run: localCiCall('Run'),
      cancel: localCiCall('Cancel'),
      record: localCiCall('Record'),
      verdicts: localCiCall('Verdicts'),
      runAt: localCiCall('RunAt'),
      resolveRange: localCiCall('ResolveRange'),
      sweep: localCiCall('Sweep'),
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
      commitMessage: aiCall('CommitMessage'),
      listModels: aiCall('ListModels'),
      detectCli: aiCall('DetectCli'),
      explainCode: aiCall('ExplainCode'),
      hoverExplain: aiCall('HoverExplain'),
      resolveConflict: aiCall('ResolveConflict'),
      generateConfig: aiCall('GenerateConfig'),
      suggestArtifacts: aiCall('SuggestArtifacts'),
      smartStage: aiCall('SmartStage'),
      generateAppTheme: aiCall('GenerateAppTheme'),
      generateCodeTheme: aiCall('GenerateCodeTheme'),
      generateGraphPalette: aiCall('GenerateGraphPalette'),
      generateBranchName: aiCall('GenerateBranchName'),
      reviewPR: aiCall('ReviewPR'),
      prDescription: aiCall('PrDescription'),
      planActions: aiCall('PlanActions'),
      repoChat: repoChatCall('RepoChat'),
      repoChatFinalize: repoChatCall('RepoChatFinalize')
    },

    wiki: {
      facts: wikiCall('Facts'),
      imports: wikiCall('Imports'),
      get: wikiCall('Get'),
      generate: wikiCall('Generate'),
      export: wikiCall('Export'),
      clear: wikiCall('Clear'),
      onProgress: (cb) => listen('wiki:progress', cb)
    },

    analytics: {
      get: analyticsCall('Get'),
      clear: analyticsCall('Clear'),
      setRetention: analyticsCall('SetRetention')
    },

    vault: {
      list: vaultCall('List'),
      upsert: vaultCall('Upsert'),
      remove: vaultCall('Remove'),
      exportAll: vaultCall('ExportAll'),
      importAll: vaultCall('ImportAll')
    },

    repos: {
      list: reposCall('List'),
      remember: reposCall('Remember'),
      forget: reposCall('Forget'),
      scan: reposCall('Scan'),
      locate: reposCall('Locate'),
      refresh: reposCall('Refresh')
    },

    secureShare: {
      candidates: secureCall('Candidates'),
      export: secureCall('Export'),
      pick: secureCall('Pick'),
      preview: secureCall('Preview'),
      apply: secureCall('Apply'),
      exportV2: secureCall('ExportV2'),
      openV2: secureCall('OpenV2'),
      previewRepoV2: secureCall('PreviewRepoV2'),
      applyV2: secureCall('ApplyV2'),
      previewNotesV2: secureCall('PreviewNotesV2')
    },

    info: {
      list: infoCall('List'),
      upsert: infoCall('Upsert'),
      remove: infoCall('Remove'),
      reorder: infoCall('Reorder'),
      exportAll: infoCall('ExportAll'),
      importAll: infoCall('ImportAll')
    },

    log: {
      get: () => invoke(() => LogService.Get()).then((r) => r ?? []),
      clear: () => invoke(() => LogService.Clear()).then((r) => r ?? [])
    },

    hosting: {
      listRepos: hostingCall('ListRepos'),
      uploadSshKey: hostingCall('UploadSshKey'),
      listOwners: hostingCall('ListOwners'),
      whoAmI: hostingCall('WhoAmI'),
      createRepo: hostingCall('CreateRepo'),
      listPRs: hostingCall('ListPRs'),
      listReleases: hostingCall('ListReleases'),
      ciStatuses: hostingCall('CiStatuses'),
      openCreatePR: (remoteUrl, source, target) =>
        invoke(() => HostingService.OpenCreatePR(remoteUrl, source, target)),
      createPR: hostingCall('CreatePR'),
      prDetail: hostingCall('PrDetail'),
      prComment: hostingCall('PrComment'),
      prReplyReviewComment: hostingCall('PrReplyReviewComment'),
      prChecks: hostingCall('PrChecks'),
      prFiles: hostingCall('PrFiles'),
      prReview: hostingCall('PrReview'),
      prMerge: hostingCall('PrMerge'),
      listNotifications: hostingCall('ListNotifications'),
      markNotificationRead: hostingCall('MarkNotificationRead'),
      markAllNotificationsRead: hostingCall('MarkAllNotificationsRead'),
      listIssues: hostingCall('ListIssues'),
      issueDetail: hostingCall('IssueDetail'),
      setIssueState: hostingCall('SetIssueState'),
      createIssue: hostingCall('CreateIssue'),
      applyPrMeta: hostingCall('ApplyPrMeta'),
      updatePR: hostingCall('UpdatePR'),
      mergedPrHeads: hostingCall('MergedPrHeads'),
      ensureStack: hostingCall('EnsureStack'),
      listMilestones: hostingCall('ListMilestones'),
      milestoneIssues: hostingCall('MilestoneIssues')
    },

    term: {
      create: termCall('Create'),
      input: sendTo(termCall('Input'), 'term:input'),
      resize: sendTo(termCall('Resize'), 'term:resize'),
      kill: sendTo(termCall('Kill'), 'term:kill'),
      procName: termCall('ProcName'),
      onData: (id, cb) => listen(`term:data:${id}`, cb),
      onExit: (id, cb) => listen(`term:exit:${id}`, () => cb())
    },

    launch: {
      discover: launchCall('Discover'),
      run: launchCall('Run'),
      runTasks: launchCall('RunTasks'),
      attach: sendTo(launchCall('Attach'), 'launch:attach'),
      input: sendTo(launchCall('Input'), 'launch:input'),
      resize: sendTo(launchCall('Resize'), 'launch:resize'),
      signal: sendTo(launchCall('Signal'), 'launch:signal'),
      stop: sendTo(launchCall('Stop'), 'launch:stop'),
      onData: (id, cb) => listen(`launch:data:${id}`, cb),
      onDevTools: (id, cb) => listen(`launch:devtools:${id}`, cb),
      onExit: (id, cb) => listen(`launch:exit:${id}`, cb)
    },

    analyze: {
      run: analyzeCall('Run'),
      detect: analyzeCall('Detect'),
      cancel: sendTo(analyzeCall('Cancel'), 'analyze:cancel')
    },

    todoScan: {
      run: todoScanCall('Run'),
      cancel: sendTo(todoScanCall('Cancel'), 'todoscan:cancel')
    },

    devices: {
      list: devicesCall('List'),
      boot: devicesCall('Boot')
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
      repo: watchCall('Repo'),
      mute: watchCall('Mute'),
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
