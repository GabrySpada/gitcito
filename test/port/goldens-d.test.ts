// Goldens for Wave 1 slice D of the git service port (sync, clone/init,
// submodules, worktrees, subtrees, hooks, LFS, attributes, filters,
// credential helpers, replace refs, maintenance, bundles/archives, clean,
// .gitcito.json, user identity). Replayed by internal/gitsvc/d_golden_test.go.
//
// PORT_WRITE_GOLDENS=1 writes internal/gitsvc/testdata/goldens/<method>/<fixture>-<case>.json;
// without it this re-runs the TypeScript and asserts it still produces what the
// files say, so the goldens stay guarded for as long as the TS exists.
//
// A golden is self-describing so the Go side can replay it without code of its
// own per case: `repo` says what the repository is (the shared playground repo,
// a copy of it, an empty temp dir, a fresh `git init`), `setup` is a list of
// data ops that put it into the state the case needs, `args` may carry
// placeholders (<REPO>, <NAME> for anything setup created, <REV:spec>), and
// `capture` lists extra observations taken after the call.
//
// Normalization (identical on both sides):
//   - absolute paths → <REPO>, <NAME>, <HOME>, <TMP>, <PLAYGROUND>, <ROOT>;
//   - the playground does not pin commit dates, so every object name differs
//     each time it is regenerated: full shas are renamed <SHA1>, <SHA2>… in order
//     of first appearance (walking keys sorted, arrays in order), and an
//     abbreviation of a known sha becomes its label plus "~";
//   - keys listed in `mask` (sizes on disk, wall-clock ages) keep 0 and lose
//     the exact value: a positive number becomes "<POS>", anything else "<MASKED>".
import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { execFileSync } from 'node:child_process'
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  utimesSync,
  writeFileSync
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, isAbsolute, join, resolve } from 'node:path'
import { gitService, redactCredentials, stripAnsi } from '../../src/main/git'
import { cloneFixture, cleanupFixtures } from '../fixtures'

const ROOT = resolve(__dirname, '../..')
const PLAYGROUND = join(ROOT, 'examples', 'playground')
const GOLDENS = join(ROOT, 'internal', 'gitsvc', 'testdata', 'goldens')
const WRITE = process.env.PORT_WRITE_GOLDENS === '1'

// ─── The case language ──────────────────────────────────────────────────────

type Op =
  | { git: string[]; dir?: string }
  | { write: string; content: string }
  | { mkdir: string }
  | { rm: string }
  | { mtime: string; ms: number }
  | { chmod: string; mode: number }
  | { copy: string; as: string }
  | { tmpdir: string }

type Capture =
  | { git: string[]; dir?: string }
  | { read: string }
  | { exists: string }
  | { mode: string }
  | { tar: string }
  | { list: string }

interface Case {
  method: string
  /** Playground repo name ('' for none). */
  fixture: string
  name: string
  repo: 'shared' | 'clone' | 'tmp' | 'init'
  setup?: Op[]
  args: unknown[]
  /** Snapshot the repository state after the call (write methods). */
  state?: boolean
  /** Snapshot the directory the method returned instead of the repo (clone/init). */
  stateOf?: 'result'
  capture?: Capture[]
  mask?: string[]
}

// Shorthands.
const sh = (method: string, fixture: string, name: string, args: unknown[], extra: Partial<Case> = {}): Case => ({
  method,
  fixture,
  name,
  repo: 'shared',
  args,
  ...extra
})
const cl = (method: string, fixture: string, name: string, args: unknown[], extra: Partial<Case> = {}): Case => ({
  method,
  fixture,
  name,
  repo: 'clone',
  args,
  state: true,
  ...extra
})
const commit = (msg: string, dir?: string): Op => ({ git: ['commit', '-q', '-m', msg], ...(dir ? { dir } : {}) })
const addAll = (dir?: string): Op => ({ git: ['add', '-A'], ...(dir ? { dir } : {}) })

// Remotes in the fixtures are absolute paths into the shared playground. A case
// that pushes must own copies, or it would rewrite the playground for everyone.
const ownRemotes = (fixture: string, remotes: string[]): Op[] =>
  remotes.flatMap((r) => [
    { copy: `${fixture}-${r}.git`, as: r.toUpperCase() } as Op,
    { git: ['remote', 'set-url', r, `<${r.toUpperCase()}>`] } as Op
  ])

/**
 * A bare `origin` built from the case's own repository, at <O>/origin.git, with
 * `main` pushed and tracked. The fixture remotes rebuild history on every
 * playground run (a rewritten commit made in the same second as the original
 * IS the original), so cases that need a remote to move under them script one.
 */
const scriptedOrigin: Op[] = [
  { tmpdir: 'O' },
  { git: ['init', '-q', '--bare', '-b', 'main', '<O>/origin.git'], dir: '<O>' },
  { git: ['remote', 'add', 'origin', '<O>/origin.git'] },
  { git: ['push', '-q', '-u', 'origin', 'main'] }
]

/** A second working clone of <O>/origin.git, at <W>/theirs, to publish from. */
const theirs: Op[] = [{ tmpdir: 'W' }, { git: ['clone', '-q', '-b', 'main', '<O>/origin.git', '<W>/theirs'], dir: '<W>' }]

/** Commit one new file in <W>/theirs and push it. */
const publish = (file: string, msg: string, branch = 'main'): Op[] => [
  { write: `<W>/theirs/${file}`, content: `${msg}\n` },
  addAll('<W>/theirs'),
  commit(msg, '<W>/theirs'),
  { git: ['push', '-q', 'origin', branch], dir: '<W>/theirs' }
]

const SUBMODULE_SETUP: Op[] = [
  // file:// submodules are refused by default since git 2.38.1; the isolated
  // global config is the only config a submodule clone reads.
  { git: ['config', '--global', 'protocol.file.allow', 'always'] },
  { copy: 'cherry-pick', as: 'SUB' },
  { git: ['submodule', 'add', '-q', '<SUB>', 'libs/sub'] },
  commit('chore: add submodule')
]

const cases: Case[] = [
  // ── sync ────────────────────────────────────────────────────────────────
  cl('fetchAll', 'bisect-bug', 'forced', ['<REPO>'], {
    setup: [
      ...scriptedOrigin,
      { git: ['checkout', '-q', '-b', 'feature'] },
      { write: 'feature.txt', content: 'one\n' },
      addAll(),
      commit('feat: one'),
      { git: ['push', '-q', '-u', 'origin', 'feature'] },
      { git: ['checkout', '-q', 'main'] },
      ...theirs,
      { git: ['checkout', '-q', 'feature'], dir: '<W>/theirs' },
      { git: ['commit', '-q', '--amend', '-m', 'feat: one, rewritten'], dir: '<W>/theirs' },
      { git: ['push', '-q', '--force', 'origin', 'feature'], dir: '<W>/theirs' },
      { git: ['checkout', '-q', '-b', 'fresh'], dir: '<W>/theirs' },
      { git: ['push', '-q', 'origin', 'fresh'], dir: '<W>/theirs' }
    ]
  }),
  cl('fetchAll', 'multi-remote', 'two-remotes', ['<REPO>']),
  cl('fetchAll', 'bisect-bug', 'no-remotes', ['<REPO>']),
  cl('fetchAll', 'bisect-bug', 'broken-remote', ['<REPO>'], {
    setup: [{ git: ['remote', 'add', 'origin', '/nowhere/at/all.git'] }]
  }),

  sh('lastFetchAt', 'empty-repo', 'never', ['<REPO>']),
  cl('lastFetchAt', 'bisect-bug', 'fetch-head', ['<REPO>'], {
    state: false,
    setup: [
      { write: '.git/FETCH_HEAD', content: '' },
      { mtime: '.git/FETCH_HEAD', ms: 1700000000000 }
    ]
  }),
  { method: 'lastFetchAt', fixture: '', name: 'not-a-repo', repo: 'tmp', args: ['<REPO>'] },

  cl('pull', 'bisect-bug', 'ff', ['<REPO>'], { setup: [...scriptedOrigin, ...theirs, ...publish('NEWS.md', 'docs: news')] }),
  cl('pull', 'bisect-bug', 'autostash', ['<REPO>', 'ff-only'], {
    setup: [
      ...scriptedOrigin,
      ...theirs,
      ...publish('NEWS.md', 'docs: news'),
      { write: 'README.md', content: '# dirty\n' },
      { write: 'untracked.txt', content: 'u\n' }
    ]
  }),
  cl('pull', 'bisect-bug', 'rebase-mode', ['<REPO>', 'rebase'], {
    setup: [...scriptedOrigin, ...theirs, ...publish('NEWS.md', 'docs: news'), { write: 'LOCAL.md', content: 'mine\n' }, addAll(), commit('docs: local')]
  }),
  cl('pull', 'bisect-bug', 'ff-only-diverged', ['<REPO>', 'ff-only'], {
    setup: [...scriptedOrigin, ...theirs, ...publish('NEWS.md', 'docs: news'), { write: 'LOCAL.md', content: 'mine\n' }, addAll(), commit('docs: local')]
  }),
  cl('pull', 'bisect-bug', 'conflict', ['<REPO>'], {
    setup: [
      ...scriptedOrigin,
      ...theirs,
      { write: '<W>/theirs/math.js', content: 'theirs\n' },
      { git: ['commit', '-q', '-am', 'fix: theirs'], dir: '<W>/theirs' },
      { git: ['push', '-q', 'origin', 'main'], dir: '<W>/theirs' },
      { write: 'math.js', content: 'mine\n' },
      { git: ['commit', '-q', '-am', 'fix: mine'] }
    ]
  }),
  cl('pull', 'bisect-bug', 'no-upstream', ['<REPO>']),

  cl('upstreamSuggestion', 'force-push', 'tracking', ['<REPO>'], { state: false }),
  cl('upstreamSuggestion', 'force-push', 'remote-has-it', ['<REPO>'], {
    state: false,
    setup: [{ git: ['checkout', '-q', 'feature/login'] }, { git: ['branch', '--unset-upstream', 'feature/login'] }]
  }),
  cl('upstreamSuggestion', 'force-push', 'local-only', ['<REPO>'], {
    state: false,
    setup: [{ git: ['checkout', '-q', '-b', 'local-only'] }]
  }),
  cl('upstreamSuggestion', 'force-push', 'detached', ['<REPO>'], {
    state: false,
    setup: [{ git: ['checkout', '-q', '--detach', 'main'] }]
  }),
  cl('upstreamSuggestion', 'multi-remote', 'first-remote', ['<REPO>'], {
    state: false,
    setup: [{ git: ['remote', 'remove', 'origin'] }, { git: ['checkout', '-q', '-b', 'topic'] }]
  }),
  sh('upstreamSuggestion', 'bisect-bug', 'no-remotes', ['<REPO>']),

  cl('setUpstream', 'force-push', 'unset', ['<REPO>', 'feature/login', null], {
    capture: [{ git: ['config', '--get-regexp', '^branch\\.'] }]
  }),
  cl('setUpstream', 'force-push', 'set', ['<REPO>', 'feature/login', 'origin'], {
    setup: [{ git: ['branch', '--unset-upstream', 'feature/login'] }],
    capture: [{ git: ['config', '--get-regexp', '^branch\\.'] }]
  }),
  cl('setUpstream', 'force-push', 'missing-remote-branch', ['<REPO>', 'nope', 'origin'], {
    setup: [{ git: ['branch', 'nope'] }]
  }),
  cl('setUpstream', 'force-push', 'unset-untracked', ['<REPO>', 'nope', null], {
    setup: [{ git: ['branch', 'nope'] }]
  }),

  cl('push', 'multi-remote', 'new-branch', ['<REPO>', 'feature/push-test'], {
    setup: [...ownRemotes('multi-remote', ['origin', 'upstream']), { git: ['checkout', '-q', '-b', 'feature/push-test'] }],
    capture: [{ git: ['for-each-ref', '--format=%(refname) %(objectname)'], dir: '<ORIGIN>' }]
  }),
  cl('push', 'multi-remote', 'to-upstream', ['<REPO>', 'main', { remote: 'upstream' }], {
    setup: [...ownRemotes('multi-remote', ['origin', 'upstream'])]
  }),
  cl('push', 'multi-remote', 'force-with-lease', ['<REPO>', 'main', { force: true, remote: 'upstream' }], {
    setup: [...ownRemotes('multi-remote', ['origin', 'upstream']), { git: ['fetch', '-q', 'upstream'] }],
    capture: [{ git: ['rev-parse', 'main'], dir: '<UPSTREAM>' }]
  }),
  cl('push', 'multi-remote', 'missing-remote', ['<REPO>', 'main', { remote: 'nope' }]),

  cl('pullBranch', 'bisect-bug', 'fast-forward', ['<REPO>', 'feature'], {
    setup: [
      ...scriptedOrigin,
      { git: ['branch', '-q', 'feature'] },
      { git: ['push', '-q', '-u', 'origin', 'feature'] },
      ...theirs,
      { git: ['checkout', '-q', 'feature'], dir: '<W>/theirs' },
      ...publish('A.md', 'feat: a', 'feature'),
      { git: ['fetch', '-q', 'origin'] }
    ]
  }),
  cl('pullBranch', 'bisect-bug', 'diverged', ['<REPO>', 'feature'], {
    setup: [
      ...scriptedOrigin,
      { git: ['checkout', '-q', '-b', 'feature'] },
      { write: 'feature.txt', content: 'one\n' },
      addAll(),
      commit('feat: one'),
      { git: ['push', '-q', '-u', 'origin', 'feature'] },
      { git: ['checkout', '-q', 'main'] },
      ...theirs,
      { git: ['checkout', '-q', 'feature'], dir: '<W>/theirs' },
      { git: ['commit', '-q', '--amend', '-m', 'feat: one, rewritten'], dir: '<W>/theirs' },
      { git: ['push', '-q', '--force', 'origin', 'feature'], dir: '<W>/theirs' }
    ]
  }),
  cl('pullBranch', 'bisect-bug', 'checked-out', ['<REPO>', 'main'], {
    setup: [...scriptedOrigin, ...theirs, ...publish('NEWS.md', 'docs: news')]
  }),
  cl('pullBranch', 'force-push', 'no-upstream', ['<REPO>', 'local-only'], {
    setup: [{ git: ['branch', 'local-only'] }]
  }),

  cl('setBranchRef', 'force-push', 'move', ['<REPO>', 'feature/login', '<REV:main>']),
  cl('setBranchRef', 'force-push', 'checked-out', ['<REPO>', 'main', '<REV:feature/login>']),

  cl('pushToRemotes', 'multi-remote', 'both', ['<REPO>', 'feature/push-test', ['origin', 'upstream']], {
    setup: [...ownRemotes('multi-remote', ['origin', 'upstream']), { git: ['checkout', '-q', '-b', 'feature/push-test'] }],
    capture: [
      { git: ['rev-parse', 'feature/push-test'], dir: '<ORIGIN>' },
      { git: ['rev-parse', 'feature/push-test'], dir: '<UPSTREAM>' }
    ]
  }),
  cl('pushToRemotes', 'multi-remote', 'one-fails', ['<REPO>', 'feature/push-test', [' origin ', 'upstream', '']], {
    setup: [
      ...ownRemotes('multi-remote', ['origin', 'upstream']),
      { git: ['remote', 'set-url', 'upstream', '/nowhere/at/all.git'] },
      { git: ['checkout', '-q', '-b', 'feature/push-test'] }
    ]
  }),
  cl('pushToRemotes', 'multi-remote', 'tags-force', ['<REPO>', 'main', ['upstream'], { force: true, tags: true }], {
    setup: [
      ...ownRemotes('multi-remote', ['origin', 'upstream']),
      { git: ['fetch', '-q', 'upstream'] },
      { git: ['tag', '-a', 'v9.9.9', '-m', 'Release'] }
    ],
    capture: [{ git: ['tag', '--list'], dir: '<UPSTREAM>' }]
  }),
  cl('pushToRemotes', 'multi-remote', 'empty', ['<REPO>', 'main', ['  ']]),

  cl('pushAllTags', 'multi-remote', 'origin', ['<REPO>', 'origin'], {
    setup: [
      ...ownRemotes('multi-remote', ['origin', 'upstream']),
      { git: ['tag', 'v9.9.9-light'] },
      { git: ['tag', '-a', 'v9.9.9', '-m', 'Release'] }
    ],
    capture: [{ git: ['tag', '--list'], dir: '<ORIGIN>' }]
  }),
  cl('pushAllTags', 'multi-remote', 'default-remote', ['<REPO>'], {
    setup: [...ownRemotes('multi-remote', ['origin', 'upstream']), { git: ['tag', 'only-light'] }],
    capture: [{ git: ['tag', '--list'], dir: '<ORIGIN>' }]
  }),
  cl('pushAllTags', 'bisect-bug', 'no-remote', ['<REPO>', 'origin']),

  cl('staleLocks', 'stacked-branches', 'old-and-fresh', ['<REPO>'], {
    state: false,
    mask: ['ageSeconds'],
    setup: [
      { write: '.git/index.lock', content: '' },
      { mtime: '.git/index.lock', ms: 1000000000000 },
      { write: '.git/refs/heads/feature/api.lock', content: '' },
      { mtime: '.git/refs/heads/feature/api.lock', ms: 1100000000000 },
      { write: '.git/config.lock', content: '' },
      { mtime: '.git/config.lock', ms: 1200000000000 },
      { write: '.git/something.lock', content: '' },
      { mtime: '.git/something.lock', ms: 1300000000000 },
      { mkdir: '.git/modules/x' },
      { write: '.git/modules/x/index.lock', content: '' },
      { write: '.git/objects/pack/tmp.lock', content: '' }
    ]
  }),
  sh('staleLocks', 'stacked-branches', 'none', ['<REPO>']),

  cl('clearLocks', 'stacked-branches', 'fresh', ['<REPO>', ['index.lock']], {
    setup: [{ write: '.git/index.lock', content: '' }],
    capture: [{ exists: '.git/index.lock' }]
  }),
  cl('clearLocks', 'stacked-branches', 'old', ['<REPO>', ['index.lock', 'refs/heads/feature/api.lock', 'index.lock']], {
    setup: [
      { write: '.git/index.lock', content: '' },
      { mtime: '.git/index.lock', ms: 1000000000000 },
      { write: '.git/refs/heads/feature/api.lock', content: '' },
      { mtime: '.git/refs/heads/feature/api.lock', ms: 1000000000000 }
    ],
    capture: [{ exists: '.git/index.lock' }, { exists: '.git/refs/heads/feature/api.lock' }]
  }),
  cl('clearLocks', 'stacked-branches', 'escape', ['<REPO>', ['../victim.txt', '../../etc/hosts', 'config', 'x\u0000.lock']], {
    setup: [{ write: 'victim.txt', content: 'still here' }],
    capture: [{ exists: 'victim.txt' }]
  }),

  // ── clone / init ────────────────────────────────────────────────────────
  sh('remoteBranches', '', 'local', ['<PLAYGROUND>/merge-conflict']),
  sh('remoteBranches', '', 'missing', ['/nowhere/at/all.git']),
  { method: 'clone', fixture: '', name: 'full', repo: 'tmp', args: ['<REPO>', '<PLAYGROUND>/merge-conflict', 'full'], state: true, stateOf: 'result', capture: [{ git: ['remote', 'get-url', 'origin'], dir: '<REPO>/full' }] },
  { method: 'clone', fixture: '', name: 'shallow', repo: 'tmp', args: ['<REPO>', '<PLAYGROUND>/merge-conflict', 'shallow', { depth: 1 }], state: true, stateOf: 'result', capture: [{ exists: '<REPO>/shallow/.git/shallow' }, { git: ['remote', 'get-url', 'origin'], dir: '<REPO>/shallow' }] },
  { method: 'clone', fixture: '', name: 'branch', repo: 'tmp', args: ['<REPO>', '<PLAYGROUND>/merge-conflict', 'onbranch', { branch: ' feature ' }], state: true, stateOf: 'result' },
  { method: 'clone', fixture: '', name: 'single-branch', repo: 'tmp', args: ['<REPO>', '<PLAYGROUND>/merge-conflict', 'single', { singleBranch: true }], state: true, stateOf: 'result' },
  { method: 'clone', fixture: '', name: 'shallow-all-branches', repo: 'tmp', args: ['<REPO>', '<PLAYGROUND>/merge-conflict', '', { depth: 1.7, singleBranch: false }], state: true, stateOf: 'result' },
  { method: 'clone', fixture: '', name: 'exists', repo: 'tmp', setup: [{ mkdir: 'taken' }], args: ['<REPO>', '<PLAYGROUND>/merge-conflict', 'taken'] },
  { method: 'clone', fixture: '', name: 'no-source', repo: 'tmp', args: ['<REPO>', '/nowhere/at/all.git', 'x'] },
  { method: 'clone', fixture: '', name: 'no-parent', repo: 'tmp', args: ['<REPO>/missing', '<PLAYGROUND>/merge-conflict', 'x'] },
  { method: 'init', fixture: '', name: 'named', repo: 'tmp', args: ['<REPO>', ' proj '], state: true, stateOf: 'result', capture: [{ git: ['symbolic-ref', 'HEAD'], dir: '<REPO>/proj' }] },
  { method: 'init', fixture: '', name: 'default-name', repo: 'tmp', args: ['<REPO>', ''], capture: [{ exists: '<REPO>/my-repo/.git' }] },
  { method: 'init', fixture: '', name: 'exists', repo: 'tmp', setup: [{ mkdir: 'proj' }], args: ['<REPO>', 'proj'] },
  { method: 'initHere', fixture: '', name: 'empty-dir', repo: 'tmp', args: ['<REPO>'], capture: [{ git: ['symbolic-ref', 'HEAD'] }] },
  cl('initHere', 'bisect-bug', 'reinit', ['<REPO>']),
  { method: 'initHere', fixture: '', name: 'missing-dir', repo: 'tmp', args: ['<REPO>/missing'] },

  // ── replace refs ────────────────────────────────────────────────────────
  sh('replacements', 'deep-history-monorepo', 'none', ['<REPO>']),
  // One replacement per case: `git replace -l` lists them in sha order, and the
  // playground's shas change every time it is regenerated.
  cl('replacements', 'deep-history-monorepo', 'graft', ['<REPO>'], {
    state: false,
    setup: [{ git: ['replace', '--graft', '<REV:HEAD~5>'] }, { git: ['config', 'core.useReplaceRefs', 'false'] }]
  }),
  cl('replacements', 'deep-history-monorepo', 'object', ['<REPO>'], {
    state: false,
    setup: [{ git: ['replace', '-f', '<REV:HEAD~2>', '<REV:HEAD~7>'] }]
  }),
  cl('replaceGraft', 'deep-history-monorepo', 'root', ['<REPO>', ' <REV:HEAD~5> ', []], {
    capture: [{ git: ['rev-list', '--count', 'HEAD'] }]
  }),
  cl('replaceGraft', 'deep-history-monorepo', 'parent', ['<REPO>', '<REV:HEAD~5>', ['<REV:HEAD~8>', ' ']], {
    capture: [{ git: ['rev-list', '--count', 'HEAD'] }]
  }),
  cl('replaceGraft', 'deep-history-monorepo', 'empty', ['<REPO>', '  ', []]),
  cl('replaceGraft', 'deep-history-monorepo', 'bad-commit', ['<REPO>', 'no-such-rev', []]),
  cl('replaceObject', 'deep-history-monorepo', 'ok', ['<REPO>', '<REV:HEAD~2>', ' <REV:HEAD~7> ']),
  cl('replaceObject', 'deep-history-monorepo', 'empty', ['<REPO>', '', '<REV:HEAD>']),
  cl('replaceDelete', 'deep-history-monorepo', 'ok', ['<REPO>', '<REV:HEAD~4>'], {
    setup: [{ git: ['replace', '--graft', '<REV:HEAD~4>'] }]
  }),
  cl('replaceDelete', 'deep-history-monorepo', 'empty', ['<REPO>', ' ']),
  cl('replaceDelete', 'deep-history-monorepo', 'unknown', ['<REPO>', '<REV:HEAD~4>']),
  cl('setUseReplaceRefs', 'deep-history-monorepo', 'off', ['<REPO>', false], {
    capture: [{ git: ['config', '--local', '--get', 'core.useReplaceRefs'] }]
  }),
  cl('setUseReplaceRefs', 'deep-history-monorepo', 'on', ['<REPO>', true], {
    setup: [{ git: ['config', 'core.useReplaceRefs', 'false'] }],
    capture: [{ git: ['config', '--local', '--get', 'core.useReplaceRefs'] }]
  }),
  cl('setUseReplaceRefs', 'deep-history-monorepo', 'on-unset', ['<REPO>', true]),

  // ── credential helpers ──────────────────────────────────────────────────
  sh('credentialStatus', 'bisect-bug', 'none', ['<REPO>'], { mask: ['candidates'] }),
  cl('credentialStatus', 'bisect-bug', 'configured', ['<REPO>'], {
    state: false,
    mask: ['candidates'],
    setup: [
      { git: ['config', '--local', '--add', 'credential.helper', 'cache'] },
      { git: ['config', '--local', '--add', 'credential.helper', 'store --file /tmp/x'] },
      { git: ['config', '--local', '--add', 'credential.helper', 'definitely-not-a-helper'] },
      { git: ['config', '--global', 'credential.helper', '!true'] },
      { git: ['config', '--local', 'credential.https://example.com.helper', 'cache'] },
      { git: ['config', '--local', 'credential.https://example.com.username', 'octocat'] },
      { git: ['config', '--global', 'credential.https://git.example.org.username', 'me'] },
      { git: ['remote', 'add', 'origin', 'https://git.example.com/example/demo.git'] },
      { git: ['remote', 'add', 'other', 'http://user@other.example.com:8080/demo.git'] },
      { git: ['remote', 'add', 'ssh', 'git@ssh.example.com:example/demo.git'] },
      { write: '<HOME>/.git-credentials', content: 'https://a:b@x.example\n\nhttps://c:d@y.example\n' }
    ]
  }),
  cl('setCredentialHelper', 'bisect-bug', 'repo', ['<REPO>', ' store ', 'repo'], {
    setup: [
      { git: ['config', '--local', '--add', 'credential.helper', 'cache'] },
      { git: ['config', '--local', '--add', 'credential.helper', 'osxkeychain'] }
    ],
    capture: [{ git: ['config', '--local', '--get-all', 'credential.helper'] }]
  }),
  cl('setCredentialHelper', 'bisect-bug', 'clear', ['<REPO>', '', 'repo'], {
    setup: [{ git: ['config', '--local', '--add', 'credential.helper', 'cache'] }],
    capture: [{ git: ['config', '--local', '--get-all', 'credential.helper'] }]
  }),
  cl('setCredentialHelper', 'bisect-bug', 'global', ['<REPO>', 'cache', 'global'], {
    capture: [{ git: ['config', '--global', '--get-all', 'credential.helper'] }]
  }),
  cl('forgetCredential', 'bisect-bug', 'empty', ['<REPO>', '  ']),
  cl('forgetCredential', 'bisect-bug', 'no-helper', ['<REPO>', 'example.com']),
  cl('forgetCredential', 'bisect-bug', 'http', ['<REPO>', ' example.com ', 'http']),

  // ── attributes, diff drivers, filters ───────────────────────────────────
  sh('attributeFiles', 'attributes', 'fixture', ['<REPO>']),
  cl('attributeFiles', 'bisect-bug', 'untracked-root', ['<REPO>'], {
    state: false,
    setup: [{ write: '.gitattributes', content: '* text=auto\n' }]
  }),
  sh('attributeFiles', 'bisect-bug', 'none', ['<REPO>']),
  cl('attributeWrite', 'bisect-bug', 'root', ['<REPO>', '.gitattributes', '*.png binary\n'], {
    capture: [{ read: '.gitattributes' }]
  }),
  cl('attributeWrite', 'bisect-bug', 'nested', ['<REPO>', 'deep/er/.gitattributes', '*.md merge=union\n'], {
    capture: [{ read: 'deep/er/.gitattributes' }]
  }),
  cl('attributeWrite', 'bisect-bug', 'info', ['<REPO>', '.git/info/attributes', '*.key -diff\n'], {
    capture: [{ read: '.git/info/attributes' }]
  }),
  cl('attributeWrite', 'bisect-bug', 'not-attributes', ['<REPO>', 'math.js', 'nope']),
  cl('attributeWrite', 'bisect-bug', 'escape', ['<REPO>', '../escape/.gitattributes', 'nope']),
  sh('checkAttributes', 'attributes', 'paths', ['<REPO>', ['CHANGELOG.md', ' design/logo.psd ', 'test/fixture.txt', 'x.scratch', '']]),
  sh('checkAttributes', 'attributes', 'empty', ['<REPO>', ['  ']]),
  sh('diffDrivers', 'bisect-bug', 'none', ['<REPO>']),
  cl('diffDrivers', 'bisect-bug', 'configured', ['<REPO>'], {
    state: false,
    setup: [
      { git: ['config', '--local', 'diff.nonesuch.textconv', 'definitely-not-installed --to-text'] },
      { git: ['config', '--local', 'diff.cat.textconv', 'cat'] },
      { git: ['config', '--local', 'diff.quoted.textconv', '"/bin/cat" -'] },
      { git: ['config', '--global', 'diff.glob.textconv', 'cat'] }
    ]
  }),
  sh('diffDriverSuggestions', 'bisect-bug', 'all', ['<REPO>'], { mask: ['available'] }),
  cl('setDiffDriver', 'bisect-bug', 'set', ['<REPO>', 'nonesuch', ' definitely-not-installed --to-text '], {
    capture: [{ git: ['config', '--local', '--get-regexp', '^diff\\.'] }]
  }),
  cl('setDiffDriver', 'bisect-bug', 'clear', ['<REPO>', 'nonesuch', ' '], {
    setup: [{ git: ['config', '--local', 'diff.nonesuch.textconv', 'x'] }],
    capture: [{ git: ['config', '--local', '--get-regexp', '^diff\\.'] }]
  }),
  cl('setDiffDriver', 'bisect-bug', 'clear-missing', ['<REPO>', 'nonesuch', '']),
  cl('setDiffDriver', 'bisect-bug', 'global', ['<REPO>', 'pdf', 'pdftotext -layout - -', true], {
    capture: [{ git: ['config', '--global', '--get-regexp', '^diff\\.'] }]
  }),
  sh('filterDrivers', 'lfs-assets', 'lfs', ['<REPO>']),
  cl('filterDrivers', 'bisect-bug', 'configured', ['<REPO>'], {
    state: false,
    setup: [
      { git: ['config', '--local', 'filter.vault.clean', 'cat'] },
      { git: ['config', '--local', 'filter.vault.smudge', 'definitely-not-installed --x'] },
      { git: ['config', '--local', 'filter.vault.required', 'true'] },
      { git: ['config', '--local', 'filter.half.required', 'yes'] },
      { git: ['config', '--global', 'filter.glob.clean', 'cat'] }
    ]
  }),
  cl('filterDryRun', 'bisect-bug', 'lossless', ['<REPO>', '*.secret', 'cat', 'cat'], {
    setup: [{ write: 'creds.secret', content: 'token=hunter2\n' }, { git: ['add', 'creds.secret'] }]
  }),
  cl('filterDryRun', 'bisect-bug', 'lossy', ['<REPO>', '*.secret', "tr 'a-z' 'A-Z'", 'cat'], {
    setup: [{ write: 'creds.secret', content: 'token=hunter2\n' }, { git: ['add', 'creds.secret'] }]
  }),
  cl('filterDryRun', 'bisect-bug', 'no-smudge', ['<REPO>', '*.secret', "tr 'a-z' 'A-Z'", ' '], {
    setup: [{ write: 'creds.secret', content: 'token=hunter2\n' }, { git: ['add', 'creds.secret'] }]
  }),
  cl('filterDryRun', 'bisect-bug', 'failing', ['<REPO>', '*.secret', 'sh -c "echo broken >&2; exit 3"', ''], {
    setup: [{ write: 'creds.secret', content: 'token=hunter2\n' }, { git: ['add', 'creds.secret'] }]
  }),
  cl('filterDryRun', 'bisect-bug', 'silent-failure', ['<REPO>', '*.secret', 'exit 4', ''], {
    setup: [{ write: 'creds.secret', content: 'token=hunter2\n' }, { git: ['add', 'creds.secret'] }]
  }),
  cl('filterDryRun', 'bisect-bug', 'percent-f', ['<REPO>', '*.secret', 'echo %f', ''], {
    setup: [{ write: "it's.secret", content: 'x\n' }, { git: ['add', "it's.secret"] }]
  }),
  cl('filterDryRun', 'bisect-bug', 'many', ['<REPO>', '*.js', 'cat', 'cat']),
  sh('filterDryRun', 'bisect-bug', 'unmatched', ['<REPO>', '*.nothing-matches-this', 'cat', 'cat']),
  cl('setFilterDriver', 'bisect-bug', 'new', ['<REPO>', 'vault', { clean: ' cat ', smudge: 'cat', required: true }], {
    capture: [{ git: ['config', '--local', '--get-regexp', '^filter\\.'] }]
  }),
  cl('setFilterDriver', 'bisect-bug', 'overwrite', ['<REPO>', 'vault', { clean: 'tac', smudge: '', required: false }], {
    setup: [
      { git: ['config', '--local', 'filter.vault.clean', 'cat'] },
      { git: ['config', '--local', 'filter.vault.smudge', 'cat'] },
      { git: ['config', '--local', 'filter.vault.required', 'true'] }
    ],
    capture: [{ git: ['config', '--local', '--get-regexp', '^filter\\.'] }]
  }),
  cl('setFilterDriver', 'bisect-bug', 'required-without-commands', ['<REPO>', 'vault', { clean: '', smudge: ' ', required: true }], {
    setup: [{ git: ['config', '--local', 'filter.vault.clean', 'cat'] }],
    capture: [{ git: ['config', '--local', '--get-regexp', '^filter\\.'] }]
  }),
  cl('setFilterDriver', 'bisect-bug', 'global', ['<REPO>', 'g', { clean: 'cat', smudge: 'cat', required: false }, true], {
    capture: [{ git: ['config', '--global', '--get-regexp', '^filter\\.'] }]
  }),

  // ── maintenance, fsck ───────────────────────────────────────────────────
  sh('maintenanceStats', 'bisect-bug', 'fixture', ['<REPO>'], {
    mask: ['looseBytes', 'packBytes', 'garbageBytes', 'prunableBytes', 'gitBytes', 'lastPack']
  }),
  cl('maintenanceStats', 'bisect-bug', 'unreachable', ['<REPO>'], {
    state: false,
    mask: ['looseBytes', 'packBytes', 'garbageBytes', 'prunableBytes', 'gitBytes', 'lastPack'],
    setup: [
      { write: 'doomed.txt', content: 'this commit is about to be abandoned\n' },
      addAll(),
      commit('doomed'),
      { git: ['reset', '-q', '--hard', 'HEAD~1'] },
      { git: ['reflog', 'expire', '--expire=now', '--all'] },
      { git: ['gc', '-q'] },
      { write: '.git/gc.log', content: 'warning: There are too many unreachable loose objects\n' },
      { git: ['config', '--global', '--add', 'maintenance.repo', '<REPO>'] }
    ]
  }),
  cl('maintenanceRun', 'bisect-bug', 'gc', ['<REPO>', 'gc'], {
    mask: ['before', 'after'],
    // Pack sizes follow the commit dates the playground does not pin; whether
    // the loose objects went is the part that is the same every time.
    capture: [{ git: ['count-objects'] }]
  }),
  cl('maintenanceRun', 'bisect-bug', 'prune', ['<REPO>', 'prune'], {
    mask: ['before', 'after'],
    setup: [
      { write: 'doomed.txt', content: 'gone\n' },
      addAll(),
      commit('doomed'),
      { git: ['reset', '-q', '--hard', 'HEAD~1'] },
      { git: ['reflog', 'expire', '--expire=now', '--all'] }
    ],
    capture: [{ git: ['prune', '-n', '--expire=now'] }]
  }),
  cl('maintenanceRun', 'deep-history-monorepo', 'commit-graph', ['<REPO>', 'commitGraph'], {
    mask: ['before', 'after'],
    capture: [{ exists: '.git/objects/info/commit-graph' }]
  }),
  cl('maintenanceRun', 'bisect-bug', 'aggressive', ['<REPO>', 'aggressive'], { mask: ['before', 'after'] }),
  { method: 'maintenanceRun', fixture: '', name: 'not-a-repo', repo: 'tmp', args: ['<REPO>', 'gc'] },
  cl('maintenanceSchedule', 'bisect-bug', 'unregister', ['<REPO>', false], {
    setup: [{ git: ['config', '--global', '--add', 'maintenance.repo', '<REPO>'] }],
    capture: [{ git: ['config', '--global', '--get-all', 'maintenance.repo'] }]
  }),
  cl('maintenanceSchedule', 'bisect-bug', 'unregister-unknown', ['<REPO>', false]),
  sh('fsck', 'bisect-bug', 'healthy', ['<REPO>']),
  cl('fsck', 'bisect-bug', 'dangling', ['<REPO>'], {
    state: false,
    setup: [
      { write: 'orphan.txt', content: 'dropped\n' },
      addAll(),
      commit('orphan'),
      { git: ['reset', '-q', '--hard', 'HEAD~1'] },
      { git: ['reflog', 'expire', '--expire=now', '--all'] }
    ]
  }),
  cl('fsck', 'bisect-bug', 'missing-object', ['<REPO>'], {
    state: false,
    setup: [{ rm: '<OBJ:HEAD:math.js>' }]
  }),

  // ── bundles, archives ───────────────────────────────────────────────────
  sh('bundleCreate', 'tags-and-releases', 'all', ['<REPO>', '<OUT>/all.bundle', { kind: 'all' }], {
    setup: [{ tmpdir: 'OUT' }],
    mask: ['bytes'],
    capture: [{ git: ['bundle', 'list-heads', '<OUT>/all.bundle'] }]
  }),
  sh('bundleCreate', 'tags-and-releases', 'ref', ['<REPO>', '<OUT>/main.bundle', { kind: 'ref', ref: 'main' }], {
    setup: [{ tmpdir: 'OUT' }],
    mask: ['bytes']
  }),
  sh('bundleCreate', 'tags-and-releases', 'range', ['<REPO>', '<OUT>/range.bundle', { kind: 'range', from: 'HEAD~2', to: 'main' }], {
    setup: [{ tmpdir: 'OUT' }],
    mask: ['bytes']
  }),
  sh('bundleCreate', 'tags-and-releases', 'empty-range', ['<REPO>', '<OUT>/empty.bundle', { kind: 'range', from: 'main', to: 'main' }], {
    setup: [{ tmpdir: 'OUT' }],
    capture: [{ exists: '<OUT>/empty.bundle' }]
  }),
  sh('bundleInspect', 'tags-and-releases', 'all', ['<REPO>', '<OUT>/all.bundle'], {
    setup: [{ tmpdir: 'OUT' }, { git: ['bundle', 'create', '-q', '<OUT>/all.bundle', '--all'] }]
  }),
  sh('bundleInspect', 'bisect-bug', 'stranger', ['<REPO>', '<OUT>/range.bundle'], {
    setup: [
      { tmpdir: 'OUT' },
      { git: ['bundle', 'create', '-q', '<OUT>/range.bundle', 'HEAD~2..main'], dir: '<PLAYGROUND>/tags-and-releases' }
    ]
  }),
  sh('bundleInspect', 'bisect-bug', 'missing-file', ['<REPO>', '<TMP>/no-such.bundle']),
  cl('bundleFetch', 'tags-and-releases', 'main', ['<REPO>', '<OUT>/all.bundle', ['refs/heads/main', 'refs/tags/v1.0.0', '']], {
    setup: [
      { tmpdir: 'OUT' },
      { git: ['bundle', 'create', '-q', '<OUT>/all.bundle', '--all'] },
      { git: ['reset', '-q', '--hard', 'HEAD~2'] }
    ]
  }),
  cl('bundleFetch', 'tags-and-releases', 'none', ['<REPO>', '<TMP>/x.bundle', ['']]),
  cl('bundleFetch', 'tags-and-releases', 'missing-file', ['<REPO>', '<TMP>/x.bundle', ['refs/heads/main']]),
  sh('archiveCreate', 'attributes', 'tar-prefix', ['<REPO>', '<OUT>/src.tar', 'HEAD', 'tar', ' demo-1.0// ', ''], {
    setup: [{ tmpdir: 'OUT' }],
    mask: ['bytes'],
    capture: [{ tar: '<OUT>/src.tar' }]
  }),
  sh('archiveCreate', 'deep-history-monorepo', 'subdir', ['<REPO>', '<OUT>/part.tar', ' ', 'tar', '', ' packages/ '], {
    setup: [{ tmpdir: 'OUT' }],
    mask: ['bytes'],
    capture: [{ tar: '<OUT>/part.tar' }]
  }),
  sh('archiveCreate', 'bisect-bug', 'targz', ['<REPO>', '<OUT>/a.tar.gz', 'main', 'tar.gz', 'p', ''], {
    setup: [{ tmpdir: 'OUT' }],
    mask: ['bytes'],
    capture: [{ tar: '<OUT>/a.tar.gz' }]
  }),
  sh('archiveCreate', 'bisect-bug', 'zip', ['<REPO>', '<OUT>/a.zip', 'main', 'zip', '', ''], {
    setup: [{ tmpdir: 'OUT' }],
    mask: ['bytes'],
    capture: [{ exists: '<OUT>/a.zip' }]
  }),
  sh('archiveCreate', 'bisect-bug', 'bad-ref', ['<REPO>', '<OUT>/a.tar', 'no-such-ref', 'tar', '', ''], {
    setup: [{ tmpdir: 'OUT' }]
  }),

  // ── clean, discard, ignore, untrack ─────────────────────────────────────
  sh('cleanPreview', 'untracked-mess', 'fixture', ['<REPO>'], { mask: ['bytes'] }),
  sh('cleanPreview', 'bisect-bug', 'nothing', ['<REPO>']),
  cl('clean', 'untracked-mess', 'chosen', ['<REPO>', ['notes.md', 'tmp/', ''], false], {
    mask: ['bytes'],
    capture: [{ exists: 'notes.md' }, { exists: 'tmp' }, { exists: '.env' }, { exists: 'dist' }]
  }),
  cl('clean', 'untracked-mess', 'ignored', ['<REPO>', ['dist/'], false], {
    mask: ['bytes'],
    capture: [{ exists: 'dist' }, { exists: 'node_modules' }]
  }),
  cl('clean', 'untracked-mess', 'tracked', ['<REPO>', ['src/app.js'], true]),
  cl('clean', 'untracked-mess', 'escape', ['<REPO>', ['../escape.txt'], true]),
  cl('clean', 'untracked-mess', 'nothing', ['<REPO>', [''], false]),
  cl('clean', 'untracked-mess', 'nested', ['<REPO>', ['experiment/', 'notes.md'], false], {
    capture: [{ exists: 'experiment' }, { exists: 'notes.md' }]
  }),
  cl('clean', 'untracked-mess', 'trash', ['<REPO>', ['experiment/', 'notes.md'], true], {
    mask: ['bytes'],
    capture: [{ exists: 'experiment' }, { exists: 'notes.md' }]
  }),
  cl('discard', 'snapshots', 'modified', ['<REPO>', ['draft.md'], false], { capture: [{ read: 'draft.md' }] }),
  cl('discard', 'snapshots', 'untracked', ['<REPO>', ['scratch.txt'], true], { capture: [{ exists: 'scratch.txt' }] }),
  cl('discard', 'snapshots', 'staged-new', ['<REPO>', ['notes.md'], false], { capture: [{ exists: 'notes.md' }] }),
  cl('discard', 'snapshots', 'staged-delete', ['<REPO>', ['draft.md', 'nope.md'], false], {
    setup: [{ git: ['rm', '-q', '-f', 'draft.md'] }],
    capture: [{ read: 'draft.md' }]
  }),
  cl('discard', 'bisect-bug', 'clean-tree', ['<REPO>', ['math.js'], false]),
  cl('addToGitignore', 'bisect-bug', 'new', ['<REPO>', [' /debug.log ', '', 'build/', '/debug.log']], {
    setup: [{ write: 'debug.log', content: 'noise\n' }],
    capture: [{ read: '.gitignore' }]
  }),
  cl('addToGitignore', 'untracked-mess', 'existing', ['<REPO>', ['dist/', '*.tmp']], {
    capture: [{ read: '.gitignore' }]
  }),
  cl('addToGitignore', 'untracked-mess', 'all-present', ['<REPO>', ['dist/', '.env']]),
  cl('addToGitignoreAt', 'bisect-bug', 'subdir', ['<REPO>', 'deep/er', ['*.cache']], {
    capture: [{ read: 'deep/er/.gitignore' }]
  }),
  cl('addToGitignoreAt', 'bisect-bug', 'no-trailing-newline', ['<REPO>', '', ['b', 'c']], {
    setup: [{ write: '.gitignore', content: 'a\r\nb' }],
    capture: [{ read: '.gitignore' }]
  }),
  cl('untrack', 'bisect-bug', 'cached', ['<REPO>', ['math.js']], { capture: [{ exists: 'math.js' }] }),
  cl('untrack', 'bisect-bug', 'from-disk', ['<REPO>', ['tax.js', 'not-tracked.txt'], true], {
    capture: [{ exists: 'tax.js' }]
  }),
  cl('untrack', 'bisect-bug', 'none', ['<REPO>', []]),

  // ── .gitcito.json ───────────────────────────────────────────────────────
  sh('repoConfig', 'project-tree', 'absent', ['<REPO>']),
  cl('repoConfig', 'project-tree', 'mixed', ['<REPO>'], {
    state: false,
    setup: [
      {
        write: '.gitcito.json',
        content: JSON.stringify({
          version: 1,
          zeta: 1,
          '2': true,
          alpha: 2,
          protect: ['main', '  ', 3, 'release/*', 'x'.repeat(201)],
          links: { tickets: [{ match: '[A-Z]+-\\d+', url: 'https://t.example/$0', label: 'Tracker' }, { match: '(', url: 'https://x' }, { match: 'a', url: 'javascript:alert(1)' }, 'nope', { match: 'b' }] },
          commit: { scopes: ['ui', 'api'], trailers: 'nope', ticketFromBranch: false },
          requires: { node: ' 20 ', submodules: 'yes', lfs: true, hooksPath: '../elsewhere', files: ['.env', { path: '/etc/passwd' }, { path: 'a', from: '../b' }, { path: '.env.local', from: '.env.example', why: 'dev server' }, 7] },
          checklist: { push: ['Run the tests', ''] }
        })
      }
    ]
  }),
  cl('repoConfig', 'project-tree', 'bad-json', ['<REPO>'], { state: false, setup: [{ write: '.gitcito.json', content: '{ nope' }] }),
  cl('repoConfig', 'project-tree', 'future-version', ['<REPO>'], { state: false, setup: [{ write: '.gitcito.json', content: '{"version":2}' }] }),
  cl('repoConfig', 'project-tree', 'not-object', ['<REPO>'], { state: false, setup: [{ write: '.gitcito.json', content: '[1]' }] }),
  cl('setRepoConfig', 'project-tree', 'write', ['<REPO>', { version: 1, protect: ['main', ' '], commit: { scopes: ['ui'], ticketFromBranch: false }, requires: { node: '20', files: [{ path: '.env', from: '.env.example' }] }, links: { tickets: [{ match: '#(\\d+)', url: 'https://x.example/$1' }] }, checklist: { push: ['<b>&</b>'] } }], {
    capture: [{ read: '.gitcito.json' }]
  }),
  cl('setRepoConfig', 'project-tree', 'bad-version', ['<REPO>', { version: 7 }], { capture: [{ exists: '.gitcito.json' }] }),
  sh('repoDoctor', 'project-tree', 'no-config', ['<REPO>']),
  cl('repoDoctor', 'project-tree', 'checks', ['<REPO>'], {
    state: false,
    setup: [
      { write: '.env.example', content: 'A=1\n' },
      { mkdir: '.husky/_' },
      { git: ['config', 'core.hooksPath', '<REPO>/.husky/_'] },
      {
        write: '.gitcito.json',
        content: JSON.stringify({
          version: 1,
          requires: {
            submodules: true,
            lfs: true,
            hooksPath: '.husky/_',
            files: [{ path: '.env.local', from: '.env.example', why: 'the dev server reads it' }, { path: '.env' }, { path: 'nofile', from: 'nofrom' }]
          }
        })
      }
    ]
  }),
  cl('repoDoctor', 'project-tree', 'hooks-wrong', ['<REPO>'], {
    state: false,
    setup: [
      { git: ['config', 'core.hooksPath', '/opt/elsewhere'] },
      { write: '.gitcito.json', content: JSON.stringify({ version: 1, requires: { hooksPath: '.husky' } }) }
    ]
  }),
  cl('repoDoctor', 'project-tree', 'hooks-unset', ['<REPO>'], {
    state: false,
    setup: [{ write: '.gitcito.json', content: JSON.stringify({ version: 1, requires: { hooksPath: '.husky' } }) }]
  }),
  cl('repoDoctor', 'project-tree', 'node', ['<REPO>'], {
    state: false,
    setup: [{ write: '.gitcito.json', content: JSON.stringify({ version: 1, requires: { node: '>=9999', files: ['.env'] } }) }],
    mask: ['actual']
  }),
  cl('repoDoctor', 'lfs-assets', 'lfs-ok', ['<REPO>'], {
    state: false,
    setup: [{ write: '.gitcito.json', content: JSON.stringify({ version: 1, requires: { lfs: true } }) }]
  }),
  cl('repoDoctor', 'lfs-assets', 'lfs-pointer', ['<REPO>'], {
    state: false,
    setup: [
      { write: 'asset.bin', content: 'version https://git-lfs.github.com/spec/v1\noid sha256:00\nsize 1\n' },
      { write: '.gitcito.json', content: JSON.stringify({ version: 1, requires: { lfs: true } }) }
    ]
  }),
  cl('applyDoctorFix', 'project-tree', 'copy', ['<REPO>', { kind: 'copyFile', from: '.env.example', to: '.env.local' }], {
    setup: [{ write: '.env.example', content: 'API_URL=http://localhost\n' }],
    capture: [{ read: '.env.local' }]
  }),
  cl('applyDoctorFix', 'project-tree', 'copy-exists', ['<REPO>', { kind: 'copyFile', from: '.env.example', to: '.env' }], {
    setup: [{ write: '.env.example', content: 'A=1\n' }],
    capture: [{ read: '.env' }]
  }),
  cl('applyDoctorFix', 'project-tree', 'copy-escape', ['<REPO>', { kind: 'copyFile', from: '../../etc/hosts', to: 'stolen' }], {
    capture: [{ exists: 'stolen' }]
  }),
  cl('applyDoctorFix', 'project-tree', 'copy-missing-source', ['<REPO>', { kind: 'copyFile', from: 'nope', to: 'there' }]),
  cl('applyDoctorFix', 'project-tree', 'hooks', ['<REPO>', { kind: 'hooksPath', value: '.husky' }], {
    capture: [{ git: ['config', '--get', 'core.hooksPath'] }]
  }),
  cl('applyDoctorFix', 'project-tree', 'hooks-escape', ['<REPO>', { kind: 'hooksPath', value: '../../hooks' }]),
  cl('applyDoctorFix', 'project-tree', 'submodules', ['<REPO>', { kind: 'submodules' }]),
  sh('suggestRepoConfig', 'project-tree', 'fixture', ['<REPO>']),
  cl('suggestRepoConfig', 'project-tree', 'inferred', ['<REPO>'], {
    state: false,
    setup: [
      { write: '.nvmrc', content: 'v20.11.1\n' },
      { write: '.env.example', content: 'A=1\n' },
      { rm: '.env' },
      { mkdir: '.husky' },
      { write: '.gitattributes', content: '*.bin filter=lfs\n' },
      { write: '.gitmodules', content: '' },
      { git: ['config', 'gitcito.protectedbranches', 'main, release/3.x,,develop'] },
      { git: ['branch', 'develop'] },
      { git: ['commit', '-q', '--allow-empty', '-m', 'feat(ui): a'] },
      { git: ['commit', '-q', '--allow-empty', '-m', 'fix(ui)!: b'] },
      { git: ['commit', '-q', '--allow-empty', '-m', 'chore(build): c'] },
      { git: ['commit', '-q', '--allow-empty', '-m', 'docs(build): d'] },
      { git: ['commit', '-q', '--allow-empty', '-m', 'feat(once): e'] },
      ...[1, 2, 3, 4, 5].map((i) => ({ git: ['commit', '-q', '--allow-empty', '-m', `GC-${i}: work`] }) as Op)
    ]
  }),
  cl('suggestRepoConfig', 'project-tree', 'husky-abs', ['<REPO>'], {
    state: false,
    setup: [
      { write: 'package.json', content: '{"engines":{"node":">=18"}}' },
      { mkdir: '.husky/_' },
      { git: ['config', 'core.hooksPath', '<REPO>/.husky/_'] }
    ]
  }),
  cl('suggestRepoConfig', 'project-tree', 'foreign-hooks', ['<REPO>'], {
    state: false,
    setup: [{ mkdir: '.husky' }, { git: ['config', 'core.hooksPath', '/opt/somebody-elses/hooks'] }]
  }),

  // ── subtrees ────────────────────────────────────────────────────────────
  sh('subtrees', 'subtree', 'fixture', ['<REPO>']),
  cl('subtrees', 'subtree', 'remembered', ['<REPO>'], {
    state: false,
    setup: [
      { git: ['config', 'gitcito.subtree.vendor%2Fparser.url', 'https://example.com/parser.git'] },
      { git: ['config', 'gitcito.subtree.vendor%2Fparser.ref', 'main'] },
      { git: ['config', 'gitcito.subtree.libs%2Fgone.url', 'x'] },
      { git: ['config', 'gitcito.subtree.Alpha.ref', 'v1'] }
    ]
  }),
  cl('subtreeAdd', 'subtree', 'add', ['<REPO>', '/vendor/second/', ' <LIB> ', ' main '], {
    setup: [{ copy: 'subtree-lib.git', as: 'LIB' }],
    capture: [{ git: ['config', '--get-regexp', '^gitcito\\.'] }, { exists: 'vendor/second/parser.js' }]
  }),
  cl('subtreeAdd', 'subtree', 'no-squash', ['<REPO>', 'vendor/third', '<LIB>', 'main', false], {
    setup: [{ copy: 'subtree-lib.git', as: 'LIB' }]
  }),
  cl('subtreeAdd', 'subtree', 'exists', ['<REPO>', 'vendor/parser', '<LIB>', 'main'], {
    setup: [{ copy: 'subtree-lib.git', as: 'LIB' }]
  }),
  cl('subtreeAdd', 'subtree', 'missing-prefix', ['<REPO>', ' / ', 'x', 'main']),
  cl('subtreeAdd', 'subtree', 'missing-ref', ['<REPO>', 'vendor/x', 'x', ' ']),
  cl('subtreeAdd', 'subtree', 'bad-repo', ['<REPO>', 'vendor/x', '/nowhere/at/all.git', 'main']),
  cl('subtreePull', 'subtree', 'pull', ['<REPO>', 'vendor/parser', '<LIB>', 'main'], {
    setup: [
      { copy: 'subtree-lib.git', as: 'LIB' },
      { tmpdir: 'W' },
      { git: ['clone', '-q', '<LIB>', '<W>/w'], dir: '<W>' },
      { write: '<W>/w/parser.js', content: 'exports.parse = () => 42\n' },
      { git: ['commit', '-q', '-am', 'feat: upstream change'], dir: '<W>/w' },
      { git: ['push', '-q', 'origin', 'main'], dir: '<W>/w' }
    ],
    capture: [{ read: 'vendor/parser/parser.js' }, { git: ['config', '--get-regexp', '^gitcito\\.'] }]
  }),
  cl('subtreePull', 'subtree', 'no-url', ['<REPO>', 'vendor/parser', '', 'main']),
  cl('subtreePush', 'subtree', 'push', ['<REPO>', 'vendor/parser', '<LIB>', 'from-app'], {
    setup: [
      { copy: 'subtree-lib.git', as: 'LIB' },
      { write: 'vendor/parser/EXTRA.md', content: 'local change\n' },
      addAll(),
      commit('feat: local change to the vendored lib')
    ],
    capture: [{ git: ['for-each-ref', '--format=%(refname) %(objectname)'], dir: '<LIB>' }]
  }),
  cl('subtreePush', 'subtree', 'no-ref', ['<REPO>', 'vendor/parser', 'x', '']),
  cl('subtreeSplit', 'subtree', 'split', ['<REPO>', 'vendor/parser', ' parser-only '], {
    capture: [{ git: ['ls-tree', '--name-only', 'parser-only'] }]
  }),
  cl('subtreeSplit', 'subtree', 'no-name', ['<REPO>', 'vendor/parser', ' ']),
  cl('subtreeForget', 'subtree', 'forget', ['<REPO>', 'vendor/parser'], {
    setup: [
      { git: ['config', 'gitcito.subtree.vendor%2Fparser.url', 'x'] },
      { git: ['config', 'gitcito.subtree.vendor%2Fparser.ref', 'main'] }
    ],
    capture: [{ git: ['config', '--get-regexp', '^gitcito\\.'] }]
  }),
  cl('subtreeForget', 'subtree', 'unknown', ['<REPO>', 'nothing/here']),

  // ── hooks ───────────────────────────────────────────────────────────────
  sh('hooksInfo', 'hooks', 'fixture', ['<REPO>']),
  sh('hooksInfo', 'bisect-bug', 'samples', ['<REPO>']),
  cl('hooksInfo', 'bisect-bug', 'custom-path', ['<REPO>'], {
    state: false,
    setup: [
      { git: ['config', 'core.hooksPath', '.githooks'] },
      { write: '.githooks/pre-push', content: '#!/bin/sh\nexit 0\n' },
      { chmod: '.githooks/pre-push', mode: 0o755 },
      { write: '.pre-commit-config.yml', content: 'repos: []\n' }
    ]
  }),
  sh('readHook', 'hooks', 'real', ['<REPO>', 'pre-commit']),
  sh('readHook', 'bisect-bug', 'sample', ['<REPO>', 'pre-push']),
  sh('readHook', 'bisect-bug', 'nothing', ['<REPO>', 'post-rewrite']),
  cl('writeHook', 'bisect-bug', 'new', ['<REPO>', 'pre-commit', '#!/bin/sh\necho hi\n'], {
    capture: [{ read: '.git/hooks/pre-commit' }, { mode: '.git/hooks/pre-commit' }]
  }),
  cl('writeHook', 'bisect-bug', 'custom-dir', ['<REPO>', 'commit-msg', '#!/bin/sh\n'], {
    setup: [{ git: ['config', 'core.hooksPath', 'tools/hooks'] }],
    capture: [{ read: 'tools/hooks/commit-msg' }, { mode: 'tools/hooks/commit-msg' }]
  }),
  cl('setHookEnabled', 'hooks', 'enable', ['<REPO>', 'commit-msg', true], { capture: [{ mode: '.git/hooks/commit-msg' }] }),
  cl('setHookEnabled', 'hooks', 'disable', ['<REPO>', 'pre-commit', false], { capture: [{ mode: '.git/hooks/pre-commit' }] }),
  cl('setHookEnabled', 'bisect-bug', 'missing', ['<REPO>', 'pre-rebase', true]),
  cl('deleteHook', 'hooks', 'delete', ['<REPO>', 'pre-commit'], {
    capture: [{ exists: '.git/hooks/pre-commit' }, { exists: '.git/hooks/pre-commit.sample' }]
  }),
  cl('deleteHook', 'bisect-bug', 'missing', ['<REPO>', 'pre-rebase']),

  // ── LFS ─────────────────────────────────────────────────────────────────
  // The fixture's own .gitattributes lists its two patterns in whatever order
  // `git lfs track` happened to write them; pin the file.
  cl('lfsInfo', 'lfs-assets', 'fixture', ['<REPO>'], {
    state: false,
    setup: [
      {
        write: '.gitattributes',
        content: '# assets\n*.bin filter=lfs diff=lfs merge=lfs -text\n\n*.psd   filter=lfs diff=lfs merge=lfs -text\n*.txt text\n'
      }
    ]
  }),
  sh('lfsInfo', 'bisect-bug', 'none', ['<REPO>']),
  cl('lfsTrack', 'bisect-bug', 'track', ['<REPO>', '*.psd'], { capture: [{ read: '.gitattributes' }] }),
  cl('lfsUntrack', 'lfs-assets', 'untrack', ['<REPO>', '*.psd'], { capture: [{ read: '.gitattributes' }] }),
  cl('lfsPull', 'lfs-assets', 'no-remote', ['<REPO>']),
  cl('lfsPrune', 'lfs-assets', 'prune', ['<REPO>']),

  // ── worktrees ───────────────────────────────────────────────────────────
  sh('worktrees', 'submodules-worktrees', 'linked', ['<REPO>']),
  sh('worktrees', 'bisect-bug', 'single', ['<REPO>']),
  { method: 'worktrees', fixture: '', name: 'not-a-repo', repo: 'tmp', args: ['<REPO>'] },
  cl('worktreeAdd', 'bisect-bug', 'new-branch', ['<REPO>', '<W>/wt', 'feature/wt', true, 'HEAD~1'], {
    setup: [{ tmpdir: 'W' }],
    capture: [{ git: ['worktree', 'list', '--porcelain'] }, { git: ['branch', '--show-current'], dir: '<W>/wt' }]
  }),
  cl('worktreeAdd', 'force-push', 'existing-branch', ['<REPO>', '<W>/wt', 'feature/login', false, 'ignored'], {
    setup: [{ tmpdir: 'W' }],
    capture: [{ git: ['branch', '--show-current'], dir: '<W>/wt' }]
  }),
  cl('worktreeAdd', 'force-push', 'checked-out', ['<REPO>', '<W>/wt', 'main', false], { setup: [{ tmpdir: 'W' }] }),
  cl('worktreeRemove', 'bisect-bug', 'remove', ['<REPO>', '<W>/wt', false], {
    setup: [{ tmpdir: 'W' }, { git: ['worktree', 'add', '-q', '-b', 'wt', '<W>/wt'] }],
    capture: [{ exists: '<W>/wt' }]
  }),
  cl('worktreeRemove', 'bisect-bug', 'dirty', ['<REPO>', '<W>/wt', false], {
    setup: [{ tmpdir: 'W' }, { git: ['worktree', 'add', '-q', '-b', 'wt', '<W>/wt'] }, { write: '<W>/wt/dirt.txt', content: 'x' }],
    capture: [{ exists: '<W>/wt' }]
  }),
  cl('worktreeRemove', 'bisect-bug', 'dirty-force', ['<REPO>', '<W>/wt', true], {
    setup: [{ tmpdir: 'W' }, { git: ['worktree', 'add', '-q', '-b', 'wt', '<W>/wt'] }, { write: '<W>/wt/dirt.txt', content: 'x' }],
    capture: [{ exists: '<W>/wt' }]
  }),

  // ── submodules ──────────────────────────────────────────────────────────
  sh('submodules', 'submodule-states', 'states', ['<REPO>']),
  sh('submodules', 'submodules-worktrees', 'two', ['<REPO>']),
  sh('submodules', 'bisect-bug', 'none', ['<REPO>']),
  cl('submoduleAdd', 'bisect-bug', 'add', ['<REPO>', '<SUB>', 'libs/sub', 'main'], {
    setup: [{ git: ['config', '--global', 'protocol.file.allow', 'always'] }, { copy: 'cherry-pick', as: 'SUB' }],
    capture: [{ read: '.gitmodules' }]
  }),
  cl('submoduleAdd', 'bisect-bug', 'file-protocol-refused', ['<REPO>', '<SUB>', 'libs/sub'], {
    setup: [{ copy: 'cherry-pick', as: 'SUB' }]
  }),
  cl('submoduleSetUrl', 'bisect-bug', 'set', ['<REPO>', 'libs/sub', 'https://example.com/sub.git'], {
    setup: SUBMODULE_SETUP,
    capture: [{ read: '.gitmodules' }, { git: ['config', '--get', 'submodule.libs/sub.url'] }]
  }),
  cl('submoduleUpdate', 'bisect-bug', 'all', ['<REPO>'], {
    setup: [...SUBMODULE_SETUP, { git: ['submodule', 'deinit', '-q', '--force', '--', 'libs/sub'] }],
    capture: [{ list: 'libs/sub' }]
  }),
  cl('submoduleUpdate', 'bisect-bug', 'path-no-init', ['<REPO>', 'libs/sub', false], {
    setup: [...SUBMODULE_SETUP, { git: ['submodule', 'deinit', '-q', '--force', '--', 'libs/sub'] }],
    capture: [{ list: 'libs/sub' }]
  }),
  cl('submoduleSync', 'bisect-bug', 'all', ['<REPO>'], {
    setup: [...SUBMODULE_SETUP, { git: ['config', '--file', '.gitmodules', 'submodule.libs/sub.url', '../elsewhere.git'] }],
    capture: [{ git: ['config', '--get', 'submodule.libs/sub.url'] }]
  }),
  cl('submoduleSync', 'bisect-bug', 'path', ['<REPO>', 'libs/sub'], { setup: SUBMODULE_SETUP }),
  cl('submoduleDeinit', 'bisect-bug', 'deinit', ['<REPO>', 'libs/sub'], { setup: SUBMODULE_SETUP, capture: [{ list: 'libs/sub' }] }),
  cl('submoduleDeinit', 'bisect-bug', 'dirty', ['<REPO>', 'libs/sub'], {
    setup: [...SUBMODULE_SETUP, { write: 'libs/sub/dirt.txt', content: 'x' }]
  }),
  cl('submoduleDeinit', 'bisect-bug', 'dirty-force', ['<REPO>', 'libs/sub', true], {
    setup: [...SUBMODULE_SETUP, { write: 'libs/sub/dirt.txt', content: 'x' }]
  }),
  cl('submoduleRemove', 'bisect-bug', 'remove', ['<REPO>', 'libs/sub'], {
    setup: SUBMODULE_SETUP,
    capture: [{ read: '.gitmodules' }, { exists: 'libs/sub' }]
  }),
  cl('submoduleRemove', 'bisect-bug', 'unknown', ['<REPO>', 'libs/none']),

  // ── user identity ───────────────────────────────────────────────────────
  sh('getUser', 'bisect-bug', 'local', ['<REPO>']),
  cl('getUser', 'bisect-bug', 'global-fallback', ['<REPO>'], {
    state: false,
    setup: [{ git: ['config', '--local', '--unset', 'user.name'] }, { git: ['config', '--local', '--unset', 'user.email'] }]
  }),
  cl('setUser', 'bisect-bug', 'set', ['<REPO>', 'Ada Lovelace', 'ada@example.com'], {
    capture: [{ git: ['config', '--local', '--get-regexp', '^user\\.'] }]
  })
]

// ─── Running a case ─────────────────────────────────────────────────────────

let BASE = ''
const saved: Record<string, string | undefined> = {}

const ENV: Record<string, string> = {
  GIT_CONFIG_NOSYSTEM: '1',
  GIT_TERMINAL_PROMPT: '0',
  GIT_AUTHOR_NAME: 'Golden Author',
  GIT_AUTHOR_EMAIL: 'author@gitcito.invalid',
  GIT_AUTHOR_DATE: '2001-02-03T04:05:06Z',
  GIT_COMMITTER_NAME: 'Golden Committer',
  GIT_COMMITTER_EMAIL: 'committer@gitcito.invalid',
  GIT_COMMITTER_DATE: '2001-02-03T04:05:06Z'
}

beforeAll(() => {
  BASE = mkdtempSync(join(tmpdir(), 'gitcito-goldens-d-'))
  const home = join(BASE, 'home')
  mkdirSync(home, { recursive: true })
  mkdirSync(join(BASE, 'xdg'), { recursive: true })
  mkdirSync(join(BASE, 'xdg-data'), { recursive: true })
  const env = {
    ...ENV,
    TMPDIR: BASE,
    HOME: home,
    XDG_CONFIG_HOME: join(BASE, 'xdg'),
    XDG_DATA_HOME: join(BASE, 'xdg-data'),
    GIT_CONFIG_GLOBAL: join(home, '.gitconfig')
  }
  for (const [k, v] of Object.entries(env)) {
    saved[k] = process.env[k]
    process.env[k] = v
  }
})

afterAll(() => {
  cleanupFixtures()
  for (const [k, v] of Object.entries(saved)) {
    if (v === undefined) delete process.env[k]
    else process.env[k] = v
  }
  rmSync(BASE, { recursive: true, force: true })
})

/** The isolated global config: the same file internal/testutil.IsolateGitEnv writes. */
function resetGlobalConfig(): void {
  writeFileSync(
    process.env.GIT_CONFIG_GLOBAL as string,
    '[user]\n\tname = Gitcito Test\n\temail = test@gitcito.invalid\n' +
      '[init]\n\tdefaultBranch = main\n' +
      '[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n'
  )
  rmSync(join(process.env.HOME as string, '.git-credentials'), { force: true })
}

const git = (dir: string, args: string[]): string =>
  execFileSync('git', ['-C', dir, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })

type Places = Record<string, string>

function expandString(s: string, ph: Places): string {
  let out = s.replace(/<(REV|OBJ)(?:@(\w+))?:([^>]+)>/g, (_m, kind: string, where: string | undefined, spec: string) => {
    const dir = where ? ph[where] : ph.REPO
    const sha = git(dir, ['rev-parse', spec]).trim()
    if (kind === 'REV') return sha
    return join(dir, '.git', 'objects', sha.slice(0, 2), sha.slice(2))
  })
  for (const [name, path] of Object.entries(ph)) out = out.split(`<${name}>`).join(path)
  return out
}

function expand(v: unknown, ph: Places): unknown {
  if (typeof v === 'string') return expandString(v, ph)
  if (Array.isArray(v)) return v.map((x) => expand(x, ph))
  if (v && typeof v === 'object') {
    return Object.fromEntries(Object.entries(v as Record<string, unknown>).map(([k, x]) => [k, expand(x, ph)]))
  }
  return v
}

const pathOf = (p: string, ph: Places): string => {
  const e = expandString(p, ph)
  return isAbsolute(e) ? e : join(ph.REPO, e)
}

function applyOp(op: Op, ph: Places): void {
  if ('git' in op) git(pathOf(op.dir ?? '<REPO>', ph), expand(op.git, ph) as string[])
  else if ('write' in op) {
    const p = pathOf(op.write, ph)
    mkdirSync(dirname(p), { recursive: true })
    writeFileSync(p, op.content)
  } else if ('mkdir' in op) mkdirSync(pathOf(op.mkdir, ph), { recursive: true })
  else if ('rm' in op) rmSync(pathOf(op.rm, ph), { recursive: true, force: true })
  else if ('mtime' in op) utimesSync(pathOf(op.mtime, ph), op.ms / 1000, op.ms / 1000)
  else if ('chmod' in op) chmodSync(pathOf(op.chmod, ph), op.mode)
  else if ('copy' in op) ph[op.as] = cloneFixture(op.copy)
  else if ('tmpdir' in op) ph[op.tmpdir] = mkdtempSync(join(BASE, `gitcito-${op.tmpdir.toLowerCase()}-`))
}

function capture(c: Capture, ph: Places): unknown {
  if ('git' in c) {
    try {
      return { out: git(pathOf(c.dir ?? '<REPO>', ph), expand(c.git, ph) as string[]).trim() }
    } catch {
      return { failed: true }
    }
  }
  if ('read' in c) {
    const p = pathOf(c.read, ph)
    return existsSync(p) ? readFileSync(p, 'utf8') : null
  }
  if ('exists' in c) return existsSync(pathOf(c.exists, ph))
  if ('mode' in c) {
    const p = pathOf(c.mode, ph)
    return existsSync(p) ? statSync(p).mode & 0o777 : null
  }
  if ('tar' in c) {
    return execFileSync('tar', ['-tf', pathOf(c.tar, ph)], { encoding: 'utf8' })
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
  }
  const dir = pathOf(c.list, ph)
  return existsSync(dir) ? readdirSync(dir).sort() : null
}

const lines = (s: string): string[] => s.split('\n').filter((l) => l !== '')

/**
 * `log --all` breaks ties between commits by commit date, and the playground's
 * dates are wall-clock seconds — so the same graph comes out in a different
 * order after every regeneration. Order it by what does not move instead:
 * subject, depth from the root, then the parents' subjects.
 */
function canonicalLog(entries: string[]): string[] {
  const rows = entries.map((l) => {
    const [sha, parents, subject] = l.split('\0')
    // A subject can quote a sha (`Merge commit '<sha>'`): sort without it.
    return { line: l, sha, parents: parents ? parents.split(' ') : [], subject: (subject ?? '').replace(HEX, '#') }
  })
  const bySha = new Map(rows.map((r) => [r.sha, r]))
  const depth = new Map<string, number>()
  const depthOf = (sha: string): number => {
    const known = depth.get(sha)
    if (known !== undefined) return known
    // Iterative post-order: deep histories overflow a recursive walk.
    const stack = [sha]
    while (stack.length) {
      const top = stack[stack.length - 1]
      const row = bySha.get(top)
      const pending = (row?.parents ?? []).filter((p) => bySha.has(p) && !depth.has(p))
      if (pending.length) {
        stack.push(...pending)
        continue
      }
      stack.pop()
      if (depth.has(top)) continue
      const ps = (row?.parents ?? []).filter((p) => bySha.has(p))
      depth.set(top, ps.length ? 1 + Math.max(...ps.map((p) => depth.get(p) as number)) : 0)
    }
    return depth.get(sha) as number
  }
  const key = (r: (typeof rows)[number]): string =>
    [
      r.subject,
      String(depthOf(r.sha)).padStart(8, '0'),
      String(r.parents.length),
      r.parents.map((p) => bySha.get(p)?.subject ?? '').join('\u0001')
    ].join('\u0002')
  return rows
    .map((r) => ({ r, k: key(r) }))
    .sort((a, b) => (a.k < b.k ? -1 : a.k > b.k ? 1 : a.r.line < b.r.line ? -1 : a.r.line > b.r.line ? 1 : 0))
    .map(({ r }) => r.line)
}

/** The repository state a write leaves behind. WIP snapshot refs carry a
 *  wall-clock name and message, so they are reported apart, by their tree. */
function snapshot(dir: string): unknown {
  const tryGit = (args: string[]): string => {
    try {
      return git(dir, args)
    } catch {
      return ''
    }
  }
  const snapshots = lines(tryGit(['for-each-ref', '--format=%(refname)%00%(objectname)', 'refs/gitcito'])).map((l) => {
    const [ref, sha] = l.split('\0')
    return {
      ref: ref.replace(/\d{10,}/, '<TS>'),
      tree: tryGit(['rev-parse', `${sha}^{tree}`]).trim(),
      parents: tryGit(['log', '-1', '--format=%P', sha]).trim(),
      subject: tryGit(['log', '-1', '--format=%s', sha]).trim().replace(/\d{4}-\d\d-\d\dT[\d:.]+Z/, '<TIME>')
    }
  })
  return {
    status: lines(tryGit(['status', '--porcelain=v2'])),
    refs: lines(tryGit(['for-each-ref', '--format=%(refname) %(objectname)'])).filter((l) => !l.startsWith('refs/gitcito/')),
    // The stored graph, not what replacements make it look like.
    log: canonicalLog(
      lines(
        tryGit([
          '--no-replace-objects',
          'log',
          '--format=%H%x00%P%x00%s',
          '--exclude=refs/gitcito/*',
          '--exclude=refs/replace/*',
          '--all'
        ])
      )
    ),
    stash: lines(tryGit(['stash', 'list'])),
    index: lines(tryGit(['ls-files', '-s'])),
    snapshots
  }
}

// ─── Normalization ──────────────────────────────────────────────────────────

function replacePaths(s: string, pairs: [string, string][]): string {
  let out = s
  for (const [path, label] of pairs) out = out.split(path).join(label)
  return out
}

function walk(v: unknown, fn: (s: string) => string): unknown {
  if (typeof v === 'string') return fn(v)
  if (Array.isArray(v)) return v.map((x) => walk(x, fn))
  if (v && typeof v === 'object') {
    const out: Record<string, unknown> = {}
    for (const k of Object.keys(v as object).sort()) out[k] = walk((v as Record<string, unknown>)[k], fn)
    return out
  }
  return v
}

function applyMask(v: unknown, keys: Set<string>): unknown {
  if (Array.isArray(v)) return v.map((x) => applyMask(x, keys))
  if (v && typeof v === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, x] of Object.entries(v as Record<string, unknown>)) {
      if (!keys.has(k)) out[k] = applyMask(x, keys)
      else if (typeof x === 'number') out[k] = x > 0 ? '<POS>' : x
      else if (x === null || x === '' ) out[k] = x
      else out[k] = '<MASKED>'
    }
    return out
  }
  return v
}

const HEX = /\b[0-9a-f]{7,64}\b/g

function renameShas(v: unknown): unknown {
  const labels = new Map<string, string>()
  walk(v, (s) => {
    for (const m of s.match(HEX) ?? []) {
      if ((m.length === 40 || m.length === 64) && !labels.has(m)) labels.set(m, `<SHA${labels.size + 1}>`)
    }
    return s
  })
  const full = [...labels.keys()]
  // An abbreviation of an object this output never names in full (a commit
  // in another repository, quoted in a message) still changes with the
  // playground: it gets a label of its own.
  const abbrevs = new Map<string, string>()
  return walk(v, (s) =>
    s.replace(HEX, (m) => {
      const exact = labels.get(m)
      if (exact) return exact
      const owners = full.filter((f) => f.startsWith(m))
      if (owners.length === 1) return `${labels.get(owners[0])}~`
      if (m.length > 12) return m
      if (!abbrevs.has(m)) abbrevs.set(m, `<ABBR${abbrevs.size + 1}>`)
      return abbrevs.get(m) as string
    })
  )
}

function normalize(raw: unknown, ph: Places, mask: string[] | undefined): unknown {
  const pairs: [string, string][] = []
  const add = (path: string, label: string): void => {
    if (!path) return
    pairs.push([path, label])
  }
  for (const [name, path] of Object.entries(ph)) add(path, `<${name}>`)
  pairs.sort((a, b) => b[0].length - a[0].length)
  const json = JSON.parse(JSON.stringify(raw)) as unknown
  const masked = mask?.length ? applyMask(json, new Set(mask)) : json
  return renameShas(walk(masked, (s) => replacePaths(s, pairs)))
}

async function runCase(c: Case): Promise<Record<string, unknown>> {
  resetGlobalConfig()
  const ph: Places = {}
  let repo: string
  if (c.repo === 'shared') repo = c.fixture ? join(PLAYGROUND, c.fixture) : BASE
  else if (c.repo === 'clone') repo = cloneFixture(c.fixture)
  else {
    repo = mkdtempSync(join(BASE, 'gitcito-tmp-'))
    if (c.repo === 'init') git(repo, ['init', '-q', '-b', 'main'])
  }
  ph.REPO = repo
  ph.HOME = process.env.HOME as string
  ph.PLAYGROUND = PLAYGROUND
  ph.ROOT = ROOT
  ph.TMP = BASE
  for (const op of c.setup ?? []) applyOp(op, ph)

  const args = expand(c.args, ph) as unknown[]
  const progress: unknown[] = []
  const call = (gitService as unknown as Record<string, (...a: unknown[]) => Promise<unknown>>)[c.method]
  let result: unknown
  let error: string | undefined
  try {
    // The dispatcher appends the progress callback to clone's own arguments.
    const full = c.method === 'clone' ? [...args, ...Array(Math.max(0, 4 - args.length)).fill(undefined), (p: unknown) => progress.push(p)] : args
    result = await call(...full)
  } catch (err) {
    // What the dispatcher hands the renderer.
    error = stripAnsi(redactCredentials(err instanceof Error ? err.message : String(err)))
  }

  const out: Record<string, unknown> = {}
  if (error !== undefined) out.error = error
  else out.result = result === undefined ? null : result
  if (c.state) {
    const dir = c.stateOf === 'result' ? (typeof result === 'string' ? result : '') : repo
    out.state = dir ? snapshot(dir) : null
  }
  if (c.capture) out.extra = c.capture.map((x) => capture(x, ph))
  const norm = normalize(out, ph, c.mask) as Record<string, unknown>

  return {
    method: c.method,
    fixture: c.fixture,
    case: c.name,
    repo: c.repo,
    ...(c.setup ? { setup: c.setup } : {}),
    args: c.args,
    ...(c.stateOf ? { stateOf: c.stateOf } : {}),
    ...(c.capture ? { capture: c.capture } : {}),
    ...(c.mask ? { mask: c.mask } : {}),
    ...norm
  }
}

const fileFor = (c: Case): string => join(GOLDENS, c.method, `${c.fixture || 'none'}-${c.name}.json`)

describe('slice D goldens', () => {
  it('has one golden per case and no duplicate names', () => {
    const files = cases.map(fileFor)
    expect(new Set(files).size).toBe(files.length)
  })

  for (const c of cases) {
    it(`${c.method} ${c.fixture || 'none'}-${c.name}`, async () => {
      const got = await runCase(c)
      const file = fileFor(c)
      if (WRITE) {
        mkdirSync(dirname(file), { recursive: true })
        writeFileSync(file, JSON.stringify(got, null, 2) + '\n')
      }
      expect(got).toEqual(JSON.parse(readFileSync(file, 'utf8')))
    })
  }
})
