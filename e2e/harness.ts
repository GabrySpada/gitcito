/**
 * Starts the Wails server build on a free loopback port with a throwaway data dir and keychain suffix,
 * and opens a Chromium page on it. Every test file gets its own server: nothing leaks between files,
 * and nothing ever touches the real app data, keychain, ~/.ssh or global git config.
 */
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync, existsSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { chromium, type Browser, type Page } from 'playwright'

export const ROOT = resolve(__dirname, '..')
const BINARY = join(ROOT, 'bin', process.platform === 'win32' ? 'gitcito-server.exe' : 'gitcito-server')

export interface Running {
  url: string
  dataDir: string
  home: string
  page: Page
  browser: Browser
  logs: string[]
  stop(): Promise<void>
}

function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const s = createServer()
    s.once('error', rej)
    s.listen(0, '127.0.0.1', () => {
      const addr = s.address()
      const port = typeof addr === 'object' && addr ? addr.port : 0
      s.close(() => res(port))
    })
  })
}

async function waitHealthy(url: string, child: ChildProcess, logs: string[]): Promise<void> {
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`server exited early:\n${logs.join('')}`)
    try {
      const r = await fetch(`${url}/health`)
      if (r.ok) return
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 200))
  }
  throw new Error(`server never became healthy:\n${logs.join('')}`)
}

export async function startApp(): Promise<Running> {
  if (!existsSync(BINARY)) throw new Error(`missing ${BINARY}: run \`npm run build:server\` first`)
  const dataDir = mkdtempSync(join(tmpdir(), 'gitcito-e2e-data-'))
  const home = mkdtempSync(join(tmpdir(), 'gitcito-e2e-home-'))
  const port = await freePort()
  const logs: string[] = []
  const child = spawn(BINARY, [], {
    env: {
      ...process.env,
      GITCITO_DATA_DIR: dataDir,
      GITCITO_KEYCHAIN_SUFFIX: `e2e-${port}`,
      WAILS_SERVER_PORT: String(port),
      WAILS_SERVER_HOST: '127.0.0.1',
      HOME: home,
      XDG_CONFIG_HOME: join(home, '.config'),
      GIT_CONFIG_GLOBAL: join(home, '.gitconfig'),
      GIT_CONFIG_NOSYSTEM: '1'
    },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: process.platform !== 'win32'
  })
  child.stdout?.on('data', (d) => logs.push(String(d)))
  child.stderr?.on('data', (d) => logs.push(String(d)))
  const url = `http://127.0.0.1:${port}`
  await waitHealthy(url, child, logs)

  // PLAYWRIGHT_CHROMIUM_PATH points at a preinstalled Chromium when the bundled one is not downloaded.
  const executablePath = process.env.PLAYWRIGHT_CHROMIUM_PATH || undefined
  const browser = await chromium.launch({ executablePath })
  const page = await browser.newPage()
  page.on('console', (m) => logs.push(`[console.${m.type()}] ${m.text()}\n`))
  page.on('pageerror', (e) => logs.push(`[pageerror] ${e.message}\n`))
  await page.goto(url)
  await page.waitForFunction(() => Boolean(window.api), undefined, { timeout: 30_000 })

  return {
    url,
    dataDir,
    home,
    page,
    browser,
    logs,
    async stop() {
      await browser.close().catch(() => undefined)
      // The server runs in its own process group; take the whole group down.
      try {
        if (child.pid && process.platform !== 'win32') process.kill(-child.pid, 'SIGTERM')
        else child.kill('SIGTERM')
      } catch {
        /* already gone */
      }
      const exited = new Promise<void>((r) => child.once('exit', () => r()))
      await Promise.race([exited, new Promise((r) => setTimeout(r, 3000))])
      if (child.exitCode === null && child.signalCode === null) {
        try {
          if (child.pid && process.platform !== 'win32') process.kill(-child.pid, 'SIGKILL')
          else child.kill('SIGKILL')
        } catch {
          /* already gone */
        }
      }
      rmSync(dataDir, { recursive: true, force: true })
      rmSync(home, { recursive: true, force: true })
    }
  }
}
