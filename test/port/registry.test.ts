// Port vectors for the registry group: src/main/repoMeta.ts, repoConfig.ts
// (with src/shared/repoConfig.ts), repoFileActions.ts (with
// src/shared/secretFiles.ts) and textconv.ts. The Go ports replay them:
//   testdata/vectors/repoMeta.json      → internal/registry
//   testdata/vectors/repoConfig.json    → internal/repoconfig
//   testdata/vectors/fileActions.json   → internal/fileactions
//   testdata/vectors/textconv.json      → internal/textconv
//   internal/textconv/testdata/*.docx|*.xlsx (+ documents.json) → internal/textconv
//
// PORT_WRITE_VECTORS=1 regenerates every file; otherwise this recomputes the
// TypeScript output and fails when it drifts from what the files say, so
// neither implementation can move without a test failing.
//
// The documents are generated once (jszip / SheetJS) and then read back from
// disk in both modes: the check run converts the committed binaries, so a
// drift in mammoth or SheetJS shows up here, not as a silent Go mismatch.
import { describe, it, expect, afterAll } from 'vitest'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import JSZip from 'jszip'
import { ownerFromRemoteUrl } from '../../src/main/repoMeta'
import {
  isRepoConfigEmpty,
  isSafeLinkUrl,
  isSafePattern,
  isSafeRepoRelPath,
  parseRepoConfig,
  satisfiesNodeRange,
  serializeRepoConfig
} from '../../src/shared/repoConfig'
import { isSecretFile } from '../../src/shared/secretFiles'
import {
  applyPreparedRepoFileActions,
  isGeneratedRepoPath,
  prepareRepoFileActions,
  RepoFileActionError
} from '../../src/main/repoFileActions'
import { convertFile, jsonToStable, stringsToText } from '../../src/main/textconv'
import type { RepoChatFileAction } from '../../src/shared/types'

const ROOT = resolve(__dirname, '../..')
const VECTORS = join(ROOT, 'testdata/vectors')
const DOCS = join(ROOT, 'internal/textconv/testdata')
const WRITE = !!process.env.PORT_WRITE_VECTORS

/** Write (in write mode) and compare against the committed file. */
function settle(file: string, value: unknown): void {
  const json = JSON.parse(JSON.stringify(value))
  if (WRITE) {
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, JSON.stringify(json, null, 2) + '\n')
  }
  expect(JSON.parse(readFileSync(file, 'utf8'))).toEqual(json)
}

const temps: string[] = []
afterAll(() => {
  for (const d of temps) rmSync(d, { recursive: true, force: true })
})

// ─── repoMeta ───────────────────────────────────────────────────────────────

const REMOTE_URLS = [
  'https://github.com/acme/widget.git',
  'git@github.com:acme/widget.git',
  'https://gitlab.com/top-solution/sub/widget.git',
  'git@gitlab.com:group/sub/repo.git',
  'https://git.example.com/top-solution/widget.git',
  'git@git.example.com:top-solution/widget.git',
  'https://git.example.com/widget.git',
  'not a url',
  '',
  'ssh://git@host.example.com:2222/group/repo.git',
  'https://user@git.example.com:8443/top-solution/widget.git',
  'https://dev.azure.com/contoso/Payments/_git/host-remotes',
  'git@ssh.dev.azure.com:v3/contoso/Payments/repo',
  'https://contoso.visualstudio.com/Payments/_git/repo',
  'https://bitbucket.org/team/repo.git',
  'file:///srv/git/repo.git',
  '/srv/git/repo.git',
  'ssh://host/a/b/c.git',
  '  git@host:ns/r.git  ',
  'https://github.com/acme',
  'git@host:',
  'https://host//a//b.git',
  'HTTPS://Host.Example.com/Org/Repo',
  'git+ssh://git@host/org/repo',
  'https://dev.azure.com/org/proj%ZZ/_git/repo',
  'user@host:2222/ns/repo.git'
]

describe('repoMeta vectors', () => {
  it('ownerFromRemoteUrl', () => {
    const cases = REMOTE_URLS.map((url) => {
      try {
        return { url, out: ownerFromRemoteUrl(url) }
      } catch {
        // parseRemoteUrl throws on a broken percent-escape; readOriginOwner
        // turns that into null, which is what the Go port returns directly.
        return { url, out: null, throws: true }
      }
    })
    settle(join(VECTORS, 'repoMeta.json'), cases)
  })
})

// ─── repoConfig ─────────────────────────────────────────────────────────────

const many = (n: number, f: (i: number) => unknown): unknown[] => Array.from({ length: n }, (_, i) => f(i))

const CONFIG_DOCS: string[] = [
  '',
  '{nope',
  '[]',
  'null',
  '"x"',
  '{}',
  '{"version":2}',
  '{"version":"1"}',
  '{"version":1}',
  '{"version":1,"protect":["main"," release/* ",""," "]}',
  '{"version":1,"unknownField":true,"other":1,"2":0,"10":1}',
  '{"b":1,"version":1,"a":2}',
  '{"version":1,"protect":"main"}',
  '{"version":1,"protect":[1,"main",null]}',
  JSON.stringify({ version: 1, protect: many(60, (i) => `b${i}`) }),
  JSON.stringify({ version: 1, protect: ['x'.repeat(201), 'ok'] }),
  JSON.stringify({
    version: 1,
    links: {
      tickets: [
        { match: 'ABC-\\d+', url: 'https://tracker.example.com/browse/{0}', label: 'Jira' },
        { match: '(?<=x)y', url: 'https://t.example.com/{0}' },
        { match: '(a)\\1', url: 'https://t.example.com/{0}' },
        { match: '(?P<x>a)', url: 'https://t.example.com/{0}' },
        { match: '(?i)abc', url: 'https://t.example.com/{0}' },
        { match: '[', url: 'https://t.example.com/{0}' },
        { match: 'ok', url: 'javascript:alert(1)' },
        { match: 'ok', url: 'https://has space.example.com' },
        { match: '', url: 'https://t.example.com' },
        'not-an-object',
        { match: 'x'.repeat(121), url: 'https://t.example.com' }
      ]
    }
  }),
  JSON.stringify({ version: 1, links: { tickets: many(12, (i) => ({ match: `T${i}-\\d+`, url: 'https://t.example.com' })) } }),
  '{"version":1,"links":[]}',
  '{"version":1,"links":{"tickets":{}}}',
  JSON.stringify({ version: 1, commit: { scopes: ['ui', 'api', ''], trailers: ['Signed-off-by'], ticketFromBranch: true } }),
  '{"version":1,"commit":{"ticketFromBranch":"yes"}}',
  '{"version":1,"commit":{"ticketFromBranch":false}}',
  '{"version":1,"commit":"x"}',
  JSON.stringify({
    version: 1,
    requires: {
      node: ' >=20 ',
      submodules: true,
      lfs: false,
      hooksPath: '.husky/_',
      files: ['.env', { path: '.env.local', from: '.env.example', why: 'dev server' }, { path: '../x' }, { path: 'a', from: '/etc/passwd' }, 3, { path: '' }]
    }
  }),
  '{"version":1,"requires":{"hooksPath":"../elsewhere"}}',
  '{"version":1,"requires":{"hooksPath":".git/hooks"}}',
  '{"version":1,"requires":{"node":7,"submodules":"y"}}',
  '{"version":1,"requires":[]}',
  JSON.stringify({ version: 1, requires: { files: many(25, (i) => `f${i}`) } }),
  JSON.stringify({ version: 1, checklist: { push: ['Run the tests', '', 'y'.repeat(201), 5] } }),
  '{"version":1,"checklist":{"push":"x"}}',
  '{"version":1,"checklist":null}',
  '{"version":1,"protect":["a"],"protect":["b"]}',
  '{"version":1.0,"protect":["\\u2028x"]}',
  '  {"version":1}  ',
  '\uFEFF{"version":1}'
]

const REL_PATHS = [
  '.env',
  'a/b/c',
  '',
  '/abs',
  '\\abs',
  '~/x',
  'C:/x',
  'c:x',
  '..',
  'a/../b',
  'a\\..\\b',
  '.git',
  'a/.git/hooks',
  '.gitignore',
  'a\0b',
  'x'.repeat(200),
  'x'.repeat(201),
  '.husky/_',
  'é'.repeat(150)
]

const NODE_RANGES: [string, string][] = [
  ['20.11.1', '20'],
  ['20.11.1', '20.x'],
  ['20.11.1', 'v20.11.1'],
  ['20.11.1', '>=20'],
  ['22.1.0', '>=20'],
  ['18.0.0', '>=20'],
  ['20.1.0', '^20.1'],
  ['21.0.0', '~20.1'],
  ['18.0.0', '^20'],
  ['20.0.0', '>20'],
  ['21.0.0', '>20'],
  ['20.0.0', '<=20'],
  ['21.0.0', '<20'],
  ['19.0.0', '<20'],
  ['x', '20'],
  ['20.0.0', 'lts/*'],
  ['20.0.0', ' 20 '],
  ['v20.0.0', '20']
]

const PATTERNS = ['ABC-\\d+', '(?<=x)y', '(?<!x)y', '(?=a)b', '(?!a)b', '(a)\\1', '(?P<x>a)', '(?i)a', '[', '(', 'a{2,3}', '\\bword\\b', '', 'x'.repeat(121), '[[:alpha:]]', '\\p{L}+']

const LINK_URLS = ['https://a.example.com', 'HTTP://A', 'ftp://x', 'https://', 'https://a b', 'javascript:x', '', 'https://' + 'x'.repeat(193)]

describe('repoConfig vectors', () => {
  it('parse, validate, serialize and the predicates', () => {
    const parse = CONFIG_DOCS.map((raw) => {
      const { config, issues } = parseRepoConfig(raw)
      return {
        raw,
        config,
        issues,
        serialized: config ? serializeRepoConfig(config) : null,
        empty: isRepoConfigEmpty(config)
      }
    })
    settle(join(VECTORS, 'repoConfig.json'), {
      parse,
      relPaths: REL_PATHS.map((p) => ({ in: p, out: isSafeRepoRelPath(p) })),
      nodeRanges: NODE_RANGES.map(([actual, spec]) => ({ actual, spec, out: satisfiesNodeRange(actual, spec) })),
      patterns: PATTERNS.map((p) => ({ in: p, out: isSafePattern(p) })),
      linkUrls: LINK_URLS.map((u) => ({ in: u, out: isSafeLinkUrl(u) }))
    })
  })
})

// ─── repoFileActions ────────────────────────────────────────────────────────

const PATH_SAMPLES = [
  'src/generated/client.ts',
  'gen/x',
  'a/gen',
  'general/x',
  'src/api.generated.ts',
  'src/API.GENERATED.d.ts',
  'Generated/x',
  'src/regen/x',
  '.env',
  '.env.local',
  '.env.example',
  'config/.env.production',
  '.envrc',
  'key.pem',
  'id_rsa',
  'id_rsa.pub',
  '.npmrc',
  'credentials',
  'credentials.json',
  'secrets.yaml',
  'secret.toml',
  'app.mobileprovision',
  'AuthKey.p8',
  'cert.cer',
  'README.md',
  'src/.netrc',
  '.pgpass',
  '.env.defaults',
  '.env.Template'
]

interface Scenario {
  name: string
  /** Files to create before preparing: path → utf-8 text, or { b64 } for bytes. */
  files: Record<string, string | { b64: string }>
  actions: unknown[]
  evidence?: string[]
  complete?: string[]
  ignored?: string[]
  /** When set, also apply the prepared batch after these edits to the tree. */
  apply?: { mutate?: Record<string, string | null> }
}

const SCENARIOS: Scenario[] = [
  {
    name: 'exact edit',
    files: { LICENSE: 'MIT License\nCopyright MyAppDesk\n' },
    actions: [{ type: 'edit_file', path: 'LICENSE', oldText: 'MIT License', newText: 'Apache License, Version 2.0', description: 'heading' }],
    evidence: ['LICENSE'],
    apply: {}
  },
  {
    name: 'ambiguous edit',
    files: { 'a.txt': 'same\nsame\n' },
    actions: [{ type: 'edit_file', path: 'a.txt', oldText: 'same', newText: 'next', description: 'Edit' }],
    evidence: ['a.txt'],
    complete: []
  },
  {
    name: 'incomplete replace',
    files: { 'a.txt': 'same\n' },
    actions: [{ type: 'write_file', path: 'a.txt', content: 'next', mode: 'replace', description: 'Replace' }],
    evidence: ['a.txt'],
    complete: []
  },
  {
    name: 'mixed batch',
    files: { 'a.txt': 'same same\n', 'gone.txt': 'gone\n' },
    actions: [
      { type: 'write_file', path: 'new.txt', content: 'created\n', mode: 'create', description: 'Create' },
      { type: 'edit_file', path: 'a.txt', oldText: 'same', newText: 'next', replaceAll: true, description: 'Replace all' },
      { type: 'edit_file', path: 'a.txt', oldText: 'next next', newText: 'done', description: 'Edit again' },
      { type: 'delete_file', path: 'gone.txt', description: 'Delete' }
    ],
    evidence: ['a.txt', 'gone.txt'],
    apply: {}
  },
  {
    name: 'nested create makes parents',
    files: {},
    actions: [{ type: 'write_file', path: 'deep/er/x.txt', content: 'x\n', mode: 'create', description: 'Create' }],
    apply: {}
  },
  {
    name: 'stale before apply',
    files: { 'a.txt': 'old\n' },
    actions: [{ type: 'edit_file', path: 'a.txt', oldText: 'old', newText: 'new', description: 'Edit' }],
    evidence: ['a.txt'],
    apply: { mutate: { 'a.txt': 'user changed it\n' } }
  },
  {
    name: 'create raced by a new file',
    files: {},
    actions: [{ type: 'write_file', path: 'n.txt', content: 'n\n', mode: 'create', description: 'Create' }],
    apply: { mutate: { 'n.txt': 'already\n' } }
  },
  {
    name: 'delete raced by a delete',
    files: { 'd.txt': 'd\n' },
    actions: [{ type: 'delete_file', path: 'd.txt', description: 'Delete' }],
    evidence: ['d.txt'],
    apply: { mutate: { 'd.txt': null } }
  },
  { name: 'git internal', files: {}, actions: [{ type: 'write_file', path: '.git/config', content: 'x', mode: 'create', description: 'c' }] },
  { name: 'nested git internal', files: {}, actions: [{ type: 'write_file', path: 'nested/.GIT/config', content: 'x', mode: 'create', description: 'c' }] },
  { name: 'secret', files: {}, actions: [{ type: 'write_file', path: '.env', content: 'x', mode: 'create', description: 'c' }] },
  { name: 'generated', files: {}, actions: [{ type: 'write_file', path: 'src/generated/client.ts', content: 'x', mode: 'create', description: 'c' }] },
  { name: 'escape', files: {}, actions: [{ type: 'write_file', path: '../outside.txt', content: 'x', mode: 'create', description: 'c' }] },
  { name: 'absolute', files: {}, actions: [{ type: 'write_file', path: '/etc/passwd', content: 'x', mode: 'create', description: 'c' }] },
  { name: 'backslashes', files: { 'w/x.txt': 'one\n' }, actions: [{ type: 'edit_file', path: 'w\\x.txt', oldText: 'one', newText: 'two', description: 'e' }], evidence: ['w/x.txt'] },
  { name: 'ignored', files: {}, actions: [{ type: 'write_file', path: 'dist/app.js', content: 'x', mode: 'create', description: 'c' }], ignored: ['dist/app.js'] },
  { name: 'binary content', files: {}, actions: [{ type: 'write_file', path: 'data.txt', content: 'a\0b', mode: 'create', description: 'c' }] },
  { name: 'binary file', files: { 'bin.dat': { b64: Buffer.from([1, 0, 2]).toString('base64') } }, actions: [{ type: 'delete_file', path: 'bin.dat', description: 'd' }], evidence: ['bin.dat'] },
  { name: 'evidence required', files: { 'a.txt': 'a\n' }, actions: [{ type: 'delete_file', path: 'a.txt', description: 'd' }] },
  { name: 'missing edit target', files: {}, actions: [{ type: 'edit_file', path: 'nope.txt', oldText: 'a', newText: 'b', description: 'e' }], evidence: ['nope.txt'] },
  { name: 'old text missing', files: { 'a.txt': 'abc\n' }, actions: [{ type: 'edit_file', path: 'a.txt', oldText: 'zzz', newText: 'b', description: 'e' }], evidence: ['a.txt'] },
  { name: 'create over existing', files: { 'a.txt': 'abc\n' }, actions: [{ type: 'write_file', path: 'a.txt', content: 'x', mode: 'create', description: 'c' }] },
  { name: 'replace missing', files: {}, actions: [{ type: 'write_file', path: 'a.txt', content: 'x', mode: 'replace', description: 'r' }], evidence: ['a.txt'], complete: ['a.txt'] },
  { name: 'delete missing', files: {}, actions: [{ type: 'delete_file', path: 'a.txt', description: 'd' }], evidence: ['a.txt'] },
  { name: 'no-op write', files: { 'a.txt': 'same' }, actions: [{ type: 'write_file', path: 'a.txt', content: 'same', mode: 'replace', description: 'r' }], evidence: ['a.txt'] },
  { name: 'unknown type', files: {}, actions: [{ type: 'rename_file', path: 'a.txt', description: 'r' }] },
  { name: 'oversized', files: {}, actions: [{ type: 'write_file', path: 'big.txt', content: 'é'.repeat(256 * 1024 + 1), mode: 'create', description: 'c' }] },
  {
    name: 'dollar patterns in the replacement',
    files: { 'a.txt': 'cost: X\n' },
    actions: [{ type: 'edit_file', path: 'a.txt', oldText: 'X', newText: "$$5 $& $` $' $1", description: 'e' }],
    evidence: ['a.txt'],
    apply: {}
  },
  {
    // `$&` / `$'` would splice the temp path into the vector; `$$` and `$\``
    // exercise the same replacement expansion deterministically.
    name: 'path with a dollar',
    files: { 'pay$$x$`.txt': 'one\n' },
    actions: [{ type: 'edit_file', path: 'pay$$x$`.txt', oldText: 'one', newText: 'two', description: 'e' }],
    evidence: ['pay$$x$`.txt'],
    apply: {}
  },
  {
    name: 'no trailing newline and unicode',
    files: { 'u.txt': 'naïve — café' },
    actions: [{ type: 'edit_file', path: 'u.txt', oldText: 'café', newText: 'thé 🍵', description: 'e' }],
    evidence: ['u.txt'],
    apply: {}
  },
  {
    name: 'not a regular file',
    files: { 'dir/inner.txt': 'x' },
    actions: [{ type: 'delete_file', path: 'dir', description: 'd' }],
    evidence: ['dir']
  },
  {
    name: 'parent is a file',
    files: { 'file.txt': 'x' },
    actions: [{ type: 'write_file', path: 'file.txt/child.txt', content: 'x', mode: 'create', description: 'c' }]
  }
]

function materialize(dir: string, files: Record<string, string | { b64: string } | null>): void {
  for (const [rel, content] of Object.entries(files)) {
    const full = join(dir, rel)
    if (content === null) {
      rmSync(full, { force: true })
      continue
    }
    mkdirSync(dirname(full), { recursive: true })
    writeFileSync(full, typeof content === 'string' ? content : Buffer.from(content.b64, 'base64'))
  }
}

/** Every regular file under dir, relative, with its utf-8 content. */
function tree(dir: string, prefix = ''): Record<string, string> {
  const out: Record<string, string> = {}
  for (const name of readdirSync(dir).sort()) {
    const full = join(dir, name)
    const rel = prefix ? `${prefix}/${name}` : name
    if (statSync(full).isDirectory()) Object.assign(out, tree(full, rel))
    else out[rel] = readFileSync(full, 'utf8')
  }
  return out
}

const asError = (e: unknown): unknown =>
  e instanceof RepoFileActionError
    ? { code: e.code, message: e.message, paths: e.paths }
    : { code: 'thrown', message: e instanceof Error ? e.message : String(e), paths: [] }

describe('repoFileActions vectors', () => {
  it('prepare and apply scenarios', async () => {
    const scenarios = []
    for (const sc of SCENARIOS) {
      const repo = mkdtempSync(join(tmpdir(), 'gitcito-fa-vec-'))
      temps.push(repo)
      materialize(repo, sc.files)
      const evidence = sc.evidence ?? []
      const ctx = {
        evidencePaths: new Set(evidence),
        completePaths: new Set(sc.complete ?? evidence),
        ignoredPaths: new Set(sc.ignored ?? [])
      }
      let prepared: unknown = null
      let prepareError: unknown = null
      let applied: unknown = null
      let applyError: unknown = null
      let after: unknown = null
      try {
        const out = await prepareRepoFileActions(repo, sc.actions as RepoChatFileAction[], ctx)
        prepared = out
        if (sc.apply) {
          materialize(repo, sc.apply.mutate ?? {})
          try {
            applied = await applyPreparedRepoFileActions(repo, out, new Set(sc.ignored ?? []))
          } catch (e) {
            applyError = asError(e)
          }
          after = tree(repo)
        }
      } catch (e) {
        prepareError = asError(e)
      }
      scenarios.push({ ...sc, prepared, prepareError, applied, applyError, after })
    }
    settle(join(VECTORS, 'fileActions.json'), {
      generated: PATH_SAMPLES.map((p) => ({ in: p, out: isGeneratedRepoPath(p) })),
      secret: PATH_SAMPLES.map((p) => ({ in: p, out: isSecretFile(p) })),
      scenarios
    })
  })
})

// ─── textconv ───────────────────────────────────────────────────────────────

const JSON_INPUTS = [
  '{"b":1,"a":{"z":2,"y":[{"q":1,"p":2}]}}',
  '[3,1,2]',
  '{nope',
  '{}',
  '[]',
  '"str"',
  '42',
  'null',
  'true',
  '{"n":[1.0,1e2,1e21,1.5e-7,0.1,-0,12345678901234567890,1e-6,123e-20,5e-324]}',
  '{"s":"quote\\" back\\\\ tab\\t nl\\n ctl\\u0001 del\\u007f bmp\\u2028\\u2029 <&> \\u00e9 \\ud83d\\ude00"}',
  '{"Z":1,"a":2,"_":3,"10":4,"9":5,"é":6,"B":7}',
  '{"a":1,"a":2}',
  '{"nested":{"empty":{},"arr":[[],{}],"deep":{"c":{"b":{"a":null}}}}}',
  ' \n {"ws":true} \n ',
  '{"a":1} x',
  '\uFEFF{"a":1}',
  '{"emoji":"\\ud83d"}'
]

const STRINGS_INPUTS: { name: string; bytes: Buffer }[] = (() => {
  const src = '/* Greeting */\n"hello" = "Hola";\n'
  const le = (t: string): Buffer => Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from(t, 'utf16le')])
  const be = (t: string): Buffer => Buffer.concat([Buffer.from([0xfe, 0xff]), Buffer.from(t, 'utf16le').swap16()])
  return [
    { name: 'utf16le', bytes: le(src) },
    { name: 'utf16be', bytes: be(src) },
    { name: 'utf8', bytes: Buffer.from(src) },
    { name: 'utf8 bom', bytes: Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from(src)]) },
    { name: 'accented le', bytes: le('"greet" = "Buenos días — ¿qué tal?";\n') },
    { name: 'no newline', bytes: Buffer.from('"a" = "b";') },
    { name: 'truncated be', bytes: Buffer.from([0xfe, 0xff, 0x00, 0x61, 0x00]) },
    { name: 'odd le', bytes: Buffer.from([0xff, 0xfe, 0x61, 0x00, 0x62]) },
    { name: 'empty', bytes: Buffer.alloc(0) },
    { name: 'astral le', bytes: le('"e" = "😀";') },
    { name: 'lone surrogate le', bytes: Buffer.from([0xff, 0xfe, 0x3d, 0xd8, 0x61, 0x00]) },
    { name: 'invalid utf8', bytes: Buffer.from([0x61, 0xc3, 0x28, 0xe2, 0x82, 0x0a]) },
    { name: 'bom only le', bytes: Buffer.from([0xff, 0xfe]) },
    { name: 'crlf', bytes: Buffer.from('"a" = "b";\r\n') }
  ]
})()

describe('textconv vectors', () => {
  it('jsonToStable and stringsToText', () => {
    settle(join(VECTORS, 'textconv.json'), {
      json: JSON_INPUTS.map((input) => {
        try {
          return { in: input, out: jsonToStable(input) }
        } catch {
          return { in: input, throws: true }
        }
      }),
      strings: STRINGS_INPUTS.map(({ name, bytes }) => {
        try {
          return { name, b64: bytes.toString('base64'), out: stringsToText(bytes) }
        } catch (e) {
          return { name, b64: bytes.toString('base64'), error: (e as Error).message }
        }
      })
    })
  })
})

// ─── documents (mammoth / SheetJS) ──────────────────────────────────────────

const W = 'http://schemas.openxmlformats.org/wordprocessingml/2006/main'
const W_STRICT = 'http://purl.oclc.org/ooxml/wordprocessingml/main'
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
const MC = 'http://schemas.openxmlformats.org/markup-compatibility/2006'
const V = 'urn:schemas-microsoft-com:vml'
const WP = 'http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing'
const W14 = 'http://schemas.microsoft.com/office/word/2010/wordml'

const CONTENT_TYPES =
  '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>' +
  '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">' +
  '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>' +
  '<Default Extension="xml" ContentType="application/xml"/>' +
  '<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>' +
  '</Types>'

const rootRels = (target: string, type = `${R}/officeDocument`): string =>
  '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>' +
  '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">' +
  `<Relationship Id="rId1" Type="${type}" Target="${target}"/>` +
  '</Relationships>'

const doc = (body: string, ns = W): string =>
  '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>' +
  `<w:document xmlns:w="${ns}" xmlns:r="${R}" xmlns:mc="${MC}" xmlns:v="${V}" xmlns:wp="${WP}" xmlns:w14="${W14}">` +
  `<w:body>${body}</w:body></w:document>`

const p = (inner: string): string => `<w:p>${inner}</w:p>`
const r = (inner: string): string => `<w:r>${inner}</w:r>`
const t = (text: string): string => `<w:t xml:space="preserve">${text}</w:t>`

interface DocxSpec {
  name: string
  files: Record<string, string>
}

const DOCX: DocxSpec[] = [
  { name: 'paragraphs', files: { 'word/document.xml': doc(p(r(t('Hello'))) + p(r(t('World ')) + r(t(' again')))) } },
  { name: 'empty-body', files: { 'word/document.xml': doc('') } },
  {
    name: 'tabs-breaks-hyphens',
    files: {
      'word/document.xml': doc(
        p(r(t('a') + '<w:tab/>' + t('b') + '<w:br/>' + t('c') + '<w:br w:type="page"/>' + '<w:noBreakHyphen/>' + '<w:softHyphen/>' + '<w:cr/>' + t('d')))
      )
    }
  },
  {
    name: 'table-merges',
    files: {
      'word/document.xml': doc(
        '<w:tbl><w:tblPr/><w:tblGrid/>' +
          '<w:tr><w:tc>' + p(r(t('A1'))) + '</w:tc><w:tc><w:tcPr><w:vMerge w:val="restart"/></w:tcPr>' + p(r(t('B1'))) + '</w:tc></w:tr>' +
          '<w:tr><w:tc><w:tcPr><w:gridSpan w:val="1"/></w:tcPr>' + p(r(t('A2'))) + '</w:tc><w:tc><w:tcPr><w:vMerge/></w:tcPr>' + p(r(t('hidden'))) + '</w:tc></w:tr>' +
          '<w:tr><w:trPr><w:del/></w:trPr><w:tc>' + p(r(t('deleted row'))) + '</w:tc></w:tr>' +
          '<w:tr><w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr>' + p(r(t('wide'))) + '</w:tc></w:tr>' +
          '</w:tbl>' + p(r(t('after')))
      )
    }
  },
  {
    name: 'revisions',
    files: {
      'word/document.xml': doc(
        p(r(t('kept ')) + '<w:ins>' + r(t('inserted ')) + '</w:ins>' + '<w:del>' + r('<w:delText>removed</w:delText>') + '</w:del>' + r(t('end'))) +
          '<w:p><w:pPr><w:rPr><w:del/></w:rPr></w:pPr>' + r(t('merged-')) + '</w:p>' +
          p(r(t('into next')))
      )
    }
  },
  {
    name: 'hyperlinks-fields',
    files: {
      'word/document.xml': doc(
        p('<w:hyperlink r:id="rId9">' + r(t('link text')) + '</w:hyperlink>' + '<w:hyperlink w:anchor="x">' + r(t(' anchor')) + '</w:hyperlink>') +
          p(
            r('<w:fldChar w:fldCharType="begin"/>') +
              r('<w:instrText xml:space="preserve"> HYPERLINK "https://example.com" </w:instrText>') +
              r('<w:fldChar w:fldCharType="separate"/>') +
              r(t('field link')) +
              r('<w:fldChar w:fldCharType="end"/>')
          ) +
          p('<w:fldSimple w:instr="PAGE">' + r(t('1')) + '</w:fldSimple>' + r(t('x')))
      ),
      'word/_rels/document.xml.rels': rootRels('https://example.com', `${R}/hyperlink`).replace('rId1', 'rId9')
    }
  },
  {
    name: 'textbox-alternate',
    files: {
      'word/document.xml': doc(
        p(
          r(t('before ')) +
            r('<w:pict><v:shape><v:textbox><w:txbxContent>' + p(r(t('in a box'))) + '</w:txbxContent></v:textbox></v:shape></w:pict>') +
            r(t('after'))
        ) +
          p(
            r(
              '<mc:AlternateContent><mc:Choice Requires="wps"><w:t>choice</w:t></mc:Choice><mc:Fallback>' +
                t('fallback') +
                '</mc:Fallback></mc:AlternateContent>'
            )
          ) +
          '<w:sdt><w:sdtPr/><w:sdtContent>' + p(r(t('in sdt'))) + '</w:sdtContent></w:sdt>' +
          p('<w:smartTag>' + r(t('smart')) + '</w:smartTag>' + '<w:bookmarkStart w:name="b"/>' + r(t(' tag')) + '<w:bookmarkEnd/>') +
          p(r('<w:footnoteReference w:id="1"/>') + r(t('noted')) + '<w:customXml>' + r(t('dropped')) + '</w:customXml>')
      )
    }
  },
  {
    name: 'checkbox-sdt',
    files: {
      'word/document.xml': doc(
        p('<w:sdt><w:sdtPr><w14:checkbox><w14:checked w14:val="1"/></w14:checkbox></w:sdtPr><w:sdtContent>' + r(t('☒')) + '</w:sdtContent></w:sdt>' + r(t(' done')))
      )
    }
  },
  { name: 'strict-namespace', files: { 'word/document.xml': doc(p(r(t('strict text'))), W_STRICT) } },
  {
    name: 'custom-main-part',
    files: { 'custom/main.xml': doc(p(r(t('from the relationship')))), '_rels/.rels': rootRels('custom/main.xml') }
  },
  { name: 'entities-whitespace', files: { 'word/document.xml': doc(p(r('<w:t>  a &amp; b &lt;c&gt; &#233;  </w:t>')) + p('') + p(r(t('\n  spaced  ')))) } },
  { name: 'no-document', files: { 'word/other.xml': doc(p(r(t('x')))) } },
  { name: 'no-body', files: { 'word/document.xml': `<?xml version="1.0"?><w:document xmlns:w="${W}"><w:x/></w:document>` } }
]

async function buildDocx(spec: DocxSpec): Promise<Buffer> {
  const zip = new JSZip()
  const date = new Date(Date.UTC(2020, 0, 1))
  zip.file('[Content_Types].xml', CONTENT_TYPES, { date })
  if (!spec.files['_rels/.rels']) zip.file('_rels/.rels', rootRels('word/document.xml'), { date })
  for (const [name, content] of Object.entries(spec.files)) zip.file(name, content, { date })
  return zip.generateAsync({ type: 'nodebuffer', compression: 'DEFLATE' })
}

async function buildXlsx(): Promise<Record<string, Buffer>> {
  const XLSX = await import('xlsx')
  const out: Record<string, Buffer> = {}
  const write = (wb: unknown): Buffer => XLSX.write(wb as never, { type: 'buffer', bookType: 'xlsx' }) as Buffer

  {
    const wb = XLSX.utils.book_new()
    XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet([['name', 'qty'], ['ok', 2]]), 'First')
    XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet([['x'], [1]]), 'Second')
    out['two-sheets'] = write(wb)
  }
  {
    const wb = XLSX.utils.book_new()
    const ws = XLSX.utils.aoa_to_sheet([
      ['text', 'a,b', 'say "hi"', 'line\nbreak', 'ID', ' padded ', ''],
      [1, -2, 0.1 + 0.2, 1 / 3, 1e10, 123456789012, -0.000012345],
      [1e21, 2147483648, -2147483649, 0.5, 100, 1e-10, 9.87654321e15],
      [true, false, null, undefined, 'é ü 😀', '=SUM(A1)', 42]
    ])
    XLSX.utils.book_append_sheet(wb, ws, 'Values')
    out['values'] = write(wb)
  }
  {
    const wb = XLSX.utils.book_new()
    const ws = XLSX.utils.aoa_to_sheet([[1234.5, 0.256, 1234567.891, 45000.75, 45000.75, 45000.75, 0.5, 1234.5]])
    const fmts = ['0.00', '0%', '#,##0.00', 'm/d/yy', 'yyyy-mm-dd hh:mm:ss', 'd-mmm-yy', 'h:mm AM/PM', '#,##0']
    fmts.forEach((z, c) => {
      const cell = ws[XLSX.utils.encode_cell({ r: 0, c })]
      cell.z = z
      delete cell.w
    })
    XLSX.utils.book_append_sheet(wb, ws, 'Formats')
    out['formats'] = write(wb)
  }
  {
    const wb = XLSX.utils.book_new()
    const ws = XLSX.utils.aoa_to_sheet([['a'], [], ['c', null, 'e']])
    ws['B5'] = { t: 'n', v: 7 }
    ws['!ref'] = 'A1:C5'
    ws['C6'] = { t: 'n', f: 'A1+1' }
    ws['!ref'] = 'A1:C6'
    XLSX.utils.book_append_sheet(wb, ws, 'Gaps')
    XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet([]), 'Empty')
    XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet([['#N/A?']]), 'Sheet "3", odd')
    out['gaps'] = write(wb)
  }
  {
    const wb = XLSX.utils.book_new()
    const ws = XLSX.utils.aoa_to_sheet([[new Date(Date.UTC(2024, 1, 29, 13, 45, 30))]], { cellDates: true })
    XLSX.utils.book_append_sheet(wb, ws, 'Dates')
    out['dates'] = write(wb)
  }
  return out
}

describe('document vectors', () => {
  it('docx through mammoth and xlsx through SheetJS', async () => {
    if (WRITE) {
      mkdirSync(DOCS, { recursive: true })
      for (const spec of DOCX) writeFileSync(join(DOCS, `${spec.name}.docx`), await buildDocx(spec))
      for (const [name, buf] of Object.entries(await buildXlsx())) writeFileSync(join(DOCS, `${name}.xlsx`), buf)
      writeFileSync(join(DOCS, 'not-a-zip.docx'), 'this is not a zip archive')
    }
    const results: { file: string; out?: string; throws?: boolean }[] = []
    for (const file of readdirSync(DOCS).filter((f) => /\.(docx|xlsx)$/.test(f)).sort()) {
      try {
        results.push({ file, out: await convertFile(join(DOCS, file)) })
      } catch {
        results.push({ file, throws: true })
      }
    }
    expect(existsSync(join(DOCS, 'paragraphs.docx'))).toBe(true)
    settle(join(DOCS, 'documents.json'), results)
  })
})
