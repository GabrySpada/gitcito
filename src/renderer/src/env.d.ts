/// <reference types="vite/client" />

// The contract lives in src/shared so both hosts — the Electron preload and the Wails bridge — are checked
// against the same type.
import type { PreloadApi } from '../../shared/bridgeApi'

declare global {
  interface Window {
    api: PreloadApi
  }
}

export {}
