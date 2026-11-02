// Plain Vite for the Wails host (docs/port/BRIEF.md). electron.vite.config.ts keeps building the Electron
// renderer from the same sources; this one builds the page Go embeds (frontend-dist/) and serves it in
// `wails3 dev` on WAILS_VITE_PORT.
import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig, type PluginOption, type UserConfig } from 'vite'
import react from '@vitejs/plugin-react'

const HERE = dirname(fileURLToPath(import.meta.url))
const BINDINGS = resolve(HERE, 'src/renderer/src/bindings')

const config = (plugins: PluginOption[]): UserConfig => ({
  root: resolve(HERE, 'src/renderer'),
  plugins,
  server: {
    host: '127.0.0.1',
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
    // The handbook (docs/help) and its screenshots are globbed from outside the Vite root.
    fs: { allow: [HERE] }
  },
  build: {
    outDir: resolve(HERE, 'frontend-dist'),
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Same rule as electron.vite.config.ts: one chunk per handbook locale instead of one per page.
        manualChunks(id) {
          const help = /docs\/help\/([a-z]{2}(?:-[A-Za-z]{2,4})?)\//.exec(id)
          return help ? `help-${help[1]}` : undefined
        }
      }
    }
  }
})

export default defineConfig(async () => {
  // The typed-events plugin aborts the build when the generated bindings are absent. Until
  // `wails3 generate bindings` has run, build without it: events still work, just untyped. Imported
  // dynamically because it is ESM-only and this package is not `"type": "module"`, so Vite loads this
  // config as CommonJS.
  const plugins: PluginOption[] = [react()]
  if (existsSync(resolve(BINDINGS, 'github.com/wailsapp/wails/v3/internal/eventcreate.ts'))) {
    const { default: wails } = await import('@wailsio/runtime/plugins/vite')
    plugins.push(wails(BINDINGS))
  }
  return config(plugins)
})
