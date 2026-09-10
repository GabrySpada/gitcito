import { describe, it, expect, beforeEach, afterAll } from 'vitest'
import { rmSync, mkdtempSync, mkdirSync, writeFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { registryFilePath, listRepos, rememberRepo, forgetRepo, scanRoots } from '../src/main/repoRegistry'
import { cloneFixture, cleanupFixtures } from './fixtures'

// The electron stub's app.getPath() returns tmpdir(), so the registry lands at
// a predictable path we can clear between tests.
beforeEach(() => {
  rmSync(registryFilePath(), { force: true })
})

const dirs: string[] = []
function tempRepo(): string {
  const d = mkdtempSync(join(tmpdir(), 'gitcito-reg-'))
  dirs.push(d)
  return d
}

afterAll(() => {
  cleanupFixtures()
  for (const d of dirs) rmSync(d, { recursive: true, force: true })
})

describe('repoRegistry', () => {
  it('starts empty', async () => {
    expect(await listRepos()).toEqual([])
  })

  it('remembers a repo and survives a reload', async () => {
    const dir = tempRepo()
    await rememberRepo(dir)
    const repos = await listRepos()
    expect(repos).toHaveLength(1)
    expect(repos[0].path).toBe(dir)
    expect(repos[0].source).toBe('opened')
    expect(repos[0].lastOpenedAt).toBeGreaterThan(0)
  })

  it('upserts rather than duplicating, and bumps lastOpenedAt', async () => {
    const dir = tempRepo()
    await rememberRepo(dir)
    const first = (await listRepos())[0].lastOpenedAt
    await new Promise((r) => setTimeout(r, 1100))
    await rememberRepo(dir)
    const repos = await listRepos()
    expect(repos).toHaveLength(1)
    expect(repos[0].lastOpenedAt).toBeGreaterThan(first)
  })

  it('marks a deleted folder missing instead of dropping it', async () => {
    const dir = tempRepo()
    await rememberRepo(dir)
    rmSync(dir, { recursive: true, force: true })
    const repos = await listRepos()
    expect(repos).toHaveLength(1)
    expect(repos[0].missing).toBe(true)
  })

  it('forgets a repo without touching disk', async () => {
    const dir = tempRepo()
    await rememberRepo(dir)
    await forgetRepo(dir)
    expect(await listRepos()).toEqual([])
    expect(existsSync(dir)).toBe(true)
  })

  it('rejects a path that is not safe', async () => {
    await rememberRepo('')
    expect(await listRepos()).toEqual([])
  })

  it('populates branch from .git when remembering a real repo', async () => {
    const dir = cloneFixture('file-nav')
    await rememberRepo(dir)
    const repos = await listRepos()
    expect(repos[0].branch).toBe('main')
  })

  it('keeps an opened repo opened when a scan finds it again', async () => {
    // A dedicated parent, never $TMPDIR itself: scanning the system temp
    // directory would walk every other test's scratch files.
    const parent = mkdtempSync(join(tmpdir(), 'gitcito-scanroot-'))
    dirs.push(parent)
    const dir = join(parent, 'alpha')
    mkdirSync(join(dir, '.git'), { recursive: true })
    writeFileSync(join(dir, '.git', 'HEAD'), 'ref: refs/heads/main\n')

    await rememberRepo(dir)
    await scanRoots([{ path: parent, depth: 2 }])
    const entry = (await listRepos()).find((r) => r.path === dir)
    expect(entry?.source).toBe('opened')
    expect(entry?.lastOpenedAt).toBeGreaterThan(0)
  })

  it('adds a repo found only by a scan as scanned, unopened', async () => {
    const parent = mkdtempSync(join(tmpdir(), 'gitcito-scanroot-'))
    dirs.push(parent)
    const dir = join(parent, 'beta')
    mkdirSync(join(dir, '.git'), { recursive: true })
    writeFileSync(join(dir, '.git', 'HEAD'), 'ref: refs/heads/develop\n')

    await scanRoots([{ path: parent, depth: 2 }])
    const entry = (await listRepos()).find((r) => r.path === dir)
    expect(entry?.source).toBe('scanned')
    expect(entry?.lastOpenedAt).toBe(0)
    expect(entry?.branch).toBe('develop')
  })
})
