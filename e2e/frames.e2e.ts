import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { startApp, type Running } from './harness'

// BRIEF §8: content that does not run with the app's origin — a sandboxed frame without allow-same-origin,
// or the data: URL the PDF preview uses — must not be able to call bound Go methods. Such frames have an
// opaque origin, which browsers send as `Origin: null`. Bound calls are POSTs to /wails/runtime.

let app: Running

beforeAll(async () => {
  app = await startApp()
})
afterAll(async () => {
  await app?.stop()
})

const VERSION_CALL = JSON.stringify({
  object: 0,
  method: 0,
  args: { 'call-id': 'e2e', methodName: 'github.com/GabrySpada/gitcito/internal/app.AppService.Version', args: [] }
})

async function callWithOrigin(origin: string): Promise<{ status: number; body: string }> {
  const r = await fetch(`${app.url}/wails/runtime`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'x-wails-client-id': 'e2e', Origin: origin },
    body: VERSION_CALL
  })
  return { status: r.status, body: await r.text() }
}

describe('only the app origin reaches the bindings', () => {
  it('answers a call from the app origin (control)', async () => {
    const r = await callWithOrigin(app.url)
    expect(r.status).toBe(200)
    expect(r.body).toMatch(/^\d+\.\d+\.\d+/)
  })

  it('refuses an opaque-origin caller (sandboxed or data: frame)', async () => {
    const r = await callWithOrigin('null')
    expect(r.status).toBe(403)
    expect(r.body).not.toMatch(/\d+\.\d+\.\d+/)
  })

  it('refuses a foreign origin', async () => {
    expect((await callWithOrigin('https://evil.example')).status).toBe(403)
  })

  it('a frame cannot even run inline script under the app CSP', async () => {
    // srcdoc and data: frames inherit the page's `script-src 'self'`, so an injected probe never runs.
    const ran = await app.page.evaluate(async () => {
      const frame = document.createElement('iframe')
      frame.setAttribute('sandbox', 'allow-scripts')
      frame.srcdoc = `<script>parent.postMessage({ ran: true }, '*')<\/script>`
      const got = new Promise<boolean>((resolve) => {
        const on = (e: MessageEvent): void => {
          if (e.data?.ran) resolve(true)
        }
        window.addEventListener('message', on)
        setTimeout(() => resolve(false), 3000)
      })
      document.body.appendChild(frame)
      const r = await got
      frame.remove()
      return r
    })
    expect(ran).toBe(false)
  })
})
