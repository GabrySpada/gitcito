import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RefreshSlice } from '../src/renderer/src/stores/repo'

// Moving HEAD or renaming a branch rewrites the `HEAD -> …` / branch-name
// decorations the graph reads off each commit. A refetch that skips the log
// slice leaves the graph's ✓ on the previous branch (or the old name on the
// label) while the sidebar already shows the new state.

const gitApi = vi.hoisted(() => ({
  checkout: vi.fn(async () => undefined),
  renameBranch: vi.fn(async () => undefined),
  renameBranchRemote: vi.fn(async () => undefined)
}))
// Busy labels, toasts, the undo stack: none of it matters here, so every UI
// call is a no-op spy.
const ui = vi.hoisted(() => {
  const fns = new Map<PropertyKey, unknown>()
  return new Proxy({ tabs: [] } as Record<PropertyKey, unknown>, {
    get: (target, key) => {
      if (key in target) return target[key]
      if (!fns.has(key)) fns.set(key, vi.fn())
      return fns.get(key)
    }
  })
})

vi.mock('../src/renderer/src/infrastructure/api', () => ({ gitApi, hostingApi: {} }))
vi.mock('../src/renderer/src/stores/ui', () => ({ useUIStore: { getState: () => ui } }))
vi.mock('../src/renderer/src/stores/settings', () => ({
  useSettingsStore: {
    getState: () => ({ settings: { initialCommitCount: 400, loadMoreCount: 200, tabs: [] }, openRepoTab: vi.fn() }),
    subscribe: vi.fn()
  }
}))
vi.mock('../src/renderer/src/i18n', () => ({
  t: (key: string) => key,
  interp: (value: string) => value
}))

import { repoActions, useRepoStore, type RepoData } from '../src/renderer/src/stores/repo'

const PATH = '/repo'
const refresh = vi.fn(async (_path: string, _opts?: { only?: RefreshSlice[] }) => undefined)

/** The slices the last refresh asked for; `undefined` means every slice. */
const lastSlices = (): RefreshSlice[] | undefined => {
  expect(refresh).toHaveBeenCalled()
  return refresh.mock.calls.at(-1)?.[1]?.only
}

describe('operations that move HEAD or rename a ref refetch the graph', () => {
  beforeEach(() => {
    refresh.mockClear()
    useRepoStore.setState({
      repos: {
        [PATH]: { path: PATH, commits: [], branches: { current: 'main', all: [] }, worktrees: [] } as unknown as RepoData
      },
      refresh
    })
  })

  it('checkout refetches the log, so the head badge follows the branch', async () => {
    await repoActions.checkout(PATH, 'feature')
    expect(gitApi.checkout).toHaveBeenCalledWith(PATH, 'feature')
    const only = lastSlices()
    if (only) expect(only).toContain('log')
  })

  it('renaming a branch refetches the log, so the label shows the new name', async () => {
    await repoActions.renameBranch(PATH, 'old', 'new')
    expect(gitApi.renameBranch).toHaveBeenCalledWith(PATH, 'old', 'new')
    const only = lastSlices()
    if (only) expect(only).toContain('log')
  })
})
