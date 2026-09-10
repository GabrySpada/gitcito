import { describe, it, expect, beforeEach, afterAll } from 'vitest'
import { rmSync, mkdtempSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { registryFilePath, listRepos, rememberRepo, forgetRepo } from '../src/main/repoRegistry'

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
})
