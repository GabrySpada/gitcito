// Goldens for wave-1 slice C of the Go port (commit/amend, branches, merge,
// rebase, stacks, git-flow, rerere, bisect, patches, signing, fs* actions).
//
// Every case calls the TypeScript gitService method against a playground
// fixture and records what it returned (or the error it threw) in
// internal/gitsvc/testdata/goldens/<method>/<fixture>-<case>.json. WRITE methods
// run on a cloneFixture() copy and also record a snapshot of the repository
// afterwards. internal/gitsvc/c_golden_test.go replays every file against the
// Go port with the same setup and the same normalization.
//
// PORT_WRITE_GOLDENS=1 (re)writes the files. Without it this test re-runs the
// TypeScript and asserts it still produces exactly what the files say, so the
// goldens stay honest for as long as the TypeScript exists.
//
// Normalization (mirrored by the Go replay):
//   - the repository path → <REPO>, a per-case scratch dir → <OUT>, the
//     playground root → <PLAYGROUND>, $HOME → <HOME>, os.tmpdir() → <TMP>;
//   - commit SHAs (full, and abbreviations of known commits) → a label built
//     from the commit's subject and its parents' labels. Most scenarios do not
//     pin GIT_*_DATE, so SHAs change on every playground rebuild; the label
//     does not. Annotated tag objects → <TAG:name>. Blob/tree SHAs are content
//     hashes and stay as they are;
//   - wall-clock values: ISO-8601 dates (%aI) → <DATE>, a mail `Date:` header
//     → `Date: <DATE>`, backup-branch stamps → <STAMP>, the applyPatch temp
//     file's millisecond suffix → <N>, and format-patch's git-version
//     signature → <GITVERSION>.
import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { gitService, gitMethodIsRead, redactCredentials, stripAnsi } from '../../src/main/git'
import { prepareRepoFileActions } from '../../src/main/repoFileActions'
import { cloneFixture, cleanupFixtures } from '../fixtures'
import { PLAYGROUND, repoPath } from '../helpers'

const GOLDENS = resolve(__dirname, '../../internal/gitsvc/testdata/goldens')
const WRITE = !!process.env.PORT_WRITE_GOLDENS

// ─── Case model (shared with the Go replay — keep the two in step) ─────────

/** A string arg may hold <REPO> / <OUT>; an object `{ $var }` is a setup result. */
type Arg = unknown

type Step =
  /** `git -C <REPO> …args` (or plain `git …args` with `noRepo`). */
  | { git: string[]; noRepo?: boolean; allowFail?: boolean }
  /** Write a file under <REPO> (or under <OUT> with `out`). */
  | { write: string; content: string; out?: boolean; mode?: number }
  /** Create a directory under <REPO> (or <OUT>). */
  | { mkdir: string; out?: boolean }
  /** Call a slice-C gitService method; its result is kept as `$var: as`. */
  | { call: string; args: Arg[]; as?: string; allowFail?: boolean }

interface Case {
  method: string
  fixture: string
  name: string
  setup?: Step[]
  args: Arg[]
  /** Build the args from the prepared clone (still stored literally). */
  prepare?: (repo: string) => Promise<Arg[]>
  /** Collect the `bisect:output` stream. */
  events?: boolean
}

const ID = ['-c', 'user.name=Setup', '-c', 'user.email=setup@example.com']
const commitAll = (msg: string): Step[] => [{ git: ['add', '-A'] }, { git: [...ID, 'commit', '-qm', msg] }]

/** Two branches editing one line of shared.txt — what -X can decide. */
const contentConflict: Step[] = [
  { write: 'shared.txt', content: 'alpha\nbeta\ngamma\n' },
  ...commitAll('base'),
  { git: ['checkout', '-q', '-b', 'incoming'] },
  { write: 'shared.txt', content: 'alpha\ntheir beta\ngamma\n' },
  ...commitAll('their edit'),
  { git: ['checkout', '-q', '-b', 'mine', 'HEAD~1'] },
  { write: 'shared.txt', content: 'alpha\nour beta\ngamma\n' },
  ...commitAll('our edit')
]

/** A side branch that merges cleanly into a HEAD that moved on too. */
const cleanBranch: Step[] = [
  { git: ['checkout', '-q', '-b', 'addition'] },
  { write: 'added.txt', content: 'a file nothing else touches\n' },
  ...commitAll('add a file'),
  { git: ['checkout', '-q', '-'] },
  { write: 'here.txt', content: 'moved on over here\n' },
  ...commitAll('move on')
]

/** A writable bare remote `pushme` in <OUT>, with every local branch published. */
const pushRemote: Step[] = [
  { git: ['init', '-q', '--bare', '<OUT>/origin.git'], noRepo: true },
  { git: ['remote', 'add', 'pushme', '<OUT>/origin.git'] },
  { git: ['push', '-q', 'pushme', '--all'] },
  { git: ['fetch', '-q', 'pushme'] }
]

const bisectSeed: Step[] = [
  { call: 'bisectStart', args: ['<REPO>'] },
  { call: 'bisectMark', args: ['<REPO>', 'bad'] },
  { call: 'bisectMark', args: ['<REPO>', 'good', ':/feat: discount function'] }
]

const mergeConflictSetup: Step[] = [{ git: ['merge', 'feature'], allowFail: true }]

/** feature ahead of origin/feature (diverged-checkout). */
const aheadSetup: Step[] = [
  { git: ['checkout', '-q', 'feature'] },
  { git: ['reset', '-q', '--hard', 'origin/feature'] },
  { write: 'local-only.ts', content: 'export const localOnly = 1\n' },
  ...commitAll('feat: unpushed work'),
  { git: ['checkout', '-q', 'main'] }
]
/** feature strictly behind origin/feature (diverged-checkout). */
const behindSetup: Step[] = [
  { git: ['checkout', '-q', 'feature'] },
  { git: ['reset', '-q', '--hard', 'origin/feature~1'] },
  { git: ['checkout', '-q', 'main'] }
]

/** Two stack stops appending to one file: swapping them cannot replay. */
const conflictingStack: Step[] = [
  { git: ['checkout', '-q', 'main'] },
  { git: ['checkout', '-q', '-b', 'note-1'] },
  { write: 'NOTES.md', content: 'one\n' },
  ...commitAll('note one'),
  { git: ['checkout', '-q', '-b', 'note-2'] },
  { write: 'NOTES.md', content: 'one\ntwo\n' },
  ...commitAll('note two'),
  { git: ['config', 'branch.note-1.gitcitoparent', 'main'] },
  { git: ['config', 'branch.note-2.gitcitoparent', 'note-1'] }
]

/** Resolve a rerere-tracked conflict by hand and commit it. */
const rerereFirstPass = (answer: string): Step[] => [
  { call: 'setRerere', args: ['<REPO>', { enabled: true }, 'repo'] },
  { call: 'merge', args: ['<REPO>', 'feature'], allowFail: true },
  { write: 'greeting.txt', content: answer },
  { write: 'app.js', content: answer },
  { write: 'units_service.dart', content: answer },
  { write: 'release-notes.md', content: answer },
  { write: 'big-log.txt', content: answer },
  { git: ['add', '-A'] },
  { git: [...ID, 'commit', '-q', '--no-edit'] },
  { git: ['reset', '-q', '--hard', 'HEAD~1'] }
]

const NEW_FILE_PATCH = [
  'diff --git a/patched.txt b/patched.txt',
  'new file mode 100644',
  'index 0000000..3b18e51',
  '--- /dev/null',
  '+++ b/patched.txt',
  '@@ -0,0 +1 @@',
  '+hello world',
  ''
].join('\n')

const MBOX_PATCH = [
  'From 1111111111111111111111111111111111111111 Mon Sep 17 00:00:00 2001',
  'From: Patch Author <patch@example.com>',
  'Date: Mon, 1 Jan 2024 00:00:00 +0000',
  'Subject: [PATCH] feat: add patched file',
  '',
  '---',
  ' patched.txt | 1 +',
  ' 1 file changed, 1 insertion(+)',
  ' create mode 100644 patched.txt',
  '',
  NEW_FILE_PATCH + '-- ',
  '2.43.0',
  ''
].join('\n')

const BAD_CONTEXT_PATCH = [
  'diff --git a/discount.js b/discount.js',
  'index 1234567..89abcde 100644',
  '--- a/discount.js',
  '+++ b/discount.js',
  '@@ -1,3 +1,3 @@',
  ' this line is not there',
  '-neither is this one',
  '+so nothing applies',
  ' and the context is invented',
  ''
].join('\n')

const sha256 = (s: string): string => createHash('sha256').update(s, 'utf8').digest('hex')

const cases: Case[] = [
  // ── commit / amend / messages ──────────────────────────────────────────
  { method: 'amendCommitMessage', fixture: 'bisect-bug', name: 'reword', args: ['<REPO>', 'fix: reworded head\n\nwith a body'] },
  { method: 'amendCommitMessage', fixture: 'empty-repo', name: 'unborn', args: ['<REPO>', 'nothing to amend'] },
  {
    method: 'commit', fixture: 'bisect-bug', name: 'new',
    setup: [{ write: 'scratch.txt', content: 'hello\n' }, { git: ['add', 'scratch.txt'] }],
    args: ['<REPO>', 'test: add scratch file']
  },
  {
    method: 'commit', fixture: 'bisect-bug', name: 'amend-keeps-author',
    setup: [
      { write: 'theirs.txt', content: 'their work\n' }, { git: ['add', 'theirs.txt'] },
      { git: ['-c', 'user.name=Colleague', '-c', 'user.email=colleague@test', 'commit', '-qm', 'feat: theirs'] },
      { write: 'mine.txt', content: 'my work\n' }, { git: ['add', 'mine.txt'] }
    ],
    args: ['<REPO>', 'feat: mine', true]
  },
  {
    method: 'commit', fixture: 'bisect-bug', name: 'amend-reset-author',
    setup: [
      { write: 'theirs.txt', content: 'their work\n' }, { git: ['add', 'theirs.txt'] },
      { git: ['-c', 'user.name=Colleague', '-c', 'user.email=colleague@test', 'commit', '-qm', 'feat: theirs'] }
    ],
    args: ['<REPO>', 'feat: mine', true, true]
  },
  // simple-git reads exit 1 with a silent stderr as success: nothing staged is not an error.
  { method: 'commit', fixture: 'bisect-bug', name: 'nothing-staged', args: ['<REPO>', 'empty'] },
  {
    method: 'commit', fixture: 'bisect-bug', name: 'empty-message',
    setup: [{ write: 'scratch.txt', content: 'x\n' }, { git: ['add', 'scratch.txt'] }],
    args: ['<REPO>', '']
  },
  { method: 'commit', fixture: 'empty-repo', name: 'root-commit', args: ['<REPO>', 'first commit'] },
  {
    method: 'commitFixup', fixture: 'interactive-rebase', name: 'basic',
    setup: [{ write: 'fix.txt', content: 'fix\n' }, { git: ['add', 'fix.txt'] }],
    args: ['<REPO>', 'HEAD~1']
  },
  { method: 'commitFixup', fixture: 'interactive-rebase', name: 'nothing-staged', args: ['<REPO>', 'HEAD~1'] },
  { method: 'getCommitMessage', fixture: 'bisect-bug', name: 'head', args: ['<REPO>', 'HEAD'] },
  { method: 'getCommitMessage', fixture: 'collaborators', name: 'co-authored', args: ['<REPO>', 'HEAD'] },
  { method: 'getCommitMessage', fixture: 'bisect-bug', name: 'missing', args: ['<REPO>', 'nope'] },
  { method: 'commitAuthor', fixture: 'bisect-bug', name: 'head', args: ['<REPO>', 'HEAD'] },
  { method: 'commitAuthor', fixture: 'empty-repo', name: 'unborn', args: ['<REPO>', 'HEAD'] },
  { method: 'commitAuthor', fixture: 'collaborators', name: 'first', args: ['<REPO>', 'HEAD~3'] },
  { method: 'resolveRev', fixture: 'bisect-bug', name: 'relative', args: ['<REPO>', 'HEAD~2'] },
  { method: 'resolveRev', fixture: 'bisect-bug', name: 'missing', args: ['<REPO>', 'nope'] },
  { method: 'resolveRev', fixture: 'tags-and-releases', name: 'annotated-tag', args: ['<REPO>', 'v1.0.0'] },
  { method: 'resolveRev', fixture: 'bisect-bug', name: 'tree-is-not-a-commit', args: ['<REPO>', 'HEAD^{tree}'] },
  { method: 'commitSummary', fixture: 'bisect-bug', name: 'head', args: ['<REPO>', 'HEAD'] },
  { method: 'commitSummary', fixture: 'collaborators', name: 'body', args: ['<REPO>', 'HEAD'] },
  { method: 'commitSummary', fixture: 'bisect-bug', name: 'missing', args: ['<REPO>', 'nope'] },
  { method: 'commitTemplate', fixture: 'commit-template', name: 'relative', args: ['<REPO>'] },
  { method: 'commitTemplate', fixture: 'bisect-bug', name: 'none', args: ['<REPO>'] },
  {
    method: 'commitTemplate', fixture: 'bisect-bug', name: 'missing-file',
    setup: [{ git: ['config', 'commit.template', 'missing.txt'] }], args: ['<REPO>']
  },
  {
    method: 'commitTemplate', fixture: 'bisect-bug', name: 'absolute',
    setup: [{ write: 'tpl.txt', content: 'subject\n\n# body\n', out: true }, { git: ['config', 'commit.template', '<OUT>/tpl.txt'] }],
    args: ['<REPO>']
  },
  { method: 'signingConfig', fixture: 'bisect-bug', name: 'default', args: ['<REPO>'] },
  {
    method: 'signingConfig', fixture: 'bisect-bug', name: 'ssh',
    setup: [{ git: ['config', 'commit.gpgsign', 'true'] }, { git: ['config', 'gpg.format', 'ssh'] }, { git: ['config', 'user.signingkey', '~/.ssh/id.pub'] }],
    args: ['<REPO>']
  },
  { method: 'setSigningConfig', fixture: 'bisect-bug', name: 'all', args: ['<REPO>', { sign: true, format: 'ssh', key: 'key-1' }] },
  {
    method: 'setSigningConfig', fixture: 'bisect-bug', name: 'unset-key',
    setup: [{ git: ['config', 'user.signingkey', 'old'] }], args: ['<REPO>', { sign: false, key: '' }]
  },
  { method: 'setSigningConfig', fixture: 'bisect-bug', name: 'unset-missing-key', args: ['<REPO>', { key: '' }] },

  // ── squash / autosquash ─────────────────────────────────────────────────
  { method: 'squashCommits', fixture: 'changelog', name: 'top-two', args: ['<REPO>', 'HEAD~1', 'squash two\n\nbody'] },
  {
    method: 'squashCommits', fixture: 'changelog', name: 'keeps-staged',
    setup: [{ write: 'pending.txt', content: 'pending\n' }, { git: ['add', 'pending.txt'] }, { write: '.git/hooks/pre-commit', content: '#!/bin/sh\nexit 1\n', mode: 0o755 }],
    args: ['<REPO>', 'HEAD~2', 'squash three']
  },
  { method: 'squashCommits', fixture: 'changelog', name: 'bad-sha', args: ['<REPO>', 'nope', 'x'] },
  { method: 'autosquash', fixture: 'interactive-rebase', name: 'basic', args: ['<REPO>', 'main'] },
  {
    method: 'autosquash', fixture: 'interactive-rebase', name: 'autostash',
    setup: [{ write: 'README.md', content: 'dirty\n' }], args: ['<REPO>', 'main', true]
  },
  { method: 'autosquash', fixture: 'interactive-rebase', name: 'bad-base', args: ['<REPO>', 'nope'] },

  // ── branches ────────────────────────────────────────────────────────────
  { method: 'checkout', fixture: 'gitflow', name: 'branch', args: ['<REPO>', 'main'] },
  { method: 'checkout', fixture: 'gitflow', name: 'missing', args: ['<REPO>', 'nope'] },
  {
    method: 'checkout', fixture: 'merge-conflict', name: 'dirty-blocks',
    setup: [{ write: 'greeting.txt', content: 'dirty\n' }], args: ['<REPO>', 'feature']
  },
  { method: 'createBranch', fixture: 'bisect-bug', name: 'checkout', args: ['<REPO>', 'test/scratch'] },
  { method: 'createBranch', fixture: 'bisect-bug', name: 'at-no-checkout', args: ['<REPO>', 'wip', 'HEAD~2', false] },
  { method: 'createBranch', fixture: 'bisect-bug', name: 'at-checkout', args: ['<REPO>', 'old', 'HEAD~3', true] },
  { method: 'createBranch', fixture: 'bisect-bug', name: 'duplicate', args: ['<REPO>', 'main'] },
  { method: 'createBranch', fixture: 'bisect-bug', name: 'duplicate-no-checkout', args: ['<REPO>', 'main', undefined, false] },
  { method: 'deleteBranch', fixture: 'gitflow', name: 'unmerged', args: ['<REPO>', 'feature/search'] },
  { method: 'deleteBranch', fixture: 'gitflow', name: 'force', args: ['<REPO>', 'feature/search', true] },
  { method: 'deleteBranch', fixture: 'gitflow', name: 'current', args: ['<REPO>', 'develop', true] },
  {
    method: 'deleteBranch', fixture: 'bisect-bug', name: 'merged',
    setup: [{ git: ['branch', 'done', 'HEAD~1'] }], args: ['<REPO>', 'done']
  },
  { method: 'renameBranch', fixture: 'bisect-bug', name: 'current', args: ['<REPO>', 'main', 'trunk'] },
  {
    method: 'renameBranch', fixture: 'bisect-bug', name: 'taken',
    setup: [{ git: ['branch', 'one'] }, { git: ['branch', 'two'] }], args: ['<REPO>', 'one', 'two']
  },
  {
    method: 'renameBranchRemote', fixture: 'gitflow', name: 'basic',
    setup: pushRemote, args: ['<REPO>', 'feature/search', 'feature/find', 'pushme']
  },
  {
    method: 'deleteRemoteBranch', fixture: 'gitflow', name: 'basic',
    setup: pushRemote, args: ['<REPO>', 'pushme', 'feature/search']
  },
  {
    method: 'deleteRemoteBranch', fixture: 'gitflow', name: 'already-gone',
    setup: [...pushRemote, { git: ['--git-dir', '<OUT>/origin.git', 'update-ref', '-d', 'refs/heads/feature/search'], noRepo: true }],
    args: ['<REPO>', 'pushme', 'feature/search']
  },
  { method: 'deleteRemoteBranch', fixture: 'gitflow', name: 'no-remote', args: ['<REPO>', 'nowhere', 'feature/search'] },
  { method: 'checkoutRemote', fixture: 'diverged-checkout', name: 'diverged', args: ['<REPO>', 'origin/feature', 'feature', 'origin'] },
  { method: 'checkoutRemote', fixture: 'diverged-checkout', name: 'new-local', args: ['<REPO>', 'origin/feature', 'feature-copy'] },
  { method: 'checkoutRemote', fixture: 'diverged-checkout', name: 'ahead', setup: aheadSetup, args: ['<REPO>', 'origin/feature', 'feature', 'origin'] },
  { method: 'checkoutRemote', fixture: 'diverged-checkout', name: 'behind', setup: behindSetup, args: ['<REPO>', 'origin/feature', 'feature', 'origin'] },
  {
    method: 'checkoutRemote', fixture: 'diverged-checkout', name: 'behind-dirty',
    setup: [...behindSetup, { write: 'app.ts', content: "export const VERSION = 'dirty'\n" }, { write: 'scratch.txt', content: 'untracked\n' }],
    args: ['<REPO>', 'origin/feature', 'feature']
  },
  {
    method: 'checkoutRemote', fixture: 'diverged-checkout', name: 'missing-remote-ref',
    args: ['<REPO>', 'origin/nope', 'nope']
  },
  {
    method: 'resolveDivergedCheckout', fixture: 'diverged-checkout', name: 'rebase-backup',
    args: ['<REPO>', 'origin/feature', 'feature', 'rebase', true]
  },
  { method: 'resolveDivergedCheckout', fixture: 'diverged-checkout', name: 'merge', args: ['<REPO>', 'origin/feature', 'feature', 'merge', false] },
  { method: 'resolveDivergedCheckout', fixture: 'diverged-checkout', name: 'reset-hard', setup: aheadSetup, args: ['<REPO>', 'origin/feature', 'feature', 'reset-hard', false] },
  { method: 'resolveDivergedCheckout', fixture: 'diverged-checkout', name: 'reset-mixed', setup: aheadSetup, args: ['<REPO>', 'origin/feature', 'feature', 'reset-mixed', true] },
  { method: 'resolveDivergedCheckout', fixture: 'diverged-checkout', name: 'reset-soft', setup: aheadSetup, args: ['<REPO>', 'origin/feature', 'feature', 'reset-soft', false] },
  { method: 'resolveDivergedCheckout', fixture: 'diverged-checkout', name: 'missing', args: ['<REPO>', 'origin/feature', 'nope', 'merge', false] },
  { method: 'remoteHasBranches', fixture: 'diverged-checkout', name: 'some', args: ['<REPO>', 'origin', ['feature', 'main', 'nope']] },
  { method: 'remoteHasBranches', fixture: 'diverged-checkout', name: 'empty', args: ['<REPO>', 'origin', []] },
  { method: 'remoteHasBranches', fixture: 'diverged-checkout', name: 'unknown-remote', args: ['<REPO>', 'nowhere', ['main']] },
  { method: 'filesToPush', fixture: 'diverged-checkout', name: 'ahead', args: ['<REPO>', 'feature'] },
  { method: 'filesToPush', fixture: 'diverged-checkout', name: 'in-sync', args: ['<REPO>', 'main'] },
  { method: 'filesToPush', fixture: 'bisect-bug', name: 'no-upstream', args: ['<REPO>', 'main'] },

  // ── merge / rebase ──────────────────────────────────────────────────────
  { method: 'merge', fixture: 'merge-conflict', name: 'conflict', args: ['<REPO>', 'feature'] },
  { method: 'merge', fixture: 'merge-conflict', name: 'ff-only', args: ['<REPO>', 'feature', { ffOnly: true }] },
  { method: 'merge', fixture: 'merge-conflict', name: 'favour-ours-modify-delete', args: ['<REPO>', 'feature', { favour: 'ours' }] },
  { method: 'merge', fixture: 'bisect-bug', name: 'favour-ours', setup: contentConflict, args: ['<REPO>', 'incoming', { favour: 'ours' }] },
  { method: 'merge', fixture: 'bisect-bug', name: 'favour-theirs', setup: contentConflict, args: ['<REPO>', 'incoming', { favour: 'theirs' }] },
  { method: 'merge', fixture: 'bisect-bug', name: 'content-conflict', setup: contentConflict, args: ['<REPO>', 'incoming'] },
  { method: 'merge', fixture: 'bisect-bug', name: 'squash', setup: cleanBranch, args: ['<REPO>', 'addition', { squash: true }] },
  { method: 'merge', fixture: 'bisect-bug', name: 'no-commit', setup: cleanBranch, args: ['<REPO>', 'addition', { noCommit: true }] },
  { method: 'merge', fixture: 'bisect-bug', name: 'no-ff-boolean', setup: [{ git: ['branch', 'side', 'HEAD~2'] }, { git: ['checkout', '-q', 'side'] }], args: ['<REPO>', 'main', true] },
  { method: 'merge', fixture: 'bisect-bug', name: 'fast-forward', setup: [{ git: ['checkout', '-q', '-b', 'side', 'HEAD~2'] }], args: ['<REPO>', 'main'] },
  { method: 'merge', fixture: 'bisect-bug', name: 'strategy-resolve', setup: cleanBranch, args: ['<REPO>', 'addition', { strategy: 'resolve', ignoreSpace: 'eol' }] },
  {
    method: 'merge', fixture: 'bisect-bug', name: 'autostash',
    setup: [...cleanBranch, { write: 'math.js', content: 'dirty\n' }, { write: 'untracked.txt', content: 'u\n' }],
    args: ['<REPO>', 'addition']
  },
  { method: 'merge', fixture: 'bisect-bug', name: 'unknown-ref', args: ['<REPO>', 'nope'] },
  { method: 'merge', fixture: 'bisect-bug', name: 'up-to-date', args: ['<REPO>', 'HEAD~1'] },
  { method: 'mergeInto', fixture: 'stacked-branches', name: 'no-ff-boolean', args: ['<REPO>', 'main', 'feature/api', true] },
  { method: 'mergeInto', fixture: 'stacked-branches', name: 'options', args: ['<REPO>', 'feature/api', 'main', { noFf: true }] },
  { method: 'mergeInto', fixture: 'merge-conflict', name: 'conflict', setup: [{ git: ['checkout', '-q', 'feature'] }], args: ['<REPO>', 'feature', 'main'] },
  { method: 'conflictCommits', fixture: 'merge-conflict', name: 'merging', setup: mergeConflictSetup, args: ['<REPO>', 'greeting.txt'] },
  { method: 'conflictCommits', fixture: 'bisect-bug', name: 'no-merge', args: ['<REPO>', 'math.js'] },
  { method: 'rebase', fixture: 'rebase-conflict', name: 'conflict', args: ['<REPO>', 'main'] },
  { method: 'rebase', fixture: 'stacked-branches', name: 'clean', args: ['<REPO>', 'feature/api'] },
  { method: 'rebase', fixture: 'stacked-branches', name: 'unknown', args: ['<REPO>', 'nope'] },
  { method: 'rebaseOnto', fixture: 'stacked-branches', name: 'basic', args: ['<REPO>', 'feature/api', 'main'] },
  { method: 'rebaseOnto', fixture: 'rebase-conflict', name: 'conflict', setup: [{ git: ['checkout', '-q', 'main'] }], args: ['<REPO>', 'feature', 'main'] },
  { method: 'rebaseAbort', fixture: 'rebase-conflict', name: 'mid-rebase', setup: [{ git: ['rebase', 'main'], allowFail: true }], args: ['<REPO>'] },
  { method: 'rebaseAbort', fixture: 'bisect-bug', name: 'idle', args: ['<REPO>'] },

  // ── stacks ──────────────────────────────────────────────────────────────
  { method: 'stackInfo', fixture: 'stacked-branches', name: 'current', args: ['<REPO>'] },
  { method: 'stackInfo', fixture: 'stacked-branches', name: 'leaf-api', args: ['<REPO>', 'feature/api'] },
  { method: 'stackInfo', fixture: 'stacked-branches', name: 'trunk', args: ['<REPO>', 'main'] },
  { method: 'stackInfo', fixture: 'merge-conflict', name: 'no-stack', args: ['<REPO>'] },
  { method: 'stackInfo', fixture: 'detached-head', name: 'detached', args: ['<REPO>'] },
  { method: 'stackLeaves', fixture: 'stacked-branches', name: 'one', args: ['<REPO>'] },
  {
    method: 'stackLeaves', fixture: 'stacked-branches', name: 'two',
    setup: [{ git: ['branch', 'docs-1', 'main'] }, { git: ['config', 'branch.docs-1.gitcitoparent', 'main'] }], args: ['<REPO>']
  },
  { method: 'stackLeaves', fixture: 'merge-conflict', name: 'none', args: ['<REPO>'] },
  { method: 'stackSetParent', fixture: 'merge-conflict', name: 'basic', args: ['<REPO>', 'feature', 'main'] },
  { method: 'stackSetParent', fixture: 'merge-conflict', name: 'missing-parent', args: ['<REPO>', 'feature', 'nope'] },
  { method: 'stackClearParent', fixture: 'stacked-branches', name: 'basic', args: ['<REPO>', 'feature/ui'] },
  { method: 'stackClearParent', fixture: 'merge-conflict', name: 'untracked', args: ['<REPO>', 'feature'] },
  { method: 'stackRestack', fixture: 'stacked-branches', name: 'cascade', args: ['<REPO>', 'feature/ui'] },
  {
    method: 'stackRestack', fixture: 'stacked-branches', name: 'protected',
    setup: [{ git: ['config', 'branch.main.gitcitoparent', 'feature/api'] }], args: ['<REPO>', 'main']
  },
  {
    method: 'stackRestack', fixture: 'stacked-branches', name: 'conflict',
    setup: [
      { git: ['checkout', '-q', 'main'] }, { write: 'api.js', content: 'main owns this now\n' }, ...commitAll('main: claim api.js'),
      { git: ['checkout', '-q', 'feature/ui'] }
    ],
    args: ['<REPO>', 'feature/ui']
  },
  { method: 'stackInsert', fixture: 'stacked-branches', name: 'middle', args: ['<REPO>', 'feature/mid', 'feature/api'] },
  { method: 'stackInsert', fixture: 'stacked-branches', name: 'exists', args: ['<REPO>', 'feature/ui', 'main'] },
  { method: 'stackReorder', fixture: 'stacked-branches', name: 'swap', args: ['<REPO>', 'main', ['feature/ui', 'feature/api']] },
  { method: 'stackReorder', fixture: 'stacked-branches', name: 'new-trunk', setup: [{ git: ['branch', 'release', 'main'] }], args: ['<REPO>', 'release', ['feature/api', 'feature/ui']] },
  { method: 'stackReorder', fixture: 'stacked-branches', name: 'trunk-as-stop', args: ['<REPO>', 'main', ['main', 'feature/ui']] },
  { method: 'stackReorder', fixture: 'stacked-branches', name: 'duplicate', args: ['<REPO>', 'main', ['feature/ui', 'feature/ui']] },
  { method: 'stackReorder', fixture: 'stacked-branches', name: 'empty', args: ['<REPO>', 'main', []] },
  { method: 'stackSetRoute', fixture: 'stacked-branches', name: 'drop-stop', args: ['<REPO>', 'main', ['feature/ui']] },
  {
    method: 'stackSetRoute', fixture: 'stacked-branches', name: 'swap-branch',
    setup: [{ git: ['branch', 'feature/docs', 'main'] }], args: ['<REPO>', 'main', ['feature/docs', 'feature/ui']]
  },
  { method: 'stackSetRoute', fixture: 'stacked-branches', name: 'creates-stop', args: ['<REPO>', 'main', ['feature/api', 'feature/new', 'feature/ui']] },
  { method: 'stackSetRoute', fixture: 'stacked-branches', name: 'trunk-as-stop', args: ['<REPO>', 'main', ['main', 'feature/ui']] },
  {
    method: 'stackSetRoute', fixture: 'stacked-branches', name: 'protected',
    setup: [{ git: ['branch', 'release', 'main'] }, { git: ['config', 'gitcito.protectedbranches', 'main,release'] }],
    args: ['<REPO>', 'feature/api', ['release']]
  },
  { method: 'stackSetRoute', fixture: 'stacked-branches', name: 'conflict-rolls-back', setup: conflictingStack, args: ['<REPO>', 'main', ['note-2', 'note-1']] },
  { method: 'stackSetRoute', fixture: 'stacked-branches', name: 'clear-all', args: ['<REPO>', 'main', []] },
  {
    method: 'stackPruneMerged', fixture: 'stacked-branches', name: 'merged-bottom',
    setup: [
      { git: ['checkout', '-q', 'main'] }, { git: [...ID, 'merge', '-q', '--no-ff', '-m', 'merge feature/api', 'feature/api'] },
      { git: ['checkout', '-q', 'feature/ui'] }
    ],
    args: ['<REPO>']
  },
  { method: 'stackPruneMerged', fixture: 'stacked-branches', name: 'also-merged', args: ['<REPO>', ['feature/api']] },
  { method: 'stackPruneMerged', fixture: 'stacked-branches', name: 'nothing', args: ['<REPO>'] },
  {
    method: 'stackPruneMerged', fixture: 'stacked-branches', name: 'by-leaf',
    setup: [{ git: ['checkout', '-q', 'main'] }, { git: [...ID, 'merge', '-q', '--no-ff', '-m', 'merge feature/api', 'feature/api'] }],
    args: ['<REPO>', [], 'feature/ui']
  },

  // ── git-flow ────────────────────────────────────────────────────────────
  { method: 'gitflowStatus', fixture: 'gitflow', name: 'initialized', args: ['<REPO>'] },
  { method: 'gitflowStatus', fixture: 'gitflow', name: 'on-feature', setup: [{ git: ['checkout', '-q', 'feature/search'] }], args: ['<REPO>'] },
  { method: 'gitflowStatus', fixture: 'merge-conflict', name: 'uninitialized', args: ['<REPO>'] },
  { method: 'gitflowStatus', fixture: 'empty-repo', name: 'unborn', args: ['<REPO>'] },
  {
    method: 'gitflowInit', fixture: 'merge-conflict', name: 'creates-develop',
    args: ['<REPO>', { main: 'main', develop: 'develop', featurePrefix: 'feature/', releasePrefix: 'release/', hotfixPrefix: 'hotfix/', versionTagPrefix: 'v' }]
  },
  {
    method: 'gitflowInit', fixture: 'merge-conflict', name: 'missing-main',
    args: ['<REPO>', { main: 'trunk', develop: 'dev', featurePrefix: 'f/', releasePrefix: 'r/', hotfixPrefix: 'h/', versionTagPrefix: '' }]
  },
  { method: 'gitflowStart', fixture: 'gitflow', name: 'feature', args: ['<REPO>', 'feature', ' login '] },
  { method: 'gitflowStart', fixture: 'gitflow', name: 'hotfix', args: ['<REPO>', 'hotfix', '1.0.1'] },
  { method: 'gitflowStart', fixture: 'gitflow', name: 'exists', args: ['<REPO>', 'feature', 'search'] },
  { method: 'gitflowStart', fixture: 'gitflow', name: 'empty-name', args: ['<REPO>', 'release', '  '] },
  { method: 'gitflowStart', fixture: 'merge-conflict', name: 'no-develop', args: ['<REPO>', 'feature', 'x'] },
  { method: 'gitflowFinish', fixture: 'gitflow', name: 'feature', args: ['<REPO>', 'feature', 'search'] },
  { method: 'gitflowFinish', fixture: 'gitflow', name: 'release', args: ['<REPO>', 'release', '1.1.0'] },
  { method: 'gitflowFinish', fixture: 'gitflow', name: 'release-keep', args: ['<REPO>', 'release', '1.1.0', { tag: false, deleteBranch: false }] },
  { method: 'gitflowFinish', fixture: 'gitflow', name: 'release-message', args: ['<REPO>', 'release', '1.1.0', { message: '  Shiny  ' }] },
  {
    method: 'gitflowFinish', fixture: 'gitflow', name: 'hotfix',
    setup: [{ call: 'gitflowStart', args: ['<REPO>', 'hotfix', '1.0.1'] }, { write: 'HOTFIX', content: 'fixed\n' }, ...commitAll('fix: hot')],
    args: ['<REPO>', 'hotfix', '1.0.1']
  },
  { method: 'gitflowFinish', fixture: 'gitflow', name: 'dirty', setup: [{ write: 'app.js', content: 'dirty\n' }], args: ['<REPO>', 'feature', 'search'] },
  { method: 'gitflowFinish', fixture: 'gitflow', name: 'missing', args: ['<REPO>', 'feature', 'nope'] },
  {
    method: 'gitflowFinish', fixture: 'gitflow', name: 'conflict-rolls-back',
    setup: [
      { git: ['checkout', '-q', 'feature/search'] }, { write: 'CLASH', content: 'feature side\n' }, ...commitAll('feature clash'),
      { git: ['checkout', '-q', 'develop'] }, { write: 'CLASH', content: 'develop side\n' }, ...commitAll('develop clash')
    ],
    args: ['<REPO>', 'feature', 'search']
  },
  {
    method: 'gitflowUndo', fixture: 'gitflow', name: 'release',
    setup: [{ call: 'gitflowFinish', args: ['<REPO>', 'release', '1.1.0'], as: 'snap' }],
    args: ['<REPO>', { $var: 'snap' }]
  },

  // ── rerere ──────────────────────────────────────────────────────────────
  { method: 'rerereStatus', fixture: 'merge-conflict', name: 'off', args: ['<REPO>'] },
  {
    method: 'rerereStatus', fixture: 'merge-conflict', name: 'replayed',
    setup: [...rerereFirstPass('resolved by hand\n'), { call: 'merge', args: ['<REPO>', 'feature'], allowFail: true }],
    args: ['<REPO>']
  },
  { method: 'setRerere', fixture: 'merge-conflict', name: 'repo', args: ['<REPO>', { enabled: true, autoUpdate: true }, 'repo'] },
  { method: 'setRerere', fixture: 'merge-conflict', name: 'global', args: ['<REPO>', { enabled: false }] },
  { method: 'setRerere', fixture: 'merge-conflict', name: 'nothing', args: ['<REPO>', {}, 'repo'] },
  {
    method: 'rerereForget', fixture: 'merge-conflict', name: 'brings-conflict-back',
    setup: [...rerereFirstPass('first answer\n'), { call: 'merge', args: ['<REPO>', 'feature'], allowFail: true }],
    args: ['<REPO>', 'greeting.txt']
  },
  { method: 'rerereForget', fixture: 'merge-conflict', name: 'no-rerere', args: ['<REPO>', 'greeting.txt'] },
  {
    method: 'rerereClear', fixture: 'merge-conflict', name: 'basic',
    setup: rerereFirstPass('answer\n'), args: ['<REPO>']
  },

  // ── bisect ──────────────────────────────────────────────────────────────
  { method: 'bisectStatus', fixture: 'bisect-bug', name: 'idle', args: ['<REPO>'] },
  { method: 'bisectStatus', fixture: 'bisect-bug', name: 'seeded', setup: bisectSeed, args: ['<REPO>'] },
  { method: 'bisectStart', fixture: 'bisect-bug', name: 'basic', args: ['<REPO>'] },
  { method: 'bisectMark', fixture: 'bisect-bug', name: 'bad', setup: [{ call: 'bisectStart', args: ['<REPO>'] }], args: ['<REPO>', 'bad'] },
  { method: 'bisectMark', fixture: 'bisect-bug', name: 'good-rev', setup: bisectSeed.slice(0, 2), args: ['<REPO>', 'good', ':/feat: discount function'] },
  { method: 'bisectMark', fixture: 'bisect-bug', name: 'skip', setup: bisectSeed, args: ['<REPO>', 'skip'] },
  { method: 'bisectMark', fixture: 'bisect-bug', name: 'bad-rev', setup: [{ call: 'bisectStart', args: ['<REPO>'] }], args: ['<REPO>', 'bad', 'nope'] },
  {
    method: 'bisectMark', fixture: 'bisect-bug', name: 'to-the-end',
    setup: [
      ...bisectSeed,
      { git: ['bisect', 'good', ':/feat: addTax'] }, { git: ['bisect', 'bad', ':/feat: barrel export'] }
    ],
    args: ['<REPO>', 'bad', ':/refactor: simplify discount']
  },
  { method: 'bisectReset', fixture: 'bisect-bug', name: 'seeded', setup: bisectSeed, args: ['<REPO>'] },
  { method: 'bisectReset', fixture: 'bisect-bug', name: 'idle', args: ['<REPO>'] },
  {
    method: 'bisectRunScript', fixture: 'bisect-bug', name: 'finds-culprit', setup: bisectSeed, events: true,
    args: ['<REPO>', "  grep -q 'price - multiply' discount.js  "]
  },
  { method: 'bisectRunScript', fixture: 'bisect-bug', name: 'not-started', args: ['<REPO>', 'true'] },
  { method: 'bisectRunScript', fixture: 'bisect-bug', name: 'empty-command', args: ['<REPO>', '   '] },
  { method: 'bisectCancel', fixture: 'bisect-bug', name: 'idle', args: ['<REPO>'] },

  // ── patches ─────────────────────────────────────────────────────────────
  // Not HEAD: the bisect-bug tip adds .bisect-hint, whose content is a SHA.
  { method: 'formatPatch', fixture: 'bisect-bug', name: 'one', args: ['<REPO>', 'HEAD~1'] },
  { method: 'formatPatch', fixture: 'bisect-bug', name: 'two', args: ['<REPO>', 'HEAD~1', 2] },
  { method: 'formatPatch', fixture: 'bisect-bug', name: 'missing', args: ['<REPO>', 'nope'] },
  { method: 'applyPatch', fixture: 'bisect-bug', name: 'apply', args: ['<REPO>', NEW_FILE_PATCH] },
  { method: 'applyPatch', fixture: 'bisect-bug', name: 'am', args: ['<REPO>', MBOX_PATCH, true] },
  { method: 'applyPatch', fixture: 'bisect-bug', name: 'conflict', args: ['<REPO>', BAD_CONTEXT_PATCH] },
  { method: 'applyPatch', fixture: 'bisect-bug', name: 'am-garbage', args: ['<REPO>', 'not a patch\n', true] },

  // ── tracked files, sizes, tree status ──────────────────────────────────
  { method: 'listTrackedFiles', fixture: 'bisect-bug', name: 'basic', args: ['<REPO>'] },
  { method: 'listTrackedFiles', fixture: 'binary-images-unicode', name: 'unicode', args: ['<REPO>'] },
  { method: 'listTrackedFiles', fixture: 'empty-repo', name: 'staged-only', args: ['<REPO>'] },
  { method: 'ignoredTrackedFiles', fixture: 'untracked-mess', name: 'mixed', args: ['<REPO>', ['node_modules/x.js', 'notes.md', 'app.log', 'src/index.js', 'dist']] },
  { method: 'ignoredTrackedFiles', fixture: 'gitignore-untrack', name: 'tracked-but-ignored', args: ['<REPO>', ['.env', 'debug.log', 'build/out.js', 'README.md']] },
  { method: 'ignoredTrackedFiles', fixture: 'bisect-bug', name: 'empty', args: ['<REPO>', []] },
  { method: 'ignoredTrackedFiles', fixture: 'bisect-bug', name: 'not-a-repo', setup: [{ mkdir: 'plain', out: true }], args: ['<OUT>/plain', ['a.txt']] },
  { method: 'commitsTouchingPath', fixture: 'bisect-bug', name: 'file', args: ['<REPO>', 'discount.js'] },
  { method: 'commitsTouchingPath', fixture: 'bisect-bug', name: 'max', args: ['<REPO>', 'math.js', 1] },
  { method: 'commitsTouchingPath', fixture: 'bisect-bug', name: 'none', args: ['<REPO>', 'nope.txt'] },
  { method: 'fileSizes', fixture: 'binary-images-unicode', name: 'mixed', args: ['<REPO>', ['data.bin', 'café-☕.txt', 'missing.txt', 'assets']] },
  { method: 'fileSizes', fixture: 'bisect-bug', name: 'empty', args: ['<REPO>', []] },
  { method: 'treeStatus', fixture: 'untracked-mess', name: 'ignored-collapsed', args: ['<REPO>'] },
  { method: 'treeStatus', fixture: 'project-tree', name: 'kinds', args: ['<REPO>'] },
  { method: 'treeStatus', fixture: 'merge-conflict', name: 'conflicted', setup: mergeConflictSetup, args: ['<REPO>'] },
  { method: 'treeStatus', fixture: 'bisect-bug', name: 'clean', args: ['<REPO>'] },

  // ── file-system actions ─────────────────────────────────────────────────
  { method: 'fsCreate', fixture: 'tree-dnd', name: 'nested-file', args: ['<REPO>', 'new/dir/file.txt', false] },
  { method: 'fsCreate', fixture: 'tree-dnd', name: 'dir', args: ['<REPO>', 'newdir/inner', true] },
  { method: 'fsCreate', fixture: 'tree-dnd', name: 'exists', args: ['<REPO>', 'loose.txt', false] },
  { method: 'fsRename', fixture: 'tree-dnd', name: 'tracked', args: ['<REPO>', 'src/app.ts', 'src/main.ts'] },
  { method: 'fsRename', fixture: 'tree-dnd', name: 'untracked-new-dir', args: ['<REPO>', 'loose.txt', 'notes/loose2.txt'] },
  { method: 'fsRename', fixture: 'tree-dnd', name: 'same', args: ['<REPO>', 'loose.txt', 'loose.txt'] },
  { method: 'fsRename', fixture: 'tree-dnd', name: 'exists', args: ['<REPO>', 'loose.txt', 'draft.md'] },
  { method: 'fsDelete', fixture: 'tree-dnd', name: 'files-and-dir', args: ['<REPO>', ['loose.txt', 'docs']] },
  { method: 'fsDelete', fixture: 'tree-dnd', name: 'outside', args: ['<REPO>', ['../escape']] },
  { method: 'fsExisting', fixture: 'tree-dnd', name: 'docs', args: ['<REPO>', 'docs', ['readme.md', 'loose.txt', '/abs/path/readme.md']] },
  { method: 'fsExisting', fixture: 'tree-dnd', name: 'root', args: ['<REPO>', '', ['loose.txt', 'nope']] },
  { method: 'fsExisting', fixture: 'tree-dnd', name: 'slashes', args: ['<REPO>', '//docs/', ['readme.md']] },
  { method: 'fsMove', fixture: 'tree-dnd', name: 'tracked', args: ['<REPO>', ['src/app.ts'], 'lib'] },
  { method: 'fsMove', fixture: 'tree-dnd', name: 'same-folder', args: ['<REPO>', ['/loose.txt/', 'docs/readme.md'], ''] },
  { method: 'fsMove', fixture: 'tree-dnd', name: 'untracked', args: ['<REPO>', ['loose.txt'], 'docs'] },
  { method: 'fsMove', fixture: 'tree-dnd', name: 'to-root', setup: [{ git: ['mv', 'lib/helpers.ts', 'docs/helpers.ts'] }], args: ['<REPO>', ['docs/helpers.ts'], ''] },
  { method: 'fsMove', fixture: 'tree-dnd', name: 'into-itself', args: ['<REPO>', ['src'], 'src/deep'] },
  { method: 'fsMove', fixture: 'tree-dnd', name: 'exists', setup: [{ write: 'helpers.ts', content: 'root copy' }], args: ['<REPO>', ['helpers.ts'], 'lib'] },
  { method: 'fsMove', fixture: 'tree-dnd', name: 'replace', setup: [{ write: 'readme.md', content: 'incoming' }], args: ['<REPO>', ['readme.md'], 'docs', 'replace'] },
  {
    method: 'fsMove', fixture: 'tree-dnd', name: 'keep-both',
    setup: [{ write: 'docs/readme 2.md', content: 'second' }], args: ['<REPO>', ['readme.md'], 'docs', 'keepBoth']
  },
  { method: 'fsMove', fixture: 'tree-dnd', name: 'keep-both-no-ext', setup: [{ write: 'docs/LICENSE', content: 'a' }, { write: 'LICENSE', content: 'b' }], args: ['<REPO>', ['LICENSE'], 'docs', 'keepBoth'] },
  {
    method: 'fsImport', fixture: 'tree-dnd', name: 'copy-and-move',
    setup: [{ write: 'dropped.txt', content: 'from Finder', out: true }],
    args: ['<REPO>', ['<OUT>/dropped.txt', '<REPO>/draft.md'], 'lib']
  },
  {
    method: 'fsImport', fixture: 'tree-dnd', name: 'folder',
    setup: [{ mkdir: 'kit/sub', out: true }, { write: 'kit/a.txt', content: 'a', out: true }, { write: 'kit/sub/b.txt', content: 'b', out: true }],
    args: ['<REPO>', ['<OUT>/kit'], 'vendor/new']
  },
  {
    method: 'fsImport', fixture: 'tree-dnd', name: 'keep-both',
    setup: [{ write: 'readme.md', content: 'from Finder', out: true }], args: ['<REPO>', ['<OUT>/readme.md'], 'docs', 'keepBoth']
  },
  {
    method: 'fsImport', fixture: 'tree-dnd', name: 'replace',
    setup: [{ write: 'readme.md', content: 'from Finder', out: true }], args: ['<REPO>', ['<OUT>/readme.md'], 'docs', 'replace']
  },
  {
    method: 'fsImport', fixture: 'tree-dnd', name: 'conflict',
    setup: [{ write: 'readme.md', content: 'from Finder', out: true }], args: ['<REPO>', ['<OUT>/readme.md'], 'docs']
  },
  { method: 'fsImport', fixture: 'tree-dnd', name: 'outside', args: ['<REPO>', ['<OUT>/x'], '../escape'] },
  { method: 'fsImport', fixture: 'tree-dnd', name: 'already-there', args: ['<REPO>', ['<REPO>/docs/readme.md'], 'docs'] },

  // ── repository-chat file batches ────────────────────────────────────────
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'edit',
    args: [],
    prepare: async (repo) => [
      '<REPO>',
      await prepareRepoFileActions(
        repo,
        [{ type: 'edit_file', path: 'README.md', oldText: '# BugShop', newText: '# BugShop $& updated', description: 'Update heading' }],
        { evidencePaths: new Set(['README.md']), completePaths: new Set(['README.md']), ignoredPaths: new Set() }
      )
    ]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'batch',
    args: [],
    prepare: async (repo) => [
      '<REPO>',
      await prepareRepoFileActions(
        repo,
        [
          { type: 'write_file', path: 'docs/new/notes.md', content: 'notes\n', mode: 'create', description: 'add notes' },
          { type: 'delete_file', path: 'stats.js', description: 'drop stats' },
          { type: 'edit_file', path: 'math.js', oldText: 'a', newText: 'A', replaceAll: true, description: 'shout' },
          { type: 'write_file', path: 'currency.js', content: 'replaced\n', mode: 'replace', description: 'rewrite' }
        ],
        {
          evidencePaths: new Set(['stats.js', 'math.js', 'currency.js']),
          completePaths: new Set(['currency.js']),
          ignoredPaths: new Set()
        }
      )
    ]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'stale',
    args: ['<REPO>', [{ type: 'edit_file', path: 'README.md', oldText: '# BugShop', newText: 'x', description: 'd', expectedHash: '0'.repeat(64), expectedOccurrences: 1, preview: '' }]]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'unsafe',
    args: ['<REPO>', [{ type: 'write_file', path: '../escape.txt', content: 'x', mode: 'create', description: 'd', expectedHash: null, preview: '' }]]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'secret',
    args: ['<REPO>', [{ type: 'write_file', path: 'config/.env.local', content: 'x', mode: 'create', description: 'd', expectedHash: null, preview: '' }]]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'git-internal',
    args: ['<REPO>', [{ type: 'write_file', path: 'sub/.GIT/config', content: 'x', mode: 'create', description: 'd', expectedHash: null, preview: '' }]]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'generated',
    args: ['<REPO>', [{ type: 'write_file', path: 'src/generated/x.ts', content: 'x', mode: 'create', description: 'd', expectedHash: null, preview: '' }]]
  },
  {
    method: 'applyRepoFileActions', fixture: 'untracked-mess', name: 'ignored',
    args: ['<REPO>', [{ type: 'write_file', path: 'app.log', content: 'x', mode: 'replace', description: 'd', expectedHash: null, preview: '' }]]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'unknown-type',
    args: ['<REPO>', [{ type: 'chmod_file', path: 'math.js', description: 'd', expectedHash: null, preview: '' }]]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'binary-content',
    args: ['<REPO>', [{ type: 'write_file', path: 'bin.dat', content: 'a\u0000b', mode: 'create', description: 'd', expectedHash: null, preview: '' }]]
  },
  {
    method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'file-as-parent',
    args: ['<REPO>', [{ type: 'write_file', path: 'math.js/inner.txt', content: 'x', mode: 'create', description: 'd', expectedHash: null, preview: '' }]]
  },
  { method: 'applyRepoFileActions', fixture: 'bisect-bug', name: 'empty', args: ['<REPO>', []] }
]

// ─── Running a case ─────────────────────────────────────────────────────────

const git = (cwd: string | null, args: string[]): string =>
  execFileSync('git', cwd ? ['-C', cwd, ...args] : args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })

const tryGit = (cwd: string | null, args: string[]): string => {
  try {
    return git(cwd, args)
  } catch {
    return ''
  }
}

interface Ctx {
  repo: string
  out: string
  vars: Record<string, unknown>
}

function subst(v: Arg, ctx: Ctx): unknown {
  if (typeof v === 'string') return v.split('<REPO>').join(ctx.repo).split('<OUT>').join(ctx.out)
  if (Array.isArray(v)) return v.map((x) => subst(x, ctx))
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>
    if (typeof o.$var === 'string') return ctx.vars[o.$var]
    return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, subst(x, ctx)]))
  }
  return v
}

type Svc = Record<string, (...a: unknown[]) => Promise<unknown>>

async function runStep(step: Step, ctx: Ctx): Promise<void> {
  if ('git' in step) {
    const args = subst(step.git, ctx) as string[]
    try {
      git(step.noRepo ? null : ctx.repo, args)
    } catch (err) {
      if (!step.allowFail) throw err
    }
  } else if ('write' in step) {
    const p = join(step.out ? ctx.out : ctx.repo, step.write)
    mkdirSync(dirname(p), { recursive: true })
    writeFileSync(p, step.content, step.mode ? { mode: step.mode } : undefined)
  } else if ('mkdir' in step) {
    mkdirSync(join(step.out ? ctx.out : ctx.repo, step.mkdir), { recursive: true })
  } else {
    const fn = (gitService as unknown as Svc)[step.call]
    try {
      const res = await fn.apply(gitService, subst(step.args, ctx) as unknown[])
      if (step.as) ctx.vars[step.as] = JSON.parse(JSON.stringify(res ?? null))
    } catch (err) {
      if (!step.allowFail) throw err
    }
  }
}

// ─── Normalization (mirrored in Go: cNormalize in c_golden_test.go) ─────────

interface CommitRow {
  parents: string[]
  subject: string
}

/** Commit labels for one repository: `<subject@hash>` of the subject DAG. */
class Labeler {
  private rows = new Map<string, CommitRow>()
  private labels = new Map<string, string>()
  private kinds = new Map<string, string>()

  constructor(private repo: string) {
    const out = tryGit(repo, ['log', '--all', '--reflog', '--format=%H%x00%P%x00%s'])
    for (const line of out.split('\n')) {
      const [sha, parents, subject] = line.split('\0')
      if (sha && sha.length === 40) this.rows.set(sha, { parents: parents ? parents.split(' ') : [], subject: subject ?? '' })
    }
  }

  private row(sha: string): CommitRow | null {
    const hit = this.rows.get(sha)
    if (hit) return hit
    const out = tryGit(this.repo, ['log', '-1', '--format=%H%x00%P%x00%s', sha, '--'])
    const [full, parents, subject] = out.replace(/\n$/, '').split('\0')
    if (full !== sha) return null
    const row = { parents: parents ? parents.split(' ') : [], subject: subject ?? '' }
    this.rows.set(sha, row)
    return row
  }

  private kind(sha: string): string {
    let k = this.kinds.get(sha)
    if (k === undefined) {
      k = tryGit(this.repo, ['cat-file', '-t', sha]).trim()
      this.kinds.set(sha, k)
    }
    return k
  }

  /** Label for a full sha, or null when it is not a commit or tag object. */
  label(sha: string): string | null {
    const cached = this.labels.get(sha)
    if (cached) return cached
    const row = this.rows.has(sha) ? this.rows.get(sha)! : this.kind(sha) === 'commit' ? this.row(sha) : null
    if (!row) {
      if (this.kind(sha) === 'tag') {
        const name = /^tag (.*)$/m.exec(tryGit(this.repo, ['cat-file', 'tag', sha]))?.[1] ?? '?'
        const l = `<TAG:${name}>`
        this.labels.set(sha, l)
        return l
      }
      return null
    }
    const parentLabels = row.parents.map((p) => this.label(p) ?? p)
    const h = createHash('sha1').update([row.subject, ...parentLabels].join('\0')).digest('hex').slice(0, 8)
    const l = `<${row.subject}@${h}>`
    this.labels.set(sha, l)
    return l
  }

  /** A known commit whose sha starts with `abbrev` (unique), or null. */
  expand(abbrev: string): string | null {
    let found: string | null = null
    for (const sha of this.rows.keys()) {
      if (sha.startsWith(abbrev)) {
        if (found) return null
        found = sha
      }
    }
    return found
  }
}

const ISO_DATE = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2})/g

function normString(s: string, ctx: Ctx, lab: Labeler): string {
  const paths: [string, string][] = [
    [ctx.repo, '<REPO>'],
    [ctx.out, '<OUT>'],
    [PLAYGROUND, '<PLAYGROUND>'],
    [homedir(), '<HOME>'],
    [tmpdir(), '<TMP>']
  ]
  for (const [p, token] of paths) if (p) s = s.split(p).join(token)
  s = s.replace(ISO_DATE, '<DATE>')
  s = s.replace(/^Date: .*$/gm, 'Date: <DATE>')
  s = s.replace(/\d{8}-\d{6}/g, '<STAMP>')
  s = s.replace(/gitcito-apply-\d+\.patch/g, 'gitcito-apply-<N>.patch')
  s = s.replace(/\n-- \n\d+\.\d+[^\n]*\n/g, '\n-- \n<GITVERSION>\n')
  s = s.replace(/\b[0-9a-f]{40}\b/g, (sha) => lab.label(sha) ?? sha)
  s = s.replace(/\b[0-9a-f]{7,39}\b/g, (abbr) => {
    const full = lab.expand(abbr)
    return full ? (lab.label(full) ?? abbr) : abbr
  })
  return s
}

function norm(v: unknown, ctx: Ctx, lab: Labeler): unknown {
  if (typeof v === 'string') return normString(v, ctx, lab)
  if (Array.isArray(v)) return v.map((x) => norm(x, ctx, lab))
  if (v && typeof v === 'object') {
    return Object.fromEntries(Object.entries(v as Record<string, unknown>).map(([k, x]) => [normString(k, ctx, lab), norm(x, ctx, lab)]))
  }
  return v
}

const CONFIG_KEYS = /^(branch|gitflow|rerere|commit|gpg|user|gitcito|remote)\./
const OPS = ['MERGE_HEAD', 'CHERRY_PICK_HEAD', 'REVERT_HEAD', 'rebase-merge', 'rebase-apply', 'BISECT_START', 'ORIG_HEAD']

/** The repository after a write: refs, history shape, index, tree, config. */
function snapshot(repo: string): Record<string, unknown> {
  const lines = (s: string): string[] => s.split('\n').filter(Boolean)
  const gitDir = tryGit(repo, ['rev-parse', '--absolute-git-dir']).trim()
  const rr = join(gitDir, 'rr-cache')
  return {
    head: tryGit(repo, ['symbolic-ref', '-q', 'HEAD']).trim() || 'DETACHED',
    headAuthor: tryGit(repo, ['log', '-1', '--format=%an <%ae>']).trim(),
    status: lines(tryGit(repo, ['status', '--porcelain=v2', '--branch'])),
    refs: lines(tryGit(repo, ['for-each-ref', '--format=%(refname) %(objectname)'])),
    log: lines(tryGit(repo, ['log', '--all', '--format=%H%x00%P%x00%s'])),
    stash: lines(tryGit(repo, ['stash', 'list'])),
    index: lines(tryGit(repo, ['ls-files', '-s'])),
    diff: tryGit(repo, ['diff', '--no-color', '--no-ext-diff']),
    config: lines(tryGit(repo, ['config', '--local', '--list'])).filter((l) => CONFIG_KEYS.test(l)),
    globalRerere: lines(tryGit(repo, ['config', '--global', '--get-regexp', '^rerere\\.'])),
    ops: OPS.filter((name) => existsSync(join(gitDir, name))),
    rrCache: existsSync(rr) ? readdirSync(rr).filter((n) => !n.startsWith('.')).length : 0
  }
}

/**
 * Files whose content is a SHA the scenario wrote (bisect-bug's .bisect-hint),
 * so their blob id changes with every playground rebuild. Their object ids are
 * masked in the index and status listings.
 */
const VOLATILE_PATHS = ['.bisect-hint']

function maskVolatile(lines: string[]): string[] {
  return lines.map((l) =>
    VOLATILE_PATHS.some((p) => l.endsWith(`\t${p}`) || l.endsWith(` ${p}`)) ? l.replace(/\b[0-9a-f]{7,40}\b/g, '<VOLATILE>') : l
  )
}

/**
 * Normalize a snapshot. The log (ordered by date) and the refs (ordered by
 * name, and bisect ref names embed SHAs) are sorted once SHAs are labels.
 */
function normSnapshot(state: Record<string, unknown>, ctx: Ctx, lab: Labeler): unknown {
  state.index = maskVolatile(state.index as string[])
  state.status = maskVolatile(state.status as string[])
  const n = norm(state, ctx, lab) as Record<string, unknown>
  n.log = [...(n.log as string[])].sort()
  n.refs = [...(n.refs as string[])].sort()
  return n
}

interface Golden {
  method: string
  fixture: string
  case: string
  kind: 'read' | 'write'
  setup?: Step[]
  args: Arg[]
  result?: unknown
  error?: string
  state?: unknown
  events?: string
}

async function capture(c: Case): Promise<Golden> {
  const write = !gitMethodIsRead(c.method)
  const clone = write || !!c.setup?.length || !!c.prepare
  resetGlobalConfig()
  const out = mkdtempSync(join(tmpdir(), 'gitcito-goldens-out-'))
  const repo = clone ? cloneFixture(c.fixture) : repoPath(c.fixture)
  const ctx: Ctx = { repo, out, vars: {} }
  try {
    for (const step of c.setup ?? []) await runStep(step, ctx)
    const args = c.prepare ? await c.prepare(repo) : c.args
    const call = subst(args, ctx) as unknown[]
    const chunks: string[] = []
    if (c.events) call.push((chunk: string) => chunks.push(chunk))
    const golden: Golden = { method: c.method, fixture: c.fixture, case: c.name, kind: write ? 'write' : 'read' }
    if (c.setup?.length) golden.setup = c.setup
    golden.args = JSON.parse(JSON.stringify(args))
    let result: unknown
    let error: string | undefined
    try {
      result = await (gitService as unknown as Svc)[c.method].apply(gitService, call)
    } catch (err) {
      // What the dispatcher hands the renderer: redacted, ANSI-free.
      error = stripAnsi(redactCredentials(err instanceof Error ? err.message : String(err)))
    }
    const lab = new Labeler(repo)
    if (error !== undefined) golden.error = normString(error, ctx, lab)
    else golden.result = norm(JSON.parse(JSON.stringify(result ?? null)), ctx, lab)
    if (write) golden.state = normSnapshot(snapshot(repo), ctx, lab)
    if (c.events) golden.events = normString(chunks.join(''), ctx, lab)
    return golden
  } finally {
    rmSync(out, { recursive: true, force: true })
  }
}

// ─── The suite ──────────────────────────────────────────────────────────────

const saved: Record<string, string | undefined> = {}
const isolated = mkdtempSync(join(tmpdir(), 'gitcito-goldens-home-'))
const home = join(isolated, 'home')
const globalConfig = join(home, '.gitconfig')

/** A fresh global config per case — the content testutil.IsolateGitEnv writes. */
function resetGlobalConfig(): void {
  writeFileSync(
    globalConfig,
    '[user]\n\tname = Gitcito Test\n\temail = test@gitcito.invalid\n[init]\n\tdefaultBranch = main\n' +
      '[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n'
  )
}

beforeAll(() => {
  // The developer's global git config must not decide what a golden says, and
  // a golden must never write to it (setRerere's default scope is global).
  // Same shape as testutil.IsolateGitEnv on the Go side.
  mkdirSync(home, { recursive: true })
  mkdirSync(join(isolated, 'xdg'), { recursive: true })
  const cfg = globalConfig
  resetGlobalConfig()
  const env = { HOME: home, XDG_CONFIG_HOME: join(isolated, 'xdg'), GIT_CONFIG_GLOBAL: cfg, GIT_CONFIG_NOSYSTEM: '1', GIT_TERMINAL_PROMPT: '0' }
  for (const [k, v] of Object.entries(env)) {
    saved[k] = process.env[k]
    process.env[k] = v
  }
})

afterAll(() => {
  for (const [k, v] of Object.entries(saved)) {
    if (v === undefined) delete process.env[k]
    else process.env[k] = v
  }
  rmSync(isolated, { recursive: true, force: true })
  cleanupFixtures()
})

const fileFor = (c: Case): string => join(GOLDENS, c.method, `${c.fixture}-${c.name}.json`)

describe('slice C goldens (TypeScript gitService)', () => {
  it('has one case per file name', () => {
    const names = cases.map(fileFor)
    expect(new Set(names).size).toBe(names.length)
  })

  for (const c of cases) {
    it(`${c.method} ${c.fixture}-${c.name}`, async () => {
      const golden = await capture(c)
      const file = fileFor(c)
      if (WRITE) {
        mkdirSync(dirname(file), { recursive: true })
        writeFileSync(file, JSON.stringify(golden, null, 2) + '\n')
      }
      expect(JSON.parse(readFileSync(file, 'utf8'))).toEqual(JSON.parse(JSON.stringify(golden)))
    })
  }
})
