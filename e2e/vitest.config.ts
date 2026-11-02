import { defineConfig } from 'vitest/config'

// End-to-end tests drive the Wails *server* build (real Go services, browser UI) with Playwright.
// Build it first: `npm run build:server`. Run: `npm run test:e2e`.
export default defineConfig({
  test: {
    include: ['e2e/**/*.e2e.ts'],
    testTimeout: 60_000,
    hookTimeout: 120_000,
    // One server per file; files run one after another so ports and CPU stay predictable.
    fileParallelism: false
  }
})
