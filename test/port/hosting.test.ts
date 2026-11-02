import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { beforeAll, describe, expect, it, vi } from 'vitest'

/**
 * Port vectors for internal/hosting (src/main/hosting.ts + src/main/credentials.ts).
 *
 * Every `hosting:*` channel is driven through the real ipcMain handlers against
 * a fake `fetch` with canned responses and a fake `git credential` / `gh`
 * process. Each case records the exact requests the TS built (method, URL,
 * headers, body — the Authorization / PRIVATE-TOKEN header is reduced to its
 * scheme and a sha256, never the value), the credential subprocesses it
 * spawned, and the mapped result or error message. The Go tests replay the same
 * files against an httptest.Server and must produce identical requests and
 * identical results.
 *
 * PORT_WRITE_VECTORS=1 regenerates the files; otherwise this test recomputes
 * everything and fails when the TS output drifts from them.
 */

// Rate-limit messages format a reset time with toLocaleTimeString; pin the zone
// so the vector does not depend on the machine. The Go replay uses UTC too.
process.env.TZ = 'UTC'
// The credential env diff must show what credentials.ts sets, not what the
// shell running the tests happened to export.
delete process.env.GIT_TERMINAL_PROMPT
delete process.env.GCM_INTERACTIVE

type GhState = 'missing' | 'signed-out' | 'authenticated' | 'unknown'
/** What the fake helper answers for one `host/path` key: stdout, a sequence of them, or failure. */
type FillReply = string | null | Array<string | null>
interface Helper {
  fill?: Record<string, FillReply>
  gh?: GhState
}
interface ProcCall {
  file: string
  args: string[]
  env: Record<string, string>
  timeout: number
  input: string | null
}

const h = vi.hoisted(() => ({
  handlers: new Map<string, (...a: unknown[]) => unknown>(),
  opened: [] as string[],
  helper: { fill: {}, gh: 'missing' } as { fill: Record<string, unknown>; gh: string },
  procs: [] as Array<{ file: string; args: string[]; env: Record<string, string>; timeout: number; input: string | null }>
}))

vi.mock('electron', () => ({
  ipcMain: { handle: (channel: string, fn: (...a: unknown[]) => unknown) => h.handlers.set(channel, fn) },
  shell: {
    openExternal: async (url: string) => {
      h.opened.push(url)
    }
  }
}))

vi.mock('child_process', async () => {
  const { createHash: hash } = await import('node:crypto')
  const redact = (input: string): string =>
    input.replace(/^password=(.*)$/gm, (_m, v: string) => `password=<sha256:${hash('sha256').update(v).digest('hex')}>`)
  const envDiff = (env: Record<string, string | undefined> | undefined): Record<string, string> => {
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(env ?? {})) if (v !== undefined && process.env[k] !== v) out[k] = v
    return out
  }
  type Cb = (err: Error | null, stdout: string, stderr: string) => void
  const execFile = (file: string, args: string[], opts: unknown, cb?: Cb): unknown => {
    let callback = cb as Cb
    let o = opts as { env?: Record<string, string>; timeout?: number }
    if (typeof opts === 'function') {
      callback = opts as Cb
      o = {}
    }
    const call = { file, args: [...args], env: envDiff(o.env), timeout: o.timeout ?? 0, input: null as string | null }
    h.procs.push(call)
    let input = ''
    const finish = (): void => {
      if (file === 'gh') {
        switch (h.helper.gh) {
          case 'authenticated':
            return callback(null, '', '')
          case 'signed-out':
            return callback(Object.assign(new Error('Command failed: gh auth status'), { code: 1 }), '', '')
          case 'unknown':
            return callback(Object.assign(new Error('Command failed: gh auth status'), { killed: true }), '', '')
          default:
            return callback(Object.assign(new Error('spawn gh ENOENT'), { code: 'ENOENT' }), '', '')
        }
      }
      if (file === 'git' && args.includes('credential')) {
        const verb = args[args.length - 1]
        if (verb === 'reject') return callback(null, '', '')
        const fields: Record<string, string> = {}
        for (const line of input.split('\n')) {
          const eq = line.indexOf('=')
          if (eq > 0) fields[line.slice(0, eq)] = line.slice(eq + 1)
        }
        const fill = h.helper.fill
        const key = `${fields.host}/${fields.path}`
        let reply = key in fill ? fill[key] : fields.host in fill ? fill[fields.host] : null
        if (Array.isArray(reply)) reply = reply.length ? reply.shift() : null
        if (typeof reply !== 'string') {
          return callback(Object.assign(new Error('Command failed: git credential fill'), { code: 128 }), '', '')
        }
        return callback(null, reply, '')
      }
      callback(Object.assign(new Error(`unexpected spawn ${file}`), { code: 1 }), '', '')
    }
    const child = {
      stdin: {
        end: (data: string) => {
          input = data
          call.input = redact(data)
          setImmediate(finish)
        }
      }
    }
    if (file === 'gh') setImmediate(finish)
    return child
  }
  return { execFile, default: { execFile } }
})

import { registerHostingHandlers, parseRemoteUrl, azureAuth, githubCredentialMessage, providerBaseUrl, glJobToCheck, diffLineCounts } from '../../src/main/hosting'
import { clearCredentialCache, isJwt, parseCredential, providerOfHost } from '../../src/main/credentials'
import { buildStackSection, mergeStackSection } from '../../src/shared/stackPr'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const WRITE = process.env.PORT_WRITE_VECTORS === '1'

const norm = (v: unknown): unknown => (v === undefined ? null : JSON.parse(JSON.stringify(v)))
const sha256 = (s: string): string => createHash('sha256').update(s).digest('hex')

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

// ─── fake fetch ──────────────────────────────────────────────────────────────

/** One canned answer. `network` simulates a connection failure (fetch rejects). */
interface Canned {
  method: string
  url: string
  status?: number
  headers?: Record<string, string>
  /** A JSON value (stringified) or, with `raw`, the exact body text. */
  json?: unknown
  raw?: string
  network?: boolean
}

interface RecordedRequest {
  method: string
  url: string
  headers: Record<string, string>
  auth: { header: string; scheme: string | null; sha256: string } | null
  body: string | null
  unmatched?: boolean
}

let queues = new Map<string, Canned[]>()
let requests: RecordedRequest[] = []

const NULL_BODY = new Set([101, 204, 205, 304])

async function fakeFetch(input: unknown, init: RequestInit = {}): Promise<Response> {
  const url = new URL(String(input)).href
  const method = (init.method ?? 'GET').toUpperCase()
  const rec: RecordedRequest = { method, url, headers: {}, auth: null, body: null }
  for (const [k, v] of new Headers(init.headers as HeadersInit)) {
    if (k === 'authorization') rec.auth = { header: k, scheme: v.split(' ')[0], sha256: sha256(v) }
    else if (k === 'private-token') rec.auth = { header: k, scheme: null, sha256: sha256(v) }
    else rec.headers[k] = v
  }
  if (init.body != null) rec.body = String(init.body)
  requests.push(rec)

  const key = `${method} ${url}`
  const q = queues.get(key)
  const canned = q && q.length > 1 ? q.shift() : q?.[0]
  if (!canned) {
    rec.unmatched = true
    throw new TypeError('fetch failed')
  }
  if (canned.network) throw new TypeError('fetch failed')
  const status = canned.status ?? 200
  const body = NULL_BODY.has(status) ? null : canned.raw ?? (canned.json === undefined ? '' : JSON.stringify(canned.json))
  return new Response(body, {
    status,
    headers: { 'content-type': 'application/json', ...(canned.headers ?? {}) }
  })
}

// ─── cases ──────────────────────────────────────────────────────────────────

interface Case {
  name: string
  channel: string
  args: unknown[]
  helper?: Helper
  responses?: Canned[]
}

const GH_TOKEN = 'ghp_vectorGithubToken0001'
const GL_TOKEN = 'glpat-vectorGitlabToken01'
const BB_APP = 'bbuser:vector-app-password'
const BB_TOKEN = 'vector-bitbucket-access-token'
const ADO_PAT = 'vectorazurepat0123456789abcdefghij'
const ADO_JWT = 'eyJhbGciOiJSUzI1NiJ9.eyJhdWQiOiJhZG8ifQ.c2ln'

const GH = 'https://github.com/acme/app.git'
const GH_SSH = 'git@github.com:acme/app.git'
const GL = 'https://gitlab.com/acme/platform/app.git'
const BB = 'https://bitbucket.org/team/app.git'
const ADO = 'https://dev.azure.com/contoso/My%20Project/_git/app'
const ADO_SSH = 'git@ssh.dev.azure.com:v3/contoso/Proj/app'
const UNKNOWN = 'https://example.com/x/y.git'

const GHA = 'https://api.github.com/repos/acme/app'
const GLA = 'https://gitlab.com/api/v4/projects/acme%2Fplatform%2Fapp'
const ADOA = 'https://dev.azure.com/contoso/My%20Project/_apis/git/repositories/app'

/** A credential helper reply, as `git credential fill` prints it. */
const cred = (username: string, password: string): string =>
  `protocol=https\nhost=x\nusername=${username}\npassword=${password}\n`

const ghErr = (status: number, message: string): Partial<Canned> => ({ status, json: { message } })

const ghPull = (n: number, extra: Record<string, unknown> = {}): Record<string, unknown> => ({
  number: n,
  title: `PR ${n}`,
  draft: false,
  html_url: `https://github.com/acme/app/pull/${n}`,
  user: { login: `dev${n}` },
  head: { ref: `feat-${n}`, sha: `${n}`.repeat(40).slice(0, 40) },
  base: { ref: 'main' },
  ...extra
})

const T = { github: GH_TOKEN }

const CASES: Case[] = [
  // ── hosting:listRepos ──
  {
    name: 'github typed token',
    channel: 'hosting:listRepos',
    args: ['github', `  ${GH_TOKEN}  `],
    responses: [
      {
        method: 'GET',
        url: 'https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member',
        json: [
          { full_name: 'acme/app', clone_url: 'https://github.com/acme/app.git', private: true, description: 'App', owner: { avatar_url: 'https://a/1' } },
          { full_name: 'acme/lib', clone_url: 'https://github.com/acme/lib.git', private: false, description: null, owner: null },
          { full_name: 'acme/doc', clone_url: 'https://github.com/acme/doc.git', private: false, description: '', owner: { avatar_url: 'https://a/2' } }
        ]
      }
    ]
  },
  {
    name: 'github error status',
    channel: 'hosting:listRepos',
    args: ['github', GH_TOKEN],
    responses: [
      {
        method: 'GET',
        url: 'https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member',
        ...ghErr(500, 'boom')
      }
    ]
  },
  {
    name: 'github borrowed credential from git',
    channel: 'hosting:listRepos',
    args: ['github', ''],
    helper: { fill: { 'github.com/': cred('octo', 'gho_helperToken') } },
    responses: [
      {
        method: 'GET',
        url: 'https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member',
        json: []
      }
    ]
  },
  { name: 'github no credential, gh missing', channel: 'hosting:listRepos', args: ['github', ''], helper: { gh: 'missing' } },
  { name: 'github no credential, gh signed out', channel: 'hosting:listRepos', args: ['github', ''], helper: { gh: 'signed-out' } },
  { name: 'github no credential, gh authenticated', channel: 'hosting:listRepos', args: ['github', ' '], helper: { gh: 'authenticated' } },
  { name: 'github no credential, gh timed out', channel: 'hosting:listRepos', args: ['github', ''], helper: { gh: 'unknown' } },
  {
    name: 'github network failure',
    channel: 'hosting:listRepos',
    args: ['github', GH_TOKEN],
    responses: [
      {
        method: 'GET',
        url: 'https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member',
        network: true
      }
    ]
  },
  {
    name: 'github non-json 200 body',
    channel: 'hosting:listRepos',
    args: ['github', GH_TOKEN],
    responses: [
      {
        method: 'GET',
        url: 'https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member',
        raw: '<!DOCTYPE html><html><body>proxy</body></html>'
      }
    ]
  },
  {
    name: 'gitlab typed token',
    channel: 'hosting:listRepos',
    args: ['gitlab', GL_TOKEN],
    responses: [
      {
        method: 'GET',
        url: 'https://gitlab.com/api/v4/projects?membership=true&per_page=100&order_by=last_activity_at&simple=true',
        json: [
          { path_with_namespace: 'acme/app', http_url_to_repo: 'https://gitlab.com/acme/app.git', visibility: 'private', description: 'd', avatar_url: null, namespace: { avatar_url: 'https://ns/1' } },
          { path_with_namespace: 'acme/pub', http_url_to_repo: 'https://gitlab.com/acme/pub.git', visibility: 'public', description: null, avatar_url: 'https://p/2' },
          { path_with_namespace: 'acme/int', http_url_to_repo: 'https://gitlab.com/acme/int.git', visibility: 'internal', description: '', avatar_url: null }
        ]
      }
    ]
  },
  {
    name: 'gitlab borrowed credential is bearer',
    channel: 'hosting:listRepos',
    args: ['gitlab', undefined],
    helper: { fill: { 'gitlab.com': cred('oauth2', 'gl-oauth-helper') } },
    responses: [
      {
        method: 'GET',
        url: 'https://gitlab.com/api/v4/projects?membership=true&per_page=100&order_by=last_activity_at&simple=true',
        json: []
      }
    ]
  },
  {
    name: 'gitlab error status',
    channel: 'hosting:listRepos',
    args: ['gitlab', GL_TOKEN],
    responses: [
      {
        method: 'GET',
        url: 'https://gitlab.com/api/v4/projects?membership=true&per_page=100&order_by=last_activity_at&simple=true',
        status: 401,
        json: { message: '401 Unauthorized' }
      }
    ]
  },
  { name: 'gitlab no credential', channel: 'hosting:listRepos', args: ['gitlab', ''] },
  {
    name: 'bitbucket app password is basic',
    channel: 'hosting:listRepos',
    args: ['bitbucket', BB_APP],
    responses: [
      {
        method: 'GET',
        url: 'https://api.bitbucket.org/2.0/repositories?role=member&pagelen=100&sort=-updated_on',
        json: {
          values: [
            {
              full_name: 'team/app',
              is_private: true,
              description: '',
              links: {
                clone: [
                  { name: 'ssh', href: 'git@bitbucket.org:team/app.git' },
                  { name: 'https', href: 'https://bitbucket.org/team/app.git' }
                ],
                avatar: { href: 'https://bb/avatar' }
              }
            },
            { full_name: 'team/ssh-only', is_private: false, description: 'only ssh', links: { clone: [{ name: 'ssh', href: 'git@bitbucket.org:team/ssh-only.git' }] } },
            { full_name: 'team/none', is_private: false, description: 'x', links: { clone: [] } }
          ]
        }
      }
    ]
  },
  {
    name: 'bitbucket access token is bearer',
    channel: 'hosting:listRepos',
    args: ['bitbucket', BB_TOKEN],
    responses: [
      {
        method: 'GET',
        url: 'https://api.bitbucket.org/2.0/repositories?role=member&pagelen=100&sort=-updated_on',
        status: 403,
        json: { error: { message: 'nope' } }
      }
    ]
  },
  {
    name: 'bitbucket borrowed credential uses username',
    channel: 'hosting:listRepos',
    args: ['bitbucket', ''],
    helper: { fill: { 'bitbucket.org': cred('bbuser', 'bb-helper-secret') } },
    responses: [
      {
        method: 'GET',
        url: 'https://api.bitbucket.org/2.0/repositories?role=member&pagelen=100&sort=-updated_on',
        json: { values: [] }
      }
    ]
  },
  { name: 'bitbucket no credential', channel: 'hosting:listRepos', args: ['bitbucket', ''] },
  {
    name: 'azure organization repositories',
    channel: 'hosting:listRepos',
    args: ['azure', ADO_PAT, ' contoso '],
    responses: [
      {
        method: 'GET',
        url: 'https://dev.azure.com/contoso/_apis/git/repositories?api-version=7.1',
        json: {
          value: [
            { name: 'app', remoteUrl: 'https://contoso@dev.azure.com/contoso/Proj/_git/app', project: { name: 'Proj' } },
            { name: 'lib', remoteUrl: 'https://contoso@dev.azure.com/contoso/Other/_git/lib', project: { name: 'Other' } }
          ]
        }
      }
    ]
  },
  { name: 'azure without organization', channel: 'hosting:listRepos', args: ['azure', ADO_PAT, '  '] },
  { name: 'azure no credential', channel: 'hosting:listRepos', args: ['azure', '', 'contoso'] },
  {
    name: 'azure sign-in page with typed pat',
    channel: 'hosting:listRepos',
    args: ['azure', ADO_PAT, 'contoso'],
    responses: [
      {
        method: 'GET',
        url: 'https://dev.azure.com/contoso/_apis/git/repositories?api-version=7.1',
        status: 203,
        headers: { 'content-type': 'text/html' },
        raw: '<!DOCTYPE html><html><body>Sign In</body></html>'
      }
    ]
  },
  {
    name: 'azure jwt typed token is bearer',
    channel: 'hosting:listRepos',
    args: ['azure', ADO_JWT, 'contoso'],
    responses: [
      {
        method: 'GET',
        url: 'https://dev.azure.com/contoso/_apis/git/repositories?api-version=7.1',
        status: 404,
        json: { message: 'TF200016: The following project does not exist' }
      }
    ]
  },
  {
    name: 'azure borrowed credential retried after 401',
    channel: 'hosting:listRepos',
    args: ['azure', '', 'contoso'],
    helper: { fill: { 'dev.azure.com/contoso': [cred('me', 'stale-helper-token'), cred('me', ADO_JWT)] } },
    responses: [
      { method: 'GET', url: 'https://dev.azure.com/contoso/_apis/git/repositories?api-version=7.1', status: 401, raw: '' },
      {
        method: 'GET',
        url: 'https://dev.azure.com/contoso/_apis/git/repositories?api-version=7.1',
        json: { value: [{ name: 'app', remoteUrl: 'https://dev.azure.com/contoso/P/_git/app', project: { name: 'P' } }] }
      }
    ]
  },
  {
    name: 'azure borrowed credential rejected twice',
    channel: 'hosting:listRepos',
    args: ['azure', '', 'contoso'],
    helper: { fill: { 'dev.azure.com/contoso': [cred('me', 'stale-1'), null] } },
    responses: [{ method: 'GET', url: 'https://dev.azure.com/contoso/_apis/git/repositories?api-version=7.1', status: 403, raw: 'denied' }]
  },
  {
    name: 'azure server error not retried',
    channel: 'hosting:listRepos',
    args: ['azure', '', 'contoso'],
    helper: { fill: { 'dev.azure.com/contoso': cred('me', 'helper-ok') } },
    responses: [{ method: 'GET', url: 'https://dev.azure.com/contoso/_apis/git/repositories?api-version=7.1', status: 500, raw: 'upstream exploded' }]
  },
  {
    name: 'azure unparseable json',
    channel: 'hosting:listRepos',
    args: ['azure', ADO_PAT, 'contoso'],
    responses: [{ method: 'GET', url: 'https://dev.azure.com/contoso/_apis/git/repositories?api-version=7.1', raw: '{"value": [' }]
  },

  // ── hosting:listOwners ──
  {
    name: 'github user and orgs',
    channel: 'hosting:listOwners',
    args: ['github', GH_TOKEN],
    responses: [
      { method: 'GET', url: 'https://api.github.com/user', json: { login: 'octo', avatar_url: 'https://a/octo' } },
      { method: 'GET', url: 'https://api.github.com/user/orgs?per_page=100', json: [{ login: 'acme', avatar_url: 'https://a/acme' }] }
    ]
  },
  {
    name: 'github orgs failure is fatal',
    channel: 'hosting:listOwners',
    args: ['github', GH_TOKEN],
    responses: [
      { method: 'GET', url: 'https://api.github.com/user', json: { login: 'octo', avatar_url: 'https://a/octo' } },
      { method: 'GET', url: 'https://api.github.com/user/orgs?per_page=100', status: 403, json: { message: 'Resource not accessible by integration' } }
    ]
  },
  {
    name: 'github rate limited',
    channel: 'hosting:listOwners',
    args: ['github', GH_TOKEN],
    responses: [
      {
        method: 'GET',
        url: 'https://api.github.com/user',
        status: 403,
        headers: { 'x-ratelimit-remaining': '0', 'x-ratelimit-reset': '1700000000' },
        json: { message: 'API rate limit exceeded' }
      }
    ]
  },
  {
    name: 'github rate limit without reset',
    channel: 'hosting:listOwners',
    args: ['github', GH_TOKEN],
    responses: [
      { method: 'GET', url: 'https://api.github.com/user', status: 429, headers: { 'x-ratelimit-remaining': '0' }, raw: 'slow down' }
    ]
  },
  {
    name: 'gitlab user and groups',
    channel: 'hosting:listOwners',
    args: ['gitlab', GL_TOKEN],
    responses: [
      { method: 'GET', url: 'https://gitlab.com/api/v4/user', json: { id: 7, username: 'gl', avatar_url: null, namespace_id: 70 } },
      {
        method: 'GET',
        url: 'https://gitlab.com/api/v4/groups?min_access_level=30&per_page=100',
        json: [
          { id: 11, full_path: 'acme', avatar_url: 'https://g/11' },
          { id: 12, full_path: 'acme/platform', avatar_url: null }
        ]
      }
    ]
  },
  {
    name: 'bitbucket workspaces',
    channel: 'hosting:listOwners',
    args: ['bitbucket', BB_APP],
    responses: [
      {
        method: 'GET',
        url: 'https://api.bitbucket.org/2.0/workspaces?pagelen=100',
        json: { values: [{ slug: 'team', name: 'Team', links: { avatar: { href: 'https://bb/team' } } }, { slug: 'solo', name: 'Solo' }] }
      }
    ]
  },
  {
    name: 'bitbucket workspaces error',
    channel: 'hosting:listOwners',
    args: ['bitbucket', BB_APP],
    responses: [{ method: 'GET', url: 'https://api.bitbucket.org/2.0/workspaces?pagelen=100', status: 401, raw: '' }]
  },
  {
    name: 'azure projects',
    channel: 'hosting:listOwners',
    args: ['azure', ADO_PAT, 'contoso'],
    responses: [
      {
        method: 'GET',
        url: 'https://dev.azure.com/contoso/_apis/projects?api-version=7.1',
        json: { value: [{ id: 'p-1', name: 'Proj' }, { id: 'p-2', name: 'Other' }] }
      }
    ]
  },
  { name: 'azure without organization', channel: 'hosting:listOwners', args: ['azure', ADO_PAT] },
  { name: 'gitlab no credential', channel: 'hosting:listOwners', args: ['gitlab', ''] },

  // ── hosting:whoAmI ──
  {
    name: 'github account with orgs',
    channel: 'hosting:whoAmI',
    args: ['github', GH_TOKEN],
    responses: [
      { method: 'GET', url: 'https://api.github.com/user', json: { login: 'octo', name: null, avatar_url: 'https://a/octo', html_url: 'https://github.com/octo' } },
      { method: 'GET', url: 'https://api.github.com/user/orgs?per_page=100', json: [{ login: 'acme', avatar_url: 'https://a/acme' }] }
    ]
  },
  {
    name: 'github orgs failure is tolerated',
    channel: 'hosting:whoAmI',
    args: ['github', GH_TOKEN],
    responses: [
      { method: 'GET', url: 'https://api.github.com/user', json: { login: 'octo', name: 'Octo Cat', avatar_url: 'https://a/octo', html_url: 'https://github.com/octo' } },
      { method: 'GET', url: 'https://api.github.com/user/orgs?per_page=100', status: 500, raw: '' }
    ]
  },
  {
    name: 'github bad token',
    channel: 'hosting:whoAmI',
    args: ['github', GH_TOKEN],
    responses: [{ method: 'GET', url: 'https://api.github.com/user', status: 401, json: { message: 'Bad credentials' } }]
  },
  {
    name: 'github interactive sign-in asks the helper with prompts on',
    channel: 'hosting:whoAmI',
    args: ['github', '', undefined, true],
    helper: { fill: { 'github.com/': cred('octo', 'gho_interactive') } },
    responses: [
      { method: 'GET', url: 'https://api.github.com/user', json: { login: 'octo', name: 'O', avatar_url: 'https://a/o', html_url: 'https://github.com/octo' } },
      { method: 'GET', url: 'https://api.github.com/user/orgs?per_page=100', json: [] }
    ]
  },
  {
    name: 'gitlab account with groups',
    channel: 'hosting:whoAmI',
    args: ['gitlab', GL_TOKEN],
    responses: [
      { method: 'GET', url: 'https://gitlab.com/api/v4/user', json: { username: 'gl', name: 'G L', avatar_url: 'https://g/u', web_url: 'https://gitlab.com/gl' } },
      {
        method: 'GET',
        url: 'https://gitlab.com/api/v4/groups?min_access_level=10&per_page=100',
        json: [{ full_path: 'acme', avatar_url: null, web_url: 'https://gitlab.com/acme' }]
      }
    ]
  },
  {
    name: 'gitlab groups failure is tolerated',
    channel: 'hosting:whoAmI',
    args: ['gitlab', GL_TOKEN],
    responses: [
      { method: 'GET', url: 'https://gitlab.com/api/v4/user', json: { username: 'gl', name: null, avatar_url: null, web_url: 'https://gitlab.com/gl' } },
      { method: 'GET', url: 'https://gitlab.com/api/v4/groups?min_access_level=10&per_page=100', status: 500, raw: '' }
    ]
  },
  {
    name: 'gitlab user failure',
    channel: 'hosting:whoAmI',
    args: ['gitlab', GL_TOKEN],
    responses: [{ method: 'GET', url: 'https://gitlab.com/api/v4/user', status: 401, json: { message: '401 Unauthorized' } }]
  },
  {
    name: 'bitbucket account with workspaces',
    channel: 'hosting:whoAmI',
    args: ['bitbucket', BB_APP],
    responses: [
      {
        method: 'GET',
        url: 'https://api.bitbucket.org/2.0/user',
        json: { username: 'bbuser', display_name: 'BB User', links: { avatar: { href: 'https://bb/u' }, html: { href: 'https://bitbucket.org/bbuser' } } }
      },
      {
        method: 'GET',
        url: 'https://api.bitbucket.org/2.0/workspaces?pagelen=100',
        json: { values: [{ slug: 'team', name: 'Team', links: { avatar: { href: 'https://bb/t' }, html: { href: 'https://bitbucket.org/team/' } } }, { slug: 'solo', name: 'Solo' }] }
      }
    ]
  },
  {
    name: 'bitbucket workspaces failure is tolerated',
    channel: 'hosting:whoAmI',
    args: ['bitbucket', BB_TOKEN],
    responses: [
      { method: 'GET', url: 'https://api.bitbucket.org/2.0/user', json: { username: 'bbuser', display_name: null } },
      { method: 'GET', url: 'https://api.bitbucket.org/2.0/workspaces?pagelen=100', status: 500, raw: '' }
    ]
  },
  {
    name: 'bitbucket user failure',
    channel: 'hosting:whoAmI',
    args: ['bitbucket', BB_TOKEN],
    responses: [{ method: 'GET', url: 'https://api.bitbucket.org/2.0/user', status: 401, raw: '' }]
  },
  {
    name: 'azure with organization',
    channel: 'hosting:whoAmI',
    args: ['azure', ADO_PAT, 'contoso'],
    responses: [
      {
        method: 'GET',
        url: 'https://dev.azure.com/contoso/_apis/connectionData?api-version=7.1-preview',
        json: { authenticatedUser: { providerDisplayName: 'Jane', properties: { Account: { $value: 'jane@contoso.com' } } } }
      }
    ]
  },
  {
    name: 'azure with organization, anonymous user',
    channel: 'hosting:whoAmI',
    args: ['azure', ADO_PAT, 'My Org'],
    responses: [{ method: 'GET', url: 'https://dev.azure.com/My%20Org/_apis/connectionData?api-version=7.1-preview', json: {} }]
  },
  {
    name: 'azure with organization rejected',
    channel: 'hosting:whoAmI',
    args: ['azure', ADO_PAT, 'contoso'],
    responses: [{ method: 'GET', url: 'https://dev.azure.com/contoso/_apis/connectionData?api-version=7.1-preview', status: 401, raw: '' }]
  },
  {
    name: 'azure with organization not found',
    channel: 'hosting:whoAmI',
    args: ['azure', ADO_PAT, 'contoso'],
    responses: [{ method: 'GET', url: 'https://dev.azure.com/contoso/_apis/connectionData?api-version=7.1-preview', status: 404, raw: '' }]
  },
  {
    name: 'azure with organization api message is kept',
    channel: 'hosting:whoAmI',
    args: ['azure', ADO_PAT, 'contoso'],
    responses: [
      { method: 'GET', url: 'https://dev.azure.com/contoso/_apis/connectionData?api-version=7.1-preview', status: 400, json: { message: 'bad request' } }
    ]
  },
  {
    name: 'azure without organization uses the profile service',
    channel: 'hosting:whoAmI',
    args: ['azure', ADO_PAT],
    responses: [
      {
        method: 'GET',
        url: 'https://app.vssps.visualstudio.com/_apis/profile/profiles/me?api-version=7.1',
        json: { displayName: 'Jane', emailAddress: 'jane@contoso.com', id: 'u1' }
      }
    ]
  },
  {
    name: 'azure without organization rejected',
    channel: 'hosting:whoAmI',
    args: ['azure', ADO_PAT, ''],
    responses: [
      { method: 'GET', url: 'https://app.vssps.visualstudio.com/_apis/profile/profiles/me?api-version=7.1', status: 203, raw: '<html>' }
    ]
  },
  {
    name: 'azure no credential',
    channel: 'hosting:whoAmI',
    args: ['azure', '', 'contoso', false]
  },

  // ── hosting:createRepo ──
  { name: 'empty name', channel: 'hosting:createRepo', args: ['github', GH_TOKEN, { owner: 'octo', ownerType: 'user', name: '  ', private: true }] },
  {
    name: 'github user repo',
    channel: 'hosting:createRepo',
    args: ['github', GH_TOKEN, { owner: 'octo', ownerType: 'user', name: 'new-app', description: '', private: true }],
    responses: [
      {
        method: 'POST',
        url: 'https://api.github.com/user/repos',
        status: 201,
        json: { full_name: 'octo/new-app', clone_url: 'https://github.com/octo/new-app.git', private: true }
      }
    ]
  },
  {
    name: 'github org repo',
    channel: 'hosting:createRepo',
    args: ['github', GH_TOKEN, { owner: 'acme corp', ownerType: 'org', name: 'svc', description: 'A <service> & "more"', private: false }],
    responses: [
      {
        method: 'POST',
        url: 'https://api.github.com/orgs/acme%20corp/repos',
        status: 201,
        json: { full_name: 'acme/svc', clone_url: 'https://github.com/acme/svc.git', private: false }
      }
    ]
  },
  {
    name: 'github validation failure',
    channel: 'hosting:createRepo',
    args: ['github', GH_TOKEN, { owner: 'octo', ownerType: 'user', name: 'dup', private: false }],
    responses: [{ method: 'POST', url: 'https://api.github.com/user/repos', status: 422, json: { message: 'Repository creation failed.' } }]
  },
  {
    name: 'gitlab project in namespace',
    channel: 'hosting:createRepo',
    args: ['gitlab', GL_TOKEN, { owner: 'acme', ownerType: 'org', ownerId: '42', name: 'svc', description: 'Svc', private: true }],
    responses: [
      {
        method: 'POST',
        url: 'https://gitlab.com/api/v4/projects',
        status: 201,
        json: { path_with_namespace: 'acme/svc', http_url_to_repo: 'https://gitlab.com/acme/svc.git', visibility: 'private' }
      }
    ]
  },
  {
    name: 'gitlab personal project',
    channel: 'hosting:createRepo',
    args: ['gitlab', GL_TOKEN, { owner: 'gl', ownerType: 'user', name: 'mine', private: false }],
    responses: [
      {
        method: 'POST',
        url: 'https://gitlab.com/api/v4/projects',
        status: 201,
        json: { path_with_namespace: 'gl/mine', http_url_to_repo: 'https://gitlab.com/gl/mine.git', visibility: 'public' }
      }
    ]
  },
  {
    name: 'gitlab structured error message',
    channel: 'hosting:createRepo',
    args: ['gitlab', GL_TOKEN, { owner: 'gl', ownerType: 'user', ownerId: 'abc', name: 'mine', private: false }],
    responses: [
      {
        method: 'POST',
        url: 'https://gitlab.com/api/v4/projects',
        status: 400,
        json: { message: { name: ['has already been taken'], path: ['has already been taken', 'é <b>'] } }
      }
    ]
  },
  {
    name: 'gitlab error without message',
    channel: 'hosting:createRepo',
    args: ['gitlab', GL_TOKEN, { owner: 'gl', ownerType: 'user', name: 'mine', private: false }],
    responses: [{ method: 'POST', url: 'https://gitlab.com/api/v4/projects', status: 500, raw: 'oops' }]
  },
  {
    name: 'bitbucket repo slug',
    channel: 'hosting:createRepo',
    args: ['bitbucket', BB_APP, { owner: 'team', ownerType: 'org', name: ' My  New\tRepo ', description: 'x', private: true }],
    responses: [
      {
        method: 'POST',
        url: 'https://api.bitbucket.org/2.0/repositories/team/my-new-repo',
        json: {
          full_name: 'team/my-new-repo',
          is_private: true,
          links: { clone: [{ name: 'https', href: 'https://bbuser@bitbucket.org/team/my-new-repo.git' }] }
        }
      }
    ]
  },
  {
    name: 'bitbucket error',
    channel: 'hosting:createRepo',
    args: ['bitbucket', BB_APP, { owner: 'team', ownerType: 'org', name: 'x', private: false }],
    responses: [{ method: 'POST', url: 'https://api.bitbucket.org/2.0/repositories/team/x', status: 400, json: { error: { message: 'exists' } } }]
  },
  {
    name: 'azure repo in project',
    channel: 'hosting:createRepo',
    args: ['azure', ADO_PAT, { owner: 'p-1', ownerType: 'org', project: 'My Proj', name: 'svc', private: true }, 'contoso'],
    responses: [
      {
        method: 'POST',
        url: 'https://dev.azure.com/contoso/My%20Proj/_apis/git/repositories?api-version=7.1',
        status: 201,
        json: { name: 'svc', remoteUrl: 'https://contoso@dev.azure.com/contoso/My%20Proj/_git/svc', project: { name: 'My Proj' } }
      }
    ]
  },
  { name: 'azure missing project', channel: 'hosting:createRepo', args: ['azure', ADO_PAT, { owner: 'p-1', ownerType: 'org', name: 'svc', private: true }, 'contoso'] },
  { name: 'azure missing organization', channel: 'hosting:createRepo', args: ['azure', ADO_PAT, { owner: 'p-1', ownerType: 'org', project: 'P', name: 'svc', private: true }] },
  { name: 'github no credential', channel: 'hosting:createRepo', args: ['github', '', { owner: 'o', ownerType: 'user', name: 'n', private: true }], helper: { gh: 'signed-out' } },

  // ── hosting:listPRs ──
  { name: 'unknown remote', channel: 'hosting:listPRs', args: [UNKNOWN, {}] },
  { name: 'gitlab without credential is empty', channel: 'hosting:listPRs', args: [GL, {}] },
  {
    name: 'gitlab merge requests',
    channel: 'hosting:listPRs',
    args: [GL, { gitlab: GL_TOKEN, github: GH_TOKEN }],
    responses: [
      {
        method: 'GET',
        url: `${GLA}/merge_requests?state=opened&per_page=30`,
        json: [
          { iid: 3, title: 'MR 3', author: { username: 'gl' }, source_branch: 'f', target_branch: 'main', web_url: 'https://gitlab.com/x/-/merge_requests/3', draft: true },
          { iid: 4, title: 'MR 4', author: null, source_branch: 'g', target_branch: 'main', web_url: 'https://gitlab.com/x/-/merge_requests/4', work_in_progress: true },
          { iid: 5, title: 'MR 5', author: { username: 'z' }, source_branch: 'h', target_branch: 'dev', web_url: 'https://gitlab.com/x/-/merge_requests/5' }
        ]
      }
    ]
  },
  {
    name: 'gitlab error',
    channel: 'hosting:listPRs',
    args: [GL, { gitlab: GL_TOKEN }],
    responses: [{ method: 'GET', url: `${GLA}/merge_requests?state=opened&per_page=30`, status: 404, json: { message: '404 Project Not Found' } }]
  },
  {
    name: 'bitbucket pull requests',
    channel: 'hosting:listPRs',
    args: [BB, { bitbucket: BB_APP }],
    responses: [
      {
        method: 'GET',
        url: 'https://api.bitbucket.org/2.0/repositories/team/app/pullrequests?state=OPEN&pagelen=30',
        json: {
          values: [
            { id: 9, title: 'BB 9', author: { display_name: 'Ann' }, source: { branch: { name: 'f' } }, destination: { branch: { name: 'main' } }, links: { html: { href: 'https://bitbucket.org/team/app/pull-requests/9' } } },
            { id: 10, title: 'BB 10', author: null, source: null, destination: { branch: null }, links: { html: { href: 'https://bitbucket.org/team/app/pull-requests/10' } } }
          ]
        }
      }
    ]
  },
  {
    name: 'bitbucket error',
    channel: 'hosting:listPRs',
    args: [BB, { bitbucket: BB_TOKEN }],
    responses: [{ method: 'GET', url: 'https://api.bitbucket.org/2.0/repositories/team/app/pullrequests?state=OPEN&pagelen=30', status: 500, raw: '' }]
  },
  { name: 'bitbucket without credential is empty', channel: 'hosting:listPRs', args: [BB, { github: GH_TOKEN }] },
  {
    name: 'github anonymous skips stacks and checks',
    channel: 'hosting:listPRs',
    args: [GH_SSH, {}],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/pulls?state=open&per_page=30`,
        json: [ghPull(1, { draft: true }), ghPull(2, { stack: { number: 7 } })]
      }
    ]
  },
  {
    name: 'github with stacks and checks',
    channel: 'hosting:listPRs',
    args: [GH, T],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/pulls?state=open&per_page=30`,
        json: [ghPull(1), ghPull(2, { stack: { number: 7 } }), ghPull(3, { stack: { number: 7 } }), ghPull(4, { stack: null })]
      },
      {
        method: 'GET',
        url: `${GHA}/stacks/7`,
        json: {
          pull_requests: [
            { number: 2 },
            { number: 5, title: 'Bottom', html_url: 'https://github.com/acme/app/pull/5', draft: false, state: 'closed', merged_at: '2024-01-01T00:00:00Z', user: { login: 'dev5' }, head: { ref: 'feat-5', sha: 'e'.repeat(40) }, base: { ref: 'main' } },
            { number: 6, state: 'closed', merged_at: null },
            { number: 0 },
            null,
            { number: 8, state: 'open' }
          ]
        }
      },
      {
        method: 'GET',
        url: `${GHA}/commits/${'1'.repeat(40)}/check-runs?per_page=30`,
        json: {
          check_runs: [
            { name: 'build', status: 'completed', conclusion: 'success', html_url: 'https://ci/1' },
            { name: 'lint', status: 'completed', conclusion: 'failure', html_url: 'https://ci/2' },
            { name: 'e2e', status: 'in_progress', conclusion: null, html_url: 'https://ci/3' },
            { name: 'docs', status: 'completed', conclusion: 'skipped', html_url: 'https://ci/4' }
          ]
        }
      },
      {
        method: 'GET',
        url: `${GHA}/commits/${'2'.repeat(40)}/check-runs?per_page=30`,
        json: { check_runs: [{ name: 'build', status: 'queued', conclusion: null, html_url: 'https://ci/5' }] }
      },
      { method: 'GET', url: `${GHA}/commits/${'3'.repeat(40)}/check-runs?per_page=30`, json: { check_runs: [] } },
      { method: 'GET', url: `${GHA}/commits/${'4'.repeat(40)}/check-runs?per_page=30`, status: 500, raw: '' },
      {
        method: 'GET',
        url: `${GHA}/commits/${'e'.repeat(40)}/check-runs?per_page=30`,
        json: { check_runs: [{ name: 'a', status: 'completed', conclusion: 'neutral', html_url: 'https://ci/6' }] }
      }
    ]
  },
  {
    name: 'github stack lookup failure is ignored',
    channel: 'hosting:listPRs',
    args: [GH, T],
    responses: [
      { method: 'GET', url: `${GHA}/pulls?state=open&per_page=30`, json: [ghPull(1, { stack: { number: 3 } })] },
      { method: 'GET', url: `${GHA}/stacks/3`, status: 404, json: { message: 'Not Found' } },
      { method: 'GET', url: `${GHA}/commits/${'1'.repeat(40)}/check-runs?per_page=30`, network: true }
    ]
  },
  {
    name: 'github error',
    channel: 'hosting:listPRs',
    args: [GH, T],
    responses: [{ method: 'GET', url: `${GHA}/pulls?state=open&per_page=30`, status: 404, json: { message: 'Not Found' } }]
  },
  {
    name: 'azure pull requests',
    channel: 'hosting:listPRs',
    args: [ADO, { azure: ADO_PAT }],
    responses: [
      {
        method: 'GET',
        url: `${ADOA}/pullrequests?searchCriteria.status=active&api-version=7.1`,
        json: {
          value: [
            { pullRequestId: 12, title: 'ADO 12', isDraft: false, createdBy: { displayName: 'Jane' }, sourceRefName: 'refs/heads/feat/x', targetRefName: 'refs/heads/main' },
            { pullRequestId: 13, title: 'ADO 13', isDraft: true, createdBy: { displayName: 'Joe' }, sourceRefName: 'refs/heads/refs/heads/odd', targetRefName: 'main' }
          ]
        }
      }
    ]
  },
  {
    name: 'azure ssh remote',
    channel: 'hosting:listPRs',
    args: [ADO_SSH, { azure: ADO_PAT }],
    responses: [
      { method: 'GET', url: 'https://dev.azure.com/contoso/Proj/_apis/git/repositories/app/pullrequests?searchCriteria.status=active&api-version=7.1', json: { value: [] } }
    ]
  },
  { name: 'azure no credential', channel: 'hosting:listPRs', args: [ADO, {}] },
  { name: 'azure ssh no credential', channel: 'hosting:listPRs', args: [ADO_SSH, {}] },
  { name: 'azure malformed project encoding', channel: 'hosting:listPRs', args: ['https://dev.azure.com/o/%E0%A4%A/_git/r', {}] },

  // ── hosting:listReleases ──
  { name: 'non-github remote', channel: 'hosting:listReleases', args: [GL, T] },
  { name: 'unknown remote', channel: 'hosting:listReleases', args: [UNKNOWN, T] },
  {
    name: 'github releases',
    channel: 'hosting:listReleases',
    args: [GH, T],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/releases?per_page=50`,
        json: [
          { id: 1, tag_name: 'v1.0.0', name: 'One', body: 'notes', draft: false, prerelease: false, published_at: '2024-01-01T00:00:00Z', html_url: 'https://github.com/acme/app/releases/tag/v1.0.0' },
          { id: 2, tag_name: '', name: null, body: null, draft: true, prerelease: true, published_at: null, html_url: 'https://github.com/acme/app/releases/2' }
        ]
      }
    ]
  },
  {
    name: 'github releases anonymous',
    channel: 'hosting:listReleases',
    args: [GH, {}],
    responses: [{ method: 'GET', url: `${GHA}/releases?per_page=50`, json: [] }]
  },
  {
    name: 'github releases error',
    channel: 'hosting:listReleases',
    args: [GH, T],
    responses: [{ method: 'GET', url: `${GHA}/releases?per_page=50`, status: 403, json: { message: 'Forbidden' } }]
  },

  // ── hosting:ciStatuses ──
  { name: 'non-github remote', channel: 'hosting:ciStatuses', args: [GL, ['a'.repeat(40)], GH_TOKEN] },
  { name: 'no credential', channel: 'hosting:ciStatuses', args: [GH, ['a'.repeat(40)], ''] },
  {
    name: 'mixed states',
    channel: 'hosting:ciStatuses',
    args: [GH, ['a'.repeat(40), 'b'.repeat(40), 'c'.repeat(40), 'd'.repeat(40)], GH_TOKEN],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/commits/${'a'.repeat(40)}/check-runs?per_page=30`,
        json: { check_runs: [{ name: 'x', status: 'completed', conclusion: 'success', html_url: 'https://ci/a' }] }
      },
      {
        method: 'GET',
        url: `${GHA}/commits/${'b'.repeat(40)}/check-runs?per_page=30`,
        json: {
          check_runs: [
            { name: 'x', status: 'completed', conclusion: 'neutral', html_url: 'https://ci/b1' },
            { name: 'y', status: 'completed', conclusion: 'skipped', html_url: 'https://ci/b2' }
          ]
        }
      },
      {
        method: 'GET',
        url: `${GHA}/commits/${'c'.repeat(40)}/check-runs?per_page=30`,
        json: {
          check_runs: [
            { name: 'x', status: 'completed', conclusion: 'timed_out', html_url: 'https://ci/c1' },
            { name: 'y', status: 'in_progress', conclusion: null, html_url: 'https://ci/c2' }
          ]
        }
      },
      { method: 'GET', url: `${GHA}/commits/${'d'.repeat(40)}/check-runs?per_page=30`, raw: 'not json at all' }
    ]
  },
  {
    name: 'only the first forty shas',
    channel: 'hosting:ciStatuses',
    args: [GH, Array.from({ length: 42 }, (_, i) => i.toString(16).padStart(40, '0')), GH_TOKEN],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/commits/${'0'.repeat(39)}1/check-runs?per_page=30`,
        json: { check_runs: [{ name: 'x', status: 'completed', conclusion: 'success', html_url: 'https://ci/1' }] }
      }
    ]
  },

  // ── hosting:openCreatePR ──
  { name: 'github', channel: 'hosting:openCreatePR', args: [GH, 'feat/a b', 'main'] },
  { name: 'gitlab', channel: 'hosting:openCreatePR', args: [GL, 'feat/a b', 'main&x'] },
  { name: 'bitbucket', channel: 'hosting:openCreatePR', args: [BB, 'feat/ä', 'main'] },
  { name: 'azure', channel: 'hosting:openCreatePR', args: [ADO, 'feat/x', 'main'] },
  { name: 'unknown', channel: 'hosting:openCreatePR', args: [UNKNOWN, 'a', 'b'] },

  // ── hosting:createPR ──
  { name: 'unknown remote', channel: 'hosting:createPR', args: [UNKNOWN, T, { title: 't', body: 'b', source: 's', target: 'main', draft: false }] },
  {
    name: 'gitlab draft',
    channel: 'hosting:createPR',
    args: [GL, { gitlab: GL_TOKEN }, { title: 'Add x', body: 'Body\nline', source: 'feat', target: 'main', draft: true }],
    responses: [{ method: 'POST', url: `${GLA}/merge_requests`, status: 201, json: { iid: 8, web_url: 'https://gitlab.com/acme/platform/app/-/merge_requests/8' } }]
  },
  {
    name: 'gitlab error message',
    channel: 'hosting:createPR',
    args: [GL, { gitlab: GL_TOKEN }, { title: 'Add x', body: '', source: 'feat', target: 'main', draft: false }],
    responses: [{ method: 'POST', url: `${GLA}/merge_requests`, status: 409, json: { message: ['Another open merge request already exists'] } }]
  },
  {
    name: 'gitlab error status',
    channel: 'hosting:createPR',
    args: [GL, { gitlab: GL_TOKEN }, { title: 'Add x', body: '', source: 'feat', target: 'main', draft: false }],
    responses: [{ method: 'POST', url: `${GLA}/merge_requests`, status: 500, raw: '' }]
  },
  { name: 'gitlab no credential', channel: 'hosting:createPR', args: [GL, T, { title: 't', body: 'b', source: 's', target: 'main', draft: false }] },
  {
    name: 'bitbucket',
    channel: 'hosting:createPR',
    args: [BB, { bitbucket: BB_APP }, { title: 'BB', body: 'desc', source: 'feat', target: 'main', draft: true }],
    responses: [
      {
        method: 'POST',
        url: 'https://api.bitbucket.org/2.0/repositories/team/app/pullrequests',
        status: 201,
        json: { id: 4, links: { html: { href: 'https://bitbucket.org/team/app/pull-requests/4' } } }
      }
    ]
  },
  {
    name: 'bitbucket error',
    channel: 'hosting:createPR',
    args: [BB, { bitbucket: BB_APP }, { title: 'BB', body: 'desc', source: 'feat', target: 'main', draft: false }],
    responses: [{ method: 'POST', url: 'https://api.bitbucket.org/2.0/repositories/team/app/pullrequests', status: 400, json: { error: { message: 'There are no changes' } } }]
  },
  {
    name: 'bitbucket error without message',
    channel: 'hosting:createPR',
    args: [BB, { bitbucket: BB_APP }, { title: 'BB', body: 'desc', source: 'feat', target: 'main', draft: false }],
    responses: [{ method: 'POST', url: 'https://api.bitbucket.org/2.0/repositories/team/app/pullrequests', status: 400, json: { error: {} } }]
  },
  {
    name: 'github',
    channel: 'hosting:createPR',
    args: [GH, T, { title: 'Feat "x" <y>', body: 'Body   sep', source: 'feat', target: 'main', draft: false }],
    responses: [{ method: 'POST', url: `${GHA}/pulls`, status: 201, json: { html_url: 'https://github.com/acme/app/pull/9', number: 9 } }]
  },
  {
    name: 'github head not pushed',
    channel: 'hosting:createPR',
    args: [GH, T, { title: 't', body: 'b', source: 'feat', target: 'main', draft: true }],
    responses: [
      {
        method: 'POST',
        url: `${GHA}/pulls`,
        status: 422,
        json: { message: 'Validation Failed', errors: [{ resource: 'PullRequest', field: 'head', code: 'invalid' }, { field: 'base', code: 'invalid' }, { field: 'x', code: 'custom' }, { code: 'missing' }, {}] }
      }
    ]
  },
  {
    name: 'github error messages listed',
    channel: 'hosting:createPR',
    args: [GH, T, { title: 't', body: 'b', source: 'feat', target: 'main', draft: false }],
    responses: [
      { method: 'POST', url: `${GHA}/pulls`, status: 422, json: { errors: [{ message: 'A pull request already exists for acme:feat.' }] } }
    ]
  },
  {
    name: 'github error headline only',
    channel: 'hosting:createPR',
    args: [GH, T, { title: 't', body: 'b', source: 'feat', target: 'main', draft: false }],
    responses: [{ method: 'POST', url: `${GHA}/pulls`, status: 404, json: { message: 'Not Found' } }]
  },
  {
    name: 'github error not json',
    channel: 'hosting:createPR',
    args: [GH, T, { title: 't', body: 'b', source: 'feat', target: 'main', draft: false }],
    responses: [{ method: 'POST', url: `${GHA}/pulls`, status: 502, raw: '<html>bad gateway</html>' }]
  },
  { name: 'github no credential', channel: 'hosting:createPR', args: [GH, {}, { title: 't', body: 'b', source: 's', target: 'main', draft: false }], helper: { gh: 'authenticated' } },
  {
    name: 'azure',
    channel: 'hosting:createPR',
    args: [ADO, { azure: ADO_PAT }, { title: 'ADO', body: 'desc', source: 'feat/x', target: 'main', draft: true }],
    responses: [{ method: 'POST', url: `${ADOA}/pullrequests?api-version=7.1`, status: 201, json: { pullRequestId: 77 } }]
  },
  { name: 'azure no credential', channel: 'hosting:createPR', args: [ADO, {}, { title: 't', body: 'b', source: 's', target: 'main', draft: false }] },

  // ── hosting:prDetail ──
  {
    name: 'github full detail',
    channel: 'hosting:prDetail',
    args: [GH, T, 5],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/pulls/5`,
        json: { number: 5, title: 'Five', body: null, user: { login: 'dev' }, head: { ref: 'feat' }, base: { ref: 'main' }, draft: false, state: 'open', merged: false, mergeable: null, html_url: 'https://github.com/acme/app/pull/5' }
      },
      {
        method: 'GET',
        url: `${GHA}/issues/5/comments?per_page=100`,
        json: [
          { user: { login: 'a' }, body: 'first', created_at: '2024-01-01T00:00:00Z' },
          { user: null, body: 'ghost', created_at: '2024-01-02T00:00:00Z' }
        ]
      },
      {
        method: 'GET',
        url: `${GHA}/pulls/5/reviews?per_page=100`,
        json: [
          { user: { login: 'r1' }, state: 'APPROVED' },
          { user: { login: 'r2' }, state: 'PENDING' },
          { user: null, state: 'CHANGES_REQUESTED' }
        ]
      },
      {
        method: 'GET',
        url: `${GHA}/pulls/5/comments?per_page=100`,
        json: [
          { id: 30, user: { login: 'b' }, body: 'reply', created_at: '2024-01-03T00:00:00Z', path: 'a.ts', line: 3, original_line: 3, diff_hunk: '@@ a', in_reply_to_id: 10 },
          { id: 10, user: { login: 'a' }, body: 'root', created_at: '2024-01-01T00:00:00Z', path: 'a.ts', line: null, original_line: 2, diff_hunk: '@@ a' },
          { id: 20, user: null, body: 'other', created_at: '2024-01-02T00:00:00Z', path: 'b.ts', line: null, original_line: null, diff_hunk: '@@ b' }
        ]
      }
    ]
  },
  {
    name: 'github merged, review comments failing',
    channel: 'hosting:prDetail',
    args: [GH, T, 6],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/pulls/6`,
        json: { number: 6, title: 'Six', body: 'b', user: { login: 'dev' }, head: { ref: 'f' }, base: { ref: 'main' }, draft: true, state: 'closed', merged: true, mergeable: true, html_url: 'https://github.com/acme/app/pull/6' }
      },
      { method: 'GET', url: `${GHA}/issues/6/comments?per_page=100`, json: [] },
      { method: 'GET', url: `${GHA}/pulls/6/reviews?per_page=100`, json: [] },
      { method: 'GET', url: `${GHA}/pulls/6/comments?per_page=100`, status: 500, raw: '' }
    ]
  },
  {
    name: 'github detail failure',
    channel: 'hosting:prDetail',
    args: [GH, T, 7],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/7`, status: 404, json: { message: 'Not Found' } },
      { method: 'GET', url: `${GHA}/issues/7/comments?per_page=100`, json: [] },
      { method: 'GET', url: `${GHA}/pulls/7/reviews?per_page=100`, json: [] },
      { method: 'GET', url: `${GHA}/pulls/7/comments?per_page=100`, json: [] }
    ]
  },
  { name: 'bitbucket is github-only', channel: 'hosting:prDetail', args: [BB, { bitbucket: BB_APP }, 1] },
  { name: 'github no credential', channel: 'hosting:prDetail', args: [GH, {}, 1], helper: { gh: 'missing' } },
  {
    name: 'gitlab full detail',
    channel: 'hosting:prDetail',
    args: [GL, { gitlab: GL_TOKEN }, 3],
    responses: [
      {
        method: 'GET',
        url: `${GLA}/merge_requests/3`,
        json: { iid: 3, title: 'MR', description: null, author: null, source_branch: 'f', target_branch: 'main', work_in_progress: true, state: 'opened', merge_status: 'can_be_merged', web_url: 'https://gitlab.com/x/-/merge_requests/3' }
      },
      {
        method: 'GET',
        url: `${GLA}/merge_requests/3/discussions?per_page=100`,
        json: [
          { id: 'd1', notes: [{ id: 1, system: true, author: { username: 'bot' }, body: 'added 1 commit', created_at: '2024-01-01T00:00:00Z' }] },
          {
            id: 'd2',
            notes: [
              { id: 2, system: false, author: { username: 'a' }, body: 'inline', created_at: '2024-01-03T00:00:00Z', position: { new_path: null, old_path: 'old.ts', new_line: null, old_line: 4 } },
              { id: 3, system: false, author: null, body: 'reply', created_at: '2024-01-04T00:00:00Z', position: { new_path: null, old_path: 'old.ts', new_line: null, old_line: 4 } }
            ]
          },
          { id: 'd3', notes: [{ id: 4, system: false, author: { username: 'b' }, body: 'later', created_at: '2024-01-05T00:00:00Z', position: null }] },
          { id: 'd4', notes: [{ id: 5, system: false, author: { username: 'c' }, body: 'earlier', created_at: '2024-01-02T00:00:00Z' }] },
          {
            id: 'd5',
            notes: [{ id: 6, system: false, author: { username: 'd' }, body: 'new file', created_at: '2024-01-06T00:00:00Z', position: { new_path: 'n.ts', old_path: 'n.ts', new_line: 9, old_line: null } }]
          }
        ]
      },
      { method: 'GET', url: `${GLA}/merge_requests/3/approvals`, json: { approved_by: [{ user: { username: 'r' } }, { user: null }] } }
    ]
  },
  {
    name: 'gitlab merged, side calls failing',
    channel: 'hosting:prDetail',
    args: [GL, { gitlab: GL_TOKEN }, 4],
    responses: [
      {
        method: 'GET',
        url: `${GLA}/merge_requests/4`,
        json: { iid: 4, title: 'MR4', description: 'd', author: { username: 'me' }, source_branch: 'f', target_branch: 'main', draft: false, state: 'merged', merge_status: 'cannot_be_merged', web_url: 'https://gitlab.com/x/-/merge_requests/4' }
      },
      { method: 'GET', url: `${GLA}/merge_requests/4/discussions?per_page=100`, status: 500, raw: '' },
      { method: 'GET', url: `${GLA}/merge_requests/4/approvals`, status: 403, json: { message: '403 Forbidden' } }
    ]
  },
  {
    name: 'gitlab detail failure',
    channel: 'hosting:prDetail',
    args: [GL, { gitlab: GL_TOKEN }, 5],
    responses: [
      { method: 'GET', url: `${GLA}/merge_requests/5`, status: 404, json: { error: '404 Not Found' } },
      { method: 'GET', url: `${GLA}/merge_requests/5/discussions?per_page=100`, json: [] },
      { method: 'GET', url: `${GLA}/merge_requests/5/approvals`, json: {} }
    ]
  },
  { name: 'gitlab no credential', channel: 'hosting:prDetail', args: [GL, T, 5] },

  // ── hosting:prComment ──
  {
    name: 'github',
    channel: 'hosting:prComment',
    args: [GH, T, 5, 'Looks good'],
    responses: [{ method: 'POST', url: `${GHA}/issues/5/comments`, status: 201, json: { id: 1 } }]
  },
  {
    name: 'github failure',
    channel: 'hosting:prComment',
    args: [GH, T, 5, 'x'],
    responses: [{ method: 'POST', url: `${GHA}/issues/5/comments`, status: 403, json: { message: 'Must have push access' } }]
  },
  {
    name: 'gitlab',
    channel: 'hosting:prComment',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'Note'],
    responses: [{ method: 'POST', url: `${GLA}/merge_requests/3/notes`, status: 201, json: { id: 2 } }]
  },
  {
    name: 'gitlab array error',
    channel: 'hosting:prComment',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'Note'],
    responses: [{ method: 'POST', url: `${GLA}/merge_requests/3/notes`, status: 400, json: { message: ['a', 1, null, ['b', 'c']] } }]
  },

  // ── hosting:prReplyReviewComment ──
  {
    name: 'github numeric root',
    channel: 'hosting:prReplyReviewComment',
    args: [GH, T, 5, 10, 'reply'],
    responses: [{ method: 'POST', url: `${GHA}/pulls/5/comments`, status: 201, json: { id: 11 } }]
  },
  {
    name: 'gitlab discussion id',
    channel: 'hosting:prReplyReviewComment',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'abc/def 1', 'reply'],
    responses: [{ method: 'POST', url: `${GLA}/merge_requests/3/discussions/abc%2Fdef%201/notes`, status: 201, json: { id: 12 } }]
  },
  {
    name: 'gitlab numeric id is stringified',
    channel: 'hosting:prReplyReviewComment',
    args: [GL, { gitlab: GL_TOKEN }, 3, 42, 'reply'],
    responses: [{ method: 'POST', url: `${GLA}/merge_requests/3/discussions/42/notes`, status: 201, raw: '' }]
  },

  // ── hosting:prFiles ──
  {
    name: 'github',
    channel: 'hosting:prFiles',
    args: [GH, T, 5],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/pulls/5/files?per_page=300`,
        json: [
          { filename: 'a.ts', status: 'modified', additions: 3, deletions: 1, patch: '...' },
          { filename: 'b.ts', status: 'added', additions: 10, deletions: 0 }
        ]
      }
    ]
  },
  {
    name: 'github failure is empty',
    channel: 'hosting:prFiles',
    args: [GH, T, 5],
    responses: [{ method: 'GET', url: `${GHA}/pulls/5/files?per_page=300`, status: 500, raw: '' }]
  },
  {
    name: 'gitlab',
    channel: 'hosting:prFiles',
    args: [GL, { gitlab: GL_TOKEN }, 3],
    responses: [
      {
        method: 'GET',
        url: `${GLA}/merge_requests/3/diffs?per_page=100`,
        json: [
          { old_path: 'a', new_path: 'a', new_file: false, renamed_file: false, deleted_file: false, diff: '@@ -1 +1 @@\n-x\n+y\n+z\n' },
          { old_path: 'b', new_path: 'b', new_file: true, renamed_file: false, deleted_file: false, diff: '+n\n' },
          { old_path: 'c', new_path: '', new_file: false, renamed_file: false, deleted_file: true, diff: '-gone\n--- not header? no\n' },
          { old_path: 'd', new_path: 'e', new_file: false, renamed_file: true, deleted_file: false, diff: '' },
          { old_path: 'f', new_path: 'f', new_file: false, renamed_file: false, deleted_file: false, diff: null }
        ]
      }
    ]
  },
  {
    name: 'gitlab failure is empty',
    channel: 'hosting:prFiles',
    args: [GL, { gitlab: GL_TOKEN }, 3],
    responses: [{ method: 'GET', url: `${GLA}/merge_requests/3/diffs?per_page=100`, network: true }]
  },

  // ── hosting:prChecks ──
  {
    name: 'github',
    channel: 'hosting:prChecks',
    args: [GH, T, 5],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/5`, json: { head: { sha: 'f'.repeat(40) } } },
      {
        method: 'GET',
        url: `${GHA}/commits/${'f'.repeat(40)}/check-runs?per_page=100`,
        json: {
          check_runs: [
            { name: 'a', status: 'completed', conclusion: 'success', html_url: 'https://h/1', details_url: 'https://d/1' },
            { name: 'b', status: 'in_progress', conclusion: null, html_url: null, details_url: 'https://d/2' },
            { name: 'c', status: 'queued', conclusion: null, html_url: '', details_url: null }
          ]
        }
      }
    ]
  },
  {
    name: 'github check-runs failure is empty',
    channel: 'hosting:prChecks',
    args: [GH, T, 5],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/5`, json: { head: { sha: 'f'.repeat(40) } } },
      { method: 'GET', url: `${GHA}/commits/${'f'.repeat(40)}/check-runs?per_page=100`, status: 403, json: { message: 'no' } }
    ]
  },
  {
    name: 'github pull failure',
    channel: 'hosting:prChecks',
    args: [GH, T, 5],
    responses: [{ method: 'GET', url: `${GHA}/pulls/5`, status: 404, raw: '' }]
  },
  {
    name: 'gitlab head pipeline jobs',
    channel: 'hosting:prChecks',
    args: [GL, { gitlab: GL_TOKEN }, 3],
    responses: [
      { method: 'GET', url: `${GLA}/merge_requests/3/pipelines?per_page=1`, json: [{ id: 99 }] },
      {
        method: 'GET',
        url: `${GLA}/pipelines/99/jobs?per_page=100`,
        json: [
          { name: 'build', status: 'success', web_url: 'https://gl/j/1' },
          { name: 'test', status: 'running', web_url: null },
          { name: 'deploy', status: 'manual' },
          { name: 'new', status: 'weird' }
        ]
      }
    ]
  },
  {
    name: 'gitlab no pipelines',
    channel: 'hosting:prChecks',
    args: [GL, { gitlab: GL_TOKEN }, 3],
    responses: [{ method: 'GET', url: `${GLA}/merge_requests/3/pipelines?per_page=1`, json: [] }]
  },
  {
    name: 'gitlab pipelines failure is empty',
    channel: 'hosting:prChecks',
    args: [GL, { gitlab: GL_TOKEN }, 3],
    responses: [{ method: 'GET', url: `${GLA}/merge_requests/3/pipelines?per_page=1`, status: 500, raw: '' }]
  },
  {
    name: 'gitlab jobs failure is empty',
    channel: 'hosting:prChecks',
    args: [GL, { gitlab: GL_TOKEN }, 3],
    responses: [
      { method: 'GET', url: `${GLA}/merge_requests/3/pipelines?per_page=1`, json: [{ id: 5 }] },
      { method: 'GET', url: `${GLA}/pipelines/5/jobs?per_page=100`, status: 500, raw: '' }
    ]
  },

  // ── hosting:prReview ──
  {
    name: 'github approve without body',
    channel: 'hosting:prReview',
    args: [GH, T, 5, 'APPROVE', ''],
    responses: [{ method: 'POST', url: `${GHA}/pulls/5/reviews`, json: { id: 1 } }]
  },
  {
    name: 'github request changes',
    channel: 'hosting:prReview',
    args: [GH, T, 5, 'REQUEST_CHANGES', 'Please fix'],
    responses: [{ method: 'POST', url: `${GHA}/pulls/5/reviews`, status: 422, json: { message: 'Unprocessable Entity', errors: ['Can not request changes on your own pull request'] } }]
  },
  {
    name: 'gitlab approve with body',
    channel: 'hosting:prReview',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'APPROVE', 'LGTM'],
    responses: [
      { method: 'POST', url: `${GLA}/merge_requests/3/approve`, status: 201, json: {} },
      { method: 'POST', url: `${GLA}/merge_requests/3/notes`, status: 201, json: {} }
    ]
  },
  {
    name: 'gitlab approve failure',
    channel: 'hosting:prReview',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'APPROVE', 'LGTM'],
    responses: [{ method: 'POST', url: `${GLA}/merge_requests/3/approve`, status: 401, json: { message: '401 Unauthorized' } }]
  },
  {
    name: 'gitlab request changes',
    channel: 'hosting:prReview',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'REQUEST_CHANGES', 'Fix it'],
    responses: [
      { method: 'POST', url: `${GLA}/merge_requests/3/unapprove`, status: 404, json: { message: '404 Not Found' } },
      { method: 'POST', url: `${GLA}/merge_requests/3/notes`, status: 201, json: {} }
    ]
  },
  {
    name: 'gitlab comment without body is a no-op',
    channel: 'hosting:prReview',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'COMMENT', '']
  },

  // ── hosting:prMerge ──
  {
    name: 'github squash',
    channel: 'hosting:prMerge',
    args: [GH, T, 5, 'squash'],
    responses: [{ method: 'PUT', url: `${GHA}/pulls/5/merge`, json: { merged: true } }]
  },
  {
    name: 'github not mergeable',
    channel: 'hosting:prMerge',
    args: [GH, T, 5, 'rebase'],
    responses: [{ method: 'PUT', url: `${GHA}/pulls/5/merge`, status: 405, json: { message: 'Pull Request is not mergeable' } }]
  },
  {
    name: 'gitlab squash',
    channel: 'hosting:prMerge',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'squash'],
    responses: [{ method: 'PUT', url: `${GLA}/merge_requests/3/merge`, json: { state: 'merged' } }]
  },
  {
    name: 'gitlab merge commit',
    channel: 'hosting:prMerge',
    args: [GL, { gitlab: GL_TOKEN }, 3, 'merge'],
    responses: [{ method: 'PUT', url: `${GLA}/merge_requests/3/merge`, status: 405, json: { message: 'Method Not Allowed' } }]
  },

  // ── hosting:mergedPrHeads ──
  {
    name: 'one merged one not',
    channel: 'hosting:mergedPrHeads',
    args: [GH, T, ['feat/a', 'feat/b', 'feat/c']],
    responses: [
      { method: 'GET', url: `${GHA}/pulls?state=closed&head=acme%3Afeat%2Fa&per_page=5`, json: [{ merged_at: null }, { merged_at: '2024-01-01T00:00:00Z' }] },
      { method: 'GET', url: `${GHA}/pulls?state=closed&head=acme%3Afeat%2Fb&per_page=5`, json: [{ merged_at: null }] },
      { method: 'GET', url: `${GHA}/pulls?state=closed&head=acme%3Afeat%2Fc&per_page=5`, status: 500, raw: '' }
    ]
  },
  { name: 'gitlab is github-only', channel: 'hosting:mergedPrHeads', args: [GL, T, ['a']] },

  // ── hosting:ensureStack ──
  { name: 'one pr is not a stack', channel: 'hosting:ensureStack', args: [GH, T, [4]] },
  {
    name: 'create a new stack',
    channel: 'hosting:ensureStack',
    args: [GH, T, [4, 5, 6]],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/4`, json: { number: 4 } },
      { method: 'POST', url: `${GHA}/stacks`, status: 201, json: { number: 2, html_url: 'https://github.com/acme/app/stacks/2' } }
    ]
  },
  {
    name: 'create when the bottom lookup fails',
    channel: 'hosting:ensureStack',
    args: [GH, T, [4, 5]],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/4`, status: 500, raw: '' },
      { method: 'POST', url: `${GHA}/stacks`, status: 201, json: { number: 3 } }
    ]
  },
  {
    name: 'extend an existing stack',
    channel: 'hosting:ensureStack',
    args: [GH, T, [4, 5, 6]],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/4`, json: { number: 4, stack: { number: 9 } } },
      { method: 'GET', url: `${GHA}/stacks/9`, json: { pull_requests: [{ number: 4 }, { number: 5 }] } },
      { method: 'POST', url: `${GHA}/stacks/9/add`, json: {} }
    ]
  },
  {
    name: 'existing stack already complete',
    channel: 'hosting:ensureStack',
    args: [GH, T, [4, 5]],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/4`, json: { number: 4, stack: { number: 9 } } },
      { method: 'GET', url: `${GHA}/stacks/9`, json: { pull_requests: [{ number: 5 }, { number: 4 }] } }
    ]
  },
  {
    name: 'not in the preview',
    channel: 'hosting:ensureStack',
    args: [GH, T, [4, 5]],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/4`, json: { number: 4, stack: null } },
      { method: 'POST', url: `${GHA}/stacks`, status: 404, json: { message: 'Not Found' } }
    ]
  },

  // ── hosting:updatePR ──
  {
    name: 'retarget and retitle',
    channel: 'hosting:updatePR',
    args: [GH, T, 5, { base: 'feat/a', title: 'New title' }],
    responses: [{ method: 'PATCH', url: `${GHA}/pulls/5`, json: {} }]
  },
  {
    name: 'merge stack section into the live body',
    channel: 'hosting:updatePR',
    args: [GH, T, 5, { title: '', stackSection: '<!-- gitcito-stack -->\nnew\n<!-- /gitcito-stack -->' }],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/5`, json: { body: 'Intro\n\n<!-- gitcito-stack -->\nold\n<!-- /gitcito-stack -->\nOutro' } },
      { method: 'PATCH', url: `${GHA}/pulls/5`, json: {} }
    ]
  },
  {
    name: 'stack section on an empty body',
    channel: 'hosting:updatePR',
    args: [GH, T, 5, { stackSection: '' }],
    responses: [
      { method: 'GET', url: `${GHA}/pulls/5`, json: { body: null } },
      { method: 'PATCH', url: `${GHA}/pulls/5`, status: 422, json: { message: 'Validation Failed', errors: [{ field: 'base', code: 'invalid' }] } }
    ]
  },
  { name: 'nothing to change', channel: 'hosting:updatePR', args: [GH, T, 5, { base: '', title: '' }] },

  // ── hosting:listNotifications ──
  {
    name: 'unread notifications',
    channel: 'hosting:listNotifications',
    args: [GH_TOKEN],
    responses: [
      {
        method: 'GET',
        url: 'https://api.github.com/notifications?all=false&per_page=50',
        json: [
          { id: '1', reason: 'review_requested', unread: true, updated_at: '2024-03-01T12:00:00Z', subject: { title: 'PR', url: 'https://api.github.com/repos/acme/app/pulls/5', type: 'PullRequest' }, repository: { full_name: 'acme/app', html_url: 'https://github.com/acme/app' } },
          { id: '2', reason: 'mention', unread: false, updated_at: '2024-03-02T12:00:00.500Z', subject: { title: 'Issue', url: 'https://api.github.com/repos/acme/app/issues/9', type: 'Issue' }, repository: { full_name: 'acme/app', html_url: 'https://github.com/acme/app' } },
          { id: '3', reason: 'subscribed', unread: true, updated_at: '2024-03-03T00:00:00Z', subject: { title: 'v1', url: 'https://api.github.com/repos/acme/app/releases/123', type: 'Release' }, repository: { full_name: 'acme/app', html_url: 'https://github.com/acme/app' } },
          { id: '4', reason: 'subscribed', unread: true, updated_at: '2024-03-04T00:00:00Z', subject: { title: 'v2', url: null, type: 'Release' }, repository: { full_name: 'acme/app', html_url: 'https://github.com/acme/app' } },
          { id: '5', reason: 'ci_activity', unread: true, updated_at: '2024-03-05T00:00:00Z', subject: { title: 'CI', url: 'https://api.github.com/repos/acme/app/commits/abc', type: 'CheckSuite' }, repository: { full_name: 'acme/app', html_url: 'https://github.com/acme/app' } },
          { id: '6', reason: 'author', unread: true, updated_at: 'not a date', subject: { title: 'D', url: 'https://api.github.com/repos/acme/app/discussions/4', type: 'Discussion' }, repository: { full_name: 'acme/app', html_url: 'https://github.com/acme/app' } }
        ]
      }
    ]
  },
  {
    name: 'all notifications via borrowed credential',
    channel: 'hosting:listNotifications',
    args: ['', true],
    helper: { fill: { 'github.com/': cred('octo', 'gho_poll') } },
    responses: [{ method: 'GET', url: 'https://api.github.com/notifications?all=true&per_page=50', json: [] }]
  },
  { name: 'no credential is empty', channel: 'hosting:listNotifications', args: [''] },
  {
    name: 'failure',
    channel: 'hosting:listNotifications',
    args: [GH_TOKEN],
    responses: [{ method: 'GET', url: 'https://api.github.com/notifications?all=false&per_page=50', status: 401, json: { message: 'Requires authentication' } }]
  },

  // ── hosting:markNotificationRead ──
  {
    name: 'mark one read',
    channel: 'hosting:markNotificationRead',
    args: [GH_TOKEN, '123'],
    responses: [{ method: 'PATCH', url: 'https://api.github.com/notifications/threads/123', json: {} }]
  },
  {
    name: 'already read is swallowed',
    channel: 'hosting:markNotificationRead',
    args: [GH_TOKEN, '124'],
    responses: [{ method: 'PATCH', url: 'https://api.github.com/notifications/threads/124', status: 205 }]
  },
  { name: 'no credential is a no-op', channel: 'hosting:markNotificationRead', args: ['', '1'] },

  // ── hosting:markAllNotificationsRead ──
  {
    name: 'mark all read',
    channel: 'hosting:markAllNotificationsRead',
    args: [GH_TOKEN],
    responses: [{ method: 'PUT', url: 'https://api.github.com/notifications', status: 202 }]
  },
  {
    name: 'network failure is swallowed',
    channel: 'hosting:markAllNotificationsRead',
    args: [GH_TOKEN],
    responses: [{ method: 'PUT', url: 'https://api.github.com/notifications', network: true }]
  },
  {
    name: 'error status is ignored',
    channel: 'hosting:markAllNotificationsRead',
    args: [GH_TOKEN],
    responses: [{ method: 'PUT', url: 'https://api.github.com/notifications', status: 500, raw: '' }]
  },
  { name: 'no credential is a no-op', channel: 'hosting:markAllNotificationsRead', args: [''] },

  // ── hosting:listIssues ──
  {
    name: 'issues without pull requests',
    channel: 'hosting:listIssues',
    args: [GH, T],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/issues?state=open&per_page=50`,
        json: [
          { number: 1, title: 'Bug', user: { login: 'a' }, state: 'open', html_url: 'https://github.com/acme/app/issues/1', comments: 2 },
          { number: 2, title: 'PR', user: { login: 'b' }, state: 'open', html_url: 'https://github.com/acme/app/pull/2', comments: 0, pull_request: {} },
          { number: 3, title: 'Closed', user: null, state: 'closed', html_url: 'https://github.com/acme/app/issues/3', comments: 0 }
        ]
      }
    ]
  },
  { name: 'non-github remote', channel: 'hosting:listIssues', args: [BB, T] },
  { name: 'no credential is empty', channel: 'hosting:listIssues', args: [GH, {}] },
  {
    name: 'failure',
    channel: 'hosting:listIssues',
    args: [GH, T],
    responses: [{ method: 'GET', url: `${GHA}/issues?state=open&per_page=50`, status: 410, json: { message: 'Issues are disabled for this repo' } }]
  },

  // ── hosting:listMilestones ──
  {
    name: 'milestones',
    channel: 'hosting:listMilestones',
    args: [GH, T],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/milestones?state=all&per_page=50`,
        json: [
          { number: 1, title: 'v1', description: null, state: 'open', due_on: '2024-06-01T07:00:00Z', open_issues: 3, closed_issues: 4, html_url: 'https://github.com/acme/app/milestone/1' },
          { number: 2, title: 'v0', description: 'old', state: 'closed', due_on: null, open_issues: 0, closed_issues: 9, html_url: 'https://github.com/acme/app/milestone/2' }
        ]
      }
    ]
  },
  { name: 'unknown remote', channel: 'hosting:listMilestones', args: [UNKNOWN, T] },
  { name: 'no credential is empty', channel: 'hosting:listMilestones', args: [GH, { github: '' }] },

  // ── hosting:milestoneIssues ──
  {
    name: 'issues in a milestone',
    channel: 'hosting:milestoneIssues',
    args: [GH, T, 1],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/issues?milestone=1&state=all&per_page=100`,
        json: [
          { number: 1, title: 'Bug', user: { login: 'a' }, state: 'closed', html_url: 'https://github.com/acme/app/issues/1', comments: 1 },
          { number: 2, title: 'PR', user: { login: 'b' }, state: 'open', html_url: 'https://github.com/acme/app/pull/2', comments: 0, pull_request: { url: 'x' } }
        ]
      }
    ]
  },
  { name: 'gitlab is github-only', channel: 'hosting:milestoneIssues', args: [GL, T, 1] },

  // ── hosting:uploadSshKey ──
  {
    name: 'upload',
    channel: 'hosting:uploadSshKey',
    args: [GH_TOKEN, 'Gitcito laptop', '  ssh-ed25519 AAAAC3Nza me@host\n'],
    responses: [{ method: 'POST', url: 'https://api.github.com/user/keys', status: 201, json: { id: 77, key: 'ssh-ed25519 AAAAC3Nza' } }]
  },
  {
    name: 'already in use',
    channel: 'hosting:uploadSshKey',
    args: [GH_TOKEN, 't', 'ssh-ed25519 AAAA'],
    responses: [{ method: 'POST', url: 'https://api.github.com/user/keys', status: 422, json: { message: 'Validation Failed', errors: [{ message: 'key is already in use' }] } }]
  },

  // ── hosting:issueDetail ──
  {
    name: 'full issue',
    channel: 'hosting:issueDetail',
    args: [GH, T, 9],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/issues/9`,
        json: {
          number: 9,
          title: 'Nine',
          body: null,
          user: null,
          state: 'open',
          html_url: 'https://github.com/acme/app/issues/9',
          created_at: '2024-01-01T00:00:00Z',
          labels: [{ name: 'bug' }, 'plain'],
          assignees: null,
          milestone: { title: 'v1' }
        }
      },
      { method: 'GET', url: `${GHA}/issues/9/comments?per_page=100`, json: [{ user: { login: 'a' }, body: 'hi', created_at: '2024-01-02T00:00:00Z' }] },
      {
        method: 'GET',
        url: `${GHA}/issues/9/timeline?per_page=100`,
        json: [
          { event: 'labeled' },
          { event: 'cross-referenced', source: { issue: { number: 11, title: 'Fix', html_url: 'https://github.com/acme/app/pull/11', state: 'open', pull_request: {} } } },
          { event: 'cross-referenced', source: { issue: { number: 12, title: 'Other issue', html_url: 'https://github.com/acme/app/issues/12', state: 'open' } } },
          { event: 'cross-referenced', source: { issue: { number: 13, title: 'Old', html_url: 'https://github.com/acme/app/pull/13', state: 'closed', pull_request: {} } } },
          { event: 'cross-referenced', source: { issue: { number: 11, title: 'Fix (renamed)', html_url: 'https://github.com/acme/app/pull/11', state: 'closed', pull_request: {} } } },
          { event: 'referenced', source: { issue: { number: 14, title: 'X', html_url: 'u', state: 'open', pull_request: {} } } }
        ]
      },
      {
        method: 'POST',
        url: 'https://api.github.com/graphql',
        json: {
          data: {
            repository: {
              issue: {
                projectItems: {
                  nodes: [
                    {
                      project: { title: 'Roadmap' },
                      fieldValues: {
                        nodes: [
                          { __typename: 'ProjectV2ItemFieldTextValue', text: 'Some text', field: { name: 'Notes' } },
                          { __typename: 'ProjectV2ItemFieldNumberValue', number: 3.5, field: { name: 'Effort' } },
                          { __typename: 'ProjectV2ItemFieldNumberValue', number: 0, field: { name: 'Zero' } },
                          { __typename: 'ProjectV2ItemFieldDateValue', date: '2024-05-01', field: { name: 'Start' } },
                          { __typename: 'ProjectV2ItemFieldSingleSelectValue', name: 'High', field: { name: 'Priority' } },
                          { __typename: 'ProjectV2ItemFieldIterationValue', title: 'Sprint 3', field: { name: 'Iteration' } },
                          { __typename: 'ProjectV2ItemFieldTextValue', text: '', field: { name: 'Empty' } },
                          { __typename: 'ProjectV2ItemFieldRepositoryValue' },
                          { __typename: 'ProjectV2ItemFieldTextValue', text: null, name: 'fallback', field: { name: 'Fallback' } }
                        ]
                      }
                    },
                    { project: null, fieldValues: { nodes: [{ text: 'x', field: { name: 'F' } }] } },
                    { project: { title: 'Empty' }, fieldValues: { nodes: [] } }
                  ]
                }
              }
            }
          }
        }
      }
    ]
  },
  {
    name: 'closed issue, side calls failing',
    channel: 'hosting:issueDetail',
    args: [GH, T, 10],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/issues/10`,
        json: {
          number: 10,
          title: 'Ten',
          body: 'b',
          user: { login: 'me' },
          state: 'closed',
          html_url: 'https://github.com/acme/app/issues/10',
          created_at: '2024-01-01T00:00:00Z',
          labels: [],
          assignees: [{ login: 'x' }, { login: 'y' }],
          milestone: null
        }
      },
      { method: 'GET', url: `${GHA}/issues/10/comments?per_page=100`, json: [] },
      { method: 'GET', url: `${GHA}/issues/10/timeline?per_page=100`, status: 500, raw: '' },
      { method: 'POST', url: 'https://api.github.com/graphql', status: 401, json: { message: 'Bad credentials' } }
    ]
  },
  {
    name: 'graphql errors are empty',
    channel: 'hosting:issueDetail',
    args: [GH, T, 11],
    responses: [
      {
        method: 'GET',
        url: `${GHA}/issues/11`,
        json: { number: 11, title: 'E', body: '', user: { login: 'me' }, state: 'open', html_url: 'u', created_at: 'c', labels: [], assignees: [], milestone: null }
      },
      { method: 'GET', url: `${GHA}/issues/11/comments?per_page=100`, json: [] },
      { method: 'GET', url: `${GHA}/issues/11/timeline?per_page=100`, json: [] },
      { method: 'POST', url: 'https://api.github.com/graphql', json: { errors: [{ message: 'INSUFFICIENT_SCOPES' }] } }
    ]
  },
  {
    name: 'missing issue',
    channel: 'hosting:issueDetail',
    args: [GH, T, 12],
    responses: [
      { method: 'GET', url: `${GHA}/issues/12`, status: 404, json: { message: 'Not Found' } },
      { method: 'GET', url: `${GHA}/issues/12/comments?per_page=100`, status: 404, json: { message: 'Not Found' } },
      { method: 'GET', url: `${GHA}/issues/12/timeline?per_page=100`, json: [] },
      { method: 'POST', url: 'https://api.github.com/graphql', network: true }
    ]
  },

  // ── hosting:setIssueState ──
  {
    name: 'close',
    channel: 'hosting:setIssueState',
    args: [GH, T, 9, 'closed'],
    responses: [{ method: 'PATCH', url: `${GHA}/issues/9`, json: { state: 'closed' } }]
  },
  {
    name: 'failure',
    channel: 'hosting:setIssueState',
    args: [GH, T, 9, 'open'],
    responses: [{ method: 'PATCH', url: `${GHA}/issues/9`, status: 403, json: { message: 'Forbidden' } }]
  },

  // ── hosting:createIssue ──
  {
    name: 'with body',
    channel: 'hosting:createIssue',
    args: [GH, T, { title: 'New bug', body: 'Steps' }],
    responses: [{ method: 'POST', url: `${GHA}/issues`, status: 201, json: { number: 20, html_url: 'https://github.com/acme/app/issues/20' } }]
  },
  {
    name: 'without body',
    channel: 'hosting:createIssue',
    args: [GH, T, { title: 'Bare' }],
    responses: [{ method: 'POST', url: `${GHA}/issues`, status: 201, json: { number: 21, html_url: 'https://github.com/acme/app/issues/21' } }]
  },

  // ── hosting:applyPrMeta ──
  {
    name: 'all three, reviewers failing',
    channel: 'hosting:applyPrMeta',
    args: [GH, T, 5, { reviewers: ['r1', 'r2'], labels: ['bug'], assignees: ['me'] }],
    responses: [
      { method: 'POST', url: `${GHA}/pulls/5/requested_reviewers`, status: 422, json: { message: 'Reviews may only be requested from collaborators.' } },
      { method: 'POST', url: `${GHA}/issues/5/labels`, json: [] },
      { method: 'POST', url: `${GHA}/issues/5/assignees`, status: 201, json: {} }
    ]
  },
  { name: 'nothing to apply', channel: 'hosting:applyPrMeta', args: [GH, T, 5, { reviewers: [], labels: [] }] },
  { name: 'non-github remote', channel: 'hosting:applyPrMeta', args: [ADO, { github: GH_TOKEN }, 5, { labels: ['x'] }] }
]

const JSON_SAMPLES = [
  '', '   ', '<html>', '<!DOCTYPE html><html><body>proxy</body></html>', '{', '{"a":', 'nul', 'nulx', '[1,]', '"abc',
  '{"a" 1}', '{"a":1,}', '{a:1}', '[1 2]', '1 2', '{} x', 'tru', '-', '-x', '01', '1.', '1.x', '1e', '"a\\x"',
  '"a\u0001b"', '"\\u12"', '[', '["a",', '{"a":1', '{"a"}', ']', '}', '"\\uZZZZ"',
  '[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,]', '{"key":"value","other":nope}', '"tab\there"', 'é',
  '["ééééééééééé", x]', 'Infinity', 'NaN', 'undefined',
  '[object Object]', ' NaN', '+1', '.5', '[-]', '{"a":-}', '"\\"', '"x', '[1]]', 'falsey', '{"a":1 "b":2}',
  '\n\n  <x', '[1,\n2,\n<]', '{"a":"b"}garbage-after-the-value', '"\u{1F600}" x', '1e5x', '[1e+]', '{"a":nul}',
  '{"a":t}', '{"":1,"b"::2}', '[,1]', '{,}', '"abc\ndef"', '[1,2', '{"a":[1,{"b":', '{"a"', '"\\u12', '"\\',
  '[1,2,', '{"a":1,', '{"a":1,"b"', '[1', '{"a":1}\r\n\r\nx', '{"a":1\n,\n"b":}', '"a"b', '1-', '-0x', '00',
  '0.5.', 'tx', 'n', '﻿{}', '{"a":1} ', '[1,2\n,3 4]', '\r\n\r\n{', '"\u{1F600}',
  'x'.repeat(25) + '\u{1F600}' + 'y', 'upstream exploded', 'not json at all', '{"ok":true}', '[1, 2.5e3, -0]'
]

// ─── runner ─────────────────────────────────────────────────────────────────

async function runCase(c: Case): Promise<Record<string, unknown>> {
  clearCredentialCache()
  const helper: Helper = JSON.parse(JSON.stringify(c.helper ?? {}))
  h.helper.fill = helper.fill ?? {}
  h.helper.gh = helper.gh ?? 'missing'
  h.procs.length = 0
  h.opened.length = 0
  requests = []
  queues = new Map()
  for (const r of c.responses ?? []) {
    const key = `${r.method} ${r.url}`
    queues.set(key, [...(queues.get(key) ?? []), r])
  }

  const handler = h.handlers.get(c.channel)
  if (!handler) throw new Error(`no handler for ${c.channel}`)
  let outcome: Record<string, unknown>
  try {
    outcome = { result: norm(await handler({}, ...c.args)) }
  } catch (err) {
    outcome = { error: (err as Error).message }
  }
  // Fire-and-forget calls (applyPrMeta's tasks) all settle within the handler;
  // give stragglers a turn so nothing records after we read.
  await new Promise((r) => setImmediate(r))

  const unmatched = requests.filter((r) => r.unmatched)
  if (unmatched.length && WRITE) console.warn(`[${c.channel} ${c.name}] unmatched:`, unmatched.map((r) => `${r.method} ${r.url}`))

  return {
    name: c.name,
    channel: c.channel,
    args: norm(c.args),
    helper: { fill: c.helper?.fill ?? {}, gh: c.helper?.gh ?? 'missing' },
    responses: c.responses ?? [],
    requests: requests.map((r) => ({ ...r })),
    processes: h.procs.map((p) => ({ ...p })),
    opened: [...h.opened],
    ...outcome
  }
}

beforeAll(() => {
  vi.stubGlobal('fetch', fakeFetch)
  registerHostingHandlers()
})

describe('hosting port vectors', () => {
  it('covers every hosting channel', () => {
    const channels = new Set(CASES.map((c) => c.channel))
    expect([...h.handlers.keys()].filter((k) => !channels.has(k))).toEqual([])
  })

  it('request/response vectors per channel', async () => {
    const byChannel = new Map<string, Record<string, unknown>[]>()
    for (const c of CASES) {
      const out = await runCase(c)
      byChannel.set(c.channel, [...(byChannel.get(c.channel) ?? []), out])
    }
    for (const [channel, cases] of byChannel) {
      check(`internal/hosting/testdata/requests/${channel.replace('hosting:', '')}.json`, cases)
    }
  })

  it('pure logic vectors', () => {
    const remotes = [
      'https://github.com/o/r.git',
      'git@github.com:o/r.git',
      'https://github.com/o/r',
      'https://user:tok@github.com/o/r.git',
      'ssh://git@github.com/o/r.git',
      'https://github.com/o/r.git/',
      'https://gitlab.com/group/repo.git',
      'git@gitlab.com:group/sub/repo.git',
      'https://gitlab.com/group/sub/repo',
      'https://gitlab.com/solo',
      'https://bitbucket.org/team/repo.git',
      'git@bitbucket.org:team/repo.git',
      'https://bitbucket.org/team/a/b',
      'https://dev.azure.com/org/proj/_git/repo',
      'https://org@dev.azure.com/org/My%20Proj/_git/My%20Repo.git',
      'git@ssh.dev.azure.com:v3/org/proj/repo',
      'git@ssh.dev.azure.com:v3/org/My%20Proj/repo.git',
      'https://contoso.visualstudio.com/Proj/_git/Repo',
      'https://contoso.visualstudio.com/DefaultCollection/Proj/_git/Repo',
      'https://example.com/x/y.git',
      'https://dev.azure.com/o/%E0%A4%A/_git/r',
      'https://dev.azure.com/o/p/_git/%ZZ',
      ''
    ]
    const parse = (u: string): unknown => {
      try {
        return { out: norm(parseRemoteUrl(u)) }
      } catch (e) {
        return { error: (e as Error).message }
      }
    }
    const jwt = 'eyJhbGciOiJSUzI1NiJ9.eyJhdWQiOiJhZG8ifQ.c2ln'
    const pat = 'abcdefghijklmnopqrstuvwxyz234567'
    const statuses = ['missing', 'signed-out', 'authenticated', 'unknown'] as const
    check('testdata/vectors/hosting.json', {
      parseRemoteUrl: remotes.map((u) => ({ in: u, ...(parse(u) as object) })),
      azureAuth: [pat, jwt, '', 'ey.x.', 'ä-pat'].map((t) => ({ in: t, out: azureAuth(t) })),
      isJwt: [jwt, pat, 'ghp_abcdef', '', 'eyJ.x', 'eyJ_a-b.c-d.', ' eyJa.b.c', 'ey.x.'].map((t) => ({ in: t, out: isJwt(t) })),
      providerOfHost: ['github.com', 'GitHub.com', 'api.github.com', 'dev.azure.com', 'contoso.visualstudio.com', 'gitlab.com', 'gitlab.example.com', 'bitbucket.org', 'x.bitbucket.org', 'git.example.com', 'notgithub.com'].map(
        (host) => ({ in: host, out: providerOfHost(host) ?? null })
      ),
      parseCredential: [
        'protocol=https\nhost=dev.azure.com\nusername=me\npassword=secret\n',
        'password=a=b=c\n',
        'protocol=https\nhost=dev.azure.com\n',
        'username=me\r\npassword=secret\r\n',
        'password=\n',
        '=x\npassword=p\nusername=u\nusername=v\n',
        ''
      ].map((s) => ({ in: s, out: parseCredential(s) })),
      githubCredentialMessage: [
        ...statuses.map((s) => ({ status: s, host: null, out: githubCredentialMessage(s) })),
        ...statuses.map((s) => ({ status: s, host: 'ghe.example.com', out: githubCredentialMessage(s, 'ghe.example.com') }))
      ],
      providerBaseUrl: [
        { provider: 'github', org: null, out: providerBaseUrl('github') },
        { provider: 'gitlab', org: null, out: providerBaseUrl('gitlab') },
        { provider: 'bitbucket', org: null, out: providerBaseUrl('bitbucket') },
        { provider: 'azure', org: 'contoso', out: providerBaseUrl('azure', 'contoso') },
        { provider: 'azure', org: ' My Org/x ', out: providerBaseUrl('azure', ' My Org/x ') },
        { provider: 'azure', org: null, out: providerBaseUrl('azure') }
      ],
      glJobToCheck: [
        'created',
        'pending',
        'waiting_for_resource',
        'preparing',
        'scheduled',
        'running',
        'success',
        'failed',
        'canceled',
        'canceling',
        'skipped',
        'manual',
        'somethingnew',
        'constructor'
      ].flatMap((status) => [
        { in: { name: 'job', status, web_url: 'https://x' }, out: glJobToCheck({ name: 'job', status, web_url: 'https://x' }) },
        { in: { name: 'job', status }, out: glJobToCheck({ name: 'job', status }) }
      ]),
      diffLineCounts: [
        ['--- a/f.txt', '+++ b/f.txt', '@@ -1,3 +1,3 @@', ' ctx', '-old', '+new', '+more'].join('\n'),
        '',
        '+a\r\n-b\r\n+++x\n---y\n++z\n--w',
        '\n\n+'
      ].map((d) => ({ in: d, out: diffLineCounts(d) })),
      // `res.json()` on a body that is not JSON rejects with V8's SyntaxError
      // text, which the renderer showed verbatim; Go reproduces the messages.
      jsonParseErrors: JSON_SAMPLES.map((s) => {
        try {
          JSON.parse(s)
          return { in: s, error: null }
        } catch (e) {
          return { in: s, error: (e as Error).message }
        }
      })
    })

    const s1 = buildStackSection([{ branch: 'a', number: 1 }], 1, 'main')
    const s2 = buildStackSection(
      [
        { branch: 'a', number: 1 },
        { branch: 'b', number: 2 }
      ],
      1,
      'main'
    )
    const body1 = mergeStackSection('My description.', s1)
    const pairs: Array<[string, string]> = [
      ['My description.', s1],
      [body1, s2],
      ['', s1],
      ['  \n\t', s1],
      ['text <!-- /gitcito-stack --> then <!-- gitcito-stack --> x', s1],
      ['ünïcode — body\n\n', 'section'],
      ['a<!-- gitcito-stack -->b<!-- /gitcito-stack -->c<!-- /gitcito-stack -->', 'S']
    ]
    check('testdata/vectors/stackPr.json', {
      mergeStackSection: pairs.map(([body, section]) => ({ body, section, out: mergeStackSection(body, section) }))
    })
  })
})
