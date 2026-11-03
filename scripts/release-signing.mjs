#!/usr/bin/env node
// Adds the macOS signing identity to wails.yaml for one release run (never committed).
//   node scripts/release-signing.mjs developer-id <p12 path>   → Developer ID certificate, password from CSC_KEY_PASSWORD
//   node scripts/release-signing.mjs adhoc                      → ad-hoc signature (no certificate configured)
// The committed wails.yaml carries `darwin.signing.entitlements` only; this inserts the identity lines right
// under that `signing:` key, so the result stays a single mapping that `wails3 config check` validates.
import { readFileSync, writeFileSync } from 'node:fs'

const [mode, p12] = process.argv.slice(2)
const file = 'wails.yaml'
const lines = readFileSync(file, 'utf8').split('\n')
const darwin = lines.indexOf('darwin:')
const signing = lines.findIndex((l, i) => i > darwin && l === '  signing:')
if (darwin < 0 || signing < 0) {
  console.error('wails.yaml: no darwin.signing block to extend')
  process.exit(1)
}
let add
if (mode === 'developer-id' && p12) {
  add = [
    `    certificate: ${p12}`,
    '    credential: CSC_KEY_PASSWORD',
    // Notarization needs an RFC 3161 SHA-2 timestamp (yaml-builds docs).
    '    timestamp_server: http://timestamp.digicert.com'
  ]
} else if (mode === 'adhoc') {
  add = ['    identity: "-"']
} else {
  console.error('usage: release-signing.mjs developer-id <p12> | adhoc')
  process.exit(2)
}
lines.splice(signing + 1, 0, ...add)
writeFileSync(file, lines.join('\n'))
console.log(`wails.yaml: macOS signing set to ${mode}`)
