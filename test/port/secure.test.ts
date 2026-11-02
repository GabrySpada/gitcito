import * as nodeCrypto from 'node:crypto'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  truncateSync,
  writeFileSync
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, describe, expect, it, vi } from 'vitest'

/**
 * Port vectors for the `secure` group: src/main/secureBundle.ts (the .gitcito
 * format), src/main/secureShare.ts (candidate walk, renderer-safe shapes),
 * src/main/vault.ts, src/main/analytics.ts and src/main/info.ts.
 *
 * Crypto is the core of it: v4 (Electron) and v5 (Wails) users exchange
 * bundles, so the TS packs fixed plaintexts under fixed salts/IVs/clocks and the
 * Go port must decrypt every one of them and re-encrypt byte for byte. Crafted
 * envelopes pin the error mapping (invalid / bad-password / unsupported-version
 * / read-failed). The stores are driven through their real ipcMain handlers
 * against a temp userData dir, a fixed clock and a counting UUID source.
 *
 * PORT_WRITE_VECTORS=1 regenerates the files; otherwise this test recomputes
 * everything and fails when the TS output drifts from them.
 */

// dayKey() buckets analytics by local day; pin a zone with DST so the Go
// replay proves it uses the same one (it loads Europe/Rome explicitly).
process.env.TZ = 'Europe/Rome'

const h = vi.hoisted(() => ({
  handlers: new Map<string, (...a: unknown[]) => unknown>(),
  userData: '',
  now: 0,
  uuid: 0,
  available: true,
  queue: [] as Buffer[]
}))

vi.mock('node:crypto', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:crypto')>()
  const randomBytes = (n: number): Buffer => {
    const next = h.queue.shift()
    if (next) {
      if (next.length !== n) throw new Error(`queued ${next.length} bytes, asked ${n}`)
      return next
    }
    return actual.randomBytes(n)
  }
  const randomUUID = (): string => `00000000-0000-4000-8000-${String(++h.uuid).padStart(12, '0')}`
  return { ...actual, default: { ...actual, randomBytes, randomUUID }, randomBytes, randomUUID }
})

vi.mock('electron', () => ({
  ipcMain: { handle: (channel: string, fn: (...a: unknown[]) => unknown) => h.handlers.set(channel, fn) },
  app: { getPath: () => h.userData, isPackaged: false },
  dialog: {
    showOpenDialog: () => Promise.reject(new Error('dialog stubbed')),
    showSaveDialog: () => Promise.reject(new Error('dialog stubbed'))
  },
  // Identity "encryption": the vault file format changes in Go (keychain
  // master key), only the store's behaviour is captured.
  safeStorage: {
    isEncryptionAvailable: () => true,
    encryptString: (s: string) => Buffer.from('ENC:' + s, 'utf-8'),
    decryptString: (b: Buffer) => {
      const s = b.toString('utf-8')
      if (!s.startsWith('ENC:')) throw new Error('bad')
      return s.slice(4)
    }
  },
  BrowserWindow: { getFocusedWindow: () => null, getAllWindows: () => [] }
}))

vi.mock('../../src/main/keychain', () => ({
  adoptExistingConsent: async () => {},
  ensureKeychain: async () => h.available,
  canUseKeychain: async () => h.available
}))

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const WRITE = process.env.PORT_WRITE_VECTORS === '1'

function check(rel: string, data: unknown): void {
  const file = join(ROOT, rel)
  const text = JSON.stringify(data, null, 2) + '\n'
  if (WRITE) {
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, text)
    return
  }
  expect(existsSync(file), `${rel} missing — run with PORT_WRITE_VECTORS=1`).toBe(true)
  // Byte comparison: bundle texts inside must match exactly, not just parse equal.
  expect(readFileSync(file, 'utf-8')).toBe(text)
}

const tmp: string[] = []
afterAll(() => {
  for (const d of tmp) rmSync(d, { recursive: true, force: true })
})
const tempDir = (prefix: string): string => {
  const d = mkdtempSync(join(tmpdir(), prefix))
  tmp.push(d)
  return d
}

/** Fixed bytes: a recognisable ramp starting at `seed`. */
const ramp = (n: number, seed: number): Buffer => Buffer.from(Array.from({ length: n }, (_, i) => (seed + i * 7) & 0xff))

const B = (s: string): Buffer => Buffer.from(s, 'utf-8')

// ─── secure bundle ─────────────────────────────────────────────────────────

import {
  packBundle,
  packSections,
  unpackBundle,
  unpackSections,
  readBundleHeader,
  isSafeRelPath,
  BundleError,
  type BundleFile,
  type BundleSection
} from '../../src/main/secureBundle'
import { isSecretFile } from '../../src/shared/secretFiles'
import { listCandidates, openBundleV2 } from '../../src/main/secureShare'

/** encodeSection's shape (content base64): the vector files hold sections like this. */
function encSection(s: BundleSection): unknown {
  if (s.kind === 'repo') {
    return {
      ...s,
      files: s.files.map((f) => ({ path: f.path, content: f.content.toString('base64'), ...(f.executable ? { executable: true } : {}) }))
    }
  }
  return s
}
const encFiles = (files: BundleFile[]): unknown =>
  files.map((f) => ({ path: f.path, content: f.content.toString('base64'), ...(f.executable ? { executable: true } : {}) }))

function codeOf(e: unknown): string {
  return e instanceof BundleError ? e.code : 'read-failed'
}

interface PackCase {
  name: string
  project: string
  password: string
  sections?: BundleSection[]
  files?: BundleFile[] // packBundle instead of packSections
  createdAt: number
}

const PACK_CASES: PackCase[] = [
  {
    name: 'v2-single-repo',
    project: 'myproject',
    password: 'correct horse battery',
    createdAt: 1767225600000,
    files: [
      { path: '.env', content: B('API_KEY=sk-live-abc123\nDB=postgres://u:p@h/db\n') },
      { path: 'app/.env.prod', content: B('NODE_ENV=production\n') },
      { path: 'certs/dev.pem', content: Buffer.from([0, 1, 2, 250, 251, 252]) },
      { path: 'scripts/run.sh', content: B('#!/bin/sh\necho hi\n'), executable: true }
    ]
  },
  {
    name: 'v2-multi-section',
    project: 'Workspace',
    password: 'a good long password',
    createdAt: 1767225600001,
    sections: [
      {
        kind: 'repo',
        project: 'api',
        folder: 'api',
        remote: 'git@github.com:acme/api.git',
        files: [{ path: '.env', content: B('API_KEY=sk-1\n') }]
      },
      {
        kind: 'repo',
        project: 'web',
        folder: 'web-frontend',
        files: [{ path: '.env.local', content: B('NEXT_PUBLIC=1\n'), executable: false }]
      },
      {
        kind: 'vault',
        entries: [
          { key: 'STRIPE', value: 'sk_live_x', note: 'prod' },
          { key: 'SENTRY', value: 'https://dsn' }
        ]
      }
    ]
  },
  {
    name: 'v3-workspace-notes',
    project: 'Team',
    password: 'correct horse battery',
    createdAt: 1767225600002,
    sections: [
      {
        kind: 'workspace',
        name: 'Team',
        tabs: [
          { kind: 'repo', repo: { name: 'api', folder: 'api', remote: 'git@x.test:acme/api.git' } },
          { kind: 'group', name: 'Front', color: '#f00', repos: [{ name: 'web', folder: 'web' }] }
        ]
      },
      {
        kind: 'notes',
        folder: 'api',
        remote: 'https://x.test/acme/api.git',
        ref: 'refs/notes/commits',
        notes: [
          { sha: 'a'.repeat(40), body: 'ship it' },
          { sha: 'b'.repeat(40), body: 'line one\n\nline three' }
        ]
      }
    ]
  },
  {
    name: 'v2-vault-only',
    project: 'p',
    password: 'correct horse battery',
    createdAt: 1767225600003,
    sections: [{ kind: 'vault', entries: [{ key: 'K', value: 'v' }] }]
  },
  {
    name: 'empty-sections-empty-password',
    project: '',
    password: '',
    createdAt: 0,
    sections: []
  },
  {
    name: 'escapes-and-unicode',
    // Everything JSON.stringify treats specially, plus what Go's encoder would
    // escape and JS does not (<, >, &, U+2028, U+2029).
    project: 'Proj <x> & "y" \u2028\u2029 \u0007\u001f\u007f é 😀 \\ / \t\n\r\b\f',
    password: 'pässwörd 🔑 \u2028',
    createdAt: 1767225600123,
    sections: [
      {
        kind: 'repo',
        project: 'ñandú <&>',
        folder: 'dir with spaces',
        remote: 'https://h.test/a?b=<c>&d',
        files: [
          { path: 'dir/ñ.env', content: B('KEY="välue" # 😀\n'), executable: true },
          { path: 'empty', content: Buffer.alloc(0) },
          { path: 'bin.dat', content: ramp(300, 3) }
        ]
      },
      {
        kind: 'vault',
        entries: [
          { key: 'TAB\tKEY', value: 'line1\nline2\u2028', note: 'n<o>te & "q"' },
          { key: 'EMPTY_NOTE', value: '', note: '' }
        ]
      },
      {
        kind: 'workspace',
        name: 'W & <W> 🧪',
        tabs: [
          {
            kind: 'repo',
            repo: { name: 'r', folder: 'f', remote: 'u' },
            // Not part of the type, but the renderer's tab objects pass through
            // verbatim — pin JS number formatting and key order.
            extra: {
              big: 1e21,
              small: 1.5e-7,
              tiny: 5e-7,
              frac: 0.1,
              negZero: -0,
              huge: 123456789012345680000,
              neg: -42.5,
              int: 9007199254740993,
              nested: { z: null, a: true, m: false, empty: {}, list: [] }
            }
          } as never,
          { kind: 'group', name: 'G', repos: [] }
        ]
      },
      {
        kind: 'notes',
        folder: 'notes-folder',
        ref: 'refs/notes/commits',
        notes: [{ sha: 'c'.repeat(40), body: 'émoji 😀\ttab "quoted" <html>\u2029' }]
      }
    ]
  }
]

/** Seal an arbitrary plaintext under a hand-built envelope (crafted vectors). */
function craft(opts: {
  envelope?: Record<string, unknown>
  plaintext: string
  password: string
  salt?: Buffer
  iv?: Buffer
  tagLength?: number
  mutate?: (raw: Record<string, unknown>) => void
}): string {
  const salt = opts.salt ?? ramp(16, 101)
  const iv = opts.iv ?? ramp(12, 202)
  const key = nodeCrypto.scryptSync(opts.password, salt, 32, { N: 32768, r: 8, p: 1, maxmem: 64 * 1024 * 1024 })
  const cipher = nodeCrypto.createCipheriv('aes-256-gcm', key, iv, opts.tagLength ? { authTagLength: opts.tagLength } : {})
  const enc = Buffer.concat([cipher.update(B(opts.plaintext)), cipher.final()])
  const raw: Record<string, unknown> = {
    format: 'gitcito-secure-bundle',
    version: 2,
    project: 'crafted',
    createdAt: 1,
    fileCount: 0,
    ...opts.envelope,
    kdf: { algo: 'scrypt', N: 32768, r: 8, p: 1, salt: salt.toString('base64') },
    cipher: { algo: 'aes-256-gcm', iv: iv.toString('base64'), tag: cipher.getAuthTag().toString('base64') },
    payload: enc.toString('base64')
  }
  opts.mutate?.(raw)
  return JSON.stringify(raw, null, 2)
}

const PW = 'pw pw pw pw'

interface UnpackCase {
  name: string
  text: string
  password: string
}

function unpackCases(valid: Map<string, string>): UnpackCase[] {
  const single = valid.get('v2-single-repo')!
  const tampered = (): string => {
    const raw = JSON.parse(single)
    const bytes = Buffer.from(raw.payload, 'base64')
    bytes[0] ^= 0xff
    raw.payload = bytes.toString('base64')
    return JSON.stringify(raw)
  }
  const badTag = (): string => {
    const raw = JSON.parse(single)
    const bytes = Buffer.from(raw.cipher.tag, 'base64')
    bytes[15] ^= 0x01
    raw.cipher.tag = bytes.toString('base64')
    return JSON.stringify(raw)
  }
  const v1Files = JSON.stringify({
    files: [
      { path: '.env', content: B('X=1\n').toString('base64') },
      { path: 'run.sh', content: B('#!/bin/sh\n').toString('base64'), executable: 1 }
    ]
  })
  return [
    { name: 'wrong-password', text: single, password: 'wrong password' },
    { name: 'tampered-payload', text: tampered(), password: 'correct horse battery' },
    { name: 'tampered-tag', text: badTag(), password: 'correct horse battery' },
    { name: 'not-json', text: 'not json at all', password: PW },
    { name: 'json-null', text: 'null', password: PW },
    { name: 'json-number', text: '5', password: PW },
    { name: 'json-array', text: '[]', password: PW },
    { name: 'empty-object', text: '{}', password: PW },
    { name: 'other-format', text: '{"format":"something-else"}', password: PW },
    { name: 'future-version', text: JSON.stringify({ format: 'gitcito-secure-bundle', version: 99 }), password: PW },
    { name: 'version-missing', text: JSON.stringify({ format: 'gitcito-secure-bundle' }), password: PW },
    { name: 'version-string', text: JSON.stringify({ format: 'gitcito-secure-bundle', version: '2' }), password: PW },
    { name: 'no-kdf', text: JSON.stringify({ format: 'gitcito-secure-bundle', version: 2, project: 'x' }), password: PW },
    {
      name: 'v1-legacy',
      text: craft({
        envelope: { version: 1, project: 'legacy', fileCount: 2 },
        plaintext: v1Files,
        password: PW
      }),
      password: PW
    },
    {
      name: 'v1-legacy-no-sections-header-fields',
      text: craft({
        envelope: { version: 1, project: 42, createdAt: 'x', fileCount: null },
        plaintext: v1Files,
        password: PW
      }),
      password: PW
    },
    {
      name: 'version-zero-is-v1',
      text: craft({ envelope: { version: 0 }, plaintext: v1Files, password: PW }),
      password: PW
    },
    {
      name: 'version-fraction',
      text: craft({ envelope: { version: 1.5 }, plaintext: v1Files, password: PW }),
      password: PW
    },
    {
      name: 'v1-files-not-array',
      text: craft({ envelope: { version: 1 }, plaintext: '{"files":"nope"}', password: PW }),
      password: PW
    },
    {
      name: 'v1-plaintext-null',
      text: craft({ envelope: { version: 1 }, plaintext: 'null', password: PW }),
      password: PW
    },
    {
      name: 'plaintext-not-json',
      text: craft({ plaintext: 'definitely { not json', password: PW }),
      password: PW
    },
    {
      name: 'v2-sections-not-array',
      text: craft({ plaintext: '{"sections":5}', password: PW }),
      password: PW
    },
    {
      name: 'v2-plaintext-null',
      text: craft({ plaintext: 'null', password: PW }),
      password: PW
    },
    {
      name: 'v2-plaintext-string',
      text: craft({ plaintext: '"abc"', password: PW }),
      password: PW
    },
    {
      name: 'v2-null-section',
      text: craft({ plaintext: '{"sections":[null]}', password: PW }),
      password: PW
    },
    {
      name: 'v2-lenient-sections',
      text: craft({
        envelope: {
          version: 3,
          sections: [{ kind: 'repo', zeta: 1, project: 'p', alpha: [true, null] }, 'odd', 7]
        },
        plaintext: JSON.stringify({
          sections: [
            7,
            'str',
            { kind: 'mystery', data: 1 },
            { kind: 'repo', files: [] },
            {
              kind: 'repo',
              project: 'proj',
              remote: '',
              files: [
                null,
                { path: 'a', content: 'QUJD\nRA' },
                { path: 'b', content: '-_8', executable: 'yes' },
                { path: 'c', content: 'QQ==QUJD', executable: 0 },
                { path: 'd', content: 'Q', executable: '' },
                { path: 'e', content: '***QUJD***', executable: [] },
                { path: 5, content: 'QQ==' },
                { path: 'f' }
              ]
            },
            { kind: 'repo', project: 9, folder: false, remote: 3, files: [] },
            {
              kind: 'vault',
              entries: [
                null,
                { key: 'A', value: '1', note: 'n' },
                { key: 'B', value: '2', note: '' },
                { key: 'C', value: '3', note: 7 },
                { key: 'D', value: 4 },
                { key: 5, value: 'x' }
              ]
            },
            { kind: 'vault', entries: 'nope' },
            {
              kind: 'workspace',
              tabs: [
                null,
                5,
                'tab',
                { kind: 'repo' },
                { kind: 'repo', repo: '' },
                { kind: 'repo', repo: 'r' },
                { kind: 'repo', repo: { name: 'x', folder: 'x' }, keep: 1.0, order: [3, 2, 1] },
                { kind: 'group', repos: 'no' },
                { kind: 'group', name: 'g', repos: [] },
                { kind: 'other', repos: [] }
              ]
            },
            { kind: 'workspace', name: 7, tabs: 'no' },
            {
              kind: 'notes',
              notes: [null, { sha: 'abc', body: 'b' }, { sha: 1, body: 'x' }, { sha: 'def' }]
            },
            { kind: 'notes', folder: 'f', remote: 'r', ref: 'refs/notes/other', notes: 'no' }
          ]
        }),
        password: PW
      }),
      password: PW
    },
    {
      name: 'v2-repo-files-not-array',
      text: craft({ plaintext: '{"sections":[{"kind":"repo","files":{}}]}', password: PW }),
      password: PW
    },
    {
      name: 'v2-header-without-sections',
      text: craft({
        envelope: { version: 2, project: 'np', fileCount: 3, createdAt: 12.5 },
        plaintext: '{"sections":[]}',
        password: PW
      }),
      password: PW
    },
    {
      name: 'missing-salt',
      text: craft({ plaintext: '{"sections":[]}', password: PW, mutate: (r) => ((r.kdf as Record<string, unknown>).salt = '') }),
      password: PW
    },
    {
      name: 'kdf-is-string',
      text: craft({ plaintext: '{"sections":[]}', password: PW, mutate: (r) => (r.kdf = 'scrypt') }),
      password: PW
    },
    {
      name: 'missing-payload',
      text: craft({ plaintext: '{"sections":[]}', password: PW, mutate: (r) => delete r.payload }),
      password: PW
    },
    {
      name: 'salt-not-string',
      text: craft({ plaintext: '{"sections":[]}', password: PW, mutate: (r) => ((r.kdf as Record<string, unknown>).salt = 123) }),
      password: PW
    },
    {
      name: 'iv-decodes-empty',
      text: craft({ plaintext: '{"sections":[]}', password: PW, mutate: (r) => ((r.cipher as Record<string, unknown>).iv = '!!!!') }),
      password: PW
    },
    {
      name: 'tag-bad-length',
      text: craft({ plaintext: '{"sections":[]}', password: PW, mutate: (r) => ((r.cipher as Record<string, unknown>).tag = 'AAAA') }),
      password: PW
    },
    {
      name: 'payload-not-string',
      text: craft({ plaintext: '{"sections":[]}', password: PW, mutate: (r) => (r.payload = 5) }),
      password: PW
    },
    {
      name: 'iv-16-bytes',
      text: craft({ plaintext: '{"sections":[{"kind":"vault","entries":[{"key":"K","value":"V"}]}]}', password: PW, iv: ramp(16, 9) }),
      password: PW
    },
    {
      name: 'tag-12-bytes',
      text: craft({ plaintext: '{"sections":[{"kind":"vault","entries":[{"key":"K","value":"V"}]}]}', password: PW, tagLength: 12 }),
      password: PW
    },
    {
      name: 'lenient-base64-envelope',
      text: craft({
        plaintext: '{"sections":[]}',
        password: PW,
        mutate: (r) => {
          const k = r.kdf as Record<string, string>
          const c = r.cipher as Record<string, string>
          // Line breaks, missing padding and URL-safe letters all decode in Node.
          k.salt = k.salt.replace(/=+$/, '').replace(/(.{8})/, '$1\n')
          c.iv = c.iv.replace(/\+/g, '-').replace(/\//g, '_')
          r.payload = ' ' + (r.payload as string) + ' '
        }
      }),
      password: PW
    },
    {
      name: 'bom-prefixed',
      text: '\ufeff' + craft({ plaintext: '{"sections":[]}', password: PW }),
      password: PW
    },
    {
      name: 'duplicate-keys',
      text: craft({ plaintext: '{"sections":[{"kind":"vault","entries":[]}],"sections":[{"kind":"vault","entries":[{"key":"dup","value":"last"}]}]}', password: PW })
        .replace('"project": "crafted"', '"project": "first", "project": "second"'),
      password: PW
    }
  ]
}

interface Captured {
  header: unknown
  headerError?: string
  sections?: unknown
  files?: unknown
  opened?: unknown
  error?: string
}

async function capture(text: string, password: string): Promise<Captured> {
  const out: Captured = { header: null }
  try {
    out.header = readBundleHeader(text)
  } catch (e) {
    out.headerError = codeOf(e)
  }
  try {
    out.sections = unpackSections(text, password).map(encSection)
    out.files = encFiles(unpackBundle(text, password))
  } catch (e) {
    out.error = codeOf(e)
  }
  const file = join(tempDir('gitcito-port-secure-open-'), 'b.gitcito')
  writeFileSync(file, text)
  out.opened = await openBundleV2(file, password)
  return out
}

describe('secure bundle vectors', () => {
  it('packs, unpacks and maps errors exactly as the vector file says', async () => {
    const dateSpy = vi.spyOn(Date, 'now')
    const pack: unknown[] = []
    const valid = new Map<string, string>()
    PACK_CASES.forEach((c, i) => {
      const salt = ramp(16, 11 + i)
      const iv = ramp(12, 77 + i)
      h.queue.push(salt, iv)
      dateSpy.mockReturnValue(c.createdAt)
      const bundle = c.files ? packBundle(c.project, c.files, c.password) : packSections(c.project, c.sections!, c.password)
      expect(h.queue).toHaveLength(0)
      valid.set(c.name, bundle)
      const sections: BundleSection[] = c.files
        ? [{ kind: 'repo', project: c.project, folder: c.project, files: c.files }]
        : c.sections!
      const key = nodeCrypto.scryptSync(c.password, salt, 32, { N: 32768, r: 8, p: 1, maxmem: 64 * 1024 * 1024 })
      pack.push({
        name: c.name,
        project: c.project,
        password: c.password,
        salt: salt.toString('base64'),
        iv: iv.toString('base64'),
        createdAt: c.createdAt,
        key: key.toString('hex'),
        sections: sections.map(encSection),
        bundle
      })
    })
    dateSpy.mockRestore()

    const decoded: unknown[] = []
    for (const c of PACK_CASES) decoded.push({ name: c.name, ...(await capture(valid.get(c.name)!, c.password)) })
    const crafted: unknown[] = []
    for (const c of unpackCases(valid)) crafted.push({ ...c, ...(await capture(c.text, c.password)) })

    const SAFE = [
      '.env', 'app/.env.prod', 'deep/nested/dir/file.txt', '../outside', 'a/../../outside', '/etc/passwd',
      'C:\\windows\\system32', 'c:x', 'a\\..\\b', 'a\\b', '', 'a//b', './a', 'a/.', 'a/', '..', '...', '.a./b',
      'x'.repeat(1024), 'x'.repeat(1025), 'é'.repeat(1024), '😀'.repeat(512), '😀'.repeat(513), '1:2', 'a:/b'
    ]
    const SECRET = [
      '.env', 'app/.env.prod', 'certs/dev.pem', '.env.example', 'src/index.ts', '.ENV', '.env.Local', 'x.env',
      '.env.sample', '.env.template', '.env.dist', '.env.default', '.env.defaults', 'a/.env.example.local',
      '.envrc', 'k.KEY', 'store.p12', 'a.pfx', 'release.keystore', 'x.jks', 'id_rsa', 'ssh/id_ed25519',
      'id_rsa.pub', '.npmrc', '.pgpass', 'App.mobileprovision', 'x.provisionprofile', 'AuthKey.p8', 'cert.cer',
      '.netrc', 'credentials', 'aws/credentials.json', 'credentials.yaml', 'secret.yml', 'secrets.yaml',
      'secrets.json', 'secret.toml', 'secrets.ini', 'my-secrets.json', '.env.prod.bak', '.env.with space'
    ]

    check('testdata/vectors/secure-bundle.json', {
      scrypt: { N: 32768, r: 8, p: 1, keyLen: 32 },
      pack,
      decoded,
      crafted,
      safeRelPath: SAFE.map((p) => ({ path: p, safe: isSafeRelPath(p) })),
      secretFile: SECRET.map((p) => ({ path: p, secret: isSecretFile(p) }))
    })
  }, 120_000)
})

// ─── secure share: candidate walk ──────────────────────────────────────────

describe('secure share candidates', () => {
  it('walks, filters and sorts like the vector file says', async () => {
    // Names chosen to pin localeCompare (ICU root): punctuation before digits
    // before letters, case-insensitive first, digits as characters.
    const files: { path: string; size?: number; content?: string }[] = [
      { path: '.env', content: 'A=1\n' },
      { path: 'alpha.txt', content: 'a' },
      { path: 'Beta.txt', content: 'bb' },
      { path: 'beta2.txt', content: '' },
      { path: '_under', content: 'u' },
      { path: '-dash', content: 'd' },
      { path: '10.txt', content: '10' },
      { path: '9.txt', content: '9' },
      { path: 'ébc.txt', content: 'e' },
      { path: 'z/.env.local', content: 'L=1' },
      { path: 'z/.env.example', content: 'L=' },
      { path: 'z/a b.txt', content: 'space' },
      { path: 'node_modules/x.js', content: 'x' },
      { path: 'dist/out.js', content: 'x' },
      { path: '.git/config', content: 'x' },
      { path: 'Pods/p', content: 'x' },
      { path: 'deep/er/id_rsa', content: 'k' },
      { path: 'deep/Readme.md', content: 'r' },
      { path: 'big.bin', size: 16 * 1024 * 1024 + 1 },
      { path: 'exact.bin', size: 16 * 1024 * 1024 }
    ]
    const symlinks = [
      { path: 'link.txt', target: 'alpha.txt' },
      { path: 'linkdir', target: 'z' },
      { path: 'dangling', target: 'missing' }
    ]
    const root = tempDir('gitcito-port-candidates-')
    for (const f of files) {
      const full = join(root, f.path)
      mkdirSync(dirname(full), { recursive: true })
      writeFileSync(full, f.content ?? '')
      if (f.size !== undefined) truncateSync(full, f.size)
    }
    for (const l of symlinks) symlinkSync(l.target, join(root, l.path))
    const list = await listCandidates(root)
    check('internal/secure/testdata/candidates.json', { files, symlinks, expected: list })
  })
})

// ─── stores: info, vault, analytics ────────────────────────────────────────

type Op = { op: string; args: unknown[]; now?: number; available?: boolean }

async function runOps(ops: Op[], call: (op: Op) => Promise<unknown>): Promise<unknown[]> {
  const out: unknown[] = []
  for (const op of ops) {
    if (op.now !== undefined) h.now = op.now
    if (op.available !== undefined) h.available = op.available
    const result = await call(op)
    out.push({ ...op, result: result === undefined ? null : JSON.parse(JSON.stringify(result)) })
  }
  return out
}

const BASE = 1767268800000 // 2026-01-01T12:00:00Z

describe('info store', () => {
  it('replays the scenario in the vector file', async () => {
    vi.resetModules()
    h.handlers.clear()
    h.uuid = 0
    h.userData = tempDir('gitcito-port-info-')
    const dateSpy = vi.spyOn(Date, 'now').mockImplementation(() => h.now)
    const { registerInfoHandlers } = await import('../../src/main/info')
    registerInfoHandlers()
    const e = (label: string, id = '', extra: Record<string, unknown> = {}): Record<string, unknown> => ({
      id,
      label,
      value: `${label} value`,
      field: 'custom',
      updatedAt: 0,
      ...extra
    })
    const id = (n: number): string => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
    const ops: Op[] = [
      { op: 'list', args: ['/r1'], now: BASE },
      { op: 'upsert', args: ['/r1', e('Bundle ID')], now: BASE + 1000 },
      { op: 'upsert', args: ['/r1', e('Web')], now: BASE + 2000 },
      { op: 'upsert', args: ['/r1', e('Bundle ID v2', id(1), { updatedAt: 5 })], now: BASE + 3000 },
      { op: 'upsert', args: ['/r1', e('Custom id', 'custom')], now: BASE + 4000 },
      { op: 'reorder', args: ['/r1', ['custom', 'missing', id(1), id(1)]], now: BASE + 5000 },
      { op: 'remove', args: ['/r1', id(2)], now: BASE + 6000 },
      { op: 'remove', args: ['/nope', 'x'], now: BASE + 7000 },
      { op: 'exportAll', args: [] },
      {
        op: 'importAll',
        args: [
          {
            repos: {
              '/r1': [e('Imported custom', 'custom', { updatedAt: 9 }), e('No id'), e('New one', 'new1', { updatedAt: 10 })],
              '/r2': [e('Z', 'z', { updatedAt: 11 })]
            }
          }
        ]
      },
      { op: 'importAll', args: [null] },
      { op: 'importAll', args: [{}] },
      { op: 'exportAll', args: [] },
      { op: 'list', args: ['/r2'] },
      { op: 'reorder', args: ['/r3', ['a']] },
      { op: 'list', args: ['/r3'] }
    ]
    const results = await runOps(ops, (op) => h.handlers.get(`info:${op.op}`)!({}, ...op.args) as Promise<unknown>)
    dateSpy.mockRestore()
    const file = JSON.parse(readFileSync(join(h.userData, 'gitcito-info.json'), 'utf-8'))
    check('internal/info/testdata/scenario.json', { ops: results, file })
  })
})

describe('vault store', () => {
  it('replays the scenario in the vector file', async () => {
    vi.resetModules()
    h.handlers.clear()
    h.uuid = 0
    h.available = true
    h.userData = tempDir('gitcito-port-vault-')
    const dateSpy = vi.spyOn(Date, 'now').mockImplementation(() => h.now)
    const vault = await import('../../src/main/vault')
    vault.registerVaultHandlers()
    const e = (key: string, id = '', extra: Record<string, unknown> = {}): Record<string, unknown> => ({
      id,
      key,
      value: `${key}-secret`,
      updatedAt: 0,
      ...extra
    })
    const id = (n: number): string => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
    const ops: Op[] = [
      { op: 'list', args: ['/r1'], now: BASE },
      { op: 'upsert', args: ['repo', '/r1', e('DB_URL', '', { note: 'prod db' })], now: BASE + 1000 },
      { op: 'upsert', args: ['global', '/r1', e('STRIPE')], now: BASE + 2000 },
      { op: 'upsert', args: ['global', '/r1', e('SENTRY', '', { note: '' })], now: BASE + 3000 },
      { op: 'upsert', args: ['repo', '/r1', e('DB_URL', id(1), { value: 'changed' })], now: BASE + 4000 },
      { op: 'upsert', args: ['repo', '/r2', e('FIXED', 'fixed-id')], now: BASE + 5000 },
      { op: 'upsert', args: ['anything', '/r2', e('SCOPE_FALLS_BACK_TO_REPO')], now: BASE + 6000 },
      { op: 'list', args: ['/r2'] },
      { op: 'remove', args: ['global', '/r1', id(2)], now: BASE + 7000 },
      { op: 'remove', args: ['repo', '/r9', 'missing'], now: BASE + 8000 },
      { op: 'list', args: ['/r9'] },
      { op: 'exportAll', args: [] },
      { op: 'exportGlobalEntries', args: [] },
      {
        op: 'importGlobalEntries',
        args: [[{ key: 'SENTRY', value: 'new-dsn', note: 'from bundle' }, { key: 'NEW_KEY', value: 'v' }, { key: 'SENTRY', value: 'again' }]],
        now: BASE + 9000
      },
      { op: 'importGlobalEntries', args: [[]], now: BASE + 9500 },
      { op: 'list', args: ['/r1'] },
      {
        op: 'importAll',
        args: [
          {
            global: [e('IMPORTED', 'imp-1', { updatedAt: 3 }), e('NO_ID')],
            repos: { '/r1': [e('DB_URL', id(1), { value: 'from import', updatedAt: 4 })], '/r3': [e('R3', 'r3')] }
          }
        ]
      },
      { op: 'importAll', args: [{}] },
      { op: 'importAll', args: [null] },
      { op: 'exportAll', args: [] },
      // Keychain refused: every channel degrades, nothing is read or written.
      { op: 'list', args: ['/r1'], available: false },
      { op: 'upsert', args: ['global', '/r1', e('IGNORED')] },
      { op: 'remove', args: ['global', '/r1', 'imp-1'] },
      { op: 'exportAll', args: [] },
      { op: 'importAll', args: [{ global: [e('IGNORED', 'x')], repos: {} }] },
      { op: 'exportGlobalEntries', args: [] },
      { op: 'importGlobalEntries', args: [[{ key: 'IGNORED', value: 'x' }]] },
      { op: 'list', args: ['/r1'], available: true }
    ]
    const results = await runOps(ops, (op) => {
      if (op.op === 'exportGlobalEntries') return vault.exportGlobalEntries()
      if (op.op === 'importGlobalEntries') return vault.importGlobalEntries(op.args[0] as never)
      return h.handlers.get(`vault:${op.op}`)!({}, ...op.args) as Promise<unknown>
    })
    dateSpy.mockRestore()
    const enc = readFileSync(join(h.userData, 'gitcito-vault.enc'), 'utf-8')
    const plain = Buffer.from(enc, 'base64').toString('utf-8').slice(4)
    check('internal/vault/testdata/scenario.json', { ops: results, plain: JSON.parse(plain) })
  })
})

describe('analytics store', () => {
  const DAY = 86_400_000
  const usage = (p: number, c: number, t: number): Record<string, number> => ({
    promptTokens: p,
    completionTokens: c,
    totalTokens: t
  })

  async function scenario(seed: unknown, ops: Op[]): Promise<unknown> {
    vi.resetModules()
    h.handlers.clear()
    h.userData = tempDir('gitcito-port-analytics-')
    if (seed !== undefined) writeFileSync(join(h.userData, 'gitcito-analytics.json'), JSON.stringify(seed))
    const dateSpy = vi.spyOn(Date, 'now').mockImplementation(() => h.now)
    const analytics = await import('../../src/main/analytics')
    analytics.registerAnalyticsHandlers()
    const results = await runOps(ops, (op) => {
      if (op.op === 'recordEvent') return analytics.recordEvent(op.args[0] as never)
      if (op.op === 'recordAIUsage') return analytics.recordAIUsage(op.args[0] as string, op.args[1] as string, op.args[2] as never)
      return Promise.resolve(h.handlers.get(`analytics:${op.op}`)!({}, ...op.args))
    })
    dateSpy.mockRestore()
    return { seed: seed ?? null, ops: results }
  }

  it('replays the scenarios in the vector file', async () => {
    // 2026-03-28 23:30 local (Rome, UTC+1) — the next day starts DST.
    const T0 = Date.UTC(2026, 2, 28, 22, 30)
    const fresh = await scenario(undefined, [
      { op: 'get', args: [], now: T0 },
      { op: 'recordEvent', args: ['commit'], now: T0 },
      { op: 'recordEvent', args: ['commit'], now: T0 + 1000 },
      { op: 'recordEvent', args: ['push'], now: T0 + 2000 },
      { op: 'recordAIUsage', args: ['commitMessage', 'GPT-4o-mini-2024', usage(1200, 300, 1500)], now: T0 + 3000 },
      { op: 'recordAIUsage', args: ['commitMessage', 'gpt-4o', usage(1000, 1000, 2000)], now: T0 + 4000 },
      { op: 'recordAIUsage', args: ['reviewPR', 'claude-3-5-sonnet-latest', usage(333, 77, 410)] },
      { op: 'recordAIUsage', args: ['reviewPR', 'my-local-model', usage(10, 5, 15)] },
      { op: 'recordAIUsage', args: ['reviewPR', 'gpt-4.1-mini', usage(0, 0, 0)] },
      { op: 'recordAIUsage', args: ['reviewPR', 'gpt-4.1-mini', usage(-1, 0, 7)] },
      { op: 'get', args: [] },
      // Past local midnight: a new bucket, in date order.
      { op: 'recordEvent', args: ['pull'], now: T0 + 3600_000 },
      { op: 'recordEvent', args: ['merge'], now: T0 + 2 * DAY },
      { op: 'recordAIUsage', args: ['smartStage', 'codestral-2501', usage(1_000_000, 1_000_000, 2_000_000)] },
      { op: 'get', args: [] },
      { op: 'setRetention', args: [1] },
      { op: 'setRetention', args: [2.7] },
      { op: 'setRetention', args: [-5] },
      { op: 'setRetention', args: [null] },
      { op: 'clear', args: [] },
      { op: 'recordEvent', args: ['clone'], now: T0 + 3 * DAY },
      { op: 'get', args: [] }
    ])
    const seeded = await scenario(
      {
        since: 1000,
        retentionDays: 3,
        days: [
          { date: '2026-01-01', events: { commit: 4 }, ai: { requests: 1, promptTokens: 1, completionTokens: 1, totalTokens: 2, cost: 0.5 } },
          { date: '2026-03-25', events: { push: 1 }, ai: { requests: 0, promptTokens: 0, completionTokens: 0, totalTokens: 0, cost: 0 } },
          { date: '2026-03-27', events: {}, ai: { requests: 0, promptTokens: 0, completionTokens: 0, totalTokens: 0, cost: 0 } }
        ],
        aiTotal: { requests: 1, promptTokens: 1, completionTokens: 1, totalTokens: 2, cost: 0.5 },
        aiByModel: { 'gpt-4o': { requests: 1, promptTokens: 1, completionTokens: 1, totalTokens: 2, cost: 0.5 } }
      },
      [
        { op: 'get', args: [], now: T0 },
        { op: 'recordEvent', args: ['tagCreate'] },
        { op: 'setRetention', args: [0] },
        { op: 'clear', args: [] },
        { op: 'get', args: [] }
      ]
    )
    // `clear` before anything loaded the ledger: the retention on disk is lost.
    const clearFirst = await scenario({ since: 5, retentionDays: 9, days: [] }, [
      { op: 'clear', args: [], now: T0 },
      { op: 'get', args: [] }
    ])
    check('internal/analytics/testdata/scenario.json', { tz: process.env.TZ, scenarios: [fresh, seeded, clearFirst] })
  })
})

// ─── the other direction: TS opens what Go wrote ───────────────────────────

describe('TS opens Go-produced bundles', () => {
  // Written by internal/secure/goproduced_test.go (PORT_WRITE_VECTORS=1): a v4
  // (Electron) user must be able to import what a v5 (Wails) user exported.
  const DIR = join(ROOT, 'testdata/vectors/secure-go-produced')
  const expected = JSON.parse(readFileSync(join(DIR, 'expected.json'), 'utf-8')) as {
    file: string
    password: string
    header: unknown
    sections: unknown
  }[]

  it('has bundles to open', () => {
    expect(expected.length).toBeGreaterThanOrEqual(3)
  })

  for (const c of expected) {
    it(`reads, decrypts and opens ${c.file}`, async () => {
      const text = readFileSync(join(DIR, c.file), 'utf-8')
      expect(readBundleHeader(text)).toEqual(c.header)
      expect(unpackSections(text, c.password).map(encSection)).toEqual(c.sections)
      expect(() => unpackSections(text, c.password + 'x')).toThrowError(
        expect.objectContaining({ code: 'bad-password' })
      )
      const opened = await openBundleV2(join(DIR, c.file), c.password)
      expect('error' in opened).toBe(false)
    }, 30_000)
  }
})
