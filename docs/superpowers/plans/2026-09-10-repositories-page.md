# Repositories Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Repositories page tab that lists every repository Gitcito knows about — open, favourite, recent, per-workspace and all — backed by a persistent registry and optional folder scanning.

**Architecture:** A new main-process domain module owns `gitcito-repos.json` in userData and exposes `repos:*` IPC handlers, following the `main/vault.ts` pattern. Repository metadata (branch, owner) is read from `.git` files rather than by spawning git, so rows cost nothing. The renderer gets a `stores/repos.ts` slice, a pure `lib/repoSections.ts` for section assembly, and a `RepositoriesPage.tsx` component rendered as a page tab.

**Tech Stack:** Electron, React 18, TypeScript, Zustand, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-10-repositories-page-design.md`

## Global Constraints

- **Style:** No semicolons, single quotes, 2-space indent, ~110 columns. Explicit return types on exported functions. `type` for unions, `interface` for object shapes. No `any` — use `unknown` plus narrowing.
- **Comments explain why, not what.** Match the surrounding file's comment density.
- **Every user-facing string is translated** and must be added to **all 16 locale dictionaries** in `src/renderer/src/i18n/`: `en, ar, de, es, fr, he, it, ja, ko, nl, pl, pt-BR, ru, tr, uk, zh-CN`. `Dict` is derived from `en.ts`, so a missing key is a compile error. Never clear that error by pasting English — translate it. Load the **`translations`** skill before Task 9.
- **Git runs in the main process only.** The renderer never touches the filesystem.
- **Registry paths are validated** with `isSafeRepoPath` from `src/main/aiSchemas.ts` before reaching the filesystem.
- **The gate:** `npm run typecheck`, `npm run lint:i18n`, `npm run lint:docs`, `npm test`, `npm run build`. `/verify` runs all of them.
- **Do not launch the app.** Compile-only checks.
- **Commit subjects are lowercase** after the Conventional Commits type (`docs: design for…`). Commitlint rejects sentence-case, despite CLAUDE.md's examples.
- **Branch:** all work lands on `feat/repositories-page`.

---

### Task 1: The registry file store

The persistence layer alone: load, save, list with existence checking, remember, forget. No metadata reading yet (Task 2 adds it), no IPC (Task 4 adds it).

**Files:**
- Modify: `src/shared/types.ts` (add `RegistryRepo`, `RepoScanRoot`, two `AppSettings` fields and their defaults)
- Create: `src/main/repoRegistry.ts`
- Test: `test/repoRegistry.test.ts`

**Interfaces:**
- Consumes: nothing
- Produces: `RegistryRepo`, `RepoScanRoot` (shared types); `registryFilePath(): string`, `listRepos(): Promise<RegistryRepo[]>`, `rememberRepo(path: string): Promise<RegistryRepo[]>`, `forgetRepo(path: string): Promise<RegistryRepo[]>`

- [ ] **Step 1: Add the shared types**

In `src/shared/types.ts`, near the other repo types (around `RepoRef`, line ~2590):

```ts
/** One repository Gitcito knows about. Everything here except `path` is a
 *  cache: lose the registry file and a rescan rebuilds it. */
export interface RegistryRepo {
  /** Canonical absolute path — the identity, and the key used everywhere else. */
  path: string
  /** Folder name at index time. `repoAliases` still wins for display. */
  name: string
  /** First path segment of the remote's namespace: 'top-solution' for a GitLab
   *  group, the org for GitHub. Null with no remote or an unparseable URL. */
  owner: string | null
  /** Branch as of the last index, read from .git/HEAD. Null when detached. */
  branch: string | null
  /** How it got here — a scanned repo the user has never opened still lists. */
  source: 'opened' | 'scanned'
  /** Unix seconds. Drives the Recent section, which is therefore not capped. */
  lastOpenedAt: number
  /** Folder absent at the last existence check. Stored rather than computed:
   *  stat-ing 200 paths belongs on page load, never in a render. */
  missing: boolean
}

/** A folder Gitcito scans for repositories. A preference, not an index. */
export interface RepoScanRoot {
  path: string
  /** How deep to descend. 3 covers ~/Code/<client>/<repo>; deeper gets slow
   *  fast, and a repo nested further is almost always vendored. */
  depth: number
}
```

In the `AppSettings` interface, next to `recentRepos` (line ~2787):

```ts
  /** Folders scanned for repositories by the Repositories page. */
  repoScanRoots: RepoScanRoot[]
  /** Starred repositories, by canonical path. Path-keyed for the same reason
   *  `repoAliases` is: the same folder in two tabs must not diverge. */
  favouriteRepos: string[]
```

In `defaultSettings` (line ~3221, beside `recentRepos: []`):

```ts
    repoScanRoots: [],
    favouriteRepos: [],
```

- [ ] **Step 2: Write the failing test**

Create `test/repoRegistry.test.ts`:

```ts
import { describe, it, expect, beforeEach, afterAll } from 'vitest'
import { rmSync, mkdtempSync } from 'node:fs'
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
```

Add `import { existsSync } from 'node:fs'` to the imports at the top.

- [ ] **Step 3: Run the test and watch it fail**

Run: `npx vitest run test/repoRegistry.test.ts`
Expected: FAIL — cannot resolve `../src/main/repoRegistry`.

- [ ] **Step 4: Write the module**

Create `src/main/repoRegistry.ts`:

```ts
import { app } from 'electron'
import { join, basename } from 'path'
import { readFile, writeFile, mkdir } from 'fs/promises'
import { existsSync } from 'fs'
import type { RegistryRepo } from '../shared/types'
import { isSafeRepoPath } from './aiSchemas'

// Every repository Gitcito knows about, whether or not it is open. Kept in its
// own file rather than in settings: it is a cache of what is on *this* disk,
// while settings are preferences you would carry to another machine. Losing
// this file costs a rescan; losing settings costs information.

interface RegistryData {
  repos: RegistryRepo[]
}

export const registryFilePath = (): string => join(app.getPath('userData'), 'gitcito-repos.json')

async function load(): Promise<RegistryRepo[]> {
  try {
    const raw = await readFile(registryFilePath(), 'utf-8')
    const data = JSON.parse(raw) as RegistryData
    return Array.isArray(data.repos) ? data.repos : []
  } catch {
    return [] // missing or corrupt → start fresh; a rescan rebuilds it
  }
}

async function save(repos: RegistryRepo[]): Promise<void> {
  await mkdir(app.getPath('userData'), { recursive: true })
  const data: RegistryData = { repos }
  await writeFile(registryFilePath(), JSON.stringify(data, null, 2), 'utf-8')
}

/** The registry, with `missing` refreshed. Stat-ing every path is why this is
 *  called on page open and not per render. */
export async function listRepos(): Promise<RegistryRepo[]> {
  const repos = await load()
  let changed = false
  for (const repo of repos) {
    const missing = !existsSync(repo.path)
    if (missing !== repo.missing) {
      repo.missing = missing
      changed = true
    }
  }
  if (changed) await save(repos)
  return repos
}

/** Record a repository as opened. Upserts: the same folder is one entry. */
export async function rememberRepo(repoPath: string): Promise<RegistryRepo[]> {
  if (!isSafeRepoPath(repoPath)) return load()
  const repos = await load()
  const now = Math.floor(Date.now() / 1000)
  const existing = repos.find((r) => r.path === repoPath)
  if (existing) {
    existing.lastOpenedAt = now
    existing.missing = !existsSync(repoPath)
    existing.source = 'opened'
  } else {
    repos.push({
      path: repoPath,
      name: basename(repoPath),
      owner: null,
      branch: null,
      source: 'opened',
      lastOpenedAt: now,
      missing: !existsSync(repoPath)
    })
  }
  await save(repos)
  return repos
}

/** Drop an entry from the index. Never touches the folder on disk. */
export async function forgetRepo(repoPath: string): Promise<RegistryRepo[]> {
  const repos = (await load()).filter((r) => r.path !== repoPath)
  await save(repos)
  return repos
}
```

- [ ] **Step 5: Run the test and watch it pass**

Run: `npx vitest run test/repoRegistry.test.ts`
Expected: PASS, 6 tests.

- [ ] **Step 6: Typecheck and commit**

```bash
npm run typecheck
git add src/shared/types.ts src/main/repoRegistry.ts test/repoRegistry.test.ts
git commit -m "feat: add the repository registry store"
```

---

### Task 2: Reading branch and owner from `.git`

The reason rows are free: metadata comes from file reads, not process spawns.

**Files:**
- Create: `src/main/repoMeta.ts`
- Modify: `src/main/repoRegistry.ts` (use it in `rememberRepo`, add `refreshRepos`)
- Test: `test/repoMeta.test.ts`

**Interfaces:**
- Consumes: `parseRemoteUrl` from `src/main/hosting.ts`
- Produces: `readHeadBranch(repoPath: string): Promise<string | null>`, `ownerFromRemoteUrl(url: string): string | null`, `readOriginOwner(repoPath: string): Promise<string | null>`, `gitDirOf(repoPath: string): Promise<string | null>`; `refreshRepos(paths: string[]): Promise<RegistryRepo[]>`

- [ ] **Step 1: Write the failing test**

Create `test/repoMeta.test.ts`:

```ts
import { describe, it, expect, afterAll } from 'vitest'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { cloneFixture, cleanupFixtures } from './fixtures'
import { readHeadBranch, ownerFromRemoteUrl, readOriginOwner, gitDirOf } from '../src/main/repoMeta'

const tmpDirs: string[] = []

afterAll(() => {
  cleanupFixtures()
  for (const d of tmpDirs) rmSync(d, { recursive: true, force: true })
})

describe('ownerFromRemoteUrl', () => {
  it('reads a GitHub org', () => {
    expect(ownerFromRemoteUrl('https://github.com/acme/widget.git')).toBe('acme')
    expect(ownerFromRemoteUrl('git@github.com:acme/widget.git')).toBe('acme')
  })

  it('takes the first segment of a nested GitLab namespace', () => {
    expect(ownerFromRemoteUrl('https://gitlab.com/top-solution/sub/widget.git')).toBe('top-solution')
  })

  // The reason this function exists rather than parseRemoteUrl alone: a
  // self-hosted GitLab matches none of parseRemoteUrl's known hosts.
  it('falls back to the first path segment on an unknown host', () => {
    expect(ownerFromRemoteUrl('https://git.example.com/top-solution/widget.git')).toBe('top-solution')
    expect(ownerFromRemoteUrl('git@git.example.com:top-solution/widget.git')).toBe('top-solution')
  })

  it('returns null when there is no namespace to read', () => {
    expect(ownerFromRemoteUrl('https://git.example.com/widget.git')).toBeNull()
    expect(ownerFromRemoteUrl('not a url')).toBeNull()
    expect(ownerFromRemoteUrl('')).toBeNull()
  })
})

describe('readHeadBranch', () => {
  it('reads the checked-out branch without spawning git', async () => {
    const dir = cloneFixture('basic')
    expect(await readHeadBranch(dir)).toBe('main')
  })

  it('returns null for a path that is not a repo', async () => {
    expect(await readHeadBranch('/definitely/not/here')).toBeNull()
  })
})

describe('gitDirOf', () => {
  it('resolves a .git directory', async () => {
    const dir = cloneFixture('basic')
    expect(await gitDirOf(dir)).toBe(join(dir, '.git'))
  })

  // A worktree and a submodule both have a .git *file* pointing elsewhere.
  // Reading HEAD from the wrong place is the bug this guards against.
  it('follows a .git file to the real git directory', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'gitcito-wt-'))
    tmpDirs.push(dir)
    const real = mkdtempSync(join(tmpdir(), 'gitcito-wtgit-'))
    tmpDirs.push(real)
    writeFileSync(join(dir, '.git'), `gitdir: ${real}\n`)
    writeFileSync(join(real, 'HEAD'), 'ref: refs/heads/feature\n')
    expect(await gitDirOf(dir)).toBe(real)
    expect(await readHeadBranch(dir)).toBe('feature')
  })

  it('returns null for a detached HEAD', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'gitcito-det-'))
    tmpDirs.push(dir)
    mkdirSync(join(dir, '.git'))
    writeFileSync(join(dir, '.git', 'HEAD'), '9f4a1c2e8b7d6a5f4e3c2b1a0d9e8f7a6b5c4d3e\n')
    expect(await readHeadBranch(dir)).toBeNull()
  })
})

describe('readOriginOwner', () => {
  it('returns null for a repo with no remote', async () => {
    const dir = cloneFixture('basic')
    expect(await readOriginOwner(dir)).toBeNull()
  })
})
```

If the `basic` fixture's default branch is not `main`, or it has an origin, adjust the two assertions to match — run `git -C examples/playground/basic branch --show-current` and `git -C examples/playground/basic remote -v` to check, and load the **`playground-fixture`** skill if a new scenario is needed.

- [ ] **Step 2: Run the test and watch it fail**

Run: `npx vitest run test/repoMeta.test.ts`
Expected: FAIL — cannot resolve `../src/main/repoMeta`.

- [ ] **Step 3: Write the module**

Create `src/main/repoMeta.ts`:

```ts
import { join, isAbsolute, resolve } from 'path'
import { readFile, stat } from 'fs/promises'
import { parseRemoteUrl } from './hosting'

// Repository metadata read straight from the files in .git, never by spawning
// git. A row on the Repositories page costs two file reads; spawning `git
// rev-parse` per row would cost a process, and the page lists everything the
// user has ever opened.

/** The real .git directory: a plain repo has a directory, a worktree or
 *  submodule has a file containing `gitdir: <path>`. Null when neither. */
export async function gitDirOf(repoPath: string): Promise<string | null> {
  const dot = join(repoPath, '.git')
  try {
    const info = await stat(dot)
    if (info.isDirectory()) return dot
  } catch {
    return null
  }
  try {
    const text = await readFile(dot, 'utf-8')
    const m = /^gitdir:\s*(.+)$/m.exec(text)
    if (!m) return null
    const target = m[1].trim()
    return isAbsolute(target) ? target : resolve(repoPath, target)
  } catch {
    return null
  }
}

/** The checked-out branch, or null when detached or unreadable. */
export async function readHeadBranch(repoPath: string): Promise<string | null> {
  const gitDir = await gitDirOf(repoPath)
  if (!gitDir) return null
  try {
    const head = await readFile(join(gitDir, 'HEAD'), 'utf-8')
    const m = /^ref:\s*refs\/heads\/(.+)$/m.exec(head)
    return m ? m[1].trim() : null // a raw sha means detached HEAD
  } catch {
    return null
  }
}

/**
 * The owning namespace of a remote URL: an org, a user, or the top level of a
 * GitLab group path.
 *
 * `parseRemoteUrl` handles the hosts Gitcito integrates with, but returns null
 * for a self-hosted GitLab or Gitea — which is exactly where an owner column
 * earns its keep. The fallback takes the first path segment after the host.
 */
export function ownerFromRemoteUrl(url: string): string | null {
  if (!url) return null
  const known = parseRemoteUrl(url)
  if (known) return known.owner.split('/')[0] || null

  // scp-style: git@host:namespace/repo.git
  let m = /^[^@\s]+@[^:\s]+:(.+)$/.exec(url.trim())
  if (!m) {
    // URL form: scheme://[user@]host/namespace/repo.git
    m = /^[a-z][a-z0-9+.-]*:\/\/(?:[^@/]+@)?[^/]+\/(.+)$/i.exec(url.trim())
  }
  if (!m) return null
  const segments = m[1].split('/').filter(Boolean)
  // One segment is just the repo — there is no namespace to report.
  return segments.length >= 2 ? segments[0] : null
}

/** The owner of `origin`, read from .git/config. */
export async function readOriginOwner(repoPath: string): Promise<string | null> {
  const gitDir = await gitDirOf(repoPath)
  if (!gitDir) return null
  try {
    const config = await readFile(join(gitDir, 'config'), 'utf-8')
    const section = /\[remote "origin"\]([\s\S]*?)(?=\n\[|$)/.exec(config)
    if (!section) return null
    const url = /^\s*url\s*=\s*(.+)$/m.exec(section[1])
    return url ? ownerFromRemoteUrl(url[1].trim()) : null
  } catch {
    return null
  }
}
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `npx vitest run test/repoMeta.test.ts`
Expected: PASS.

- [ ] **Step 5: Use the metadata in the registry**

In `src/main/repoRegistry.ts`, add the import and populate the fields.

Add to imports:

```ts
import { readHeadBranch, readOriginOwner } from './repoMeta'
```

In `rememberRepo`, replace the `if (existing) { … } else { … }` block with:

```ts
  const [branch, owner] = await Promise.all([readHeadBranch(repoPath), readOriginOwner(repoPath)])
  const existing = repos.find((r) => r.path === repoPath)
  if (existing) {
    existing.lastOpenedAt = now
    existing.missing = !existsSync(repoPath)
    existing.source = 'opened'
    existing.branch = branch
    existing.owner = owner
  } else {
    repos.push({
      path: repoPath,
      name: basename(repoPath),
      owner,
      branch,
      source: 'opened',
      lastOpenedAt: now,
      missing: !existsSync(repoPath)
    })
  }
```

Append a refresh function at the end of the module:

```ts
/** Re-read branch for the given paths. Cheap enough to call whenever the page
 *  opens: one file read each, no process spawned. */
export async function refreshRepos(paths: string[]): Promise<RegistryRepo[]> {
  const repos = await load()
  const wanted = new Set(paths)
  await Promise.all(
    repos
      .filter((r) => wanted.has(r.path) && !r.missing)
      .map(async (r) => {
        r.branch = await readHeadBranch(r.path)
      })
  )
  await save(repos)
  return repos
}
```

- [ ] **Step 6: Add a registry test for the populated metadata**

Append to `test/repoRegistry.test.ts`:

```ts
  it('populates branch from .git when remembering a real repo', async () => {
    const dir = cloneFixture('basic')
    await rememberRepo(dir)
    const repos = await listRepos()
    expect(repos[0].branch).toBe('main')
  })
```

Add `import { cloneFixture, cleanupFixtures } from './fixtures'` and `afterAll(() => cleanupFixtures())` to that file.

- [ ] **Step 7: Run both test files and commit**

```bash
npx vitest run test/repoMeta.test.ts test/repoRegistry.test.ts
npm run typecheck
git add src/main/repoMeta.ts src/main/repoRegistry.ts test/repoMeta.test.ts test/repoRegistry.test.ts
git commit -m "feat: read repository branch and owner from .git files"
```

---

### Task 3: The folder scanner

**Files:**
- Create: `src/main/repoScan.ts`
- Modify: `src/main/repoRegistry.ts` (add `scanRoots`)
- Test: `test/repoScan.test.ts`

**Interfaces:**
- Consumes: `gitDirOf` from `src/main/repoMeta.ts`, `isSafeRepoPath` from `src/main/aiSchemas.ts`
- Produces: `scanForRepos(root: string, depth: number): Promise<string[]>`; `scanRoots(roots: RepoScanRoot[]): Promise<RegistryRepo[]>`

- [ ] **Step 1: Write the failing test**

Create `test/repoScan.test.ts`:

```ts
import { describe, it, expect, afterAll } from 'vitest'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { scanForRepos } from '../src/main/repoScan'

const roots: string[] = []

/** A tree of fake repos: a .git directory is all the scanner looks for. */
function tree(): string {
  const root = mkdtempSync(join(tmpdir(), 'gitcito-scan-'))
  roots.push(root)
  const repo = (rel: string): void => {
    mkdirSync(join(root, rel, '.git'), { recursive: true })
    writeFileSync(join(root, rel, '.git', 'HEAD'), 'ref: refs/heads/main\n')
  }
  repo('alpha')
  repo('client/beta')
  repo('client/gamma')
  repo('too/deep/for/us/delta')
  // A vendored repo inside another repo must not be reported separately.
  repo('alpha/vendor/nested')
  // node_modules is skipped outright, however shallow.
  repo('node_modules/evil')
  mkdirSync(join(root, 'plain-folder'), { recursive: true })
  return root
}

afterAll(() => {
  for (const r of roots) rmSync(r, { recursive: true, force: true })
})

describe('scanForRepos', () => {
  it('finds repos down to the given depth', async () => {
    const root = tree()
    const found = await scanForRepos(root, 3)
    expect(found).toContain(join(root, 'alpha'))
    expect(found).toContain(join(root, 'client', 'beta'))
    expect(found).toContain(join(root, 'client', 'gamma'))
  })

  it('does not descend past the depth limit', async () => {
    const root = tree()
    const found = await scanForRepos(root, 3)
    expect(found).not.toContain(join(root, 'too', 'deep', 'for', 'us', 'delta'))
  })

  it('stops at a repository rather than walking into it', async () => {
    const root = tree()
    const found = await scanForRepos(root, 5)
    expect(found).not.toContain(join(root, 'alpha', 'vendor', 'nested'))
  })

  it('skips node_modules', async () => {
    const root = tree()
    const found = await scanForRepos(root, 5)
    expect(found).not.toContain(join(root, 'node_modules', 'evil'))
  })

  it('returns nothing for a root that does not exist', async () => {
    expect(await scanForRepos('/definitely/not/here', 3)).toEqual([])
  })
})
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `npx vitest run test/repoScan.test.ts`
Expected: FAIL — cannot resolve `../src/main/repoScan`.

- [ ] **Step 3: Write the scanner**

Create `src/main/repoScan.ts`:

```ts
import { join } from 'path'
import { readdir } from 'fs/promises'
import type { RepoScanRoot, RegistryRepo } from '../shared/types'
import { gitDirOf } from './repoMeta'

// Walking a developer's home directory naively means a million files. Three
// rules keep a scan to a fraction of a second: stop at a repository (its own
// contents are never interesting), never enter a dot-directory, and skip the
// dependency folders that dwarf everything else.

const SKIP = new Set(['node_modules', 'vendor', 'Pods', 'target', 'dist', 'build', 'out'])

/** Absolute paths of every repository under `root`, to `depth` levels. */
export async function scanForRepos(root: string, depth: number): Promise<string[]> {
  const found: string[] = []
  await walk(root, Math.max(0, depth), found)
  return found
}

async function walk(dir: string, depth: number, found: string[]): Promise<void> {
  if (await gitDirOf(dir)) {
    // A repository's own contents are never scanned: a vendored checkout is
    // part of its parent, not a repo the user is looking for.
    found.push(dir)
    return
  }
  if (depth === 0) return

  let entries: string[]
  try {
    const dirents = await readdir(dir, { withFileTypes: true })
    entries = dirents.filter((d) => d.isDirectory()).map((d) => d.name)
  } catch {
    return // unreadable (permissions, a vanished folder) — not an error
  }

  for (const name of entries) {
    if (name.startsWith('.') || SKIP.has(name)) continue
    await walk(join(dir, name), depth - 1, found)
  }
}
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `npx vitest run test/repoScan.test.ts`
Expected: PASS, 5 tests.

- [ ] **Step 5: Add `scanRoots` to the registry**

In `src/main/repoRegistry.ts`, add the imports:

```ts
import type { RegistryRepo, RepoScanRoot } from '../shared/types'
import { scanForRepos } from './repoScan'
```

and append:

```ts
/** Index every repository under the configured roots. A repo already in the
 *  registry keeps its `source` and `lastOpenedAt` — a scan adds knowledge, it
 *  never demotes a repo the user has actually opened. */
export async function scanRoots(rootList: RepoScanRoot[]): Promise<RegistryRepo[]> {
  const repos = await load()
  const byPath = new Map(repos.map((r) => [r.path, r]))

  for (const root of rootList) {
    if (!isSafeRepoPath(root.path)) continue
    for (const found of await scanForRepos(root.path, root.depth)) {
      const existing = byPath.get(found)
      if (existing) {
        existing.missing = false
        continue
      }
      const [branch, owner] = await Promise.all([readHeadBranch(found), readOriginOwner(found)])
      const entry: RegistryRepo = {
        path: found,
        name: basename(found),
        owner,
        branch,
        source: 'scanned',
        lastOpenedAt: 0,
        missing: false
      }
      repos.push(entry)
      byPath.set(found, entry)
    }
  }

  await save(repos)
  return repos
}
```

- [ ] **Step 6: Add a registry test for scanning and commit**

Append to `test/repoRegistry.test.ts`:

```ts
  it('keeps an opened repo opened when a scan finds it again', async () => {
    const dir = cloneFixture('basic')
    await rememberRepo(dir)
    await scanRoots([{ path: dirname(dir), depth: 2 }])
    const entry = (await listRepos()).find((r) => r.path === dir)
    expect(entry?.source).toBe('opened')
    expect(entry?.lastOpenedAt).toBeGreaterThan(0)
  })
```

Add `scanRoots` to the import from `../src/main/repoRegistry` and `import { dirname } from 'node:path'`.

```bash
npx vitest run test/repoScan.test.ts test/repoRegistry.test.ts
npm run typecheck
git add src/main/repoScan.ts src/main/repoRegistry.ts test/repoScan.test.ts test/repoRegistry.test.ts
git commit -m "feat: scan configured folders for repositories"
```

---

### Task 4: Locate, IPC handlers, and the renderer bridge

Wires the module to the renderer. No UI yet.

**Files:**
- Modify: `src/main/repoRegistry.ts` (add `locateRepo`, `registerRepoRegistryHandlers`)
- Modify: `src/main/index.ts` (call the registration)
- Modify: `src/preload/index.ts` (add the `repos` namespace)
- Modify: `src/renderer/src/infrastructure/api.ts` (add `reposApi`)
- Create: `src/renderer/src/stores/repos.ts`
- Test: `test/repoRegistry.test.ts` (locate case)

**Interfaces:**
- Consumes: `listRepos`, `rememberRepo`, `forgetRepo`, `scanRoots`, `refreshRepos` from Tasks 1–3
- Produces: `locateRepo(oldPath: string, newPath: string): Promise<RegistryRepo[]>`; `reposApi.{list,remember,forget,scan,locate,refresh}`; `useReposStore` with `entries`, `loading`, `scanning`, `load()`, `scan()`, `forget()`, `locate()`

- [ ] **Step 1: Write the failing test for locate**

Append to `test/repoRegistry.test.ts`:

```ts
  it('locates a moved repo, keeping its identity', async () => {
    const dir = tempRepo()
    await rememberRepo(dir)
    const moved = tempRepo()
    await locateRepo(dir, moved)
    const repos = await listRepos()
    expect(repos).toHaveLength(1)
    expect(repos[0].path).toBe(moved)
    expect(repos[0].missing).toBe(false)
  })
```

Add `locateRepo` to the import.

- [ ] **Step 2: Run it and watch it fail**

Run: `npx vitest run test/repoRegistry.test.ts`
Expected: FAIL — `locateRepo` is not exported.

- [ ] **Step 3: Implement locate and the handlers**

Append to `src/main/repoRegistry.ts`:

```ts
/** Re-point a missing entry at the folder it moved to. Favourites and aliases
 *  are path-keyed and live in settings, so the renderer migrates those; this
 *  moves the index entry and refreshes what it caches. */
export async function locateRepo(oldPath: string, newPath: string): Promise<RegistryRepo[]> {
  if (!isSafeRepoPath(newPath)) return load()
  const repos = await load()
  const entry = repos.find((r) => r.path === oldPath)
  if (!entry) return repos
  const [branch, owner] = await Promise.all([readHeadBranch(newPath), readOriginOwner(newPath)])
  entry.path = newPath
  entry.name = basename(newPath)
  entry.branch = branch
  entry.owner = owner
  entry.missing = !existsSync(newPath)
  await save(repos)
  return repos
}

export function registerRepoRegistryHandlers(): void {
  ipcMain.handle('repos:list', () => listRepos())
  ipcMain.handle('repos:remember', (_e, repoPath: string) => rememberRepo(repoPath))
  ipcMain.handle('repos:forget', (_e, repoPath: string) => forgetRepo(repoPath))
  ipcMain.handle('repos:scan', (_e, rootList: RepoScanRoot[]) => scanRoots(rootList))
  ipcMain.handle('repos:locate', (_e, oldPath: string, newPath: string) => locateRepo(oldPath, newPath))
  ipcMain.handle('repos:refresh', (_e, paths: string[]) => refreshRepos(paths))
}
```

Change the electron import at the top of the file to `import { app, ipcMain } from 'electron'`.

- [ ] **Step 4: Run the test and watch it pass**

Run: `npx vitest run test/repoRegistry.test.ts`
Expected: PASS.

- [ ] **Step 5: Register the handlers in main**

In `src/main/index.ts`, alongside the other `register*Handlers()` calls (find them with `grep -n "registerVaultHandlers" src/main/index.ts`), add the import and the call:

```ts
import { registerRepoRegistryHandlers } from './repoRegistry'
```

```ts
  registerRepoRegistryHandlers()
```

- [ ] **Step 6: Add the preload namespace**

In `src/preload/index.ts`, after the `vault` block (around line 200):

```ts
  repos: {
    list: (): Promise<unknown> => ipcRenderer.invoke('repos:list'),
    remember: (repoPath: string): Promise<unknown> => ipcRenderer.invoke('repos:remember', repoPath),
    forget: (repoPath: string): Promise<unknown> => ipcRenderer.invoke('repos:forget', repoPath),
    scan: (roots: unknown): Promise<unknown> => ipcRenderer.invoke('repos:scan', roots),
    locate: (oldPath: string, newPath: string): Promise<unknown> =>
      ipcRenderer.invoke('repos:locate', oldPath, newPath),
    refresh: (paths: string[]): Promise<unknown> => ipcRenderer.invoke('repos:refresh', paths)
  },
```

If `src/preload/index.d.ts` (or an `Api` interface in the preload) declares the shape, add the matching `repos` block there too — `npm run typecheck` will say so.

- [ ] **Step 7: Add the renderer adapter**

In `src/renderer/src/infrastructure/api.ts`, after `vaultApi` (line ~737):

```ts
export const reposApi = {
  list: () => window.api.repos.list() as Promise<RegistryRepo[]>,
  remember: (repoPath: string) => window.api.repos.remember(repoPath) as Promise<RegistryRepo[]>,
  forget: (repoPath: string) => window.api.repos.forget(repoPath) as Promise<RegistryRepo[]>,
  scan: (roots: RepoScanRoot[]) => window.api.repos.scan(roots) as Promise<RegistryRepo[]>,
  locate: (oldPath: string, newPath: string) =>
    window.api.repos.locate(oldPath, newPath) as Promise<RegistryRepo[]>,
  refresh: (paths: string[]) => window.api.repos.refresh(paths) as Promise<RegistryRepo[]>
}
```

Add `RegistryRepo` and `RepoScanRoot` to the existing `import type { … } from '../../../shared/types'` at the top of that file.

- [ ] **Step 8: Add the store slice**

Create `src/renderer/src/stores/repos.ts`:

```ts
import { create } from 'zustand'
import type { RegistryRepo, RepoScanRoot } from '../../../shared/types'
import { reposApi } from '../infrastructure/api'

// The registry, as the Repositories page sees it. Loaded on mount and after any
// mutation; nothing subscribes to it in the background, because nothing outside
// that page needs to know.

interface ReposState {
  entries: RegistryRepo[]
  loading: boolean
  scanning: boolean
  load: () => Promise<void>
  scan: (roots: RepoScanRoot[]) => Promise<void>
  forget: (path: string) => Promise<void>
  locate: (oldPath: string, newPath: string) => Promise<void>
}

export const useReposStore = create<ReposState>((set) => ({
  entries: [],
  loading: false,
  scanning: false,

  load: async () => {
    set({ loading: true })
    try {
      set({ entries: await reposApi.list() })
    } finally {
      set({ loading: false })
    }
  },

  scan: async (roots) => {
    set({ scanning: true })
    try {
      set({ entries: await reposApi.scan(roots) })
    } finally {
      set({ scanning: false })
    }
  },

  forget: async (path) => {
    set({ entries: await reposApi.forget(path) })
  },

  locate: async (oldPath, newPath) => {
    set({ entries: await reposApi.locate(oldPath, newPath) })
  }
}))
```

Check an existing store (e.g. `src/renderer/src/stores/updates.ts`) for the exact `create` import style and match it.

- [ ] **Step 9: Record opens in the registry**

In `src/renderer/src/stores/settings.ts`, in `openRepoTab` (line ~533) and the other place that prepends to `recentRepos` (line ~558), add a fire-and-forget call after the state update so opening a repo indexes it:

```ts
      void reposApi.remember(repo.path)
```

Add `import { reposApi } from '../infrastructure/api'` to that file. Deliberately not awaited: indexing must never delay opening a tab.

- [ ] **Step 10: Verify and commit**

```bash
npm run typecheck
npx vitest run test/repoRegistry.test.ts
git add src/main/repoRegistry.ts src/main/index.ts src/preload src/renderer/src/infrastructure/api.ts src/renderer/src/stores/repos.ts src/renderer/src/stores/settings.ts test/repoRegistry.test.ts
git commit -m "feat: expose the repository registry to the renderer"
```

---

### Task 5: Section assembly

The pure logic, tested before any component exists. This is where the duplication policy lives.

**Files:**
- Create: `src/renderer/src/lib/repoSections.ts`
- Test: `test/pureLogic.test.ts` (append a describe block)

**Interfaces:**
- Consumes: `RegistryRepo` (shared types), `TabState`, `Workspace`, `tabRepos`
- Produces: `buildSections(input: SectionInput): RepoSection[]`, `filterSections(sections: RepoSection[], query: string): RepoSection[]`, types `RepoSection`, `SectionInput`, `SectionKind`, `RepoRow`

- [ ] **Step 1: Write the failing tests**

Append to `test/pureLogic.test.ts` (add the import beside the other lib imports at the top):

```ts
import { buildSections, filterSections } from '../src/renderer/src/lib/repoSections'
```

and the describe block at the end of the file:

```ts
describe('repoSections', () => {
  const repo = (path: string, extra: Partial<RegistryRepo> = {}): RegistryRepo => ({
    path,
    name: path.split('/').pop() ?? path,
    owner: 'top-solution',
    branch: 'main',
    source: 'opened',
    lastOpenedAt: 100,
    missing: false,
    ...extra
  })

  const base = {
    registry: [repo('/r/alpha'), repo('/r/beta'), repo('/r/gamma', { lastOpenedAt: 300 })],
    openPaths: ['/r/alpha'],
    favourites: ['/r/beta'],
    workspaces: [{ id: 'w1', name: 'Collins', tabs: [], activeTabId: null }],
    workspaceRepoPaths: { w1: ['/r/alpha', '/r/gamma'] },
    aliases: {} as Record<string, string>
  }

  it('builds open, favourites, recent, one per workspace, and all', () => {
    const kinds = buildSections(base).map((s) => s.kind)
    expect(kinds).toEqual(['open', 'favourites', 'recent', 'workspace', 'all'])
  })

  // The GitKraken behaviour, chosen deliberately: each section is a complete
  // answer to its own question. "What is open?" must not omit an open repo
  // because that repo also happens to be starred.
  it('lists a repo in every section it qualifies for', () => {
    const sections = buildSections(base)
    const paths = (kind: string): string[] =>
      sections.filter((s) => s.kind === kind).flatMap((s) => s.rows.map((r) => r.repo.path))
    expect(paths('open')).toContain('/r/alpha')
    expect(paths('workspace')).toContain('/r/alpha')
    expect(paths('all')).toContain('/r/alpha')
  })

  it('orders recent by lastOpenedAt, newest first', () => {
    const recent = buildSections(base).find((s) => s.kind === 'recent')
    expect(recent?.rows[0].repo.path).toBe('/r/gamma')
  })

  it('never shows a repo with lastOpenedAt 0 in recent', () => {
    const sections = buildSections({
      ...base,
      registry: [...base.registry, repo('/r/delta', { source: 'scanned', lastOpenedAt: 0 })]
    })
    const recent = sections.find((s) => s.kind === 'recent')
    expect(recent?.rows.map((r) => r.repo.path)).not.toContain('/r/delta')
    const all = sections.find((s) => s.kind === 'all')
    expect(all?.rows.map((r) => r.repo.path)).toContain('/r/delta')
  })

  it('marks favourite rows so the star renders filled', () => {
    const all = buildSections(base).find((s) => s.kind === 'all')
    expect(all?.rows.find((r) => r.repo.path === '/r/beta')?.favourite).toBe(true)
    expect(all?.rows.find((r) => r.repo.path === '/r/alpha')?.favourite).toBe(false)
  })

  it('uses the alias as the display name when one is set', () => {
    const sections = buildSections({ ...base, aliases: { '/r/alpha': 'The Alpha' } })
    const all = sections.find((s) => s.kind === 'all')
    expect(all?.rows.find((r) => r.repo.path === '/r/alpha')?.label).toBe('The Alpha')
  })

  it('filters within each section and keeps empty sections so they can say so', () => {
    const filtered = filterSections(buildSections(base), 'beta')
    const open = filtered.find((s) => s.kind === 'open')
    const favourites = filtered.find((s) => s.kind === 'favourites')
    expect(open?.rows).toHaveLength(0)
    expect(favourites?.rows.map((r) => r.repo.path)).toEqual(['/r/beta'])
  })

  it('matches the filter against alias, name, owner and path', () => {
    const sections = buildSections({ ...base, aliases: { '/r/alpha': 'The Alpha' } })
    expect(filterSections(sections, 'the alpha').find((s) => s.kind === 'all')?.rows).toHaveLength(1)
    expect(filterSections(sections, 'top-solution').find((s) => s.kind === 'all')?.rows).toHaveLength(3)
    expect(filterSections(sections, '/r/gamma').find((s) => s.kind === 'all')?.rows).toHaveLength(1)
  })

  it('returns an empty section rather than omitting a workspace with no repos', () => {
    const sections = buildSections({ ...base, workspaceRepoPaths: { w1: [] } })
    const ws = sections.find((s) => s.kind === 'workspace')
    expect(ws).toBeDefined()
    expect(ws?.rows).toHaveLength(0)
  })
})
```

Add `RegistryRepo` to the existing `import type { … } from '../src/shared/types'` line in that test file.

- [ ] **Step 2: Run the tests and watch them fail**

Run: `npx vitest run test/pureLogic.test.ts -t repoSections`
Expected: FAIL — cannot resolve `repoSections`.

- [ ] **Step 3: Write the module**

Create `src/renderer/src/lib/repoSections.ts`:

```ts
import type { RegistryRepo, Workspace } from '../../../shared/types'

// Assembling the Repositories page. Pure: the page hands in the registry and
// what it knows about tabs and workspaces, and gets back the sections to draw.

export type SectionKind = 'open' | 'favourites' | 'recent' | 'workspace' | 'all'

export interface RepoRow {
  repo: RegistryRepo
  /** Alias if one is set, else the folder name — what the row displays. */
  label: string
  favourite: boolean
}

export interface RepoSection {
  kind: SectionKind
  /** Set only for `workspace` sections: which workspace this is. */
  workspaceId?: string
  /** Set only for `workspace` sections: its name, which is user data, not copy. */
  workspaceName?: string
  rows: RepoRow[]
}

export interface SectionInput {
  registry: RegistryRepo[]
  /** Paths open in a tab in the active workspace. */
  openPaths: string[]
  favourites: string[]
  workspaces: Workspace[]
  /** Repo paths per workspace id — the caller flattens tabs, since only it
   *  knows how a workspace's tabs are stored. */
  workspaceRepoPaths: Record<string, string[]>
  aliases: Record<string, string>
}

function rowsFor(paths: string[], input: SectionInput): RepoRow[] {
  const byPath = new Map(input.registry.map((r) => [r.path, r]))
  const favourites = new Set(input.favourites)
  const rows: RepoRow[] = []
  for (const path of paths) {
    const repo = byPath.get(path)
    if (!repo) continue // open but never indexed: the next `remember` fixes it
    rows.push({ repo, label: input.aliases[path] || repo.name, favourite: favourites.has(path) })
  }
  return rows
}

function byLabel(a: RepoRow, b: RepoRow): number {
  return a.label.localeCompare(b.label)
}

/**
 * The page's sections, in display order.
 *
 * A repository appears in **every** section it qualifies for — open, starred
 * and in two workspaces means four rows. Each section is then a complete answer
 * to its own question, which is what makes them independently readable; the
 * cost is a longer page, which collapsing and the filter address.
 */
export function buildSections(input: SectionInput): RepoSection[] {
  const sections: RepoSection[] = []

  sections.push({ kind: 'open', rows: rowsFor(input.openPaths, input).sort(byLabel) })
  sections.push({ kind: 'favourites', rows: rowsFor(input.favourites, input).sort(byLabel) })

  // Recent is the registry ordered by when it was last opened. A scanned repo
  // the user has never opened has no place here, however recently it was found.
  const recent = input.registry
    .filter((r) => r.lastOpenedAt > 0)
    .sort((a, b) => b.lastOpenedAt - a.lastOpenedAt)
    .map((r) => r.path)
  sections.push({ kind: 'recent', rows: rowsFor(recent, input) })

  for (const ws of input.workspaces) {
    sections.push({
      kind: 'workspace',
      workspaceId: ws.id,
      workspaceName: ws.name,
      rows: rowsFor(input.workspaceRepoPaths[ws.id] ?? [], input).sort(byLabel)
    })
  }

  sections.push({ kind: 'all', rows: rowsFor(input.registry.map((r) => r.path), input).sort(byLabel) })
  return sections
}

/** Apply the search box. Sections that match nothing are kept, empty, so the
 *  page can say "no matches" there rather than silently losing a heading. */
export function filterSections(sections: RepoSection[], query: string): RepoSection[] {
  const q = query.trim().toLowerCase()
  if (!q) return sections
  const matches = (row: RepoRow): boolean =>
    row.label.toLowerCase().includes(q) ||
    row.repo.name.toLowerCase().includes(q) ||
    (row.repo.owner?.toLowerCase().includes(q) ?? false) ||
    row.repo.path.toLowerCase().includes(q)
  return sections.map((s) => ({ ...s, rows: s.rows.filter(matches) }))
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `npx vitest run test/pureLogic.test.ts -t repoSections`
Expected: PASS, 9 tests.

- [ ] **Step 5: Commit**

```bash
npm run typecheck
git add src/renderer/src/lib/repoSections.ts test/pureLogic.test.ts
git commit -m "feat: assemble the repositories page sections"
```

---

### Task 6: The page itself

Cheap rows only — no status, no favourites yet. Those are Tasks 7 and 8.

**Files:**
- Modify: `src/shared/types.ts` (`PageContent` gains `repositories`)
- Modify: `src/renderer/src/App.tsx` (`PageView` case)
- Modify: `src/renderer/src/lib/tabLabel.ts` (`pageTabLabel` case)
- Modify: `src/renderer/src/components/TitleBar.tsx` (`pageTabIcon` case)
- Create: `src/renderer/src/components/RepositoriesPage.tsx`
- Modify: the renderer stylesheet holding the mission-control rules (find it with `grep -rln "mc-spark" src/renderer/src`)

**Interfaces:**
- Consumes: `useReposStore` (Task 4), `buildSections` / `filterSections` (Task 5)
- Produces: `RepositoriesPage(): React.JSX.Element`; `{ type: 'repositories' }` page content

- [ ] **Step 1: Add the page type**

In `src/shared/types.ts`, in the `PageContent` union (line ~2662), beside `{ type: 'vault' }`:

```ts
  | { type: 'repositories' }
```

`npm run typecheck` will now fail in three switch statements. That is the point — the compiler lists exactly what has to be wired.

- [ ] **Step 2: Run typecheck to see the wiring list**

Run: `npm run typecheck`
Expected: FAIL, naming `tabLabel.ts` and `App.tsx`.

- [ ] **Step 3: Add the i18n keys for the page shell**

Add to **every** locale file in `src/renderer/src/i18n/` (16 of them), translated. The English reference values:

```ts
  'tab.repositories': 'Repositories',
  'repos.title': 'Repositories',
  'repos.open': 'Repositories',
  'repos.search': 'Search repositories…',
  'repos.collapseAll': 'Collapse all',
  'repos.expandAll': 'Expand all',
  'repos.openFolder': 'Open folder…',
  'repos.clone': 'Clone…',
  'repos.addScanRoot': 'Add scan folder…',
  'repos.scanning': 'Scanning…',
  'repos.sectionOpen': 'Open repositories',
  'repos.sectionFavourites': 'Favourites',
  'repos.sectionRecent': 'Recent repositories',
  'repos.sectionAll': 'All repositories',
  'repos.noMatches': 'No matches',
  'repos.emptySection': 'Nothing here yet.',
  'repos.empty': 'No repositories yet. Open or clone one to get started.',
  'repos.openInTab': 'Open in a tab',
  'repos.noOwner': '—',
```

Load the **`translations`** skill before doing this. Do not paste English into the other 15 files.

- [ ] **Step 4: Wire the label, icon and route**

`src/renderer/src/lib/tabLabel.ts`, in `pageTabLabel`:

```ts
    case 'repositories':
      return t('tab.repositories')
```

`src/renderer/src/components/TitleBar.tsx`, in `pageTabIcon` (import `FolderGit2` from `lucide-react` if it is not already imported there):

```ts
    case 'repositories':
      return <FolderGit2 size={13} />
```

`src/renderer/src/App.tsx`, in `PageView` beside `case 'vault':`:

```ts
    case 'repositories':
      return <RepositoriesPage />
```

with `import { RepositoriesPage } from './components/RepositoriesPage'` beside the other component imports.

- [ ] **Step 5: Write the component**

Create `src/renderer/src/components/RepositoriesPage.tsx`:

```tsx
import { useEffect, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, FolderGit2, GitBranch, Search } from 'lucide-react'
import { useSettingsStore } from '../stores/settings'
import { useReposStore } from '../stores/repos'
import { buildSections, filterSections, type RepoSection, type SectionKind } from '../lib/repoSections'
import { tabRepos } from '../../../shared/types'
import { useT, type TranslationKey } from '../i18n'

/** Section headings live here as keys, not strings: a module-level constant
 *  holding translated text freezes at whatever language was active on import. */
const SECTION_TITLE: Record<Exclude<SectionKind, 'workspace'>, TranslationKey> = {
  open: 'repos.sectionOpen',
  favourites: 'repos.sectionFavourites',
  recent: 'repos.sectionRecent',
  all: 'repos.sectionAll'
}

function sectionKey(section: RepoSection): string {
  return section.kind === 'workspace' ? `workspace:${section.workspaceId}` : section.kind
}

/**
 * The Repositories page — every repository Gitcito knows about, whether or not
 * it is open, grouped into sections you can collapse.
 *
 * Rows are deliberately cheap: name, owner and branch come from the registry,
 * which read them from files in `.git`. Nothing here spawns a git process, and
 * nothing refreshes on a timer — this is a page you open to find something.
 */
export function RepositoriesPage(): React.JSX.Element {
  const t = useT()
  const entries = useReposStore((s) => s.entries)
  const loading = useReposStore((s) => s.loading)
  const load = useReposStore((s) => s.load)
  const settings = useSettingsStore((s) => s.settings)
  const openRepoTab = useSettingsStore((s) => s.openRepoTab)

  const [query, setQuery] = useState('')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  useEffect(() => {
    void load()
  }, [load])

  const sections = useMemo(() => {
    const workspaceRepoPaths: Record<string, string[]> = {}
    for (const ws of settings.workspaces ?? []) {
      workspaceRepoPaths[ws.id] = ws.tabs.flatMap((tab) => tabRepos(tab).map((r) => r.path))
    }
    return filterSections(
      buildSections({
        registry: entries,
        openPaths: settings.tabs.flatMap((tab) => tabRepos(tab).map((r) => r.path)),
        favourites: settings.favouriteRepos ?? [],
        workspaces: settings.workspaces ?? [],
        workspaceRepoPaths,
        aliases: settings.repoAliases ?? {}
      }),
      query
    )
  }, [entries, settings, query])

  const toggle = (key: string): void => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  return (
    <div className="repos-page">
      <header className="repos-header">
        <h1 className="repos-title">
          <FolderGit2 size={16} /> {t('repos.title')}
        </h1>
        <div className="repos-search">
          <Search size={13} />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('repos.search')}
            aria-label={t('repos.search')}
          />
        </div>
        <button className="repos-btn" onClick={() => setCollapsed(new Set())}>
          {t('repos.expandAll')}
        </button>
        <button
          className="repos-btn"
          onClick={() => setCollapsed(new Set(sections.map(sectionKey)))}
        >
          {t('repos.collapseAll')}
        </button>
      </header>

      {!loading && entries.length === 0 ? (
        <p className="repos-empty">{t('repos.empty')}</p>
      ) : (
        <div className="repos-sections">
          {sections.map((section) => {
            const key = sectionKey(section)
            const isCollapsed = collapsed.has(key)
            const title =
              section.kind === 'workspace'
                ? (section.workspaceName ?? '')
                : t(SECTION_TITLE[section.kind])
            return (
              <section className="repos-section" key={key}>
                <button className="repos-section-head" onClick={() => toggle(key)} aria-expanded={!isCollapsed}>
                  {isCollapsed ? <ChevronRight size={13} /> : <ChevronDown size={13} />}
                  <span className="repos-section-title">{title}</span>
                  <span className="repos-section-count">{section.rows.length}</span>
                </button>
                {!isCollapsed && (
                  <div className="repos-rows">
                    {section.rows.length === 0 ? (
                      <p className="repos-none">{query ? t('repos.noMatches') : t('repos.emptySection')}</p>
                    ) : (
                      section.rows.map((row) => (
                        <button
                          className="repos-row"
                          key={row.repo.path}
                          title={row.repo.path}
                          onClick={() => openRepoTab({ path: row.repo.path, name: row.repo.name })}
                        >
                          <span className="repos-row-name">{row.label}</span>
                          <span className="repos-row-owner">{row.repo.owner ?? t('repos.noOwner')}</span>
                          <span className="repos-row-branch">
                            <GitBranch size={11} /> {row.repo.branch ?? ''}
                          </span>
                        </button>
                      ))
                    )}
                  </div>
                )}
              </section>
            )
          })}
        </div>
      )}
    </div>
  )
}
```

- [ ] **Step 6: Add the styles**

Find the stylesheet holding the mission-control rules: `grep -rln "mc-spark" src/renderer/src`. Add the `repos-*` rules beside them, using **that file's own CSS variables** — read the `.mc-*` rules first and reuse the same custom properties for colours, borders and spacing rather than hard-coding values. The structure to style:

```css
.repos-page { display: flex; flex-direction: column; height: 100%; overflow: auto; }
.repos-header { display: flex; align-items: center; gap: 8px; padding: 10px 14px; }
.repos-title { display: flex; align-items: center; gap: 6px; font-size: 14px; font-weight: 600; margin: 0; }
.repos-search { display: flex; align-items: center; gap: 5px; margin-left: auto; }
.repos-section-head { display: flex; align-items: center; gap: 6px; width: 100%; }
.repos-section-count { margin-left: auto; opacity: 0.6; }
.repos-row { display: flex; align-items: center; gap: 8px; }
.repos-row-open { display: flex; align-items: center; gap: 12px; flex: 1; text-align: left; }
.repos-row-name { flex: 0 0 auto; }
.repos-row-owner { opacity: 0.65; }
.repos-row-branch, .repos-row-wip { display: flex; align-items: center; gap: 4px; margin-left: auto; opacity: 0.8; }
.repos-row-missing { opacity: 0.55; }
.repos-star { opacity: 0.5; }
.repos-star[aria-pressed='true'] { opacity: 1; }
```

Fill in colours, hover states and borders from the neighbouring `.mc-*` rules so the two pages read as one app.

- [ ] **Step 7: Verify and commit**

```bash
npm run typecheck
npm run lint:i18n
npx vitest run test/i18n.test.ts
git add src/shared/types.ts src/renderer/src
git commit -m "feat: add the repositories page"
```

---

### Task 7: Favourites, forget, locate and the missing state

**Files:**
- Modify: `src/renderer/src/stores/settings.ts` (add `toggleFavouriteRepo`)
- Modify: `src/renderer/src/components/RepositoriesPage.tsx` (star, row menu, missing row)
- Modify: the renderer stylesheet (missing-row styles)
- Test: `test/pureLogic.test.ts`

**Interfaces:**
- Consumes: `useReposStore.forget/locate` (Task 4), `RepoRow.favourite` (Task 5)
- Produces: `toggleFavouriteRepo(path: string): void` on the settings store

- [ ] **Step 1: Write the failing test for the favourites toggle**

The toggle is small but has one case worth pinning: starring a repo that is already starred must not duplicate it. Append to the `repoSections` describe block in `test/pureLogic.test.ts`:

```ts
  it('does not duplicate a repo starred twice', () => {
    const sections = buildSections({ ...base, favourites: ['/r/beta', '/r/beta'] })
    const favourites = sections.find((s) => s.kind === 'favourites')
    expect(favourites?.rows).toHaveLength(1)
  })
```

- [ ] **Step 2: Run it and watch it fail**

Run: `npx vitest run test/pureLogic.test.ts -t 'does not duplicate'`
Expected: FAIL — two rows.

- [ ] **Step 3: De-duplicate in `rowsFor`**

In `src/renderer/src/lib/repoSections.ts`, change the loop in `rowsFor` to skip paths it has already emitted:

```ts
function rowsFor(paths: string[], input: SectionInput): RepoRow[] {
  const byPath = new Map(input.registry.map((r) => [r.path, r]))
  const favourites = new Set(input.favourites)
  const seen = new Set<string>()
  const rows: RepoRow[] = []
  for (const path of paths) {
    if (seen.has(path)) continue // the same folder twice is still one repository
    seen.add(path)
    const repo = byPath.get(path)
    if (!repo) continue // open but never indexed: the next `remember` fixes it
    rows.push({ repo, label: input.aliases[path] || repo.name, favourite: favourites.has(path) })
  }
  return rows
}
```

Note this also fixes a repo opened in two tabs of the same workspace appearing twice in the Open section.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `npx vitest run test/pureLogic.test.ts -t repoSections`
Expected: PASS, 10 tests.

- [ ] **Step 5: Add the settings action**

In `src/renderer/src/stores/settings.ts`, add to the store interface and implementation, following the shape of the neighbouring actions:

```ts
  toggleFavouriteRepo: (path: string) => void
```

```ts
  toggleFavouriteRepo: (path) => {
    update((s) => {
      const current = s.favouriteRepos ?? []
      const next = current.includes(path) ? current.filter((p) => p !== path) : [...current, path]
      return { ...s, favouriteRepos: next }
    })
  },
```

- [ ] **Step 6: Add the i18n keys**

To all 16 locale files, translated:

```ts
  'repos.star': 'Add to favourites',
  'repos.unstar': 'Remove from favourites',
  'repos.forget': 'Forget this repository',
  'repos.forgetConfirm': 'Remove {name} from the list? The folder on disk is not touched.',
  'repos.forgetAction': 'Forget',
  'repos.missing': 'Missing',
  'repos.locate': 'Locate…',
  'repos.locateTitle': 'Where is {name}?',
```

- [ ] **Step 7: Wire the row affordances**

In `RepositoriesPage.tsx`, add these imports:

```tsx
import { AlertTriangle, Star } from 'lucide-react'
import { useUIStore } from '../stores/ui'
import { repositoryMenuItems } from '../lib/repositoryMenuItems'
import { shellApi } from '../infrastructure/api'
import { useT, interp, type TranslationKey } from '../i18n'
```

Add the handlers inside the component, above the `return`:

```tsx
  const forget = useReposStore((s) => s.forget)
  const locate = useReposStore((s) => s.locate)
  const toggleFavouriteRepo = useSettingsStore((s) => s.toggleFavouriteRepo)
  const repathRepo = useSettingsStore((s) => s.repathRepo)
  const openModal = useUIStore((s) => s.openModal)
  const openContextMenu = useUIStore((s) => s.openContextMenu)

  // "Forget" sits next to a repository name, where it reads as "delete". The
  // confirm says what it does and does not do, rather than relying on the verb.
  const confirmForget = (path: string, label: string): void => {
    openModal({
      kind: 'confirm',
      title: t('repos.forget'),
      message: interp(t('repos.forgetConfirm'), { name: label }),
      confirmLabel: t('repos.forgetAction'),
      onConfirm: () => void forget(path)
    })
  }

  const runLocate = async (path: string, label: string): Promise<void> => {
    const chosen = await shellApi.selectDirectory(interp(t('repos.locateTitle'), { name: label }))
    if (!chosen) return
    await locate(path, chosen)
    // The registry moved; the star, alias and profile binding are keyed by path
    // in settings and have to move with it.
    repathRepo(path, chosen)
  }
```

Check `shellApi`'s directory-picker method name in `api.ts` (`grep -n "selectDirectory" src/renderer/src/infrastructure/api.ts`) and use whatever it is actually called — the same one `openRepositoryDialog` uses in `appCommands.ts`. Do not add a second picker.

Replace the row `<button>` in the section body with:

```tsx
                      section.rows.map((row) => (
                        <div
                          className={`repos-row${row.repo.missing ? ' repos-row-missing' : ''}`}
                          key={row.repo.path}
                          title={row.repo.path}
                          onContextMenu={(e) => {
                            e.preventDefault()
                            e.stopPropagation()
                            openContextMenu(
                              e.clientX,
                              e.clientY,
                              repositoryMenuItems(row.repo.path, () => confirmForget(row.repo.path, row.label), [
                                {
                                  label: t(row.favourite ? 'repos.unstar' : 'repos.star'),
                                  onClick: () => toggleFavouriteRepo(row.repo.path)
                                },
                                ...(row.repo.missing
                                  ? [{ label: t('repos.locate'), onClick: () => void runLocate(row.repo.path, row.label) }]
                                  : [])
                              ])
                            )
                          }}
                        >
                          <button
                            className="repos-star"
                            aria-pressed={row.favourite}
                            title={t(row.favourite ? 'repos.unstar' : 'repos.star')}
                            onClick={(e) => {
                              e.stopPropagation()
                              toggleFavouriteRepo(row.repo.path)
                            }}
                          >
                            <Star size={12} fill={row.favourite ? 'currentColor' : 'none'} />
                          </button>
                          <button
                            className="repos-row-open"
                            disabled={row.repo.missing}
                            title={t('repos.openInTab')}
                            onClick={() => openRepoTab({ path: row.repo.path, name: row.repo.name })}
                          >
                            <span className="repos-row-name">{row.label}</span>
                            <span className="repos-row-owner">{row.repo.owner ?? t('repos.noOwner')}</span>
                            {row.repo.missing ? (
                              <span className="repos-row-missing-tag">
                                <AlertTriangle size={11} /> {t('repos.missing')}
                              </span>
                            ) : (
                              <span className="repos-row-branch">
                                <GitBranch size={11} /> {row.repo.branch ?? ''}
                              </span>
                            )}
                          </button>
                          {row.repo.missing && (
                            <span className="repos-row-fix">
                              <button onClick={() => void runLocate(row.repo.path, row.label)}>
                                {t('repos.locate')}
                              </button>
                              <button onClick={() => confirmForget(row.repo.path, row.label)}>
                                {t('repos.forgetAction')}
                              </button>
                            </span>
                          )}
                        </div>
                      ))
```

Reusing `repositoryMenuItems` with its `extras` argument is the point: the repository menu is already identical on five surfaces, and a sixth that differed would be a papercut on all of them. Check the `MenuItem` shape in `src/renderer/src/lib/repositoryMenuItems.ts` and match it — the `extras` entries above assume `{ label, onClick }`.

Then migrate the path-keyed settings so the star and alias follow a moved repo — in `settings.ts`, add:

```ts
  /** A repo that moved keeps its alias, profile binding and star: all three are
   *  keyed by path, so re-pointing the registry has to re-key them too. */
  repathRepo: (oldPath: string, newPath: string) => void
```

```ts
  repathRepo: (oldPath, newPath) => {
    update((s) => {
      const aliases = { ...s.repoAliases }
      if (aliases[oldPath]) {
        aliases[newPath] = aliases[oldPath]
        delete aliases[oldPath]
      }
      const repoProfiles = { ...s.repoProfiles }
      if (repoProfiles[oldPath]) {
        repoProfiles[newPath] = repoProfiles[oldPath]
        delete repoProfiles[oldPath]
      }
      const favouriteRepos = (s.favouriteRepos ?? []).map((p) => (p === oldPath ? newPath : p))
      return { ...s, repoAliases: aliases, repoProfiles, favouriteRepos }
    })
  },
```

The confirm modal shape matches `WorkspaceSwitcher.tsx:103` — verify the field names there (`kind`, `title`, `message`, `confirmLabel`, `onConfirm`) still match before relying on the code above.

- [ ] **Step 8: Verify and commit**

```bash
npm run typecheck
npm run lint:i18n
npx vitest run test/pureLogic.test.ts test/i18n.test.ts
git add src/renderer/src
git commit -m "feat: star, forget and locate repositories"
```

---

### Task 8: Scan roots and the WIP summary

**Files:**
- Modify: `src/renderer/src/components/RepositoriesPage.tsx` (action bar, WIP toggle)
- Modify: `src/renderer/src/components/SettingsPanel.tsx` (scan roots list)
- Modify: the renderer stylesheet

**Interfaces:**
- Consumes: `useReposStore.scan` (Task 4), `gitApi.repoPulse` (existing), `settings.repoScanRoots` (Task 1)
- Produces: nothing later tasks depend on

- [ ] **Step 1: Add the i18n keys**

To all 16 locale files, translated:

```ts
  'repos.wip': 'WIP summary',
  'repos.wipTitle': 'Show uncommitted work and sync state for expanded sections',
  'repos.scanRoots': 'Scanned folders',
  'repos.scanRootsHint': 'Folders Gitcito searches for repositories.',
  'repos.scanDepth': 'Depth',
  'repos.removeScanRoot': 'Stop scanning this folder',
  'repos.scanNow': 'Scan now',
  'repos.scanFound': 'Found {n} repositories.',
  'repos.clean': 'Clean',
```

- [ ] **Step 2: Add the action bar**

In `RepositoriesPage.tsx`, add a row above the sections with three buttons:

- **Open folder…** — call the same helper `appCommands.ts` uses for `open-repository`. Export it from `appCommands.ts` if it is not already exported, rather than duplicating the dialog call.
- **Clone…** — `useUIStore.getState().openModal({ kind: 'clone', onClone: (repo) => openRepoTab(repo) })`, matching `appCommands.ts:110` exactly.
- **Add scan folder…** — pick a directory, append `{ path, depth: 3 }` to `settings.repoScanRoots` via the settings store, then call `scan(roots)` and toast `interp(t('repos.scanFound'), { n })` with the resulting count.

While `scanning` is true, the button shows `t('repos.scanning')` and is disabled — the first scan of a deep tree takes a visible moment, and a frozen page with no explanation is the failure mode to avoid.

- [ ] **Step 3: Add the WIP summary toggle**

A checkbox in the header bound to local state (not persisted — it is a per-visit choice), labelled `t('repos.wip')` with `title={t('repos.wipTitle')}`.

Add the state and the effect:

```tsx
  const [wip, setWip] = useState(false)
  const [pulses, setPulses] = useState<Record<string, RepoPulse>>({})

  // Status is opt-in because it is expensive: repoPulse spawns roughly five git
  // processes per repository, and this page can list every repo on the machine.
  // Only expanded sections are fetched, only once per visit, and never on a
  // timer — you open this page to find something, not to watch it.
  useEffect(() => {
    if (!wip) return
    let cancelled = false
    const wanted = [
      ...new Set(
        sections
          .filter((s) => !collapsed.has(sectionKey(s)))
          .flatMap((s) => s.rows)
          .filter((r) => !r.repo.missing)
          .map((r) => r.repo.path)
      )
    ].filter((p) => !(p in pulses))

    void (async () => {
      for (let i = 0; i < wanted.length; i += 8) {
        if (cancelled) return
        const batch = wanted.slice(i, i + 8)
        const results = await Promise.all(
          batch.map((p) => gitApi.repoPulse(p).catch(() => null))
        )
        if (cancelled) return
        setPulses((prev) => {
          const next = { ...prev }
          batch.forEach((p, n) => {
            const pulse = results[n]
            if (pulse) next[p] = pulse
          })
          return next
        })
      }
    })()

    return () => {
      cancelled = true
    }
  }, [wip, sections, collapsed, pulses])
```

Import `gitApi` from `../infrastructure/api` and `type RepoPulse` from `../../../shared/types`.

Render the pills inside `repos-row-open`, after the branch, when a pulse exists:

```tsx
                            {wip && pulses[row.repo.path] && (
                              <span className="repos-row-wip">
                                {pulses[row.repo.path].ahead > 0 && <span>↑{pulses[row.repo.path].ahead}</span>}
                                {pulses[row.repo.path].behind > 0 && <span>↓{pulses[row.repo.path].behind}</span>}
                                {dirtyCount(pulses[row.repo.path]) > 0 ? (
                                  <span>●{dirtyCount(pulses[row.repo.path])}</span>
                                ) : (
                                  pulses[row.repo.path].ahead === 0 &&
                                  pulses[row.repo.path].behind === 0 && <span>{t('repos.clean')}</span>
                                )}
                              </span>
                            )}
```

with this helper at module level, beside `sectionKey`:

```tsx
/** Uncommitted work of any kind — staged, unstaged or untracked. */
function dirtyCount(pulse: RepoPulse): number {
  return pulse.staged + pulse.unstaged + pulse.untracked
}
```

The arrows and dot are symbols, not prose, and read the same in every language — no i18n key, and no `i18n-ignore` needed since they are not word content. If `npm run lint:i18n` disagrees, add `// i18n-ignore sync symbols, not copy` rather than inventing keys for them.

- [ ] **Step 4: Add the settings section**

In `SettingsPanel.tsx`, near the existing repository-related settings (the "clear recent repositories" control at line ~3273 is a good neighbour), add a **Scanned folders** section: the list of `repoScanRoots` with each path, a depth number input, a remove button (`t('repos.removeScanRoot')`), an add button, and a **Scan now** button. The section header is `t('repos.scanRoots')` with `t('repos.scanRootsHint')` beneath.

- [ ] **Step 5: Verify and commit**

```bash
npm run typecheck
npm run lint:i18n
npx vitest run test/i18n.test.ts
git add src/renderer/src
git commit -m "feat: scan folders and show working state on the repositories page"
```

---

### Task 9: Entry points, docs, and the screenshot

The task that makes the feature discoverable and documented. CLAUDE.md §8: a user-facing change is not finished until the handbook says so.

**Files:**
- Modify: `src/renderer/src/components/WorkspaceSwitcher.tsx` (the button)
- Modify: `src/renderer/src/components/CommandPalette.tsx` (the palette entry)
- Modify: `src/renderer/src/appCommands.ts` (the command case)
- Modify: `src/renderer/src/lib/shortcuts.ts` (a rebindable binding)
- Create: `docs/help/repositories.md`
- Modify: `scripts/docs-map.json`, `README.md`, `docs/help/workspaces.md`, `docs/help/mission-control.md`
- Modify: `examples/screenshots/shots.config.mjs`

**Interfaces:**
- Consumes: everything above
- Produces: nothing

- [ ] **Step 1: Add the i18n keys**

To all 16 locale files, translated:

```ts
  'cmd.repositories': 'Repositories',
  'sc.openRepositories': 'Open repositories page',
```

- [ ] **Step 2: Add the title-bar button**

In `WorkspaceSwitcher.tsx`, after the mission-control button (line ~162), add a sibling styled the same way:

```tsx
        <button
          className="workspace-mission"
          title={t('repos.open')}
          aria-label={t('repos.open')}
          onClick={() => useSettingsStore.getState().openPageTab({ type: 'repositories' })}
        >
          <FolderGit2 size={14} />
        </button>
```

Import `FolderGit2` from `lucide-react` there. Unlike the gauge it takes no `aria-pressed` — it opens a tab rather than toggling an overlay.

- [ ] **Step 3: Add the command and palette entry**

`appCommands.ts`, beside `case 'vault':`:

```ts
    case 'repositories':
      st.openPageTab({ type: 'repositories' })
      return true
```

`CommandPalette.tsx`, beside the `vault` entry (line 286), matching its formatting exactly:

```tsx
      { id: 'repositories', title: t('cmd.repositories'), group: 'Actions', keywords: 'repositories repos all known registry favourites recent browse find open workspace', icon: <FolderGit2 size={15} />, run: act(() => useSettingsStore.getState().openPageTab({ type: 'repositories' })) },
```

`shortcuts.ts`, following the `vault` entry at line 31, add a `repositories` binding with a default combo that `isReservedCombo` accepts and no existing binding claims — verify with `npx vitest run test/pureLogic.test.ts -t shortcuts` after adding.

- [ ] **Step 4: Write the handbook page**

Create `docs/help/repositories.md`. Front matter is required:

```md
---
title: Repositories
category: Sync & many repos
order: 52
summary: Every repository Gitcito knows about, open or not, in one searchable list.
keywords: repositories registry all repos favourites starred recent scan folder browse find open manage repository management
---
```

The page must, per CLAUDE.md's rules on writing pages:

- Open with **the problem**: mission control shows the repos you have open; this shows the ones you do not. Say which page answers which question, and link both ways.
- Explain the sections, including that a repository appears in every section it qualifies for, and why.
- Explain scanned folders: what depth means, what is skipped (`node_modules`, dot-directories, repos inside repos), and that scanning reads folder names only.
- **State the limits**, plainly: nothing here refreshes on a timer; WIP summary costs a `git status` per repo and only covers expanded sections; a repo is only known once you have opened it or scanned a folder containing it; **Forget removes it from the list and never from disk**.
- Use a table for what each row action does; prose for why you would want the page.
- Reference the screenshot as `![...](../screenshots/repositories.webp)`.

- [ ] **Step 5: Map the surface and update the tour**

`scripts/docs-map.json` — add to `pages.covered`:

```json
      "repositories": "repositories",
```

and to `commands.covered`:

```json
      "repositories": "repositories",
```

`README.md` — one line in the feature tour, linked to `docs/help/repositories.md`.

`docs/help/workspaces.md` and `docs/help/mission-control.md` — add the page to their **See also** lines.

- [ ] **Step 6: Run the docs guard**

Run: `npm run lint:docs`
Expected: PASS. If it names an unmapped surface, map it or add it to `exempt` with a real reason.

- [ ] **Step 7: Capture the screenshot**

Add an entry to `examples/screenshots/shots.config.mjs` with `out: 'repositories'`, `repos` naming playground repos, and a `drive` that opens the page and expands a couple of sections. Read `examples/screenshots/README.md` for the `--shot` store bridge first.

The state must be **generated, not borrowed**: point the scan root at the playground directory, never at the machine's real `~/Code`. CLAUDE.md's rule against photographing the machine applies directly here — this page's whole subject is folders on disk.

Run: `npm run screenshots repositories`

- [ ] **Step 8: Run the full gate and commit**

```bash
npm run typecheck
npm run lint:i18n
npm run lint:docs
npm test
npm run build
git add -A
git commit -m "docs: document the repositories page"
```

Every one of the five must pass. If any fails, report the failure and its output rather than proceeding — CLAUDE.md calls a green summary over a red tree the one unrecoverable mistake.

---

## Notes for the executor

- **Task 2's fixture assumptions are unverified.** The `basic` playground repo's default branch and remote are asserted in tests without having been checked. Run the two `git -C` commands in Task 2 Step 1 and adjust before writing the implementation, or load the **`playground-fixture`** skill if a new scenario is warranted.
- **The i18n work is real work.** Sixteen files, roughly 35 keys. The guard enforces that keys exist, not that they are translated; pasting English passes the gate and fails the user.
- **Never launch the app.** Compile-only verification. If a change seems to need visual confirmation, say so and stop rather than running `npm run dev`.
