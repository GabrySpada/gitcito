#!/usr/bin/env node
// Runs the wails3 CLI with Wake (the YAML build system) enabled. Wake is experimental and every wails3
// command needs WAILS_EXP_USE_WAKE in its environment; setting it here keeps npm scripts working on
// Windows, where `VAR=1 cmd` is not shell syntax. Usage: node scripts/wails.mjs <wails3 args…>
import { spawnSync } from 'node:child_process'

const env = { ...process.env, WAILS_EXP_USE_WAKE: '1' }
const res = spawnSync('wails3', process.argv.slice(2), { stdio: 'inherit', env, shell: process.platform === 'win32' })
if (res.error) {
  console.error(`wails3: ${res.error.message} — install it with: go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0`)
  process.exit(1)
}
process.exit(res.status ?? 1)
