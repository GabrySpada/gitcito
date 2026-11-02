#!/usr/bin/env node
// Parity inventory for the Wails port (docs/port/BRIEF.md §4.1).
//
// Reads the Electron contract straight from the TypeScript AST — preload bridge, ipcMain handlers,
// webContents emissions, the gitService object and its lock tables, renderer call sites — and writes
// docs/port/INVENTORY.md plus docs/port/inventory.json.
//
// The Go owner / status / test columns are progress, not facts derived from the TS source, so they are
// merged back from the existing INVENTORY.md on every run (keyed by table + first column). `--go-scan`
// additionally fills them from `// channel: <name>` / `// gitsvc: <method>` doc comments in internal/.
//
//   node scripts/port/inventory.mjs            regenerate, keep progress columns
//   node scripts/port/inventory.mjs --go-scan  … and mark rows owned by annotated Go methods as ported
//   … --go-scan --go-dir=<path>                scan another tree instead of internal/

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const OUT_MD = path.join(ROOT, 'docs/port/INVENTORY.md')
const OUT_JSON = path.join(ROOT, 'docs/port/inventory.json')
const PRELOAD = path.join(ROOT, 'src/preload/index.ts')
const MAIN_DIR = path.join(ROOT, 'src/main')
const GIT_TS = path.join(MAIN_DIR, 'git.ts')
const RENDERER_DIR = path.join(ROOT, 'src/renderer/src')
const API_TS = path.join(RENDERER_DIR, 'infrastructure/api.ts')
const argv = process.argv.slice(2)
const args = new Set(argv)
// --go-dir=<path> points the scan elsewhere (used to test the annotation parser on a scratch tree).
const GO_DIR = path.resolve(ROOT, argv.find((a) => a.startsWith('--go-dir='))?.slice(9) ?? 'internal')
const GO_SCAN = args.has('--go-scan')
const MAX_SITES_MD = 4
const MAX_TYPE_MD = 140

// Diagnostics the parser could not classify; printed and written to the Markdown.
const problems = []

// ─── helpers ─────────────────────────────────────────────────────────────────

const rel = (f) => path.relative(ROOT, f).split(path.sep).join('/')
const lineOf = (sf, node) => sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1
const at = (sf, node) => `${rel(sf.fileName)}:${lineOf(sf, node)}`
const squash = (s) => s.replace(/\s+/g, ' ').trim()

function walkFiles(dir, exts) {
  const out = []
  if (!fs.existsSync(dir)) return out
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) {
      if (e.name === 'node_modules' || e.name.startsWith('.')) continue
      out.push(...walkFiles(p, exts))
    } else if (exts.some((x) => e.name.endsWith(x)) && !e.name.endsWith('.d.ts')) out.push(p)
  }
  return out.sort()
}

function visit(node, fn) {
  fn(node)
  ts.forEachChild(node, (c) => visit(c, fn))
}

/** A channel argument as a stable pattern: 'a:b' → a:b, `a:${id}` → a:${id}. */
function channelOf(sf, node) {
  if (!node) return null
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text
  if (ts.isTemplateExpression(node)) return node.getText(sf).slice(1, -1)
  return null
}

// Printed without comments (a JSDoc inside an inline object type would otherwise land in the table),
// otherwise as written.
const printer = ts.createPrinter({ removeComments: true, newLine: ts.NewLineKind.LineFeed })
const nodeText = (sf, node) => squash(printer.printNode(ts.EmitHint.Unspecified, node, sf)).replace(/;(\s*})/g, '$1')
const paramText = (sf, p) => nodeText(sf, p)
const isFn = (n) => n && (ts.isArrowFunction(n) || ts.isFunctionExpression(n) || ts.isFunctionDeclaration(n) || ts.isMethodDeclaration(n))

// ─── type-checked program over main + preload + shared ─────────────────────

const mainFiles = walkFiles(MAIN_DIR, ['.ts'])
const program = ts.createProgram([...mainFiles, PRELOAD], {
  target: ts.ScriptTarget.ES2022,
  module: ts.ModuleKind.ESNext,
  moduleResolution: ts.ModuleResolutionKind.Bundler,
  strict: true,
  skipLibCheck: true,
  esModuleInterop: true,
  resolveJsonModule: true,
  noEmit: true,
  types: ['node']
})
const checker = program.getTypeChecker()

/** Return type as written, or the checker's inference marked "(inferred)". */
function returnTypeOf(sf, fn) {
  if (fn.type) return nodeText(sf, fn.type)
  try {
    const sig = checker.getSignatureFromDeclaration(fn)
    if (!sig) return '?'
    const t = checker.getReturnTypeOfSignature(sig)
    return `${squash(checker.typeToString(t, fn, ts.TypeFormatFlags.UseAliasDefinedOutsideCurrentScope))} (inferred)`
  } catch {
    return '?'
  }
}

// ─── src/main: emissions, local call graph, ipcMain handlers ────────────────

/** Per file: named local functions (with the local names they reference) and every emission. */
function analyseMainFile(sf) {
  const fns = new Map() // name -> [node]
  const emissions = [] // { channel, at, pos }
  const addFn = (name, node) => {
    if (!fns.has(name)) fns.set(name, [])
    fns.get(name).push(node)
  }
  visit(sf, (n) => {
    if (ts.isFunctionDeclaration(n) && n.name) addFn(n.name.text, n)
    else if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name) && isFn(n.initializer)) addFn(n.name.text, n.initializer)
    else if ((ts.isMethodDeclaration(n) || ts.isPropertyAssignment(n)) && n.name && ts.isIdentifier(n.name)) {
      const body = ts.isMethodDeclaration(n) ? n : n.initializer
      if (isFn(body)) addFn(n.name.text, body)
    }
    if (ts.isCallExpression(n) && ts.isPropertyAccessExpression(n.expression) && n.expression.name.text === 'send') {
      const recv = n.expression.expression.getText(sf)
      const channel = channelOf(sf, n.arguments[0])
      if (channel && !/ipcRenderer/.test(recv) && /^[A-Za-z][\w-]*:/.test(channel)) {
        emissions.push({ channel, at: at(sf, n), pos: n.getStart(sf) })
      }
    }
  })
  return { fns, emissions }
}

const handlers = new Map() // channel -> { method, at, params, returns, events, alts }
const emissionsByChannel = new Map() // channel -> [at]
const mainInfo = new Map()
const allEmissions = [] // { channel, file, pos }
const mainFileSet = new Set(mainFiles.map((f) => path.resolve(f)))

for (const file of mainFiles) {
  const sf = program.getSourceFile(file)
  if (!sf) continue
  const info = analyseMainFile(sf)
  mainInfo.set(file, { sf, info })
  for (const e of info.emissions) {
    allEmissions.push({ ...e, file: sf.fileName })
    if (!emissionsByChannel.has(e.channel)) emissionsByChannel.set(e.channel, [])
    emissionsByChannel.get(e.channel).push(e.at)
  }
}

/** The function body a declaration stands for, if it is one. */
function fnOfDecl(d) {
  if (ts.isFunctionDeclaration(d) || ts.isMethodDeclaration(d)) return d.body ? d : null
  if ((ts.isVariableDeclaration(d) || ts.isPropertyAssignment(d)) && isFn(d.initializer)) return d.initializer
  return null
}

// Functions a function references (calls, or passes as a callback), resolved through imports by the
// checker and limited to src/main. Memoised: the graph is walked once per handler.
const refsMemo = new Map()
function fnRefs(node) {
  if (refsMemo.has(node)) return refsMemo.get(node)
  const out = new Set()
  visit(node, (n) => {
    if (!ts.isIdentifier(n)) return
    let sym = checker.getSymbolAtLocation(n)
    if (sym && sym.flags & ts.SymbolFlags.Alias) sym = checker.getAliasedSymbol(sym)
    for (const d of sym?.declarations ?? []) {
      if (!mainFileSet.has(path.resolve(d.getSourceFile().fileName))) continue
      const f = fnOfDecl(d)
      if (f && f !== node) out.add(f)
    }
  })
  refsMemo.set(node, out)
  return out
}

/** Event channels reachable from `root`: sent inside it or inside any src/main function it reaches. */
function emissionsReachable(root) {
  const seen = new Set()
  const queue = [root]
  const ranges = []
  while (queue.length) {
    const node = queue.pop()
    if (seen.has(node)) continue
    seen.add(node)
    ranges.push([node.getSourceFile().fileName, node.getStart(), node.getEnd()])
    for (const f of fnRefs(node)) queue.push(f)
  }
  const hits = allEmissions.filter((e) => ranges.some(([f, a, b]) => f === e.file && e.pos >= a && e.pos < b))
  return [...new Set(hits.map((e) => e.channel))].sort()
}

for (const [file, { sf, info }] of mainInfo) {
  visit(sf, (n) => {
    if (!ts.isCallExpression(n) || !ts.isPropertyAccessExpression(n.expression)) return
    if (n.expression.expression.getText(sf) !== 'ipcMain') return
    const method = n.expression.name.text
    if (!['handle', 'handleOnce', 'on', 'once'].includes(method)) return
    const channel = channelOf(sf, n.arguments[0])
    if (!channel) {
      problems.push(`ipcMain.${method} with a non-literal channel at ${at(sf, n)}`)
      return
    }
    let fn = n.arguments[1]
    if (fn && ts.isIdentifier(fn)) fn = info.fns.get(fn.text)?.[0]
    if (!isFn(fn)) problems.push(`ipcMain.${method}('${channel}') handler is not a resolvable function at ${at(sf, n)}`)
    const row = {
      method,
      at: at(sf, n),
      params: isFn(fn) ? fn.parameters.slice(1).map((p) => paramText(sf, p)) : [],
      returns: isFn(fn) ? returnTypeOf(sf, fn) : '?',
      events: isFn(fn) ? emissionsReachable(fn) : []
    }
    // A channel registered on alternative code paths (e.g. menu:set is a no-op on frameless platforms):
    // keep every source, describe the richest handler.
    const prev = handlers.get(channel)
    if (!prev) handlers.set(channel, { ...row, alts: [] })
    else {
      problems.push(`channel '${channel}' is registered at ${prev.at} and ${row.at} (alternative code paths; the one with more parameters is described)`)
      const richer = row.params.length > prev.params.length ? row : prev
      handlers.set(channel, { ...richer, alts: [...prev.alts, richer === row ? prev.at : row.at], events: [...new Set([...prev.events, ...row.events])].sort() })
    }
  })
}

// ─── src/main/git.ts: gitService + lock tables + activity events ────────────

const gitSf = program.getSourceFile(GIT_TS)
const gitInfo = mainInfo.get(GIT_TS).info

function stringSet(name) {
  const out = new Set()
  visit(gitSf, (n) => {
    if (!ts.isVariableDeclaration(n) || !ts.isIdentifier(n.name) || n.name.text !== name) return
    visit(n.initializer, (m) => {
      if (ts.isStringLiteral(m)) out.add(m.text)
    })
  })
  if (!out.size) problems.push(`could not find ${name} in src/main/git.ts`)
  return out
}

const READ = stringSet('READ_METHODS')
const APP = stringSet('APP_LEVEL_METHODS')

// Activity events: the EVENT_FOR_METHOD table, overridden by eventForCall's special cases.
const activity = new Map() // method -> string[]
visit(gitSf, (n) => {
  if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name) && n.name.text === 'EVENT_FOR_METHOD') {
    for (const p of n.initializer.properties ?? []) {
      if (ts.isPropertyAssignment(p) && ts.isStringLiteral(p.initializer)) activity.set(p.name.getText(gitSf), [p.initializer.text])
    }
  }
  if (ts.isFunctionDeclaration(n) && n.name?.text === 'eventForCall') {
    visit(n.body, (m) => {
      if (!ts.isIfStatement(m)) return
      const cond = m.expression.getText(gitSf).match(/^method === '([^']+)'$/)
      if (!cond) return
      const lits = []
      visit(m.thenStatement, (k) => ts.isStringLiteral(k) && lits.push(k.text))
      activity.set(cond[1], [...new Set(lits)])
    })
  }
})

// IPC events the 'git' dispatcher emits on behalf of a method (clone:progress, bisect:output).
const dispatcherEvents = new Map()
visit(gitSf, (n) => {
  if (!ts.isIfStatement(n)) return
  const cond = n.expression.getText(gitSf).match(/^method === '([^']+)'$/)
  if (!cond) return
  const evs = gitInfo.emissions.filter((e) => e.pos >= n.getStart(gitSf) && e.pos < n.getEnd()).map((e) => e.channel)
  if (evs.length) dispatcherEvents.set(cond[1], [...new Set(evs)])
})

const gitMethods = new Map() // name -> row
visit(gitSf, (n) => {
  if (!ts.isVariableDeclaration(n) || !ts.isIdentifier(n.name) || n.name.text !== 'gitService') return
  if (!n.initializer || !ts.isObjectLiteralExpression(n.initializer)) {
    problems.push('gitService is not an object literal')
    return
  }
  for (const p of n.initializer.properties) {
    const name = p.name?.getText(gitSf)
    let fn = ts.isMethodDeclaration(p) ? p : ts.isPropertyAssignment(p) ? p.initializer : null
    if (fn && ts.isIdentifier(fn)) fn = gitInfo.fns.get(fn.text)?.[0] ?? fn
    if (!name || !isFn(fn)) {
      problems.push(`gitService member not a function: ${squash(p.getText(gitSf)).slice(0, 80)} at ${at(gitSf, p)}`)
      continue
    }
    gitMethods.set(name, {
      events: emissionsReachable(fn),
      at: at(gitSf, p),
      params: fn.parameters.map((x) => paramText(gitSf, x)),
      returns: returnTypeOf(gitSf, fn)
    })
  }
})
for (const m of [...READ, ...APP]) if (!gitMethods.has(m)) problems.push(`'${m}' is listed in a lock table but is not a gitService method`)
for (const m of activity.keys()) if (!gitMethods.has(m)) problems.push(`'${m}' has an activity event but is not a gitService method`)

// ─── src/preload/index.ts ──────────────────────────────────────────────────

const preloadSf = program.getSourceFile(PRELOAD)
const preloadMembers = [] // { path, kind, channel, params, returns, at }
let apiLiteral = null
visit(preloadSf, (n) => {
  if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name) && n.name.text === 'api' && n.initializer && ts.isObjectLiteralExpression(n.initializer)) apiLiteral = n.initializer
})
if (!apiLiteral) problems.push('could not find `const api = { … }` in src/preload/index.ts')

function walkPreload(obj, prefix) {
  for (const p of obj.properties) {
    const key = p.name?.getText(preloadSf)
    if (!key) continue
    const pathStr = prefix ? `${prefix}.${key}` : key
    const init = ts.isPropertyAssignment(p) ? p.initializer : ts.isMethodDeclaration(p) ? p : null
    if (init && ts.isObjectLiteralExpression(init)) {
      walkPreload(init, pathStr)
      continue
    }
    const ipc = []
    if (init) {
      visit(init, (n) => {
        if (!ts.isCallExpression(n) || !ts.isPropertyAccessExpression(n.expression)) return
        if (n.expression.expression.getText(preloadSf) !== 'ipcRenderer') return
        const verb = n.expression.name.text
        if (!['invoke', 'send', 'on', 'once', 'sendSync'].includes(verb)) return
        const channel = channelOf(preloadSf, n.arguments[0])
        if (!channel) {
          // The generic git dispatcher passes the method name as data, not as the channel.
          problems.push(`preload ${pathStr}: ipcRenderer.${verb} with non-literal channel at ${at(preloadSf, n)}`)
          return
        }
        ipc.push({ verb, channel })
      })
    }
    const fn = isFn(init) ? init : null
    const base = {
      path: pathStr,
      at: at(preloadSf, p),
      params: fn ? fn.parameters.map((x) => paramText(preloadSf, x)) : [],
      returns: fn ? (fn.type ? nodeText(preloadSf, fn.type) : '?') : init ? nodeText(preloadSf, init) : '?'
    }
    if (!ipc.length) {
      preloadMembers.push({ ...base, kind: 'local', channel: null })
      continue
    }
    const kinds = new Set(ipc.map((i) => i.verb))
    const channels = [...new Set(ipc.map((i) => i.channel))]
    if (channels.length > 1) problems.push(`preload ${pathStr} touches several channels: ${channels.join(', ')}`)
    const kind = kinds.has('invoke') ? 'invoke' : kinds.has('send') || kinds.has('sendSync') ? 'send' : 'event'
    preloadMembers.push({ ...base, kind, channel: channels[0] })
  }
}
if (apiLiteral) walkPreload(apiLiteral, '')

// ─── renderer call sites ───────────────────────────────────────────────────

const memberPaths = new Set(preloadMembers.map((m) => m.path))
const memberSites = new Map() // member path -> [{ at, via }]
const gitSites = new Map() // git method -> [{ at, via }]
const adapterMap = new Map() // 'settingsApi.get' -> { member?: path, git?: method }
const addSite = (map, key, site) => {
  if (!map.has(key)) map.set(key, [])
  map.get(key).push(site)
}

/** `window.api.a.b` (or `window.api?.a`) → 'a.b', matched to the longest preload member path. */
function windowApiPath(sf, n) {
  if (!ts.isPropertyAccessExpression(n)) return null
  const inner = n.expression
  if (!ts.isPropertyAccessExpression(inner) || inner.name.text !== 'api') return null
  if (!ts.isIdentifier(inner.expression) || inner.expression.text !== 'window') return null
  const parts = [n.name.text]
  let cur = n
  while (cur.parent && ts.isPropertyAccessExpression(cur.parent) && cur.parent.expression === cur) {
    cur = cur.parent
    parts.push(cur.name.text)
  }
  for (let i = parts.length; i > 0; i--) {
    const p = parts.slice(0, i).join('.')
    if (memberPaths.has(p)) return { path: p, node: cur }
  }
  return { path: parts.join('.'), node: cur, unknown: true }
}

/** If `node` is `window.api.git('method', …)` or the api.ts `call('method', …)` helper, the method. */
function gitCallMethod(sf, node) {
  if (!ts.isCallExpression(node)) return null
  const c = node.expression
  const isCallHelper = ts.isIdentifier(c) && c.text === 'call' && rel(sf.fileName) === rel(API_TS)
  const isDirect = ts.isPropertyAccessExpression(c) && c.getText(sf).replace(/\?/g, '') === 'window.api.git'
  if (!isCallHelper && !isDirect) return null
  const m = node.arguments[0]
  return m && ts.isStringLiteral(m) ? m.text : undefined
}

// The Wails bridge and its generated bindings implement window.api; they are not call sites of it.
const rendererFiles = walkFiles(RENDERER_DIR, ['.ts', '.tsx']).filter(
  (f) => !rel(f).startsWith('src/renderer/src/bindings/') && rel(f) !== 'src/renderer/src/infrastructure/bridge.ts'
)
const rendererSfs = rendererFiles.map((f) =>
  ts.createSourceFile(f, fs.readFileSync(f, 'utf8'), ts.ScriptTarget.Latest, true, f.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
)

// Pass 1: adapter objects in api.ts (`export const xxxApi = { prop: … window.api.a.b … }`).
const apiSf = rendererSfs.find((s) => s.fileName === API_TS)
const adapterRanges = []
if (apiSf) {
  visit(apiSf, (n) => {
    if (!ts.isVariableDeclaration(n) || !ts.isIdentifier(n.name) || !/Api$/.test(n.name.text)) return
    if (!n.initializer || !ts.isObjectLiteralExpression(n.initializer)) return
    adapterRanges.push([n.initializer.getStart(apiSf), n.initializer.getEnd()])
    for (const p of n.initializer.properties) {
      const key = `${n.name.text}.${p.name?.getText(apiSf)}`
      const target = { members: new Set(), git: new Set() }
      visit(p, (k) => {
        const w = windowApiPath(apiSf, k)
        if (w && !w.unknown && w.path !== 'git') target.members.add(w.path)
        const g = gitCallMethod(apiSf, k)
        if (g) target.git.add(g)
      })
      if (target.members.size || target.git.size) adapterMap.set(key, target)
    }
  })
}
const inAdapter = (sf, n) => sf === apiSf && adapterRanges.some(([a, b]) => n.getStart(sf) >= a && n.getStart(sf) < b)

// Pass 2: every reference in the renderer.
const unknownApi = new Set()
for (const sf of rendererSfs) {
  visit(sf, (n) => {
    if (inAdapter(sf, n)) return
    const g = gitCallMethod(sf, n)
    if (g === undefined && sf !== apiSf) problems.push(`git call with non-literal method at ${at(sf, n)}`)
    else if (g && !(sf === apiSf && ts.isIdentifier(n.expression))) addSite(gitSites, g, { at: at(sf, n), via: 'window.api.git' })
    const w = windowApiPath(sf, n)
    // windowApiPath only matches the innermost `window.api.x` node, so each reference counts once.
    if (w) {
      if (w.unknown) unknownApi.add(`window.api.${w.path} at ${at(sf, n)}`)
      else if (w.path !== 'git') addSite(memberSites, w.path, { at: at(sf, n), via: null })
    }
    if (ts.isPropertyAccessExpression(n) && ts.isIdentifier(n.expression)) {
      const key = `${n.expression.text}.${n.name.text}`
      const target = adapterMap.get(key)
      if (!target) return
      for (const m of target.members) addSite(memberSites, m, { at: at(sf, n), via: key })
      for (const m of target.git) addSite(gitSites, m, { at: at(sf, n), via: key })
    }
  })
}
for (const u of unknownApi) problems.push(`renderer reads a window.api member the preload does not define: ${u}`)
for (const [k, t] of adapterMap) for (const m of t.git) if (!gitMethods.has(m)) problems.push(`${k} calls git method '${m}', which gitService does not define`)

// ─── rows ──────────────────────────────────────────────────────────────────

const TABLES = {
  ipc: 'IPC channels (invoke / send)',
  events: 'Events (main → renderer)',
  local: 'Preload members with no channel',
  git: 'gitService methods'
}

const rows = []
const fmtSites = (list) => list ?? []

// IPC: one row per channel; preload members and main handlers joined by channel.
const ipcChannels = new Set([
  ...preloadMembers.filter((m) => m.kind === 'invoke' || m.kind === 'send').map((m) => m.channel),
  ...handlers.keys()
])
const eventChannels = new Set([
  ...preloadMembers.filter((m) => m.kind === 'event').map((m) => m.channel),
  ...emissionsByChannel.keys()
])
for (const ch of [...ipcChannels].sort()) {
  if (eventChannels.has(ch)) problems.push(`'${ch}' is both an IPC channel and an event`)
  const members = preloadMembers.filter((m) => m.channel === ch && (m.kind === 'invoke' || m.kind === 'send'))
  const h = handlers.get(ch)
  const kinds = new Set(members.map((m) => m.kind))
  if (h) kinds.add(h.method === 'handle' || h.method === 'handleOnce' ? 'invoke' : 'send')
  if (kinds.size > 1) problems.push(`'${ch}': preload and main disagree on invoke vs send`)
  if (!members.length) problems.push(`'${ch}' has a main handler (${h.at}) but no preload member`)
  if (!h) problems.push(`'${ch}' is called by preload (${members.map((m) => m.at).join(', ')}) but has no ipcMain handler`)
  const sites = members.flatMap((m) => fmtSites(memberSites.get(m.path)))
  const isGit = ch === 'git'
  rows.push({
    table: 'ipc',
    key: ch,
    api: members.map((m) => m.path),
    kind: [...kinds][0] ?? '?',
    args: h ? h.params : members[0]?.params ?? [],
    returns: h ? h.returns : members[0]?.returns ?? '?',
    events: h ? h.events : [],
    source: [...members.map((m) => m.at), ...(h ? [h.at, ...h.alts] : [])],
    sites: isGit ? [] : sites,
    note: isGit ? 'dispatcher: one row per method in the gitService table' : undefined
  })
}

for (const ch of [...eventChannels].sort()) {
  const members = preloadMembers.filter((m) => m.channel === ch && m.kind === 'event')
  const emitters = emissionsByChannel.get(ch) ?? []
  if (!members.length) problems.push(`event '${ch}' is emitted (${emitters.join(', ')}) but no preload member listens`)
  if (!emitters.length) problems.push(`event '${ch}' is listened to by preload but never emitted by main`)
  const emittedBy = [...handlers.entries()].filter(([, h]) => h.events.includes(ch)).map(([c]) => c)
  for (const [m, g] of gitMethods) if (g.events.includes(ch) || dispatcherEvents.get(m)?.includes(ch)) emittedBy.push(`git:${m}`)
  rows.push({
    table: 'events',
    key: ch,
    api: members.map((m) => m.path),
    kind: 'event',
    args: members[0]?.params ?? [],
    returns: '() => void (unsubscribe)',
    events: emittedBy.sort(),
    source: [...members.map((m) => m.at), ...emitters],
    sites: members.flatMap((m) => fmtSites(memberSites.get(m.path)))
  })
}

for (const m of preloadMembers.filter((x) => x.kind === 'local')) {
  rows.push({
    table: 'local',
    key: `api.${m.path}`,
    api: [m.path],
    kind: 'local',
    args: m.params,
    returns: m.returns,
    events: [],
    source: [m.at],
    sites: fmtSites(memberSites.get(m.path))
  })
}

for (const [name, g] of [...gitMethods.entries()].sort(([a], [b]) => a.localeCompare(b))) {
  const kind = APP.has(name) ? 'git-app' : READ.has(name) ? 'git-read' : 'git-write'
  const events = [...new Set([...(dispatcherEvents.get(name) ?? []), ...g.events])].concat((activity.get(name) ?? []).map((a) => `activity:${a}`))
  const sites = fmtSites(gitSites.get(name))
  rows.push({
    table: 'git',
    key: name,
    api: [],
    kind,
    read: READ.has(name),
    appLevel: APP.has(name),
    args: g.params,
    returns: g.returns,
    events,
    source: [g.at],
    sites
  })
}
for (const m of gitSites.keys()) if (!gitMethods.has(m)) problems.push(`renderer calls git method '${m}', which gitService does not define`)

// ─── merge progress columns from the existing INVENTORY.md ──────────────────

const DEFAULTS = { owner: '—', status: 'todo', test: '—' }
const titleToTable = Object.fromEntries(Object.entries(TABLES).map(([k, v]) => [v, k]))
const unesc = (s) => s.replace(/\\\|/g, '|').trim()
const cellKey = (s) => unesc(s).replace(/^`|`$/g, '')

function readExisting() {
  const prev = new Map()
  if (!fs.existsSync(OUT_MD)) return prev
  let table = null
  let header = null
  for (const line of fs.readFileSync(OUT_MD, 'utf8').split('\n')) {
    const h = line.match(/^## (.+?)\s*(\(\d+\))?$/)
    if (h) {
      table = titleToTable[h[1].trim()] ?? null
      header = null
      continue
    }
    if (!table || !line.startsWith('|')) continue
    const cells = line.slice(1, line.endsWith('|') ? -1 : undefined).split(/(?<!\\)\|/).map((c) => c.trim())
    if (!header) {
      header = cells.map((c) => c.toLowerCase())
      continue
    }
    if (/^-+$/.test(cells[0].replace(/:/g, ''))) continue
    const get = (name) => {
      const i = header.indexOf(name)
      return i >= 0 && cells[i] !== undefined ? unesc(cells[i]) : undefined
    }
    prev.set(`${table}|${cellKey(cells[0])}`, { owner: get('go owner'), status: get('status'), test: get('test') })
  }
  return prev
}

const previous = readExisting()
let carried = 0
for (const r of rows) {
  const p = previous.get(`${r.table}|${r.key}`)
  r.owner = p?.owner || DEFAULTS.owner
  r.status = p?.status || DEFAULTS.status
  r.test = p?.test || DEFAULTS.test
  if (p && (r.owner !== DEFAULTS.owner || r.status !== DEFAULTS.status || r.test !== DEFAULTS.test)) carried++
}
const rowKeys = new Set(rows.map((r) => `${r.table}|${r.key}`))
const dropped = [...previous.entries()].filter(
  ([k, v]) => !rowKeys.has(k) && (v.owner !== DEFAULTS.owner || v.status !== DEFAULTS.status || v.test !== DEFAULTS.test)
)
for (const [k] of dropped) problems.push(`row '${k}' had progress in the previous INVENTORY.md but no longer exists in the TS source`)

// ─── --go-scan: `// channel: x` / `// gitsvc: y` above an exported method ─────

const goFindings = []
if (GO_SCAN) {
  const goFiles = walkFiles(GO_DIR, ['.go']).filter((f) => !f.endsWith('_test.go'))
  for (const f of goFiles) {
    const lines = fs.readFileSync(f, 'utf8').split('\n')
    const pkg = lines.map((l) => l.match(/^package (\w+)/)).find(Boolean)?.[1] ?? '?'
    for (let i = 0; i < lines.length; i++) {
      const fn = lines[i].match(/^func\s+\(\s*\w*\s*\*?\s*(\w+)(?:\[[^\]]*\])?\s*\)\s+([A-Z]\w*)\s*[([]/)
      if (!fn) continue
      // Walk up the contiguous doc-comment block directly above the func.
      for (let j = i - 1; j >= 0 && /^\s*\/\//.test(lines[j]); j--) {
        const tag = lines[j].match(/^\s*\/\/\s*(channel|gitsvc):\s*(\S+)\s*$/)
        if (tag) goFindings.push({ tag: tag[1], name: tag[2], owner: `${pkg}.${fn[1]}.${fn[2]}`, at: `${rel(f)}:${j + 1}` })
      }
    }
  }
  for (const g of goFindings) {
    const targets = rows.filter((r) => (g.tag === 'gitsvc' ? r.table === 'git' : r.table !== 'git') && r.key === g.name)
    if (!targets.length) {
      problems.push(`Go annotation '// ${g.tag}: ${g.name}' at ${g.at} matches no inventory row`)
      continue
    }
    for (const r of targets) {
      r.owner = r.owner && r.owner !== DEFAULTS.owner && !r.owner.split(', ').includes(g.owner) ? `${r.owner}, ${g.owner}` : g.owner
      // A row someone already moved past "ported" (e.g. "tested") keeps its status.
      if (!r.status || r.status === DEFAULTS.status || r.status === 'in progress') r.status = 'ported'
    }
  }
}

// ─── output ────────────────────────────────────────────────────────────────

const esc = (s) => String(s).replace(/\|/g, '\\|').replace(/\n/g, ' ')
const code = (s) => (s ? `\`${s}\`` : '')
const clip = (s, n) => (s.length > n ? `${s.slice(0, n - 1)}…` : s)

function sitesCell(list) {
  if (!list.length) return '0'
  const vias = [...new Set(list.map((s) => s.via).filter(Boolean))]
  const shown = list.slice(0, MAX_SITES_MD).map((s) => s.at.replace(/^src\/renderer\/src\//, ''))
  const more = list.length > MAX_SITES_MD ? ` +${list.length - MAX_SITES_MD}` : ''
  return `${list.length}: ${shown.join(', ')}${more}${vias.length ? ` (via ${vias.join(', ')})` : ''}`
}

function rowLine(r) {
  const cells = [
    code(r.key),
    r.api.length ? r.api.map((a) => code(a)).join(', ') : '—',
    r.kind,
    r.args.length ? code(clip(r.args.join(', '), MAX_TYPE_MD)) : '—',
    code(clip(r.returns, MAX_TYPE_MD)),
    r.events.length ? r.events.map(code).join(', ') : '—',
    r.source.join(', '),
    sitesCell(r.sites),
    r.owner,
    r.status,
    r.test
  ]
  return `| ${cells.map(esc).join(' | ')} |`
}

const HEAD =
  '| channel/method | window.api | kind | args | returns | events emitted | source | renderer call sites | Go owner | status | test |\n' +
  '|---|---|---|---|---|---|---|---|---|---|---|'

const count = (pred) => rows.filter(pred).length
const preloadCount = preloadMembers.length
const byKind = (k) => preloadMembers.filter((m) => m.kind === k).length
const statusCounts = rows.reduce((acc, r) => ((acc[r.status] = (acc[r.status] ?? 0) + 1), acc), {})
const zeroSiteGit = rows.filter((r) => r.table === 'git' && !r.sites.length).map((r) => r.key)
const zeroSiteMembers = rows.filter((r) => r.table !== 'git' && r.key !== 'git' && !r.sites.length).map((r) => r.key)

const md = `# Parity inventory

<!-- Generated by scripts/port/inventory.mjs. Edit only the Go owner / status / test columns; everything
     else is rewritten from the TypeScript source on every run. -->

Every contract the Electron app exposes, captured from the TypeScript AST so the Go port can prove parity
(brief §4.1). **Regenerate** with \`npm run port:inventory\` (or \`node scripts/port/inventory.mjs\`); add
\`--go-scan\` to pick up Go ownership annotations. Re-running never loses progress: the **Go owner**,
**status** and **test** cells are read back from this file and merged by table + first column.
\`docs/port/inventory.json\` carries the same rows, untruncated, for tooling.

## How porting agents mark a row as ported

Put a tag line in the doc comment **directly above** the exported Go method that serves the row, then run
\`node scripts/port/inventory.mjs --go-scan\`:

\`\`\`go
// Status returns the working-tree status.
// gitsvc: status
func (s *GitService) Status(ctx context.Context, repoPath string) (RepoStatus, error) {

// channel: localci:run
func (s *LocalCIService) Run(repoPath, workflowFile string) (*int, error) {
\`\`\`

- \`// gitsvc: <method>\` matches a row of the gitService table; \`// channel: <name>\` matches the first
  column of any other table — an IPC channel (\`ssh:status\`), an event (\`term:data:\${id}\`, written exactly
  as in the table) or a channel-less preload member (\`api.zoom.get\`).
- One method may carry several tags (one per line). \`_test.go\` files are ignored.
- The scan sets **Go owner** to \`<package>.<Type>.<Method>\` and **status** to \`ported\` when the status is
  still \`todo\`/\`in progress\`; a later status (e.g. \`tested\`, \`verified\`) is kept. Fill **test** by hand
  with the Go test (or golden) that proves the row, e.g. \`gitsvc/TestStatusGoldens\`.
- An annotation that matches no row is reported below, so a typo does not pass silently.

## Columns

| Column | Meaning |
|---|---|
| channel/method | IPC channel, event channel pattern, \`api.<member>\` for channel-less members, or gitService method name. The merge key. |
| window.api | The preload member path(s) bound to the channel. |
| kind | \`invoke\` (ipcRenderer.invoke ↔ ipcMain.handle), \`send\` (fire-and-forget ↔ ipcMain.on), \`event\` (main → renderer), \`local\` (preload-only, no IPC), \`git-read\` (in READ_METHODS: shared lock), \`git-write\` (not in READ_METHODS: exclusive lock), \`git-app\` (APP_LEVEL_METHODS: no repo lock). |
| args | Parameters as written in the main handler (event argument dropped) or the gitService method; for events, the listener signature. |
| returns | Return type as written; \`(inferred)\` marks the type checker's inference where no annotation exists. |
| events emitted | Events reachable from the handler or method through any src/main function it calls or passes as a callback (resolved by the type checker; an over-approximation — a branch may never send), \`activity:<event>\` for gitService analytics/oplog events, and dispatcher streams (\`clone:progress\`, \`bisect:output\`). For event rows: the channels/methods that emit it. |
| source | preload file:line, then main handler / emitter file:line. |
| renderer call sites | Count, the first ${MAX_SITES_MD} file:line (relative to src/renderer/src), and the api.ts adapter used (\`via\`). Adapter definitions themselves are not counted. |
| Go owner / status / test | Port progress; preserved across runs. Status vocabulary: \`todo\`, \`in progress\`, \`ported\`, \`tested\`, \`verified\`, \`dropped (reason)\`. |

## Summary

| Measure | Count |
|---|---|
| Preload members (leaf paths under window.api) | ${preloadCount} |
| … invoke / send / event listener / local | ${byKind('invoke')} / ${byKind('send')} / ${byKind('event')} / ${byKind('local')} |
| ipcMain handlers (handle / on) | ${handlers.size} (${[...handlers.values()].filter((h) => h.method.startsWith('handle')).length} / ${[...handlers.values()].filter((h) => !h.method.startsWith('handle')).length}) |
| IPC channel rows | ${count((r) => r.table === 'ipc')} |
| Event channels emitted by main | ${emissionsByChannel.size} (${[...emissionsByChannel.values()].reduce((a, b) => a + b.length, 0)} emission sites) |
| gitService methods | ${gitMethods.size} |
| … git-read / git-write / git-app | ${count((r) => r.kind === 'git-read')} / ${count((r) => r.kind === 'git-write')} / ${count((r) => r.kind === 'git-app')} |
| gitService methods with no renderer call site | ${zeroSiteGit.length} |
| Non-git rows with no renderer call site | ${zeroSiteMembers.length} |
| Rows by status | ${Object.entries(statusCounts).map(([k, v]) => `${k}: ${v}`).join(', ')} |

${zeroSiteGit.length ? `gitService methods no renderer code reaches (internal helpers or dead code — port them only if another method needs them): ${zeroSiteGit.map(code).join(', ')}.\n\n` : ''}${zeroSiteMembers.length ? `Non-git rows with no renderer call site: ${zeroSiteMembers.map(code).join(', ')}.\n\n` : ''}## Parser notes

${problems.length ? problems.map((p) => `- ${esc(p)}`).join('\n') : '- none'}

${Object.entries(TABLES)
  .map(([id, title]) => {
    const list = rows.filter((r) => r.table === id)
    return `## ${title} (${list.length})\n\n${HEAD}\n${list.map(rowLine).join('\n')}\n`
  })
  .join('\n')}`

fs.mkdirSync(path.dirname(OUT_MD), { recursive: true })
fs.writeFileSync(OUT_MD, md)
fs.writeFileSync(
  OUT_JSON,
  JSON.stringify(
    {
      generatedBy: 'scripts/port/inventory.mjs',
      counts: {
        preloadMembers: preloadCount,
        preloadByKind: { invoke: byKind('invoke'), send: byKind('send'), event: byKind('event'), local: byKind('local') },
        ipcMainHandlers: handlers.size,
        eventChannels: emissionsByChannel.size,
        gitMethods: gitMethods.size,
        gitRead: count((r) => r.kind === 'git-read'),
        gitWrite: count((r) => r.kind === 'git-write'),
        gitApp: count((r) => r.kind === 'git-app')
      },
      problems,
      rows: rows.map((r) => ({ ...r, sites: r.sites }))
    },
    null,
    2
  ) + '\n'
)

console.log(
  `inventory: ${preloadCount} preload members (${byKind('invoke')} invoke, ${byKind('send')} send, ${byKind('event')} event, ${byKind('local')} local), ` +
    `${handlers.size} ipcMain handlers, ${emissionsByChannel.size} emitted events, ${gitMethods.size} git methods ` +
    `(${count((r) => r.kind === 'git-read')} read, ${count((r) => r.kind === 'git-write')} write, ${count((r) => r.kind === 'git-app')} app); ` +
    `${carried} rows carried progress${GO_SCAN ? `, ${goFindings.length} Go annotations` : ''}; ${problems.length} parser notes`
)
for (const p of problems) console.log(`  note: ${p}`)
