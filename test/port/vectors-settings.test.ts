import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, describe, expect, it, vi } from 'vitest'
import {
  AI_FEATURES,
  AI_PROVIDERS,
  defaultAIConfig,
  defaultSettings,
  type AIConfig,
  type AppSettings,
  type Profile
} from '../../src/shared/types'
import {
  TOKEN_FIELDS,
  applySecrets,
  extractSecrets,
  hasSettingsSecrets,
  mergeSecretStores,
  pruneSecrets,
  stripSettingsSecrets,
  type SecretStore
} from '../../src/shared/secrets'
import {
  accountEndpoint,
  accountFor,
  findAccount,
  migrateAIConfig,
  modelFor,
  resolveAI
} from '../../src/shared/aiAccounts'

/**
 * Port vectors for internal/shared (secrets, aiAccounts), internal/settings and
 * the defaults Go embeds (internal/shared/tsdefaults.json).
 *
 * PORT_WRITE_VECTORS=1 regenerates the files from the TS implementation;
 * otherwise this test recomputes everything and fails when the TS output no
 * longer matches what the Go tests replay — so both sides stay guarded.
 */

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const WRITE = process.env.PORT_WRITE_VECTORS === '1'

/** JSON semantics: undefined disappears, exactly what crosses the bridge. */
const norm = (v: unknown): unknown => (v === undefined ? null : JSON.parse(JSON.stringify(v)))

function check(rel: string, data: unknown): void {
  const file = join(ROOT, rel)
  const text = JSON.stringify(data, null, 2) + '\n'
  if (WRITE) {
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, text)
    return
  }
  expect(existsSync(file), `${rel} missing — run with PORT_WRITE_VECTORS=1`).toBe(true)
  expect(JSON.parse(readFileSync(file, 'utf-8'))).toEqual(JSON.parse(text))
}

// ─── fixtures, shared with test/pureLogic.test.ts ──────────────────────────

const profile = (id: string, over: Record<string, unknown> = {}): Profile => ({
  id,
  name: id,
  gitName: 'Dev',
  gitEmail: 'dev@example.com',
  githubToken: '',
  azureToken: '',
  gitlabToken: '',
  bitbucketToken: '',
  ai: { ...defaultSettings().profiles[0].ai },
  ...over
})
// A compact base keeps the vector file small; the full defaults are covered
// by tsdefaults.json and the settings scenarios.
const withProfiles = (...profiles: unknown[]): AppSettings =>
  ({ activeProfileId: 'p1', language: 'en', profiles }) as unknown as AppSettings

const baseAI = defaultSettings().profiles[0].ai
const withKey = {
  ...baseAI,
  accounts: baseAI.accounts.map((a) => ({ ...a, apiKey: 'sk-secret' })),
  apiKey: 'sk-secret'
}
const loaded = withProfiles(
  profile('p1', { githubToken: 'ghp_secret', ai: withKey }),
  profile('p2', { gitlabToken: 'glpat_secret' })
)
const twoAccounts = withProfiles(
  profile('p1', {
    ai: {
      ...baseAI,
      accounts: [
        { ...baseAI.accounts[0], id: 'work', apiKey: 'sk-work' },
        { ...baseAI.accounts[0], id: 'personal', provider: 'anthropic' as const, apiKey: 'sk-personal' }
      ]
    }
  })
)
const preAccounts = withProfiles(
  profile('p1', {
    ai: { enabled: true, provider: 'anthropic', endpoint: '', apiKey: 'sk-pre', model: 'claude-x' }
  })
)
const oddShapes = withProfiles(
  profile('p1', { ai: undefined }),
  profile('p2', { ai: null, azureToken: 'az' }),
  profile('p3', {
    ai: { ...baseAI, accountId: 'b', defaultAccountId: '', accounts: [{ id: 'a', apiKey: '' }, { id: 'b' }] }
  }),
  { name: 'no id', githubToken: 'ghp_noid', ai: { apiKey: 'sk-noid' } },
  profile('p5', { ai: { ...baseAI, accounts: [{ label: 'no id', apiKey: 'sk-x' }] } }),
  profile('p6', { bitbucketToken: 'bb', ai: { ...baseAI, accounts: undefined, apiKey: 'sk-6' } })
)

const settingsInputs: Record<string, unknown> = {
  loaded,
  twoAccounts,
  preAccounts,
  oddShapes,
  defaults: { profiles: defaultSettings().profiles },
  stripped: stripSettingsSecrets(loaded),
  noProfiles: { language: 'en' },
  nullProfiles: { profiles: null },
  emptyProfiles: { profiles: [] }
}

const stores: Record<string, SecretStore> = {
  empty: {},
  full: extractSecrets(loaded),
  twoAccounts: extractSecrets(twoAccounts),
  legacy: { p1: { aiApiKey: 'sk-legacy' } },
  githubOnly: { p1: { githubToken: 'ghp_secret' } },
  mixed: {
    p1: { githubToken: 'ghp_stored', aiKeys: { default: 'sk-stored' } },
    p2: { gitlabToken: 'glpat_stored', aiApiKey: 'sk-old' },
    p3: { aiKeys: { b: 'sk-b', a: 'sk-a' } },
    undefined: { githubToken: 'ghp_undef' },
    p6: { aiApiKey: 'sk-6-stored', azureToken: 'az6' }
  },
  orphan: { gone: { githubToken: 'ghp_gone' }, p1: { azureToken: 'az1' } }
}

function secretsVectors(): unknown {
  // Inputs are stored once and referenced by name, so the file stays reviewable.
  const settings: Record<string, unknown> = {}
  for (const [name, s] of Object.entries(settingsInputs)) settings[name] = norm(s)
  const cases: unknown[] = []
  cases.push({ fn: 'hasSettingsSecrets', settings: null, out: hasSettingsSecrets(null) })
  for (const [name, s] of Object.entries(settingsInputs)) {
    const input = s as AppSettings
    cases.push({ fn: 'hasSettingsSecrets', settings: name, out: hasSettingsSecrets(input) })
    cases.push({ fn: 'stripSettingsSecrets', settings: name, out: norm(stripSettingsSecrets(input)) })
    cases.push({ fn: 'extractSecrets', settings: name, out: norm(extractSecrets(input)) })
    for (const storeName of Object.keys(stores)) {
      cases.push({ fn: 'applySecrets', settings: name, store: storeName, out: norm(applySecrets(input, stores[storeName])) })
    }
  }
  for (const [a, stored] of Object.entries(stores)) {
    for (const [b, next] of Object.entries(stores)) {
      cases.push({ fn: 'mergeSecretStores', stored: a, next: b, out: norm(mergeSecretStores(stored, next)) })
    }
  }
  const idLists: string[][] = [[], ['p1'], ['p1', 'p2'], ['p2', 'p3', 'undefined'], ['gone']]
  for (const [name, store] of Object.entries(stores)) {
    for (const ids of idLists) {
      cases.push({ fn: 'pruneSecrets', store: name, ids, out: norm(pruneSecrets(store, ids)) })
    }
  }
  return { settings, stores, cases }
}

// ─── aiAccounts ────────────────────────────────────────────────────────────

const legacy = {
  enabled: true,
  provider: 'anthropic' as const,
  endpoint: 'https://api.anthropic.com/v1',
  apiKey: 'sk-ant-old',
  model: 'claude-3-5-haiku-latest',
  repoChatModel: 'claude-3-5-sonnet-latest',
  commitStyle: 'auto' as const,
  explainStyle: 'normal' as const,
  conflictStyle: 'clean' as const,
  branchNamingStyle: 'prefix/description' as const,
  customInstructions: '',
  generateDescription: true
}

const aiInputs: Record<string, unknown> = {
  undefined: undefined,
  empty: {},
  legacy,
  legacyMigrated: migrateAIConfig(legacy),
  twoAccounts: {
    ...legacy,
    accounts: [
      { id: 'work', label: 'Work', provider: 'openai', endpoint: '', apiKey: 'sk-work', model: 'gpt-4o-mini' },
      { id: 'home', label: 'Home', provider: 'anthropic', endpoint: '', apiKey: 'sk-home', model: 'claude-sonnet-5' }
    ],
    defaultAccountId: 'work',
    assignments: { chat: { accountId: 'home', model: 'claude-opus-5' } }
  },
  deletedAssignment: {
    ...legacy,
    accounts: [{ id: 'work', label: 'Work', provider: 'openai', endpoint: '', apiKey: '', model: 'gpt-4o-mini' }],
    defaultAccountId: 'work',
    assignments: { chat: { accountId: 'deleted', model: 'ghost-model' } }
  },
  defaultGone: {
    ...legacy,
    accounts: [{ id: 'only', label: 'Only', provider: 'openai', endpoint: '', apiKey: '', model: 'gpt-4o' }],
    defaultAccountId: 'gone'
  },
  anthropicV1: { provider: 'anthropic', endpoint: 'https://api.anthropic.com/v1' },
  customChat: { provider: 'custom', endpoint: 'https://gateway.internal/v1/chat/completions/' },
  google: { provider: 'google' },
  anthropicKey: { provider: 'anthropic', apiKey: 'sk-ant' },
  openaiKey: { provider: 'openai', apiKey: 'sk-oai' },
  openaiNoKey: { provider: 'openai', apiKey: '' },
  ollama: { provider: 'ollama', apiKey: '' },
  cli: { provider: 'cli', apiKey: '' },
  readOnlyOff: { repoChatReadOnly: false },
  aiJsonCustom: { provider: 'custom', endpoint: 'http://127.0.0.1:8000/v1', model: 'default' },
  aiJsonOpenai: { provider: 'openai', endpoint: 'http://127.0.0.1:8000/v1', apiKey: 'test-key', model: 'gpt-test' },
  aiJsonAnthropic: { provider: 'anthropic', endpoint: 'http://127.0.0.1:8000', apiKey: 'test-key', model: 'claude-test' },
  repoChatGroups: {
    accounts: [
      { id: 'work', label: 'Work', provider: 'openai', endpoint: '', apiKey: '', model: 'gpt-4o-mini' },
      { id: 'home', label: '', provider: 'anthropic', endpoint: '', apiKey: '', model: 'claude-sonnet-5' }
    ],
    defaultAccountId: 'work',
    assignments: { chat: { accountId: 'work', model: 'a-private-deployment' } }
  },
  // Edge shapes the migration must tolerate.
  blankProvider: { provider: '', endpoint: '  x  ', model: '  ' },
  unknownProvider: { provider: 'nope', apiKey: 'k' },
  nullFields: { provider: null, endpoint: null, apiKey: null, model: null, accounts: null },
  badAccounts: {
    accounts: [null, 3, 'x', { id: 7 }, { label: 'no id' }, { id: 'ok', provider: 'groq', endpoint: ' ', model: '' }],
    assignments: { chat: { accountId: 'ok', model: '  ' }, commit: 'x', review: { accountId: 'nope' } }
  },
  cliAccount: {
    accounts: [{ id: 'c', label: '', provider: 'cli', endpoint: '', apiKey: '', model: '', cli: 'claude', cliPath: '/x' }],
    cli: 'gemini',
    cliPath: '/old'
  },
  noModelAccount: {
    accounts: [{ id: 'n', provider: 'mistral' }],
    defaultAccountId: 'n',
    repoChatModel: 'kept-because-accounts-exist'
  },
  legacyChatWithAssignment: {
    ...legacy,
    assignments: { chat: { accountId: 'default', model: 'already' } }
  },
  weirdAssignments: { assignments: ['chat'], defaultAccountId: 5 },
  trailingSpaceModel: { provider: 'openai', model: ' gpt﻿ ' }
}

const features = [undefined, ...AI_FEATURES]

function aiVectors(): unknown {
  // Each input is migrated once; the resolution cases run on that migrated
  // config (referenced by name) so they test resolution alone.
  const inputs: Record<string, unknown> = {}
  const cases: unknown[] = []
  const raw = {
    // Unmigrated configs reach accountFor too (activeProfileAiKey after a read).
    accounts: [{ id: 'a', model: 'm' }, { id: 'b', model: '' }],
    defaultAccountId: 'b',
    assignments: { chat: { accountId: 'a', model: ' x ' }, commit: { accountId: 'zzz', model: 'y' } }
  }
  const configs: Record<string, AIConfig> = { raw: raw as never, none: {} as never }
  for (const [name, input] of Object.entries(aiInputs)) {
    const migrated = migrateAIConfig(input as never)
    inputs[name] = { input: norm(input), migrated: norm(migrated) }
    configs[name] = migrated
  }
  inputs['raw'] = { input: null, migrated: raw }
  inputs['none'] = { input: null, migrated: {} }
  for (const [name, ai] of Object.entries(configs)) {
    for (const feature of features) {
      const f = feature ?? null
      cases.push({ fn: 'accountFor', ai: name, feature: f, out: norm(accountFor(ai, feature)) })
      cases.push({ fn: 'modelFor', ai: name, feature: f, out: modelFor(ai, feature) })
      if (name !== 'raw' && name !== 'none') {
        cases.push({ fn: 'resolveAI', ai: name, feature: f, out: norm(resolveAI(ai, feature)) })
      }
    }
    for (const account of ai.accounts ?? []) {
      cases.push({ fn: 'accountEndpoint', ai: name, accountId: account.id, out: accountEndpoint(account) })
    }
    for (const id of [...(ai.accounts ?? []).map((a) => a.id), '', 'missing']) {
      cases.push({ fn: 'findAccount', ai: name, accountId: id, out: norm(findAccount(ai, id)) })
    }
  }
  return { inputs, cases }
}

// ─── internal/settings: the real settings.ts against a fake electron ──────

const h = vi.hoisted(() => {
  const state = {
    dir: '',
    available: true,
    hasWindow: true,
    answer: true,
    asks: [] as unknown[],
    handlers: new Map<string, (...args: unknown[]) => unknown>()
  }
  const win = {
    isDestroyed: (): boolean => false,
    once: (): void => {},
    webContents: {
      send: (_ch: string, payload: unknown): void => {
        state.asks.push(payload)
        void state.handlers.get('keychain:answer')?.(null, state.answer)
      }
    }
  }
  return { state, win }
})

vi.mock('electron', () => ({
  app: { getPath: (): string => h.state.dir, isPackaged: false },
  ipcMain: {
    handle: (ch: string, fn: (...args: unknown[]) => unknown): void => {
      h.state.handlers.set(ch, fn)
    }
  },
  dialog: {},
  BrowserWindow: {
    getFocusedWindow: (): unknown => (h.state.hasWindow ? h.win : null),
    getAllWindows: (): unknown[] => (h.state.hasWindow ? [h.win] : [])
  },
  // A transparent stand-in for the OS: "ENC:" + plaintext. The Go side seals
  // for real; scenarios compare decrypted content, never bytes.
  safeStorage: {
    isEncryptionAvailable: (): boolean => h.state.available,
    encryptString: (s: string): Buffer => Buffer.from('ENC:' + s),
    decryptString: (b: Buffer): string => {
      const s = b.toString('utf-8')
      if (!h.state.available || !s.startsWith('ENC:')) throw new Error('cannot decrypt')
      return s.slice(4)
    }
  },
  default: {}
}))

type Step =
  | { op: 'get' }
  | { op: 'unlock' }
  | { op: 'set'; settings: unknown }
  | { op: 'token'; host: string; repoPath?: string }
  | { op: 'aiKey'; accountId?: string }
  | { op: 'status' }
  | { op: 'keychainSet'; granted: boolean }
  | { op: 'env'; hasWindow?: boolean; answer?: boolean }

interface Scenario {
  name: string
  /** Raw settings file content, when one exists. */
  settingsFile?: string
  /** A readable encrypted store, or 'garbage' for one this build cannot read. */
  secrets?: SecretStore | 'garbage'
  consent?: unknown
  available?: boolean
  hasWindow?: boolean
  answer?: boolean
  forceOnboarding?: boolean
  steps: Step[]
}

const tokenSettings = (over: Record<string, unknown> = {}): unknown => ({
  ...defaultSettings(),
  profiles: [
    profile('p1', { githubToken: 'ghp_1', gitlabToken: '  glpat_1  ', ai: withKey }),
    profile('p2', { githubToken: 'ghp_2' })
  ],
  activeProfileId: 'p1',
  repoProfiles: { '/repo/bound': 'p2', '/repo/ghost': 'gone' },
  ...over
})
const plain = { ...defaultSettings(), activeProfileId: 'default', tabs: [{ path: '/x', name: 'x' }] }
const granted = { consent: 'granted', explained: true }

const scenarios: Scenario[] = [
  { name: 'no file reads the defaults', steps: [{ op: 'get' }, { op: 'status' }] },
  { name: 'unparseable file reads the defaults', settingsFile: '{ nope', steps: [{ op: 'get' }] },
  { name: 'trailing garbage is unparseable', settingsFile: '{"language":"it"} x', steps: [{ op: 'get' }] },
  {
    name: 'existing file without the onboarding key counts as onboarded',
    settingsFile: JSON.stringify({ language: 'it', extra: { kept: [1, 2.5, 'x'] } }),
    steps: [{ op: 'get' }]
  },
  {
    name: 'explicit onboardingCompleted false wins over the default true',
    settingsFile: JSON.stringify({ onboardingCompleted: false }),
    steps: [{ op: 'get' }]
  },
  { name: 'null settings file', settingsFile: 'null', steps: [{ op: 'get' }] },
  { name: 'number settings file', settingsFile: '42', steps: [{ op: 'get' }] },
  {
    name: 'pre-accounts profile migrates on read',
    settingsFile: JSON.stringify({
      profiles: [{ id: 'p', name: 'P', ai: { provider: 'groq', apiKey: '', repoChatModel: 'big' } }],
      activeProfileId: 'p'
    }),
    steps: [{ op: 'get' }]
  },
  {
    name: 'profiles null becomes an empty list',
    settingsFile: JSON.stringify({ profiles: null }),
    steps: [{ op: 'get' }]
  },
  {
    name: 'forced onboarding holds until the wizard completes',
    settingsFile: JSON.stringify(plain),
    forceOnboarding: true,
    steps: [
      { op: 'get' },
      { op: 'set', settings: { ...plain, onboardingCompleted: false } },
      { op: 'get' },
      { op: 'set', settings: { ...plain, onboardingCompleted: true } },
      { op: 'get' }
    ]
  },
  {
    name: 'saving a token with no window keeps it in memory only',
    hasWindow: false,
    steps: [{ op: 'set', settings: tokenSettings() }, { op: 'get' }, { op: 'status' }, { op: 'token', host: 'github' }]
  },
  {
    name: 'saving a token asks, then encrypts',
    steps: [
      { op: 'set', settings: tokenSettings() },
      { op: 'status' },
      { op: 'get' },
      { op: 'token', host: 'github' },
      { op: 'token', host: 'gitlab' },
      { op: 'token', host: 'github', repoPath: '/repo/bound' },
      { op: 'token', host: 'github', repoPath: '/repo/ghost' },
      { op: 'token', host: 'azure' },
      { op: 'aiKey' },
      { op: 'aiKey', accountId: 'default' },
      { op: 'aiKey', accountId: 'nope' }
    ]
  },
  {
    name: 'declining keeps tokens in memory and never asks again',
    answer: false,
    steps: [
      { op: 'set', settings: tokenSettings() },
      { op: 'status' },
      { op: 'get' },
      { op: 'set', settings: tokenSettings() },
      { op: 'unlock' },
      { op: 'token', host: 'github' }
    ]
  },
  {
    name: 'granted but keyring unavailable',
    consent: granted,
    available: false,
    steps: [{ op: 'status' }, { op: 'set', settings: tokenSettings() }, { op: 'unlock' }, { op: 'aiKey' }]
  },
  {
    name: 'a settings write with nothing sensitive never goes near the keychain',
    steps: [{ op: 'set', settings: plain }, { op: 'status' }]
  },
  {
    name: 'stored secrets stay encrypted until unlock',
    settingsFile: JSON.stringify(stripSettingsSecrets(tokenSettings() as AppSettings)),
    secrets: extractSecrets(tokenSettings() as AppSettings),
    consent: granted,
    steps: [{ op: 'get' }, { op: 'unlock' }, { op: 'get' }]
  },
  {
    name: 'an existing secrets file is adopted, explained once',
    settingsFile: JSON.stringify(stripSettingsSecrets(tokenSettings() as AppSettings)),
    secrets: extractSecrets(tokenSettings() as AppSettings),
    steps: [{ op: 'status' }, { op: 'unlock' }, { op: 'status' }]
  },
  {
    name: 'an adopted install that declines the explanation',
    settingsFile: JSON.stringify(stripSettingsSecrets(tokenSettings() as AppSettings)),
    secrets: extractSecrets(tokenSettings() as AppSettings),
    answer: false,
    steps: [{ op: 'unlock' }, { op: 'status' }, { op: 'token', host: 'github' }]
  },
  {
    name: 'a credential-less save from an un-hydrated renderer keeps the store',
    settingsFile: JSON.stringify(stripSettingsSecrets(tokenSettings() as AppSettings)),
    secrets: extractSecrets(tokenSettings() as AppSettings),
    consent: granted,
    steps: [
      { op: 'get' },
      { op: 'token', host: 'github' },
      { op: 'set', settings: stripSettingsSecrets(tokenSettings() as AppSettings) },
      { op: 'unlock' }
    ]
  },
  {
    name: 'a served renderer clearing its tokens deletes them',
    settingsFile: JSON.stringify(stripSettingsSecrets(tokenSettings() as AppSettings)),
    secrets: extractSecrets(tokenSettings() as AppSettings),
    consent: granted,
    steps: [
      { op: 'unlock' },
      { op: 'set', settings: stripSettingsSecrets(tokenSettings() as AppSettings) },
      { op: 'unlock' }
    ]
  },
  {
    name: 'an un-served save adds to the store instead of replacing it',
    settingsFile: JSON.stringify(stripSettingsSecrets(tokenSettings() as AppSettings)),
    secrets: extractSecrets(tokenSettings() as AppSettings),
    consent: granted,
    steps: [
      { op: 'get' },
      {
        op: 'set',
        settings: {
          ...(stripSettingsSecrets(tokenSettings() as AppSettings) as unknown as Record<string, unknown>),
          profiles: [profile('p1', { azureToken: 'az_new' }), profile('p2')]
        }
      },
      { op: 'unlock' }
    ]
  },
  {
    name: 'deleting a profile prunes its stored secrets',
    settingsFile: JSON.stringify(stripSettingsSecrets(tokenSettings() as AppSettings)),
    secrets: extractSecrets(tokenSettings() as AppSettings),
    consent: granted,
    steps: [
      { op: 'unlock' },
      { op: 'set', settings: { ...(tokenSettings() as Record<string, unknown>), profiles: [profile('p2', { githubToken: 'ghp_2' })] } },
      { op: 'unlock' }
    ]
  },
  {
    name: 'plaintext tokens in the file are moved into the store on unlock',
    settingsFile: JSON.stringify(tokenSettings()),
    secrets: { p1: { githubToken: 'ghp_stale', azureToken: 'az_old' }, gone: { githubToken: 'x' } },
    consent: granted,
    steps: [{ op: 'get' }, { op: 'unlock' }, { op: 'get' }]
  },
  {
    name: 'an unreadable store is backed up, never silently overwritten',
    settingsFile: JSON.stringify(plain),
    secrets: 'garbage',
    consent: granted,
    steps: [
      { op: 'unlock' },
      { op: 'set', settings: plain },
      { op: 'set', settings: tokenSettings() },
      { op: 'unlock' }
    ]
  },
  {
    name: 'no secrets file means no key and no question',
    settingsFile: JSON.stringify(plain),
    steps: [{ op: 'aiKey' }, { op: 'status' }]
  },
  {
    name: 'session tokens are persisted once access is granted from Settings',
    answer: false,
    steps: [
      { op: 'set', settings: tokenSettings() },
      { op: 'keychainSet', granted: true },
      { op: 'status' },
      { op: 'unlock' },
      { op: 'keychainSet', granted: false },
      { op: 'status' }
    ]
  },
  {
    name: 'consent file with junk reads as unset',
    consent: { consent: 'maybe', explained: 'yes' },
    hasWindow: false,
    steps: [{ op: 'status' }, { op: 'unlock' }]
  },
  {
    name: 'granted but unexplained still shows the explanation',
    consent: { consent: 'granted', explained: false },
    steps: [{ op: 'status' }, { op: 'set', settings: tokenSettings() }, { op: 'status' }]
  },
  {
    name: 'declined consent never asks',
    consent: { consent: 'declined', explained: true },
    steps: [{ op: 'set', settings: tokenSettings() }, { op: 'unlock' }, { op: 'status' }]
  }
]

/** A file the scenario left unparseable is recorded verbatim. */
function parseOr(text: string): unknown {
  try {
    return { json: JSON.parse(text) }
  } catch {
    return { raw: text }
  }
}

async function runScenario(sc: Scenario): Promise<unknown> {
  const dir = mkdtempSync(join(tmpdir(), 'gitcito-port-settings-'))
  try {
    Object.assign(h.state, {
      dir,
      available: sc.available ?? true,
      hasWindow: sc.hasWindow ?? true,
      answer: sc.answer ?? true,
      asks: []
    })
    h.state.handlers.clear()
    if (sc.settingsFile !== undefined) writeFileSync(join(dir, 'gitcito-settings.json'), sc.settingsFile)
    if (sc.secrets === 'garbage') writeFileSync(join(dir, 'gitcito-secrets.enc'), 'Z2FyYmFnZQ==')
    else if (sc.secrets)
      writeFileSync(join(dir, 'gitcito-secrets.enc'), Buffer.from('ENC:' + JSON.stringify(sc.secrets)).toString('base64'))
    if (sc.consent !== undefined) writeFileSync(join(dir, 'gitcito-keychain.json'), JSON.stringify(sc.consent))

    if (sc.forceOnboarding) process.env['GITCITO_FORCE_ONBOARDING'] = '1'
    else delete process.env['GITCITO_FORCE_ONBOARDING']
    vi.resetModules()
    const settingsMod = await import('../../src/main/settings')
    const keychainMod = await import('../../src/main/keychain')
    delete process.env['GITCITO_FORCE_ONBOARDING']
    settingsMod.registerSettingsHandlers()
    keychainMod.registerKeychainHandlers()
    const call = (ch: string, ...args: unknown[]): unknown => h.state.handlers.get(ch)!(null, ...args)

    const results: unknown[] = []
    for (const step of sc.steps) {
      switch (step.op) {
        case 'get':
          results.push(norm(await call('settings:get')))
          break
        case 'unlock':
          results.push(norm(await call('settings:unlock')))
          break
        case 'set':
          results.push(norm(await call('settings:set', step.settings)))
          break
        case 'token':
          results.push(norm(await settingsMod.activeProfileToken(step.host as never, step.repoPath)))
          break
        case 'aiKey':
          results.push(norm(await settingsMod.activeProfileAiKey(step.accountId)))
          break
        case 'status':
          results.push(norm(await call('keychain:status')))
          break
        case 'keychainSet':
          results.push(norm(await call('keychain:set', step.granted)))
          break
        case 'env':
          if (step.hasWindow !== undefined) h.state.hasWindow = step.hasWindow
          if (step.answer !== undefined) h.state.answer = step.answer
          results.push(null)
          break
      }
    }

    const read = (name: string): string | null =>
      existsSync(join(dir, name)) ? readFileSync(join(dir, name), 'utf-8') : null
    const settingsText = read('gitcito-settings.json')
    const secretsText = read('gitcito-secrets.enc')
    let secrets: unknown = null
    if (secretsText !== null) {
      const s = Buffer.from(secretsText, 'base64').toString('utf-8')
      secrets = s.startsWith('ENC:') ? JSON.parse(s.slice(4)) : 'unreadable'
    }
    const bak = read('gitcito-secrets.enc.bak')
    const consentText = read('gitcito-keychain.json')
    return {
      name: sc.name,
      setup: {
        settingsFile: sc.settingsFile ?? null,
        secrets: sc.secrets ?? null,
        consent: sc.consent ?? null,
        available: sc.available ?? true,
        hasWindow: sc.hasWindow ?? true,
        answer: sc.answer ?? true,
        forceOnboarding: sc.forceOnboarding ?? false
      },
      steps: sc.steps,
      results,
      asks: h.state.asks,
      final: {
        settings: settingsText === null ? null : parseOr(settingsText),
        secrets,
        // The backup holds the bytes this build could not read, verbatim.
        backup: bak === null ? null : bak === 'Z2FyYmFnZQ==' ? 'garbage' : 'other',
        consent: consentText === null ? null : JSON.parse(consentText)
      }
    }
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

// ─── the tests ─────────────────────────────────────────────────────────────

describe('port vectors — settings, secrets, aiAccounts', () => {
  afterAll(() => {
    delete process.env['GITCITO_FORCE_ONBOARDING']
  })

  it('tsdefaults.json matches the TS defaults', () => {
    check('internal/shared/tsdefaults.json', {
      defaultSettings: defaultSettings(),
      defaultAIConfig: defaultAIConfig(),
      aiProviders: AI_PROVIDERS,
      aiFeatures: AI_FEATURES,
      tokenFields: TOKEN_FIELDS
    })
  })

  it('secrets.json matches src/shared/secrets.ts', () => {
    check('testdata/vectors/secrets.json', secretsVectors())
  })

  it('aiAccounts.json matches src/shared/aiAccounts.ts', () => {
    check('testdata/vectors/aiAccounts.json', aiVectors())
  })

  it('settings.json matches src/main/settings.ts + keychain.ts', async () => {
    const out: unknown[] = []
    for (const sc of scenarios) out.push(await runScenario(sc))
    check('testdata/vectors/settings.json', { scenarios: out })
  })
})
