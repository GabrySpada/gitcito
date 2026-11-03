import { cpSync, mkdtempSync, rmSync, writeFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { ROOT, startApp, type Running } from './harness'

// Core git flows through the real Go git service, called the way the renderer calls it (window.api.git).
// Each run works on a copy of a playground repo, never the shared one.

let app: Running
let repo: string
let scratch: string

const git = <T>(method: string, ...args: unknown[]): Promise<T> =>
  app.page.evaluate(([m, a]) => window.api.git(m as string, ...(a as unknown[])), [method, args] as const) as Promise<T>

beforeAll(async () => {
  const src = join(ROOT, 'examples', 'playground', 'merge-conflict')
  if (!existsSync(src)) execFileSync('bash', [join(ROOT, 'examples', 'setup-playground.sh')], { stdio: 'ignore' })
  scratch = mkdtempSync(join(tmpdir(), 'gitcito-e2e-repo-'))
  repo = join(scratch, 'repo')
  cpSync(src, repo, { recursive: true, verbatimSymlinks: true })
  app = await startApp()
})
afterAll(async () => {
  await app?.stop()
  if (scratch) rmSync(scratch, { recursive: true, force: true })
})

interface Status {
  current: string
  staged: { path: string }[]
  unstaged: { path: string }[]
  untracked: { path: string }[]
}

describe('core git flows (server build)', () => {
  it('opens a repository and reads its log', async () => {
    const log = await git<{ hash: string; subject: string }[]>('log', repo)
    expect(log.length).toBeGreaterThan(0)
    expect(log[0].hash).toMatch(/^[0-9a-f]{40}$/)
  })

  it('stages and unstages a file', async () => {
    writeFileSync(join(repo, 'e2e-new.txt'), 'hello\n')
    let st = await git<Status>('status', repo)
    expect(JSON.stringify(st)).toContain('e2e-new.txt')
    await git('stage', repo, ['e2e-new.txt'])
    st = await git<Status>('status', repo)
    expect(st.staged.map((f) => f.path)).toContain('e2e-new.txt')
    await git('unstage', repo, ['e2e-new.txt'])
    st = await git<Status>('status', repo)
    expect(st.staged.map((f) => f.path)).not.toContain('e2e-new.txt')
  })

  it('commits, then undoes the commit keeping the work', async () => {
    await git('stage', repo, ['e2e-new.txt'])
    await git('commit', repo, 'test: e2e commit')
    const log = await git<{ subject: string }[]>('log', repo)
    expect(log[0].subject).toBe('test: e2e commit')
    await git('undoCommit', repo)
    const after = await git<{ subject: string }[]>('log', repo)
    expect(after[0].subject).not.toBe('test: e2e commit')
    expect(existsSync(join(repo, 'e2e-new.txt'))).toBe(true)
  })

  it('creates a branch and switches to it', async () => {
    await git('createBranch', repo, 'e2e/branch')
    const st = await git<Status>('status', repo)
    expect(st.current).toBe('e2e/branch')
    await git('checkout', repo, 'main')
    expect((await git<Status>('status', repo)).current).toBe('main')
  })

  it('stashes and pops a change', async () => {
    await git('stage', repo, ['e2e-new.txt'])
    await git('stash', repo, 'e2e stash')
    const stashes = await git<{ message: string }[]>('stashes', repo)
    expect(stashes[0]?.message).toContain('e2e stash')
    await git('stashPop', repo, 0)
    expect(await git<unknown[]>('stashes', repo)).toHaveLength(stashes.length - 1)
  })

  it('surfaces a git error as a rejected promise with git’s message', async () => {
    const err = await app.page.evaluate(
      (r) => window.api.git('checkout', r, 'no-such-branch-e2e').then(() => '', (e: Error) => e.message),
      repo
    )
    expect(err).toMatch(/no-such-branch-e2e/)
  })

  it('runs a terminal session and streams its output in order', async () => {
    const out = await app.page.evaluate(async (cwd) => {
      const id = await window.api.term.create(cwd, 80, 24)
      let buf = ''
      const off = window.api.term.onData(id, (d: string) => {
        buf += d
      })
      window.api.term.input(id, 'echo e2e-one; echo e2e-two\r')
      const deadline = Date.now() + 10_000
      while (Date.now() < deadline && !/e2e-two/.test(buf.replace(/echo e2e-one; echo e2e-two/g, ''))) {
        await new Promise((r) => setTimeout(r, 100))
      }
      off()
      window.api.term.kill(id)
      return buf
    }, repo)
    const printed = out.replace(/echo e2e-one; echo e2e-two/g, '')
    expect(printed.indexOf('e2e-one')).toBeGreaterThan(-1)
    expect(printed.indexOf('e2e-two')).toBeGreaterThan(printed.indexOf('e2e-one'))
  })
})
