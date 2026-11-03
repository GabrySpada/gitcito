/**
 * Port vectors for the `processes` group: src/main/launch.ts, devices.ts,
 * analyze.ts, todoScan.ts, src/shared/localCi.ts and isLoopbackUrl from
 * src/main/index.ts. Replayed by internal/{launch,devices,analyze,todoscan}
 * and internal/shared.
 *
 * - Pure logic (parsers, pattern tables, token substitution) → testdata/vectors/<module>.json.
 * - launch:run / launch:runTasks are driven through the real ipcMain handlers
 *   with a fake node-pty: each case records what would be spawned (shell, args,
 *   cwd, size, the environment it adds) and every event sent to the window, and
 *   discovery runs against generated .vscode trees → internal/launch/testdata/.
 * - devices:list / devices:boot run against a fake execFile/spawn with canned
 *   SDK output → internal/devices/testdata/.
 *
 * PORT_WRITE_VECTORS=1 regenerates the files; otherwise this recomputes
 * everything and fails when the TypeScript output drifts from them.
 */
import { createRequire } from 'node:module'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync, existsSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { afterAll, describe, expect, it, vi } from 'vitest'
import ts from 'typescript'
import type { RunDevice } from '../../src/shared/types'

const h = vi.hoisted(() => ({
  handlers: new Map<string, (...a: unknown[]) => unknown>(),
  listeners: new Map<string, (...a: unknown[]) => unknown>(),
  opened: [] as string[],
  /** Canned replies for the fake execFile, keyed by "cmd args…"; null = failure. */
  fakeExec: null as null | Record<string, string | null | { error: string }>,
  execCalls: [] as Array<{ cmd: string; args: string[]; timeout: number }>,
  spawnCalls: [] as Array<{ cmd: string; args: string[]; detached: boolean }>
}))

vi.mock('electron', () => ({
  ipcMain: {
    handle: (ch: string, fn: (...a: unknown[]) => unknown) => h.handlers.set(ch, fn),
    on: (ch: string, fn: (...a: unknown[]) => unknown) => h.listeners.set(ch, fn)
  },
  shell: {
    openExternal: async (url: string) => {
      h.opened.push(url)
    }
  }
}))

vi.mock('child_process', async () => {
  const actual = await vi.importActual<typeof import('child_process')>('child_process')
  const { promisify } = await import('node:util')
  type Cb = (err: Error | null, stdout: string, stderr: string) => void
  const reply = (cmd: string, args: string[], timeout: number): Promise<{ stdout: string; stderr: string }> => {
    h.execCalls.push({ cmd, args: [...args], timeout })
    const r = h.fakeExec![[cmd, ...args].join(' ')]
    if (r === undefined || r === null) return Promise.reject(new Error(`spawn ${cmd} ENOENT`))
    if (typeof r === 'object') return Promise.reject(new Error(r.error))
    return Promise.resolve({ stdout: r, stderr: '' })
  }
  const execFile = (cmd: string, args: string[], opts: unknown, cb?: Cb): unknown => {
    if (!h.fakeExec) return (actual.execFile as unknown as (...a: unknown[]) => unknown)(cmd, args, opts, cb)
    const o = (typeof opts === 'object' && opts ? opts : {}) as { timeout?: number }
    const done = (typeof opts === 'function' ? opts : cb) as Cb
    reply(cmd, args, o.timeout ?? 0).then(
      (r) => done(null, r.stdout, r.stderr),
      (e: Error) => done(e, '', '')
    )
    return { stdin: { end: () => undefined } }
  }
  ;(execFile as unknown as Record<symbol, unknown>)[promisify.custom] = (
    cmd: string,
    args: string[],
    opts?: { timeout?: number }
  ) => {
    if (!h.fakeExec) return promisify(actual.execFile)(cmd, args, opts as never)
    return reply(cmd, args, opts?.timeout ?? 0)
  }
  const spawn = (cmd: string, args: string[], opts?: { detached?: boolean }): unknown => {
    if (!h.fakeExec) return (actual.spawn as unknown as (...a: unknown[]) => unknown)(cmd, args, opts)
    h.spawnCalls.push({ cmd, args: [...args], detached: !!opts?.detached })
    return { unref: () => undefined, on: () => undefined }
  }
  return { ...actual, default: { ...actual, execFile, spawn }, execFile, spawn }
})

// ─── fake node-pty ──────────────────────────────────────────────────────────
// launch.ts loads node-pty with a lazy `require`; intercept it at the module
// loader so the real handler code runs unchanged against a recorder.

interface FakePty {
  pid: number
  dataCb: ((d: string) => void) | null
  exitCb: ((e: { exitCode: number }) => void) | null
}
const ptys: FakePty[] = []
const spawns: unknown[] = []

const envDiff = (env: Record<string, string | undefined> | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(env ?? {})) if (v !== undefined && process.env[k] !== v) out[k] = v
  return out
}

const fakeNodePty = {
  spawn(file: string, args: string[], opts: Record<string, unknown>): unknown {
    spawns.push({
      file,
      args,
      cwd: opts.cwd,
      cols: opts.cols,
      rows: opts.rows,
      name: opts.name,
      env: envDiff(opts.env as Record<string, string>)
    })
    const p: FakePty & Record<string, unknown> = {
      pid: 4242,
      dataCb: null,
      exitCb: null,
      write: () => undefined,
      resize: () => undefined,
      kill: () => undefined,
      onData: (cb: (d: string) => void) => {
        p.dataCb = cb
      },
      onExit: (cb: (e: { exitCode: number }) => void) => {
        p.exitCb = cb
      }
    }
    ptys.push(p)
    return p
  }
}
const Mod = createRequire(import.meta.url)('module') as { _load: (...a: unknown[]) => unknown }
const origLoad = Mod._load
Mod._load = function (request: unknown, ...rest: unknown[]) {
  if (request === 'node-pty') return fakeNodePty
  return origLoad.call(this, request, ...rest)
}

const write = !!process.env.PORT_WRITE_VECTORS
const ROOT_DIR = resolve(__dirname, '../..')
function check(rel: string, data: unknown): void {
  const file = join(ROOT_DIR, rel)
  if (write) {
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, JSON.stringify(data, null, 2) + '\n')
  }
  expect(JSON.parse(readFileSync(file, 'utf8'))).toEqual(JSON.parse(JSON.stringify(data)))
}

const temps: string[] = []
afterAll(() => {
  for (const d of temps) rmSync(d, { recursive: true, force: true })
})
function tempTree(files: Record<string, string>): string {
  const root = mkdtempSync(join(tmpdir(), 'gitcito-vec-'))
  temps.push(root)
  for (const [rel, content] of Object.entries(files)) {
    mkdirSync(dirname(join(root, rel)), { recursive: true })
    writeFileSync(join(root, rel), content)
  }
  return root
}
/** Replace machine paths with placeholders, through the JSON text. */
function normalize<T>(v: T, root: string): T {
  let s = JSON.stringify(v)
  const esc = (p: string): string => JSON.stringify(p).slice(1, -1)
  if (root) s = s.split(esc(root)).join('<ROOT>')
  s = s.split(esc(homedir())).join('<HOME>')
  return JSON.parse(s) as T
}
function expand<T>(v: T, root: string): T {
  const esc = (p: string): string => JSON.stringify(p).slice(1, -1)
  return JSON.parse(JSON.stringify(v).split('<ROOT>').join(esc(root))) as T
}

// ═══ launch ═════════════════════════════════════════════════════════════════

describe('launch vectors', () => {
  it('extractDevToolsUrl, resolveInputTokens, isLoopbackUrl', async () => {
    const { extractDevToolsUrl, resolveInputTokens } = await import('../../src/main/launch')
    const devtoolsInputs = [
      'The Flutter DevTools debugger and profiler on iPhone 16 Pro is available at: http://127.0.0.1:9100?uri=http://127.0.0.1:53412/uJ8k=/',
      'Serving DevTools at http://127.0.0.1:9101/',
      'Vue Devtools listening on http://localhost:8098',
      'Vue devtools at http://localhost:8098/',
      'Prisma Studio is up on http://localhost:5555',
      'Drizzle Studio is up and running on https://local.drizzle.studio',
      'Drizzle Studio is up and running on http://127.0.0.1:4983',
      'Webpack Bundle Analyzer is started at http://127.0.0.1:8888',
      'DevTools is available at: http://127.0.0.1:9100/.',
      'DevTools is available at: http://127.0.0.1:9100/),',
      'DevTools is available at: https://devtools.example.com/',
      'A Dart VM Service is available at: http://127.0.0.1:53412/',
      'Debugger listening on ws://127.0.0.1:9229/2f1e',
      'Syncing files to device…',
      'devtools listening on http://0.0.0.0:3000/x',
      'DEVTOOLS AT HTTP://LOCALHOST:1/',
      'devtools at http://[::1]:9/',
      'devtools at http://localhost',
      'devtools at http://localhostile.com/',
      'devtools at http://127.0.0.1.evil.com/',
      'flutter devtools: available at http://example.com/ then devtools at http://127.0.0.1:2/',
      'Flutter DevTools\navailable at http://127.0.0.1:3/',
      'serving devtools at     http://127.0.0.1:4/path?q=1#frag',
      ''
    ]
    const resolveCases: Array<{ value: unknown; values: Record<string, string> }> = [
      {
        value: {
          name: 'Run ${input:who}',
          args: ['--from', '${input:who}', '${input:who}+${input:greeting}'],
          env: { GREETING: '${input:greeting}', KEEP: '${input:unknown}' },
          nested: { deep: ['${input:who}'] },
          port: 3000,
          flag: true,
          nothing: null
        },
        values: { who: 'carlos', greeting: 'hola' }
      },
      { value: '${input:a}${input:b}${input:a}', values: { a: '1', b: '$&' } },
      { value: '${input:}${input:x y}', values: { 'x y': 'spaced' } },
      { value: ['${input:a}', ['${input:a}']], values: { a: 'deep' } },
      { value: 42, values: { a: 'x' } }
    ]
    const loopbackInputs = [
      'http://localhost:3000/',
      'https://127.0.0.1/',
      'http://[::1]:9100/x',
      'http://0.0.0.0:8080',
      'http://LOCALHOST/',
      'http://localhost./',
      'http://127.1/',
      'http://0x7f.0.0.1/',
      'http://2130706433/',
      'http://[0:0:0:0:0:0:0:1]/',
      'http://user:pw@localhost:1/',
      'http:localhost',
      'http:\\\\localhost\\x',
      '  http://localhost/  ',
      'http://%6cocalhost/',
      'http://local\thost/',
      'ws://localhost/',
      'file:///etc/passwd',
      'devtools://devtools/bundled/inspector.html',
      'http://example.com/',
      'http://localhost.example.com/',
      'http://127.0.0.2/',
      'http://localhost:99999/',
      'http://[::2]/',
      'http://127.0.0.1:65535/',
      'not a url',
      '',
      'http://',
      'HTTPS://[::1]'
    ]
    // isLoopbackUrl lives in src/main/index.ts, whose import boots the whole
    // app; run its exact source text instead.
    const indexSrc = readFileSync(join(ROOT_DIR, 'src/main/index.ts'), 'utf8')
    const start = indexSrc.indexOf('export function isLoopbackUrl')
    const end = indexSrc.indexOf('\n}\n', start) + 3
    const js = ts.transpileModule(indexSrc.slice(start, end).replace('export ', ''), {
      compilerOptions: { target: ts.ScriptTarget.ES2022 }
    }).outputText
    const isLoopbackUrl = new Function(`${js}; return isLoopbackUrl`)() as (raw: string) => boolean

    check('testdata/vectors/launch.json', {
      extractDevToolsUrl: devtoolsInputs.map((input) => ({ input, out: extractDevToolsUrl(input) })),
      resolveInputTokens: resolveCases.map((c) => ({ ...c, out: resolveInputTokens(c.value, c.values) })),
      isLoopbackUrl: loopbackInputs.map((input) => ({ input, out: isLoopbackUrl(input) }))
    })
  })

  it('discovery over generated .vscode trees and the launch-configs playground', async () => {
    const { discoverLaunch } = await import('../../src/main/launch')
    const trees: Array<{ name: string; files: Record<string, string> }> = [
      {
        name: 'jsonc-root-and-nested',
        files: {
          '.vscode/launch.json': `// top comment
{
  /* block
     comment */
  "version": "0.2.0",
  "configurations": [
    {
      "name": "App", // trailing comment
      "type": "node",
      "request": "launch",
      "program": "\${workspaceFolder}/index.js",
      "args": ["it's // not a comment", "/* nor this */", "esc \\" quote // still string"],
      "port": 9229,
      "extra": { "deep": [1, 2.5, true, null], },
    },
    { "type": "node" },
    null,
    "junk",
    { "name": 7 },
  ],
  "compounds": [
    { "name": "Both", "configurations": ["App", "Other"], "stopAll": true, "presentation": { "group": "g", "order": 2 } },
    { "name": "Loose", "configurations": ["App"], "stopAll": "yes" },
    { "name": "NoMembers" },
    { "configurations": ["App"] },
  ],
  "inputs": [
    { "id": "who", "type": "promptString", "default": "me" },
    { "id": "pick", "type": "pickString", "options": ["a", { "label": "B", "value": "b" }] },
    { "type": "promptString" },
  ],
}
`,
          '.vscode/tasks.json': `{
  // tasks
  "version": "2.0.0",
  "tasks": [
    { "label": "build", "type": "shell", "command": "make", "dependsOn": ["prep"] },
    { "label": "prep", "type": "npm", "script": "prep" },
    { "type": "shell", "command": "nolabel" },
    { "label": 3 },
  ],
}`,
          'package.json': '{ "name": "x", "scripts": { "dev": "vite", "n": 1, "build": "tsc -b" } }',
          'apps/web/.vscode/launch.json': '{ "configurations": [ { "name": "Web", "type": "chrome", "url": "http://localhost:5173" } ] }',
          'apps/web/package.json': 'not json',
          'apps/api/.vscode/launch.json': '{ "configurations": [ { "name": "Api", "type": "go", "program": "." } ], "inputs": "nope" }',
          'apps/api/.vscode/tasks.json': '{ "tasks": null }',
          'apps/empty/.vscode/launch.json': '{ "configurations": [] }',
          'apps/broken/.vscode/launch.json': '{ "configurations": [ { "name": "x" } ',
          'apps/notasks/.vscode/settings.json': '{}',
          'node_modules/pkg/.vscode/launch.json': '{ "configurations": [ { "name": "Dep" } ] }',
          '.hidden/.vscode/launch.json': '{ "configurations": [ { "name": "Hidden" } ] }',
          'a/b/c/d/.vscode/launch.json': '{ "configurations": [ { "name": "Depth4" } ] }',
          'a/b/c/d/e/.vscode/launch.json': '{ "configurations": [ { "name": "Depth5" } ] }',
          'dist/.vscode/launch.json': '{ "configurations": [ { "name": "Dist" } ] }',
          'Zeta/.vscode/launch.json': '{ "compounds": [ { "name": "Only compound", "configurations": [] } ] }'
        }
      },
      {
        name: 'no-root-config',
        files: {
          'svc/.vscode/launch.json': "{ 'configurations': [] }",
          'svc2/.vscode/launch.json': '{ "configurations": [ { "name": "S2", "linux": { "program": "l" } } ] }'
        }
      }
    ]
    const cases = []
    for (const t of trees) {
      const root = tempTree(t.files)
      cases.push({ name: t.name, files: t.files, groups: normalize(await discoverLaunch(root), root) })
    }
    const pg = resolve(ROOT_DIR, 'examples/playground/launch-configs')
    const playground = normalize(await discoverLaunch(pg), pg)
    check('internal/launch/testdata/discover.json', { cases, playground })
  })

  it('launch:run / launch:runTasks spawn plans and the event stream', async () => {
    const launch = await import('../../src/main/launch')
    launch.registerLaunchHandlers()
    process.env.SHELL = '/bin/sh'
    process.env.GITCITO_VEC = 'from-env'
    const sent: Array<{ ch: string; data: unknown }> = []
    const sender = { isDestroyed: () => false, send: (ch: string, data: unknown) => sent.push({ ch, data }) }
    const run = h.handlers.get('launch:run')!
    const runTasks = h.handlers.get('launch:runTasks')!

    const tasks = [
      { label: 'prep', type: 'npm', script: 'prep', args: ['--flag', "it's"] },
      { label: 'build', type: 'shell', command: 'make all && echo ok', dependsOn: ['prep', 'lint'] },
      { label: 'lint', type: 'process', command: 'eslint', args: ['.', '--max-warnings', '0'], options: { cwd: '${workspaceFolder}/web' } },
      { label: 'serve', type: 'shell', command: 'node serve.js', isBackground: true },
      { label: 'envtask', type: 'shell', command: 'printenv', args: ['X'], options: { env: { X: '${workspaceFolder}' } } },
      { label: 'group', dependsOn: 'build' },
      { label: 'cycle-a', command: 'a', dependsOn: 'cycle-b' },
      { label: 'cycle-b', command: 'b', dependsOn: 'cycle-a' },
      { label: 'plat', command: 'generic', linux: { command: 'linux-only' }, windows: { command: 'win' } },
      { label: 'post', command: 'cleanup' }
    ]
    type Cfg = Record<string, unknown>
    const runCases: Array<{ name: string; files?: Record<string, string>; payload: Record<string, unknown> }> = [
      {
        name: 'node program with args, env and cwd',
        payload: {
          dir: '/work/app',
          config: {
            name: 'Node',
            type: 'node',
            request: 'launch',
            program: '${workspaceFolder}/src/index.js',
            args: ['--port', '3000', "it's"],
            env: { NODE_ENV: 'development', HOME_DIR: '${userHome}/x' },
            cwd: '${workspaceFolder}/src'
          },
          tasks: [],
          cols: 120,
          rows: 40
        }
      },
      {
        name: 'preLaunchTask chain with dependsOn, npm, shell and background tasks',
        payload: {
          dir: '/work/app',
          config: { name: 'Chain', type: 'node', program: 'main.mjs', preLaunchTask: 'group', postDebugTask: 'post' },
          tasks,
          cols: 0,
          rows: 0
        }
      },
      {
        name: 'background task then a program',
        payload: { dir: '/work/app', config: { name: 'Bg', type: 'python', program: 'app.py', preLaunchTask: 'serve' }, tasks, cols: 80, rows: 24 }
      },
      {
        name: 'task env prefix and cycle safety',
        payload: { dir: '/work/app', config: { name: 'Env', command: 'npm run dev', preLaunchTask: 'envtask' }, tasks, cols: 80, rows: 24 }
      },
      {
        name: 'cyclic dependsOn',
        payload: { dir: '/w', config: { name: 'Cyc', program: 'x.rb', preLaunchTask: 'cycle-a' }, tasks, cols: 80, rows: 24 }
      },
      {
        name: 'platform overrides on config and task',
        payload: {
          dir: '/w',
          config: { name: 'Plat', type: 'node', program: 'generic.js', linux: { program: 'linux.js', args: ['L'] }, osx: { program: 'mac.js' }, preLaunchTask: 'plat' },
          tasks,
          cols: 80,
          rows: 24
        }
      },
      {
        name: 'compound with members, member cwd and skipTasks',
        payload: {
          dir: '/work/app',
          config: { name: 'Both', type: 'compound', compound: ['A', 'B', 'Missing', 'Both'] },
          configs: [
            { name: 'A', type: 'node', program: 'a.js', cwd: '${workspaceFolder}/a', preLaunchTask: 'build', env: { WHO: 'a' } },
            { name: 'B', type: 'node', program: 'b.js', preLaunchTask: 'build', env: { WHO: 'b', ONLY_B: '1' } },
            { name: 'Both', type: 'compound', compound: ['A', 'B', 'Missing', 'Both'] }
          ],
          tasks,
          skipTasks: ['prep'],
          cols: 80,
          rows: 24
        }
      },
      {
        name: 'compound whose last member is wrapped in a subshell (no exec)',
        payload: {
          dir: '/w',
          config: { name: 'C', compound: ['Only'] },
          configs: [{ name: 'Only', type: 'node', program: 'o.js', cwd: 'sub' }],
          tasks: [],
          cols: 80,
          rows: 24
        }
      },
      {
        name: 'unresolvable variable is refused',
        payload: { dir: '/w', config: { name: 'Needs file', type: 'node', program: '${file}' }, tasks: [], cols: 80, rows: 24 }
      },
      {
        name: 'unanswered input is refused',
        payload: { dir: '/w', config: { name: 'In', type: 'node', program: 'x.js', args: ['${input:who}', '${input:other}'] }, inputValues: { who: 'me' }, tasks: [], cols: 80, rows: 24 }
      },
      {
        name: 'answered inputs are substituted everywhere',
        payload: {
          dir: '/w',
          config: { name: 'In ${input:who}', type: 'node', program: '${input:prog}', env: { W: '${input:who}' }, preLaunchTask: 'say' },
          tasks: [{ label: 'say', command: 'echo', args: ['${input:who}'] }],
          inputValues: { who: 'me', prog: 'p.js' },
          cols: 80,
          rows: 24
        }
      },
      { name: 'extension host is not runnable', payload: { dir: '/w', config: { name: 'Ext', type: 'extensionHost', request: 'launch' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'attach without program is not runnable', payload: { dir: '/w', config: { name: 'Att', type: 'node', request: 'attach', port: 9229 }, tasks: [], cols: 80, rows: 24 } },
      { name: 'attach with a preLaunchTask runs the task', payload: { dir: '/w', config: { name: 'Att2', type: 'node', request: 'attach', preLaunchTask: 'post' }, tasks, cols: 80, rows: 24 } },
      { name: 'missing type in error', payload: { dir: '/w', config: { name: 'NoType', request: 'Attach' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'chrome opens the url', payload: { dir: '/w', config: { name: 'Chrome', type: 'pwa-chrome', request: 'attach', url: 'http://localhost:${env:GITCITO_VEC}/' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'edge without url', payload: { dir: '/w', config: { name: 'Edge', type: 'msedge' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'flutter lib entrypoint in profile mode', payload: { dir: '/w', config: { name: 'F', type: 'dart', program: 'lib/main_dev.dart', flutterMode: 'Profile', args: ['-d', 'chrome'] }, tasks: [], cols: 80, rows: 24 } },
      { name: 'flutter without program in release mode', payload: { dir: '/w', config: { name: 'F2', type: 'flutter', flutterMode: 'release' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'plain dart script', payload: { dir: '/w', config: { name: 'D', type: 'dart', program: 'bin/tool.dart' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'go with program', payload: { dir: '/w', config: { name: 'G', type: 'go', program: '${workspaceFolder}/cmd/srv', args: ['-v'] }, tasks: [], cols: 80, rows: 24 } },
      { name: 'go from cwd', payload: { dir: '/w', config: { name: 'G2', type: 'go', cwd: '${workspaceFolder}/svc' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'go with nothing', payload: { dir: '/w', config: { name: 'G3', type: 'go' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'coreclr dll', payload: { dir: '/w', config: { name: 'N', type: 'coreclr', program: 'bin/App.DLL', args: ['x'] }, tasks: [], cols: 80, rows: 24 } },
      { name: 'coreclr exe', payload: { dir: '/w', config: { name: 'N2', type: 'clr', program: 'bin/app' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'coreclr without program', payload: { dir: '/w', config: { name: 'N3', type: 'dotnet' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'php', payload: { dir: '/w', config: { name: 'P', type: 'php', program: 'index.php' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'python module', payload: { dir: '/w', config: { name: 'Py', type: 'debugpy', module: 'uvicorn', args: ['app:app', '--reload'] }, tasks: [], cols: 80, rows: 24 } },
      { name: 'interpreters by extension', payload: { dir: '/w', config: { name: 'Ts', program: 'src/x.MTS' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'shell script', payload: { dir: '/w', config: { name: 'Sh', program: 'run.sh', args: ['a b'] }, tasks: [], cols: 80, rows: 24 } },
      { name: 'perl', payload: { dir: '/w', config: { name: 'Pl', program: 'x.pl' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'ruby type', payload: { dir: '/w', config: { name: 'Rb', type: 'rdbg', program: 'app' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'runtimeExecutable with runtimeArgs', payload: { dir: '/w', config: { name: 'Rt', type: 'node', runtimeExecutable: 'npm', runtimeArgs: ['run-script', 'dev'], program: 'ignored.js' }, tasks: [], cols: 80, rows: 24 } },
      { name: 'binary program run directly', payload: { dir: '/w', config: { name: 'Bin', program: './bin/server', args: ['--x'] }, tasks: [], cols: 80, rows: 24 } },
      { name: 'no extension takes the last character', payload: { dir: '/w', config: { name: 'Odd', program: 'scripts' }, tasks: [], cols: 80, rows: 24 } },
      {
        name: 'variables',
        payload: {
          dir: '/work/My Proj',
          config: {
            name: 'Vars',
            command: 'echo ${workspaceFolderBasename} ${workspaceRoot} ${cwd} ${pathSeparator}${/} ${env:GITCITO_VEC} ${env:GITCITO_UNSET_VAR} [${config:editor.tabSize}] ${userHome}'
          },
          tasks: [],
          cols: 80,
          rows: 24
        }
      },
      {
        name: 'replacement patterns in a folder path',
        payload: { dir: '/work/a$&b$$c', config: { name: 'Dollar', command: 'echo ${workspaceFolder} ${cwd}' }, tasks: [], cols: 80, rows: 24 }
      },
      {
        name: 'envFile merged under config env',
        files: { '.env': '# comment\nexport A=1\nB="two words"\nC=\'single\'\n  D = spaced  \nE=a=b\n=nokey\nnoequals\nWIN=crlf\r\nKEEP=from-file\n' },
        payload: {
          dir: '<ROOT>',
          config: { name: 'EnvFile', type: 'node', program: 'x.js', envFile: '${workspaceFolder}/.env', env: { KEEP: 'from-config' } },
          tasks: [],
          cols: 80,
          rows: 24
        }
      },
      {
        name: 'missing envFile is ignored',
        payload: { dir: '<ROOT>', config: { name: 'NoEnvFile', type: 'node', program: 'x.js', envFile: '${workspaceFolder}/missing.env' }, tasks: [], cols: 80, rows: 24 }
      },
      {
        name: 'empty configs array means no siblings',
        payload: { dir: '/w', config: { name: 'C', compound: ['A'] }, configs: [], tasks: [], cols: 80, rows: 24 }
      }
    ]
    const runOut = []
    for (const c of runCases) {
      const root = c.files || JSON.stringify(c.payload).includes('<ROOT>') ? tempTree(c.files ?? {}) : ''
      const payload = root ? expand(c.payload, root) : c.payload
      spawns.length = 0
      sent.length = 0
      const res = (await run({ sender }, payload)) as { id?: number; error?: string }
      runOut.push({
        name: c.name,
        files: c.files,
        payload: c.payload,
        error: res.error ?? null,
        spawn: normalize(spawns[0] ?? null, root),
        sent: normalize(sent, root)
      })
    }

    const taskCases = [
      { name: 'labels share a dedupe set', payload: { dir: '/w', tasks, labels: ['build', 'prep', 'lint'], cols: 100, rows: 30 } },
      { name: 'background and inputs', payload: { dir: '/w', tasks: [...tasks, { label: 'hi', command: 'echo', args: ['${input:who}'] }], labels: ['serve', 'hi'], inputValues: { who: 'you' }, cols: 0, rows: 0 } },
      { name: 'nothing runnable', payload: { dir: '/w', tasks, labels: ['nope', 'missing'], cols: 80, rows: 24 } },
      { name: 'dependsOn-only task with no deps', payload: { dir: '/w', tasks: [{ label: 'empty' }], labels: ['empty'], cols: 80, rows: 24 } }
    ]
    const taskOut = []
    for (const c of taskCases) {
      spawns.length = 0
      sent.length = 0
      const res = (await runTasks({ sender }, c.payload)) as { id?: number; error?: string }
      taskOut.push({ name: c.name, payload: c.payload, error: res.error ?? null, spawn: spawns[0] ?? null, sent: [...sent] })
    }

    // The event stream: data before and after attach, the DevTools watcher, a
    // serverReadyAction, and the exit code — in the order the window sees them.
    type Step = { data?: string; attach?: true; exit?: number }
    const streamCases: Array<{ name: string; config: Cfg; steps: Step[] }> = [
      {
        name: 'server ready opens once; devtools reported per new url; backlog replayed on attach',
        config: {
          name: 'Srv',
          command: 'npm run dev',
          serverReadyAction: { pattern: 'Local:\\s+(https?://\\S+)', uriFormat: '%s', action: 'openExternally' }
        },
        steps: [
          { data: 'booting\r\n' },
          { data: '\x1b[32m- Local:   http://loc' },
          { data: 'alhost:5173/\x1b[0m\r\n' },
          { data: 'The Flutter DevTools debugger and profiler on X is available at: http://127.0.0.1:9100?uri=a\r\n' },
          { data: 'again Flutter DevTools at http://127.0.0.1:9100?uri=a\r\n' },
          { attach: true },
          { data: '- Local:   http://localhost:9999/\r\n' },
          { data: 'Serving DevTools at http://127.0.0.1:9101/\r\n' },
          { exit: 3 },
          { attach: true }
        ]
      },
      {
        name: 'uriFormat with a port capture; debugWithChrome degrades to open',
        config: {
          name: 'Port',
          command: 'x',
          serverReadyAction: { pattern: 'listening on port ([0-9]+)', uriFormat: 'http://localhost:%s/app', action: 'debugWithChrome' }
        },
        steps: [{ data: 'listening on port 8080\n' }, { exit: 0 }, { attach: true }]
      },
      {
        name: 'no capture group uses the whole match; non-http urls are not opened',
        config: { name: 'Whole', command: 'x', serverReadyAction: { pattern: 'ftp://\\S+' } },
        steps: [{ data: 'at ftp://host/x\n' }, { attach: true }]
      },
      {
        name: 'startDebugging is skipped; an invalid pattern disables the watcher',
        config: { name: 'Skip', command: 'x', serverReadyAction: { pattern: 'http://\\S+', action: 'startDebugging' } },
        steps: [{ data: 'http://localhost:1/\n' }, { attach: true }]
      },
      {
        name: 'invalid regexp',
        config: { name: 'Bad', command: 'x', serverReadyAction: { pattern: '(unclosed' } },
        steps: [{ data: '(unclosed http://localhost:1/\n' }, { attach: true }]
      },
      {
        name: 'exit before attach is replayed with the backlog',
        config: { name: 'Fast', command: 'false' },
        steps: [{ data: 'oops\r\n' }, { exit: 1 }, { attach: true }, { attach: true }]
      }
    ]
    const streamOut = []
    for (const c of streamCases) {
      spawns.length = 0
      sent.length = 0
      h.opened.length = 0
      ptys.length = 0
      const res = (await run({ sender }, { dir: '/w', config: c.config, tasks: [], cols: 80, rows: 24 })) as { id: number }
      const p = ptys[0]
      for (const s of c.steps) {
        if (s.data !== undefined) p.dataCb!(s.data)
        if (s.attach) h.listeners.get('launch:attach')!({}, res.id)
        if (s.exit !== undefined) p.exitCb!({ exitCode: s.exit })
      }
      // Ids differ between runs; the vector keeps the channel's shape.
      const events = sent.map((e) => ({ ch: e.ch.replace(`:${res.id}`, ':<id>'), data: e.data }))
      streamOut.push({ name: c.name, config: c.config, steps: c.steps, events, opened: [...h.opened] })
    }

    // The backlog keeps only the tail when nobody attached for a long time.
    spawns.length = 0
    sent.length = 0
    ptys.length = 0
    const big = (await run({ sender }, { dir: '/w', config: { name: 'Big', command: 'yes' }, tasks: [], cols: 80, rows: 24 })) as { id: number }
    const line = 'x'.repeat(1023) + '\n'
    for (let i = 0; i < 300; i++) ptys[0].dataCb!(String(i).padStart(4, '0') + line)
    sent.length = 0
    h.listeners.get('launch:attach')!({}, big.id)
    const replay = sent[0]?.data as string
    const backlog = { chunks: 300, chunkLen: 1028, replayLen: replay.length, replayHead: replay.slice(0, 40) }

    check('internal/launch/testdata/run.json', { run: runOut, runTasks: taskOut, stream: streamOut, backlog })
  })
})

// ═══ devices ════════════════════════════════════════════════════════════════

describe('devices vectors', () => {
  it('parsers and merge', async () => {
    const d = await import('../../src/main/devices')
    const flutter = [
      `Warning: dart is not on your PATH.
[
  { "name": "iPhone 16 Pro", "id": "3B7F", "isSupported": true, "targetPlatform": "ios", "emulator": true, "sdk": "iOS 18.0" },
  { "name": "Pixel 8", "id": "39121", "targetPlatform": "android-arm64", "emulator": false, "sdk": "Android 14 (API 34)" },
  { "name": "sdk gphone64 arm64", "id": "emulator-5554", "targetPlatform": "android-arm64", "emulator": true, "sdk": "" },
  { "name": "macOS", "id": "macos", "targetPlatform": "darwin", "emulator": false },
  { "name": "Windows", "id": "windows", "targetPlatform": "windows-x64" },
  { "name": "Linux", "id": "linux", "targetPlatform": "linux-x64" },
  { "name": "Chrome", "id": "chrome", "targetPlatform": "web-javascript", "emulator": false },
  { "id": "fuchsia", "targetPlatform": "fuchsia-arm64", "emulator": "yes" },
  { "name": "no id" },
  null, 5, "x"
]
trailing warning`,
      'No devices detected.',
      '[not json]',
      '{"a": 1}',
      '] then [',
      '[]'
    ]
    const emulators = [
      `2 available emulators:

apple_ios_simulator • iOS Simulator • Apple • ios
Pixel_7_API_34      • Pixel 7 API 34 • Google • android
 • missing id • x • android
three • parts • only
nameless •  • Google • Android
nomaker • Name •  • iOS-ish

To run an emulator, run 'flutter emulators --launch <emulator id>'.
`
    ]
    const simctl = [
      JSON.stringify({
        devices: {
          'com.apple.CoreSimulator.SimRuntime.iOS-18-0': [
            { udid: 'UDID-1', name: 'iPhone 16', state: 'Booted', isAvailable: true },
            { udid: 'UDID-2', name: 'iPad Air', state: 'Shutdown', isAvailable: true },
            { udid: 'UDID-3', name: 'Broken', state: 'Shutdown', isAvailable: false },
            { udid: 'UDID-5', state: 'Shutdown' },
            { name: 'no udid' }
          ],
          'com.apple.CoreSimulator.SimRuntime.ios-17-5-1': [{ udid: 'UDID-6', name: 'iPhone 15', state: 'Booted' }],
          'com.apple.CoreSimulator.SimRuntime.watchOS-11-0': [{ udid: 'UDID-4', name: 'Apple Watch', state: 'Shutdown', isAvailable: true }],
          'iOS-16-0': 'not a list'
        }
      }),
      'not json',
      '{"devices": null}',
      'null'
    ]
    const adb = [
      `List of devices attached
emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a
39121FDJG0018Z         device usb:1-2 product:shiba model:Pixel_8 device:shiba
R5CT10ABCDE            unauthorized
1234567890             offline
plainserial device
lonely
`,
      'List of devices attached\n\n'
    ]
    const avds = ['INFO    | Storing crashdata\nPixel_7_API_34\nNexus_5X_API_28\n\nwith space\nhas:colon\nINFOish\n']
    const dev = (o: Partial<RunDevice>): RunDevice =>
      ({ id: 'x', name: 'x', platform: 'other', kind: 'device', running: false, source: 'adb', ...o }) as RunDevice
    const mergeCases: RunDevice[][][] = [
      [
        [dev({ id: 'U1', name: 'iPhone 16', platform: 'ios', kind: 'simulator', running: true, source: 'flutter' })],
        [
          dev({ id: 'U1', name: 'iPhone 16', platform: 'ios', kind: 'simulator', running: true, source: 'simctl' }),
          dev({ id: 'U2', name: 'iPad Air', platform: 'ios', kind: 'simulator', running: false, source: 'simctl' })
        ]
      ],
      [
        [
          dev({ id: 'chrome', name: 'Chrome', platform: 'web', kind: 'web', running: true, source: 'flutter' }),
          dev({ id: 'a', name: 'Pixel 8', platform: 'android', running: true, source: 'adb' }),
          dev({ id: 'u', name: 'iPhone 16', platform: 'ios', kind: 'simulator', running: true, source: 'simctl' }),
          dev({ id: 'm', name: 'mac', platform: 'macos', kind: 'desktop', running: true, source: 'flutter' }),
          dev({ id: 'o', name: 'Other', platform: 'other', running: true, source: 'flutter' })
        ]
      ],
      [
        [dev({ id: 'cold', name: 'Pixel 9a', platform: 'android', kind: 'simulator', source: 'avd' })],
        [dev({ id: 'emu', name: 'pixel 9A', platform: 'android', kind: 'simulator', running: true, source: 'adb', detail: 'emulator-5554' })],
        [dev({ id: 'fl', name: 'Pixel 9a', platform: 'android', kind: 'simulator', source: 'flutter-emulator' })]
      ],
      [
        [
          dev({ id: '1', name: 'b', platform: 'android', source: 'avd' }),
          dev({ id: '2', name: 'B', platform: 'android', source: 'avd' }),
          dev({ id: '3', name: 'a', platform: 'android', source: 'avd' }),
          dev({ id: '4', name: 'Á', platform: 'android', source: 'avd' }),
          dev({ id: '5', name: '10', platform: 'android', source: 'avd' }),
          dev({ id: '6', name: '9', platform: 'android', source: 'avd' })
        ]
      ],
      [
        [dev({ id: 'f', name: 'Same', platform: 'ios', running: false, source: 'flutter-emulator' })],
        [dev({ id: 's', name: 'same', platform: 'ios', running: false, source: 'simctl' })]
      ]
    ]
    const renameCases = [
      {
        devices: d.parseAdbDevices(adb[0]),
        names: { 'emulator-5554': 'Pixel_9a', plainserial: 'Odd_Name_X' }
      },
      { devices: d.parseAdbDevices(adb[0]), names: {} }
    ]
    check('testdata/vectors/devices.json', {
      parseFlutterDevices: flutter.map((input) => ({ input, out: d.parseFlutterDevices(input) })),
      parseFlutterEmulators: emulators.map((input) => ({ input, out: d.parseFlutterEmulators(input) })),
      parseSimctl: simctl.map((input) => ({ input, out: d.parseSimctl(input) })),
      parseAdbDevices: adb.map((input) => ({ input, out: d.parseAdbDevices(input) })),
      parseAvds: avds.map((input) => ({ input, out: d.parseAvds(input) })),
      mergeDevices: mergeCases.map((lists) => ({ lists, out: d.mergeDevices(lists) })),
      renameRunningEmulators: renameCases.map((c) => ({
        ...c,
        out: d.renameRunningEmulators(c.devices, new Map(Object.entries(c.names)))
      }))
    })
  })

  it('devices:list and devices:boot against canned SDK tools', async () => {
    const d = await import('../../src/main/devices')
    const flutterJson = JSON.stringify([
      { name: 'sdk gphone64 arm64', id: 'emulator-5554', targetPlatform: 'android-arm64', emulator: true, sdk: 'Android 15' },
      { name: 'Linux', id: 'linux', targetPlatform: 'linux-x64', emulator: false }
    ])
    const adbOut = `List of devices attached
emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a
emulator-5556          device product:x model:x device:y
39121FDJG0018Z         device usb:1-2 product:shiba model:Pixel_8 device:shiba
R5CT10ABCDE            unauthorized
`
    type Case = {
      name: string
      platform: NodeJS.Platform
      dart: boolean
      sdk: boolean
      shot?: string[]
      replies: Record<string, string | null>
    }
    const cases: Case[] = [
      {
        name: 'linux flutter project with an android sdk',
        platform: 'linux',
        dart: true,
        sdk: true,
        replies: {
          'flutter devices --machine': 'Warning: noise\n' + flutterJson,
          'flutter emulators': 'Pixel_9a • Pixel 9a • Google • android\napple_ios_simulator • iOS Simulator • Apple • ios\n',
          '<SDK>/platform-tools/adb devices -l': adbOut,
          '<SDK>/platform-tools/adb -s emulator-5554 emu avd name': 'Pixel_9a\nOK\n',
          '<SDK>/platform-tools/adb -s emulator-5556 emu avd name': '\n',
          '<SDK>/emulator/emulator -list-avds': 'Pixel_9a\nNexus_5X\n'
        }
      },
      { name: 'linux, nothing installed', platform: 'linux', dart: false, sdk: false, replies: {} },
      {
        name: 'macos simulators, no android',
        platform: 'darwin',
        dart: false,
        sdk: false,
        replies: {
          'xcrun simctl list devices available --json': JSON.stringify({
            devices: { 'com.apple.CoreSimulator.SimRuntime.iOS-18-0': [{ udid: 'U', name: 'iPhone 16', state: 'Shutdown' }] }
          })
        }
      },
      { name: 'macos, simctl missing, flutter project failing', platform: 'darwin', dart: true, sdk: false, replies: {} },
      {
        name: 'screenshot mode redirects found tools',
        platform: 'linux',
        dart: false,
        sdk: false,
        shot: ['adb'],
        replies: { '<BIN>/adb devices -l': 'List of devices attached\nSHOT1 device model:Shot_Phone\n' }
      }
    ]
    const realPlatform = process.platform
    const env = { ...process.env }
    const out = []
    try {
      for (const c of cases) {
        const repo = tempTree(c.dart ? { 'pubspec.yaml': 'name: app\n' } : { 'README.md': 'x\n' })
        const sdk = tempTree(c.sdk ? { 'platform-tools/adb': '', 'emulator/emulator': '' } : {})
        const bin = tempTree(Object.fromEntries((c.shot ?? []).map((t) => [t, ''])))
        delete process.env.ANDROID_HOME
        delete process.env.ANDROID_SDK_ROOT
        delete process.env.GITCITO_DEVICE_BIN
        if (c.sdk) process.env.ANDROID_HOME = sdk
        if (c.shot) {
          process.env.GITCITO_DEVICE_BIN = bin
          process.argv.push('--shot')
        }
        Object.defineProperty(process, 'platform', { value: c.platform })
        h.fakeExec = Object.fromEntries(
          Object.entries(c.replies).map(([k, v]) => [k.replace('<SDK>', sdk).replace('<BIN>', bin), v])
        )
        h.execCalls.length = 0
        const snap = await d.listRunDevices(repo)
        if (c.shot) process.argv.pop()
        const norm = (s: string): string => s.split(sdk).join('<SDK>').split(bin).join('<BIN>')
        out.push({
          ...c,
          snapshot: snap,
          calls: h.execCalls.map((x) => ({ ...x, cmd: norm(x.cmd) })).sort((a, b) =>
            [a.cmd, ...a.args].join(' ').localeCompare([b.cmd, ...b.args].join(' '))
          )
        })
      }
    } finally {
      Object.defineProperty(process, 'platform', { value: realPlatform })
      process.env = env
      h.fakeExec = null
    }

    const bootCases: Array<{ device: RunDevice; replies: Record<string, string | null | { error: string }> }> = [
      {
        device: { id: 'Pixel_7', name: 'Pixel 7', platform: 'android', kind: 'simulator', running: false, source: 'flutter-emulator' },
        replies: { 'flutter emulators --launch Pixel_7': '' }
      },
      {
        device: { id: 'Pixel_7', name: 'Pixel 7', platform: 'android', kind: 'simulator', running: false, source: 'flutter-emulator' },
        replies: { 'flutter emulators --launch Pixel_7': { error: 'Command failed: flutter emulators --launch Pixel_7\nboom' } }
      },
      {
        device: { id: 'UDID-2', name: 'iPad', platform: 'ios', kind: 'simulator', running: false, source: 'simctl' },
        replies: { 'xcrun simctl boot UDID-2': '', 'open -a Simulator': null }
      },
      {
        device: { id: 'UDID-2', name: 'iPad', platform: 'ios', kind: 'simulator', running: false, source: 'simctl' },
        replies: {}
      },
      {
        device: { id: 'Pixel_9a', name: 'Pixel 9a', platform: 'android', kind: 'simulator', running: false, source: 'avd' },
        replies: {}
      },
      {
        device: { id: 'emulator-5554', name: 'x', platform: 'android', kind: 'simulator', running: true, source: 'adb' },
        replies: {}
      }
    ]
    const bootOut = []
    try {
      delete process.env.ANDROID_HOME
      delete process.env.ANDROID_SDK_ROOT
      for (const c of bootCases) {
        h.fakeExec = c.replies as Record<string, string | null | { error: string }>
        h.execCalls.length = 0
        h.spawnCalls.length = 0
        const result = await d.bootRunDevice(c.device)
        const norm = (s: string): string => s.split(homedir()).join('<HOME>')
        bootOut.push({
          ...c,
          result,
          execCalls: [...h.execCalls],
          spawnCalls: h.spawnCalls.map((x) => ({ ...x, cmd: norm(x.cmd) }))
        })
      }
    } finally {
      process.env = env
      h.fakeExec = null
    }
    check('internal/devices/testdata/io.json', { list: out, boot: bootOut })
  })
})

// ═══ analyze ════════════════════════════════════════════════════════════════

describe('analyze vectors', () => {
  it('parsers, normalising and the project plan', async () => {
    const a = await import('../../src/main/analyze')
    const repo = '/repo'
    const dart = [
      [
        'INFO|LINT|unused_import|/repo/lib/main.dart|3|8|29|Unused import: \\|package:x\\|.',
        'WARNING|STATIC_WARNING|unused_local|/repo/lib/a.dart|10|5|4|The value of the local variable is not used.',
        'ERROR|COMPILE_TIME_ERROR|undefined_method|/repo/lib/b.dart|42|11|6|The method is not defined.',
        'ERROR|X||lib/rel.dart|x|0|1|  spaced \\\\ backslash  ',
        'HINT|Y|code|/elsewhere/c.dart|7|2|1|outside the repo',
        'INFO|Y|code||1|1|1|no file',
        'INFO|Y|code|/repo/x.dart|1|1|1|   ',
        'not a diagnostic line',
        'A|B|C|D|E|F|G'
      ].join('\n'),
      'Analyzing...\n',
      'INFO|LINT|c|/repo/crlf.dart|1|2|3|message\r\nINFO|LINT|c|/repo/lib/../lib/dots.dart| 4 |0x10|3|hex col'
    ]
    const tsc = [
      [
        "src/a.ts(12,5): error TS2304: Cannot find name 'x'.",
        'src/b.tsx(1,1): warning TS6133: Declared but never read.',
        '  /repo/src/c.ts(3,4): info TS9999: indented and absolute.  ',
        'src (weird)/d.ts(2,2): error TS1005: paren in path (1,1).',
        'src/e.ts(2,2): error ts1005: lower-case code',
        'src/f.ts(2,2): fatal TS1005: unknown level',
        'Found 2 errors.'
      ].join('\n'),
      ''
    ]
    const eslint = [
      JSON.stringify([
        {
          filePath: '/repo/src/a.ts',
          messages: [
            { ruleId: 'no-unused-vars', severity: 2, message: 'x is defined but never used.', line: 4, column: 7 },
            { ruleId: 'eqeqeq', severity: 1, message: 'Expected ===.', line: 9, column: 3 },
            { ruleId: null, severity: 2, message: 'Parsing error', line: '5', column: null },
            { ruleId: '', severity: '2', message: 'string severity' },
            { message: 42 },
            null
          ]
        },
        { filePath: '/repo/src/clean.ts', messages: [] },
        { filePath: 5, messages: [] },
        { filePath: '/repo/x.ts' },
        'junk'
      ]),
      'Oops, warnings first\n[{"filePath":"rel/y.js","messages":[{"severity":1,"message":"m","line":2.5,"column":true}]}]\nbye',
      'command not found',
      '{"not":"an array"}',
      ''
    ]
    const lines = [
      [
        'src/main.rs:10:5: warning[clippy::needless_return]: unneeded `return` statement',
        'src/lib.rs:3:1: error: cannot find value `foo` in this scope',
        'src/lib.rs:3:1: note: this is a continuation line',
        'src/lib.rs:3:1: help: and so is this',
        'cmd/main.go:7:2: unreachable code',
        'C:/win/path.go:1:2: windows drive letter',
        'vet: ./x.go:1:2: prefixed',
        '# pkg',
        'src/x.rs:1:1: error[E0425]: with code'
      ].join('\n')
    ]
    const ruff = [
      JSON.stringify([
        { filename: '/repo/app.py', code: 'F401', message: '`os` imported but unused', location: { row: 1, column: 8 } },
        { filename: 'pkg/mod.py', code: null, message: 'no code', location: null },
        { filename: '/repo/b.py', message: 'no location' },
        { filename: 3, message: 'bad' }
      ]),
      ''
    ]
    const p = (file: string, line: number, severity: string, message = 'm', col = 1, source = 's'): unknown => ({
      file,
      line,
      col,
      severity,
      message,
      source
    })
    const normaliseCases = [
      [p('a.ts', 1, 'error'), p('a.ts', 1, 'error')],
      [p('b.ts', 5, 'warning'), p('a.ts', 9, 'warning'), p('a.ts', 2, 'error'), p('a.ts', 1, 'info')],
      [
        p('src/B.ts', 1, 'error'),
        p('src/a.ts', 1, 'error'),
        p('src/a.ts', 1, 'error', 'm', 3),
        p('src/a.ts', 1, 'error', 'other', 1, 'x'),
        p('_x.ts', 1, 'warning'),
        p('10.ts', 1, 'warning'),
        p('9.ts', 1, 'warning'),
        p('é.ts', 1, 'info'),
        p('e.ts', 1, 'info')
      ]
    ]
    const planTrees: Array<Record<string, string>> = [
      { 'mobile/pubspec.yaml': 'name: app\n', 'apps/webapp/tsconfig.json': '{}' },
      { 'tsconfig.json': '{}', 'packages/ui/tsconfig.json': '{}' },
      {
        'pubspec.yaml': 'x',
        'tsconfig.json': '{}',
        'eslint.config.mjs': '',
        'Cargo.toml': '',
        'go.mod': '',
        'pyproject.toml': '',
        'web/.eslintrc.json': '',
        'web/sub/ruff.toml': '',
        'node_modules/x/tsconfig.json': '{}',
        '.hidden/go.mod': '',
        'build/go.mod': '',
        'a/b/c/go.mod': '',
        'a/b/c/d/Cargo.toml': ''
      },
      { '.eslintrc': '', 'x/.eslintrc.yml': '', 'y/eslint.config.cts': '', 'z/eslintrc.js': '', 'w/.eslintrc.foo.bar': '' },
      {}
    ]
    const plans = planTrees.map((files) => {
      const root = tempTree(files)
      return { files, plan: normalize(a.analyzerPlan(root), root), detect: a.detectAnalyzers(root) }
    })
    check('testdata/vectors/analyze.json', {
      parseDartAnalyze: dart.map((input) => ({ input, repo, out: a.parseDartAnalyze(input, repo) })),
      parseTsc: tsc.map((input) => ({ input, repo, out: a.parseTsc(input, repo) })),
      parseEslint: eslint.map((input) => ({ input, repo, out: a.parseEslint(input, repo) })),
      parseLineDiagnostics: lines.flatMap((input) =>
        ['clippy', 'go vet'].map((source) => ({ input, repo, source, out: a.parseLineDiagnostics(input, repo, source) }))
      ),
      parseRuff: ruff.map((input) => ({ input, repo, out: a.parseRuff(input, repo) })),
      normaliseProblems: normaliseCases.map((input) => ({ input, out: a.normaliseProblems(input as never) })),
      plan: plans
    })
  })
})

// ═══ todoScan ═══════════════════════════════════════════════════════════════

describe('todoScan vectors', () => {
  it('parseTodoLine, grepPattern, parseGrepOutput, sortTodos', async () => {
    const t = await import('../../src/main/todoScan')
    const lines = [
      '// TODO: ship it',
      '//TODO ship it',
      '// todo ship it',
      '# TODO: ship it',
      '  * todo: ship it',
      '// TODO(cgm): ship it',
      '// TODO (cgm) ship it',
      '// todo[CGM] ship it',
      '// TODO: @cgm ship it',
      '// TODO: @cgm: ship it',
      '// TODO(@Cgm.X+1) ship',
      '// TODO (see the issue) revisit',
      '// TODO(): empty owner',
      '// TODO[] empty brackets',
      '// TODO(this-handle-is-way-too-long-to-be-an-owner-x) msg',
      'const x = 1 // FIXME later',
      "const todos = ['todo']",
      'function reviewNote() {}',
      '        todo = [l for l in lines if UNCHECKED.match(l)]',
      'TODO: write the docs',
      '/* TODO: wire it up */',
      '<!-- FIXME: escape this -->',
      '"""TODO: docstring"""',
      "''' NOTE: triple single '''",
      '// FIXME',
      '// FIXMEX not a tag',
      '// FIX ME',
      '-- TODO: sql comment',
      '; HACK: lisp',
      '% XXX: latex',
      '//! TODO: rust inner doc',
      '# --- TODO: decorated',
      '// ==> TODO: arrows',
      '// TODO： full-width colon',
      '// TODO – en dash',
      '// TODO — em dash',
      '// TODO - hyphen',
      '// TODO\tafter tab',
      '// TODO: crlf line\r',
      '// TODO: emoji 🚀 col',
      '🚀 // TODO: emoji before',
      'x = "a" # todo: lowercase python',
      'a // b TODO not a leader right before',
      '// note NOTE both',
      'call() // reviewNotes then // REVIEW: second',
      '// TODO ' + 'long '.repeat(80),
      '   // TODO: trailing spaces   ',
      '// todo:@someone fix',
      '// TODO: @' + 'x'.repeat(40) + ' too long handle',
      '// IDEA: idea',
      '// WIP',
      '// TEMP temp',
      '// DEPRECATED: old',
      '// QUESTION? huh',
      '// OPTIMIZE: fast',
      '// REFACTOR: tidy',
      '// BUG: crash',
      '// REVIEW(bob): look',
      '/** TODO: jsdoc */',
      ' */ TODO after closer',
      '#TODO:nospace',
      ''
    ]
    const grep = [
      ['src/a.ts:12:// TODO(cgm): ship', 'node_modules/x/b.js:1:// TODO: theirs', 'src/b.ts:4:const x = 1'].join('\n'),
      'src/we:ird.ts:7:// FIXME: odd name',
      'win\\path\\x.ts:3:# TODO: backslashes\npkg/node_modules/y.js:1:// TODO: nested deps\n\nnot-a-grep-line\nsrc/c.ts:x:// TODO: bad line no\nsrc/crlf.ts:1:// TODO: crlf\r\nsrc/z.ts:9:// HACK: last'
    ]
    const td = (file: string, line: number): unknown => ({ file, line, col: 1, tag: 'TODO', message: '', text: '' })
    const sortCases = [
      [td('b.ts', 1), td('a.ts', 9), td('a.ts', 2), td('B.ts', 1), td('_.ts', 1), td('a.ts', 2), td('é.ts', 1), td('z.ts', 1)]
    ]
    check('testdata/vectors/todoScan.json', {
      tags: t.TODO_TAGS,
      grepPattern: t.grepPattern(),
      parseTodoLine: lines.map((text, i) => ({ file: 'f.ts', line: i + 1, text, out: t.parseTodoLine('f.ts', i + 1, text) })),
      parseGrepOutput: grep.map((input) => ({ input, out: t.parseGrepOutput(input) })),
      sortTodos: sortCases.map((input) => ({ input, out: t.sortTodos(input as never) }))
    })
  })
})

// ═══ localCi (shared) ═══════════════════════════════════════════════════════

describe('localCi vectors', () => {
  it('parseWorkflowName', async () => {
    const { parseWorkflowName } = await import('../../src/shared/localCi')
    const inputs = [
      'name: CI\non: [push]\n',
      'name: "Build & Test"\njobs:\n',
      'on: [push]\njobs:\n  x: {}\n',
      'on: [push]\njobs:\n  build:\n    name: inner\n',
      "name: 'single'\n",
      'name:   spaced out   \n',
      'name: CRLF\r\non: push\r\n',
      'name:\nname: second\n',
      '# comment\nname: after comment\n',
      'jobs:\nname: too late\n',
      'on:\nname: also too late\n',
      'onward: x\nname: not on\n',
      'name: "unbalanced\n',
      "name: 'a'b'\n",
      'name: "\n',
      'name: x # trailing comment\n',
      '  name: indented\nname: top\n',
      ''
    ]
    check('testdata/vectors/localCi.json', {
      parseWorkflowName: inputs.map((input) => ({ input, out: parseWorkflowName(input) }))
    })
  })
})

// The playground must exist for discovery; the global setup generates it.
it('playground present', () => {
  expect(existsSync(resolve(ROOT_DIR, 'examples/playground/launch-configs'))).toBe(true)
})
