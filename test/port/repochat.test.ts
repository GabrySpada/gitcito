import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, describe, expect, it, vi } from 'vitest'

/**
 * Port vectors for the repochat group: src/main/repoChat.ts, grounding.ts,
 * wiki.ts, wikiPack.ts, wikiSchemas.ts and the shared modules wiki reads
 * (src/shared/repoFacts.ts, src/shared/importGraph.ts, secretFiles.ts).
 *
 * Two kinds of file:
 * - testdata/vectors/<module>.json — pure functions, `{ fn, args, out }` or
 *   `{ fn, args, throws }`. Sets travel as arrays.
 * - internal/{repochat,wiki}/testdata/payloads.json — the exact requests these
 *   features hand the model (messages, schema as JSON text, name, strict,
 *   maxTokens, nativeStructuredOutput, temperature) for a scripted conversation
 *   over a repository both sides build from the same recipe, plus what the
 *   feature returned, stored and emitted. Prompt wording is product behaviour,
 *   so Go must build the same bytes.
 *
 * The AI transport (src/main/ai.ts) is replaced by a fake that answers from a
 * script, runs the feature's own normalize/validate on each answer, and throws
 * InvalidAIResponse on the first validation error — the Go tests use the same
 * fake against the same script.
 *
 * PORT_WRITE_VECTORS=1 regenerates the files; otherwise this test recomputes
 * everything and fails when the TS output drifts from them.
 */

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const WRITE = process.env.PORT_WRITE_VECTORS === '1'

type Json = unknown
interface ModelCall {
  feature: string
  model: string
  messages: Json
  spec: {
    name: string
    schema: string
    strict: boolean | null
    maxTokens: number | null
    nativeStructuredOutput: boolean | null
    normalize: boolean
  }
  temperature: number
  reply: Json
  errors: string[]
}

const h = vi.hoisted(() => ({
  handlers: new Map<string, (...a: unknown[]) => unknown>(),
  userData: '',
  sent: [] as Array<{ channel: string; payload: unknown }>,
  replies: new Map<string, unknown[]>(),
  calls: [] as unknown[],
  normalize: (v: unknown): unknown => v
}))

vi.mock('electron', () => ({
  ipcMain: { handle: (channel: string, fn: (...a: unknown[]) => unknown) => h.handlers.set(channel, fn) },
  app: { getPath: () => h.userData, isPackaged: false }
}))

vi.mock('../../src/main/ai', () => {
  class InvalidAIResponse extends Error {
    readonly lastReply: string
    constructor(message: string, lastReply = '') {
      super(message)
      this.lastReply = lastReply
    }
  }
  const replyKey = (feature: string, messages: Array<{ content: string }>): string => {
    if (feature !== 'wikiPage') return feature
    const page = /^Page: (.*) \(/.exec(messages[1]?.content ?? '')
    return `wikiPage:${page?.[1] ?? ''}`
  }
  return {
    InvalidAIResponse,
    chatCompleteJson: async (
      cfg: { model?: string },
      messages: Array<{ role: string; content: string }>,
      feature: string,
      spec: {
        name: string
        schema: unknown
        validate: (v: unknown) => string[] | Promise<string[]>
        strict?: boolean
        maxTokens?: number
        nativeStructuredOutput?: boolean
        normalize?: (v: unknown) => unknown
      },
      temperature = 0.2
    ) => {
      const key = replyKey(feature, messages)
      const queue = h.replies.get(key) ?? []
      if (!queue.length) throw new Error(`no scripted reply for ${key}`)
      const reply = queue.shift()
      const parsed = JSON.parse(JSON.stringify(reply))
      const value = spec.normalize ? spec.normalize(parsed) : parsed
      const errors = await spec.validate(value)
      h.calls.push({
        feature,
        model: cfg.model ?? '',
        messages: h.normalize(messages),
        spec: {
          name: spec.name,
          schema: JSON.stringify(spec.schema),
          strict: spec.strict ?? null,
          maxTokens: spec.maxTokens ?? null,
          nativeStructuredOutput: spec.nativeStructuredOutput ?? null,
          normalize: !!spec.normalize
        },
        temperature,
        reply,
        errors: h.normalize(errors)
      })
      if (errors.length) {
        throw new InvalidAIResponse(`The AI returned an invalid response: ${errors[0]}`, JSON.stringify(reply))
      }
      return value
    }
  }
})

const {
  attachmentLabel,
  chatAnswerSchema,
  clipEvidenceBudget,
  contentContainsActionPayload,
  filterRepoChatPaths,
  finalizationMessages,
  normalizeRepoChatAttachments,
  normalizeRepoChatMessages,
  packRepoChatEvidence,
  repoChatActionRules,
  selectSearchEvidence,
  validateChatAnswer,
  validateChatSelection,
  answerRepoChat,
  finalizeRepoChat
} = await import('../../src/main/repoChat')
const grounding = await import('../../src/main/grounding')
const wikiSchemas = await import('../../src/main/wikiSchemas')
const wikiPack = await import('../../src/main/wikiPack')
const repoFacts = await import('../../src/shared/repoFacts')
const importGraph = await import('../../src/shared/importGraph')
const { isSecretFile } = await import('../../src/shared/secretFiles')
const { registerWikiHandlers } = await import('../../src/main/wiki')
const { defaultAIConfig } = await import('../../src/shared/types')

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

interface Case {
  fn: string
  args: unknown[]
  out?: unknown
  throws?: string
}

/** Runs fn(...args) and records the JSON result or the thrown message. */
function rec(cases: Case[], fn: string, args: unknown[], call: () => unknown): void {
  try {
    cases.push({ fn, args: norm(args) as unknown[], out: norm(call()) })
  } catch (err) {
    cases.push({ fn, args: norm(args) as unknown[], throws: (err as Error).message })
  }
}

// ─── grounding.ts ───────────────────────────────────────────────────────────

const DIFF = `diff --git a/src/app.ts b/src/app.ts
index 1111111..2222222 100644
--- a/src/app.ts
+++ b/src/app.ts
@@ -10,3 +10,4 @@ function main() {
   const a = 1
-  return a
+  const b = 2
+  return a + b
 }
diff --git a/README.md b/README.md
index 3333333..4444444 100644
--- a/README.md
+++ b/README.md
@@ -1,2 +1,2 @@
-# Old
+# New
 body
`
const DELETED = `diff --git a/old.ts b/old.ts
deleted file mode 100644
index 5555555..0000000
--- a/old.ts
+++ /dev/null
@@ -1,3 +0,0 @@
-one
-two
-three
`
const ADDED = `diff --git a/new.ts b/new.ts
new file mode 100644
index 0000000..6666666
--- /dev/null
+++ b/new.ts
@@ -0,0 +1,2 @@
+héllo 🌍
+world
\\ No newline at end of file
`
const NO_HEADER = `--- a/x.ts
+++ b/x.ts
@@ -5 +5 @@
-a
+b
@@ -20,0 +21,2 @@ tail
+c
+d
`
const DELETION_HUNK = `diff --git a/d.ts b/d.ts
--- a/d.ts
+++ b/d.ts
@@ -4,3 +4,1 @@
 keep
-gone one
-gone two
`
const CRLF = 'diff --git a/w.ts b/w.ts\r\n--- a/w.ts\r\n+++ b/w.ts\r\n@@ -1,1 +1,1 @@\r\n-a\r\n+b\r\n'

function groundingVectors(): Case[] {
  const c: Case[] = []
  const diffs = [DIFF, DELETED, ADDED, NO_HEADER, DELETION_HUNK, CRLF, '', 'diff --git a/x.png b/x.png\nBinary files differ\n']
  const optsList = [{}, { maxBytes: 60 }, { maxHunks: 1 }, { maxHunkBytes: 20 }, { maxBytes: 0 }]
  for (const d of diffs) {
    for (const o of optsList) {
      rec(c, 'buildDiffEvidence', [d, o], () => grounding.buildDiffEvidence(d, o))
      rec(c, 'serializeEvidence', [d, o], () => grounding.serializeEvidence(grounding.buildDiffEvidence(d, o)))
    }
  }

  const allowed = ['E1', 'E2']
  const finding = (over: Record<string, unknown> = {}): Record<string, unknown> => ({
    kind: 'risk',
    severity: 'high',
    evidenceId: 'E1',
    claim: 'Null is dereferenced.',
    suggestion: 'Guard it.',
    ...over
  })
  const reviews: unknown[] = [
    { summary: 'Adds a helper.', findings: [finding()] },
    { summary: 'x', findings: [finding({ evidenceId: 'E9' })] },
    { summary: 'x', findings: [finding({ kind: 'nit', severity: 'critical', claim: '  ' })] },
    { findings: [] },
    { summary: 'x' },
    { summary: '  ', findings: 'nope' },
    { summary: 'Looks fine.', findings: [] },
    { summary: 'x', findings: [null, 3, 'str', [], finding({ evidenceId: undefined }), finding({ evidenceId: 7 })] },
    null,
    'text',
    [1, 2]
  ]
  for (const r of reviews) {
    rec(c, 'validateReview', [r, allowed], () => grounding.validateReview(r, new Set(allowed)))
    rec(c, 'validateReview', [r, []], () => grounding.validateReview(r, new Set()))
  }

  const findingsList: unknown[][] = [
    [{ kind: 'risk', severity: 'high', evidenceId: 'E1', claim: 'c', suggestion: 's' }],
    [{ kind: 'risk', evidenceId: 'E7', claim: 'c' }],
    [{ kind: 'risk', evidenceId: 'E1', claim: 'c' }],
    [
      { kind: 'risk', severity: 'high', evidenceId: 'E1', claim: 'Leaks a handle.', suggestion: 'Close it.' },
      { kind: 'suggestion', severity: 'low', evidenceId: 'E2', claim: 'Naming.', suggestion: 'Rename b.' },
      { kind: 'other', severity: 'low', evidenceId: 'E2', claim: '  padded  ', suggestion: '  s  ' },
      { kind: 'suggestion', evidenceId: 'E1', claim: 'Only claim.' },
      { kind: 'risk', evidenceId: 'E1', claim: 42, suggestion: 5 },
      { kind: 'risk', evidenceId: 'E1', claim: null },
      { kind: 'risk', evidenceId: 'E1', claim: true },
      { kind: 'risk', evidenceId: 'E1' },
      { kind: 'risk', evidenceId: 'E1', claim: ['a', 'b'] }
    ]
  ]
  for (const f of findingsList) {
    rec(c, 'groundFindings', [f, DIFF], () =>
      grounding.groundFindings(f as never, grounding.evidenceIndex(grounding.buildDiffEvidence(DIFF)))
    )
    for (const kind of ['risk', 'suggestion'] as const) {
      rec(c, 'renderFindings', [f, DIFF, kind], () =>
        grounding.renderFindings(
          grounding.groundFindings(f as never, grounding.evidenceIndex(grounding.buildDiffEvidence(DIFF))),
          kind
        )
      )
    }
  }

  const file200 = Array.from({ length: 200 }, (_, i) => `line ${i + 1} content`).join('\n')
  const lineCases: Array<[string, number, Record<string, number>]> = [
    [file200, 100, { radius: 5 }],
    [file200, 3, { radius: 1 }],
    [file200, 1, { radius: 10 }],
    [file200, 200, { radius: 10 }],
    [file200, 999, { radius: 2 }],
    [file200, 0, { radius: 2 }],
    [file200, -5, {}],
    [file200, 100, { radius: 50, maxBytes: 120 }],
    [file200, 100, {}],
    ['only line', 1, {}],
    ['', 1, {}],
    ['a\nb\nc', 2, { radius: 0 }],
    ['ünïcödé 🌍 line\nsecond', 1, { maxBytes: 10 }]
  ]
  for (const [content, line, opts] of lineCases) {
    rec(c, 'buildLineWindow', [content, line, opts], () => grounding.buildLineWindow(content, line, opts))
  }
  const hunkLines = [
    { no: 10, text: 'function a() {' },
    { no: 11, text: '  return 1' },
    { no: 12, text: '}' },
    { no: 80, text: 'function b() {' },
    { no: 81, text: '  return 2' }
  ]
  for (const [line, opts] of [
    [11, { radius: 1 }],
    [30, { radius: 0 }],
    [70, { radius: 0 }],
    [46, { radius: 1 }],
    [5, {}],
    [81, { radius: 3, maxBytes: 30 }]
  ] as Array<[number, Record<string, number>]>) {
    rec(c, 'buildWindowFromLines', [hunkLines, line, opts], () => grounding.buildWindowFromLines(hunkLines, line, opts))
  }
  rec(c, 'buildWindowFromLines', [[], 5, {}], () => grounding.buildWindowFromLines([], 5))

  const win = grounding.buildLineWindow(Array.from({ length: 30 }, (_, i) => `line ${i + 1}`).join('\n'), 15, {
    radius: 5
  })
  const hovers: unknown[] = [
    { summary: 'A helper.', bullets: ['Used twice.'], lines: [10, 20] },
    { summary: 'A helper.', lines: [42] },
    { summary: '   ' },
    { summary: 'x', bullets: ['a', 'b', 'c', 'd'] },
    { summary: 'Defined elsewhere.' },
    { summary: 'x', bullets: 'no', lines: 'no' },
    { summary: 'x', lines: ['10', 12.5, null, 15] },
    { lines: [], bullets: [] },
    null,
    [1]
  ]
  for (const v of hovers) rec(c, 'validateHoverExplain', [v, win], () => grounding.validateHoverExplain(v, win))

  const known = ['a.ts', 'docs/b.md']
  const plans: unknown[] = [
    { summary: 'Stage a.ts', actions: [{ type: 'stage', files: ['a.ts'], description: 'Stage a.ts' }] },
    { summary: 'x', actions: [{ type: 'stage', files: ['ghost.ts'], description: 'Stage ghost' }] },
    { summary: 'x', actions: [{ type: 'push', description: 'Push it' }] },
    { summary: 'x', actions: [{ type: 'gitignore', patterns: ['*.tsx'], description: 'Ignore tsx' }] },
    { summary: 'x', actions: [{ type: 'commit', description: 'Commit' }] },
    { summary: 'x', actions: [{ type: 'branch', description: 'Branch' }] },
    { summary: '', actions: [], note: 'Nothing to do.' },
    { summary: 1 },
    { summary: 'x', actions: [null, 'x', { type: 'rebase', onto: 'main', description: 'r' }, { type: 'zap' }, {}] },
    {
      summary: 'x',
      actions: [
        { type: 'tag', name: ' ', description: ' ' },
        { type: 'checkout', description: 'c' },
        { type: 'gitignore', patterns: [], description: 'g' },
        { type: 'stage', files: 'a.ts', description: 's' },
        { type: 'unstage', files: [1, 'docs/b.md'], description: 'u' },
        { type: 'stash', description: 'st' },
        { type: 'discard', files: ['a.ts'], description: 'd' }
      ]
    },
    null
  ]
  for (const p of plans) rec(c, 'validateAskPlan', [p, known], () => grounding.validateAskPlan(p, new Set(known)))

  const askActions: Array<[unknown, Record<string, boolean>]> = [
    [[{ type: 'rebase', onto: 'main', description: 'Rebase onto main' }], {}],
    [[{ type: 'rebase', onto: 'main', description: 'Rebase onto main' }], { history: true }],
    [[{ type: 'push', branch: 'x', description: 'Push' }], { history: true }],
    [[{ type: 'push', branch: 'x', description: 'Push' }], { remote: true }],
    [
      [
        { type: 'merge', description: 'm' },
        { type: 'rebase', onto: '  ', description: 'r' },
        { type: 'revert', hashes: [], description: 'rv' },
        { type: 'cherry_pick', hashes: ['abc1234', 'the last one'], description: 'cp' },
        { type: 'revert', description: 'rv' },
        { type: 'pull', mode: 'merge', description: 'p' },
        { type: 'pull', mode: 'ff-only', description: 'p' },
        { type: 'open_pr', description: 'pr' },
        { type: 'open_pr', title: 't', target: 'main', description: 'pr' },
        { type: 'fetch', description: 'f' },
        { type: 'stack_submit', description: 's' }
      ],
      { history: true, remote: true }
    ],
    ['not an array', {}]
  ]
  for (const [v, allow] of askActions) {
    rec(c, 'validateAskActions', [v, ['a.ts'], allow], () => grounding.validateAskActions(v, new Set(['a.ts']), allow))
  }

  const ctx = { workingTreePaths: ['dirty.ts', 'a.ts'], evidencePaths: ['LICENSE'], completePaths: ['LICENSE'] }
  const editFile = {
    type: 'edit_file',
    path: 'LICENSE',
    oldText: 'MIT License',
    newText: 'Apache License',
    description: 'Replace the license heading'
  }
  const rcCases: Array<[unknown, Record<string, unknown>]> = [
    [[{ type: 'stage', files: ['a.ts'], description: 'Stage' }], { ...ctx, allowFileActions: false }],
    [[editFile], { ...ctx, allowFileActions: false }],
    [[editFile, { type: 'stage', files: ['LICENSE'], description: 'Stage LICENSE' }], ctx],
    [[{ type: 'delete_file', path: 'ghost.ts', description: 'Delete it' }], ctx],
    [[{ type: 'stage', files: ['dirty.ts'], description: 'Stage' }, editFile], ctx],
    [[{ type: 'write_file', path: 'LICENSE', content: 'Apache', mode: 'replace', description: 'Replace' }], { ...ctx, completePaths: [] }],
    [
      Array.from({ length: 49 }, (_, i) => ({
        type: 'write_file',
        path: `new-${i}.txt`,
        content: 'x',
        mode: 'create',
        description: 'Create file'
      })),
      ctx
    ],
    [Array.from({ length: 65 }, () => ({ type: 'stage', files: ['a.ts'], description: 's' })), ctx],
    [Array.from({ length: 13 }, () => ({ type: 'stage', files: ['a.ts'], description: 's' })), ctx],
    [[{ type: 'push', branch: 'feature/x', description: 'Publish the branch' }], { ...ctx, allowFileActions: true }],
    [[{ type: 'push', branch: 'feature/x', description: 'Publish the branch' }], { ...ctx, allowRemoteActions: true }],
    [[{ type: 'rebase', onto: 'main', description: 'Rebase onto main' }], ctx],
    [[{ type: 'revert', hashes: ['the last one'], description: 'Revert' }], ctx],
    [[{ type: 'open_pr', title: 'feat: x', description: 'Open it' }], { ...ctx, allowRemoteActions: true }],
    [
      [
        { type: 'edit_file', path: '../escape', oldText: '', newText: 1, replaceAll: 'yes', description: '' },
        { type: 'edit_file', path: 'LICENSE', oldText: 'a', newText: 'b', replaceAll: true, description: 'ok' },
        { type: 'write_file', path: ' new.md ', content: 5, mode: 'append', description: 'w' },
        { type: 'write_file', path: 'new.md', content: 'c', mode: 'create', description: 'w' },
        { type: 'delete_file', path: 7, description: 'd' },
        { type: 'stage', files: ['new.md', 'LICENSE', 'ghost'], description: 'stage' },
        null,
        [1],
        'str',
        { type: 'edit_file', path: 'LICENSE', oldText: 'x', newText: 'y', description: 'late' }
      ],
      ctx
    ],
    ['nope', ctx],
    [[], ctx]
  ]
  for (const [v, cx] of rcCases) {
    const context = {
      ...cx,
      workingTreePaths: new Set(cx.workingTreePaths as string[]),
      evidencePaths: new Set(cx.evidencePaths as string[]),
      completePaths: new Set(cx.completePaths as string[])
    }
    rec(c, 'validateRepoChatActions', [v, cx], () => grounding.validateRepoChatActions(v, context as never))
  }

  const loose = [
    '```json\n{"a":1}\n```',
    'Sure! Here is the answer.',
    '{"summary":"x","sections":[{"heading":"Core","claims":[{"text":"one","sourcePaths":["README.md"]},{"{"text":"two","sourcePaths":["README.md"]}"]}],"related":[]}',
    '{\\"value\\":\\"x\\"}',
    '{\\"summary\\":\\"x\\",\\"sections\\":[{\\"heading\\":\\"Core\\",\\"claims\\":[{\\"text\\":\\"one\\",\\"sourcePaths\\":[\\"README.md\\"]},{\\"{\\"text\\":\\"two\\",\\"sourcePaths\\":[\\"README.md\\"]}\\"]}],\\"related\\":[]}',
    '{"summary":"x","sections":[{"heading":"CI","claims":[{"text":"one","sourcePaths":["src/main/localCi.ts"]}]],"related":[]}',
    '{"summary":"x","sections":[{"heading":"i18n","claims":[{"text":"one","sourcePaths":["a.ts"]},{"{"text":"two","sourcePaths":["b.ts"]}]]],"related":[]}',
    '{"summary":"x","sections":[{"heading":"CLI","claims":":[{"text":"one","sourcePaths":["cli.ts"]}]}],"related":[]}',
    '{"summary":"x","heading":"First","claims":[{"text":"one","sourcePaths":["a.ts"]}]},{"heading":"Second","claims":[{"text":"two","sourcePaths":["b.ts"]}]}],"related":["overview"]}',
    '{"summary":"x","sections":[{"heading":"Security","claims":[{"text":"one","","sourcePaths":["keychain.ts"]}]}],"related":[]}',
    '{"summary":"x","sections":[{"heading":"Renderer","claims":[{"text":"one","sourcePaths":["App.tsx"]}]},{"related":["overview","cli"]}',
    '{"value":"first"}\nWait, here is the corrected object.\n{"value":"second"}\n{"value":"truncated"',
    '{"items":[{"value":"one"},{"{"value":"two"}"]}\nCorrected:\n{"items":[{"value":"three"}]}',
    '{"pages":[{"\n  "slug":"overview","title":"Overview"}]}',
    '{"value":"{\\"nested\\":true}"}',
    '<think>I should answer {"no":1}</think>\n{"a":2}',
    '<THINK>a</think>{"a":3}<think>trailing',
    'prefix </think> {"b":[1,2,3]}',
    '```\n[1, 2, 3]\n```',
    '{"a": 1, "b": [1, 2,]}',
    '{"a": "unterminated',
    "{'single': 'quotes'}",
    '{"n": 1.50, "e": 1e3, "neg": -0, "big": 12345678901234567890}',
    '  {"a":1}  ',
    '{"a":1}\n```',
    '"just a string"',
    '42',
    'null',
    'true',
    '',
    '[',
    '{"x": {"y": [true, false, null]}}',
    '{"emoji":"🌍","esc":"\\u00e9\\n\\t"}',
    'Here: {"a": {"b": 1}} and also [2]',
    '{"a":1}}',
    '{"text": "has } brace and ] bracket", "ok": true'
  ]
  for (const text of loose) rec(c, 'parseLooseJson', [text], () => grounding.parseLooseJson(text))
  return c
}

// ─── wikiPack.ts ────────────────────────────────────────────────────────────

function wikiPackVectors(): Case[] {
  const c: Case[] = []
  const paths = [
    'node_modules/react/index.js', 'dist/bundle.js', 'src/assets/logo.png', 'package-lock.json',
    'target/debug/app', 'a/vendor/lib.rb', 'app.min.js', 'src/app.ts', 'README.md', 'lib/db/pool.rb',
    'Dockerfile', '', 'x'.repeat(401), 'Src/Node_Modules/x.js', 'pkg/.Git/config', 'Pods/x.h', 'a/pods/y.h',
    'file.LOCK', '.hidden', 'noext', 'a.b.MIN.JS', 'thumbs.db', 'docs/Thumbs.DB', 'archive.tar.gz', 'x.tsx'
  ]
  for (const p of paths) rec(c, 'isReadableSource', [p], () => wikiPack.isReadableSource(p))

  const rankCases: Array<[string[], string[], number | undefined]> = [
    [['src/z.ts', 'package.json', 'src/hot.ts', 'README.md', 'src/a/b/c/deep.ts'], ['src/hot.ts'], undefined],
    [['README.md', 'dist/x.js', 'src/a.ts', 'src/b.ts'], [], 2],
    [
      [
        'docs/CONTRIBUTING.md', 'Makefile', 'go.mod', 'b/README.md', 'z.ts', 'Z.ts', 'a/Beta.ts', 'a/alpha.ts',
        'a/b/c/d/e/f/g/h.ts', 'a/b/c/d/e/f.ts', 'src/hot2.ts', 'src/hot1.ts', 'changelog.md', 'ARCHITECTURE.md',
        'a_b.ts', 'a-b.ts', 'a.b.ts', 'a10.ts', 'a9.ts', 'Cargo.toml', 'é.ts', 'e.ts', 'f.ts'
      ],
      ['src/hot1.ts', 'src/hot2.ts', 'ghost.ts'],
      undefined
    ]
  ]
  for (const [p, hot, max] of rankCases) {
    rec(c, 'rankPlanFiles', [p, hot, max ?? null], () => wikiPack.rankPlanFiles(p, hot, max))
  }

  const file = (path: string, size: number): { path: string; content: string } => ({ path, content: 'x'.repeat(size) })
  const packs: Array<[Array<{ path: string; content: string }>, Record<string, number>]> = [
    [[file('a.ts', 500)], { maxFileBytes: 100 }],
    [[file('a.ts', 80), file('b.ts', 80), file('c.ts', 80)], { maxBytes: 170 }],
    [[file('big.ts', 500)], { maxBytes: 10, maxFileBytes: 500 }],
    [[{ path: 'a.ts', content: '   ' }, file('b.ts', 10)], {}],
    [[file('a.ts', 10), file('b.ts', 10)], { maxBytes: 15 }],
    [[{ path: 'u.ts', content: '🌍'.repeat(10) }, { path: 'v.ts', content: 'é\n'.repeat(5) }], { maxFileBytes: 5 }],
    [[], {}]
  ]
  for (const [files, opts] of packs) {
    rec(c, 'buildPagePack', [files, opts], () => wikiPack.buildPagePack(files, opts))
    rec(c, 'serializePack', [files, opts], () => wikiPack.serializePack(wikiPack.buildPagePack(files, opts)))
  }

  const facts = [
    { name: 'demo', branch: 'main', totalCommits: 3, authors: [], hotspots: [] },
    {
      name: 'big',
      branch: '',
      totalCommits: 1000,
      authors: Array.from({ length: 7 }, (_, i) => ({ name: `A${i}`, commits: 10 - i })),
      hotspots: Array.from({ length: 17 }, (_, i) => ({ path: `src/f${i}.ts`, commits: 20 - i }))
    }
  ]
  for (const f of facts) rec(c, 'serializeRepoFacts', [f], () => wikiPack.serializeRepoFacts(f))
  return c
}

// ─── wikiSchemas.ts ─────────────────────────────────────────────────────────

function wikiSchemaVectors(): Case[] {
  const c: Case[] = []
  c.push({ fn: 'schemas', args: [], out: {
    WIKI_PLAN_SCHEMA: JSON.stringify(wikiSchemas.WIKI_PLAN_SCHEMA),
    WIKI_PAGE_SCHEMA: JSON.stringify(wikiSchemas.WIKI_PAGE_SCHEMA),
    TECH_STACK_SCHEMA: JSON.stringify(wikiSchemas.TECH_STACK_SCHEMA),
    WIKI_PROMPT_VERSION: wikiSchemas.WIKI_PROMPT_VERSION,
    MAX_PAGES: wikiSchemas.MAX_PAGES,
    MAX_SECTIONS: wikiSchemas.MAX_SECTIONS,
    MAX_CLAIMS: wikiSchemas.MAX_CLAIMS,
    MAX_STACK_GROUPS: wikiSchemas.MAX_STACK_GROUPS,
    WIKI_EXPORT_DIR: wikiSchemas.WIKI_EXPORT_DIR
  } })

  const known = ['README.md', 'src/app.ts', 'src/db.ts', 'src/ui/panel.tsx']
  const page = (over: Record<string, unknown> = {}): Record<string, unknown> => ({
    slug: 'data-layer',
    title: 'Data layer',
    archetype: 'module',
    scopePaths: ['src/db.ts'],
    ...over
  })
  const overview = page({ slug: 'overview', title: 'Overview', archetype: 'overview', scopePaths: ['README.md'] })
  const plans: unknown[] = [
    { pages: [overview, page()] },
    { pages: [overview, page({ scopePaths: ['src/ghost.ts'] })] },
    { pages: [overview, page({ scopePaths: ['src/db.ts', 'src/ghost.ts'] }), page({ slug: 'ghosts', scopePaths: ['src/ghost.ts'] })] },
    { pages: [page()] },
    { pages: [overview, page({ slug: 'o2', archetype: 'overview' })] },
    { pages: [overview, page(), page()] },
    { pages: [overview, page({ slug: 'Data Layer' })] },
    { pages: [overview, page({ scopePaths: [] })] },
    { pages: [overview, ...Array.from({ length: 20 }, (_, i) => page({ slug: `page-${i}` }))] },
    { pages: [] },
    { pages: 'x' },
    null,
    { pages: [null, 'x', page({ slug: 5, title: ' ', archetype: 'blog', scopePaths: 'a' }), page({ scopePaths: [1, null] })] },
    { pages: [overview, { ...page(), scopePaths: undefined }], extra: true }
  ]
  for (const p of plans) {
    rec(c, 'validateWikiPlan', [p, known], () => wikiSchemas.validateWikiPlan(p, new Set(known)))
    rec(c, 'sanitizeWikiPlan', [p, known], () => wikiSchemas.sanitizeWikiPlan(p, new Set(known)))
  }

  const allowed = { paths: ['src/db.ts', 'README.md'], slugs: ['overview', 'ui'] }
  const good = {
    summary: 'How data is read.',
    sections: [{ heading: 'Queries', claims: [{ text: 'Queries go through a pool.', sourcePaths: ['src/db.ts'] }] }],
    related: ['overview']
  }
  const pages: unknown[] = [
    good,
    { ...good, sections: [{ heading: 'x', claims: [{ text: 'y', sourcePaths: ['src/secret.ts'] }] }] },
    { ...good, sections: [{ heading: 'x', claims: [{ text: 'trust me', sourcePaths: [] }] }] },
    { ...good, related: ['ghost'] },
    { ...good, sections: Array.from({ length: 9 }, () => ({ heading: 'h', claims: [] })) },
    { summary: '', sections: 'x', related: 'y' },
    { summary: 'x', sections: [null, { heading: '', claims: 'c' }, { heading: 'h', claims: [null, { text: '', sourcePaths: [3] }, ...Array.from({ length: 9 }, () => ({ text: 't', sourcePaths: ['README.md'] }))] }] },
    { summary: 'no related', sections: [] },
    { summary: 'x', sections: [], related: [1, 'ui'] },
    null
  ]
  for (const p of pages) {
    rec(c, 'validateWikiPage', [p, allowed], () =>
      wikiSchemas.validateWikiPage(p, { paths: new Set(allowed.paths), slugs: new Set(allowed.slugs) })
    )
  }

  const declared = ['react', 'vitest', 'electron']
  const stackGood = { summary: 'A desktop app.', groups: [{ name: 'UI', items: [{ dep: 'react', role: 'renders the interface' }] }] }
  const stacks: unknown[] = [
    stackGood,
    { ...stackGood, groups: [{ name: 'UI', items: [{ dep: 'vue', role: 'x' }] }] },
    { ...stackGood, groups: [{ name: 'UI', items: [{ dep: 'react', role: 'a' }] }, { name: 'Other', items: [{ dep: 'react', role: 'b' }] }] },
    { ...stackGood, groups: [{ name: 'UI', items: [{ dep: 'react', role: '' }] }] },
    { ...stackGood, groups: Array.from({ length: 9 }, (_, i) => ({ name: `g${i}`, items: [] })) },
    { summary: ' ', groups: 'x' },
    { summary: 'x', groups: [null, { name: '', items: 'i' }, { name: 'n', items: [null, { role: 'r' }, { dep: 3, role: 'r' }] }] },
    null
  ]
  for (const s of stacks) rec(c, 'validateTechStack', [s, declared], () => wikiSchemas.validateTechStack(s, new Set(declared)))

  const wikiPage = {
    slug: 'data',
    title: 'Data layer',
    archetype: 'module' as const,
    summary: 'How data is read.',
    sections: [
      {
        heading: 'Queries',
        claims: [
          { text: 'Queries go through a pool.', sourcePaths: ['src/db.ts'] },
          { text: 'Search is separate.', sourcePaths: ['src/db.ts', 'src/search.ts'] },
          { text: 'Uncited.', sourcePaths: [] }
        ]
      },
      { heading: 'Empty', claims: [] }
    ],
    related: [],
    markdown: ''
  }
  rec(c, 'renderWikiPage', [wikiPage], () => wikiSchemas.renderWikiPage(wikiPage))
  rec(c, 'renderWikiPage', [{ ...wikiPage, sections: [], summary: '' }], () =>
    wikiSchemas.renderWikiPage({ ...wikiPage, sections: [], summary: '' })
  )
  rec(c, 'pageSources', [wikiPage], () => wikiSchemas.pageSources(wikiPage))

  const fresh: Array<[unknown, { headSha: string; model: string }]> = [
    [{ headSha: 'abc', promptVersion: wikiSchemas.WIKI_PROMPT_VERSION, model: 'gpt-4o-mini' }, { headSha: 'abc', model: 'gpt-4o-mini' }],
    [{ headSha: 'abc', promptVersion: wikiSchemas.WIKI_PROMPT_VERSION, model: 'gpt-4o-mini' }, { headSha: 'def', model: 'gpt-4o-mini' }],
    [{ headSha: 'abc', promptVersion: 'wiki.v0', model: 'm' }, { headSha: 'abc', model: 'm' }],
    [{ headSha: 'abc', promptVersion: wikiSchemas.WIKI_PROMPT_VERSION, model: 'old' }, { headSha: 'abc', model: 'new' }],
    [null, { headSha: 'abc', model: 'm' }]
  ]
  for (const [w, cur] of fresh) rec(c, 'wikiFreshness', [w, cur], () => wikiSchemas.wikiFreshness(w as never, cur))

  for (const t of ['Database Module', 'Release process', 'Commit graph  ', 'Overview', 'Reference', '  X page', 'Sync  workflow', 'Section', 'ab section', 'Data\tOverview', '']) {
    rec(c, 'cleanPageTitle', [t], () => wikiSchemas.cleanPageTitle(t))
  }

  const orders = [
    [
      { slug: 'ui', title: 'UI', archetype: 'module', scopePaths: [] },
      { slug: 'overview', title: 'Overview', archetype: 'overview', scopePaths: [] },
      { slug: 'data', title: 'Data', archetype: 'module', scopePaths: [] }
    ],
    [
      { slug: 'b', title: 'beta', archetype: 'module', scopePaths: ['x'] },
      { slug: 'a', title: 'Alpha', archetype: 'workflow', scopePaths: [] },
      { slug: 'c', title: 'alpha', archetype: 'reference', scopePaths: [] },
      { slug: 'd', title: 'Édition', archetype: 'module', scopePaths: [] },
      { slug: 'e', title: 'Zeta', archetype: 'module', scopePaths: [] }
    ]
  ]
  for (const o of orders) rec(c, 'orderPlan', [o], () => wikiSchemas.orderPlan(o as never))

  const wiki = {
    headSha: 'abc1234567',
    generatedAt: Date.UTC(2026, 0, 15),
    model: 'gpt-4o-mini',
    promptVersion: wikiSchemas.WIKI_PROMPT_VERSION,
    stack: null,
    pages: [
      { slug: 'overview', title: 'Overview', archetype: 'overview', summary: 'What this is.', sections: [], related: ['data', 'ghost'], markdown: '# Overview\n\nWhat this is.\n' },
      { slug: 'data', title: 'Data layer', archetype: 'module', summary: 'How data is read.', sections: [], related: [], markdown: '# Data layer\n\nHow data is read.\n\n\n' }
    ]
  }
  rec(c, 'wikiExportFiles', [wiki, 'gitcito'], () => wikiSchemas.wikiExportFiles(wiki as never, 'gitcito'))
  rec(c, 'wikiExportFiles', [{ ...wiki, headSha: '', generatedAt: 1767225600123 }, 'x'], () =>
    wikiSchemas.wikiExportFiles({ ...wiki, headSha: '', generatedAt: 1767225600123 } as never, 'x')
  )
  return c
}

// ─── src/shared/repoFacts.ts + importGraph.ts + secretFiles.ts ──────────────

function repoFactsVectors(): Case[] {
  const c: Case[] = []
  for (const p of ['src/app.tsx', 'lib/thing.rb', 'Dockerfile', 'deploy/Makefile', 'LICENSE', '.bashrc', 'a.CPP', 'CMakeLists.txt', 'x.unknown', 'dir.ts/file', 'Gemfile', 'a.Rakefile']) {
    rec(c, 'languageOf', [p], () => repoFacts.languageOf(p))
  }
  const sized = [
    [{ path: 'a.ts', size: 700 }, { path: 'b.ts', size: 300 }, { path: 'c.py', size: 250 }],
    [{ path: 'a.ts', size: 100 }, { path: 'data.json', size: 9000 }, { path: 'README.md', size: 500 }],
    ['ts', 'py', 'rb', 'go', 'rs', 'java', 'php', 'lua', 'zig', 'dart'].map((ext, i) => ({ path: `f.${ext}`, size: 100 - i })),
    [],
    [{ path: 'a.ts', size: 0 }, { path: 'b.go', size: -5 }, { path: 'c.go', size: 0 }, { path: 'Z.c', size: 10 }, { path: 'y.h', size: 10 }, { path: 'a.cc', size: 10 }]
  ]
  for (const files of sized) {
    rec(c, 'languageBreakdown', [files, {}], () => repoFacts.languageBreakdown(files))
    rec(c, 'languageBreakdown', [files, { includeSupporting: true }], () => repoFacts.languageBreakdown(files, { includeSupporting: true }))
  }
  rec(c, 'findManifests', [['src/app.ts', 'web/package.json', 'package.json', 'Cargo.toml'], null], () =>
    repoFacts.findManifests(['src/app.ts', 'web/package.json', 'package.json', 'Cargo.toml'])
  )
  const many = ['a/b/go.mod', 'z/package.json', 'requirements.txt', 'Gemfile', 'x/pubspec.yaml', 'b/composer.json', 'pyproject.toml', 'a/package.json']
  rec(c, 'findManifests', [many, null], () => repoFacts.findManifests(many))
  rec(c, 'findManifests', [many, 2], () => repoFacts.findManifests(many, 2))

  const manifests: Array<[string, string]> = [
    ['package.json', JSON.stringify({ dependencies: { react: '^18.0.0' }, devDependencies: { vitest: '^2.0.0' } })],
    ['package.json', '{ not json'],
    ['package.json', JSON.stringify({ peerDependencies: { a: 1, b: null }, dependencies: null })],
    ['web/composer.json', JSON.stringify({ dependencies: { 'laravel/framework': '^10' } })],
    ['Cargo.toml', '[dependencies]\nserde = "1.0"\ntokio = { version = "1.35" }\n'],
    ['Cargo.toml', '[package]\nname = "x"\n\n[dev-dependencies]\n# c\nproptest = "1"\n[dependencies.foo]\nversion = "2"\n[target.x.build-dependencies]\ncc = "1.0"\nbare\n'],
    ['go.mod', 'module x\n\nrequire (\n\tgithub.com/foo/bar v1.2.3\n\tgithub.com/b/c v0.1.0 // indirect\n\t// comment\n)\nrequire github.com/x/y v2.0.0\n'],
    ['pyproject.toml', 'dependencies = ["requests>=2.0", "flask"]'],
    ['pyproject.toml', "[project]\ndependencies = [\n  'numpy >= 1.0',\n  \"pandas\",\n]\n[tool.poetry.dependencies]\npython = \"^3.10\"\ndjango = \"^4\"\n[tool.poetry.group.test.dependencies]\npytest = \"*\"\n[tool.poetry.dev-dependencies]\nblack = { version = \"23\" }\n"],
    ['requirements.txt', '# comment\nrequests==2.0\n-e .\n'],
    ['pubspec.yaml', 'dependencies:\n  http: ^1.0.0\n  sdk: flutter\n'],
    ['pubspec.yaml', 'name: x\ndependencies:\n  flutter:\n    sdk: flutter\n  "q": \'2\'\ndev_dependencies:\n  test: any\nflutter:\n  uses: true\n'],
    ['Gemfile', "source 'x'\ngem 'rails', '7.0'\n"],
    ['sub/Gemfile', 'gem "pg"\n  gem \'puma\' , "~> 6"\n# gem "no"\n'],
    ['README.md', 'anything'],
    ['Requirements.TXT', 'flask\r\nnumpy>=1\r\n']
  ]
  for (const [p, content] of manifests) rec(c, 'parseManifest', [p, content], () => repoFacts.parseManifest(p, content))

  for (const n of ['@types/node', 'ts-loader', 'eslint-plugin-react', 'prettier', '@babel/core', 'tslib', 'react', 'electron', 'tailwindcss', 'go-sqlite3', 'eslint', 'eslintx', 'babel-jest', 'vite-plugin-x', 'webpack-cli', 'tsx', 'rollup-plugin-a']) {
    rec(c, 'isNoiseDependency', [n], () => repoFacts.isNoiseDependency(n))
  }
  const deps = (...names: string[]): Array<{ name: string; version: string; dev: boolean }> =>
    names.map((name) => ({ name, version: '1.0.0', dev: false }))
  for (const d of [
    deps('react', 'react-dom', 'vue-router'),
    deps('vue', 'vue-router', '@types/node', 'next', 'next-auth'),
    deps('next', 'react', 'tailwindcss', 'left-pad'),
    deps('react', 'react'),
    deps('Django', 'github.com/labstack/echo/v4', 'tauri-build', 'react-native', 'mattn/go-sqlite3', '@angular/core', '@angular/common', 'jest', 'vitest'),
    []
  ]) {
    rec(c, 'meaningfulDependencies', [d], () => repoFacts.meaningfulDependencies(d))
    rec(c, 'detectFrameworks', [d], () => repoFacts.detectFrameworks(d))
  }
  for (const p of ['.env', 'a/.env.local', '.env.example', 'x/.ENV.sample', 'key.pem', 'id_rsa', 'a/id_ed25519.pub', '.npmrc', 'credentials', 'creds/credentials.json', 'config/secrets.json', 'secret.yml', 'app.p8', 'README.md', '.envrc', 'x.mobileprovision']) {
    rec(c, 'isSecretFile', [p], () => isSecretFile(p))
  }
  return c
}

function importGraphVectors(): Case[] {
  const c: Case[] = []
  const src = `
import { a } from './a'
import b from "../b"
import './side-effect'
export { c } from './c'
const d = require('./d')
const e = await import('./e')
`
  const extracts: Array<[string, string]> = [
    ['src/x.ts', src],
    ['src/x.ts', "// import { x } from './commented'\n/* import './block' */\nimport { y } from './real'"],
    ['app.py', 'from .models import User\nimport os'],
    ['main.go', 'import "example.com/x/y"'],
    ['main.go', 'package main\n\nimport (\n\t"fmt"\n\tlog "github.com/x/log" // logger\n\t"example.com/x/y"\n)\n'],
    ['lib.rs', 'use crate::db::pool;\nmod helpers;'],
    ['lib.rs', 'pub use self::{a, b};\npub mod net;\nuse std::io::*;'],
    ['main.dart', "import 'package:http/http.dart';"],
    ['app.rb', "require_relative 'thing'"],
    ['app.rb', "require 'json'\n  require_relative \"lib/x\""],
    ['README.md', 'import this'],
    ['x.c', '#include <stdio.h>\n#include "local.h"\n'],
    ['x.php', "require_once('vendor/autoload.php');\ninclude 'a.php';"],
    ['x.vue', '<script>\nimport A from "./A.vue"\nimport A2 from "./A.vue"\n</script>'],
    ['u.ts', 'const s = "http://x"; import z from "./z"']
  ]
  for (const [p, content] of extracts) rec(c, 'extractImports', [p, content], () => importGraph.extractImports(p, content))

  const files = ['src/app.ts', 'src/db/pool.ts', 'src/db/index.ts', 'src/ui/panel.tsx', 'app/models/user.py', 'app/__init__.py', 'crate/mod.rs', 'src/net/mod.rs', 'lib/x.rb', 'pkg/util.go']
  const resolves: Array<[string, string]> = [
    ['src/app.ts', './db/pool'],
    ['src/ui/panel.tsx', '../db/pool'],
    ['src/app.ts', './db'],
    ['src/ui/panel.tsx', '@/db/pool'],
    ['src/ui/panel.tsx', '~/src/app'],
    ['app/main.py', 'app.models.user'],
    ['app/models/user.py', '..'],
    ['app/models/user.py', '.user'],
    ['app/models/x.py', '..models.user'],
    ['src/app.ts', 'react'],
    ['main.go', 'fmt'],
    ['src/app.ts', './nope'],
    ['src/lib.rs', 'crate::net'],
    ['src/lib.rs', 'self::net::{a, b}'],
    ['lib/y.rb', 'x'],
    ['a.dart', 'package:http/http.dart'],
    ['src/app.ts', ''],
    ['src/app.ts', '.'],
    ['x.go', 'pkg/util']
  ]
  for (const [from, spec] of resolves) rec(c, 'resolveImport', [from, spec, files], () => importGraph.resolveImport(from, spec, new Set(files)))

  for (const [p, d] of [['a/b/c.ts', 1], ['a/b/c.ts', 2], ['a/b/c.ts', 5], ['c.ts', 2], ['a/b/c.ts', 0]] as Array<[string, number]>) {
    rec(c, 'folderOf', [p, d], () => importGraph.folderOf(p, d))
  }
  const links = [
    { from: 'src/renderer/src/components/A.tsx', to: 'src/renderer/src/stores/ui.ts' },
    { from: 'src/renderer/src/components/B.tsx', to: 'src/shared/types.ts' },
    { from: 'src/main/git.ts', to: 'src/shared/types.ts' }
  ]
  rec(c, 'pickDepth', [links, 14], () => importGraph.pickDepth(links, 14))
  rec(c, 'pickDepth', [links, 3], () => importGraph.pickDepth(links, 3))
  rec(c, 'pickDepth', [[], 14], () => importGraph.pickDepth([], 14))
  rec(c, 'pickDepth', [links, null], () => importGraph.pickDepth(links))

  const repo = [
    { path: 'src/ui/panel.tsx', content: "import { query } from '../db/pool'\nimport './styles'" },
    { path: 'src/ui/styles.ts', content: '' },
    { path: 'src/db/pool.ts', content: "import { log } from '../util/log'" },
    { path: 'src/util/log.ts', content: '' },
    { path: 'src/app.ts', content: "import { Panel } from './ui/panel'\nimport react from 'react'" },
    { path: 'README.md', content: 'import x from "./src/app"' }
  ]
  for (const opts of [{ depth: 2 }, { depth: 1 }, { depth: 2, maxEdges: 1 }, {}, { depth: 0 }, { depth: -1, maxEdges: 0 }]) {
    rec(c, 'buildImportGraph', [repo, opts], () => importGraph.buildImportGraph(repo, opts))
  }
  rec(c, 'buildImportGraph', [[], {}], () => importGraph.buildImportGraph([], {}))
  return c
}

// ─── repoChat.ts pure functions ─────────────────────────────────────────────

function repoChatVectors(): Case[] {
  const c: Case[] = []
  for (const args of [
    [true],
    [false],
    [true, false],
    [true, true, true],
    [true, true, false, true],
    [true, false, true, true],
    [false, false, false, true],
    [true, true, true, true]
  ] as Array<[boolean, boolean?, boolean?, boolean?]>) {
    rec(c, 'chatAnswerSchema', [...args], () => JSON.stringify(chatAnswerSchema(...args)))
  }
  for (const args of [[true, true], [true, false], [false, true], [true, true, true], [true, false, true], [false, false, true]] as Array<[boolean, boolean, boolean?]>) {
    rec(c, 'repoChatActionRules', [...args], () => repoChatActionRules(...args))
  }
  const payloads = [
    '```json\n[{"type":"stage","files":["a.ts"]}]\n```',
    'Example: {"kind":"stage"}',
    [
      'I will add a Quick build section to README.md.',
      '```json',
      '[',
      '  {',
      '    "type": "edit_file",',
      '    "path": "README.md",',
      '    "oldText": "## Install\\nInstall the app.",',
      '    "newText": "## Install\\nInstall the app.\\n\\n## Quick build\\n```sh\\nnpm run build\\n```",',
      '    "description": "Add a Quick build section"',
      '  }',
      ']',
      '```'
    ].join('\n'),
    'The field `"type": "edit_file"` names the action.',
    '{"type":"commit","message":"x","description":"d"}',
    '  [{"type":"nope"}]',
    '{"type": "push"} then "description": here',
    '```JSON\n{"type":"tag","name":"v1"}\n```',
    '```\n{"a":[{"b":{"type":"stage"}}]}\n```',
    '```json {"type":"stage","description":"x"}```',
    'Plain prose about stage and commit.',
    '```json\nnot json "type":"merge" ... "description" :\n```',
    ''
  ]
  for (const p of payloads) rec(c, 'contentContainsActionPayload', [p], () => contentContainsActionPayload(p))

  const hits = [
    Array.from({ length: 17 }, (_, i) => ({ file: `src/i18n/l${i}.ts`, line: i + 1, text: 'MIT' })),
    [{ file: 'a.ts', line: 1, text: 'MIT' }, { file: 'a.ts', line: 20, text: 'MIT' }, { file: 'b.ts', line: 2, text: 'MIT' }],
    Array.from({ length: 60 }, (_, i) => ({ file: `f${i % 50}.ts`, line: i, text: 'x' })),
    []
  ]
  for (const hs of hits) rec(c, 'selectSearchEvidence', [hs], () => selectSearchEvidence(hs))

  const raw = Array.from({ length: 14 }, (_, i) => ({
    role: i % 2 ? 'assistant' : 'user',
    content: `message ${i}`,
    sources: [{ id: 'forged', path: '/tmp/private', startLine: 1, endLine: 1 }]
  }))
  raw[13] = { role: 'user', content: ' latest question ', sources: [] }
  const transcripts: Array<[unknown, boolean?]> = [
    [raw],
    [[{ role: 'assistant', content: 'answer' }]],
    [[{ role: 'assistant', content: 'answer' }], false],
    ['nope'],
    [[{ role: 'system', content: 'x' }]],
    [[{ role: 'user', content: 5 }]],
    [[null]],
    [[{ role: 'user', content: '   ' }]],
    [[{ role: 'user', content: 'x'.repeat(9000) + 'é' }]],
    [[{ role: 'user', content: '🌍'.repeat(5000) }]],
    [[]],
    [[{ role: 'user', content: 'q' }, { role: 'assistant', content: '  ' }]]
  ]
  for (const [v, req] of transcripts) {
    rec(c, 'normalizeRepoChatMessages', [v, req ?? true], () => normalizeRepoChatMessages(v, req ?? true))
  }

  const attachments: Array<[unknown, string]> = [
    [
      [
        { kind: 'file', path: 'src/main/git.ts' },
        { kind: 'commit', hash: 'AbC1234' },
        { kind: 'external', path: '/tmp/notes.md' },
        { kind: 'external', path: '/repo/src/app.ts' },
        { kind: 'file', path: 'src/main/git.ts' }
      ],
      '/repo'
    ],
    [undefined, ''],
    [null, ''],
    [[{ kind: 'file', path: '../escape.ts' }], ''],
    [[{ kind: 'commit', hash: 'not-a-hash' }], ''],
    [[{ kind: 'external', path: 'relative.md' }], ''],
    [{ kind: 'file' }, ''],
    [[{ kind: 'external', path: '/repo' }, { kind: 'external', path: '/repo/../other/x' }, { kind: 'external', path: '/repo/a/../b.ts' }, { kind: 'external', path: '/repo/..x/y' }], '/repo'],
    [[{ kind: 'external', path: '/tmp/a.md' }], ''],
    [Array.from({ length: 10 }, (_, i) => ({ kind: 'file', path: `f${i}.ts` })), ''],
    [[{ kind: 'file', path: 'a\nb' }], ''],
    [[{ kind: 'commit', hash: 'a'.repeat(41) }], ''],
    [[{ kind: 'external', path: '/x\0y' }], ''],
    [[{ kind: 'file', path: 'x' }, { kind: 'external', path: '/repo/x' }], '/repo']
  ]
  for (const [v, repo] of attachments) {
    rec(c, 'normalizeRepoChatAttachments', [v ?? null, repo], () => normalizeRepoChatAttachments(v, repo))
  }
  for (const a of [{ kind: 'commit', hash: 'abcdef1234' }, { kind: 'file', path: 'x.ts' }, { kind: 'external', path: '/a/b' }]) {
    rec(c, 'attachmentLabel', [a], () => attachmentLabel(a as never))
  }

  const filterCases: Array<[string[], string[]]> = [
    [
      ['README.md', 'src/app.ts', '.env', 'config/secrets.json', 'assets/logo.png', 'node_modules/pkg/index.js', 'src/generated/client.ts', 'package-lock.json', '../outside.ts'],
      ['src/app.ts']
    ],
    [['a\nb.ts', 'x.generated.ts', 'gen/x.ts', 'GENERATED/y.ts', 'regen/z.ts', 'ok.ts', '/abs.ts', 'C:/x.ts'], []]
  ]
  for (const [p, ig] of filterCases) rec(c, 'filterRepoChatPaths', [p, ig], () => filterRepoChatPaths(p, new Set(ig)))

  const allowedPaths = ['README.md', 'src/app.ts']
  const selections: unknown[] = [
    { paths: ['src/app.ts'], searches: ['createApp'] },
    { paths: ['../../secret'], searches: [] },
    { paths: [], searches: [] },
    { paths: 'x', searches: 'y' },
    { paths: Array(9).fill('README.md'), searches: Array(6).fill('q') },
    { paths: [5], searches: ['', '   ', 'x'.repeat(121), 7] },
    null,
    []
  ]
  for (const s of selections) rec(c, 'validateChatSelection', [s, allowedPaths], () => validateChatSelection(s, new Set(allowedPaths)))

  const evidence = ['E1']
  const actx = { workingTreePaths: ['a.ts'], evidencePaths: [], completePaths: [] }
  const stage = { type: 'stage', files: ['a.ts'], description: 'Stage a.ts' }
  const answers: Array<[unknown, string[], typeof actx | null]> = [
    [{ content: 'Grounded answer', sourceIds: ['E1'] }, evidence, null],
    [{ content: 'Invented', sourceIds: ['E2'] }, evidence, null],
    [{ content: 'plan', sourceIds: [], actions: [stage] }, evidence, actx],
    [{ content: 'plan', sourceIds: [], actions: [{ ...stage, files: ['ghost.ts'] }] }, evidence, actx],
    [{ content: '', sourceIds: [], actions: [stage] }, evidence, actx],
    [{ content: '', sourceIds: [] }, evidence, actx],
    [{ content: '', sourceIds: [], actions: [] }, evidence, actx],
    [{ content: '', sourceIds: [], actions: [stage] }, evidence, null],
    [{ content: 'plan', sourceIds: [], actions: [stage] }, evidence, null],
    [{ content: 'answer', sourceIds: [] }, evidence, null],
    [{ content: '```json\n[{"type":"stage","files":["a.ts"],"description":"Stage"}]\n```', sourceIds: [] }, evidence, actx],
    [{ content: '', sourceIds: [], needMore: { paths: ['a.ts'], reason: 'need the caller' } }, [], null],
    [{ content: 5, sourceIds: 'x' }, evidence, null],
    [{ content: 'x', sourceIds: Array(13).fill('E1') }, evidence, null],
    [{ content: 'x', sourceIds: [1, null] }, evidence, null],
    [{ content: 'x', sourceIds: [], actions: null }, evidence, actx],
    [{ content: 'x', sourceIds: [], actions: 'nope' }, evidence, actx],
    [null, evidence, null],
    [{ content: '   ', sourceIds: [], needMore: null }, evidence, null]
  ]
  for (const [v, ids, cx] of answers) {
    const context = cx
      ? { workingTreePaths: new Set(cx.workingTreePaths), evidencePaths: new Set(cx.evidencePaths), completePaths: new Set(cx.completePaths) }
      : null
    rec(c, 'validateChatAnswer', [v, ids, cx], () => validateChatAnswer(v, new Set(ids), context))
  }

  const status = {
    current: 'main',
    tracking: 'origin/main',
    ahead: 1,
    behind: 0,
    staged: [{ path: 'LICENSE', status: 'M' }],
    unstaged: [{ path: 'a.ts', status: 'M' }, { path: 'n.ts', status: '?', untracked: true }],
    conflicted: []
  }
  const execution = {
    applied: 1,
    failedIndex: 1,
    failedType: 'commit',
    error: { code: 'hook_failed' },
    remaining: 2,
    actionResults: [
      { index: 0, type: 'edit_file', status: 'done' },
      { index: 1, type: 'commit', status: 'failed' }
    ]
  }
  rec(c, 'finalizationMessages', [[{ role: 'user', content: 'Change the license.' }], status, execution], () =>
    finalizationMessages([{ role: 'user', content: 'Change the license.' }], status as never, execution as never)
  )
  const longHistory = Array.from({ length: 6 }, (_, i) => ({ role: i % 2 ? 'assistant' : 'user', content: `${i}:` + 'y'.repeat(5000) }))
  rec(c, 'finalizationMessages', [longHistory, { ...status, current: '' }, { applied: 0, remaining: 0, actionResults: [] }], () =>
    finalizationMessages(longHistory as never, { ...status, current: '' } as never, { applied: 0, remaining: 0, actionResults: [] })
  )

  const packs: Array<[unknown[], number | null]> = [
    [[{ path: 'a.ts', startLine: 10, endLine: 20, text: 'one\ntwo\nthree' }, { path: 'b.ts', startLine: 1, endLine: 10, text: 'abcdefghij' }], 7],
    [[
      { path: 'a.ts', startLine: 1, endLine: 3, text: '   ' },
      { path: 'b.ts', startLine: 1, endLine: 2, text: 'x\ny', complete: true, pinned: true },
      { path: 'c.ts', startLine: 5, endLine: 50, text: 'z\n'.repeat(10), complete: true },
      { path: '/abs/x.md', startLine: 1, endLine: 1, text: 'ext', external: true, complete: false }
    ], null],
    [[{ path: 'u.ts', startLine: 1, endLine: 1, text: '🌍🌍🌍' }], 3],
    [[{ path: 'x', startLine: 1, endLine: 1, text: 'abc' }], 0]
  ]
  for (const [items, max] of packs) {
    rec(c, 'packRepoChatEvidence', [items, max], () => packRepoChatEvidence(items as never, max ?? undefined))
    rec(c, 'clipEvidenceBudget', [items, max ?? 20000], () => clipEvidenceBudget(items as never, max ?? 20000))
  }
  return c
}

// ─── I/O payloads: a repository both sides build from one recipe ────────────

/** Fixed dates and an isolated config: same recipe, same hashes, on both sides. */
const GIT_ENV: Record<string, string> = {
  GIT_AUTHOR_NAME: 'Port Tester',
  GIT_AUTHOR_EMAIL: 'port@example.invalid',
  GIT_COMMITTER_NAME: 'Port Tester',
  GIT_COMMITTER_EMAIL: 'port@example.invalid',
  GIT_CONFIG_NOSYSTEM: '1',
  GIT_CONFIG_GLOBAL: '/dev/null'
}

type RecipeStep = { write: string; content: string } | { git: string[]; date?: string }

const RECIPE: RecipeStep[] = [
  { git: ['init', '-q', '-b', 'main'] },
  { git: ['config', 'commit.gpgsign', 'false'] },
  { write: 'README.md', content: '# Demo\n\nA tiny demo app. Run `createApp()` to start.\n' },
  { write: 'LICENSE', content: 'MIT License\n\nCopyright Demo\n' },
  {
    write: 'package.json',
    content: JSON.stringify(
      {
        name: 'demo',
        dependencies: { react: '^18.0.0', 'react-dom': '^18.0.0', express: '^4.0.0' },
        devDependencies: { vitest: '^2.0.0', '@types/node': '^20.0.0', typescript: '^5.0.0' }
      },
      null,
      2
    ) + '\n'
  },
  { write: 'requirements.txt', content: 'requests==2.0\n' },
  {
    write: 'src/app.ts',
    content: "import { query } from './db'\nimport { log } from './util/log'\n\nexport function createApp(): void {\n  log('start')\n  query('select 1')\n}\n"
  },
  {
    write: 'src/db.ts',
    content: "import { log } from './util/log'\n\nexport function query(sql: string): string {\n  log(sql)\n  return sql\n}\n"
  },
  { write: 'src/util/log.ts', content: 'export function log(message: string): void {\n  console.log(message)\n}\n' },
  { write: 'src/ui/panel.tsx', content: "import { createApp } from '../app'\nexport const Panel = (): null => { createApp(); return null }\n" },
  { write: '.env', content: 'TOKEN=secret\n' },
  { write: 'src/generated/client.ts', content: 'export const x = 1\n' },
  { write: 'assets/logo.png', content: '\u0089PNG\u0000\u0001\u0002' },
  { write: '.gitignore', content: '*.log\nignored-tracked.txt\n' },
  { write: 'ignored-tracked.txt', content: 'tracked although ignored\n' },
  { git: ['add', '-A'] },
  { git: ['add', '-f', 'ignored-tracked.txt'] },
  { git: ['commit', '-q', '-m', 'feat: initial demo'], date: '2026-01-01T00:00:00Z' },
  {
    write: 'src/app.ts',
    content: "import { query } from './db'\nimport { log } from './util/log'\n\nexport const VERSION = '1.0'\n\nexport function createApp(): void {\n  log('start')\n  query('select 1')\n}\n"
  },
  { git: ['commit', '-q', '-am', 'feat: add version'], date: '2026-01-02T00:00:00Z' },
  { git: ['remote', 'add', 'origin', 'https://example.invalid/demo.git'] },
  {
    write: 'src/db.ts',
    content: "import { log } from './util/log'\n\nexport function query(sql: string): string {\n  log(sql)\n  return sql\n}\nexport const POOL = 4\n"
  },
  { write: 'README.md', content: '# Demo\n\nA tiny demo app. Run `createApp()` to start.\n\nMore docs.\n' },
  { git: ['add', 'README.md'] },
  { write: 'new.txt', content: 'hello\n' },
  { write: '@TMP/notes.md', content: 'Outside notes about createApp.\n' },
  { write: '@TMP/id_rsa', content: 'not a key\n' }
]

function buildRepo(recipe: RecipeStep[]): { tmp: string; repo: string } {
  const tmp = mkdtempSync(join(tmpdir(), 'gitcito-port-repochat-'))
  const repo = join(tmp, 'demo-repo')
  mkdirSync(repo)
  for (const step of recipe) {
    if ('write' in step) {
      const target = step.write.startsWith('@TMP/') ? join(tmp, step.write.slice(5)) : join(repo, step.write)
      mkdirSync(dirname(target), { recursive: true })
      writeFileSync(target, Buffer.from(step.content, 'utf8'))
    } else {
      const env = { ...process.env, ...GIT_ENV, ...(step.date ? { GIT_AUTHOR_DATE: step.date, GIT_COMMITTER_DATE: step.date } : {}) }
      execFileSync('git', ['-C', repo, ...step.git], { env })
    }
  }
  return { tmp, repo }
}
const roots: string[] = []
afterAll(() => roots.forEach((r) => rmSync(r, { recursive: true, force: true })))

function setGitEnv(): () => void {
  const saved: Record<string, string | undefined> = {}
  for (const [k, v] of Object.entries(GIT_ENV)) {
    saved[k] = process.env[k]
    process.env[k] = v
  }
  return () => {
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k]
      else process.env[k] = v
    }
  }
}

function revParse(repo: string, ref: string): string {
  return execFileSync('git', ['-C', repo, 'rev-parse', ref], { env: { ...process.env, ...GIT_ENV } })
    .toString()
    .trim()
}

/** Replaces @HEAD / @HEAD~1 / @TMP / @REPO tokens in a scenario input. */
function bind(value: unknown, tmp: string, repo: string): unknown {
  const s = JSON.stringify(value ?? null)
  const out = s
    .replace(/@HEAD~1/g, () => revParse(repo, 'HEAD~1'))
    .replace(/@HEAD/g, () => revParse(repo, 'HEAD'))
    .replace(/@REPO/g, () => repo)
    .replace(/@TMP/g, () => tmp)
  return value === undefined ? undefined : JSON.parse(out)
}

/** Paths and hashes back to tokens, so the recorded file is machine independent. */
function unbind(value: unknown, tmp: string, repo: string): unknown {
  if (value === undefined) return null
  const head = revParse(repo, 'HEAD')
  const prev = revParse(repo, 'HEAD~1')
  const s = JSON.stringify(value)
    .split(repo).join('@REPO')
    .split(tmp).join('@TMP')
    .split(head).join('@HEAD')
    .split(prev).join('@HEAD~1')
  return JSON.parse(s)
}

const baseCfg = (over: Record<string, unknown> = {}): Record<string, unknown> => ({
  ...(defaultAIConfig() as unknown as Record<string, unknown>),
  apiKey: '',
  model: 'gpt-test',
  ...over
})

interface ChatScenario {
  name: string
  fn: 'answer' | 'finalize'
  repoPath: unknown
  messages: unknown
  cfg: Record<string, unknown> | null
  attachments?: unknown
  execution?: unknown
  replies: Record<string, unknown[]>
}

const ANSWER_REPLY = { content: 'The app starts in `createApp`.', sourceIds: ['E1', 'E2', 'E1', 'E9'] }

const CHAT_SCENARIOS: ChatScenario[] = [
  {
    name: 'read-only answer',
    fn: 'answer',
    repoPath: '@REPO',
    messages: [
      { role: 'user', content: 'Where does the app start?' },
      { role: 'assistant', content: 'Earlier answer.' },
      { role: 'user', content: '  And what does it log?  ' }
    ],
    cfg: baseCfg({ repoChatActions: false, customInstructions: '  Answer briefly.  ' }),
    replies: {
      repoChatPlan: [{ paths: ['src/app.ts', 'README.md', 'src/app.ts'], searches: ['createApp', ' log( ', 'createApp'] }],
      repoChatAnswer: [ANSWER_REPLY]
    }
  },
  {
    name: 'pinned context, a second look, then file and git actions',
    fn: 'answer',
    repoPath: '@REPO/',
    messages: [{ role: 'user', content: 'Switch the license to Apache and stage it.' }],
    cfg: baseCfg({ repoChatActions: true, repoChatReadOnly: false }),
    attachments: [
      { kind: 'file', path: 'src/db.ts' },
      { kind: 'commit', hash: '@HEAD' },
      { kind: 'external', path: '@TMP/notes.md' },
      { kind: 'file', path: '.env' },
      { kind: 'external', path: '@TMP/id_rsa' },
      { kind: 'external', path: '@TMP/missing.md' },
      { kind: 'external', path: '@REPO/LICENSE' },
      { kind: 'commit', hash: 'deadbeef' }
    ],
    replies: {
      repoChatPlan: [{ paths: ['README.md'], searches: ['createApp'] }],
      repoChatAnswer: [
        {
          content: 'Looking further.',
          sourceIds: [],
          needMore: { paths: ['src/util/log.ts', 'README.md', 'ghost.ts', 5], searches: ['log(', ' ', 'POOL'], commits: ['@HEAD~1', 'zz'], reason: 'need logger' }
        },
        {
          content: '',
          sourceIds: ['E1'],
          actions: [
            { type: 'edit_file', path: 'LICENSE', oldText: 'MIT License', newText: 'Apache License', description: 'Switch to Apache' },
            { type: 'write_file', path: 'docs/new.md', content: '# New\n', mode: 'create', description: 'Add a doc' },
            { type: 'edit_file', path: 'LICENSE', oldText: 'Apache', newText: 'Apache 2.0', replaceAll: true, description: 'Pin the version' },
            { type: 'stage', files: ['LICENSE', 'docs/new.md', 'new.txt'], description: 'Stage the change' },
            { type: 'commit', message: 'chore: relicense', description: 'Commit it' }
          ]
        }
      ]
    }
  },
  {
    name: 'ungrounded action is rejected',
    fn: 'answer',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'Delete ghost.ts' }],
    cfg: baseCfg({ repoChatReadOnly: false }),
    replies: {
      repoChatPlan: [{ paths: ['README.md'], searches: [] }],
      repoChatAnswer: [{ content: 'Deleting.', sourceIds: [], actions: [{ type: 'delete_file', path: 'ghost.ts', description: 'Delete it' }] }]
    }
  },
  {
    name: 'file action fails to prepare',
    fn: 'answer',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'Edit the readme' }],
    cfg: baseCfg({ repoChatReadOnly: false }),
    replies: {
      repoChatPlan: [{ paths: ['README.md'], searches: [] }],
      repoChatAnswer: [
        { content: 'Editing.', sourceIds: [], actions: [{ type: 'edit_file', path: 'README.md', oldText: 'Not there', newText: 'x', description: 'Edit' }] }
      ]
    }
  },
  {
    name: 'remote and history actions in file read-only mode',
    fn: 'answer',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'Publish main and merge it.' }],
    cfg: baseCfg({ repoChatRemoteActions: true }),
    replies: {
      repoChatPlan: [{ paths: [], searches: ['VERSION'] }],
      repoChatAnswer: [
        {
          content: 'Publishing.',
          sourceIds: ['E1'],
          needMore: {},
          actions: [
            { type: 'push', branch: 'main', remote: 'origin', description: 'Publish main' },
            { type: 'merge', ref: 'main', noFf: false, description: 'Merge main' },
            { type: 'revert', hashes: ['@HEAD~1'], description: 'Revert the first commit' }
          ]
        }
      ]
    }
  },
  {
    name: 'committed only',
    fn: 'answer',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'What is POOL?' }],
    cfg: baseCfg({ repoChatActions: false, repoChatCommittedOnly: true }),
    attachments: [{ kind: 'file', path: 'src/db.ts' }],
    replies: {
      repoChatPlan: [{ paths: ['src/db.ts'], searches: ['POOL'] }],
      repoChatAnswer: [
        { content: 'Asking.', sourceIds: [], needMore: { paths: ['src/app.ts'] } },
        { content: 'Still asking.', sourceIds: [], needMore: { searches: ['nothing-matches-this'] } },
        { content: 'POOL is not committed.', sourceIds: ['E1'] }
      ]
    }
  },
  {
    name: 'three rounds then a forced answer',
    fn: 'answer',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'Trace the logger.' }],
    cfg: baseCfg({ repoChatActions: false }),
    replies: {
      repoChatPlan: [{ paths: ['src/app.ts'], searches: [] }],
      repoChatAnswer: [
        { content: 'more', sourceIds: [], needMore: { paths: ['src/db.ts'] } },
        { content: 'more', sourceIds: [], needMore: { paths: ['src/util/log.ts'] } },
        { content: 'Final.', sourceIds: ['E1', 'E3'] }
      ]
    }
  },
  { name: 'relative repo path', fn: 'answer', repoPath: 'relative', messages: [], cfg: baseCfg(), replies: {} },
  { name: 'non-string repo path', fn: 'answer', repoPath: 5, messages: [], cfg: baseCfg(), replies: {} },
  { name: 'ai disabled', fn: 'answer', repoPath: '@REPO', messages: [], cfg: baseCfg({ enabled: false }), replies: {} },
  { name: 'no config', fn: 'answer', repoPath: '@REPO', messages: [], cfg: null, replies: {} },
  { name: 'chat disabled', fn: 'answer', repoPath: '@REPO', messages: [], cfg: baseCfg({ repoChat: false }), replies: {} },
  { name: 'bad transcript', fn: 'answer', repoPath: '@REPO', messages: [{ role: 'assistant', content: 'x' }], cfg: baseCfg(), replies: {} },
  { name: 'bad attachments', fn: 'answer', repoPath: '@REPO', messages: [{ role: 'user', content: 'x' }], cfg: baseCfg(), attachments: 'x', replies: {} },
  {
    name: 'finalize',
    fn: 'finalize',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'Change the license.' }, { role: 'assistant', content: 'Proposed.' }],
    cfg: baseCfg(),
    execution: {
      applied: 1,
      failedIndex: 1,
      failedType: 'commit',
      error: { code: 'hook_failed', detail: 'dropped' },
      remaining: 2,
      actionResults: [
        { index: 0, type: 'edit_file', status: 'done', extra: 1 },
        { index: 1, type: 'commit', status: 'failed' }
      ],
      snapshot: { sha: 'x', head: 'y' }
    },
    replies: { repoChatFinalize: [{ content: '  Applied one action; the commit hook failed.  ', sourceIds: [] }] }
  },
  {
    name: 'finalize with odd fields',
    fn: 'finalize',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'x' }],
    cfg: baseCfg(),
    execution: { applied: 0, failedIndex: 1.5, failedType: 'zap', remaining: 0, actionResults: [] },
    replies: { repoChatFinalize: [{ content: 'Nothing ran.', sourceIds: [] }] }
  },
  {
    name: 'finalize rejects sources',
    fn: 'finalize',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'x' }],
    cfg: baseCfg(),
    execution: { applied: 0, remaining: 0, actionResults: [] },
    replies: { repoChatFinalize: [{ content: 'x', sourceIds: ['E1'] }] }
  },
  { name: 'finalize bad execution', fn: 'finalize', repoPath: '@REPO', messages: [{ role: 'user', content: 'x' }], cfg: baseCfg(), execution: { applied: -1 }, replies: {} },
  { name: 'finalize bad remaining', fn: 'finalize', repoPath: '@REPO', messages: [{ role: 'user', content: 'x' }], cfg: baseCfg(), execution: { applied: 0, remaining: 1 }, replies: {} },
  {
    name: 'finalize bad action result',
    fn: 'finalize',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'x' }],
    cfg: baseCfg(),
    execution: { applied: 0, remaining: 0, actionResults: [{ index: 0, type: 'stage', status: 'maybe' }] },
    replies: {}
  },
  {
    name: 'finalize bad error',
    fn: 'finalize',
    repoPath: '@REPO',
    messages: [{ role: 'user', content: 'x' }],
    cfg: baseCfg(),
    execution: { applied: 0, remaining: 0, actionResults: [], error: null },
    replies: {}
  },
  { name: 'finalize empty transcript', fn: 'finalize', repoPath: '@REPO', messages: [], cfg: baseCfg(), execution: {}, replies: {} }
]

async function chatPayloads(): Promise<unknown> {
  const restore = setGitEnv()
  const { tmp, repo } = buildRepo(RECIPE)
  roots.push(tmp)
  h.normalize = (v) => unbind(v, tmp, repo)
  const results: unknown[] = []
  try {
    for (const sc of CHAT_SCENARIOS) {
      h.calls = []
      h.replies = new Map(Object.entries(bind(sc.replies, tmp, repo) as Record<string, unknown[]>))
      const repoPath = bind(sc.repoPath, tmp, repo)
      const messages = bind(sc.messages, tmp, repo)
      const attachments = sc.attachments === undefined ? undefined : bind(sc.attachments, tmp, repo)
      let out: unknown = null
      let error: string | null = null
      try {
        out =
          sc.fn === 'answer'
            ? await answerRepoChat(repoPath, messages, sc.cfg as never, attachments)
            : await finalizeRepoChat(repoPath, messages, bind(sc.execution, tmp, repo), sc.cfg as never)
      } catch (err) {
        error = (err as Error).message
      }
      results.push({
        name: sc.name,
        fn: sc.fn,
        repoPath: sc.repoPath,
        messages: sc.messages,
        cfg: sc.cfg,
        attachments: sc.attachments ?? null,
        execution: sc.execution ?? null,
        replies: sc.replies,
        calls: h.calls,
        result: unbind(out, tmp, repo),
        error: error === null ? null : unbind(error, tmp, repo)
      })
    }
  } finally {
    restore()
  }
  return { gitEnv: GIT_ENV, recipe: RECIPE, scenarios: results }
}

// ─── wiki ───────────────────────────────────────────────────────────────────

const WIKI_NOW = Date.UTC(2026, 0, 15, 12, 0, 0)

interface WikiStep {
  channel: string
  args: unknown[]
  replies?: Record<string, unknown[]>
}

const WIKI_PLAN = {
  pages: [
    { slug: 'data-layer', title: 'Data layer Module', archetype: 'module', scopePaths: ['src/db.ts', 'ghost.ts'] },
    { slug: 'overview', title: 'Overview', archetype: 'overview', scopePaths: ['README.md', 'package.json'] },
    { slug: 'ghosts', title: 'Ghosts', archetype: 'module', scopePaths: ['nope.ts'] },
    { slug: 'app', title: 'App startup workflow', archetype: 'workflow', scopePaths: ['src/app.ts', 'src/util/log.ts'] }
  ]
}

const WIKI_STEPS: WikiStep[] = [
  { channel: 'wiki:facts', args: ['@REPO'] },
  { channel: 'wiki:imports', args: ['@REPO', 0] },
  { channel: 'wiki:imports', args: ['@REPO', 2] },
  { channel: 'wiki:get', args: ['@REPO', 'gpt-test'] },
  { channel: 'wiki:export', args: ['@REPO'] },
  {
    channel: 'wiki:generate',
    args: ['@REPO', { ...baseCfg({ provider: 'openai' }) }],
    replies: {
      wikiPlan: [WIKI_PLAN],
      'wikiPage:Data layer': [
        { summary: ' How data is read. ', sections: [{ heading: 'Queries', claims: [{ text: 'Queries are logged.', sourcePaths: ['src/db.ts'] }] }], related: ['overview'] }
      ],
      'wikiPage:Overview': [
        { summary: 'A tiny demo.', sections: [{ heading: 'What it is', claims: [{ text: 'A demo app.', sourcePaths: ['README.md', 'package.json'] }] }], related: ['app', 'data-layer'] }
      ],
      'wikiPage:App startup': [{ summary: 'How the app starts.', sections: [], related: [] }],
      wikiStack: [{ summary: 'A Node app with React.', groups: [{ name: 'Frontend', items: [{ dep: 'react', role: 'renders the UI' }] }, { name: 'Backend', items: [{ dep: 'express', role: 'serves requests' }] }] }]
    }
  },
  { channel: 'wiki:get', args: ['@REPO', 'gpt-test'] },
  { channel: 'wiki:get', args: ['@REPO', 'other-model'] },
  { channel: 'wiki:export', args: ['@REPO'] },
  {
    channel: 'wiki:generate',
    args: ['@REPO', { ...baseCfg({ provider: 'ollama', model: '' }) }],
    replies: {
      wikiPlan: [{ pages: [{ slug: 'overview', title: 'Overview', archetype: 'overview', scopePaths: [] }] }],
      'wikiPage:Overview': [{ summary: 'Just an overview.', sections: [], related: [] }],
      wikiStack: [{ summary: 'x', groups: [{ name: 'UI', items: [{ dep: 'vue', role: 'x' }] }] }]
    }
  },
  { channel: 'wiki:get', args: ['@REPO', ''] },
  {
    channel: 'wiki:generate',
    args: ['@REPO', { ...baseCfg({ provider: 'custom' }) }],
    replies: { wikiPlan: [{ pages: [] }] }
  },
  { channel: 'wiki:generate', args: ['@EMPTY', { ...baseCfg() }] },
  { channel: 'wiki:clear', args: ['@REPO'] },
  { channel: 'wiki:get', args: ['@REPO', ''] },
  { channel: 'wiki:clear', args: ['@REPO'] }
]

/** The page key of a wikiPage call: the first line of its user message. */
const pageKey = (call: ModelCall): string =>
  String((call.messages as Array<{ content: string }>)[1]?.content ?? '').split('\n')[0]

/**
 * Pages are written three at a time over real git reads, so the order they
 * reach the model — and finish — differs from run to run, in TS as in Go. The
 * plan and stack calls keep their place; the page calls between them are sorted.
 */
function orderWikiCalls(calls: ModelCall[]): ModelCall[] {
  const pages = calls.filter((c) => c.feature === 'wikiPage').sort((a, b) => (pageKey(a) < pageKey(b) ? -1 : 1))
  let next = 0
  return calls.map((c) => (c.feature === 'wikiPage' ? pages[next++] : c))
}

/**
 * Page progress events in completion order carry a running `done` count. The
 * recorded form sorts them by slug, drops `done`, and keeps the multiset of
 * counts separately — what any completion order must produce.
 */
function orderWikiEvents(events: Array<{ channel: string; payload: unknown }>): unknown {
  type Progress = { repoPath: string; progress: { phase: string; slug?: string; done?: number } }
  const isPage = (e: { payload: unknown }): boolean => (e.payload as Progress).progress.phase === 'page'
  const pages = events.filter(isPage)
  const done = pages.map((e) => (e.payload as Progress).progress.done).sort()
  const sorted = pages
    .map((e) => {
      const p = e.payload as Progress
      const { done: _done, ...rest } = p.progress
      return { channel: e.channel, payload: { ...p, progress: rest } }
    })
    .sort((a, b) => ((a.payload.progress.slug ?? '') < (b.payload.progress.slug ?? '') ? -1 : 1))
  let next = 0
  return { sequence: events.map((e) => (isPage(e) ? sorted[next++] : e)), done }
}

async function wikiPayloads(): Promise<unknown> {
  const restore = setGitEnv()
  const { tmp, repo } = buildRepo(RECIPE)
  roots.push(tmp)
  const empty = join(tmp, 'empty-repo')
  mkdirSync(empty)
  execFileSync('git', ['-C', empty, 'init', '-q', '-b', 'main'], { env: { ...process.env, ...GIT_ENV } })
  h.userData = join(tmp, 'userData')
  const unb = (v: unknown): unknown => {
    const out = unbind(v, tmp, repo)
    return JSON.parse(JSON.stringify(out).split('@TMP/empty-repo').join('@EMPTY'))
  }
  h.normalize = unb
  const sender = { isDestroyed: () => false, send: (channel: string, payload: unknown) => h.sent.push({ channel, payload: unb(payload) }) }
  const now = vi.spyOn(Date, 'now').mockReturnValue(WIKI_NOW)
  h.handlers.clear()
  registerWikiHandlers()
  const results: unknown[] = []
  try {
    for (const step of WIKI_STEPS) {
      h.calls = []
      h.sent = []
      h.replies = new Map(Object.entries((step.replies ?? {}) as Record<string, unknown[]>))
      const args = JSON.parse(JSON.stringify(bind(step.args, tmp, repo)).split(`${tmp}/empty-repo`).join(empty).split('@EMPTY').join(empty))
      let out: unknown = null
      let error: string | null = null
      try {
        out = await h.handlers.get(step.channel)!({ sender }, ...args)
      } catch (err) {
        error = (err as Error).message
      }
      const storeFile = join(h.userData, 'gitcito-wiki.json')
      const exported: Record<string, string> = {}
      if (step.channel === 'wiki:export' && Array.isArray(out)) {
        for (const p of out as string[]) exported[p] = readFileSync(join(repo, p), 'utf-8')
      }
      results.push({
        channel: step.channel,
        args: step.args,
        replies: step.replies ?? {},
        calls: orderWikiCalls(h.calls as ModelCall[]),
        events: orderWikiEvents(h.sent),
        result: unb(out),
        error: error === null ? null : unb(error),
        exported,
        stored: existsSync(storeFile) ? unb(JSON.parse(readFileSync(storeFile, 'utf-8'))) : null
      })
    }
  } finally {
    now.mockRestore()
    restore()
  }
  return { gitEnv: GIT_ENV, recipe: RECIPE, now: WIKI_NOW, steps: results }
}

describe('repochat port vectors', () => {
  it('grounding', () => check('testdata/vectors/grounding.json', { cases: groundingVectors() }))
  it('wikiPack', () => check('testdata/vectors/wikiPack.json', { cases: wikiPackVectors() }))
  it('wikiSchemas', () => check('testdata/vectors/wikiSchemas.json', { cases: wikiSchemaVectors() }))
  it('repoFacts, secretFiles', () => check('testdata/vectors/repoFacts.json', { cases: repoFactsVectors() }))
  it('importGraph', () => check('testdata/vectors/importGraph.json', { cases: importGraphVectors() }))
  it('repoChat', () => check('testdata/vectors/repoChat.json', { cases: repoChatVectors() }))
  it('repository chat model payloads', async () => {
    check('internal/repochat/testdata/payloads.json', await chatPayloads())
  }, 60_000)
  it('wiki model payloads, storage and events', async () => {
    check('internal/wiki/testdata/payloads.json', await wikiPayloads())
  }, 60_000)
})
