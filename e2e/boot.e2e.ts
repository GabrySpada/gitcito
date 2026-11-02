import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { startApp, type Running } from './harness'

let app: Running

beforeAll(async () => {
  app = await startApp()
})
afterAll(async () => {
  await app?.stop()
})

describe('boot (server build)', () => {
  it('bootstraps from Go before the first render', async () => {
    const boot = await app.page.evaluate(async () => ({
      platform: window.api.platform,
      version: await window.api.appVersion()
    }))
    expect(['linux', 'darwin', 'win32']).toContain(boot.platform)
    expect(boot.version).toMatch(/^\d+\.\d+\.\d+/)
  })

  it('renders the onboarding on a fresh data dir', async () => {
    await expect.poll(() => app.page.locator('body').innerText(), { timeout: 15_000 }).toContain('gitcito')
  })

  it('persists settings through the Go settings service', async () => {
    const saved = await app.page.evaluate(async () => {
      const s = (await window.api.settings.get()) as Record<string, unknown>
      await window.api.settings.set({ ...s, e2eMarker: 'kept' })
      const again = (await window.api.settings.get()) as Record<string, unknown>
      return again.e2eMarker
    })
    expect(saved).toBe('kept')
  })

  it('keeps the activity log readable and clearable', async () => {
    const entries = await app.page.evaluate(() => window.api.log.clear())
    expect(entries).toEqual([])
  })
})
