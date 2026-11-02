import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, describe, expect, it, vi } from 'vitest'
import { jsonrepair } from 'jsonrepair'
import { migrateAIConfig } from '../../src/shared/aiAccounts'
import type { AIConfig } from '../../src/shared/types'
import * as schemas from '../../src/main/aiSchemas'
import * as transport from '../../src/main/aiTransport'
import * as cli from '../../src/main/aiCli'
import * as names from '../../src/shared/aiModelNames'
import * as grounding from '../../src/main/grounding'

/**
 * Port vectors for internal/ai (src/main/ai.ts, aiCli.ts, aiModels.ts,
 * aiSchemas.ts, aiTransport.ts), the src/shared/aiModelNames.ts helpers it
 * needs, and the slice of src/main/grounding.ts the AI features call.
 *
 * The request bodies are the product: prompt wording, field order and JSON
 * escaping are what a provider sees, so every `ai:*` channel this group owns is
 * driven through its real ipcMain handler against a fake `fetch` and a fake
 * `execFile`, and the vectors record the exact bytes each one sent — URL,
 * method, headers (secrets reduced to their scheme and a sha256) and body — plus
 * the mapped result or error. The Go tests replay the same inputs against an
 * httptest.Server and a fake CLI runner and must produce identical bytes.
 *
 * PORT_WRITE_VECTORS=1 regenerates the files; otherwise this test recomputes
 * everything and fails when the TS output drifts from them.
 */

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const WRITE = process.env.PORT_WRITE_VECTORS === '1'

const norm = (v: unknown): unknown => (v === undefined ? null : JSON.parse(JSON.stringify(v)))

function check(rel: string, data: unknown): void {
  const file = join(ROOT, rel)
  const text = JSON.stringify(data, null, 2) + '\n'
  if (WRITE) {
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, text)
    return
  }
  expect(existsSync(file), `${rel} missing — run with PORT_WRITE_VECTORS=1`).toBe(true)
  expect(JSON.parse(readFileSync(file, 'utf-8'))).toEqual(JSON.parse(text))
}

// ─── Fakes ──────────────────────────────────────────────────────────────────

interface FetchReply {
  status?: number
  body?: string
  /** A thrown `fetch failed` with this cause instead of a response. */
  network?: { code?: string; message?: string }
}
interface CliReply {
  code?: number
  stdout?: string
  stderr?: string
  enoent?: boolean
  killed?: boolean
  signal?: string
  maxBuffer?: boolean
}

const h = vi.hoisted(() => ({
  handlers: new Map<string, (...a: unknown[]) => unknown>(),
  userData: '',
  storedKey: undefined as string | undefined,
  keyError: null as string | null,
  keyCalls: [] as Array<string | null>,
  usage: [] as unknown[],
  fetchReplies: [] as unknown[],
  requests: [] as unknown[],
  cliReplies: [] as unknown[],
  procs: [] as Array<Record<string, unknown>>
}))

vi.mock('electron', () => ({
  ipcMain: { handle: (channel: string, fn: (...a: unknown[]) => unknown) => h.handlers.set(channel, fn) },
  app: { isPackaged: false, getPath: () => h.userData }
}))

vi.mock('../../src/main/settings', () => ({
  activeProfileAiKey: async (accountId?: string): Promise<string | undefined> => {
    h.keyCalls.push(accountId ?? null)
    if (h.keyError) throw new Error(h.keyError)
    return h.storedKey
  }
}))

vi.mock('../../src/main/analytics', () => ({
  recordAIUsage: async (feature: string, model: string, usage: unknown): Promise<void> => {
    h.usage.push({ feature, model, usage })
  }
}))

vi.mock('node:child_process', () => {
  type Cb = (err: Error | null, stdout: string, stderr: string) => void
  const execFile = (file: string, args: string[], opts: { timeout?: number; maxBuffer?: number }, cb: Cb): unknown => {
    const call: Record<string, unknown> = {
      file,
      args: [...args],
      timeout: opts.timeout ?? null,
      maxBuffer: opts.maxBuffer ?? null,
      input: null
    }
    h.procs.push(call)
    const reply = h.cliReplies.shift() as
      | { code?: number; stdout?: string; stderr?: string; enoent?: boolean; killed?: boolean; signal?: string; maxBuffer?: boolean }
      | undefined
    const respond = (): void => {
      const out = reply?.stdout ?? ''
      const errText = reply?.stderr ?? ''
      if (!reply) return cb(Object.assign(new Error('no canned reply'), { code: 99 }), '', 'no canned reply')
      if (reply.enoent) return cb(Object.assign(new Error(`spawn ${file} ENOENT`), { code: 'ENOENT' }), '', '')
      if (reply.killed) {
        return cb(Object.assign(new Error('killed'), { killed: true, code: null, signal: 'SIGTERM' }), out, errText)
      }
      if (reply.signal) {
        return cb(Object.assign(new Error('signal'), { killed: false, code: null, signal: reply.signal }), out, errText)
      }
      if (reply.maxBuffer) {
        return cb(Object.assign(new RangeError('stdout maxBuffer length exceeded'), { code: 'ERR_CHILD_PROCESS_STDIO_MAXBUFFER' }), out, errText)
      }
      if (reply.code) return cb(Object.assign(new Error('Command failed'), { code: reply.code }), out, errText)
      cb(null, out, errText)
    }
    return {
      stdin: {
        end: (input: string): void => {
          call.input = input
          setImmediate(respond)
        }
      }
    }
  }
  return { execFile, default: { execFile } }
})

const sha = (v: string): string => createHash('sha256').update(v).digest('hex')

/** Header names lowercased and sorted; secrets reduced to scheme + hash. */
function redactHeaders(raw: Record<string, string> | undefined): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(raw ?? {}).sort(([a], [b]) => (a.toLowerCase() < b.toLowerCase() ? -1 : 1))) {
    const name = k.toLowerCase()
    if (name === 'authorization') {
      const m = /^(\S+) (.*)$/.exec(v)
      out[name] = m ? `${m[1]} <sha256:${sha(m[2])}>` : `<sha256:${sha(v)}>`
    } else if (name === 'x-api-key' || name === 'x-goog-api-key') {
      out[name] = `<sha256:${sha(v)}>`
    } else {
      out[name] = v
    }
  }
  return out
}

vi.stubGlobal('fetch', async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
  h.requests.push({
    method: init?.method ?? 'GET',
    url: String(input),
    headers: redactHeaders(init?.headers as Record<string, string> | undefined),
    body: typeof init?.body === 'string' ? init.body : null
  })
  const reply = h.fetchReplies.shift() as FetchReply | undefined
  if (!reply) throw new Error('no canned fetch reply')
  if (reply.network) {
    const err = new TypeError('fetch failed') as TypeError & { cause?: unknown }
    err.cause = Object.assign(new Error(reply.network.message ?? ''), { code: reply.network.code })
    throw err
  }
  return new Response(reply.body ?? '', { status: reply.status ?? 200, headers: { 'content-type': 'application/json' } })
})

const tmpDirs: string[] = []
afterAll(() => {
  for (const d of tmpDirs) rmSync(d, { recursive: true, force: true })
})

function resetFakes(): void {
  h.handlers.clear()
  h.storedKey = undefined
  h.keyError = null
  h.keyCalls = []
  h.usage = []
  h.fetchReplies = []
  h.requests = []
  h.cliReplies = []
  h.procs = []
  h.userData = mkdtempSync(join(tmpdir(), 'gitcito-ai-vec-'))
  tmpDirs.push(h.userData)
}

async function freshHandlers(): Promise<void> {
  vi.resetModules()
  const ai = await import('../../src/main/ai')
  ai.registerAiHandlers()
}

async function invoke(channel: string, args: unknown[]): Promise<{ value?: unknown; error?: string }> {
  const fn = h.handlers.get(channel)
  if (!fn) throw new Error(`no handler for ${channel}`)
  try {
    return { value: norm(await fn({}, ...args)) }
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) }
  }
}

// ─── Fixed inputs ───────────────────────────────────────────────────────────

const KEY = 'sk-test-SENTINEL-0123456789'

type Over = Partial<AIConfig> & Record<string, unknown>
const cfgOf = (over: Over): AIConfig => migrateAIConfig(over as Partial<AIConfig>)

const PROVIDER_CFGS: Record<string, AIConfig> = {
  openai: cfgOf({ provider: 'openai', apiKey: KEY, model: 'gpt-4o-mini' }),
  anthropic: cfgOf({ provider: 'anthropic', apiKey: KEY, model: 'claude-haiku-4-5-20251001' }),
  google: cfgOf({ provider: 'google', apiKey: KEY, model: 'gemini-2.5-flash' }),
  openrouter: cfgOf({ provider: 'openrouter', apiKey: KEY, model: 'openai/gpt-5-mini' }),
  groq: cfgOf({ provider: 'groq', apiKey: KEY, model: 'llama-3.3-70b-versatile' }),
  mistral: cfgOf({ provider: 'mistral', apiKey: KEY, model: 'mistral-small-latest' }),
  ollama: cfgOf({ provider: 'ollama', model: 'llama3.2' }),
  customLocal: cfgOf({ provider: 'custom', endpoint: 'http://127.0.0.1:8000/v1', model: 'default' }),
  customRemote: cfgOf({ provider: 'custom', endpoint: 'https://gateway.example.com/v1/chat/completions/', apiKey: KEY, model: 'o3-mini' }),
  cliClaude: { ...cfgOf({ provider: 'cli', model: 'sonnet' }), cli: 'claude' },
  cliClaudeNoModel: { ...cfgOf({ provider: 'cli', model: '' }), cli: 'claude', model: '' },
  cliGemini: { ...cfgOf({ provider: 'cli', model: 'gemini-2.5-pro' }), cli: 'gemini', cliPath: ' /opt/bin/gemini ' },
  cliCodex: { ...cfgOf({ provider: 'cli', model: '' }), cli: 'codex', model: 'gpt-5' }
}

const DIFF = [
  'diff --git a/src/app.ts b/src/app.ts',
  'index 1111111..2222222 100644',
  '--- a/src/app.ts',
  '+++ b/src/app.ts',
  '@@ -1,5 +1,6 @@',
  " import { a } from './a'",
  '-const x = 1',
  '+const x = 2 // café ☕ <b>&amp;</b> </script>',
  '+const y = "quoted \\"value\\"" + \'\\u2028\'',
  ' ',
  ' export function f() {',
  '   return x\r',
  '@@ -20,3 +21,4 @@ export function g() {',
  "   const t = '\ttab'",
  "+  console.log('\u2028 sep \u2029 \u0001 ctl')",
  ' }',
  'diff --git a/old.txt b/old.txt',
  'deleted file mode 100644',
  '--- a/old.txt',
  '+++ /dev/null',
  '@@ -1,2 +0,0 @@',
  '-gone',
  '-away',
  'diff --git a/docs/new.md b/docs/new.md',
  'new file mode 100644',
  '--- /dev/null',
  '+++ b/docs/new.md',
  '@@ -0,0 +1,2 @@',
  '+# Title 🎉',
  '+body'
].join('\n')

const CODE = 'export function add(a: number, b: number): number {\n  // ünïcödé 🎯 <tag> & "quotes"\n  return a + b\n}\n'

const CONFLICT = [
  'line one',
  '<<<<<<< HEAD',
  'const greeting = "hello"',
  '||||||| base',
  'const greeting = "hi"',
  '=======',
  'const greeting = "ciao"',
  '>>>>>>> feature/italian',
  'tail'
].join('\n')

const HOVER_LINES = Array.from({ length: 80 }, (_, i) => ({
  no: i + 10,
  text: i === 35 ? 'const total = items.reduce((sum, it) => sum + it.price, 0) // €' : `line ${i + 10} <code> "x"`
}))

const STATUS = {
  current: 'feature/JIRA-42-login',
  tracking: 'origin/feature/JIRA-42-login',
  ahead: 1,
  behind: 0,
  staged: [{ path: 'src/a.ts', index: 'M', working_dir: ' ' }],
  unstaged: [
    { path: 'README.md', index: ' ', working_dir: 'M' },
    { path: 'docs/guide.md', index: '?', working_dir: '?' }
  ],
  conflicted: []
}

const SMART_FILES = [
  { path: 'src/app.ts', status: 'M' },
  { path: 'package-lock.json', status: 'M' },
  { path: '.env', status: '?' },
  { path: 'docs/ünï.md', status: 'A' }
]

const ARTIFACTS = [
  { path: 'CLAUDE.md', description: 'Project guide for Claude Code' },
  { path: '.cursor/rules/project.mdc', description: 'Cursor rules' }
]

const APP_COLORS = (hex: string): Record<string, string> => Object.fromEntries(schemas.APP_THEME_KEYS.map((k) => [k, hex]))
const CODE_COLORS = (hex: string): Record<string, string> => Object.fromEntries(schemas.CODE_THEME_KEYS.map((k) => [k, hex]))

/** A valid model reply for each channel, as the raw text the model returns. */
const GOOD: Record<string, string> = {
  'ai:commitMessage': JSON.stringify({ summary: 'feat: add login validation ✨', description: '- checks the token\n- guards null' }),
  'ai:explainCode': '  **Adds two numbers.**\n- returns `a + b` </script>\n',
  'ai:hoverExplain': JSON.stringify({ summary: 'Sums the item prices.', bullets: ['Uses reduce', '  starts at 0  '], lines: [45, 46] }),
  'ai:resolveConflict': '```ts\nline one\nconst greeting = "ciao"\ntail\n```\n',
  'ai:generateConfig': JSON.stringify({ files: [{ path: 'CLAUDE.md', content: '# Project\n' }, { path: '.cursor/rules/project.mdc', content: 'rules' }] }),
  'ai:suggestArtifacts': JSON.stringify({ suggestions: [{ path: '.editorconfig', description: 'Editor defaults', reason: 'Mixed indentation' }] }),
  'ai:smartStage': JSON.stringify({ toStage: ['src/app.ts', 'docs/ünï.md'], reason: 'Source and docs; lockfile and .env excluded.' }),
  'ai:generateAppTheme': JSON.stringify({ name: 'Dusk', light: APP_COLORS('#fafafa'), dark: APP_COLORS('#101010'), extra: 'kept by TS' }),
  'ai:generateCodeTheme': JSON.stringify({ name: 'Ink', light: CODE_COLORS('#FFFFFF'), dark: CODE_COLORS('#1e1e1e') }),
  'ai:generateGraphPalette': JSON.stringify({ name: 'Neon', colors: ['#ff0000', '#00ff00', '#0000ff', '#ffff00', '#ff00ff', '#00ffff', '#888888', '#123abc'] }),
  'ai:generateBranchName': '```json\n{"name": "  feature/add-login-form "}\n```',
  'ai:reviewPR': JSON.stringify({
    summary: ' Changes the constant. ',
    findings: [
      { kind: 'risk', severity: 'high', evidenceId: 'E1', claim: 'x changes meaning.', suggestion: 'Rename it.' },
      { kind: 'suggestion', severity: 'low', evidenceId: 'E3', claim: 'Doc lacks detail.', suggestion: '' },
      { kind: 'suggestion', severity: 'odd', evidenceId: 'E2', claim: 'Deleted file.', suggestion: 'Keep a stub.' }
    ]
  }),
  'ai:prDescription': JSON.stringify({ title: ' Add login validation ', body: '\n## Changes\n- validate token\n' }),
  'ai:planActions': JSON.stringify({
    summary: 'Stage and commit the markdown files',
    actions: [
      { type: 'stage', files: ['README.md', 'docs/guide.md'], description: 'Stage the docs' },
      { type: 'commit', message: 'docs: update guides', files: ['README.md'], description: 'Commit', extra: true }
    ]
  })
}

/** Arguments for each channel (cfg is appended or inserted where the channel takes it). */
function argsFor(channel: string, cfg: AIConfig): unknown[] {
  switch (channel) {
    case 'ai:commitMessage':
      return [DIFF, cfg, { branch: 'feature/JIRA-42-login' }]
    case 'ai:explainCode':
      return [CODE, 'typescript', cfg]
    case 'ai:hoverExplain':
      return [{ path: 'src/cart.ts', lang: 'typescript', token: 'reduce', line: 45, lines: HOVER_LINES }, cfg]
    case 'ai:resolveConflict':
      return ['src/greeting.ts', CONFLICT, cfg]
    case 'ai:generateConfig':
      return ['gitcito', ARTIFACTS, '  A Git client — Electron & React  ', cfg]
    case 'ai:suggestArtifacts':
      return ['gitcito', ['Claude Code', 'Cursor'], 'A Git client', ARTIFACTS, cfg]
    case 'ai:smartStage':
      return [SMART_FILES, cfg]
    case 'ai:generateAppTheme':
    case 'ai:generateCodeTheme':
    case 'ai:generateGraphPalette':
      return ['Sunset over the sea 🌅 "warm"', cfg]
    case 'ai:generateBranchName':
      return ['Add a login form with validation (JIRA-42)', cfg, { username: 'Gabriele Spada' }]
    case 'ai:reviewPR':
      return [DIFF, cfg]
    case 'ai:prDescription':
      return ['feat: add login\nfix: guard null', DIFF, cfg]
    case 'ai:planActions':
      return ['commit the markdown files', STATUS, cfg]
  }
  throw new Error(`unknown channel ${channel}`)
}

const CHANNELS = Object.keys(GOOD)

function transportOfCase(cfg: AIConfig): string {
  return transport.transportOf(cfg)
}

/** The canned wire reply carrying `text` for whatever the config speaks. */
function replyFor(cfg: AIConfig, text: string, usage = true): { fetch?: FetchReply; cli?: CliReply } {
  const t = transportOfCase(cfg)
  if (t === 'anthropic') {
    return {
      fetch: {
        body: JSON.stringify({
          content: [{ type: 'text', text }],
          ...(usage ? { usage: { input_tokens: 120, output_tokens: 30 } } : {})
        })
      }
    }
  }
  if (t === 'cli') {
    if ((cfg.cli ?? 'claude') === 'claude') {
      return { cli: { stdout: JSON.stringify({ result: text, is_error: false, usage: { input_tokens: 11, output_tokens: 7 } }) + '\n' } }
    }
    return { cli: { stdout: `\n${text}\n` } }
  }
  return {
    fetch: {
      body: JSON.stringify({
        choices: [{ message: { role: 'assistant', content: text } }],
        ...(usage ? { usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 125 } } : {})
      })
    }
  }
}

interface FeatureCase {
  name: string
  storedKey?: string
  keyError?: string
  fetch: FetchReply[]
  cli: CliReply[]
  calls: Array<{ channel: string; args: unknown[] }>
}

function push(list: FeatureCase[], c: Omit<FeatureCase, 'fetch' | 'cli'> & { fetch?: FetchReply[]; cli?: CliReply[] }): void {
  list.push({ fetch: [], cli: [], ...c })
}

function featureCases(): FeatureCase[] {
  const cases: FeatureCase[] = []

  // Every channel × the three wire protocols and the thinking-toggle provider.
  for (const provider of ['openai', 'anthropic', 'ollama', 'cliClaude']) {
    const cfg = PROVIDER_CFGS[provider]
    for (const channel of CHANNELS) {
      const r = replyFor(cfg, GOOD[channel])
      push(cases, {
        name: `${channel} via ${provider}`,
        fetch: r.fetch ? [r.fetch] : [],
        cli: r.cli ? [r.cli] : [],
        calls: [{ channel, args: argsFor(channel, cfg) }]
      })
    }
  }

  // The commit message through every provider shape.
  for (const provider of Object.keys(PROVIDER_CFGS)) {
    const cfg = PROVIDER_CFGS[provider]
    const r = replyFor(cfg, GOOD['ai:commitMessage'])
    push(cases, {
      name: `commit message via ${provider}`,
      fetch: r.fetch ? [r.fetch] : [],
      cli: r.cli ? [r.cli] : [],
      calls: [{ channel: 'ai:commitMessage', args: argsFor('ai:commitMessage', cfg) }]
    })
  }

  // Commit styles, ticket detection, description toggle, custom rules.
  const styles = ['auto', 'conventional', 'gitmoji', 'ticket', 'plain', 'caveman', 'haiku', 'bogus']
  for (const style of styles) {
    for (const branch of ['feature/JIRA-42-login', 'main']) {
      const cfg = cfgOf({ provider: 'openai', apiKey: KEY, commitStyle: style as AIConfig['commitStyle'] })
      push(cases, {
        name: `commit style ${style} on ${branch}`,
        fetch: [replyFor(cfg, GOOD['ai:commitMessage']).fetch!],
        calls: [{ channel: 'ai:commitMessage', args: ['small diff', cfg, { branch }] }]
      })
    }
  }
  {
    const cfg = cfgOf({ provider: 'openai', apiKey: KEY, generateDescription: false, customInstructions: '  Always mention the module.  ' })
    push(cases, {
      name: 'commit without description and with custom rules',
      fetch: [replyFor(cfg, GOOD['ai:commitMessage']).fetch!],
      calls: [{ channel: 'ai:commitMessage', args: ['d', cfg, { branch: 'ABC-7-x' }] }]
    })
    push(cases, {
      name: 'commit description null and long diff clipped',
      fetch: [replyFor(cfg, JSON.stringify({ summary: ' fix: x ', description: null })).fetch!],
      calls: [{ channel: 'ai:commitMessage', args: ['x'.repeat(16010), PROVIDER_CFGS.openai, { branch: 'main' }] }]
    })
  }
  {
    const cfg = PROVIDER_CFGS.openai
    push(cases, {
      name: 'commit falls back to plain text after two invalid replies',
      fetch: [
        replyFor(cfg, 'Here you go:\nfeat: add login').fetch!,
        replyFor(cfg, '  feat: add login\n\n- body line one\n- body two  ').fetch!
      ],
      calls: [{ channel: 'ai:commitMessage', args: ['d', cfg, { branch: 'main' }] }]
    })
    push(cases, {
      name: 'commit corrected on the retry',
      fetch: [replyFor(cfg, '{"summary":"a\\nb"}').fetch!, replyFor(cfg, GOOD['ai:commitMessage']).fetch!],
      calls: [{ channel: 'ai:commitMessage', args: ['d', cfg, { branch: 'main' }] }]
    })
    push(cases, {
      name: 'commit fails when both replies are empty',
      fetch: [replyFor(cfg, '').fetch!, replyFor(cfg, '   ').fetch!],
      calls: [{ channel: 'ai:commitMessage', args: ['d', cfg, { branch: 'main' }] }]
    })
  }

  // Keys: missing, resolved from the store, store failures.
  {
    const keyless = cfgOf({ provider: 'openai', apiKey: '' })
    push(cases, { name: 'missing key is refused before any request', calls: [{ channel: 'ai:explainCode', args: ['x', '', keyless] }] })
    push(cases, {
      name: 'stored key fills a keyless config',
      storedKey: 'sk-stored-SENTINEL',
      fetch: [replyFor(keyless, 'ok').fetch!],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', { ...keyless, accountId: 'acct-2' }] }]
    })
    push(cases, {
      name: 'stored key for anthropic',
      storedKey: 'sk-ant-stored-SENTINEL',
      fetch: [replyFor(PROVIDER_CFGS.anthropic, 'ok').fetch!],
      calls: [{ channel: 'ai:explainCode', args: ['x', 'go', { ...PROVIDER_CFGS.anthropic, apiKey: '  ' }] }]
    })
    push(cases, {
      name: 'key store failure propagates',
      keyError: 'Keychain access was denied.',
      calls: [{ channel: 'ai:explainCode', args: ['x', '', keyless] }]
    })
    push(cases, {
      name: 'keyless remote custom endpoint is refused',
      calls: [{ channel: 'ai:explainCode', args: ['x', '', cfgOf({ provider: 'custom', endpoint: 'https://llm.example.com/v1' })] }]
    })
    push(cases, {
      name: 'raw config without provider or model',
      fetch: [{ body: '{"choices":[{"message":{"content":"plain"}}]}' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', 'rust', { endpoint: 'http://localhost:1234/v1/', apiKey: '' }] }]
    })
  }

  // Explain and conflict styles.
  for (const style of ['normal', 'concise', 'detailed', 'eli5', 'caveman', 'pirate', 'formal', 'other']) {
    const cfg = cfgOf({ provider: 'openai', apiKey: KEY, explainStyle: style as AIConfig['explainStyle'] })
    push(cases, {
      name: `explain style ${style}`,
      fetch: [replyFor(cfg, 'ok').fetch!],
      calls: [{ channel: 'ai:explainCode', args: ['x'.repeat(16001), '', cfg] }]
    })
  }
  for (const style of ['clean', 'commented', 'conservative', 'other']) {
    const cfg = cfgOf({ provider: 'openai', apiKey: KEY, conflictStyle: style as AIConfig['conflictStyle'] })
    push(cases, {
      name: `conflict style ${style}`,
      fetch: [replyFor(cfg, 'merged\n').fetch!],
      calls: [{ channel: 'ai:resolveConflict', args: ['a.txt', CONFLICT, cfg] }]
    })
  }
  {
    const cfg = PROVIDER_CFGS.openai
    push(cases, {
      name: 'conflict retried when markers remain',
      fetch: [replyFor(cfg, CONFLICT).fetch!, replyFor(cfg, '```\nresolved\n```  \n').fetch!],
      calls: [{ channel: 'ai:resolveConflict', args: ['a.txt', CONFLICT, cfg] }]
    })
    push(cases, {
      name: 'conflict fails when markers remain twice',
      fetch: [replyFor(cfg, CONFLICT).fetch!, replyFor(cfg, '   ').fetch!],
      calls: [{ channel: 'ai:resolveConflict', args: ['a.txt', 'y'.repeat(24005), cfg] }]
    })
  }

  // Branch naming styles.
  for (const style of ['prefix/description', 'prefix/ticket-description', 'username/prefix/description', 'plain', 'other']) {
    const cfg = cfgOf({ provider: 'openai', apiKey: KEY, branchNamingStyle: style as AIConfig['branchNamingStyle'] })
    for (const username of ['Ünï Dev-Ops', undefined]) {
      push(cases, {
        name: `branch style ${style} user ${username ?? 'none'}`,
        fetch: [replyFor(cfg, '{"name":"feature/x"}').fetch!],
        calls: [{ channel: 'ai:generateBranchName', args: ['do it', cfg, username === undefined ? {} : { username }] }]
      })
    }
  }
  push(cases, {
    name: 'branch name invalid twice',
    fetch: [replyFor(PROVIDER_CFGS.openai, '{"name":"my branch"}').fetch!, replyFor(PROVIDER_CFGS.openai, '{"name":"a..b.lock"}').fetch!],
    calls: [{ channel: 'ai:generateBranchName', args: ['do it', PROVIDER_CFGS.openai, {}] }]
  })

  // Fallbacks of the JSON features.
  {
    const cfg = PROVIDER_CFGS.openai
    push(cases, {
      name: 'smart stage refuses unknown paths and stages nothing',
      fetch: [replyFor(cfg, '{"toStage":["ghost.ts"],"reason":"x"}').fetch!, replyFor(cfg, 'not json').fetch!],
      calls: [{ channel: 'ai:smartStage', args: [SMART_FILES, cfg] }]
    })
    push(cases, {
      name: 'smart stage reason missing',
      fetch: [replyFor(cfg, '{"toStage":[]}').fetch!],
      calls: [{ channel: 'ai:smartStage', args: [SMART_FILES, cfg] }]
    })
    push(cases, {
      name: 'suggestions invalid twice become none',
      fetch: [replyFor(cfg, '{"suggestions":[{"path":"CLAUDE.md","description":"d","reason":"r"}]}').fetch!, replyFor(cfg, '[]').fetch!],
      calls: [{ channel: 'ai:suggestArtifacts', args: ['r', [], '', ARTIFACTS, cfg] }]
    })
    push(cases, {
      name: 'suggestions with empty context and nothing selected',
      fetch: [replyFor(cfg, '{"suggestions":[]}').fetch!],
      calls: [{ channel: 'ai:suggestArtifacts', args: ['r', [], '   ', [], cfg] }]
    })
    push(cases, {
      name: 'generated config refuses escaping paths',
      fetch: [
        replyFor(cfg, '{"files":[{"path":"../evil.sh","content":"x"},{"path":"CLAUDE.md","content":""}]}').fetch!,
        replyFor(cfg, '{"files":[{"path":"/etc/passwd","content":"x"}]}').fetch!
      ],
      calls: [{ channel: 'ai:generateConfig', args: ['r', ARTIFACTS, '', cfg] }]
    })
    push(cases, {
      name: 'theme missing colours is corrected',
      fetch: [
        replyFor(cfg, JSON.stringify({ name: 'X', light: { bg0: 'red' }, dark: APP_COLORS('#000000') })).fetch!,
        replyFor(cfg, GOOD['ai:generateAppTheme']).fetch!
      ],
      calls: [{ channel: 'ai:generateAppTheme', args: ['dusk', cfg] }]
    })
    push(cases, {
      name: 'palette with wrong count fails',
      fetch: [replyFor(cfg, '{"name":"N","colors":["#000000"]}').fetch!, replyFor(cfg, '{"name":"","colors":[null,"red"]}').fetch!],
      calls: [{ channel: 'ai:generateGraphPalette', args: ['neon', cfg] }]
    })
    push(cases, {
      name: 'pr description falls back to text',
      fetch: [replyFor(cfg, 'Add login\n\nBody here').fetch!, replyFor(cfg, ' Add login \nBody line\nmore ').fetch!],
      calls: [{ channel: 'ai:prDescription', args: ['c'.repeat(4005), 'd'.repeat(20005), cfg] }]
    })
    push(cases, {
      name: 'plan with invented path reports the note',
      fetch: [
        replyFor(cfg, '{"summary":"s","actions":[{"type":"stage","files":["ghost"],"description":"d"}]}').fetch!,
        replyFor(cfg, '{"summary":"s","actions":[{"type":"push","description":"d"}]}').fetch!
      ],
      calls: [{ channel: 'ai:planActions', args: ['push it', STATUS, cfg] }]
    })
    push(cases, {
      name: 'plan with a note and no actions',
      fetch: [replyFor(cfg, '{"summary":"","actions":[],"note":"Use the push button."}').fetch!],
      calls: [{ channel: 'ai:planActions', args: ['p'.repeat(4003), { ...STATUS, staged: [], unstaged: [] }, cfg] }]
    })
    push(cases, {
      name: 'review with no reviewable hunks makes no request',
      calls: [{ channel: 'ai:reviewPR', args: ['Binary files differ', cfg] }]
    })
    push(cases, {
      name: 'review citing an unknown hunk is corrected',
      fetch: [
        replyFor(cfg, '{"summary":"s","findings":[{"kind":"bug","severity":"x","evidenceId":"E9","claim":""}]}').fetch!,
        replyFor(cfg, '{"summary":"s","findings":[]}').fetch!
      ],
      calls: [{ channel: 'ai:reviewPR', args: [DIFF, cfg] }]
    })
    push(cases, {
      name: 'hover with no lines is refused',
      calls: [{ channel: 'ai:hoverExplain', args: [{ path: 'a', lang: '', token: 't', line: 1, lines: [] }, cfg] }]
    })
    push(cases, {
      name: 'hover answers twice from one request',
      fetch: [replyFor(cfg, '{"summary":"s","bullets":[1,"",null,"b"],"lines":[10]}').fetch!],
      calls: [
        { channel: 'ai:hoverExplain', args: [{ path: 'a.go', lang: 'go', token: 't', line: 12, lines: HOVER_LINES.slice(0, 5) }, cfg] },
        { channel: 'ai:hoverExplain', args: [{ path: 'a.go', lang: 'go', token: 't', line: 12, lines: HOVER_LINES.slice(0, 5) }, cfg] }
      ]
    })
    push(cases, {
      name: 'hover citing a line outside the window',
      fetch: [replyFor(cfg, '{"summary":"s","lines":[999]}').fetch!, replyFor(cfg, '{"summary":"s","bullets":["a","b","c","d"]}').fetch!],
      calls: [{ channel: 'ai:hoverExplain', args: [{ path: 'a.go', lang: 'go', token: 't', line: 12, lines: HOVER_LINES.slice(0, 5) }, cfg] }]
    })
  }

  // Native structured output negotiation and the thinking toggle.
  {
    const cfg = PROVIDER_CFGS.openai
    push(cases, {
      name: 'json_schema rejected with 400 retries prompt-only',
      fetch: [
        { status: 400, body: '{"error":{"message":"Invalid parameter: response_format of type json_schema is not supported"}}' },
        replyFor(cfg, GOOD['ai:generateBranchName']).fetch!
      ],
      calls: [{ channel: 'ai:generateBranchName', args: ['x', cfg, {}] }]
    })
    push(cases, {
      name: 'a 400 unrelated to the schema is an error',
      fetch: [{ status: 400, body: 'x'.repeat(250) }],
      calls: [{ channel: 'ai:generateBranchName', args: ['x', cfg, {}] }]
    })
    push(cases, {
      name: 'a 500 is an error',
      fetch: [{ status: 500, body: '<html>Internal error ☠</html>' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', cfg] }]
    })
    push(cases, {
      name: 'a 200 with a non-JSON body',
      fetch: [{ status: 200, body: '<html>captive portal</html>' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', cfg] }]
    })
    push(cases, {
      name: 'a reply without choices is empty text',
      fetch: [{ body: '{}' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', cfg] }]
    })
    const local = PROVIDER_CFGS.customLocal
    push(cases, {
      name: 'thinking toggle rejected with 422 retries without it',
      fetch: [{ status: 422, body: 'Unrecognized request argument: chat_template_kwargs' }, replyFor(local, 'ok').fetch!],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', local] }]
    })
    push(cases, {
      name: 'thinking toggle accepted for JSON features on ollama',
      fetch: [{ status: 422, body: 'enable_thinking is not supported' }, replyFor(PROVIDER_CFGS.ollama, '{"name":"fix/x"}').fetch!],
      calls: [{ channel: 'ai:generateBranchName', args: ['x', PROVIDER_CFGS.ollama, {}] }]
    })
    push(cases, {
      name: 'a 400 on a thinking provider without the toggle in the body',
      fetch: [{ status: 400, body: 'bad request: response_format unsupported' }, replyFor(local, '{"name":"fix/x"}').fetch!],
      calls: [{ channel: 'ai:generateBranchName', args: ['x', local, {}] }]
    })
    push(cases, {
      name: 'structured validation exhausted drops the schema',
      fetch: [
        { status: 502, body: '{"error":{"message":"failed to produce a schema-valid structured response","type":"structured_output_validation_error"}}' },
        replyFor(local, '{"name":"fix/x"}').fetch!
      ],
      calls: [{ channel: 'ai:generateBranchName', args: ['x', local, {}] }]
    })
    for (const network of [
      { code: 'ECONNREFUSED', message: 'connect ECONNREFUSED 127.0.0.1:443' },
      { code: 'ENOTFOUND', message: 'getaddrinfo ENOTFOUND api.openai.com' },
      { code: 'SELF_SIGNED_CERT_IN_CHAIN', message: 'self-signed certificate in certificate chain' },
      { code: 'UNABLE_TO_VERIFY_LEAF_SIGNATURE', message: 'unable to verify the first certificate' },
      { code: 'ETIMEDOUT', message: 'connect ETIMEDOUT 10.0.0.1:443' },
      { message: '' }
    ]) {
      push(cases, {
        name: `network failure ${network.code ?? 'without cause'}`,
        fetch: [{ network }],
        calls: [{ channel: 'ai:explainCode', args: ['x', '', cfg] }]
      })
    }
    push(cases, {
      name: 'anthropic error status',
      fetch: [{ status: 401, body: '{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.anthropic] }]
    })
    push(cases, {
      name: 'anthropic network failure',
      fetch: [{ network: { code: 'ECONNREFUSED' } }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.anthropic] }]
    })
    push(cases, {
      name: 'anthropic mixed content blocks',
      fetch: [{ body: '{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"a"},{"text":"b"},{"type":"tool_use"}]}' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.anthropic] }]
    })
  }

  // Agent CLIs.
  {
    push(cases, {
      name: 'claude cli plain text when the envelope is missing',
      cli: [{ stdout: '  plain answer \n' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliClaude] }]
    })
    push(cases, {
      name: 'claude cli envelope without a result',
      cli: [{ stdout: '{"is_error":true}' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliClaude] }]
    })
    push(cases, {
      name: 'cli non-zero exit',
      cli: [{ code: 2, stdout: 'out', stderr: `  ${'e'.repeat(400)}  ` }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliGemini] }]
    })
    push(cases, {
      name: 'cli non-zero exit with only stdout',
      cli: [{ code: 1, stdout: ' oops ' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliCodex] }]
    })
    push(cases, {
      name: 'cli non-zero exit silent',
      cli: [{ code: 3 }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliCodex] }]
    })
    push(cases, {
      name: 'cli binary missing',
      cli: [{ enoent: true }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliGemini] }]
    })
    push(cases, {
      name: 'cli timed out',
      cli: [{ killed: true }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliClaude] }]
    })
    push(cases, {
      name: 'cli killed by a signal reads as success',
      cli: [{ signal: 'SIGKILL', stdout: 'partial' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliCodex] }]
    })
    push(cases, {
      name: 'cli output over the buffer cap',
      cli: [{ maxBuffer: true, stdout: 'big', stderr: '' }],
      calls: [{ channel: 'ai:explainCode', args: ['x', '', PROVIDER_CFGS.cliClaude] }]
    })
    push(cases, {
      name: 'cli conversation with a correction turn',
      cli: [{ stdout: '{"result":"not json"}' }, { stdout: '{"result":"{\\"name\\":\\"fix/y\\"}"}' }],
      calls: [{ channel: 'ai:generateBranchName', args: ['x', PROVIDER_CFGS.cliClaudeNoModel, {}] }]
    })
  }

  return cases
}

async function runFeatureCase(c: FeatureCase): Promise<unknown> {
  resetFakes()
  await freshHandlers()
  h.storedKey = c.storedKey
  h.keyError = c.keyError ?? null
  h.fetchReplies = [...c.fetch]
  h.cliReplies = [...c.cli]
  const results: unknown[] = []
  for (const call of c.calls) results.push(await invoke(call.channel, call.args))
  return {
    name: c.name,
    storedKey: c.storedKey ?? null,
    keyError: c.keyError ?? null,
    fetch: c.fetch,
    cli: c.cli,
    calls: norm(c.calls),
    requests: h.requests,
    procs: h.procs,
    keyCalls: h.keyCalls,
    usage: h.usage,
    results,
    unusedFetch: h.fetchReplies.length,
    unusedCli: h.cliReplies.length
  }
}

// ─── Direct transport calls (shapes no feature produces) ────────────────────

interface TransportCase {
  name: string
  cfg: AIConfig
  messages: Array<{ role: string; content: string }>
  temperature: number
  /** maxTokens as JSON: a number, or "NaN"/"Infinity" for the non-finite ones. */
  options?: { extra?: Record<string, unknown>; maxTokens?: number | string; disableThinking?: boolean }
  fetch: FetchReply[]
  cli: CliReply[]
}

function transportCases(): TransportCase[] {
  const sys = [{ role: 'system', content: 'Only system.' }, { role: 'system', content: 'Second system.' }]
  const chat = [
    { role: 'system', content: 'S' },
    { role: 'user', content: 'U1' },
    { role: 'assistant', content: 'A1' },
    { role: 'user', content: 'U2' }
  ]
  const okOpenAI: FetchReply = { body: '{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3}}' }
  const okAnthropic: FetchReply = { body: '{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":4,"output_tokens":5}}' }
  const cases: TransportCase[] = []
  const add = (c: Omit<TransportCase, 'fetch' | 'cli'> & { fetch?: FetchReply[]; cli?: CliReply[] }): void => {
    cases.push({ fetch: [], cli: [], ...c })
  }
  add({ name: 'anthropic system-only prompt', cfg: PROVIDER_CFGS.anthropic, messages: sys, temperature: 0, fetch: [okAnthropic] })
  add({ name: 'anthropic chat with every role', cfg: PROVIDER_CFGS.anthropic, messages: chat, temperature: 1.5, fetch: [okAnthropic] })
  for (const maxTokens of [99999.9, -4.2, 'NaN', 'Infinity', 0, 1, 8192, 300.7]) {
    add({
      name: `anthropic maxTokens ${maxTokens}`,
      cfg: PROVIDER_CFGS.anthropic,
      messages: chat,
      temperature: 0.2,
      options: { maxTokens, extra: { response_format: { type: 'json_object' } } },
      fetch: [okAnthropic]
    })
    add({
      name: `openai maxTokens ${maxTokens}`,
      cfg: PROVIDER_CFGS.openai,
      messages: chat,
      temperature: 0.2,
      options: { maxTokens },
      fetch: [okOpenAI]
    })
  }
  for (const model of ['o3-mini', 'gpt-5-mini', 'openai/o1', 'ft:gpt-5:org:x:1', 'gpt-4o', 'o10', 'gpt-50']) {
    add({
      name: `openai token field for ${model}`,
      cfg: { ...PROVIDER_CFGS.openai, model },
      messages: chat,
      temperature: 0.3,
      options: { maxTokens: 256, disableThinking: true },
      fetch: [okOpenAI]
    })
  }
  add({
    name: 'extra overrides a payload field in place',
    cfg: PROVIDER_CFGS.customLocal,
    messages: chat,
    temperature: 0.2,
    options: { extra: { model: 'override', temperature: 1, seed: 7 }, maxTokens: 10, disableThinking: true },
    fetch: [okOpenAI]
  })
  add({ name: 'openai empty model', cfg: { ...PROVIDER_CFGS.openai, model: '' }, messages: chat, temperature: 0.25, fetch: [okOpenAI] })
  add({ name: 'cli codex flattened chat', cfg: PROVIDER_CFGS.cliCodex, messages: chat, temperature: 0.2, cli: [{ stdout: 'answer' }] })
  add({ name: 'cli gemini system only', cfg: PROVIDER_CFGS.cliGemini, messages: sys, temperature: 0.2, cli: [{ stdout: 'answer' }] })
  add({
    name: 'cli claude usage envelope',
    cfg: { ...PROVIDER_CFGS.cliClaude, cliPath: '' },
    messages: chat,
    temperature: 0.2,
    cli: [{ stdout: '{"result":"r","usage":{"input_tokens":2}}' }]
  })
  add({
    name: 'cli unknown binary falls back to claude',
    cfg: { ...PROVIDER_CFGS.cliClaude, cli: 'nope' as AIConfig['cli'] },
    messages: chat,
    temperature: 0.2,
    cli: [{ stdout: 'x' }]
  })
  return cases
}

async function runTransportCase(c: TransportCase): Promise<unknown> {
  resetFakes()
  vi.resetModules()
  const t = await import('../../src/main/aiTransport')
  h.fetchReplies = [...c.fetch]
  h.cliReplies = [...c.cli]
  const options = c.options
    ? {
        ...c.options,
        ...(c.options.maxTokens !== undefined ? { maxTokens: Number(c.options.maxTokens) } : {})
      }
    : undefined
  let result: unknown
  try {
    result = { value: norm(await t.callModel(c.cfg, c.messages as never, c.temperature, options)) }
  } catch (err) {
    result = { error: err instanceof Error ? err.message : String(err) }
  }
  return { ...norm(c) as object, requests: h.requests, procs: h.procs, result }
}

// ─── Model catalogue ────────────────────────────────────────────────────────

const T0 = 1_760_000_000_000
const DAY = 24 * 60 * 60 * 1000

interface ModelStep {
  at: number
  cfg: AIConfig
  force?: boolean
  storedKey?: string
  fetch?: FetchReply[]
}
interface ModelScenario {
  name: string
  /** Cache file content before the first step (null = no file). */
  cacheFile: string | null
  steps: ModelStep[]
}

function modelScenarios(): ModelScenario[] {
  const openaiList = JSON.stringify({
    object: 'list',
    data: [
      { id: 'gpt-4o', created: 1_715_000_000 },
      { id: 'gpt-4o-2024-08-06', created: 1_722_000_000 },
      { id: 'gpt-4o-mini', created: 1_721_000_000 },
      { id: 'text-embedding-3-small', created: 1_705_000_000 },
      { id: 'whisper-1', created: 1_677_000_000 },
      { id: 'o3-mini', created: 1_737_000_000_000 },
      { id: 'babbage-002', created: 1_692_000_000 },
      { id: 'Custom-Model', created: 0 },
      { id: 'custom-model-b' },
      { id: '  models/gpt-4o  ' }
    ]
  })
  const anthropicList = JSON.stringify({
    data: [
      { id: 'claude-haiku-4-5-20251001', created_at: '2025-10-01T00:00:00Z' },
      { id: 'claude-opus-4-1-20250805', created_at: '2025-08-05T00:00:00Z' },
      { id: 'claude-3-5-haiku-20241022', created_at: 'not a date' },
      { id: 'claude-sonnet-4-5', created_at: '2025-09-29T12:30:00.000Z' }
    ]
  })
  const googleList = JSON.stringify({
    models: [
      { name: 'models/gemini-2.5-flash', supportedGenerationMethods: ['generateContent', 'countTokens'] },
      { name: 'models/text-embedding-004', supportedGenerationMethods: ['embedContent'] },
      { name: 'models/gemini-2.5-pro', supportedGenerationMethods: ['generateContent'] },
      { name: 'models/gemini-1.5-flash-002' }
    ]
  })
  const ollamaList = JSON.stringify({ data: [{ id: 'qwen2.5:7b', created: 1_730_000_000 }, { id: 'llama3.2:latest', created: 1_730_000_000 }] })
  const keyless = cfgOf({ provider: 'openai', apiKey: '' })
  const staleCache = JSON.stringify({
    [`openai:https://api.openai.com/v1`]: { models: ['gpt-4o-2024-05-13', 'gpt-4o', 'dall-e-3'], fetchedAt: T0 - 2 * DAY }
  })
  const freshCache = JSON.stringify({
    'cli:claude': { models: ['x'], fetchedAt: T0 },
    [`openai:https://api.openai.com/v1`]: {
      models: ['gpt-4o-2024-05-13', 'gpt-4o', 'tts-1'],
      allModels: ['gpt-4o-2024-05-13', 'gpt-4o', 'tts-1', 'zeta'],
      fetchedAt: T0 - 1000
    }
  })
  return [
    { name: 'openai live list', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.openai, fetch: [{ body: openaiList }] }] },
    { name: 'anthropic live list', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.anthropic, fetch: [{ body: anthropicList }] }] },
    { name: 'google native list', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.google, fetch: [{ body: googleList }] }] },
    {
      name: 'google keyless list',
      cacheFile: null,
      steps: [{ at: T0, cfg: cfgOf({ provider: 'google', apiKey: '' }), fetch: [{ body: googleList }] }]
    },
    { name: 'ollama list', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.ollama, fetch: [{ body: ollamaList }] }] },
    { name: 'openrouter list', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.openrouter, fetch: [{ body: openaiList }] }] },
    { name: 'cli list never fetches', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.cliGemini }, { at: T0 + 5, cfg: PROVIDER_CFGS.cliCodex }] },
    {
      name: 'cached list is served until it expires, then refreshed',
      cacheFile: null,
      steps: [
        { at: T0, cfg: PROVIDER_CFGS.openai, fetch: [{ body: openaiList }] },
        { at: T0 + DAY - 1, cfg: PROVIDER_CFGS.openai },
        { at: T0 + DAY, cfg: PROVIDER_CFGS.openai, fetch: [{ body: ollamaList }] },
        { at: T0 + DAY + 1, cfg: PROVIDER_CFGS.openai, force: true, fetch: [{ status: 503, body: 'unavailable' }] }
      ]
    },
    { name: 'fresh cache from disk', cacheFile: freshCache, steps: [{ at: T0, cfg: PROVIDER_CFGS.openai }, { at: T0, cfg: PROVIDER_CFGS.cliClaude }] },
    {
      name: 'stale cache beats the bundled list on failure',
      cacheFile: staleCache,
      steps: [{ at: T0, cfg: PROVIDER_CFGS.openai, fetch: [{ network: { code: 'ECONNREFUSED' } }] }]
    },
    { name: 'corrupt cache is a cold start', cacheFile: '{nope', steps: [{ at: T0, cfg: PROVIDER_CFGS.openai, fetch: [{ status: 401, body: 'y'.repeat(300) }] }] },
    { name: 'remote network failure hints at the proxy', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.groq, fetch: [{ network: { code: 'ENOTFOUND' } }] }] },
    { name: 'local network failure hints at the provider', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.ollama, fetch: [{ network: { code: 'ECONNREFUSED', message: 'x' } }] }] },
    { name: 'empty list is an error', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.mistral, fetch: [{ body: '{"data":[]}' }] }] },
    { name: 'malformed list body', cacheFile: null, steps: [{ at: T0, cfg: PROVIDER_CFGS.mistral, fetch: [{ body: 'not json' }] }] },
    {
      name: 'stored key is used for listing',
      cacheFile: null,
      steps: [{ at: T0, cfg: keyless, storedKey: 'sk-stored-SENTINEL', fetch: [{ body: openaiList }] }]
    },
    {
      name: 'anthropic legacy endpoint and custom gateway share no cache',
      cacheFile: null,
      steps: [
        { at: T0, cfg: cfgOf({ provider: 'anthropic', endpoint: 'https://api.anthropic.com/v1/', apiKey: KEY }), fetch: [{ body: anthropicList }] },
        { at: T0 + 1, cfg: PROVIDER_CFGS.customRemote, fetch: [{ body: openaiList }] }
      ]
    }
  ]
}

async function runModelScenario(s: ModelScenario): Promise<unknown> {
  resetFakes()
  const cachePath = join(h.userData, 'gitcito-models.json')
  if (s.cacheFile !== null) writeFileSync(cachePath, s.cacheFile)
  await freshHandlers()
  vi.useFakeTimers({ toFake: ['Date'] })
  const steps: unknown[] = []
  try {
    for (const step of s.steps) {
      vi.setSystemTime(step.at)
      h.storedKey = step.storedKey
      h.fetchReplies = [...(step.fetch ?? [])]
      h.requests = []
      h.keyCalls = []
      const result = await invoke('ai:listModels', step.force === undefined ? [step.cfg] : [step.cfg, step.force])
      steps.push({
        at: step.at,
        cfg: step.cfg,
        force: step.force ?? null,
        storedKey: step.storedKey ?? null,
        fetch: step.fetch ?? [],
        requests: h.requests,
        keyCalls: h.keyCalls,
        result,
        cacheFile: existsSync(cachePath) ? readFileSync(cachePath, 'utf-8') : null
      })
    }
  } finally {
    vi.useRealTimers()
  }
  return { name: s.name, cacheFile: s.cacheFile, steps }
}

// ─── CLI detection ──────────────────────────────────────────────────────────

const DETECT_SCENARIOS: Array<{ name: string; cli: CliReply[] }> = [
  { name: 'all three installed', cli: [{ stdout: '/usr/local/bin/claude\n' }, { stdout: ' /usr/bin/gemini \r\n/other/gemini\n' }, { stdout: '/opt/codex' }] },
  { name: 'only codex', cli: [{ code: 1 }, { code: 1, stdout: '' }, { stdout: '/opt/codex\n' }] },
  { name: 'lookup tool missing', cli: [{ enoent: true }, { enoent: true }, { enoent: true }] },
  { name: 'empty stdout and a timeout', cli: [{ stdout: '\n' }, { killed: true }, { stdout: '   ' }] }
]

async function runDetect(s: { name: string; cli: CliReply[] }): Promise<unknown> {
  resetFakes()
  await freshHandlers()
  h.cliReplies = [...s.cli]
  const result = await invoke('ai:detectCli', [])
  return { name: s.name, cli: s.cli, procs: h.procs, result }
}

// ─── Pure vectors ───────────────────────────────────────────────────────────

const ANY_VALUES: unknown[] = [null, true, 0, 1.5, 'text', [], {}, ['a'], { a: 1 }]

function schemaVectors(): unknown {
  const s = schemas
  const stringify = (v: unknown): string => JSON.stringify(v)
  const theme = (keys: readonly string[], hex: string): Record<string, unknown> => Object.fromEntries(keys.map((k) => [k, hex]))
  const commitInputs: unknown[] = [
    ...ANY_VALUES,
    { summary: 'feat: add login', description: '' },
    { summary: 'fix: guard null', description: '- checks the token' },
    { summary: '  ', description: '' },
    { summary: 'one\ntwo', description: '' },
    { summary: 'ok' },
    { summary: 'ok', description: null },
    { summary: 'ok', description: 5 },
    { summary: 5, description: [] },
    { summary: '\u00a0\ufeff', description: 'x' },
    { summary: '\u0085', description: 'x' },
    'Sure! Here is your commit'
  ]
  const fileInputs: Array<[unknown, string[]]> = [
    [{ files: [{ path: 'CLAUDE.md', content: '# Project' }] }, ['CLAUDE.md', '.cursor/rules/project.mdc']],
    [{ files: [{ path: ' CLAUDE.md ', content: 'x' }] }, ['CLAUDE.md']],
    [{ files: [] }, ['CLAUDE.md']],
    [{ files: 'nope' }, []],
    [{}, []],
    [null, []],
    [{ files: [null, 3, { path: '../x', content: '' }, { content: 'c' }, { path: 'a', content: 'b' }] }, []],
    [{ files: [{ path: 'sneaky.sh', content: 'x' }, { path: 'CLAUDE.md', content: '  ' }] }, ['CLAUDE.md', 'b']],
    [{ files: [{ path: 'C:\\Windows\\system.ini', content: 'x' }, { path: '/etc/passwd', content: 'x' }, { path: 'a/../../b', content: 'x' }] }, []],
    [{ files: [{ path: 7, content: 'x' }, { path: { p: 1 }, content: 'x' }] }, []]
  ]
  const known = ['src/app.ts', 'package-lock.json', 'docs/ünï.md']
  const stageInputs: unknown[] = [
    ...ANY_VALUES,
    { toStage: ['src/app.ts'], reason: 'source only' },
    { toStage: [], reason: 'nothing' },
    { toStage: ['src/ghost.ts', 3, null, 'docs/ünï.md'], reason: '' },
    { toStage: 'src/app.ts' },
    { toStage: [], reason: 4 },
    { toStage: [], reason: null }
  ]
  const suggestionInputs: Array<[unknown, string[]]> = [
    [{ suggestions: [{ path: '.editorconfig', description: 'Editor defaults', reason: 'Mixed' }] }, ['CLAUDE.md']],
    [{ suggestions: [{ path: ' CLAUDE.md ', description: 'd', reason: 'r' }] }, ['CLAUDE.md']],
    [{ suggestions: [{ path: '../x', description: 'd', reason: 'r' }, { path: 'a.md', description: '', reason: ' ' }] }, []],
    [{ suggestions: [] }, []],
    [{ suggestions: {} }, []],
    [{ suggestions: [null, [], 'x'] }, []],
    [[], []],
    [{}, []]
  ]
  const themeInputs: Array<[unknown, 'app' | 'code']> = [
    [{ name: 'Dusk', light: theme(s.APP_THEME_KEYS, '#ffffff'), dark: theme(s.APP_THEME_KEYS, '#101010') }, 'app'],
    [{ name: 'X', light: { ...theme(s.APP_THEME_KEYS, '#ffffff'), accent: undefined }, dark: theme(s.APP_THEME_KEYS, '#101010') }, 'app'],
    [{ name: 'X', light: { bg0: '#fff', red: 'crimson', green: ['#aabbcc'], text0: 123456, border: null }, dark: [] }, 'app'],
    [{ name: '', light: null }, 'app'],
    [{ name: 'Ink', light: theme(s.CODE_THEME_KEYS, '#ABCDEF'), dark: theme(s.CODE_THEME_KEYS, '#abcdeg') }, 'code'],
    [{ light: {}, dark: {} }, 'code'],
    ['theme', 'code'],
    [null, 'app']
  ]
  const eight = Array.from({ length: 8 }, () => '#123456')
  const paletteInputs: unknown[] = [
    ...ANY_VALUES,
    { name: 'Neon', colors: eight },
    { name: 'Neon', colors: eight.slice(0, 6) },
    { name: 'Neon', colors: [...eight.slice(1), 'red'] },
    { name: ' ', colors: 'x' },
    { name: 'N', colors: [null, 1, true, ['#123456'], { a: 1 }, '#12345', '#1234567', '#GGGGGG', '#abcdef'] }
  ]
  const prInputs: unknown[] = [
    ...ANY_VALUES,
    { title: 'Add login', body: '## Changes\n- x' },
    { title: 'a\nb', body: 'x' },
    { title: 'Add login', body: '' },
    { title: ' ', body: ' ' },
    { title: 3, body: 3 }
  ]
  const branchInputs: unknown[] = [
    ...ANY_VALUES,
    { name: 'feature/CMS-12-add-login' },
    { name: 'my branch' },
    { name: 'feature//x' },
    { name: 'feature/x.lock' },
    { name: 'a..b' },
    { name: 'feature/¡hola!' },
    { name: '/lead' },
    { name: 'trail/' },
    { name: '  padded  ' },
    { name: 'a\u00a0b' },
    { name: 'x'.repeat(101) },
    { name: 'x'.repeat(100) },
    { name: '' },
    { name: 7 },
    { name: 'tab\there' }
  ]
  const resolvedInputs = [
    'const a = 1\nconst b = 2\n',
    'a\n<<<<<<< HEAD\nb\n=======\nc\n>>>>>>> theirs\n',
    '   ',
    '',
    '=======',
    'x =======',
    'a\r>>>>>>> b',
    'a\u2028||||||| base',
    'a\u2029<<<<<<<',
    '<<<<<< six',
    'ok\n\n'
  ]
  const safePaths: unknown[] = [
    '.cursor/rules/project.mdc',
    '.git/hooks/pre-commit',
    'a/..b/c',
    '../../etc/passwd',
    '/etc/passwd',
    '\\\\server\\share',
    'C:\\Windows\\system.ini',
    'c:relative',
    'a/../../b',
    'a\\..\\b',
    '..',
    'x/..',
    'a\u0000b',
    '',
    '   ',
    ' ok.md ',
    'x'.repeat(400),
    'x'.repeat(401),
    ` ${'x'.repeat(400)} `,
    '😀'.repeat(200),
    '😀'.repeat(201),
    '\ufeff/etc/passwd',
    '\u0085../x',
    '\u00a0/abs',
    '\u2028..',
    'a//b',
    'a/./b',
    '1:x',
    null,
    7,
    ['a'],
    { p: 'a' }
  ]
  return {
    schemas: {
      COMMIT_SCHEMA: stringify(s.COMMIT_SCHEMA),
      CONFIG_FILES_SCHEMA: stringify(s.CONFIG_FILES_SCHEMA),
      ASK_ACTION_SCHEMA: stringify(s.ASK_ACTION_SCHEMA),
      ASK_ACTIONS_SCHEMA: stringify(s.ASK_ACTIONS_SCHEMA),
      'askActionSchema()': stringify(s.askActionSchema()),
      'askActionSchema(history)': stringify(s.askActionSchema({ history: true })),
      'askActionSchema(remote)': stringify(s.askActionSchema({ remote: true })),
      'askActionSchema(history,remote)': stringify(s.askActionSchema({ history: true, remote: true })),
      'askActionsSchema(false)': stringify(s.askActionsSchema(false)),
      'askActionsSchema(true)': stringify(s.askActionsSchema(true)),
      'repoChatActionsSchema(false)': stringify(s.repoChatActionsSchema(false)),
      'repoChatActionsSchema(true)': stringify(s.repoChatActionsSchema(true)),
      REPO_CHAT_ACTIONS_SCHEMA: stringify(s.REPO_CHAT_ACTIONS_SCHEMA),
      SMART_STAGE_SCHEMA: stringify(s.SMART_STAGE_SCHEMA),
      ARTIFACT_SUGGESTIONS_SCHEMA: stringify(s.ARTIFACT_SUGGESTIONS_SCHEMA),
      APP_THEME_SCHEMA: stringify(s.APP_THEME_SCHEMA),
      CODE_THEME_SCHEMA: stringify(s.CODE_THEME_SCHEMA),
      GRAPH_PALETTE_SCHEMA: stringify(s.GRAPH_PALETTE_SCHEMA),
      PR_DESCRIPTION_SCHEMA: stringify(s.PR_DESCRIPTION_SCHEMA),
      BRANCH_NAME_SCHEMA: stringify(s.BRANCH_NAME_SCHEMA)
    },
    appThemeKeys: [...s.APP_THEME_KEYS],
    codeThemeKeys: [...s.CODE_THEME_KEYS],
    validateCommitMessage: commitInputs.map((input) => ({ input: norm(input), out: s.validateCommitMessage(norm(input)) })),
    validateGeneratedFiles: fileInputs.map(([input, requested]) => ({
      input: norm(input),
      requested,
      out: s.validateGeneratedFiles(norm(input), requested)
    })),
    validateSmartStage: stageInputs.map((input) => ({ input: norm(input), known, out: s.validateSmartStage(norm(input), new Set(known)) })),
    validateArtifactSuggestions: suggestionInputs.map(([input, already]) => ({
      input: norm(input),
      already,
      out: s.validateArtifactSuggestions(norm(input), already)
    })),
    validateTheme: themeInputs.map(([input, kind]) => ({
      input: norm(input),
      keys: kind,
      out: s.validateTheme(norm(input), kind === 'app' ? s.APP_THEME_KEYS : s.CODE_THEME_KEYS)
    })),
    validateGraphPalette: paletteInputs.map((input) => ({ input: norm(input), out: s.validateGraphPalette(norm(input)) })),
    validatePRDescription: prInputs.map((input) => ({ input: norm(input), out: s.validatePRDescription(norm(input)) })),
    validateBranchName: branchInputs.map((input) => ({ input: norm(input), out: s.validateBranchName(norm(input)) })),
    validateResolvedFile: resolvedInputs.map((input) => ({ input, out: s.validateResolvedFile(input) })),
    isSafeRepoPath: safePaths.map((input) => ({ input: norm(input), out: s.isSafeRepoPath(norm(input)) }))
  }
}

function transportVectors(): unknown {
  const t = transport
  const cfgs: Array<[string, AIConfig]> = [
    ...Object.entries(PROVIDER_CFGS),
    ['anthropic legacy v1', cfgOf({ provider: 'anthropic', endpoint: 'https://api.anthropic.com/v1', apiKey: 'sk-ant' })],
    ['anthropic trailing slashes', cfgOf({ provider: 'anthropic', endpoint: 'https://proxy.local/v1///', apiKey: ' sk ' })],
    ['custom chat completions url', cfgOf({ provider: 'custom', endpoint: 'https://gateway.internal/v1/chat/completions/' })],
    ['custom empty endpoint', cfgOf({ provider: 'custom', endpoint: '' })],
    ['openai blank key', cfgOf({ provider: 'openai', apiKey: '   ' })],
    ['ollama blank', cfgOf({ provider: 'ollama', apiKey: '' })],
    ['cli no key', cfgOf({ provider: 'cli', apiKey: '' })],
    ['raw no provider', { endpoint: 'http://0.0.0.0:9/v1', apiKey: 'k' } as unknown as AIConfig],
    ['raw transport override', { provider: 'openai', transport: 'anthropic', endpoint: 'https://x/v1', apiKey: 'k' } as unknown as AIConfig],
    ['raw nothing', {} as unknown as AIConfig],
    ['raw unknown provider', { provider: 'acme', endpoint: 'https://acme.example/v1/' } as unknown as AIConfig],
    ['raw localhost no port', { provider: 'custom', endpoint: 'http://localhost' } as unknown as AIConfig],
    ['raw localhost lookalike', { provider: 'custom', endpoint: 'http://localhost.evil.com/v1' } as unknown as AIConfig]
  ]
  const urls = [
    'http://localhost:11434/v1',
    'https://localhost',
    'http://127.0.0.1/v1',
    'http://0.0.0.0:8080',
    'http://localhost.evil.com',
    'http://127.0.0.10:8',
    'ftp://localhost',
    'HTTP://localhost',
    'https://api.openai.com/v1',
    ''
  ]
  const models = [
    'o1',
    'o1-mini',
    'o3',
    'o3-mini',
    'o4-mini',
    'o9.1',
    'o10',
    'gpt-5',
    'gpt-5-mini',
    'gpt-5.1',
    'gpt-50',
    'GPT-5',
    ' openai/o3 ',
    'ft:gpt-5:org:x:1',
    'ft:o1-mini:org',
    'ft:gpt-4o:org',
    'gpt-4o',
    'omni',
    'o',
    'o0',
    '',
    'a/b/o1'
  ]
  const usages: unknown[] = [
    undefined,
    {},
    { prompt_tokens: 3, completion_tokens: 4 },
    { prompt_tokens: 3, completion_tokens: 4, total_tokens: 99 },
    { input_tokens: 5, output_tokens: 6 },
    { prompt_tokens: 0, input_tokens: 9 },
    { prompt_tokens: null, input_tokens: 9, total_tokens: null }
  ]
  return {
    configs: cfgs.map(([name, cfg]) => ({
      name,
      cfg,
      transportOf: t.transportOf(cfg),
      baseUrl: t.baseUrl(cfg),
      authHeaders: redactHeaders(t.authHeaders(cfg)),
      missingCredential: t.missingCredential(cfg)
    })),
    isLocal: urls.map((input) => ({ input, out: t.isLocal(input) })),
    usesMaxCompletionTokens: models.map((input) => ({ input, out: t.usesMaxCompletionTokens(input) })),
    parseUsage: usages.map((input) => ({ input: norm(input), out: t.parseUsage(input as never) })),
    fetchFailureReason: [
      { code: 'SELF_SIGNED_CERT_IN_CHAIN', message: 'm' },
      { code: 'UNABLE_TO_VERIFY_LEAF_SIGNATURE', message: 'm' },
      { code: 'ECONNREFUSED', message: 'm' },
      { code: 'ENOTFOUND', message: 'm' },
      { code: 'ETIMEDOUT', message: 'connect ETIMEDOUT' },
      { message: 'only message' },
      { message: '' }
    ].map((cause) => {
      const err = new TypeError('fetch failed') as TypeError & { cause?: unknown }
      err.cause = Object.assign(new Error(cause.message), cause.code ? { code: cause.code } : {})
      return { cause, out: t.fetchFailureReason(err) }
    })
  }
}

function cliVectors(): unknown {
  const conversations: Array<Array<{ role: 'system' | 'user' | 'assistant'; content: string }>> = [
    [
      { role: 'system', content: 'Be terse.' },
      { role: 'user', content: 'Why?' },
      { role: 'assistant', content: 'Because.' },
      { role: 'user', content: 'Expand.' }
    ],
    [],
    [{ role: 'system', content: 'a' }, { role: 'system', content: 'b' }],
    [{ role: 'user', content: '' }, { role: 'user', content: 'x' }],
    [{ role: 'assistant', content: 'only' }, { role: 'system', content: 'late system' }]
  ]
  return {
    binaries: cli.CLI_BINARIES,
    specs: (['claude', 'gemini', 'codex', 'unknown'] as const).map((binary) => {
      const spec = cli.cliSpec(binary as never)
      return {
        binary,
        bin: spec.bin,
        label: spec.label,
        stdin: spec.stdin,
        models: cli.cliModels(binary as never),
        args: { '': spec.args(''), sonnet: spec.args('sonnet') }
      }
    }),
    flattenMessages: conversations.map((messages) => ({ messages, out: cli.flattenMessages(messages) }))
  }
}

function modelNameVectors(): unknown {
  const idLists: Array<Array<string | undefined>> = [
    ['gpt-4o-mini', 'text-embedding-3-small', 'whisper-1', 'dall-e-3', 'omni-moderation-latest', 'gpt-4o'],
    ['models/gemini-2.5-flash', 'gemini-2.5-flash', ' ', undefined],
    ['gpt-4o', 'babbage-002', 'davinci-002', 'gpt-3.5-turbo-instruct', 'gpt-3.5-turbo'],
    ['gpt-4o', 'nova-adaptive'],
    ['my-embed-model'],
    ['Zeta', 'alpha', 'Alpha', 'beta-2', 'beta_1', 'beta.3', 'beta-10', 'b', 'B', '_x', '-y', '1abc', 'éclair', 'eclair', 'gpt-4o:latest'],
    ['x-2024-01-01', 'x-2023-01-01', 'y-20250101', 'y-0125', 'z-0125', 'z', 'mixtral-8x7b-32768', 'gpt-3.5-turbo-16k'],
    ['ft:davinci-002:org', 'openai/ada-v2', 'curie', 'x-instruct-v2', 'realtime-preview', 'GUARD', 'rerank-3', 'sora-2', 'codestral-embed']
  ]
  const entryLists: Array<Array<{ id: string; created?: number }>> = [
    [
      'gpt-4o',
      'gpt-4o-2024-05-13',
      'gpt-4o-2024-08-06',
      'gpt-4o-2024-11-20',
      'gpt-4o-mini',
      'gpt-4o-mini-2024-07-18',
      'gpt-3.5-turbo',
      'gpt-3.5-turbo-0125'
    ].map((id) => ({ id })),
    [
      { id: 'claude-haiku-4-5-20251001', created: 3 },
      { id: 'claude-haiku-4-5-20250101', created: 1 },
      { id: 'claude-opus-5-20260401', created: 4 }
    ],
    ['mixtral-8x7b-32768', 'gpt-3.5-turbo-16k', 'llama-3.1-8b-instant'].map((id) => ({ id })),
    [{ id: 'old-model', created: 1_600_000_000_000 }, { id: 'new-model', created: 1_700_000_000_000 }, { id: 'undated-b' }, { id: 'undated-a' }],
    [
      { id: 'm-20240101' },
      { id: 'm-20230101', created: 5 },
      { id: 'n-20240101' },
      { id: 'n-20230101' },
      { id: ' models/m-20220101 ', created: 9 },
      { id: 'tie-a', created: 7 },
      { id: 'Tie-b', created: 7 },
      { id: 'tie-a', created: 1 }
    ]
  ]
  return {
    normalizeModelIds: idLists.map((ids) => ({ input: ids.map((x) => x ?? null), out: names.normalizeModelIds(ids) })),
    chatModelsOnly: idLists.map((ids) => {
      const clean = ids.filter((x): x is string => typeof x === 'string')
      return { input: clean, out: names.chatModelsOnly(clean) }
    }),
    buildModelLists: entryLists.map((entries) => ({ input: entries, out: names.buildModelLists(entries) })),
    baseModelName: ['gpt-4o-2024-05-13', 'x-20250101', 'y-0125', 'z-125', 'w-2024-5-13', 'plain'].map((id) => ({
      input: id,
      out: names.baseModelName(id),
      snapshot: names.isSnapshot(id)
    }))
  }
}

function jsonVectors(): unknown {
  const loose: string[] = [
    '```json\n{"a":1}\n```',
    'Sure! Here is the answer.',
    '{"summary":"x","sections":[{"heading":"Core","claims":[{"text":"one","sourcePaths":["README.md"]},{"{"text":"two","sourcePaths":["README.md"]}"]}],"related":[]}',
    '{\\"value\\":\\"x\\"}',
    '{\\"summary\\":\\"x\\",\\"sections\\":[{\\"heading\\":\\"Core\\",\\"claims\\":[{\\"text\\":\\"one\\",\\"sourcePaths\\":[\\"README.md\\"]},{\\"{\\"text\\":\\"two\\",\\"sourcePaths\\":[\\"README.md\\"]}\\"]}],\\"related\\":[]}',
    '{"summary":"x","sections":[{"heading":"CI","claims":[{"text":"one","sourcePaths":["src/main/localCi.ts"]}]],"related":[]}',
    '{"summary":"x","sections":[{"heading":"i18n","claims":[{"text":"one","sourcePaths":["a.ts"]},{"{"text":"two","sourcePaths":["b.ts"]}]]],"related":[]}',
    '{"summary":"x","sections":[{"heading":"CLI","claims":":[{"text":"one","sourcePaths":["cli.ts"]}]}],"related":[]}',
    '{"summary":"x","heading":"First","claims":[{"text":"one","sourcePaths":["a.ts"]}]},{"heading":"Second","claims":[{"text":"two","sourcePaths":["b.ts"]}]}],"related":["overview"]}',
    '{"summary":"x","sections":[{"heading":"Security","claims":[{"text":"one","","sourcePaths":["keychain.ts"]}]}],"related":[]}',
    '{"summary":"x","sections":[{"heading":"Renderer","claims":[{"text":"one","sourcePaths":["App.tsx"]}]},{"related":["overview","cli"]}',
    '{"value":"first"}\nWait, here is the corrected object.\n{"value":"second"}\n{"value":"truncated"',
    '{"items":[{"value":"one"},{"{"value":"two"}"]}\nCorrected:\n{"items":[{"value":"three"}]}',
    '{"pages":[{"\n  "slug":"overview","title":"Overview"}]}',
    '{"value":"{\\"nested\\":true}"}',
    '<think>We need produce one object.</think>\n```json\n{"value":"x"}\n```',
    '<THINK>a</Think>{"v":1}',
    '<think>unterminated {"v":0}',
    'prefix </think> {"v":2}',
    '<think>a</think><think>b</think>{"v":3}<think>tail',
    '{"a": 1,}',
    "{'a': 'single', b: True, c: None, d: undefined}",
    '[1, 2, 3, ...]',
    '{"a": "hello" + " world"}',
    '{"text": "truncated',
    '{"a": [1, 2',
    '// comment\n{"a": /* inline */ 1}',
    '{"a": NumberLong("2"), "u": "\\u00e9 \\ud83d\\ude00"}',
    '{"a": 1}\n{"a": 2}',
    '{"url": https://example.com/x?y=1}',
    '{"q": “curly”, ‘k’: 1}',
    '{"a":"b,c,"d":"e"}',
    '{"x": 00123, "y": -.5, "z": 2., "w": 1e}',
    '{"s": "tab\there\nnew"}',
    '{&quot;a&quot;: &quot;b&quot;}',
    'null',
    '42',
    '"just a string"',
    '[]',
    '   ',
    '',
    '{"a":1} trailing words',
    '{"a":"\u2028 sep"}',
    '```\n[{"a":1},{"b":2}]\n```\nThanks!',
    '{"emoji":"🎉","nested":{"arr":[true,false,null,1.25e3,-0]}}',
    '{"dup":1,"dup":2,"other":3}',
    '{"__proto__":{"x":1},"y":2}',
    '{"big": 12345678901234567890, "small": 1e-7, "neg": -1.5E+3}'
  ]
  const repairs: string[] = [
    ...loose,
    '{a:1}',
    '[1 2 3]',
    '{"a" 1}',
    '{"a":}',
    '{,"a":1}',
    '[,1]',
    '"abc',
    'callback({"a":1});',
    '/regex/',
    '{"a":"x\\qy"}',
    '{"a":"\\u12"}',
    '{"a":"\\u12zz"}',
    '{"a":"\u0001"}',
    '{"a": "b" "c": 1}',
    '{"a":"The TV is 72""}',
    '{"a":"a (b") c"}',
    '}',
    '{"a":1}}]',
    '\\"a\\"',
    '[```{"a":1}```]',
    '{"key": value with spaces, "b": 2}',
    '{"a": True, "b": False}',
    '{"a": +1}',
    '\u00a0{"a":\u3000 1}',
    '[1,2,3,...,9]',
    '{"a":"&#x27;q&#39; &lt;b&gt;"}',
    '&quot;x&quot;'
  ]
  const repaired = repairs.map((input) => {
    try {
      return { input, out: jsonrepair(input) }
    } catch (err) {
      return { input, error: (err as Error).message }
    }
  })
  return {
    parseLooseJson: loose.map((input) => {
      const v = grounding.parseLooseJson<unknown>(input)
      // A raw `JSON.stringify` keeps key order, which is what a pass-through
      // result would send on to the renderer.
      return { input, out: v === null ? null : norm(v), json: v === null ? null : JSON.stringify(v) }
    }),
    jsonrepair: repaired
  }
}

function groundingVectors(): unknown {
  const g = grounding
  const diffs = [
    DIFF,
    '',
    'Binary files a/x.png and b/x.png differ',
    'diff --git a/a b/a\n@@ -1 +1 @@\n-x\n+y',
    'diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -5,0 +6,3 @@\n+1\n+2\n+3\n\\ No newline at end of file',
    '--- a/z\n@@ -1,2 +1,2 @@\n a\n-b\n+c',
    'diff --git a/p q b/p q\n@@ -10,4 +10,0 @@\n-a\n-b\n x'
  ]
  const big = Array.from({ length: 50 }, (_, i) => `diff --git a/f${i} b/f${i}\n@@ -${i},2 +${i},2 @@\n-${'o'.repeat(i * 30)}\n+${'n'.repeat(i * 30)}`).join('\n')
  const evidence = [
    ...diffs.map((diff) => ({ diff, opts: {} })),
    { diff: big, opts: {} },
    { diff: big, opts: { maxBytes: 3000, maxHunks: 5, maxHunkBytes: 100 } }
  ].map(({ diff, opts }) => {
    const set = g.buildDiffEvidence(diff, opts)
    return { diff, opts, set, serialized: g.serializeEvidence(set) }
  })
  const allowed = ['E1', 'E2', 'E3']
  const reviewInputs: unknown[] = [
    ...ANY_VALUES,
    { summary: 's', findings: [] },
    { summary: ' ', findings: 'x' },
    { summary: 's', findings: [null, 1, { evidenceId: 'E9', kind: 'bug', severity: 'odd', claim: '' }, { evidenceId: 'E1', kind: 'risk', severity: 'high', claim: 'c' }] },
    { findings: [{}] }
  ]
  const set = g.buildDiffEvidence(DIFF)
  const index = g.evidenceIndex(set)
  const rawFindings = [
    { kind: 'risk', severity: 'high', evidenceId: 'E1', claim: ' c1 ', suggestion: ' fix ' },
    { kind: 'suggestion', severity: 'low', evidenceId: 'E2', claim: 'c2', suggestion: '' },
    { kind: 'suggestion', severity: 'x', evidenceId: 'E3', claim: 'c3', suggestion: 's3' },
    { kind: 'other', severity: 'medium', evidenceId: 'E4', claim: 'c4' },
    { kind: 'risk', evidenceId: 'E1', claim: 5, suggestion: 7 },
    { evidenceId: 3 }
  ]
  const grounded = g.groundFindings(rawFindings, index)
  const windows = [
    { lines: HOVER_LINES, line: 45, opts: {} },
    { lines: HOVER_LINES, line: 1000, opts: {} },
    { lines: HOVER_LINES, line: 45, opts: { radius: 3 } },
    { lines: HOVER_LINES, line: 45, opts: { maxBytes: 120 } },
    { lines: HOVER_LINES, line: 12, opts: { maxBytes: 10 } },
    { lines: [], line: 3, opts: {} },
    { lines: [{ no: 3, text: 'a' }, { no: 9, text: 'b' }, { no: 10, text: 'c' }], line: 6, opts: {} },
    { lines: [{ no: 3, text: '😀'.repeat(20) }, { no: 4, text: 'b' }], line: 4, opts: { maxBytes: 20 } }
  ].map((w) => ({ ...w, out: g.buildWindowFromLines(w.lines, w.line, w.opts) }))
  const win = g.buildWindowFromLines(HOVER_LINES, 45)
  const hoverInputs: unknown[] = [
    ...ANY_VALUES,
    { summary: 's', bullets: ['a'], lines: [45] },
    { summary: '', bullets: 'x', lines: 'y' },
    { summary: 's', bullets: [1, 2, 3, 4], lines: [1, 45.5, '45', null, 46] }
  ]
  const known = ['a.ts', 'README.md']
  const planInputs: unknown[] = [
    ...ANY_VALUES,
    { summary: 's', actions: [] },
    { summary: 3, actions: 'x' },
    {
      summary: 's',
      actions: [
        null,
        { type: 'push', description: 'd' },
        { type: 'merge', description: 'd' },
        { type: 'force', description: 'd' },
        { description: 'd' },
        { type: 'commit', description: ' ', message: '' },
        { type: 'branch', description: 'd' },
        { type: 'tag', description: 'd', name: ' ' },
        { type: 'checkout', description: 'd' },
        { type: 'gitignore', description: 'd', patterns: [] },
        { type: 'gitignore', description: 'd', patterns: ['*.log'], files: ['ghost'] },
        { type: 'stage', description: 'd', files: 'a.ts' },
        { type: 'stage', description: 'd', files: ['a.ts', 'ghost', 3] },
        { type: 'stash', description: 'd' }
      ]
    }
  ]
  const actionLists: Array<[unknown, { history?: boolean; remote?: boolean }]> = [
    [
      [
        { type: 'merge', description: 'd' },
        { type: 'rebase', description: 'd', onto: ' ' },
        { type: 'revert', description: 'd', hashes: [] },
        { type: 'cherry_pick', description: 'd', hashes: ['abc1234', 'zz'] },
        { type: 'pull', description: 'd', mode: 'squash' },
        { type: 'open_pr', description: 'd' },
        { type: 'push', description: 'd' }
      ],
      { history: true, remote: true }
    ],
    [[{ type: 'push', description: 'd' }], { history: true }],
    [[{ type: 'pull', description: 'd', mode: 'rebase' }, { type: 'fetch', description: 'd' }], { remote: true }],
    ['nope', {}]
  ]
  return {
    evidence,
    validateReview: reviewInputs.map((input) => ({ input: norm(input), allowed, out: g.validateReview(norm(input), new Set(allowed)) })),
    groundFindings: { diff: DIFF, raw: rawFindings, out: grounded },
    renderFindings: { risk: g.renderFindings(grounded, 'risk'), suggestion: g.renderFindings(grounded, 'suggestion') },
    windows,
    validateHoverExplain: hoverInputs.map((input) => ({ input: norm(input), out: g.validateHoverExplain(norm(input), win) })),
    hoverWindow: win,
    validateAskPlan: planInputs.map((input) => ({ input: norm(input), known, out: g.validateAskPlan(norm(input), new Set(known)) })),
    validateAskActions: actionLists.map(([input, allow]) => ({
      input,
      known,
      allow,
      out: g.validateAskActions(input, new Set(known), allow)
    }))
  }
}

// ─── The capture ────────────────────────────────────────────────────────────

describe('ai port vectors', () => {
  it('pure: aiSchemas', () => {
    check('testdata/vectors/aiSchemas.json', schemaVectors())
  })

  it('pure: aiTransport', () => {
    check('testdata/vectors/aiTransport.json', transportVectors())
  })

  it('pure: aiCli', () => {
    check('testdata/vectors/aiCli.json', cliVectors())
  })

  it('pure: aiModelNames', () => {
    check('testdata/vectors/aiModelNames.json', modelNameVectors())
  })

  it('pure: loose JSON and jsonrepair', () => {
    check('testdata/vectors/aiJson.json', jsonVectors())
  })

  it('pure: the grounding helpers the AI features call', () => {
    check('testdata/vectors/aiGrounding.json', groundingVectors())
  })

  it('io: every ai:* channel request body and result', async () => {
    const out: unknown[] = []
    for (const c of featureCases()) out.push(await runFeatureCase(c))
    check('internal/ai/testdata/features.json', out)
  }, 120_000)

  it('io: direct transport calls', async () => {
    const out: unknown[] = []
    for (const c of transportCases()) out.push(await runTransportCase(c))
    check('internal/ai/testdata/transport.json', out)
  })

  it('io: the model catalogue and its cache', async () => {
    const out: unknown[] = []
    for (const s of modelScenarios()) out.push(await runModelScenario(s))
    check('internal/ai/testdata/models.json', out)
  })

  it('io: agent CLI detection', async () => {
    const out: unknown[] = []
    for (const s of DETECT_SCENARIOS) out.push(await runDetect(s))
    check('internal/ai/testdata/detect.json', out)
  })
})
