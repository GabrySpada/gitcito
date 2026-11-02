// The semantic diff runs in the renderer (web-tree-sitter in the webview); the Go side only reads the two
// blobs. These cases moved here from newFeatures.test.ts when the module moved to src/renderer/src/lib.
import { beforeAll, describe, it, expect } from 'vitest'
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { readFileSync, readdirSync } from 'node:fs'
import { diffSymbols, semanticCompare, setGrammarSource } from '../src/renderer/src/lib/semantic'

const ROOT = resolve(__dirname, '..')
const RUNTIME = join(ROOT, 'node_modules', 'web-tree-sitter', 'tree-sitter.wasm')
const GRAMMARS = join(ROOT, 'node_modules', 'tree-sitter-wasms', 'out')

beforeAll(() => {
  // In the page these are URLs under /tree-sitter; here they are files on disk.
  setGrammarSource({
    runtime: RUNTIME,
    grammar: (name) => join(GRAMMARS, `tree-sitter-${name}.wasm`),
    exists: async (name) => existsSync(join(GRAMMARS, `tree-sitter-${name}.wasm`))
  })
})

describe('semantic diff (tree-sitter)', () => {
  const OLD_TS = `export function start(a: string) {
  const x = compute(a)
  return x + 1
}
class Repo {
  open(path: string) { return path }
  close() { return null }
}
`
  const NEW_TS = `export function boot(a: string) {
  const x = compute(a)
  return x + 1
}
class Repo {
  open(path: string, mode: string) { return path }
  reopen() { return null }
}
export function brandNew() { return 7 }
`

  it('reads a rename as a rename, not a delete plus an add', async () => {
    const { language, changes } = await semanticCompare('app.ts', OLD_TS, NEW_TS)
    expect(language).toBe('typescript')
    const renamed = changes.filter((c) => c.kind === 'renamed')
    expect(renamed.map((c) => [c.oldName, c.symbol])).toEqual([
      ['start', 'boot'],
      ['Repo.close', 'Repo.reopen']
    ])
    // Bodies were untouched, so neither rename is flagged as rewritten.
    expect(renamed.every((c) => !c.bodyChanged)).toBe(true)
    expect(changes.some((c) => c.kind === 'removed')).toBe(false)
  })

  it('separates a signature change from a body change', async () => {
    const { changes } = await semanticCompare('app.ts', OLD_TS, NEW_TS)
    const sig = changes.find((c) => c.kind === 'signature')!
    expect(sig.symbol).toBe('Repo.open')
    expect(sig.oldSignature).toBe('(path: string)')
    expect(sig.newSignature).toBe('(path: string, mode: string)')
    expect(sig.bodyChanged).toBe(false)
  })

  it('reports new declarations and drops the container they live in', async () => {
    const { changes } = await semanticCompare('app.ts', OLD_TS, NEW_TS)
    expect(changes.find((c) => c.kind === 'added')?.symbol).toBe('brandNew')
    // `Repo` itself is not listed: its own rows already explain the change.
    expect(changes.some((c) => c.symbol === 'Repo')).toBe(false)
  })

  it('parses the other shipped languages, not just TypeScript', async () => {
    const cases: [string, string, string, string][] = [
      ['a.py', 'python', 'def start(a):\n    return a\n', 'def boot(a):\n    return a\n'],
      ['a.go', 'go', 'package m\nfunc Start(a int) int { return a }\n', 'package m\nfunc Boot(a int) int { return a }\n'],
      ['a.rs', 'rust', 'fn start(a: u8) -> u8 { a }\n', 'fn boot(a: u8) -> u8 { a }\n'],
      ['a.rb', 'ruby', 'def start(a)\n  a\nend\n', 'def boot(a)\n  a\nend\n'],
      ['a.java', 'java', 'class A { int start(int a) { return a; } }', 'class A { int boot(int a) { return a; } }'],
      ['a.cs', 'c_sharp', 'class A { int Start(int a) { return a; } }', 'class A { int Boot(int a) { return a; } }']
    ]
    for (const [file, grammar, before, after] of cases) {
      const r = await semanticCompare(file, before, after)
      expect(r.language, file).toBe(grammar)
      expect(r.changes.filter((c) => c.kind === 'renamed'), file).toHaveLength(1)
    }
  })

  it('stays out of the way for languages with no grammar', async () => {
    const r = await semanticCompare('notes.txt', 'one\n', 'two\n')
    expect(r.language).toBeNull()
    expect(r.changes).toEqual([])
  })

  it('matches a rename whose body also changed, and counts its call sites', () => {
    const before = [
      { key: 'start', name: 'start', kind: 'function' as const, signature: '(a)', body: 'const x = compute(a); return x + 1', line: 1 }
    ]
    const after = [
      { key: 'boot', name: 'boot', kind: 'function' as const, signature: '(a)', body: 'const x = compute(a); return x + 2', line: 1 }
    ]
    const [change] = diffSymbols(before, after, 'start start', 'boot boot boot')
    expect(change.kind).toBe('renamed')
    expect(change.bodyChanged).toBe(true)
    expect(change.detail).toBe('3')
  })

  it('calls a symbol that only shifted position a move', () => {
    const sym = { name: 'f', kind: 'function' as const, signature: '()', body: 'return 1' }
    const [change] = diffSymbols([{ ...sym, key: 'f', line: 10 }], [{ ...sym, key: 'f', line: 42 }])
    expect(change.kind).toBe('moved')
    expect(change.detail).toBe('+32')
  })

  it('ignores the small drift of a symbol whose neighbour grew', () => {
    const sym = { name: 'f', kind: 'function' as const, signature: '()', body: 'return 1' }
    expect(diffSymbols([{ ...sym, key: 'f', line: 10 }], [{ ...sym, key: 'f', line: 12 }])).toEqual([])
  })

  it('refuses to pair two unrelated one-liners as a rename', async () => {
    // `return "pong"` and `return true` share most of their characters; only a
    // long-enough body may be matched on similarity alone.
    const before = 'package m\nfunc LegacyPing() string { return "pong" }\n'
    const after = 'package m\nfunc HealthCheck() bool { return true }\n'
    const { changes } = await semanticCompare('a.go', before, after)
    expect(changes.map((c) => c.kind).sort()).toEqual(['added', 'removed'])
  })
})


// The Go semanticDiff returns the two blob texts; the renderer finishes the job. Every Go golden carries what the
// TypeScript main process concluded for those texts (`tsSemantic`), so the renderer must conclude the same.
describe('semantic diff goldens (Go blobs → renderer verdict)', () => {
  const dir = join(ROOT, 'internal', 'gitsvc', 'testdata', 'goldens', 'semanticDiff')
  const files = readdirSync(dir).filter((f) => f.endsWith('.json'))

  it('has goldens to check', () => {
    expect(files.length).toBeGreaterThan(0)
  })

  for (const f of files) {
    it(f, async () => {
      const g = JSON.parse(readFileSync(join(dir, f), 'utf8'))
      const file = g.args[0] as string
      const { oldText, newText } = g.result as { oldText: string; newText: string }
      const verdict =
        !oldText && !newText ? { language: null, changes: [] } : await semanticCompare(file, oldText, newText)
      expect(verdict).toEqual(g.tsSemantic)
    })
  }
})
