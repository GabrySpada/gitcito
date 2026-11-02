import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { startApp, type Running } from './harness'

// The semantic diff runs tree-sitter in the page (BRIEF §5.3a): the grammars must be served as frontend
// assets and the CSP must allow compiling them ('wasm-unsafe-eval'), and nothing broader.

let app: Running

beforeAll(async () => {
  app = await startApp()
})
afterAll(async () => {
  await app?.stop()
})

describe('tree-sitter in the page', () => {
  it('serves the runtime and a grammar, and the CSP lets them compile', async () => {
    const r = await app.page.evaluate(async () => {
      const out: Record<string, unknown> = {}
      for (const f of ['tree-sitter.wasm', 'tree-sitter-typescript.wasm']) {
        const res = await fetch(`/tree-sitter/${f}`)
        out[f] = res.status
        try {
          await WebAssembly.compile(await res.arrayBuffer())
          out[`${f}:compiled`] = true
        } catch (e) {
          out[`${f}:compiled`] = String(e)
        }
      }
      return out
    })
    expect(r).toEqual({
      'tree-sitter.wasm': 200,
      'tree-sitter.wasm:compiled': true,
      'tree-sitter-typescript.wasm': 200,
      'tree-sitter-typescript.wasm:compiled': true
    })
  })

  it('widens script-src by exactly wasm-unsafe-eval', async () => {
    // page.evaluate runs through the debugger protocol and is not subject to the CSP, so the policy is checked
    // as served rather than by trying eval.
    const html = await (await fetch(app.url)).text()
    const csp = /Content-Security-Policy"\s+content="([^"]+)"/.exec(html)?.[1] ?? ''
    const scriptSrc = csp.split(';').map((d) => d.trim()).find((d) => d.startsWith('script-src'))
    expect(scriptSrc).toBe("script-src 'self' 'wasm-unsafe-eval'")
    expect(csp.replace("'wasm-unsafe-eval'", '')).not.toContain('unsafe-eval')
  })
})
