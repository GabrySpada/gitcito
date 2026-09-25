import { describe, it, expect, afterAll } from 'vitest'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { gitService } from '../src/main/git'
import { cloneFixture, cleanupFixtures } from './fixtures'
import { parseDiff } from '../src/renderer/src/lib/diff'
import {
  buildLinePatch,
  countPatchLines,
  diffHeader,
  hunkLines,
  type PatchDirection
} from '../src/renderer/src/lib/linePatch'
import { wipSideAfter } from '../src/renderer/src/lib/wipSide'

// Line staging end to end: the patch the diff viewer builds, applied by the
// same gitService call the app makes, against a real repository. Whatever git
// accepts or refuses here is what the user gets.
afterAll(cleanupFixtures)

const git = (repo: string, ...args: string[]): string => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8' })
/** The file as the index holds it. */
const indexed = (repo: string, file: string): string => git(repo, 'show', `:${file}`)

/** Build the patch for the lines whose text matches, from the diff the app would show. */
async function patchFor(
  repo: string,
  file: string,
  direction: PatchDirection,
  pick: (text: string, kind: 'add' | 'del') => boolean
): Promise<string> {
  const diff = await gitService.diffFile(repo, file, direction === 'unstage', false)
  const lines = parseDiff(diff)
  const selected = new Set<number>()
  lines.forEach((l, i) => {
    if ((l.kind === 'add' || l.kind === 'del') && pick(l.text, l.kind)) selected.add(i)
  })
  const patch = buildLinePatch(lines, diffHeader(diff), selected, direction)
  expect(patch).not.toBeNull()
  return patch!
}

async function applyLines(
  repo: string,
  file: string,
  direction: PatchDirection,
  pick: (text: string, kind: 'add' | 'del') => boolean
): Promise<string> {
  const patch = await patchFor(repo, file, direction, pick)
  await gitService.stagePatch(repo, patch, direction === 'unstage')
  return patch
}

describe('line staging (line-staging fixture)', () => {
  const HEAD_CONFIG = `const config = {
  host: 'localhost',
  port: 3000,
  retries: 3,
  timeout: 1000,
}
module.exports = config
`

  it('stages one added line and nothing else', async () => {
    const R = cloneFixture('line-staging')
    await applyLines(R, 'config.js', 'stage', (t) => t.includes('debug'))
    const staged = indexed(R, 'config.js')
    expect(staged).toContain('debug: true')
    expect(staged).not.toContain('tls: true')
    // The unselected change to `host` stays in the working tree only.
    expect(staged).toContain("host: 'localhost'")
    expect(git(R, 'diff', '--', 'config.js')).toContain('+  tls: true,')
  })

  it('stages one removed line without its replacement', async () => {
    const R = cloneFixture('line-staging')
    await applyLines(R, 'config.js', 'stage', (t, k) => k === 'del' && t.includes('localhost'))
    const staged = indexed(R, 'config.js')
    expect(staged).not.toContain('localhost')
    expect(staged).not.toContain('api.example.com')
  })

  it('unstages one line of a staged file, leaving the rest staged', async () => {
    const R = cloneFixture('line-staging')
    git(R, 'add', 'config.js')
    await applyLines(R, 'config.js', 'unstage', (t) => t.includes('tls'))
    const staged = indexed(R, 'config.js')
    expect(staged).not.toContain('tls: true')
    expect(staged).toContain('debug: true')
    expect(staged).toContain('api.example.com')
    // The working tree is never touched by either direction.
    expect(git(R, 'diff', '--', 'config.js')).toContain('+  tls: true,')
  })

  it('unstages a removed line, putting it back in the index', async () => {
    const R = cloneFixture('line-staging')
    git(R, 'add', 'config.js')
    await applyLines(R, 'config.js', 'unstage', (t, k) => k === 'del' && t.includes('localhost'))
    const staged = indexed(R, 'config.js')
    expect(staged).toContain("host: 'localhost'")
    expect(staged).toContain('api.example.com')
  })

  it('undoes a line stage by applying the same patch in reverse', async () => {
    const R = cloneFixture('line-staging')
    const patch = await applyLines(R, 'config.js', 'stage', (t) => t.includes('debug'))
    await gitService.stagePatch(R, patch, true)
    expect(indexed(R, 'config.js')).toBe(HEAD_CONFIG)
    // …and redo puts it back.
    await gitService.stagePatch(R, patch)
    expect(indexed(R, 'config.js')).toContain('debug: true')
  })

  it('stages a whole hunk through the same builder', async () => {
    const R = cloneFixture('line-staging')
    const diff = await gitService.diffFile(R, 'config.js', false, false)
    const lines = parseDiff(diff)
    const patch = buildLinePatch(lines, diffHeader(diff), hunkLines(lines, 0), 'stage')!
    await gitService.stagePatch(R, patch)
    expect(git(R, 'diff', '--', 'config.js')).toBe('')
  })
})

describe('line staging edge cases', () => {
  const numbered = (n: number, edit: (i: number) => string | null = () => null): string =>
    Array.from({ length: n }, (_, k) => edit(k + 1) ?? `line ${k + 1}`).join('\n') + '\n'

  function repoWith(file: string, committed: string, working: string): string {
    const R = cloneFixture('line-staging')
    writeFileSync(join(R, file), committed)
    git(R, 'add', file)
    git(R, 'commit', '-qm', `add ${file}`)
    writeFileSync(join(R, file), working)
    return R
  }

  // Two hunks; the first adds lines that are left out, so the second hunk's new
  // side has to be shifted back or git lands it in the wrong place.
  it('stages a line in a later hunk when earlier hunks are skipped', async () => {
    const base = numbered(40)
    const working = base
      .replace('line 3\n', 'line 3\nextra A\nextra B\n')
      .replace('line 30\n', 'line 30\nwanted\n')
    const R = repoWith('many.txt', base, working)
    await applyLines(R, 'many.txt', 'stage', (t) => t === 'wanted')
    expect(indexed(R, 'many.txt')).toBe(base.replace('line 30\n', 'line 30\nwanted\n'))
  })

  // Undo applies the stage patch in reverse, which anchors on the shifted side.
  it('undoes a later-hunk stage exactly', async () => {
    const base = numbered(40)
    const working = base
      .replace('line 3\n', 'line 3\nextra A\nextra B\n')
      .replace('line 30\n', 'line 30\nwanted\n')
    const R = repoWith('many.txt', base, working)
    const patch = await applyLines(R, 'many.txt', 'stage', (t) => t === 'wanted')
    expect(patch).toContain('@@ -28,6 +28,7 @@')
    await gitService.stagePatch(R, patch, true)
    expect(indexed(R, 'many.txt')).toBe(base)
  })

  it('unstages a line in a later hunk when earlier hunks stay staged', async () => {
    const base = numbered(40)
    const working = base
      .replace('line 3\n', 'line 3\nextra A\nextra B\n')
      .replace('line 30\n', 'line 30\nunwanted\n')
    const R = repoWith('many.txt', base, working)
    git(R, 'add', 'many.txt')
    await applyLines(R, 'many.txt', 'unstage', (t) => t === 'unwanted')
    expect(indexed(R, 'many.txt')).toBe(base.replace('line 3\n', 'line 3\nextra A\nextra B\n'))
  })

  it('stages one line of several across two hunks, in both directions', async () => {
    const base = numbered(40)
    const working = base
      .replace('line 5\n', 'line 5\nfirst\n')
      .replace('line 20\n', '')
      .replace('line 35\n', 'line 35\nlast\n')
    const R = repoWith('many.txt', base, working)
    await applyLines(R, 'many.txt', 'stage', (t) => t === 'last' || t === 'line 20')
    expect(indexed(R, 'many.txt')).toBe(base.replace('line 20\n', '').replace('line 35\n', 'line 35\nlast\n'))
    await applyLines(R, 'many.txt', 'unstage', (t) => t === 'line 20')
    expect(indexed(R, 'many.txt')).toBe(base.replace('line 35\n', 'line 35\nlast\n'))
  })

  // "\ No newline at end of file" belongs to the line above it; when that line
  // is left out of the patch, so is the marker.
  it('keeps the no-newline marker with its own line', async () => {
    const base = 'one\ntwo\nthree'
    const working = 'one\ninserted\ntwo\nthree\nfour'
    const R = repoWith('eof.txt', base, working)
    await applyLines(R, 'eof.txt', 'stage', (t) => t === 'inserted')
    expect(indexed(R, 'eof.txt')).toBe('one\ninserted\ntwo\nthree')
  })

  it('stages the change at a file end that has no newline', async () => {
    const base = 'one\ntwo\nthree'
    const working = 'one\ninserted\ntwo\nthree\nfour'
    const R = repoWith('eof.txt', base, working)
    await applyLines(R, 'eof.txt', 'stage', (t) => t === 'three' || t === 'four')
    expect(indexed(R, 'eof.txt')).toBe('one\ntwo\nthree\nfour')
  })

  // A context line whose own text starts with a backslash is not the marker.
  it('keeps context lines that begin with a backslash', async () => {
    const base = '\\begin{doc}\nbody\n\\end{doc}\n'
    const working = '\\begin{doc}\nbody\nmore\n\\end{doc}\n'
    const R = repoWith('doc.tex', base, working)
    await applyLines(R, 'doc.tex', 'stage', (t) => t === 'more')
    expect(indexed(R, 'doc.tex')).toBe(working)
  })

  // Part of a new file unstaged: the index keeps the file, minus that line.
  it('unstages one line of a newly added file', async () => {
    const R = cloneFixture('line-staging')
    writeFileSync(join(R, 'fresh.txt'), 'keep 1\ndrop\nkeep 2\n')
    git(R, 'add', 'fresh.txt')
    const patch = await patchFor(R, 'fresh.txt', 'unstage', (t) => t === 'drop')
    expect(patch).not.toContain('new file mode')
    await gitService.stagePatch(R, patch, true)
    expect(indexed(R, 'fresh.txt')).toBe('keep 1\nkeep 2\n')
  })

  it('keeps the new-file header when every line is picked', async () => {
    const R = cloneFixture('line-staging')
    writeFileSync(join(R, 'fresh.txt'), 'a\nb\n')
    git(R, 'add', 'fresh.txt')
    await applyLines(R, 'fresh.txt', 'unstage', () => true)
    // Unstaged whole, the file leaves the index rather than staying empty.
    expect(git(R, 'ls-files', '--', 'fresh.txt')).toBe('')
  })

  it('returns null when no change line is selected', () => {
    const lines = parseDiff('@@ -1,1 +1,2 @@\n a\n+b\n')
    expect(buildLinePatch(lines, '', new Set([1]), 'stage')).toBeNull()
  })
})

// The bug this guards: unstage every line of a staged file and the viewer was
// left on an empty staged diff ("No changes") instead of following the file.
describe('which side the viewer shows after staging from it', () => {
  it('moves to the unstaged side once every staged line is unstaged', async () => {
    const R = cloneFixture('line-staging')
    git(R, 'add', 'config.js')
    await applyLines(R, 'config.js', 'unstage', () => true)
    const status = await gitService.status(R)
    expect(wipSideAfter(status, 'config.js', true)).toEqual({ kind: 'switch', staged: false, untracked: false })
  })

  it('moves to the staged side once every line is staged', async () => {
    const R = cloneFixture('line-staging')
    await applyLines(R, 'config.js', 'stage', () => true)
    const status = await gitService.status(R)
    expect(wipSideAfter(status, 'config.js', false)).toEqual({ kind: 'switch', staged: true, untracked: false })
  })

  it('stays put while the file still has changes on its side', async () => {
    const R = cloneFixture('line-staging')
    await applyLines(R, 'config.js', 'stage', (t) => t.includes('debug'))
    const status = await gitService.status(R)
    expect(wipSideAfter(status, 'config.js', false)).toEqual({ kind: 'stay' })
    expect(wipSideAfter(status, 'config.js', true)).toEqual({ kind: 'stay' })
  })

  it('reports a file with no changes left on either side as gone', async () => {
    const R = cloneFixture('line-staging')
    git(R, 'checkout', '--', 'config.js')
    const status = await gitService.status(R)
    expect(wipSideAfter(status, 'config.js', false)).toEqual({ kind: 'gone' })
  })
})

describe('countPatchLines', () => {
  it('counts changes below the headers only', () => {
    const patch = 'diff --git a/q.sql b/q.sql\n--- a/q.sql\n+++ b/q.sql\n@@ -1,2 +1,2 @@\n--- note\n+-- remark\n select 1\n'
    expect(countPatchLines(patch)).toBe(2)
  })
})
