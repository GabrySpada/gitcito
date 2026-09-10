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
