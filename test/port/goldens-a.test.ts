// Goldens for git slice A (status/staging, stash, log/branches/remotes,
// conflicts, interactive rebase, absorb, reflog, commit editing, WIP
// snapshots), captured from the TypeScript gitService and replayed by the Go
// port (internal/gitsvc/a_golden_test.go).
//
// PORT_WRITE_GOLDENS=1 rewrites internal/gitsvc/testdata/goldens/<method>/…;
// without it this re-runs every case and asserts the TypeScript still
// produces what the files say, so neither side can drift silently.
//
// Read methods run on the shared playground (read-only); write methods, and
// reads that need a prepared state, run on a cloneFixture() copy. Writes also
// record a snapshot of the repository afterwards.
//
// What is normalized, and why (the Go replay applies the same rules):
//   - absolute paths → <REPO>, <PLAYGROUND>, <HOME>, <TMP>; a clone's
//     basename → <REPONAME>.
//   - commit shas → content labels <cXXXXXXXXXX> (<…:short> for an
//     abbreviation). Most scenarios commit at wall-clock time, so every
//     regeneration of the playground mints new shas. A label hashes the tree,
//     the parents' labels, author/committer names and the message — all
//     deterministic — and never the dates. Annotated tag objects → <tag:ref>.
//   - wall-clock values: the fields date/time/lastCommitAt/scannedAt/
//     authorDate → "<DATE>" (0 is kept), repoPulse.activity → "<ACTIVITY:n>",
//     ISO timestamps → <ISO>, refs/gitcito/wip/<ms> → refs/gitcito/wip/<N>,
//     `version()` → <GITVERSION>.
//   - new commits are made with GIT_AUTHOR_DATE/GIT_COMMITTER_DATE pinned and
//     an isolated global config, so they are deterministic too.
import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  unlinkSync,
  writeFileSync
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, basename } from 'node:path'
import { gitService, redactCredentials, stripAnsi } from '../../src/main/git'
import { cloneFixture, cleanupFixtures } from '../fixtures'
import { PLAYGROUND, repoPath } from '../helpers'
import { parseDiff } from '../../src/renderer/src/lib/diff'
import { buildLinePatch, diffHeader, hunkLines, type PatchDirection } from '../../src/renderer/src/lib/linePatch'

const GOLDENS = resolve(__dirname, '../../internal/gitsvc/testdata/goldens')
const WRITE = !!process.env.PORT_WRITE_GOLDENS

// Kept in step with classify.go: these methods are reads; everything else in
// the slice is a write (clone + state snapshot).
const READS = new Set([
  'absorbPlan',
  'blobAtCommit',
  'branches',
  'commitEditInfo',
  'conflictContext',
  'conflictVersions',
  'interactiveRebaseSteps',
  'listSnapshots',
  'log',
  'mergeMessage',
  'mergePreview',
  'mergeState',
  'open',
  'rangeDiff',
  'reflog',
  'refTips',
  'remotes',
  'repoDetail',
  'repoPulse',
  'stashes',
  'status',
  'teammateRadar',
  'version'
])

type Step =
  | { git: string[] } // must succeed
  | { gitTry: string[] } // may fail (a merge that conflicts on purpose)
  | { write: string; content: string }
  | { rm: string }
  | { applyCached: string; reverse?: boolean } // `apply --cached` of a computed patch

interface Case {
  method: string
  /** Playground scenario, or '@missing' (a path that does not exist), '@plain' (an empty non-repo dir), '@none' (no repo argument). */
  fixture: string
  name: string
  args: unknown[]
  setup?: Step[]
  /**
   * Computes args after setup (patches built by the renderer's line-patch
   * builder). It may also return extra setup steps — already run against the
   * repo by the time it returns — which are recorded in the golden so the Go
   * replay runs them too.
   */
  argsFn?: (repo: string) => Promise<unknown[] | { args: unknown[]; steps: Step[] }>
}

// ─── Environment ────────────────────────────────────────────────────────────

const PINNED_DATE = '@1700000000 +0000'
let HOME = ''
let gitVersion = ''

function isolateEnv(): void {
  HOME = mkdtempSync(join(tmpdir(), 'gitcito-golden-home-'))
  mkdirSync(join(HOME, 'xdg'), { recursive: true })
  // Byte-identical to testutil.IsolateGitEnv's global config.
  const cfg =
    '[user]\n\tname = Gitcito Test\n\temail = test@gitcito.invalid\n' +
    '[init]\n\tdefaultBranch = main\n' +
    '[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n'
  writeFileSync(join(HOME, '.gitconfig'), cfg)
  process.env.HOME = HOME
  process.env.XDG_CONFIG_HOME = join(HOME, 'xdg')
  process.env.GIT_CONFIG_GLOBAL = join(HOME, '.gitconfig')
  process.env.GIT_CONFIG_NOSYSTEM = '1'
  process.env.GIT_TERMINAL_PROMPT = '0'
  process.env.GIT_AUTHOR_DATE = PINNED_DATE
  process.env.GIT_COMMITTER_DATE = PINNED_DATE
}

const git = (repo: string, args: string[]): string =>
  execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', maxBuffer: 256 * 1024 * 1024, stdio: ['pipe', 'pipe', 'pipe'] })

const gitOk = (repo: string, args: string[]): string => {
  try {
    return git(repo, args)
  } catch {
    return ''
  }
}

// ─── Normalization ──────────────────────────────────────────────────────────

interface CommitInfo {
  tree: string
  parents: string[]
  author: string
  committer: string
  body: string
}

const ISO_RE = /\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z/g
const WIP_RE = /refs\/gitcito\/wip\/\d+/g
// git prints a commit's date when it stops on one (a rebase conflict).
const DATE_LINE_RE = /^(\s*Date:\s+).+$/gm
const TOKEN_RE = /[0-9A-Za-z]+/g
const HEX_RE = /^[0-9a-f]{7,40}$/

/** Maps commit shas to content labels, for one repository state. */
class ShaLabeler {
  private info = new Map<string, CommitInfo>()
  private labels = new Map<string, string>()
  private busy = new Set<string>()
  private shas: string[] = []
  private tags = new Map<string, string>()

  constructor(private repo: string) {}

  load(extraTokens: string[]): void {
    if (!existsSync(this.repo)) return
    this.walk(['--all', '--reflog', ...this.specialHeads()])
    // A dangling commit (a commit-edit preview's tip) is in no ref: seed it.
    const extra = extraTokens.filter(
      (t) => t.length === 40 && !this.info.has(t) && gitOk(this.repo, ['cat-file', '-t', t]).trim() === 'commit'
    )
    if (extra.length) this.walk(extra)
    for (const line of gitOk(this.repo, ['for-each-ref', '--format=%(objectname) %(objecttype) %(refname)', 'refs/tags']).split('\n')) {
      const [sha, type, ref] = line.split(' ')
      if (type === 'tag' && ref) this.tags.set(sha, ref)
    }
    this.shas = [...this.info.keys(), ...this.tags.keys()].sort()
  }

  private specialHeads(): string[] {
    const out: string[] = []
    for (const h of ['MERGE_HEAD', 'CHERRY_PICK_HEAD', 'REVERT_HEAD', 'REBASE_HEAD', 'ORIG_HEAD']) {
      const sha = gitOk(this.repo, ['rev-parse', '--verify', '-q', `${h}^{commit}`]).trim()
      if (sha) out.push(sha)
    }
    return out
  }

  private walk(seeds: string[]): void {
    const raw = gitOk(this.repo, [
      'log',
      '--no-show-signature',
      '--format=%H%x00%T%x00%P%x00%an <%ae>%x00%cn <%ce>%x00%B%x1e',
      ...seeds
    ])
    for (let rec of raw.split('\x1e')) {
      if (rec.startsWith('\n')) rec = rec.slice(1)
      if (!rec) continue
      const [sha, tree, parents, author, committer, ...body] = rec.split('\x00')
      if (!sha || this.info.has(sha)) continue
      this.info.set(sha, {
        tree,
        parents: parents ? parents.split(' ').filter(Boolean) : [],
        author,
        committer,
        body: body.join('\x00')
      })
    }
  }

  private unique(prefix: string): string | null {
    let found: string | null = null
    for (const s of this.shas) {
      if (s.startsWith(prefix)) {
        if (found) return null
        found = s
      }
    }
    return found
  }

  private label(sha: string): string | null {
    const hit = this.labels.get(sha)
    if (hit) return hit
    const c = this.info.get(sha)
    if (!c || this.busy.has(sha)) return null
    this.busy.add(sha)
    const parents = c.parents.map((p) => this.label(p) ?? p)
    const msg = this.text(c.body.replace(ISO_RE, '<ISO>'))
    const h = createHash('sha1')
      .update(`${c.tree}\n${parents.join(' ')}\n${c.author}\n${c.committer}\n${msg}`)
      .digest('hex')
    const label = `c${h.slice(0, 10)}`
    this.busy.delete(sha)
    this.labels.set(sha, label)
    return label
  }

  /** Replace every hex token that names a known commit (or tag object). */
  text(s: string): string {
    return s.replace(TOKEN_RE, (tok) => {
      if (!HEX_RE.test(tok)) return tok
      const full = this.unique(tok)
      if (!full) return tok
      const tag = this.tags.get(full)
      if (tag) return tok.length === 40 ? `<tag:${tag}>` : `<tag:${tag}:short>`
      const label = this.label(full)
      if (!label) return tok
      return tok.length === 40 ? `<${label}>` : `<${label}:short>`
    })
  }
}

const DATE_KEYS = new Set(['date', 'time', 'lastCommitAt', 'scannedAt', 'authorDate'])

interface NormCtx {
  repo: string
  clone: boolean
  labeler: ShaLabeler
}

function collectTokens(v: unknown, out: Set<string>): void {
  if (typeof v === 'string') {
    for (const m of v.matchAll(TOKEN_RE)) if (HEX_RE.test(m[0])) out.add(m[0])
  } else if (Array.isArray(v)) v.forEach((x) => collectTokens(x, out))
  else if (v && typeof v === 'object') Object.values(v).forEach((x) => collectTokens(x, out))
}

function normString(s: string, ctx: NormCtx): string {
  const pairs: [string, string][] = []
  if (ctx.repo) {
    pairs.push([ctx.repo, '<REPO>'])
    try {
      const real = realpathSync(ctx.repo)
      if (real !== ctx.repo) pairs.push([real, '<REPO>'])
    } catch {
      /* missing path */
    }
  }
  pairs.push([PLAYGROUND, '<PLAYGROUND>'])
  if (HOME) pairs.push([HOME, '<HOME>'])
  pairs.push([tmpdir(), '<TMP>'])
  for (const [from, to] of pairs) if (from) s = s.split(from).join(to)
  if (ctx.clone && ctx.repo) s = s.split(basename(ctx.repo)).join('<REPONAME>')
  s = s.replace(ISO_RE, '<ISO>').replace(WIP_RE, 'refs/gitcito/wip/<N>').replace(DATE_LINE_RE, '$1<DATE>')
  return ctx.labeler.text(s)
}

function normValue(v: unknown, ctx: NormCtx, key = ''): unknown {
  if (v === undefined) return undefined
  if (DATE_KEYS.has(key) && ((typeof v === 'number' && v !== 0) || (typeof v === 'string' && v !== ''))) return '<DATE>'
  if (key === 'activity' && Array.isArray(v)) return `<ACTIVITY:${v.length}>`
  if (typeof v === 'string') return normString(v, ctx)
  if (Array.isArray(v)) return v.map((x) => normValue(x, ctx))
  if (v && typeof v === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, x] of Object.entries(v)) {
      const n = normValue(x, ctx, k)
      if (n !== undefined) out[k] = n
    }
    return out
  }
  return v
}

/** What a write left behind. Lists whose order git does not define are sorted. */
function repoState(repo: string): Record<string, unknown> {
  const lines = (s: string): string[] => s.split('\n').filter(Boolean)
  const untracked = lines(gitOk(repo, ['ls-files', '-o', '--exclude-standard'])).map(
    (f) => `${gitOk(repo, ['hash-object', '--', f]).trim()} ${f}`
  )
  const gitDir = gitOk(repo, ['rev-parse', '--absolute-git-dir']).trim()
  const ops = ['MERGE_HEAD', 'CHERRY_PICK_HEAD', 'REVERT_HEAD', 'REBASE_HEAD', 'rebase-merge', 'rebase-apply'].filter(
    (n) => gitDir && existsSync(join(gitDir, n))
  )
  return {
    status: lines(gitOk(repo, ['status', '--porcelain=v2', '--branch'])),
    refs: lines(gitOk(repo, ['for-each-ref', '--format=%(objectname) %(refname)'])),
    log: lines(gitOk(repo, ['log', '--format=%H%x00%P%x00%s', '--all'])),
    stash: lines(gitOk(repo, ['stash', 'list', '--format=%H %gs'])),
    index: lines(gitOk(repo, ['ls-files', '-s'])),
    diff: gitOk(repo, ['diff', '--no-ext-diff']),
    untracked,
    ops
  }
}

function normalize(value: unknown, repo: string, clone: boolean): unknown {
  const labeler = new ShaLabeler(repo)
  const tokens = new Set<string>()
  collectTokens(value, tokens)
  labeler.load([...tokens])
  const out = normValue(value, { repo, clone, labeler }) as Record<string, unknown>
  // The state's log lists every commit; its order follows commit dates, which
  // the playground does not pin, so it is compared as a set.
  const state = out.state as { log?: string[] } | undefined
  if (state?.log) state.log.sort()
  return out
}

// ─── Running a case ─────────────────────────────────────────────────────────

const extraDirs: string[] = []

// Placeholders keep args stable across playground regenerations:
//   {rev:<expr>}         → rev-parse <expr>
//   {each:<pattern>}     → objectname of the first ref matching <pattern>
//   {eachref:<pattern>}  → that ref's full name
function resolveArgs(v: unknown, repo: string): unknown {
  if (typeof v === 'string') {
    const m = /^\{(rev|each|eachref):(.+)\}$/.exec(v)
    if (!m) return v
    if (m[1] === 'rev') return git(repo, ['rev-parse', m[2]]).trim()
    const fmt = m[1] === 'each' ? '%(objectname)' : '%(refname)'
    return git(repo, ['for-each-ref', '--count=1', `--format=${fmt}`, m[2]]).trim()
  }
  if (Array.isArray(v)) return v.map((x) => resolveArgs(x, repo))
  if (v && typeof v === 'object') {
    return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, resolveArgs(x, repo)]))
  }
  return v
}

function runSetup(repo: string, steps: Step[] = []): void {
  for (const s of steps) {
    if ('git' in s) git(repo, s.git)
    else if ('gitTry' in s) gitOk(repo, s.gitTry)
    else if ('write' in s) {
      mkdirSync(join(repo, s.write, '..'), { recursive: true })
      writeFileSync(join(repo, s.write), s.content)
    } else if ('rm' in s) unlinkSync(join(repo, s.rm))
    else if ('applyCached' in s) {
      execFileSync('git', ['-C', repo, 'apply', '--cached', ...(s.reverse ? ['--reverse'] : []), '-'], {
        input: s.applyCached
      })
    }
  }
}

async function capture(c: Case): Promise<Record<string, unknown>> {
  const write = !READS.has(c.method)
  let repo = ''
  let clone = false
  if (c.fixture === '@missing') repo = '/definitely/not/a/repo'
  else if (c.fixture === '@plain') {
    repo = mkdtempSync(join(tmpdir(), 'gitcito-plain-'))
    extraDirs.push(repo)
  } else if (c.fixture === '@none') repo = ''
  else if (write || c.setup) {
    repo = cloneFixture(c.fixture)
    clone = true
  } else repo = repoPath(c.fixture)

  runSetup(repo, c.setup)
  let args = c.args
  let steps: Step[] = c.setup ?? []
  if (c.argsFn) {
    const got = await c.argsFn(repo)
    if (Array.isArray(got)) args = got
    else {
      args = got.args
      steps = [...steps, ...got.steps]
    }
  }
  const callArgs = resolveArgs(args, repo) as unknown[]
  const fn = (gitService as unknown as Record<string, (...a: unknown[]) => Promise<unknown>>)[c.method]
  const out: Record<string, unknown> = { method: c.method, fixture: c.fixture, args }
  if (steps.length) out.setup = steps
  try {
    const result = c.fixture === '@none' ? await fn(...callArgs) : await fn(repo, ...callArgs)
    out.result = result === undefined ? null : result
    if (c.method === 'version' && result === gitVersion) out.result = '<GITVERSION>'
  } catch (e) {
    // What the renderer sees: the dispatcher redacts and strips colour codes.
    out.error = stripAnsi(redactCredentials(e instanceof Error ? e.message : String(e)))
  }
  if (write && repo && c.fixture !== '@missing') out.state = repoState(repo)
  return normalize(out, repo, clone) as Record<string, unknown>
}

// ─── Line patches, built the way the diff viewer builds them ────────────────

async function linePatch(
  repo: string,
  file: string,
  direction: PatchDirection,
  pick: (text: string, kind: 'add' | 'del') => boolean
): Promise<string> {
  const diff = await gitService.diffFile(repo, file, direction === 'unstage', false)
  const lines = parseDiff(diff)
  const selected = new Set<number>()
  lines.forEach((l, i) => {
    if ((l.kind === 'add' || l.kind === 'del') && pick(l.text, l.kind)) selected.add(i)
  })
  const patch = buildLinePatch(lines, diffHeader(diff), selected, direction)
  if (!patch) throw new Error('no patch')
  return patch
}

/** Run `steps` now and hand them back for the golden's setup. */
function applied(repo: string, args: unknown[], steps: Step[]): { args: unknown[]; steps: Step[] } {
  runSetup(repo, steps)
  return { args, steps }
}

const numbered = (n: number): string => Array.from({ length: n }, (_, k) => `line ${k + 1}`).join('\n') + '\n'
const commitFile = (file: string, committed: string, working: string): Step[] => [
  { write: file, content: committed },
  { git: ['add', file] },
  { git: ['commit', '-qm', `add ${file}`] },
  { write: file, content: working }
]
const MANY = numbered(40)
const MANY_SKIP = MANY.replace('line 3\n', 'line 3\nextra A\nextra B\n').replace('line 30\n', 'line 30\nwanted\n')
const MANY_UNWANTED = MANY.replace('line 3\n', 'line 3\nextra A\nextra B\n').replace('line 30\n', 'line 30\nunwanted\n')
const MANY_TWO = MANY.replace('line 5\n', 'line 5\nfirst\n').replace('line 20\n', '').replace('line 35\n', 'line 35\nlast\n')

// ─── Shared setups ──────────────────────────────────────────────────────────

const MERGING: Step[] = [{ gitTry: ['merge', 'feature'] }]
const REBASING: Step[] = [{ gitTry: ['rebase', 'main'] }]
const PICKING: Step[] = [{ gitTry: ['cherry-pick', 'feature'] }]
const REVERTING: Step[] = [
  { write: 'log.txt', content: 'line 1\nline 2\n' },
  { git: ['commit', '-qam', 'add line 2'] },
  { write: 'log.txt', content: 'line 1\nline 2 changed\n' },
  { git: ['commit', '-qam', 'change line 2'] },
  { gitTry: ['revert', '--no-edit', 'HEAD~1'] }
]
const FETCHED: Step[] = [{ git: ['fetch', '-q', 'origin'] }]
// Two versions of one branch: a commit kept, one rewritten, one dropped, one new.
const REWRITTEN: Step[] = [
  { git: ['checkout', '-q', '-b', 'v1', 'main'] },
  { write: 'notes.txt', content: 'one\n' },
  { git: ['add', 'notes.txt'] },
  { git: ['commit', '-qm', 'add notes'] },
  { write: 'src/config.js', content: 'export const config = {\n  timeout: 2000,\n  retries: 1\n}\n' },
  { git: ['commit', '-qam', 'add retries'] },
  { write: 'debug.txt', content: 'debug\n' },
  { git: ['add', 'debug.txt'] },
  { git: ['commit', '-qm', 'add debug file'] },
  { git: ['checkout', '-q', '-b', 'v2', 'main'] },
  { write: 'notes.txt', content: 'one\n' },
  { git: ['add', 'notes.txt'] },
  { git: ['commit', '-qm', 'add notes'] },
  { write: 'src/config.js', content: 'export const config = {\n  timeout: 2000,\n  retries: 2\n}\n' },
  { git: ['commit', '-qam', 'add retries'] },
  { write: 'limit.txt', content: 'limit\n' },
  { git: ['add', 'limit.txt'] },
  { git: ['commit', '-qm', 'add rate limit'] },
  { git: ['checkout', '-q', 'main'] }
]
const STAGED: Step[] = [{ git: ['add', '-A'] }]
const COLLIDE: Step[] = [{ write: 'delta-untracked.txt', content: 'local, in the way\n' }]

// ─── Cases ──────────────────────────────────────────────────────────────────

const REFS = ['chore/merged', 'feature/big-refactor', 'feature/config-bump', 'feature/docs', 'feature/rename-api', 'legacy/import']

const cases: Case[] = [
  // open
  { method: 'open', fixture: 'merge-conflict', name: 'main', args: [] },
  { method: 'open', fixture: 'detached-head', name: 'detached', args: [] },
  { method: 'open', fixture: 'empty-repo', name: 'unborn', args: [] },
  { method: 'open', fixture: '@missing', name: 'missing', args: [] },
  { method: 'open', fixture: '@plain', name: 'not-a-repo', args: [] },

  // stashBaseShas
  { method: 'stashBaseShas', fixture: 'stash-picking', name: 'one', args: [] },
  { method: 'stashBaseShas', fixture: 'merge-conflict', name: 'none', args: [] },
  { method: 'stashBaseShas', fixture: 'line-staging', name: 'two-same-base', args: [], setup: [
    { git: ['stash', 'push', '-q', '-m', 'first'] },
    { write: 'README.md', content: 'changed\n' },
    { git: ['stash', 'push', '-q', '-m', 'second'] }
  ] },

  // log
  { method: 'log', fixture: 'dated-history', name: 'default', args: [] },
  { method: 'log', fixture: 'dated-history', name: 'window', args: [3, 2] },
  { method: 'log', fixture: 'interactive-rebase', name: 'linear', args: [] },
  { method: 'log', fixture: 'stash-picking', name: 'stash-base', args: [] },
  { method: 'log', fixture: 'empty-repo', name: 'empty', args: [] },
  // Without its hotfix branch: two tips committed in the same second or not reorder --date-order.
  { method: 'log', fixture: 'tags-and-releases', name: 'tags', args: [3, 0] , setup: [{ git: ['branch', '-D', 'hotfix/security-patch'] }, { git: ['tag', '-d', 'v1.0.2'] }] },
  { method: 'log', fixture: 'collaborators', name: 'coauthors', args: [3] },

  // branches
  { method: 'branches', fixture: 'merge-conflict', name: 'two', args: [] },
  { method: 'branches', fixture: 'multi-remote', name: 'remotes', args: [] },
  { method: 'branches', fixture: 'tags-and-releases', name: 'tags', args: [] },
  { method: 'branches', fixture: 'detached-head', name: 'detached', args: [] },
  { method: 'branches', fixture: 'empty-repo', name: 'unborn', args: [] },
  { method: 'branches', fixture: 'conflict-radar', name: 'merged', args: [] },
  { method: 'branches', fixture: 'teammate-radar', name: 'tracking', args: [] },
  { method: 'branches', fixture: 'merge-conflict', name: 'symbolic-remote-head', args: [], setup: [
    { git: ['update-ref', 'refs/remotes/origin/base', 'main'] },
    { git: ['update-ref', 'refs/remotes/origin/feature/topic', 'feature'] },
    { git: ['symbolic-ref', 'refs/remotes/origin/HEAD', 'refs/remotes/origin/feature/topic'] }
  ] },

  // mergePreview
  { method: 'mergePreview', fixture: 'conflict-radar', name: 'all', args: ['main', REFS] },
  { method: 'mergePreview', fixture: 'conflict-radar', name: 'filtered', args: ['main', ['feature/docs', '', 'bad ref', 'feature/docs', 'no/such-branch', 'feature/rename-api']] },
  { method: 'mergePreview', fixture: 'conflict-radar', name: 'empty', args: ['main', []] },
  { method: 'mergePreview', fixture: 'conflict-radar', name: 'bad-base', args: ['no-such-base', REFS] },

  // teammateRadar
  { method: 'teammateRadar', fixture: 'teammate-radar', name: 'all', args: [{ maxDays: 100000 }] },
  { method: 'teammateRadar', fixture: 'teammate-radar', name: 'one-branch', args: [{ maxDays: 100000, maxBranches: 1 }] },
  { method: 'teammateRadar', fixture: 'teammate-radar', name: 'too-old', args: [{ maxDays: 0 }] },
  { method: 'teammateRadar', fixture: 'stash-picking', name: 'no-remotes', args: [] },
  { method: 'teammateRadar', fixture: 'empty-repo', name: 'unborn', args: [] },

  // rangeDiff
  // Without a base the force-push fixture is not stable (see fetchRemote), so
  // those cases use two rewritten branches built during setup.
  { method: 'rangeDiff', fixture: 'force-push', name: 'with-base', args: ['origin/feature/login@{1}', 'origin/feature/login', 'main'], setup: FETCHED },
  { method: 'rangeDiff', fixture: 'conflict-radar', name: 'merge-base', args: ['v1', 'v2'], setup: REWRITTEN },
  { method: 'rangeDiff', fixture: 'conflict-radar', name: 'reflog-selector', args: ['v1@{0}', 'v2'], setup: REWRITTEN },
  { method: 'rangeDiff', fixture: 'conflict-radar', name: 'blank-base', args: ['v1', 'v2', '  '], setup: REWRITTEN },
  { method: 'rangeDiff', fixture: 'force-push', name: 'self', args: ['main', 'main'] },
  { method: 'rangeDiff', fixture: 'force-push', name: 'bad-rev', args: ['no-such-rev', 'main'] },
  { method: 'rangeDiff', fixture: 'conflict-radar', name: 'unrelated', args: ['main', 'legacy/import'] },

  // refTips
  { method: 'refTips', fixture: 'force-push', name: 'forced', args: ['origin/feature/login'], setup: FETCHED },
  { method: 'refTips', fixture: 'force-push', name: 'missing', args: ['refs/heads/does-not-exist'] },
  { method: 'refTips', fixture: 'reflog-recovery', name: 'head-max', args: ['HEAD', 3] },

  // repoPulse
  { method: 'repoPulse', fixture: 'absorb', name: 'ahead', args: [] },
  { method: 'repoPulse', fixture: 'absorb', name: 'staged', args: [], setup: STAGED },
  { method: 'repoPulse', fixture: 'merge-conflict', name: 'merging', args: [], setup: MERGING },
  { method: 'repoPulse', fixture: 'stash-picking', name: 'stashes', args: [] },
  { method: 'repoPulse', fixture: 'detached-head', name: 'detached', args: [] },
  { method: 'repoPulse', fixture: 'empty-repo', name: 'unborn', args: [] },
  { method: 'repoPulse', fixture: '@missing', name: 'missing', args: [] },

  // repoDetail
  { method: 'repoDetail', fixture: 'absorb', name: 'default', args: [] },
  { method: 'repoDetail', fixture: 'absorb', name: 'capped', args: [2] },
  { method: 'repoDetail', fixture: 'deep-history-monorepo', name: 'capped', args: [3] },
  { method: 'repoDetail', fixture: 'merge-conflict', name: 'conflicted', args: [], setup: MERGING },
  { method: 'repoDetail', fixture: '@missing', name: 'missing', args: [] },

  // status
  { method: 'status', fixture: 'line-staging', name: 'unstaged', args: [] },
  { method: 'status', fixture: 'snapshots', name: 'mixed', args: [] },
  { method: 'status', fixture: 'absorb', name: 'tracking', args: [] },
  { method: 'status', fixture: 'empty-repo', name: 'unborn', args: [] },
  { method: 'status', fixture: 'detached-head', name: 'detached', args: [] },
  { method: 'status', fixture: 'untracked-mess', name: 'untracked', args: [] },
  { method: 'status', fixture: 'merge-conflict', name: 'conflicted', args: [], setup: MERGING },
  { method: 'status', fixture: 'line-staging', name: 'renamed', args: [], setup: [
    { git: ['mv', 'README.md', 'NOTES.md'] },
    { write: 'NOTES.md', content: 'rewritten after the move\n' }
  ] },
  { method: 'status', fixture: 'line-staging', name: 'both-sides', args: [], setup: [
    { git: ['add', 'config.js'] },
    { write: 'config.js', content: 'module.exports = {}\n' },
    { write: 'new file.txt', content: 'spaces in the name\n' },
    { rm: 'README.md' }
  ] },
  { method: 'status', fixture: '@missing', name: 'missing', args: [] },

  // mergeState / mergeMessage / conflictContext / conflictVersions
  { method: 'mergeState', fixture: 'merge-conflict', name: 'none', args: [] },
  { method: 'mergeState', fixture: 'merge-conflict', name: 'merge', args: [], setup: MERGING },
  { method: 'mergeState', fixture: 'rebase-conflict', name: 'rebase', args: [], setup: REBASING },
  { method: 'mergeState', fixture: 'cherry-pick', name: 'cherry-pick', args: [], setup: PICKING },
  { method: 'mergeState', fixture: 'cherry-pick', name: 'revert', args: [], setup: REVERTING },
  { method: 'mergeState', fixture: '@plain', name: 'not-a-repo', args: [] },
  { method: 'mergeMessage', fixture: 'merge-conflict', name: 'none', args: [] },
  { method: 'mergeMessage', fixture: 'merge-conflict', name: 'merge', args: [], setup: MERGING },
  { method: 'mergeMessage', fixture: 'cherry-pick', name: 'cherry-pick', args: [], setup: PICKING },
  { method: 'conflictContext', fixture: 'merge-conflict', name: 'none', args: [] },
  { method: 'conflictContext', fixture: 'merge-conflict', name: 'merge', args: [], setup: MERGING },
  { method: 'conflictContext', fixture: 'rebase-conflict', name: 'rebase', args: [], setup: REBASING },
  { method: 'conflictContext', fixture: 'cherry-pick', name: 'cherry-pick', args: [], setup: PICKING },
  { method: 'conflictContext', fixture: 'cherry-pick', name: 'revert', args: [], setup: REVERTING },
  { method: 'conflictContext', fixture: 'merge-conflict', name: 'merge-tag', args: [], setup: [
    { git: ['tag', '-a', '-m', 'release', 'v-feature', 'feature'] },
    { gitTry: ['merge', 'v-feature'] }
  ] },
  { method: 'conflictVersions', fixture: 'merge-conflict', name: 'greeting', args: ['greeting.txt'], setup: MERGING },
  { method: 'conflictVersions', fixture: 'merge-conflict', name: 'clean-file', args: ['units_service.dart'], setup: MERGING },
  { method: 'conflictVersions', fixture: 'merge-conflict', name: 'missing', args: ['nope.txt'] },

  // conflict writes
  { method: 'resolveConflict', fixture: 'merge-conflict', name: 'greeting', args: ['greeting.txt', 'Hello and hola\n'], setup: MERGING },
  { method: 'resolveConflict', fixture: 'merge-conflict', name: 'missing-dir', args: ['no/such/dir.txt', 'x\n'] },
  { method: 'conflictTakeSide', fixture: 'merge-conflict', name: 'ours', args: ['greeting.txt', 'ours'], setup: MERGING },
  { method: 'conflictTakeSide', fixture: 'merge-conflict', name: 'theirs', args: ['greeting.txt', 'theirs'], setup: MERGING },
  { method: 'conflictTakeSide', fixture: 'merge-conflict', name: 'delete', args: ['greeting.txt', 'delete'], setup: MERGING },
  { method: 'conflictTakeSide', fixture: 'merge-conflict', name: 'not-conflicted', args: ['greeting.txt', 'ours'] },
  { method: 'conflictOpContinue', fixture: 'cherry-pick', name: 'cherry-pick', args: ['cherry-pick'], setup: [
    ...PICKING,
    { git: ['checkout', '--theirs', '--', '.'] },
    { git: ['add', '-A'] }
  ] },
  { method: 'conflictOpContinue', fixture: 'merge-conflict', name: 'merge-unresolved', args: ['merge'], setup: MERGING },
  { method: 'conflictOpContinue', fixture: 'merge-conflict', name: 'merge', args: ['merge'], setup: [
    ...MERGING,
    { gitTry: ['checkout', '--ours', '--', '.'] },
    { git: ['add', '-A'] }
  ] },
  { method: 'conflictOpContinue', fixture: 'rebase-conflict', name: 'rebase', args: ['rebase'], setup: [
    ...REBASING,
    { git: ['checkout', '--theirs', '--', '.'] },
    { git: ['add', '-A'] }
  ] },
  { method: 'conflictOpContinue', fixture: 'cherry-pick', name: 'revert', args: ['revert'], setup: [
    ...REVERTING,
    { git: ['checkout', '--ours', '--', '.'] },
    { git: ['add', '-A'] }
  ] },
  { method: 'conflictOpAbort', fixture: 'merge-conflict', name: 'merge', args: ['merge'], setup: MERGING },
  { method: 'conflictOpAbort', fixture: 'rebase-conflict', name: 'rebase', args: ['rebase'], setup: REBASING },
  { method: 'conflictOpAbort', fixture: 'cherry-pick', name: 'cherry-pick', args: ['cherry-pick'], setup: PICKING },
  { method: 'conflictOpAbort', fixture: 'cherry-pick', name: 'revert', args: ['revert'], setup: REVERTING },
  { method: 'conflictOpAbort', fixture: 'merge-conflict', name: 'nothing', args: ['merge'] },

  // stashes / stash writes
  { method: 'stashes', fixture: 'stash-picking', name: 'one', args: [] },
  { method: 'stashes', fixture: 'merge-conflict', name: 'none', args: [] },
  { method: 'stashes', fixture: 'line-staging', name: 'no-untracked', args: [], setup: [
    { git: ['stash', 'push', '-q'] },
    { write: 'README.md', content: 'changed\n' },
    { git: ['stash', 'push', '-q', '-m', 'second, with a message'] }
  ] },
  { method: 'stash', fixture: 'line-staging', name: 'message', args: ['wip message'] },
  { method: 'stash', fixture: 'line-staging', name: 'no-message', args: [] },
  { method: 'stash', fixture: 'changelog', name: 'clean', args: ['nothing'] },
  { method: 'stashPush', fixture: 'snapshots', name: 'paths', args: ['only a', ['partial-a.txt']], setup: [
    { write: 'partial-a.txt', content: 'a\n' },
    { write: 'partial-b.txt', content: 'b\n' }
  ] },
  { method: 'stashPush', fixture: 'snapshots', name: 'keep-index', args: ['keep test', ['keep.txt'], true], setup: [
    { write: 'keep.txt', content: 'keep\n' },
    { git: ['add', '-A'] }
  ] },
  { method: 'stashPush', fixture: 'snapshots', name: 'everything', args: [] },
  { method: 'stashPush', fixture: 'snapshots', name: 'bad-path', args: ['x', ['no-such-file.txt']] },
  { method: 'stashPop', fixture: 'stash-picking', name: 'default', args: [] },
  { method: 'stashPop', fixture: 'stash-picking', name: 'missing', args: [3] },
  { method: 'stashPop', fixture: 'stash-picking', name: 'collide', args: [0], setup: COLLIDE },
  { method: 'stashApply', fixture: 'stash-picking', name: 'default', args: [] },
  { method: 'stashApply', fixture: 'stash-picking', name: 'collide', args: [0], setup: COLLIDE },
  { method: 'stashDrop', fixture: 'stash-picking', name: 'default', args: [0] },
  { method: 'stashDrop', fixture: 'merge-conflict', name: 'none', args: [] },
  { method: 'stashToBranch', fixture: 'stash-picking', name: 'branch', args: ['from-stash'] },
  { method: 'stashToBranch', fixture: 'stash-picking', name: 'exists', args: ['main'] },
  { method: 'renameStash', fixture: 'stash-picking', name: 'rename', args: [0, '  a better name  '] },
  { method: 'renameStash', fixture: 'line-staging', name: 'second', args: [1, 'older one'], setup: [
    { git: ['stash', 'push', '-q', '-m', 'first'] },
    { write: 'README.md', content: 'changed\n' },
    { git: ['stash', 'push', '-q', '-m', 'second'] }
  ] },
  { method: 'renameStash', fixture: 'stash-picking', name: 'bad-index', args: [5, 'x'] },
  { method: 'renameStash', fixture: 'merge-conflict', name: 'no-stash', args: [0, 'x'] },
  { method: 'stashApplyFiles', fixture: 'stash-picking', name: 'partial', args: ['{rev:stash@{0}}', ['alpha.txt'], ['delta-untracked.txt']] },
  { method: 'stashApplyFiles', fixture: 'stash-picking', name: 'tracked-only', args: ['{rev:stash@{0}}', ['src/gamma.txt'], []] },
  { method: 'stashApplyFiles', fixture: 'stash-picking', name: 'nothing', args: ['{rev:stash@{0}}', [], []] },
  { method: 'stashApplyOverwrite', fixture: 'stash-picking', name: 'apply', args: [0, false], setup: COLLIDE },
  { method: 'stashApplyOverwrite', fixture: 'stash-picking', name: 'pop', args: [0, true], setup: COLLIDE },
  { method: 'stashApplyOverwrite', fixture: 'stash-picking', name: 'missing', args: [4] },
  { method: 'stashApplyOverwrite', fixture: 'line-staging', name: 'no-untracked-tree', args: [], setup: [{ git: ['stash', 'push', '-q'] }] },

  // restoreFromCommit
  { method: 'restoreFromCommit', fixture: 'stash-picking', name: 'alpha', args: ['{rev:HEAD}', ['alpha.txt']], setup: [
    { write: 'alpha.txt', content: 'local edit\n' }
  ] },
  { method: 'restoreFromCommit', fixture: 'stash-picking', name: 'empty', args: ['{rev:HEAD}', []] },
  { method: 'restoreFromCommit', fixture: 'stash-picking', name: 'bad-path', args: ['{rev:HEAD}', ['nope.txt']] },

  // staging
  { method: 'stage', fixture: 'line-staging', name: 'one', args: [['config.js']] },
  { method: 'stage', fixture: 'absorb', name: 'untracked', args: [['src/cache.ts']] },
  { method: 'stage', fixture: 'line-staging', name: 'missing', args: [['nope.txt']] },
  { method: 'stageAll', fixture: 'absorb', name: 'all', args: [] },
  { method: 'stageAll', fixture: 'snapshots', name: 'deletions', args: [], setup: [{ rm: 'draft.md' }] },
  { method: 'unstage', fixture: 'snapshots', name: 'notes', args: [['notes.md']] },
  { method: 'unstage', fixture: 'absorb', name: 'some', args: [['src/auth.ts']], setup: STAGED },
  { method: 'unstage', fixture: 'absorb', name: 'missing', args: [['nope.ts']] },
  { method: 'unstageAll', fixture: 'absorb', name: 'all', args: [], setup: STAGED },
  { method: 'unstageAll', fixture: 'empty-repo', name: 'unborn', args: [] },

  // stagePatch — the line-staging tests' patches, built by the renderer's builder
  { method: 'stagePatch', fixture: 'line-staging', name: 'added-line', args: [],
    argsFn: async (R) => [await linePatch(R, 'config.js', 'stage', (t) => t.includes('debug')), false] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'removed-line', args: [],
    argsFn: async (R) => [await linePatch(R, 'config.js', 'stage', (t, k) => k === 'del' && t.includes('localhost')), false] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'unstage-line', args: [], setup: [{ git: ['add', 'config.js'] }],
    argsFn: async (R) => [await linePatch(R, 'config.js', 'unstage', (t) => t.includes('tls')), true] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'unstage-removed', args: [], setup: [{ git: ['add', 'config.js'] }],
    argsFn: async (R) => [await linePatch(R, 'config.js', 'unstage', (t, k) => k === 'del' && t.includes('localhost')), true] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'undo', args: [],
    argsFn: async (R) => {
      const p = await linePatch(R, 'config.js', 'stage', (t) => t.includes('debug'))
      return applied(R, [p, true], [{ applyCached: p }])
    } },
  { method: 'stagePatch', fixture: 'line-staging', name: 'whole-hunk', args: [],
    argsFn: async (R) => {
      const diff = await gitService.diffFile(R, 'config.js', false, false)
      const lines = parseDiff(diff)
      return [buildLinePatch(lines, diffHeader(diff), hunkLines(lines, 0), 'stage')!]
    } },
  { method: 'stagePatch', fixture: 'line-staging', name: 'later-hunk', args: [], setup: commitFile('many.txt', MANY, MANY_SKIP),
    argsFn: async (R) => [await linePatch(R, 'many.txt', 'stage', (t) => t === 'wanted')] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'later-hunk-undo', args: [], setup: commitFile('many.txt', MANY, MANY_SKIP),
    argsFn: async (R) => {
      const p = await linePatch(R, 'many.txt', 'stage', (t) => t === 'wanted')
      return applied(R, [p, true], [{ applyCached: p }])
    } },
  { method: 'stagePatch', fixture: 'line-staging', name: 'later-hunk-unstage', args: [],
    setup: [...commitFile('many.txt', MANY, MANY_UNWANTED), { git: ['add', 'many.txt'] }],
    argsFn: async (R) => [await linePatch(R, 'many.txt', 'unstage', (t) => t === 'unwanted'), true] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'two-hunks', args: [], setup: commitFile('many.txt', MANY, MANY_TWO),
    argsFn: async (R) => [await linePatch(R, 'many.txt', 'stage', (t) => t === 'last' || t === 'line 20')] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'two-hunks-back', args: [], setup: commitFile('many.txt', MANY, MANY_TWO),
    argsFn: async (R) => {
      const first = await linePatch(R, 'many.txt', 'stage', (t) => t === 'last' || t === 'line 20')
      runSetup(R, [{ applyCached: first }])
      const back = await linePatch(R, 'many.txt', 'unstage', (t) => t === 'line 20')
      return { args: [back, true], steps: [{ applyCached: first }] }
    } },
  { method: 'stagePatch', fixture: 'line-staging', name: 'no-newline-marker', args: [],
    setup: commitFile('eof.txt', 'one\ntwo\nthree', 'one\ninserted\ntwo\nthree\nfour'),
    argsFn: async (R) => [await linePatch(R, 'eof.txt', 'stage', (t) => t === 'inserted')] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'no-newline-end', args: [],
    setup: commitFile('eof.txt', 'one\ntwo\nthree', 'one\ninserted\ntwo\nthree\nfour'),
    argsFn: async (R) => [await linePatch(R, 'eof.txt', 'stage', (t) => t === 'three' || t === 'four')] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'backslash-context', args: [],
    setup: commitFile('doc.tex', '\\begin{doc}\nbody\n\\end{doc}\n', '\\begin{doc}\nbody\nmore\n\\end{doc}\n'),
    argsFn: async (R) => [await linePatch(R, 'doc.tex', 'stage', (t) => t === 'more')] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'new-file-line', args: [],
    setup: [{ write: 'fresh.txt', content: 'keep 1\ndrop\nkeep 2\n' }, { git: ['add', 'fresh.txt'] }],
    argsFn: async (R) => [await linePatch(R, 'fresh.txt', 'unstage', (t) => t === 'drop'), true] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'new-file-whole', args: [],
    setup: [{ write: 'fresh.txt', content: 'a\nb\n' }, { git: ['add', 'fresh.txt'] }],
    argsFn: async (R) => [await linePatch(R, 'fresh.txt', 'unstage', () => true), true] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'all-lines-stage', args: [],
    argsFn: async (R) => [await linePatch(R, 'config.js', 'stage', () => true)] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'corrupt', args: ['not a patch\n'] },
  { method: 'stagePatch', fixture: 'line-staging', name: 'does-not-apply', args: [
    'diff --git a/config.js b/config.js\n--- a/config.js\n+++ b/config.js\n@@ -1,2 +1,2 @@\n-nothing like this\n+here\n context\n'
  ] },

  // interactive rebase
  { method: 'interactiveRebaseSteps', fixture: 'interactive-rebase', name: 'main', args: ['main'] },
  { method: 'interactiveRebaseSteps', fixture: 'interactive-rebase', name: 'bad-base', args: ['no-such-base'] },
  { method: 'runInteractiveRebase', fixture: 'interactive-rebase', name: 'fixup-all', args: [], argsFn: async (R) => {
    const steps = await gitService.interactiveRebaseSteps(R, 'main')
    return ['main', steps.map((s, i) => ({ action: i === 0 ? 'pick' : 'fixup', hash: `{rev:HEAD~${steps.length - 1 - i}}`, subject: s.subject }))]
  } },
  { method: 'runInteractiveRebase', fixture: 'interactive-rebase', name: 'reword-drop', args: [], argsFn: async (R) => {
    const steps = await gitService.interactiveRebaseSteps(R, 'main')
    return ['main', steps.map((s, i) => ({
      action: i === 0 ? 'reword' : i === 1 ? 'drop' : 'pick',
      hash: `{rev:HEAD~${steps.length - 1 - i}}`,
      subject: s.subject,
      ...(i === 0 ? { newMessage: "feat: it's a \\login\\ function" } : {})
    }))]
  } },
  { method: 'runInteractiveRebase', fixture: 'interactive-rebase', name: 'edit-pauses', args: [], argsFn: async (R) => {
    const steps = await gitService.interactiveRebaseSteps(R, 'main')
    return ['main', steps.map((s, i) => ({ action: i === 1 ? 'edit' : 'pick', hash: `{rev:HEAD~${steps.length - 1 - i}}`, subject: s.subject }))]
  } },
  { method: 'runInteractiveRebase', fixture: 'interactive-rebase', name: 'squash-conflict', args: [], argsFn: async (R) => {
    const steps = await gitService.interactiveRebaseSteps(R, 'main')
    // Dropping the commit that introduced auth.ts makes the next one conflict.
    return ['main', steps.map((s, i) => ({ action: i === 0 ? 'drop' : 'pick', hash: `{rev:HEAD~${steps.length - 1 - i}}`, subject: s.subject }))]
  } },

  // absorb
  { method: 'absorbPlan', fixture: 'absorb', name: 'staged', args: [], setup: STAGED },
  { method: 'absorbPlan', fixture: 'absorb', name: 'no-staged', args: [] },
  { method: 'absorbPlan', fixture: 'merge-conflict', name: 'in-progress', args: [], setup: MERGING },
  { method: 'absorbPlan', fixture: 'absorb', name: 'pushed', args: [], setup: [
    { git: ['update-ref', 'refs/remotes/origin/main', 'HEAD'] },
    ...STAGED
  ] },
  { method: 'absorbPlan', fixture: 'interactive-rebase', name: 'trunk', args: [], setup: [
    { write: 'auth.ts', content: "export const login = () => fetch('/auth/login')\n" },
    ...STAGED
  ] },
  { method: 'absorbApply', fixture: 'absorb', name: 'fixups', args: [], setup: STAGED },
  { method: 'absorbApply', fixture: 'absorb', name: 'rebase', args: [{ rebase: true }], setup: STAGED },
  { method: 'absorbApply', fixture: 'absorb', name: 'nothing', args: [] },

  // stagePatch-free reflog
  { method: 'reflog', fixture: 'reflog-recovery', name: 'head', args: [] },
  { method: 'reflog', fixture: 'reflog-recovery', name: 'main-max', args: ['main', 2] },
  { method: 'reflog', fixture: 'reflog-recovery', name: 'missing', args: ['no-such-ref'] },
  { method: 'reflog', fixture: 'stash-picking', name: 'stash', args: ['refs/stash'] },

  // remotes
  { method: 'remotes', fixture: 'multi-remote', name: 'two', args: [] },
  { method: 'remotes', fixture: 'merge-conflict', name: 'none', args: [] },
  { method: 'remotes', fixture: 'merge-conflict', name: 'push-only', args: [], setup: [
    { git: ['remote', 'add', 'mirror', 'https://example.com/a.git'] },
    { git: ['remote', 'set-url', '--push', 'mirror', 'https://example.com/push.git'] }
  ] },
  { method: 'addRemote', fixture: 'merge-conflict', name: 'plain', args: ['upstream', 'https://example.com/up.git'] },
  { method: 'addRemote', fixture: 'merge-conflict', name: 'push-url', args: ['upstream', 'https://example.com/up.git', 'git@example.com:up.git'] },
  { method: 'addRemote', fixture: 'merge-conflict', name: 'same-push-url', args: ['upstream', 'https://example.com/up.git', 'https://example.com/up.git'] },
  { method: 'addRemote', fixture: 'multi-remote', name: 'exists', args: ['origin', 'https://example.com/x.git'] },
  { method: 'removeRemote', fixture: 'multi-remote', name: 'upstream', args: ['upstream'] },
  { method: 'removeRemote', fixture: 'multi-remote', name: 'missing', args: ['nope'] },
  { method: 'editRemote', fixture: 'multi-remote', name: 'rename-and-url', args: ['upstream', 'source', 'https://example.com/src.git'] },
  { method: 'editRemote', fixture: 'multi-remote', name: 'push-url', args: ['upstream', 'upstream', 'https://example.com/src.git', 'git@example.com:src.git'] },
  { method: 'editRemote', fixture: 'multi-remote', name: 'keep-name', args: ['upstream', '', 'https://example.com/src.git'] },
  { method: 'editRemote', fixture: 'multi-remote', name: 'rename-exists', args: ['upstream', 'origin', ''] },
  { method: 'editRemote', fixture: 'multi-remote', name: 'missing', args: ['nope', '', 'https://example.com/x.git'] },
  // (force-push is not fetched here: whether its two "add login form" commits
  // share a sha depends on whether the scenario crossed a second boundary.)
  { method: 'fetchRemote', fixture: 'multi-remote', name: 'restores', args: ['upstream'], setup: [
    { git: ['update-ref', '-d', 'refs/remotes/upstream/main'] }
  ] },
  { method: 'fetchRemote', fixture: 'multi-remote', name: 'prunes', args: ['upstream'], setup: [
    { git: ['update-ref', 'refs/remotes/upstream/gone', 'HEAD'] }
  ] },
  { method: 'fetchRemote', fixture: 'multi-remote', name: 'missing', args: ['nope'] },

  // commit editing
  // (bisect-bug is not used: its .bisect-hint holds a sha, so its trees change on every regeneration.)
  { method: 'commitEditInfo', fixture: 'interactive-rebase', name: 'root', args: ['{rev:HEAD~6}'] },
  { method: 'commitEditInfo', fixture: 'interactive-rebase', name: 'head', args: ['{rev:HEAD}'] },
  { method: 'commitEditInfo', fixture: 'collaborators', name: 'root', args: ['{rev:main~3}'] },
  { method: 'commitEditInfo', fixture: 'absorb', name: 'pushed', args: ['{rev:HEAD~3}'] },
  { method: 'commitEditInfo', fixture: 'merge-conflict', name: 'not-ancestor', args: ['{rev:feature}'] },
  { method: 'commitEditInfo', fixture: 'merge-conflict', name: 'bad-sha', args: ['deadbeefdeadbeef'] },
  { method: 'blobAtCommit', fixture: 'interactive-rebase', name: 'text', args: ['{rev:HEAD}', 'auth.ts'] },
  { method: 'blobAtCommit', fixture: 'binary-images-unicode', name: 'binary', args: ['{rev:HEAD}', 'logo.png'] },
  { method: 'blobAtCommit', fixture: 'interactive-rebase', name: 'missing', args: ['{rev:HEAD}', 'nope.txt'] },
  { method: 'commitEditPreview', fixture: 'interactive-rebase', name: 'clean', args: ['{rev:HEAD~6}', { 'index.ts': "export const VERSION = '1.0.1'\n" }, ''] },
  { method: 'commitEditPreview', fixture: 'interactive-rebase', name: 'conflict', args: ['{rev:HEAD~5}', { 'auth.ts': 'export const login = () => null\n' }, 'sabotage'] },
  { method: 'commitEditPreview', fixture: 'interactive-rebase', name: 'reword', args: ['{rev:HEAD~3}', {}, 'chore: reworded from the future'] },
  { method: 'commitEditPreview', fixture: 'interactive-rebase', name: 'two-files', args: ['{rev:HEAD~1}', { 'session.ts': 'export {}\n', 'auth.ts': '// moved\n' }, ''] },
  { method: 'commitEditPreview', fixture: 'interactive-rebase', name: 'not-editable', args: ['{rev:HEAD~3}', { 'no/such/file.txt': 'x' }, ''] },
  { method: 'commitEditPreview', fixture: 'merge-conflict', name: 'not-ancestor', args: ['{rev:feature}', {}, 'x'] },
  { method: 'commitEditPreview', fixture: 'collaborators', name: 'across-merges', args: ['{rev:main~3}', {}, 'root, reworded'] },
  { method: 'commitEditApply', fixture: 'interactive-rebase', name: 'clean', args: ['{rev:HEAD~6}', { 'index.ts': "export const VERSION = '1.0.1'\n" }, ''] },
  { method: 'commitEditApply', fixture: 'interactive-rebase', name: 'reword', args: ['{rev:HEAD~3}', {}, 'chore: reworded from the future'] },
  { method: 'commitEditApply', fixture: 'interactive-rebase', name: 'dirty-tree', args: ['{rev:HEAD~3}', {}, 'reworded with local edits'], setup: [
    { write: 'index.ts', content: 'local edit\n' },
    { write: 'scratch.txt', content: 'untracked\n' }
  ] },
  { method: 'commitEditApply', fixture: 'interactive-rebase', name: 'conflict', args: ['{rev:HEAD~5}', { 'auth.ts': 'export const login = () => null\n' }, 'sabotage'] },
  { method: 'commitEditApply', fixture: 'detached-head', name: 'detached', args: ['{rev:HEAD~1}', {}, 'x'] },
  { method: 'commitEditApply', fixture: 'merge-conflict', name: 'not-ancestor', args: ['{rev:feature}', {}, 'x'] },

  // snapshots
  { method: 'listSnapshots', fixture: 'snapshots', name: 'seeded', args: [] },
  { method: 'listSnapshots', fixture: 'merge-conflict', name: 'none', args: [] },
  { method: 'createSnapshot', fixture: 'snapshots', name: 'manual', args: [] },
  { method: 'createSnapshot', fixture: 'snapshots', name: 'auto', args: ['auto'] },
  { method: 'createSnapshot', fixture: 'snapshots', name: 'auto-dedupe', args: ['auto'], setup: [
    { git: ['add', '-A'] },
    { git: ['stash', 'create'] }
  ] },
  { method: 'createSnapshot', fixture: 'snapshots', name: 'pruned', args: ['manual', 2] },
  { method: 'createSnapshot', fixture: 'changelog', name: 'clean', args: [] },
  { method: 'createSnapshot', fixture: 'empty-repo', name: 'unborn', args: ['guard'], setup: [
    { write: 'first.txt', content: 'first\n' }
  ] },
  { method: 'restoreSnapshot', fixture: 'snapshots', name: 'one-file', args: ['{each:refs/gitcito/wip/*-m}', ['draft.md']] },
  { method: 'restoreSnapshot', fixture: 'snapshots', name: 'whole', args: ['{each:refs/gitcito/wip/*-g}'] },
  { method: 'deleteSnapshot', fixture: 'snapshots', name: 'one', args: ['{eachref:refs/gitcito/wip/*-a}'] },
  { method: 'deleteSnapshot', fixture: 'snapshots', name: 'missing', args: ['refs/gitcito/wip/1-m'] },

  // writeChangelogFile
  { method: 'writeChangelogFile', fixture: 'changelog', name: 'new-file', args: ['## v1.1.0\n\n- feat: things\n\n\n'] },
  { method: 'writeChangelogFile', fixture: 'changelog', name: 'prepend', args: ['## v1.2.0\n\n- fix: more'], setup: [
    { write: 'CHANGELOG.md', content: '# Changelog\n\n## v1.1.0\n\n- feat: things\n' }
  ] },
  { method: 'writeChangelogFile', fixture: 'changelog', name: 'no-header', args: ['## v2\n'], setup: [
    { write: 'CHANGELOG.md', content: 'Some older notes\n' }
  ] },

  // version
  { method: 'version', fixture: '@none', name: 'git', args: [] }
]

beforeAll(() => {
  isolateEnv()
  gitVersion = /version (\d+\.\d+\.\d+)/.exec(execFileSync('git', ['--version'], { encoding: 'utf8' }))?.[1] ?? ''
})

afterAll(() => {
  cleanupFixtures()
  for (const d of extraDirs.splice(0)) rmSync(d, { recursive: true, force: true })
  if (HOME) rmSync(HOME, { recursive: true, force: true })
})

describe('slice A goldens', () => {
  const seen = new Set<string>()
  for (const c of cases) {
    const file = join(GOLDENS, c.method, `${c.fixture.replace(/^@/, '')}-${c.name}.json`)
    if (seen.has(file)) throw new Error(`duplicate golden ${file}`)
    seen.add(file)
    it(`${c.method} ${c.fixture} ${c.name}`, async () => {
      const got = await capture(c)
      if (WRITE) {
        mkdirSync(join(GOLDENS, c.method), { recursive: true })
        writeFileSync(file, JSON.stringify(got, null, 2) + '\n')
      }
      expect(got).toEqual(JSON.parse(readFileSync(file, 'utf8')))
    })
  }
})
