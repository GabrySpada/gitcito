// Contract capture for the `gitcito` command line (BRIEF §4.6): stdout, stderr
// and exit code of every verb, produced by the Electron-era implementation —
// src/main/cliEntry.ts behind the resources/cli/gitcito shim, and
// src/main/textconvCli.ts behind resources/cli/gitcito-textconv — against
// playground fixtures and small generated repositories.
//
// The Go companion binary (cmd/gitcito-cli) replays the same file in
// cmd/gitcito-cli/goldens_test.go. The case specs live in the file too, so the
// two harnesses cannot disagree about what was run.
//
// PORT_WRITE_VECTORS=1 rewrites the files; otherwise this asserts the
// TypeScript still produces them. Also writes the pure-logic vectors for
// src/shared/commitCheck.ts and src/shared/cliRepos.ts.
import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { build } from 'esbuild'
import { spawnSync } from 'node:child_process'
import {
  chmodSync,
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  writeFileSync
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { checkCommitMessage, stripCommitComments } from '../../src/shared/commitCheck'
import { knownRepos, matchesRepo, resolveRepo } from '../../src/shared/cliRepos'
import type { RepoConfig } from '../../src/shared/types'
import { repoPath } from '../helpers'

const ROOT = resolve(__dirname, '../..')
const GOLDENS = resolve(ROOT, 'cmd/gitcito-cli/testdata/goldens.json')
const COMMIT_VECTORS = resolve(ROOT, 'testdata/vectors/commitCheck.json')
const REPOS_VECTORS = resolve(ROOT, 'testdata/vectors/cliRepos.json')
const WRITE = !!process.env.PORT_WRITE_VECTORS

// ─── Case specs ─────────────────────────────────────────────────────────────

type Op =
  | { write: [string, string] }
  | { git: string[]; mayFail?: boolean }
  | { mkdir: string }
  | { fixture: string }

interface Spec {
  name: string
  /** 'cli' runs `gitcito`, 'textconv' the textconv filter. */
  bin?: 'cli' | 'textconv'
  /** No repository unless set: 'init' makes one with a single commit. */
  repo?: 'init' | { fixture: string }
  ops?: Op[]
  settings?: unknown
  cwd?: string
  args: string[]
  /** Window verbs launch a fake app that records its argv (default true). */
  app?: boolean
  normalize?: ('ago' | 'sha' | 'version')[]
  check?: { files?: string[]; config?: string[]; sentinels?: boolean }
}

const CONFIG_FULL = JSON.stringify(
  {
    version: 1,
    protect: ['main', 'release/*'],
    commit: { scopes: ['api', 'ui'], trailers: ['Refs: {ticket}'] },
    checklist: { push: ['Run the tests', 'Update CHANGELOG.md'] }
  },
  null,
  2
)
const CONFIG_REQUIRES = JSON.stringify(
  {
    version: 1,
    requires: {
      hooksPath: '.husky',
      files: [{ path: '.env', from: '.env.example', why: 'Local settings the app reads at startup' }]
    }
  },
  null,
  2
)
const SETTINGS = {
  tabs: [
    { kind: 'repo', id: 't1', repos: [{ path: '<REPO>', name: 'API' }] },
    {
      kind: 'group',
      id: 't2',
      name: 'Work',
      repos: [
        { path: '<TMP>/web', name: 'Web' },
        { path: '<REPO>', name: 'API' }
      ]
    },
    { kind: 'page', id: 't3' }
  ],
  recentRepos: [{ path: '<TMP>/old-thing', name: 'Old' }, { path: '<TMP>/unnamed-repo' }]
}

// What the shim sends to the headless half; anything else launches the app.
const HEADLESS = new Set(['doctor', 'status', 'repos', 'commit-check', 'config', 'editor', 'completions', 'help'])
HEADLESS.add('--help').add('-h').add('--version').add('-V').add('--resolve')
const isWindowVerb = (spec: Spec): boolean => spec.bin !== 'textconv' && !HEADLESS.has(spec.args[0] ?? '')

const SPECS: Spec[] = [
  // Help and version
  { name: 'help', args: ['help'] },
  { name: 'help-long', args: ['--help'] },
  { name: 'help-short', args: ['-h'] },
  { name: 'help-verbs', args: ['help', 'verbs'] },
  { name: 'version', args: ['--version'], normalize: ['version'] },
  { name: 'version-short', args: ['-V'], normalize: ['version'] },
  { name: 'completions-bash', args: ['completions', 'bash'] },
  { name: 'completions-zsh', args: ['completions', 'zsh'] },
  { name: 'completions-fish', args: ['completions', 'fish'] },
  { name: 'completions-missing', args: ['completions'] },
  { name: 'completions-unknown', args: ['completions', 'powershell'] },

  // status
  { name: 'status-clean', repo: 'init', args: ['status'], normalize: ['ago', 'sha'] },
  {
    name: 'status-subdir',
    repo: 'init',
    ops: [{ mkdir: 'src/deep' }],
    cwd: '<REPO>/src/deep',
    args: ['status'],
    normalize: ['ago', 'sha']
  },
  { name: 'status-path-arg', repo: 'init', cwd: '<TMP>', args: ['status', 'repo'], normalize: ['ago', 'sha'] },
  { name: 'status-not-a-repo', ops: [{ mkdir: '<TMP>/plain' }], cwd: '<TMP>/plain', args: ['status'] },
  { name: 'status-missing-dir', cwd: '<TMP>', args: ['status', 'nope'] },
  {
    name: 'status-dirty',
    repo: 'init',
    ops: [
      { write: ['a.txt', 'one\n'] },
      { write: ['b.txt', 'two\n'] },
      { git: ['add', 'a.txt', 'b.txt'] },
      { git: ['commit', '-q', '-m', 'feat: add files'] },
      { write: ['a.txt', 'one changed\n'] },
      { write: ['b.txt', 'two changed\n'] },
      { git: ['add', 'b.txt'] },
      { write: ['new.txt', 'untracked\n'] },
      { write: ['c.txt', 'stash me\n'] },
      { git: ['stash', 'push', '-q', '--include-untracked', '--', 'c.txt'] }
    ],
    args: ['status'],
    normalize: ['ago', 'sha']
  },
  {
    name: 'status-upstream',
    repo: 'init',
    ops: [
      { git: ['branch', 'base'] },
      { write: ['x.txt', 'x\n'] },
      { git: ['add', 'x.txt'] },
      { git: ['commit', '-q', '-m', 'feat: ahead one'] },
      { write: ['y.txt', 'y\n'] },
      { git: ['add', 'y.txt'] },
      { git: ['commit', '-q', '-m', 'feat: ahead two'] },
      { git: ['checkout', '-q', 'base'] },
      { write: ['z.txt', 'z\n'] },
      { git: ['add', 'z.txt'] },
      { git: ['commit', '-q', '-m', 'fix: behind one'] },
      { git: ['checkout', '-q', 'main'] },
      { git: ['branch', '--set-upstream-to=base'] }
    ],
    args: ['status'],
    normalize: ['ago', 'sha']
  },
  {
    name: 'status-conflicted-fixture',
    repo: { fixture: 'merge-conflict' },
    ops: [{ git: ['merge', '--no-edit', 'feature'], mayFail: true }],
    args: ['status'],
    normalize: ['ago', 'sha']
  },
  {
    name: 'status-stash-fixture',
    repo: { fixture: 'stash-picking' },
    args: ['status'],
    normalize: ['ago', 'sha']
  },
  {
    name: 'status-protected-checklist',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_FULL] }],
    args: ['status'],
    normalize: ['ago', 'sha']
  },
  {
    name: 'status-protected-glob',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_FULL] }, { git: ['checkout', '-q', '-b', 'release/1.2'] }],
    args: ['status'],
    normalize: ['ago', 'sha']
  },
  {
    name: 'status-detached',
    repo: 'init',
    ops: [{ git: ['checkout', '-q', '--detach'] }],
    args: ['status'],
    normalize: ['ago', 'sha']
  },
  {
    name: 'status-empty-repo',
    ops: [{ git: ['init', '-q', '-b', 'main', '<TMP>/empty'] }],
    cwd: '<TMP>/empty',
    args: ['status']
  },

  // doctor
  { name: 'doctor-no-config', repo: 'init', args: ['doctor'] },
  {
    name: 'doctor-no-requires',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_FULL] }],
    args: ['doctor']
  },
  {
    name: 'doctor-failing',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_REQUIRES] }, { write: ['.env.example', 'TOKEN=\n'] }],
    args: ['doctor']
  },
  {
    name: 'doctor-fix',
    repo: 'init',
    ops: [
      { write: ['.gitcito.json', CONFIG_REQUIRES] },
      { write: ['.env.example', 'TOKEN=\n'] },
      { mkdir: '.husky' }
    ],
    args: ['doctor', '--fix'],
    check: { files: ['.env'], config: ['local:core.hooksPath'] }
  },
  {
    name: 'doctor-fix-cannot',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_REQUIRES] }],
    args: ['doctor', '--fix'],
    check: { files: ['.env'] }
  },
  {
    name: 'doctor-path-arg',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_REQUIRES] }, { write: ['.env', 'TOKEN=1\n'] }],
    cwd: '<TMP>',
    args: ['doctor', 'repo']
  },
  { name: 'doctor-not-a-repo', ops: [{ mkdir: '<TMP>/plain' }], cwd: '<TMP>/plain', args: ['doctor'] },

  // commit-check
  { name: 'commit-check-good', repo: 'init', args: ['commit-check', '-m', 'feat: add range-diff'] },
  { name: 'commit-check-long', repo: 'init', args: ['commit-check', '-m', `feat: ${'x'.repeat(80)}`] },
  { name: 'commit-check-warn', repo: 'init', args: ['commit-check', `--message=feat: ${'x'.repeat(60)}.`] },
  {
    name: 'commit-check-file',
    repo: 'init',
    ops: [{ write: ['msg.txt', 'fix: thing\n# comment\nbody right away\n'] }],
    args: ['commit-check', 'msg.txt']
  },
  {
    name: 'commit-check-default-file',
    repo: 'init',
    ops: [{ write: ['.git/COMMIT_EDITMSG', 'feat: from the editor\n\nBody.\n'] }],
    args: ['commit-check']
  },
  { name: 'commit-check-nothing', repo: 'init', args: ['commit-check'] },
  { name: 'commit-check-dangling-m', repo: 'init', args: ['commit-check', '-m'] },
  { name: 'commit-check-empty-inline', repo: 'init', args: ['commit-check', '-m', ''] },
  {
    name: 'commit-check-scopes',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_FULL] }],
    args: ['commit-check', '-m', 'feat(db): add index']
  },
  {
    name: 'commit-check-not-conventional',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_FULL] }],
    args: ['commit-check', '-m', 'just some text']
  },
  {
    name: 'commit-check-ticket',
    repo: 'init',
    ops: [
      { write: ['.gitcito.json', JSON.stringify({ version: 1, commit: { ticketFromBranch: true } })] },
      { git: ['checkout', '-q', '-b', 'feature/ABC-12-thing'] }
    ],
    args: ['commit-check', '-m', 'fix: thing']
  },
  {
    name: 'commit-check-repo-flag',
    repo: 'init',
    cwd: '<TMP>',
    args: ['commit-check', '--repo=repo', '-m', 'docs: explain']
  },
  { name: 'commit-check-not-a-repo', cwd: '<TMP>', args: ['commit-check', '-m', 'feat: x'] },

  // config
  { name: 'config-show-none', repo: 'init', args: ['config'] },
  { name: 'config-show', repo: 'init', ops: [{ write: ['.gitcito.json', CONFIG_FULL + '\n\n'] }], args: ['config', 'show'] },
  { name: 'config-check-valid', repo: 'init', ops: [{ write: ['.gitcito.json', CONFIG_FULL] }], args: ['config', 'check'] },
  {
    name: 'config-check-dropped',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', '{"version":1,"protect":"main","bogus":true}'] }],
    args: ['config', 'check']
  },
  {
    name: 'config-check-invalid',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', '{ not json'] }],
    args: ['config', 'check']
  },
  { name: 'config-check-missing', repo: 'init', args: ['config', 'check'] },
  {
    name: 'config-check-path',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', CONFIG_FULL] }],
    cwd: '<TMP>',
    args: ['config', 'check', 'repo']
  },
  { name: 'config-init-nothing', repo: 'init', args: ['config', 'init'], check: { files: ['.gitcito.json'] } },
  {
    name: 'config-init-dry-run',
    repo: 'init',
    ops: [
      { write: ['.nvmrc', '20\n'] },
      { write: ['.env.example', 'A=1\n'] },
      { git: ['add', '-A'] },
      { git: ['commit', '-q', '-m', 'chore: tooling'] }
    ],
    args: ['config', 'init', '--dry-run'],
    check: { files: ['.gitcito.json'] }
  },
  {
    name: 'config-init-write',
    repo: 'init',
    ops: [
      { write: ['.nvmrc', 'v18.17.0\n'] },
      { write: ['a.txt', '1\n'] },
      { git: ['add', '-A'] },
      { git: ['commit', '-q', '-m', 'feat(api): one'] },
      { write: ['a.txt', '2\n'] },
      { git: ['commit', '-q', '-am', 'fix(api): two'] },
      { write: ['a.txt', '3\n'] },
      { git: ['commit', '-q', '-am', 'feat(ui): three'] }
    ],
    args: ['config', 'init'],
    check: { files: ['.gitcito.json'] }
  },
  {
    name: 'config-init-exists',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', '{"version":1}'] }, { write: ['.nvmrc', '20\n'] }],
    args: ['config', 'init'],
    check: { files: ['.gitcito.json'] }
  },
  {
    name: 'config-init-force',
    repo: 'init',
    ops: [{ write: ['.gitcito.json', '{"version":1}'] }, { write: ['.nvmrc', '20\n'] }],
    args: ['config', 'init', '--force'],
    check: { files: ['.gitcito.json'] }
  },
  { name: 'config-unknown', repo: 'init', args: ['config', 'frob'] },
  { name: 'config-not-a-repo', cwd: '<TMP>', args: ['config', 'check'] },

  // repos and --resolve
  { name: 'repos-none', cwd: '<TMP>', args: ['repos'] },
  { name: 'repos-list', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['repos'] },
  { name: 'repos-filter', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['repos', 'WE'] },
  { name: 'repos-filter-none', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['repos', 'zzz'] },
  { name: 'repos-paths', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['repos', '--paths'] },
  { name: 'repos-paths-filter', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['repos', '--paths', 'api'] },
  { name: 'repos-garbage-settings', settings: 'not an object', cwd: '<TMP>', args: ['repos'] },
  { name: 'resolve-exact', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['--resolve', 'api'] },
  { name: 'resolve-substring', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['--resolve', 'unnamed'] },
  { name: 'resolve-none', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['--resolve', 'zzz'] },
  { name: 'resolve-empty', cwd: '<TMP>', args: ['--resolve'] },

  // editor
  {
    name: 'editor-install-global',
    cwd: '<TMP>',
    args: ['editor'],
    check: { config: ['global:core.editor', 'global:sequence.editor'] }
  },
  {
    name: 'editor-uninstall-global',
    ops: [
      { git: ['config', '--global', 'core.editor', 'gitcito --wait'] },
      { git: ['config', '--global', 'sequence.editor', 'gitcito --wait'] }
    ],
    cwd: '<TMP>',
    args: ['editor', 'uninstall'],
    check: { config: ['global:core.editor', 'global:sequence.editor'] }
  },
  {
    name: 'editor-install-local',
    repo: 'init',
    args: ['editor', 'install', '--local'],
    check: { config: ['local:core.editor', 'local:sequence.editor', 'global:core.editor'] }
  },
  { name: 'editor-local-not-a-repo', cwd: '<TMP>', args: ['editor', '--local'] },
  { name: 'editor-unknown', cwd: '<TMP>', args: ['editor', 'frob'], check: { config: ['global:core.editor'] } },

  // Window verbs (a fake app records what it was asked to open)
  { name: 'open-cwd', repo: 'init', args: [] },
  { name: 'open-dot', repo: 'init', args: ['.'] },
  { name: 'open-path-name-group', repo: 'init', cwd: '<TMP>', args: ['repo', '-n', 'My API', '--group', 'Work'] },
  { name: 'open-blame-line', repo: 'init', args: ['blame', 'src/a.ts', '-l', '40'] },
  { name: 'open-show-ref-path', repo: 'init', cwd: '<TMP>', args: ['show', 'HEAD~1', 'repo'] },
  { name: 'open-graph', repo: 'init', args: ['graph'] },
  { name: 'open-search', repo: 'init', args: ['search', 'TODO later'] },
  { name: 'open-double-dash', repo: 'init', args: ['--', '.'] },
  { name: 'open-by-name', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['open', 'api'] },
  { name: 'open-by-name-dir', repo: 'init', cwd: '<TMP>', args: ['open', 'repo'] },
  { name: 'open-by-name-none', repo: 'init', settings: SETTINGS, cwd: '<TMP>', args: ['open', 'zzz'] },
  { name: 'open-mistyped-verb', cwd: '<TMP>', args: ['stauts'] },
  { name: 'open-missing-path', cwd: '<TMP>', args: ['no/such'] },
  { name: 'open-unknown-option', cwd: '<TMP>', args: ['--frob'] },
  { name: 'open-unknown-option-eq', cwd: '<TMP>', args: ['--name=x'] },
  { name: 'open-no-app', repo: 'init', app: false, args: [] },
  { name: 'wait-no-file', repo: 'init', args: ['--wait'] },
  { name: 'wait-missing-file', repo: 'init', args: ['--wait', 'nope.txt'] },
  {
    name: 'wait-relative',
    repo: 'init',
    ops: [{ write: ['.git/COMMIT_EDITMSG', 'feat: draft\n'] }],
    args: ['--wait', '.git/COMMIT_EDITMSG'],
    check: { files: ['.git/COMMIT_EDITMSG'], sentinels: true }
  },
  {
    name: 'wait-outside-repo',
    ops: [{ write: ['<TMP>/notes/todo.txt', 'pick 1\n'] }],
    cwd: '<TMP>',
    args: ['-w', '<TMP>/notes/todo.txt'],
    check: { files: ['<TMP>/notes/todo.txt'], sentinels: true }
  },
  {
    name: 'wait-no-app',
    repo: 'init',
    app: false,
    ops: [{ write: ['.git/COMMIT_EDITMSG', 'feat: draft\n'] }],
    args: ['--wait', '.git/COMMIT_EDITMSG'],
    check: { files: ['.git/COMMIT_EDITMSG'], sentinels: true }
  },

  // textconv
  { name: 'textconv-usage', bin: 'textconv', cwd: '<TMP>', args: [] },
  { name: 'textconv-dash', bin: 'textconv', cwd: '<TMP>', args: ['-x'] },
  {
    name: 'textconv-json',
    bin: 'textconv',
    ops: [{ write: ['<TMP>/c.json', '{"b":1,"a":[2,{"d":3,"c":4}]}'] }],
    cwd: '<TMP>',
    args: ['<TMP>/c.json']
  },
  { name: 'textconv-unsupported', bin: 'textconv', ops: [{ write: ['<TMP>/x.bin', 'zz'] }], cwd: '<TMP>', args: ['<TMP>/x.bin'] }
]

// ─── Harness ────────────────────────────────────────────────────────────────

/** Records its argv; for an edit request, writes the file and deletes the
 *  sentinel the way the app does. Same text in the Go harness. */
const FAKE_APP = `#!/bin/sh
for a in "$@"; do printf '%s\\n' "$a" >> "$FAKE_APP_LOG.part"; done
edit=; wait=
for a in "$@"; do
  case "$a" in
    --edit=*) edit=\${a#--edit=} ;;
    --wait=*) wait=\${a#--wait=} ;;
  esac
done
if [ -n "$wait" ]; then printf 'edited by the app\\n' > "$edit"; rm -f "$wait"; fi
mv "$FAKE_APP_LOG.part" "$FAKE_APP_LOG"
`

interface Result {
  stdout: string
  stderr: string
  code: number
  appArgs: string[] | null
  files?: Record<string, string | null>
  config?: Record<string, string | null>
  sentinels?: number
}

let bundle = ''
let pkgVersion = ''

const GIT_IDENTITY = {
  GIT_AUTHOR_NAME: 'Gitcito Test',
  GIT_AUTHOR_EMAIL: 'test@gitcito.invalid',
  GIT_COMMITTER_NAME: 'Gitcito Test',
  GIT_COMMITTER_EMAIL: 'test@gitcito.invalid',
  GIT_AUTHOR_DATE: '2024-01-02T03:04:05Z',
  GIT_COMMITTER_DATE: '2024-01-02T03:04:05Z'
}

function caseEnv(tmp: string): NodeJS.ProcessEnv {
  return {
    PATH: process.env.PATH,
    HOME: join(tmp, 'home'),
    XDG_CONFIG_HOME: join(tmp, 'xdg'),
    GIT_CONFIG_GLOBAL: join(tmp, 'home', '.gitconfig'),
    GIT_CONFIG_NOSYSTEM: '1',
    TMPDIR: join(tmp, 'tmp'),
    LC_ALL: 'C',
    LANG: 'C',
    GITCITO_DATA_DIR: join(tmp, 'data'),
    FAKE_APP_LOG: join(tmp, 'app.log'),
    ...GIT_IDENTITY
  }
}

const sub = (s: string, tmp: string): string => s.split('<REPO>').join(join(tmp, 'repo')).split('<TMP>').join(tmp)
const at = (p: string, tmp: string): string => (p.startsWith('<TMP>') ? sub(p, tmp) : join(tmp, 'repo', p))

function setup(spec: Spec, tmp: string, env: NodeJS.ProcessEnv): void {
  for (const d of ['home', 'xdg', 'tmp', 'data']) mkdirSync(join(tmp, d), { recursive: true })
  writeFileSync(join(tmp, 'home', '.gitconfig'), '[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n')
  const repo = join(tmp, 'repo')
  const git = (args: string[], cwd: string, mayFail = false): void => {
    const r = spawnSync('git', args, { cwd, env, encoding: 'utf8' })
    if (r.status !== 0 && !mayFail) throw new Error(`setup git ${args.join(' ')}: ${r.stderr}`)
  }
  if (spec.repo === 'init') {
    git(['init', '-q', '-b', 'main', repo], tmp)
    writeFileSync(join(repo, 'README.md'), '# demo\n')
    git(['add', '-A'], repo)
    git(['commit', '-q', '-m', 'chore: initial commit'], repo)
  } else if (spec.repo) {
    cpSync(repoPath(spec.repo.fixture), repo, { recursive: true })
    git(['config', '--local', 'rerere.enabled', 'false'], repo)
  }
  for (const op of spec.ops ?? []) {
    if ('write' in op) {
      const p = at(op.write[0], tmp)
      mkdirSync(dirname(p), { recursive: true })
      writeFileSync(p, op.write[1])
    } else if ('mkdir' in op) {
      mkdirSync(at(op.mkdir, tmp), { recursive: true })
    } else if ('git' in op) {
      git(
        op.git.map((a) => sub(a, tmp)),
        existsSync(repo) ? repo : tmp,
        op.mayFail
      )
    }
  }
  if (spec.settings !== undefined) {
    const text = typeof spec.settings === 'string' ? spec.settings : JSON.stringify(spec.settings, null, 2)
    // The Electron build reads <XDG_CONFIG_HOME>/Gitcito; the Go build its own
    // data dir. Same content in both places.
    mkdirSync(join(tmp, 'xdg', 'Gitcito'), { recursive: true })
    writeFileSync(join(tmp, 'xdg', 'Gitcito', 'gitcito-settings.json'), sub(text, tmp))
    writeFileSync(join(tmp, 'data', 'gitcito-settings.json'), sub(text, tmp))
  }
}

function sleep(ms: number): void {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms)
}

function normalize(s: string, spec: Spec, tmp: string): string {
  let out = s
  for (const t of [realpathSync(tmp), tmp]) out = out.split(t).join('<TMP>')
  out = out.replace(/gitcito-wait\.[A-Za-z0-9]+/g, 'gitcito-wait.<ID>')
  if (spec.normalize?.includes('ago'))
    out = out.replace(/\b\d+ (seconds?|minutes?|hours?|days?|weeks?|months?|years?)(, \d+ months?)? ago/g, '<AGO>')
  if (spec.normalize?.includes('sha')) out = out.replace(/\b[0-9a-f]{7,40}\b/g, '<SHA>')
  if (spec.normalize?.includes('version')) out = out.split(pkgVersion).join('<VERSION>')
  return out
}

function runTs(spec: Spec): Result {
  const tmp = realpathSync(mkdtempSync(join(tmpdir(), 'gitcito-cli-golden-')))
  try {
    const env = caseEnv(tmp)
    setup(spec, tmp, env)
    // The shims resolve everything relative to themselves: resources/cli/ two
    // levels below the bundle root, the app binary at the root (Linux layout).
    const shimRoot = join(tmp, 'ts')
    cpSync(bundle, shimRoot, { recursive: true })
    if (spec.app !== false) {
      writeFileSync(join(shimRoot, 'gitcito'), FAKE_APP)
      chmodSync(join(shimRoot, 'gitcito'), 0o755)
    }
    const shim = join(shimRoot, 'resources', 'cli', spec.bin === 'textconv' ? 'gitcito-textconv' : 'gitcito')
    const cwd = sub(spec.cwd ?? (spec.repo ? '<REPO>' : '<TMP>'), tmp)
    const r = spawnSync(shim, spec.args.map((a) => sub(a, tmp)), { cwd, env: { ...env, PWD: cwd }, encoding: 'utf8' })
    if (r.error) throw new Error(`${spec.name}: ${r.error.message}`)
    const res: Result = {
      stdout: normalize(r.stdout, spec, tmp),
      stderr: normalize(r.stderr, spec, tmp),
      code: r.status ?? -1,
      appArgs: null
    }
    // The app is launched in the background: give it a moment to record.
    const log = join(tmp, 'app.log')
    if (isWindowVerb(spec) && res.code === 0 && spec.app !== false) {
      for (let i = 0; i < 100 && !existsSync(log); i++) sleep(50)
    }
    if (existsSync(log))
      res.appArgs = readFileSync(log, 'utf8')
        .split('\n')
        .filter((l, i, all) => i < all.length - 1 || l !== '')
        .map((l) => normalize(l, spec, tmp))
    if (spec.check?.files) {
      res.files = {}
      for (const f of spec.check.files) {
        const p = at(f, tmp)
        res.files[f] = existsSync(p) ? readFileSync(p, 'utf8') : null
      }
    }
    if (spec.check?.config) {
      res.config = {}
      for (const key of spec.check.config) {
        const [scope, name] = key.split(':')
        const r2 = spawnSync('git', ['config', `--${scope}`, '--get', name], {
          cwd: existsSync(join(tmp, 'repo')) ? join(tmp, 'repo') : tmp,
          env,
          encoding: 'utf8'
        })
        res.config[key] = r2.status === 0 ? r2.stdout.replace(/\n$/, '') : null
      }
    }
    if (spec.check?.sentinels) {
      res.sentinels = readdirSync(join(tmp, 'tmp')).filter((f) => f.startsWith('gitcito-wait.')).length
    }
    return res
  } finally {
    rmSync(tmp, { recursive: true, force: true })
  }
}

describe('gitcito CLI goldens', () => {
  beforeAll(async () => {
    pkgVersion = JSON.parse(readFileSync(join(ROOT, 'package.json'), 'utf8')).version
    bundle = mkdtempSync(join(tmpdir(), 'gitcito-cli-bundle-'))
    // The packaged layout the shims expect, minus Electron: out/main/*.js run
    // by `node`, package.json two levels above them for --version.
    await build({
      entryPoints: [join(ROOT, 'src/main/cliEntry.ts'), join(ROOT, 'src/main/textconvCli.ts')],
      bundle: true,
      platform: 'node',
      format: 'cjs',
      outdir: join(bundle, 'out', 'main'),
      logLevel: 'silent'
    })
    writeFileSync(join(bundle, 'package.json'), JSON.stringify({ version: pkgVersion }))
    mkdirSync(join(bundle, 'resources', 'cli'), { recursive: true })
    for (const f of ['gitcito', 'gitcito-textconv']) {
      cpSync(join(ROOT, 'resources', 'cli', f), join(bundle, 'resources', 'cli', f))
      chmodSync(join(bundle, 'resources', 'cli', f), 0o755)
    }
  }, 120_000)

  afterAll(() => {
    if (bundle) rmSync(bundle, { recursive: true, force: true })
  })

  it('matches the golden file', () => {
    const cases = SPECS.map((spec) => ({ ...spec, expect: runTs(spec) }))
    if (WRITE) {
      mkdirSync(dirname(GOLDENS), { recursive: true })
      writeFileSync(GOLDENS, JSON.stringify({ fakeApp: FAKE_APP, cases }, null, 2) + '\n')
    }
    const stored = JSON.parse(readFileSync(GOLDENS, 'utf8')) as { cases: typeof cases }
    expect(stored.cases.map((c) => c.name)).toEqual(cases.map((c) => c.name))
    for (let i = 0; i < cases.length; i++) expect(cases[i], cases[i].name).toEqual(stored.cases[i])
  }, 300_000)
})

// ─── Pure-logic vectors ─────────────────────────────────────────────────────

const scoped: RepoConfig = { version: 1, commit: { scopes: ['api', 'ui'], trailers: ['Refs: {ticket}'] } }
const ticketed: RepoConfig = { version: 1, commit: { ticketFromBranch: true } }

const COMMIT_INPUTS: { raw: string; config?: RepoConfig | null; branch?: string }[] = [
  { raw: '' },
  { raw: '# only comments\n' },
  { raw: 'feat: add range-diff\n\nA body that is comfortably short.' },
  { raw: 'feat: x\nbody right away' },
  { raw: `feat: ${'x'.repeat(60)}.` },
  { raw: `feat: ${'x'.repeat(80)}` },
  { raw: `feat: x\n\nhttps://example.com/${'y'.repeat(80)}` },
  { raw: `feat: x\n\n  ${'word '.repeat(20)}` },
  { raw: 'subject\n# ------------------------ >8 ------------------------\ndiff' },
  { raw: 'subject\n#\u00a0--- >8 ---\ndiff' },
  { raw: 'feat: crlf subject\r\n\r\nbody\r\n' },
  { raw: '  feat: padded  \n\n' },
  { raw: `feat: ${'é'.repeat(45)}` },
  { raw: `feat: ${'😀'.repeat(30)}` },
  { raw: 'just some text', config: scoped },
  { raw: 'feat(db): x', config: scoped },
  { raw: 'feat(api): x', config: scoped },
  { raw: 'feat( ui ): x', config: scoped },
  { raw: 'feat(api)!: breaking', config: scoped },
  { raw: 'feat(api): x\n\nRefs: ABC-1', config: scoped },
  { raw: 'feat(api): x\n\n  refs: lower', config: scoped },
  { raw: 'feat: x', config: { version: 1, commit: { scopes: ['a'], trailers: [':nothing', 'Signed-off-by'] } } },
  { raw: 'fix: thing', config: ticketed, branch: 'feature/ABC-12-thing' },
  { raw: 'fix: ABC-12 thing', config: ticketed, branch: 'feature/ABC-12-thing' },
  { raw: 'fix: thing', config: ticketed, branch: 'feature/no-ticket' },
  { raw: 'fix: thing', config: ticketed },
  { raw: 'fix: thing', config: null, branch: 'feature/ABC-12' },
  { raw: 'feat:no space', config: scoped },
  { raw: 'Feat(API): x', config: scoped }
]

const STRIP_INPUTS = [
  'feat: x\n# a comment\n\nbody',
  'subject\n# ------------------------ >8 ------------------------\ndiff',
  '#-->8--\nall gone',
  '# ->8 not scissors\nkept',
  '\n\n  spaced  \n\n'
]

const REPO_SETTINGS: unknown[] = [
  null,
  'nope',
  7,
  { tabs: 'nope', recentRepos: 7 },
  { tabs: [{ repos: [{ path: '/x' }] }] },
  {
    tabs: [
      { kind: 'repo', repos: [{ path: '/a/api', name: 'API' }] },
      { kind: 'group', name: 'Work', repos: [{ path: '/a/web', name: 'Web' }, { path: '/a/api', name: 'API' }] },
      { kind: 'page' },
      null,
      { kind: 'group', name: '', repos: [{ path: '/a/nameless-group', name: '' }] },
      { kind: 'group', repos: [{ path: 'C:\\code\\win\\', name: null }, { path: '' }, { path: 5 }, null] }
    ],
    recentRepos: [{ path: '/a/old', name: 'Old' }, { path: '/a/api' }, { path: '/trailing/slash/' }, 'x', null]
  }
]

const NEEDLES = ['API', 'api', 'we', 'nothing', '/a/', 'OLD', '', 'slash']

describe('cli pure-logic vectors', () => {
  it('commitCheck matches the vector file', () => {
    const vectors = {
      check: COMMIT_INPUTS.map((input) => ({
        input,
        out: checkCommitMessage(input.raw, { config: input.config, branch: input.branch })
      })),
      strip: STRIP_INPUTS.map((input) => ({ input, out: stripCommitComments(input) }))
    }
    if (WRITE) writeFileSync(COMMIT_VECTORS, JSON.stringify(vectors, null, 2) + '\n')
    expect(JSON.parse(readFileSync(COMMIT_VECTORS, 'utf8'))).toEqual(JSON.parse(JSON.stringify(vectors)))
  })

  it('cliRepos matches the vector file', () => {
    const vectors = REPO_SETTINGS.map((settings) => {
      const repos = knownRepos(settings)
      return {
        settings,
        repos,
        matches: NEEDLES.map((n) => repos.map((r) => matchesRepo(r, n))),
        resolved: NEEDLES.map((n) => resolveRepo(repos, n))
      }
    })
    if (WRITE) writeFileSync(REPOS_VECTORS, JSON.stringify(vectors, null, 2) + '\n')
    expect(JSON.parse(readFileSync(REPOS_VECTORS, 'utf8'))).toEqual(JSON.parse(JSON.stringify(vectors)))
  })
})

