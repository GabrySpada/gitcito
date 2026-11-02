/**
 * The shape of `window.api` — the renderer's whole view of the host process.
 *
 * Written out by hand rather than derived (`typeof api`) because two hosts implement it: the Electron preload
 * (`src/preload/index.ts`) and the Wails bridge (`src/renderer/src/infrastructure/bridge.ts`). Both declare
 * `const …: PreloadApi`, so tsc proves each one still satisfies the contract the renderer codes against.
 *
 * Where the preload only knows `unknown`, this stays `unknown`: `infrastructure/api.ts` narrows those.
 */
import type { CliOpenPayload } from './cli'
import type { DiffToolConfig } from './diffTools'
import type { DetectedEditor, EditorSetting } from './editors'
import type { SshStatus, SshTest } from './sshKeys'
import type {
  AnalyzeResult,
  AppRelease,
  CloneProgress,
  KeychainConsent,
  LaunchConfig,
  LaunchGroup,
  LaunchTask,
  RunDevice,
  RunDeviceSnapshot,
  TodoScanResult,
  UpdateState
} from './types'

/**
 * Everything the renderer needs synchronously before its first render. Electron's preload knows these at
 * load time (process.platform, argv, webFrame); under Wails they arrive in one bound call
 * (`AppService.Bootstrap`) that `main.tsx` awaits before installing the bridge.
 */
export interface BootstrapInfo {
  /** Node-style platform id: `darwin`, `win32` or `linux` — the renderer compares against these. */
  platform: string
  /** Launched with `--shot`: gates the screenshot-automation store bridge. */
  shotMode: boolean
  version: string
  dev: boolean
  /** The window's zoom factor at startup; `zoom.get()` must answer synchronously. */
  zoom: number
}

export interface TermApi {
  create(cwd: string, cols: number, rows: number): Promise<number>
  input(id: number, data: string): void
  resize(id: number, cols: number, rows: number): void
  kill(id: number): void
  procName(id: number): Promise<string>
  onData(id: number, cb: (data: string) => void): () => void
  onExit(id: number, cb: () => void): () => void
}

export interface LaunchApi {
  discover(repoPath: string): Promise<LaunchGroup[]>
  run(payload: {
    dir: string
    config: LaunchConfig
    configs?: LaunchConfig[]
    tasks: LaunchTask[]
    inputValues?: Record<string, string>
    skipTasks?: string[]
    cols: number
    rows: number
  }): Promise<{ id: number } | { error: string }>
  runTasks(payload: {
    dir: string
    tasks: LaunchTask[]
    labels: string[]
    inputValues?: Record<string, string>
    cols: number
    rows: number
  }): Promise<{ id: number } | { error: string }>
  attach(id: number): void
  input(id: number, data: string): void
  resize(id: number, cols: number, rows: number): void
  signal(id: number, action: 'pause' | 'resume'): void
  stop(id: number): void
  onData(id: number, cb: (data: string) => void): () => void
  onDevTools(id: number, cb: (found: { url: string; name: string }) => void): () => void
  onExit(id: number, cb: (code: number) => void): () => void
}

export interface PreloadApi {
  platform: string
  shotMode: boolean
  git(method: string, ...args: unknown[]): Promise<unknown>
  onCloneProgress(cb: (p: CloneProgress) => void): () => void
  /** Live stdout/stderr of a running `git bisect run`. */
  onBisectOutput(cb: (chunk: string) => void): () => void
  localci: {
    status(): Promise<unknown>
    workflows(repoPath: string): Promise<unknown>
    run(repoPath: string, workflowFile: string): Promise<unknown>
    cancel(repoPath: string): Promise<unknown>
    record(repoPath: string, workflowFile: string, ok: boolean): Promise<unknown>
    verdicts(repoPath: string): Promise<unknown>
    runAt(repoPath: string, workflowFile: string, sha: string): Promise<unknown>
    resolveRange(repoPath: string, spec: string, limit: number): Promise<unknown>
    sweep(repoPath: string, workflowFile: string, shas: string[]): Promise<unknown>
    onData(cb: (p: { repoPath: string; chunk: string }) => void): () => void
    onSweepProgress(cb: (p: unknown) => void): () => void
  }
  /** Absolute paths of files dropped from the OS, in drop order. Call it from the DOM `drop` handler.
   *  Electron resolves each File synchronously; Wails reports native drops through its own event, which
   *  arrives after the DOM handler — and only for drops onto an element marked `data-file-drop-target`. */
  droppedFilePaths(files: File[]): Promise<string[]>
  keychain: {
    onAsk(cb: (payload: { reason: string; adopted: boolean }) => void): () => void
    answer(granted: boolean): Promise<void>
    status(): Promise<{
      consent: KeychainConsent
      explained: boolean
      available: boolean | null
    }>
    set(granted: boolean): Promise<boolean>
  }
  selectDirectory(title?: string): Promise<string | null>
  savePatch(defaultName: string, content: string): Promise<string | null>
  openPatch(): Promise<{ path: string; content: string } | null>
  saveBinary(
    defaultName: string,
    data: Uint8Array,
    filters?: { name: string; extensions: string[] }[]
  ): Promise<string | null>
  /** Pick a destination path; writes nothing (git produces the file). */
  choosePath(
    title: string,
    defaultName: string,
    filters?: { name: string; extensions: string[] }[]
  ): Promise<string | null>
  /** Pick an existing file, by path only. */
  openFilePath(title: string, filters?: { name: string; extensions: string[] }[]): Promise<string | null>
  openExternal(url: string): Promise<void>
  /** System clipboard. Under Wails it goes through the native clipboard, because WebKit refuses
   *  navigator.clipboard writes that run outside a user gesture (for example after an await). */
  clipboard: {
    writeText(text: string): Promise<void>
  }
  appVersion(): Promise<string>
  appReleases(): Promise<AppRelease[]>
  shell: {
    showItemInFolder(fullPath: string): Promise<void>
    openPath(fullPath: string): Promise<string>
    openWithPicker(fullPath: string): Promise<string>
    pickApplication(): Promise<{ name: string; path: string } | null>
    openWithApp(targetPath: string, appPath: string): Promise<string>
    writeFiles(repoPath: string, files: unknown[]): Promise<void>
  }
  difftool: {
    config(repoPath: string): Promise<DiffToolConfig>
    set(
      repoPath: string,
      values: { diffTool?: string; mergeTool?: string; keepBackup?: boolean },
      scope: 'global' | 'repo'
    ): Promise<void>
    diff(repoPath: string, file: string, rev?: string, tool?: string): Promise<string>
    merge(repoPath: string, file: string, tool?: string): Promise<string>
  }
  ssh: {
    status(): Promise<SshStatus>
    generate(name: string, comment: string, passphrase: string): Promise<string>
    addToAgent(publicKeyPath: string, passphrase: string): Promise<string>
    test(host: string): Promise<SshTest>
  }
  editor: {
    detect(): Promise<DetectedEditor[]>
    open(
      setting: EditorSetting,
      target: { path: string; line?: number; col?: number; isDir?: boolean; repo?: string }
    ): Promise<string>
  }
  settings: {
    get(): Promise<unknown>
    unlock(): Promise<unknown>
    set(settings: unknown): Promise<void>
    importFile(): Promise<unknown>
    exportFile(settings: unknown): Promise<boolean>
  }
  ai: {
    commitMessage(diff: string, cfg: unknown, ctx: unknown): Promise<unknown>
    listModels(cfg: unknown, force?: boolean): Promise<unknown>
    detectCli(): Promise<unknown>
    explainCode(code: string, lang: string, cfg: unknown): Promise<unknown>
    hoverExplain(req: unknown, cfg: unknown): Promise<unknown>
    resolveConflict(file: string, content: string, cfg: unknown): Promise<unknown>
    generateConfig(repoName: string, artifacts: unknown[], context: string, cfg: unknown): Promise<unknown>
    suggestArtifacts(repoName: string, selectedTools: string[], context: string, alreadySelected: unknown[], cfg: unknown): Promise<unknown>
    smartStage(files: unknown[], cfg: unknown): Promise<unknown>
    generateAppTheme(prompt: string, cfg: unknown): Promise<unknown>
    generateCodeTheme(prompt: string, cfg: unknown): Promise<unknown>
    generateGraphPalette(prompt: string, cfg: unknown): Promise<unknown>
    generateBranchName(description: string, cfg: unknown, ctx: unknown): Promise<unknown>
    reviewPR(diff: string, cfg: unknown): Promise<unknown>
    prDescription(commits: string, diff: string, cfg: unknown): Promise<unknown>
    planActions(prompt: string, status: unknown, cfg: unknown): Promise<unknown>
    repoChat(repoPath: string, messages: unknown, cfg: unknown, attachments: unknown): Promise<unknown>
    repoChatFinalize(repoPath: string, messages: unknown, result: unknown, cfg: unknown): Promise<unknown>
  }
  wiki: {
    facts(repoPath: string): Promise<unknown>
    imports(repoPath: string, depth: number): Promise<unknown>
    get(repoPath: string, model: string): Promise<unknown>
    generate(repoPath: string, cfg: unknown): Promise<unknown>
    export(repoPath: string): Promise<unknown>
    clear(repoPath: string): Promise<unknown>
    onProgress(cb: (payload: unknown) => void): () => void
  }
  analytics: {
    get(): Promise<unknown>
    clear(): Promise<unknown>
    setRetention(days: number): Promise<unknown>
  }
  vault: {
    list(repoPath: string): Promise<unknown>
    upsert(scope: string, repoPath: string, entry: unknown): Promise<unknown>
    remove(scope: string, repoPath: string, id: string): Promise<unknown>
    exportAll(): Promise<unknown>
    importAll(data: unknown): Promise<unknown>
  }
  repos: {
    list(): Promise<unknown>
    remember(repoPath: string): Promise<unknown>
    forget(repoPath: string): Promise<unknown>
    scan(roots: unknown): Promise<unknown>
    locate(oldPath: string, newPath: string): Promise<unknown>
    refresh(paths: string[]): Promise<unknown>
  }
  secureShare: {
    candidates(repoPath: string): Promise<unknown>
    export(repoPath: string, project: string, paths: string[], password: string): Promise<unknown>
    pick(): Promise<unknown>
    preview(bundlePath: string, password: string, repoPath: string): Promise<unknown>
    apply(bundlePath: string, password: string, repoPath: string, selected: string[]): Promise<unknown>
    exportV2(specs: unknown, project: string, password: string): Promise<unknown>
    openV2(bundlePath: string, password: string): Promise<unknown>
    previewRepoV2(
      bundlePath: string,
      password: string,
      sectionIndex: number,
      repoPath: string
    ): Promise<unknown>
    applyV2(bundlePath: string, password: string, plan: unknown): Promise<unknown>
    previewNotesV2(
      bundlePath: string,
      password: string,
      sectionIndex: number,
      repoPath: string
    ): Promise<unknown>
  }
  info: {
    list(repoPath: string): Promise<unknown>
    upsert(repoPath: string, entry: unknown): Promise<unknown>
    remove(repoPath: string, id: string): Promise<unknown>
    reorder(repoPath: string, ids: string[]): Promise<unknown>
    exportAll(): Promise<unknown>
    importAll(data: unknown): Promise<unknown>
  }
  log: {
    get(): Promise<unknown>
    clear(): Promise<unknown>
  }
  hosting: {
    listRepos(provider: string, token: string, org?: string): Promise<unknown>
    uploadSshKey(token: string, title: string, publicKey: string): Promise<unknown>
    listOwners(provider: string, token: string, org?: string): Promise<unknown>
    whoAmI(provider: string, token: string, org?: string, interactive?: boolean): Promise<unknown>
    createRepo(provider: string, token: string, opts: unknown, org?: string): Promise<unknown>
    listPRs(remoteUrl: string, tokens: unknown): Promise<unknown>
    listReleases(remoteUrl: string, tokens: unknown): Promise<unknown>
    ciStatuses(remoteUrl: string, shas: string[], token: string): Promise<unknown>
    openCreatePR(remoteUrl: string, source: string, target: string): Promise<boolean>
    createPR(remoteUrl: string, tokens: unknown, opts: unknown): Promise<unknown>
    prDetail(remoteUrl: string, tokens: unknown, number: number): Promise<unknown>
    prComment(remoteUrl: string, tokens: unknown, number: number, body: string): Promise<unknown>
    prReplyReviewComment(remoteUrl: string, tokens: unknown, number: number, inReplyTo: number | string, body: string): Promise<unknown>
    prChecks(remoteUrl: string, tokens: unknown, number: number): Promise<unknown>
    prFiles(remoteUrl: string, tokens: unknown, number: number): Promise<unknown>
    prReview(remoteUrl: string, tokens: unknown, number: number, event: string, body: string): Promise<unknown>
    prMerge(remoteUrl: string, tokens: unknown, number: number, method: string): Promise<unknown>
    listNotifications(token: string, all?: boolean): Promise<unknown>
    markNotificationRead(token: string, id: string): Promise<unknown>
    markAllNotificationsRead(token: string): Promise<unknown>
    listIssues(remoteUrl: string, tokens: unknown): Promise<unknown>
    issueDetail(remoteUrl: string, tokens: unknown, number: number): Promise<unknown>
    setIssueState(remoteUrl: string, tokens: unknown, number: number, state: string): Promise<unknown>
    createIssue(remoteUrl: string, tokens: unknown, opts: unknown): Promise<unknown>
    applyPrMeta(remoteUrl: string, tokens: unknown, number: number, meta: unknown): Promise<unknown>
    updatePR(remoteUrl: string, tokens: unknown, number: number, patch: unknown): Promise<unknown>
    mergedPrHeads(remoteUrl: string, tokens: unknown, branches: string[]): Promise<unknown>
    ensureStack(remoteUrl: string, tokens: unknown, numbers: number[]): Promise<unknown>
    listMilestones(remoteUrl: string, tokens: unknown): Promise<unknown>
    milestoneIssues(remoteUrl: string, tokens: unknown, number: number): Promise<unknown>
  }
  term: TermApi
  launch: LaunchApi
  analyze: {
    run(repoPath: string): Promise<AnalyzeResult>
    detect(repoPath: string): Promise<string[]>
    cancel(repoPath: string): void
  }
  todoScan: {
    run(repoPath: string): Promise<TodoScanResult>
    cancel(repoPath: string): void
  }
  devices: {
    list(repoPath: string): Promise<RunDeviceSnapshot>
    boot(
      device: RunDevice
    ): Promise<{ ok: true } | { error: string }>
  }
  menu: {
    set(spec: unknown): Promise<void>
    onCommand(cb: (id: string) => void): () => void
  }
  window: {
    minimize(): void
    maximize(): void
    close(): void
  }
  zoom: {
    get(): number
    set(factor: number): void
  }
  watch: {
    repo(path: string | null): Promise<void>
    mute(path: string, ms: number): Promise<void>
    onChange(cb: (payload: { path: string; light: boolean }) => void): () => void
  }
  updates: {
    getState(): Promise<UpdateState>
    check(silent?: boolean): Promise<void>
    download(): Promise<void>
    install(): void
    onEvent(cb: (state: UpdateState) => void): () => void
  }
  cli: {
    isInstalled(): Promise<boolean>
    install(): Promise<{ ok: boolean; error?: string }>
    uninstall(): Promise<{ ok: boolean; error?: string }>
    onOpenPath(cb: (payload: CliOpenPayload) => void): () => void
    onEdit(cb: (req: { file: string; sentinel: string; content: string }) => void): () => void
    finishEdit(sentinel: string, content: string | null): Promise<{ ok: boolean; error?: string }>
  }
}
