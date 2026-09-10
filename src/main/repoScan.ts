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
