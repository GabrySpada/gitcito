import React from 'react'
import ReactDOM from 'react-dom/client'
import '@fontsource/nunito/400.css'
import '@fontsource/nunito/600.css'
import '@fontsource/nunito/700.css'
import '@fontsource/nunito/800.css'
// The Kraken theme's face: a humanist sans with a tall x-height, closer to
// GitKraken's than Nunito. Only downloaded while that theme is active.
import '@fontsource-variable/open-sans/wght.css'
import './styles.css'
// Keyframes for the expressive title-bar avatar; honours prefers-reduced-motion.
import 'blobatar/motion.css'
import '@xterm/xterm/css/xterm.css'

async function installWailsBridge(): Promise<void> {
  // Imported lazily so the Electron build never evaluates the Wails runtime.
  const { createBridge, installLinkGuard, loadBootstrap } = await import('./infrastructure/bridge')
  window.api = createBridge(await loadBootstrap())
  installLinkGuard(window.api.openExternal)
}

async function start(): Promise<void> {
  // Electron's preload has already exposed window.api; anything else is the Wails host, which builds it from
  // one bound bootstrap call.
  if (!window.api) await installWailsBridge()

  // Screenshot automation: only when launched with `--shot` (forwarded by the main process). Attaches the
  // store bridge used by the capture harness.
  if (window.api.shotMode) {
    void import('./lib/shotBridge').then((m) => m.installShotBridge())
  }

  // App is loaded only now: modules under it (infrastructure/api.ts) read window.api at import time, which
  // under Wails exists only after the bridge is installed.
  const { default: App } = await import('./App')
  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      <App />
    </React.StrictMode>
  )
}

void start()
