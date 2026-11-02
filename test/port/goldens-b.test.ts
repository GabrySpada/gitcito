// Slice B git goldens, captured from the TypeScript gitService and replayed by
// the Go port (internal/gitsvc/b_golden_test.go).
//
// PORT_WRITE_GOLDENS=1 (re)writes internal/gitsvc/testdata/goldens/<method>/…;
// without it every case re-runs against TypeScript and must still match its
// file, so neither implementation can drift while TS exists.
//
// A golden file is self-describing — method, fixture, prep steps, arguments,
// the normalized result (or error) and, for writes, the repository state
// afterwards — so the Go side reads everything it needs from the file.
//
// Normalization (mirrored byte for byte by bNormalize in the Go test):
//   - paths: the repo under test → <REPO>, a second "upstream" clone → <UP>,
//     os.tmpdir() → <TMP>, the home directory → <HOME>.
//   - commit and tag object ids → a content label (<Cxxxxxxxxxx>, <Txxxxxxxxxx>;
//     a trailing ~ marks an abbreviation). Most playground scenarios commit at
//     "now", so ids change every time the playground is rebuilt; the label is
//     a hash of what the object says (tree, parent labels, identities without
//     dates, message) and survives a rebuild. Blob and tree ids are already
//     content addresses and stay as they are.
//   - wall clock: a JSON integer in [1e9, 1e10) → "<TS>"; in strings
//     `<epoch> <+zone>` → "<TS> <TZ>", a bare 16xxxxxxxx–19xxxxxxxx epoch →
//     "<TS>", an ISO timestamp → "<ISO>", the snapshot/purge ref stamps → <N>.
//     Cases flagged maskDates also turn YYYY-MM-DD into <DATE> (the changelog
//     heading carries today's date).
import { describe, it, expect, afterAll } from 'vitest'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { gitService } from '../../src/main/git'
import { repoPath } from '../helpers'
import { cloneFixture, cleanupFixtures } from '../fixtures'

afterAll(() => {
  cleanupFixtures()
  rmSync(dirname(GLOBAL_CONFIG), { recursive: true, force: true })
})

// The machine's git config must not decide what a golden says: the same fixed
// global config the Go side installs (testutil.IsolateGitEnv), and no system one.
const GLOBAL_CONFIG = join(mkdtempSync(join(tmpdir(), 'gitcito-goldens-b-')), 'gitconfig')
writeFileSync(
  GLOBAL_CONFIG,
  '[user]\n\tname = Gitcito Test\n\temail = test@gitcito.invalid\n' +
    '[init]\n\tdefaultBranch = main\n' +
    '[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n'
)
process.env.GIT_CONFIG_GLOBAL = GLOBAL_CONFIG
process.env.GIT_CONFIG_NOSYSTEM = '1'

const GOLDENS = resolve(__dirname, '../../internal/gitsvc/testdata/goldens')
const WRITE = process.env.PORT_WRITE_GOLDENS === '1'

type Json = null | boolean | number | string | Json[] | { [k: string]: Json }

/** A prep step, run on a clone before the call (both languages implement these). */
type Prep =
  | { git: string[]; mayFail?: boolean } // git in the repo under test
  | { write: [string, string] } // write a file (repo-relative)
  | { writeBig: [string, number] } // write N bytes of 'a'
  | { up: string } // clone a second fixture as <UP>
  | { gitUp: string[]; mayFail?: boolean } // git in <UP>
  | { writeUp: [string, string] } // write a file in <UP>

interface Case {
  method: string
  fixture: string
  name: string
  /** Arguments after the repo path. `{ $rev }` / `{ $revUp }` resolve to a full sha. */
  args?: Json[]
  prep?: Prep[]
  write?: boolean
  /** Extra git commands whose output joins the state snapshot. */
  extra?: string[][]
  maskDates?: boolean
}

const rev = (expr: string): Json => ({ $rev: expr })
const revUp = (expr: string): Json => ({ $revUp: expr })

// A remote carrying pull-request refs, built from the merge-conflict fixture —
// the shape test/prPreview.test.ts uses, without the network.
const PR_UPSTREAM: Prep[] = [
  { up: 'merge-conflict' },
  { gitUp: ['checkout', '-q', '-b', 'tidy', 'main'] },
  { writeUp: ['NOTES.md', 'from the pull request\n'] },
  { gitUp: ['add', 'NOTES.md'] },
  { gitUp: ['commit', '-q', '-m', 'docs: add notes'] },
  { gitUp: ['checkout', '-q', 'main'] },
  { gitUp: ['update-ref', 'refs/pull/7/head', 'feature'] },
  { gitUp: ['update-ref', 'refs/merge-requests/3/head', 'tidy'] },
  { git: ['remote', 'add', 'up', '<UP>'] }
]

// A bare origin copied from the playground, so a push never touches the shared one.
const OWN_ORIGIN: Prep[] = [{ up: 'multi-remote-origin.git' }, { git: ['remote', 'set-url', 'origin', '<UP>'] }]

const FAKE_BACKUP: Prep[] = [
  { git: ['update-ref', 'refs/gitcito/pre-purge/1700000000/heads/main', 'HEAD~1'] },
  { git: ['update-ref', 'refs/gitcito/pre-purge/1700000000/tags/v1.0.0', 'v1.0.0'] },
  { git: ['config', 'gitcito.prepurge.at1700000000', 'config/credentials.env\nother.txt'] }
]

const SECRET = 'config/credentials.env'

const CASES: Case[] = [
  // ─── diffs ───────────────────────────────────────────────────────────────
  { method: 'diffFile', fixture: 'line-staging', name: 'unstaged', args: ['config.js', false, false] },
  { method: 'diffFile', fixture: 'line-staging', name: 'staged-empty', args: ['config.js', true, false] },
  { method: 'diffFile', fixture: 'code-search', name: 'staged', args: ['src/util/log.js', true, false] },
  { method: 'diffFile', fixture: 'code-search', name: 'untracked', args: ['scratch.js', false, true] },
  { method: 'diffFile', fixture: 'code-search', name: 'untracked-missing', args: ['nope.js', false, true] },
  { method: 'diffFile', fixture: 'word-diff', name: 'ignore-ws', args: ['config.ts', false, false, true] },
  { method: 'stagedDiff', fixture: 'code-search', name: 'staged' },
  { method: 'stagedDiff', fixture: 'line-staging', name: 'nothing-staged' },
  { method: 'commitFiles', fixture: 'tags-and-releases', name: 'head', args: [rev('HEAD')] },
  { method: 'commitFiles', fixture: 'time-machine', name: 'renames', args: [rev(':/fold lib/ into src/utils/')] },
  { method: 'commitFiles', fixture: 'conflict-radar', name: 'merge', args: [rev(':/merge chore/merged')] },
  { method: 'commitFiles', fixture: 'tags-and-releases', name: 'root', args: [rev(':/chore: initial scaffolding')] },
  { method: 'commitFiles', fixture: 'tags-and-releases', name: 'bad-object', args: ['nonexistent'] },
  { method: 'commitFileDiff', fixture: 'tags-and-releases', name: 'head', args: [rev('HEAD'), 'plugins.py'] },
  { method: 'commitFileDiff', fixture: 'tags-and-releases', name: 'ignore-ws', args: [rev(':/feat!: rewrite as App class'), 'app.py', true] },
  { method: 'commitFileDiff', fixture: 'conflict-radar', name: 'merge', args: [rev(':/merge chore/merged'), '.gitignore'] },
  { method: 'commitDiff', fixture: 'tags-and-releases', name: 'head', args: [rev('HEAD')] },
  { method: 'commitDiff', fixture: 'binary-images-unicode', name: 'binary', args: [rev(':/design: recolour')] },
  { method: 'commitDiff', fixture: 'tags-and-releases', name: 'bad-object', args: ['nonexistent'] },
  { method: 'stashFiles', fixture: 'stash-picking', name: 'with-untracked', args: [rev('stash@{0}'), rev('stash@{0}^3')] },
  { method: 'stashFiles', fixture: 'stash-picking', name: 'tracked-only', args: [rev('stash@{0}')] },
  { method: 'stashFiles', fixture: 'stash-picking', name: 'bad-untracked', args: [rev('stash@{0}'), 'nonexistent'] },
  { method: 'stashFileDiff', fixture: 'stash-picking', name: 'tracked', args: [rev('stash@{0}'), 'alpha.txt'] },
  { method: 'stashFileDiff', fixture: 'stash-picking', name: 'untracked', args: [rev('stash@{0}'), 'delta-untracked.txt', true] },
  { method: 'stashFileDiff', fixture: 'stash-picking', name: 'ignore-ws', args: [rev('stash@{0}'), 'beta.txt', false, true] },
  {
    method: 'semanticDiff',
    fixture: 'conflict-radar',
    name: 'refs',
    args: ['src/app.js', { kind: 'ref', ref: 'main^1' }, { kind: 'ref', ref: 'main' }]
  },
  {
    method: 'semanticDiff',
    fixture: 'conflict-radar',
    name: 'missing',
    args: ['nope.ts', { kind: 'ref', ref: 'main' }, { kind: 'ref', ref: 'main' }]
  },
  {
    method: 'semanticDiff',
    fixture: 'code-search',
    name: 'index-worktree',
    args: ['src/util/mail.js', { kind: 'index' }, { kind: 'worktree' }]
  },
  {
    method: 'semanticDiff',
    fixture: 'code-search',
    name: 'empty-head',
    args: ['src/auth/guard.js', { kind: 'empty' }, { kind: 'ref', ref: 'HEAD' }]
  },
  { method: 'imageDiff', fixture: 'binary-images-unicode', name: 'refs', args: ['assets/logo.png', 'HEAD~3', 'HEAD'] },
  { method: 'imageDiff', fixture: 'binary-images-unicode', name: 'added', args: ['assets/icon.png', null] },
  { method: 'imageDiff', fixture: 'binary-images-unicode', name: 'missing-before', args: ['assets/icon.png', 'HEAD~4'] },

  // ─── commit details ──────────────────────────────────────────────────────
  { method: 'commitBranches', fixture: 'multi-remote', name: 'base', args: [rev('HEAD~1')] },
  { method: 'commitBranches', fixture: 'multi-remote', name: 'head', args: [rev('HEAD')] },
  { method: 'commitBranches', fixture: 'conflict-radar', name: 'root', args: [rev(':/init: app, config and readme')] },
  { method: 'commitBranches', fixture: 'multi-remote', name: 'bad-object', args: ['nonexistent'] },
  { method: 'commitTags', fixture: 'tags-and-releases', name: 'tagged', args: [rev('v1.0.1')] },
  { method: 'commitTags', fixture: 'tags-and-releases', name: 'untagged', args: [rev(':/chore: initial scaffolding')] },
  { method: 'commitMenuProbe', fixture: 'cherry-pick', name: 'head', args: [rev('HEAD')] },
  { method: 'commitMenuProbe', fixture: 'cherry-pick', name: 'root', args: [rev('HEAD~1')] },
  { method: 'commitMenuProbe', fixture: 'multi-remote', name: 'published-base', args: [rev('HEAD~2')] },
  { method: 'commitMenuProbe', fixture: 'multi-remote', name: 'local-head', args: [rev('HEAD')] },
  { method: 'commitMenuProbe', fixture: 'multi-remote', name: 'published-parent', args: [rev('HEAD~1')] },
  { method: 'commitMenuProbe', fixture: 'multi-remote', name: 'other-branch', args: [rev('upstream/main')] },
  {
    method: 'commitMenuProbe',
    fixture: 'merge-conflict',
    name: 'merging',
    args: [rev('HEAD')],
    prep: [{ git: ['merge', '--no-commit', 'feature'], mayFail: true }]
  },
  {
    method: 'commitMenuProbe',
    fixture: 'cherry-pick',
    name: 'detached',
    args: [rev('HEAD')],
    prep: [{ git: ['checkout', '-q', '--detach', 'HEAD'] }]
  },
  {
    method: 'commitMenuProbe',
    fixture: 'cherry-pick',
    name: 'bisecting',
    args: [rev('HEAD')],
    prep: [{ git: ['bisect', 'start'] }]
  },

  // ─── file inspection ─────────────────────────────────────────────────────
  { method: 'fileContent', fixture: 'tags-and-releases', name: 'worktree', args: ['app.py'] },
  { method: 'fileContent', fixture: 'tags-and-releases', name: 'at-ref', args: ['app.py', 'v1.0.0'] },
  { method: 'fileContent', fixture: 'tags-and-releases', name: 'bad-ref-falls-back', args: ['app.py', 'nosuchref'] },
  { method: 'fileContent', fixture: 'tags-and-releases', name: 'missing-everywhere', args: ['gone.txt', 'HEAD'] },
  { method: 'fileContent', fixture: 'tags-and-releases', name: 'missing-worktree', args: ['gone.txt'] },
  { method: 'fileContent', fixture: 'binary-images-unicode', name: 'unicode', args: ['日本語ファイル.md', 'HEAD'] },
  { method: 'fileContent', fixture: 'binary-images-unicode', name: 'binary', args: ['data.bin'] },
  {
    method: 'fileContent',
    fixture: 'tags-and-releases',
    name: 'too-large',
    args: ['big.txt'],
    prep: [{ writeBig: ['big.txt', 16 * 1024 * 1024 + 1] }]
  },
  {
    method: 'fileContent',
    fixture: 'tags-and-releases',
    name: 'forced',
    args: ['app.py', '', true]
  },
  { method: 'fileDataUrl', fixture: 'binary-images-unicode', name: 'worktree', args: ['assets/icon.png'] },
  { method: 'fileDataUrl', fixture: 'binary-images-unicode', name: 'at-ref', args: ['assets/logo.png', 'HEAD~3'] },
  { method: 'fileDataUrl', fixture: 'binary-images-unicode', name: 'missing', args: ['nope.png'] },
  { method: 'fileDataUrl', fixture: 'binary-images-unicode', name: 'missing-at-ref', args: ['assets/icon.png', 'HEAD~4'] },
  {
    method: 'fileDataUrl',
    fixture: 'tags-and-releases',
    name: 'too-large',
    args: ['big.bin'],
    prep: [{ writeBig: ['big.bin', 32 * 1024 * 1024 + 1] }]
  },
  { method: 'blameFile', fixture: 'tags-and-releases', name: 'head', args: ['app.py'] },
  { method: 'blameFile', fixture: 'tags-and-releases', name: 'at-ref', args: ['app.py', 'v1.0.1'] },
  { method: 'blameFile', fixture: 'tags-and-releases', name: 'missing', args: ['nope.txt'] },
  { method: 'blameFile', fixture: 'insights', name: 'many-authors', args: ['src/core.js'] },
  { method: 'fileHistory', fixture: 'time-machine', name: 'follow-rename', args: ['src/utils/util.js'] },
  { method: 'fileHistory', fixture: 'time-machine', name: 'missing', args: ['nope.js'] },
  { method: 'fileHistory', fixture: 'insights', name: 'authors', args: ['src/core.js'] },

  // ─── search ──────────────────────────────────────────────────────────────
  {
    method: 'searchFileContents',
    fixture: 'code-search',
    name: 'literal',
    args: [['src/util/log.js', 'src/util/mail.js', 'README.md', 'missing.js'], 'TODO']
  },
  { method: 'searchFileContents', fixture: 'code-search', name: 'empty-query', args: [['a', 'b'], ''] },
  {
    method: 'searchFileContents',
    fixture: 'code-search',
    name: 'case-sensitive',
    args: [['src/util/log.js', 'README.md'], 'todo', { caseSensitive: true }]
  },
  {
    method: 'searchFileContents',
    fixture: 'code-search',
    name: 'regex',
    args: [['src/util/mail.js', 'src/util/log.js'], '\\w+@corp\\.dev', { regex: true }]
  },
  { method: 'searchFileContents', fixture: 'code-search', name: 'bad-regex', args: [['README.md'], '(', { regex: true }] },
  {
    method: 'searchFileMatches',
    fixture: 'code-search',
    name: 'literal',
    args: [['src/util/log.js', 'src/util/mail.js'], 'TODO']
  },
  { method: 'searchFileMatches', fixture: 'code-search', name: 'case-sensitive', args: [['README.md'], 'todo', { caseSensitive: true }] },
  { method: 'searchFileMatches', fixture: 'code-search', name: 'case-insensitive', args: [['README.md'], 'todo'] },
  {
    method: 'searchFileMatches',
    fixture: 'code-search',
    name: 'regex',
    args: [['src/util/mail.js'], '\\w+@corp\\.dev', { regex: true }]
  },
  { method: 'searchFileMatches', fixture: 'code-search', name: 'whole-word', args: [['src/util/log.js'], 'TOD', { wholeWord: true }] },
  { method: 'searchFileMatches', fixture: 'code-search', name: 'max-per-file', args: [['src/util/log.js'], 'TODO', { maxPerFile: 2 }] },
  {
    method: 'searchFileMatches',
    fixture: 'code-search',
    name: 'max',
    args: [['src/util/log.js', 'src/util/mail.js'], 'TODO', { max: 4 }]
  },
  { method: 'searchFileMatches', fixture: 'code-search', name: 'blank', args: [['README.md'], '   '] },
  { method: 'searchFileMatches', fixture: 'code-search', name: 'bad-regex', args: [['README.md'], '[', { regex: true }] },
  { method: 'searchFileMatches', fixture: 'binary-images-unicode', name: 'binary-skipped', args: [['data.bin', 'README.md'], 'a'] },
  { method: 'searchCommitMatches', fixture: 'code-search', name: 'at-commit', args: [rev(':/add validateToken'), 'validateToken'] },
  { method: 'searchCommitMatches', fixture: 'code-search', name: 'gone-at-head', args: ['HEAD', 'validateToken'] },
  { method: 'searchCommitMatches', fixture: 'code-search', name: 'paths', args: ['HEAD', 'TODO', { paths: ['README.md'] }] },
  {
    method: 'searchCommitMatches',
    fixture: 'code-search',
    name: 'flags',
    args: ['HEAD', 'todo', { caseSensitive: true, wholeWord: true, regex: true, max: 3, maxPerFile: 1 }]
  },
  { method: 'searchCommitMatches', fixture: 'code-search', name: 'max-per-file', args: ['HEAD', 'TODO', { maxPerFile: 1 }] },
  { method: 'searchCommitMatches', fixture: 'code-search', name: 'blank', args: ['HEAD', ' '] },
  { method: 'grepWorkingTree', fixture: 'code-search', name: 'literal', args: ['TODO'] },
  { method: 'grepWorkingTree', fixture: 'code-search', name: 'case-sensitive', args: ['todo', { caseSensitive: true }] },
  { method: 'grepWorkingTree', fixture: 'code-search', name: 'max', args: ['TODO', { max: 2 }] },
  { method: 'grepWorkingTree', fixture: 'code-search', name: 'max-zero', args: ['TODO', { max: 0 }] },
  { method: 'grepWorkingTree', fixture: 'code-search', name: 'regex-word', args: ['\\w+@corp', { regex: true, wholeWord: true }] },
  { method: 'grepWorkingTree', fixture: 'code-search', name: 'no-match', args: ['zzzz-no-such-text'] },
  { method: 'grepWorkingTree', fixture: 'code-search', name: 'blank', args: [''] },
  { method: 'searchHistory', fixture: 'code-search', name: 'pickaxe', args: ['validateToken'] },
  { method: 'searchHistory', fixture: 'code-search', name: 'regex', args: ['valid.*Token', { regex: true }] },
  { method: 'searchHistory', fixture: 'code-search', name: 'case-max', args: ['VALIDATETOKEN', { max: 1 }] },
  { method: 'searchHistory', fixture: 'code-search', name: 'case-sensitive', args: ['VALIDATETOKEN', { caseSensitive: true }] },
  { method: 'searchHistory', fixture: 'code-search', name: 'blank', args: [''] },

  // ─── branch comparison, object explorer, project tree ────────────────────
  { method: 'compareBranches', fixture: 'merge-conflict', name: 'diverged', args: ['main', 'feature'] },
  { method: 'compareBranches', fixture: 'tags-and-releases', name: 'tags', args: ['v2.0.0', 'v1.0.2'] },
  { method: 'compareBranches', fixture: 'merge-conflict', name: 'unknown', args: ['main', 'nope'] },
  { method: 'objectRefs', fixture: 'tags-and-releases', name: 'refs' },
  { method: 'objectRefs', fixture: 'empty-repo', name: 'empty' },
  { method: 'gitObject', fixture: 'tags-and-releases', name: 'commit', args: ['HEAD'] },
  { method: 'gitObject', fixture: 'tags-and-releases', name: 'blank-is-head', args: ['   '] },
  { method: 'gitObject', fixture: 'tags-and-releases', name: 'tree', args: ['HEAD^{tree}'] },
  { method: 'gitObject', fixture: 'tags-and-releases', name: 'blob', args: ['HEAD:app.py'] },
  { method: 'gitObject', fixture: 'tags-and-releases', name: 'tag', args: ['v1.0.0'] },
  { method: 'gitObject', fixture: 'tags-and-releases', name: 'unknown', args: ['no-such-ref'] },
  { method: 'gitObject', fixture: 'binary-images-unicode', name: 'binary-blob', args: ['HEAD:assets/icon.png'] },
  { method: 'gitObject', fixture: 'conflict-radar', name: 'merge-commit', args: [':/merge chore/merged'] },
  { method: 'listDir', fixture: 'project-tree', name: 'root', args: [''] },
  { method: 'listDir', fixture: 'project-tree', name: 'default-root' },
  { method: 'listDir', fixture: 'project-tree', name: 'src', args: ['src'] },
  { method: 'listDir', fixture: 'project-tree', name: 'missing', args: ['nope'] },
  { method: 'listDir', fixture: 'binary-images-unicode', name: 'unicode', args: [''] },
  { method: 'listDirAt', fixture: 'time-machine', name: 'root', args: ['HEAD', ''] },
  { method: 'listDirAt', fixture: 'time-machine', name: 'past-subdir', args: ['HEAD~5', 'src'] },
  { method: 'listDirAt', fixture: 'time-machine', name: 'missing-dir', args: ['HEAD', 'nope'] },
  { method: 'listDirAt', fixture: 'time-machine', name: 'bad-ref', args: ['nosuchref', ''] },
  { method: 'listDirAt', fixture: 'time-machine', name: 'not-a-tree', args: ['HEAD', 'README.md'] },
  { method: 'listFiles', fixture: 'project-tree', name: 'all' },
  { method: 'listFiles', fixture: 'code-search', name: 'all' },
  { method: 'protectedBranches', fixture: 'cherry-pick', name: 'default' },
  {
    method: 'protectedBranches',
    fixture: 'cherry-pick',
    name: 'configured',
    prep: [{ git: ['config', 'gitcito.protectedbranches', ' main, release ,,dev'] }]
  },
  {
    method: 'protectedBranches',
    fixture: 'cherry-pick',
    name: 'empty',
    prep: [{ git: ['config', 'gitcito.protectedbranches', ''] }]
  },

  // ─── notes ───────────────────────────────────────────────────────────────
  { method: 'note', fixture: 'cherry-pick', name: 'none', args: [rev('HEAD')] },
  {
    method: 'note',
    fixture: 'cherry-pick',
    name: 'multiline',
    args: [rev('HEAD')],
    prep: [{ git: ['notes', 'add', '-m', 'Reviewed by Ana.', '-m', 'Shipped in build 412.', 'HEAD'] }]
  },
  { method: 'notedCommits', fixture: 'cherry-pick', name: 'none' },
  {
    method: 'notedCommits',
    fixture: 'cherry-pick',
    name: 'two',
    prep: [{ git: ['notes', 'add', '-m', 'a', 'HEAD'] }, { git: ['notes', 'add', '-m', 'b', 'HEAD~1'] }]
  },
  {
    method: 'setNote',
    fixture: 'cherry-pick',
    name: 'write',
    write: true,
    args: [rev('HEAD'), 'Reviewed by Ana.\n\nShipped in build 412 — see the incident doc.'],
    extra: [['notes', 'show', 'HEAD']]
  },
  {
    method: 'setNote',
    fixture: 'cherry-pick',
    name: 'overwrite',
    write: true,
    args: [rev('HEAD'), 'second'],
    prep: [{ git: ['notes', 'add', '-m', 'first', 'HEAD'] }],
    extra: [['notes', 'show', 'HEAD']]
  },
  {
    method: 'setNote',
    fixture: 'cherry-pick',
    name: 'blank-removes',
    write: true,
    args: [rev('HEAD'), '   '],
    prep: [{ git: ['notes', 'add', '-m', 'temporary', 'HEAD'] }],
    extra: [['notes', 'list']]
  },
  { method: 'setNote', fixture: 'cherry-pick', name: 'bad-object', write: true, args: ['nonexistent', 'x'] },
  {
    method: 'removeNote',
    fixture: 'cherry-pick',
    name: 'present',
    write: true,
    args: [rev('HEAD')],
    prep: [{ git: ['notes', 'add', '-m', 'gone soon', 'HEAD'] }],
    extra: [['notes', 'list']]
  },
  { method: 'removeNote', fixture: 'cherry-pick', name: 'missing', write: true, args: [rev('HEAD')] },
  {
    method: 'fetchNotes',
    fixture: 'cherry-pick',
    name: 'from-upstream',
    write: true,
    args: ['up'],
    prep: [{ up: 'cherry-pick' }, { gitUp: ['notes', 'add', '-m', 'from afar', 'HEAD'] }, { git: ['remote', 'add', 'up', '<UP>'] }],
    extra: [['notes', 'list'], ['notes', 'show', 'HEAD']]
  },
  { method: 'fetchNotes', fixture: 'cherry-pick', name: 'no-remote', write: true, args: ['nowhere'] },
  {
    method: 'pushNotes',
    fixture: 'cherry-pick',
    name: 'to-upstream',
    write: true,
    args: ['up'],
    prep: [
      { up: 'cherry-pick' },
      { gitUp: ['config', 'receive.denyCurrentBranch', 'ignore'] },
      { git: ['notes', 'add', '-m', 'mine', 'HEAD'] },
      { git: ['remote', 'add', 'up', '<UP>'] }
    ],
    extra: [['-C', '<UP>', 'notes', 'list'], ['-C', '<UP>', 'notes', 'show', 'HEAD']]
  },

  // ─── tags ────────────────────────────────────────────────────────────────
  {
    method: 'createTag',
    fixture: 'changelog',
    name: 'lightweight',
    write: true,
    args: ['light-1'],
    extra: [['cat-file', '-t', 'light-1']]
  },
  {
    method: 'createTag',
    fixture: 'changelog',
    name: 'annotated-at',
    write: true,
    args: ['annot-1', rev('HEAD~2'), { message: 'release notes' }],
    extra: [['tag', '-l', '--format=%(objecttype) %(contents)', 'annot-1']]
  },
  { method: 'createTag', fixture: 'changelog', name: 'exists', write: true, args: ['v1.0.0'] },
  { method: 'deleteTag', fixture: 'tags-and-releases', name: 'annotated', write: true, args: ['v1.0.0'] },
  { method: 'deleteTag', fixture: 'tags-and-releases', name: 'missing', write: true, args: ['nope'] },
  {
    method: 'pushTag',
    fixture: 'multi-remote',
    name: 'new-tag',
    write: true,
    args: ['v9.9.9'],
    prep: [...OWN_ORIGIN, { git: ['tag', 'v9.9.9'] }],
    extra: [['ls-remote', '--tags', 'origin']]
  },
  {
    method: 'pushTag',
    fixture: 'multi-remote',
    name: 'missing',
    write: true,
    args: ['nope'],
    prep: [...OWN_ORIGIN]
  },
  {
    method: 'deleteRemoteTag',
    fixture: 'multi-remote',
    name: 'pushed',
    write: true,
    args: ['v9.9.9', 'origin'],
    prep: [...OWN_ORIGIN, { git: ['tag', 'v9.9.9'] }, { git: ['push', '-q', 'origin', 'refs/tags/v9.9.9'] }],
    extra: [['ls-remote', '--tags', 'origin']]
  },
  {
    method: 'getRemoteTags',
    fixture: 'multi-remote',
    name: 'two',
    args: ['origin'],
    prep: [
      ...OWN_ORIGIN,
      { gitUp: ['tag', 'v1', 'main'] },
      { gitUp: ['-c', 'user.name=Tagger', '-c', 'user.email=tagger@example.com', 'tag', '-a', 'v2', '-m', 'two', 'main'] }
    ]
  },
  { method: 'getRemoteTags', fixture: 'multi-remote', name: 'none', args: ['origin'], prep: [...OWN_ORIGIN] },
  { method: 'getRemoteTags', fixture: 'multi-remote', name: 'bad-remote', args: ['nowhere'] },

  // ─── reset / revert / cherry-pick / undo ─────────────────────────────────
  { method: 'cherryPick', fixture: 'cherry-pick', name: 'clean', write: true, args: [rev(':/add clean-addition')] },
  {
    method: 'cherryPick',
    fixture: 'cherry-pick',
    name: 'no-commit',
    write: true,
    args: [rev(':/add clean-addition'), true]
  },
  { method: 'cherryPick', fixture: 'cherry-pick', name: 'conflict', write: true, args: [rev(':/will CONFLICT')] },
  { method: 'cherryPickMany', fixture: 'cherry-pick', name: 'none', write: true, args: [[]] },
  {
    method: 'cherryPickMany',
    fixture: 'cherry-pick',
    name: 'two',
    write: true,
    args: [[rev(':/add clean-addition'), rev(':/will CONFLICT')], true]
  },
  {
    method: 'cherryPickMany',
    fixture: 'tags-and-releases',
    name: 'in-order',
    write: true,
    args: [[rev(':/feat: add farewell'), rev(':/feat: plugin system')]]
  },
  { method: 'revertCommit', fixture: 'cherry-pick', name: 'head', write: true, args: [rev('HEAD')] },
  { method: 'revertCommit', fixture: 'cherry-pick', name: 'bad', write: true, args: ['nonexistent'] },
  { method: 'reset', fixture: 'cherry-pick', name: 'mixed', write: true, args: [rev('HEAD~1'), 'mixed'] },
  { method: 'reset', fixture: 'cherry-pick', name: 'soft', write: true, args: ['HEAD~1', 'soft'] },
  { method: 'reset', fixture: 'cherry-pick', name: 'hard-clean', write: true, args: ['HEAD~1', 'hard'] },
  {
    method: 'reset',
    fixture: 'cherry-pick',
    name: 'hard-dirty-guarded',
    write: true,
    args: ['HEAD~1', 'hard'],
    prep: [{ write: ['wip.txt', 'precious\n'] }, { write: ['log.txt', 'edited\n'] }]
  },
  {
    method: 'reset',
    fixture: 'cherry-pick',
    name: 'keep-dirty',
    write: true,
    args: ['HEAD~1', 'keep'],
    prep: [{ write: ['wip.txt', 'precious\n'] }]
  },
  { method: 'reset', fixture: 'cherry-pick', name: 'bad-ref', write: true, args: ['nonexistent', 'mixed'] },
  { method: 'undoCommit', fixture: 'cherry-pick', name: 'non-root', write: true },
  { method: 'undoCommit', fixture: 'line-staging', name: 'root', write: true },
  {
    method: 'undoCommit',
    fixture: 'line-staging',
    name: 'root-with-deletion',
    write: true,
    prep: [{ git: ['rm', '-q', 'README.md'] }]
  },
  {
    method: 'undoCommit',
    fixture: 'cherry-pick',
    name: 'detached',
    write: true,
    prep: [{ git: ['checkout', '-q', '--detach', 'HEAD'] }]
  },
  { method: 'undoCommit', fixture: 'empty-repo', name: 'unborn', write: true },
  {
    method: 'restoreUndoneCommit',
    fixture: 'cherry-pick',
    name: 'after-undo',
    write: true,
    args: [rev('HEAD')],
    prep: [{ git: ['reset', '-q', '--mixed', 'HEAD~1'] }, { write: ['wip.txt', 'keep\n'] }]
  },
  { method: 'restoreUndoneCommit', fixture: 'cherry-pick', name: 'bad', write: true, args: ['nonexistent'] },

  // ─── sparse checkout ─────────────────────────────────────────────────────
  { method: 'sparseCheckoutInfo', fixture: 'time-machine', name: 'disabled' },
  {
    method: 'sparseCheckoutInfo',
    fixture: 'time-machine',
    name: 'enabled',
    prep: [{ git: ['sparse-checkout', 'set', '--cone', 'src', 'docs'] }]
  },
  { method: 'sparseCheckoutInfo', fixture: 'empty-repo', name: 'unborn' },
  {
    method: 'sparseCheckoutSet',
    fixture: 'time-machine',
    name: 'src',
    write: true,
    args: [['src']],
    extra: [['sparse-checkout', 'list'], ['config', '--get', 'core.sparseCheckoutCone']]
  },
  {
    method: 'sparseCheckoutDisable',
    fixture: 'time-machine',
    name: 'after-set',
    write: true,
    prep: [{ git: ['sparse-checkout', 'set', '--cone', 'src'] }],
    extra: [['config', '--get', 'core.sparseCheckout']]
  },

  // ─── protected branches ──────────────────────────────────────────────────
  {
    method: 'setProtectedBranches',
    fixture: 'cherry-pick',
    name: 'trimmed',
    write: true,
    args: [[' main ', '', 'release/*']],
    extra: [['config', '--get', 'gitcito.protectedbranches']]
  },
  {
    method: 'setProtectedBranches',
    fixture: 'cherry-pick',
    name: 'none',
    write: true,
    args: [[]],
    extra: [['config', '--get', 'gitcito.protectedbranches']]
  },

  // ─── history purge ───────────────────────────────────────────────────────
  { method: 'historyPaths', fixture: 'leaked-secret', name: 'default' },
  { method: 'historyPaths', fixture: 'leaked-secret', name: 'capped', args: [2] },
  { method: 'historyPaths', fixture: 'leaked-secret', name: 'zero', args: [0] },
  { method: 'historyPurgePreview', fixture: 'leaked-secret', name: 'secret', args: [[SECRET]] },
  { method: 'historyPurgePreview', fixture: 'leaked-secret', name: 'none', args: [['  ']] },
  { method: 'historyPurgePreview', fixture: 'leaked-secret', name: 'untouched', args: [['does/not/exist.env']] },
  {
    method: 'historyPurgePreview',
    fixture: 'leaked-secret',
    name: 'dirty',
    args: [[SECRET]],
    prep: [{ write: ['app.js', 'dirty\n'] }]
  },
  {
    method: 'historyPurgePreview',
    fixture: 'merge-conflict',
    name: 'merging',
    args: [['hello.txt']],
    prep: [{ git: ['merge', '--no-commit', 'feature'], mayFail: true }]
  },
  {
    method: 'historyPurge',
    fixture: 'leaked-secret',
    name: 'secret',
    write: true,
    args: [[SECRET]],
    extra: [['config', '--get-regexp', '^gitcito\\.prepurge\\.']]
  },
  { method: 'historyPurge', fixture: 'leaked-secret', name: 'none', write: true, args: [[]] },
  { method: 'historyPurge', fixture: 'leaked-secret', name: 'untouched', write: true, args: [['does/not/exist.env']] },
  {
    method: 'historyPurge',
    fixture: 'leaked-secret',
    name: 'dirty',
    write: true,
    args: [[SECRET]],
    prep: [{ write: ['app.js', 'dirty\n'] }]
  },
  { method: 'historyPurgeBackups', fixture: 'leaked-secret', name: 'none' },
  {
    method: 'historyPurgeBackups',
    fixture: 'leaked-secret',
    name: 'two',
    prep: [
      ...FAKE_BACKUP,
      { git: ['update-ref', 'refs/gitcito/pre-purge/1600000000/heads/main', 'HEAD'] }
    ]
  },
  {
    method: 'historyPurgeRestore',
    fixture: 'leaked-secret',
    name: 'restore',
    write: true,
    args: ['refs/gitcito/pre-purge/1700000000'],
    prep: [...FAKE_BACKUP]
  },
  {
    method: 'historyPurgeRestore',
    fixture: 'leaked-secret',
    name: 'not-a-backup',
    write: true,
    args: ['refs/heads/main']
  },
  {
    method: 'historyPurgeRestore',
    fixture: 'leaked-secret',
    name: 'empty',
    write: true,
    args: ['refs/gitcito/pre-purge/1']
  },
  {
    method: 'historyPurgeDropBackup',
    fixture: 'leaked-secret',
    name: 'drop',
    write: true,
    args: ['refs/gitcito/pre-purge/1700000000'],
    prep: [...FAKE_BACKUP],
    extra: [['config', '--get-regexp', '^gitcito\\.prepurge\\.']]
  },
  {
    method: 'historyPurgeDropBackup',
    fixture: 'leaked-secret',
    name: 'not-a-backup',
    write: true,
    args: ['refs/heads/main']
  },

  // ─── pull request preview ────────────────────────────────────────────────
  { method: 'resolvePrRef', fixture: 'merge-conflict', name: 'github', write: true, args: ['up', 7], prep: PR_UPSTREAM },
  { method: 'resolvePrRef', fixture: 'merge-conflict', name: 'gitlab', write: true, args: ['up', 3], prep: PR_UPSTREAM },
  { method: 'resolvePrRef', fixture: 'merge-conflict', name: 'none', write: true, args: ['up', 999], prep: PR_UPSTREAM },
  { method: 'resolvePrRef', fixture: 'merge-conflict', name: 'bad-remote', write: true, args: ['nowhere', 1] },
  {
    method: 'previewRef',
    fixture: 'merge-conflict',
    name: 'checkout',
    write: true,
    args: ['up', 'refs/pull/7/head', 'checkout', 'pr/7'],
    prep: PR_UPSTREAM
  },
  {
    method: 'previewRef',
    fixture: 'merge-conflict',
    name: 'checkout-reuses-branch',
    write: true,
    args: ['up', 'refs/pull/7/head', 'checkout', 'pr/7'],
    prep: [...PR_UPSTREAM, { git: ['branch', 'pr/7', 'main'] }]
  },
  {
    method: 'previewRef',
    fixture: 'merge-conflict',
    name: 'merge-clean',
    write: true,
    args: ['up', 'refs/merge-requests/3/head', 'merge'],
    prep: PR_UPSTREAM
  },
  {
    method: 'previewRef',
    fixture: 'merge-conflict',
    name: 'merge-conflict',
    write: true,
    args: ['up', 'refs/pull/7/head', 'merge'],
    prep: PR_UPSTREAM
  },
  {
    method: 'previewRef',
    fixture: 'merge-conflict',
    name: 'checkout-without-branch',
    write: true,
    args: ['up', 'refs/pull/7/head', 'checkout'],
    prep: PR_UPSTREAM
  },
  {
    method: 'previewRef',
    fixture: 'merge-conflict',
    name: 'missing-ref',
    write: true,
    args: ['up', 'refs/pull/99/head', 'merge'],
    prep: PR_UPSTREAM
  },

  // ─── insights ────────────────────────────────────────────────────────────
  { method: 'repoStats', fixture: 'insights', name: 'all' },
  { method: 'repoStats', fixture: 'insights', name: 'since', args: [36500] },
  { method: 'repoStats', fixture: 'empty-repo', name: 'empty' },
  { method: 'repoInsights', fixture: 'insights', name: 'all' },
  { method: 'repoInsights', fixture: 'time-machine', name: 'renames', maskDates: true },
  { method: 'repoInsights', fixture: 'empty-repo', name: 'empty' },
  { method: 'contributors', fixture: 'insights', name: 'all' },
  { method: 'contributors', fixture: 'insights', name: 'capped', args: [2] },
  { method: 'cosmosData', fixture: 'insights', name: 'capped', args: [3] },
  { method: 'cosmosData', fixture: 'time-machine', name: 'renames' },
  { method: 'cosmosData', fixture: 'tags-and-releases', name: 'linear', args: [5] },
  { method: 'timelapseData', fixture: 'time-machine', name: 'all' },
  { method: 'timelapseData', fixture: 'time-machine', name: 'capped', args: [3] },
  { method: 'timelapseData', fixture: 'empty-repo', name: 'empty' },
  { method: 'generateChangelog', fixture: 'changelog', name: 'since-tag', maskDates: true },
  { method: 'generateChangelog', fixture: 'changelog', name: 'version', args: [{ version: 'v1.1.0' }], maskDates: true },
  {
    method: 'generateChangelog',
    fixture: 'changelog',
    name: 'range',
    args: [{ from: 'HEAD~3', to: 'HEAD~1' }],
    maskDates: true
  },
  { method: 'generateChangelog', fixture: 'tags-and-releases', name: 'tags', maskDates: true },
  { method: 'generateChangelog', fixture: 'empty-repo', name: 'empty', maskDates: true }
]

// ─── helpers ────────────────────────────────────────────────────────────────

const git = (repo: string, args: string[]): string =>
  execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 1 << 28 })

/** Output of a git command, or `<exit N>` + its stderr when it fails — the state snapshot keeps both. */
function gitOutcome(repo: string, args: string[]): string {
  try {
    return git(repo, args)
  } catch (err) {
    const e = err as { status?: number; stderr?: string }
    return `<exit ${e.status ?? '?'}>${e.stderr ?? ''}`
  }
}

const ISO_RE = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z/g
const HEX_WORD_RE = /\b[0-9a-f]{7,40}\b/g

/** Content labels for every commit and tag object in the repository. */
function objectLabels(repo: string): Map<string, string> {
  const listing = git(repo, ['cat-file', '--batch-all-objects', '--batch-check=%(objectname) %(objecttype)'])
  const wanted = listing
    .split('\n')
    .map((l) => l.split(' '))
    .filter(([, t]) => t === 'commit' || t === 'tag')
    .map(([sha]) => sha)
  const objects = new Map<string, { type: string; body: string }>()
  if (wanted.length) {
    const buf = execFileSync('git', ['-C', repo, 'cat-file', '--batch'], {
      input: wanted.join('\n') + '\n',
      maxBuffer: 1 << 30
    })
    let pos = 0
    while (pos < buf.length) {
      const nl = buf.indexOf(0x0a, pos)
      const [sha, type, size] = buf.subarray(pos, nl).toString('utf8').split(' ')
      const start = nl + 1
      const end = start + Number(size)
      objects.set(sha, { type, body: buf.subarray(start, end).toString('utf8') })
      pos = end + 1
    }
  }
  const labels = new Map<string, string>()
  const parsed = new Map<string, { headers: [string, string][]; message: string }>()
  const parse = (sha: string): { headers: [string, string][]; message: string } => {
    let p = parsed.get(sha)
    if (!p) {
      const body = objects.get(sha)!.body
      const cut = body.indexOf('\n\n')
      const head = cut < 0 ? body : body.slice(0, cut)
      const message = cut < 0 ? '' : body.slice(cut + 2)
      const headers: [string, string][] = []
      for (const line of head.split('\n')) {
        if (!line || line.startsWith(' ')) continue
        const sp = line.indexOf(' ')
        headers.push(sp < 0 ? [line, ''] : [line.slice(0, sp), line.slice(sp + 1)])
      }
      p = { headers, message }
      parsed.set(sha, p)
    }
    return p
  }
  const deps = (sha: string): string[] => {
    const { headers } = parse(sha)
    const keys = objects.get(sha)!.type === 'commit' ? ['parent'] : ['object']
    return headers.filter(([k, v]) => keys.includes(k) && objects.has(v)).map(([, v]) => v)
  }
  const noDate = (ident: string): string => ident.replace(/ \d+ [+-]\d{4}$/, '')
  const normMsg = (msg: string): string => msg.replace(ISO_RE, '<ISO>').replace(HEX_WORD_RE, '<H>')
  const ref = (v: string): string => labels.get(v) ?? v
  const compute = (sha: string): string => {
    const { headers, message } = parse(sha)
    const get = (k: string): string[] => headers.filter(([h]) => h === k).map(([, v]) => v)
    let key: string
    let prefix: string
    if (objects.get(sha)!.type === 'commit') {
      prefix = 'C'
      key = [
        'commit',
        get('tree').join(','),
        get('parent').map(ref).join(','),
        get('author').map(noDate).join(','),
        get('committer').map(noDate).join(','),
        normMsg(message)
      ].join('\n')
    } else {
      prefix = 'T'
      key = [
        'tag',
        get('object').map(ref).join(','),
        get('type').join(','),
        get('tag').join(','),
        get('tagger').map(noDate).join(','),
        normMsg(message)
      ].join('\n')
    }
    return prefix + createHash('sha1').update(key, 'utf8').digest('hex').slice(0, 10)
  }
  // Parents first, without recursion: a deep history must not blow the stack.
  for (const start of objects.keys()) {
    if (labels.has(start)) continue
    const stack = [start]
    while (stack.length) {
      const top = stack[stack.length - 1]
      if (labels.has(top)) {
        stack.pop()
        continue
      }
      const missing = deps(top).filter((d) => !labels.has(d))
      if (missing.length) {
        stack.push(...missing)
        continue
      }
      labels.set(top, compute(top))
      stack.pop()
    }
  }
  return labels
}

interface NormCtx {
  paths: [string, string][]
  labels: Map<string, string>
  sorted: string[]
  maskDates: boolean
}

function makeCtx(repo: string, up: string | null, maskDates: boolean): NormCtx {
  const paths: [string, string][] = []
  const add = (p: string, token: string): void => {
    paths.push([p, token])
    try {
      const real = realpathSync(p)
      if (real !== p) paths.push([real, token])
    } catch {
      /* gone */
    }
  }
  add(repo, '<REPO>')
  if (up) add(up, '<UP>')
  add(tmpdir(), '<TMP>')
  add(homedir(), '<HOME>')
  // Longest first, so <REPO> wins over the <TMP> it lives in.
  paths.sort((a, b) => b[0].length - a[0].length)
  const labels = objectLabels(repo)
  if (up) for (const [k, v] of objectLabels(up)) if (!labels.has(k)) labels.set(k, v)
  return { paths, labels, sorted: [...labels.keys()].sort(), maskDates }
}

function normString(s: string, c: NormCtx): string {
  for (const [p, token] of c.paths) s = s.split(p).join(token)
  s = s
    .replace(/refs\/gitcito\/wip\/\d+/g, 'refs/gitcito/wip/<N>')
    .replace(/pre-purge\/\d+/g, 'pre-purge/<N>')
    .replace(/prepurge\.at\d+/g, 'prepurge.at<N>')
    .replace(ISO_RE, '<ISO>')
    .replace(/\b\d{9,10} [+-]\d{4}\b/g, '<TS> <TZ>')
    .replace(/\b1[6-9]\d{8}\b/g, '<TS>')
  if (c.maskDates) s = s.replace(/\b\d{4}-\d{2}-\d{2}\b/g, '<DATE>')
  return s.replace(HEX_WORD_RE, (m) => {
    const full = c.labels.get(m)
    if (full) return `<${full}>`
    if (m.length === 40) return m
    // Unique abbreviation of a known commit or tag.
    let lo = 0
    let hi = c.sorted.length
    while (lo < hi) {
      const mid = (lo + hi) >> 1
      if (c.sorted[mid] < m) lo = mid + 1
      else hi = mid
    }
    const hit = c.sorted[lo]
    if (!hit || !hit.startsWith(m)) return m
    const next = c.sorted[lo + 1]
    if (next && next.startsWith(m)) return m
    return `<${c.labels.get(hit)}~>`
  })
}

function normalize(v: Json, c: NormCtx): Json {
  if (typeof v === 'string') return normString(v, c)
  if (typeof v === 'number') return Number.isInteger(v) && v >= 1e9 && v < 1e10 ? '<TS>' : v
  if (Array.isArray(v)) return v.map((x) => normalize(x, c))
  if (v && typeof v === 'object') {
    const out: { [k: string]: Json } = {}
    for (const [k, x] of Object.entries(v)) out[normString(k, c)] = normalize(x, c)
    return out
  }
  return v
}

function snapshot(repo: string, up: string | null, extra: string[][] | undefined, c: NormCtx): Json {
  // refs/notes is left out: a notes tree is named by the commits it annotates,
  // so its commits change with every rebuild. Notes cases read them back
  // through `extra` (`notes list`, `notes show`) instead.
  const raw: { [k: string]: Json } = {
    status: gitOutcome(repo, ['status', '--porcelain=v2']),
    refs: gitOutcome(repo, ['for-each-ref'])
      .split('\n')
      .filter((l) => !l.includes('\trefs/notes/'))
      .join('\n'),
    log: gitOutcome(repo, ['log', '--format=%H%x00%P%x00%s', '--exclude=refs/notes/*', '--all']),
    stash: gitOutcome(repo, ['stash', 'list']),
    index: gitOutcome(repo, ['ls-files', '-s'])
  }
  const sub = (s: string): string => (up ? s.split('<UP>').join(up) : s)
  if (extra) raw.extra = extra.map((args) => ({ args, out: gitOutcome(repo, args.map(sub)) }))
  const state = normalize(raw, c) as { [k: string]: Json }
  // The order of unrelated commits follows their dates, which a rebuilt
  // playground does not keep: compare the set of lines (sorted by code point).
  state.log = (state.log as string)
    .split('\n')
    .sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))
    .join('\n')
  return state
}

function runPrep(repo: string, prep: Prep[] | undefined): string | null {
  let up: string | null = null
  const sub = (s: string): string => (up ? s.split('<UP>').join(up) : s)
  for (const step of prep ?? []) {
    if ('git' in step) {
      try {
        git(repo, step.git.map(sub))
      } catch (err) {
        if (!step.mayFail) throw err
      }
    } else if ('gitUp' in step) {
      try {
        git(up!, step.gitUp.map(sub))
      } catch (err) {
        if (!step.mayFail) throw err
      }
    } else if ('write' in step) {
      const file = join(repo, step.write[0])
      mkdirSync(dirname(file), { recursive: true })
      writeFileSync(file, step.write[1])
    } else if ('writeBig' in step) {
      writeFileSync(join(repo, step.writeBig[0]), Buffer.alloc(step.writeBig[1], 'a'))
    } else if ('writeUp' in step) {
      writeFileSync(join(up!, step.writeUp[0]), step.writeUp[1])
    } else if ('up' in step) {
      up = cloneFixture(step.up)
    }
  }
  return up
}

function resolveArgs(args: Json[], repo: string, up: string | null): unknown[] {
  const walk = (v: Json): unknown => {
    if (Array.isArray(v)) return v.map(walk)
    if (v && typeof v === 'object') {
      if ('$rev' in v) return git(repo, ['rev-parse', String(v.$rev)]).trim()
      if ('$revUp' in v) return git(up!, ['rev-parse', String(v.$revUp)]).trim()
      return v
    }
    if (typeof v === 'string' && up) return v.split('<UP>').join(up)
    return v
  }
  return args.map(walk)
}

const ctxCache = new Map<string, NormCtx>()

async function runCase(cs: Case): Promise<Json> {
  const isolated = cs.write || !!cs.prep?.length
  const repo = isolated ? cloneFixture(cs.fixture) : repoPath(cs.fixture)
  const up = runPrep(repo, cs.prep)
  const args = resolveArgs(cs.args ?? [], repo, up)

  let outcome: { result: Json } | { error: string }
  try {
    const fn = (gitService as unknown as Record<string, (...a: unknown[]) => Promise<unknown>>)[cs.method]
    const value = await fn(repo, ...args)
    outcome = { result: value === undefined ? null : (JSON.parse(JSON.stringify(value)) as Json) }
  } catch (err) {
    outcome = { error: err instanceof Error ? err.message : String(err) }
  }

  let c: NormCtx
  if (isolated) c = makeCtx(repo, up, !!cs.maskDates)
  else {
    const key = `${cs.fixture}\0${cs.maskDates ? 1 : 0}`
    c = ctxCache.get(key) ?? makeCtx(repo, null, !!cs.maskDates)
    ctxCache.set(key, c)
  }
  const golden: { [k: string]: Json } = {
    method: cs.method,
    fixture: cs.fixture,
    case: cs.name,
    write: !!cs.write,
    args: cs.args ?? []
  }
  if (cs.prep) golden.prep = cs.prep as unknown as Json
  if (cs.extra) golden.extra = cs.extra
  if (cs.maskDates) golden.maskDates = true
  if ('result' in outcome) golden.result = normalize(outcome.result, c)
  else golden.error = normString(outcome.error, c)
  if (cs.write || cs.extra) golden.state = snapshot(repo, up, cs.extra, c)

  // semanticDiff moves to the renderer: Go returns the two texts, and the
  // renderer runs semanticCompare over them. Record what TS concluded too, so
  // the renderer port can check itself against the same answer.
  if (cs.method === 'semanticDiff' && 'result' in outcome) {
    golden.tsSemantic = golden.result
    golden.result = normalize(semanticSides(repo, args) as unknown as Json, c)
  }
  return golden
}

/**
 * The read half of TS semanticDiff, verbatim: what the Go method returns now.
 * (The other half — semanticCompare — stays TypeScript, in the renderer.)
 */
function semanticSides(repo: string, args: unknown[]): { oldText: string; newText: string } {
  const [file, oldSide, newSide] = args as [string, { kind: string; ref?: string }, { kind: string; ref?: string }]
  const read = (spec: { kind: string; ref?: string }): string => {
    if (spec.kind === 'empty') return ''
    if (spec.kind === 'worktree') {
      try {
        return readFileSync(join(repo, file), 'utf-8')
      } catch {
        return ''
      }
    }
    const r = spec.kind === 'index' ? `:${file}` : `${spec.ref}:${file}`
    try {
      return git(repo, ['show', r])
    } catch {
      return ''
    }
  }
  return { oldText: read(oldSide), newText: read(newSide) }
}

function fileFor(cs: Case): string {
  return join(GOLDENS, cs.method, `${cs.fixture}-${cs.name}.json`)
}

describe('slice B goldens', () => {
  it('every case has a unique file', () => {
    const seen = new Set<string>()
    for (const cs of CASES) {
      expect(seen.has(fileFor(cs)), fileFor(cs)).toBe(false)
      seen.add(fileFor(cs))
    }
  })

  for (const cs of CASES) {
    it(`${cs.method} ${cs.fixture}-${cs.name}`, async () => {
      const golden = await runCase(cs)
      const file = fileFor(cs)
      if (WRITE) {
        mkdirSync(dirname(file), { recursive: true })
        writeFileSync(file, JSON.stringify(golden, null, 2) + '\n')
      }
      expect(existsSync(file), `missing golden ${file} — run with PORT_WRITE_GOLDENS=1`).toBe(true)
      expect(golden).toEqual(JSON.parse(readFileSync(file, 'utf8')))
    }, 60_000)
  }

  it('no stray golden files for slice B methods', () => {
    const expected = new Set(CASES.map(fileFor))
    for (const method of new Set(CASES.map((c) => c.method))) {
      const dir = join(GOLDENS, method)
      if (!existsSync(dir)) continue
      for (const f of readdirSync(dir)) expect(expected.has(join(dir, f)), join(dir, f)).toBe(true)
    }
  })
})
