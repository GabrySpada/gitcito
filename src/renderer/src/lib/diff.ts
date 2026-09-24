// Pure diff parsing + word-level + split-view helpers, extracted from
// DiffViewer so they can be unit-tested without rendering.

export interface DiffLine {
  kind: 'add' | 'del' | 'hunk' | 'meta' | 'ctx'
  text: string
  oldNo: number | null
  newNo: number | null
  hunkIdx: number
}

export type Range = [number, number] // [start, end) in decoded-character coords

/** Tokenize into words / whitespace runs / single punctuation for word-diffing. */
export function tokenize(s: string): string[] {
  return s.match(/\s+|[A-Za-z0-9_]+|[^\sA-Za-z0-9_]/g) ?? []
}

/**
 * Word-level diff of two lines via an LCS over tokens. Returns the changed
 * character ranges on each side (delRanges over `a`, addRanges over `b`).
 */
export function wordDiff(a: string, b: string): { del: Range[]; add: Range[] } {
  const ta = tokenize(a)
  const tb = tokenize(b)
  const n = ta.length
  const m = tb.length
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = ta[i] === tb[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }
  const del: Range[] = []
  const add: Range[] = []
  let i = 0
  let j = 0
  let aPos = 0
  let bPos = 0
  const push = (arr: Range[], start: number, end: number): void => {
    const last = arr[arr.length - 1]
    if (last && last[1] === start) last[1] = end
    else arr.push([start, end])
  }
  while (i < n && j < m) {
    if (ta[i] === tb[j]) {
      aPos += ta[i].length
      bPos += tb[j].length
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      push(del, aPos, aPos + ta[i].length)
      aPos += ta[i].length
      i++
    } else {
      push(add, bPos, bPos + tb[j].length)
      bPos += tb[j].length
      j++
    }
  }
  while (i < n) {
    push(del, aPos, aPos + ta[i].length)
    aPos += ta[i].length
    i++
  }
  while (j < m) {
    push(add, bPos, bPos + tb[j].length)
    bPos += tb[j].length
    j++
  }
  return { del, add }
}

/**
 * Git's extended header lines, which sit between `diff --git` and the first
 * `@@` of a file. Content lines always carry a ' ', '+' or '-' prefix, so an
 * unprefixed line matching one of these can only be a header.
 */
const EXT_HEADER =
  /^(new file mode |deleted file mode |old mode |new mode |similarity index |dissimilarity index |rename (from|to) |copy (from|to) |Binary files |GIT binary patch)/

/** Parse a unified diff into typed lines with old/new line numbers + hunk index. */
export function parseDiff(diff: string): DiffLine[] {
  const out: DiffLine[] = []
  let oldNo = 0
  let newNo = 0
  let hunkIdx = -1
  // Git ends its output with a newline. Split as-is, that leaves one empty
  // string behind, which would parse as a phantom context line past the end.
  const body = diff.endsWith('\n') ? diff.slice(0, -1) : diff
  for (const line of body.split('\n')) {
    if (line.startsWith('@@')) {
      const m = /@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line)
      if (m) {
        oldNo = +m[1]
        newNo = +m[2]
      }
      hunkIdx++
      out.push({ kind: 'hunk', text: line, oldNo: null, newNo: null, hunkIdx })
    } else if (
      line.startsWith('+++') ||
      line.startsWith('---') ||
      line.startsWith('diff ') ||
      line.startsWith('index ') ||
      EXT_HEADER.test(line)
    ) {
      out.push({ kind: 'meta', text: line, oldNo: null, newNo: null, hunkIdx })
    } else if (line.startsWith('+')) {
      out.push({ kind: 'add', text: line.slice(1), oldNo: null, newNo: newNo++, hunkIdx })
    } else if (line.startsWith('-')) {
      out.push({ kind: 'del', text: line.slice(1), oldNo: oldNo++, newNo: null, hunkIdx })
    } else if (line.startsWith('\\')) {
      // "\ No newline at end of file" annotates the line before it; it is not a
      // line of either file, so it must not advance the numbering — mid-hunk it
      // used to shift every number after it by one.
      out.push({ kind: 'ctx', text: line, oldNo: null, newNo: null, hunkIdx })
    } else {
      out.push({ kind: 'ctx', text: line.startsWith(' ') ? line.slice(1) : line, oldNo: oldNo++, newNo: newNo++, hunkIdx })
    }
  }
  return out
}

/**
 * Per-line changed-character ranges, pairing each run of consecutive deletions
 * with the additions that immediately follow it (zipped line-by-line). Keyed by
 * index into `lines`.
 */
export function wordRangesByLine(lines: DiffLine[]): Map<number, Range[]> {
  const map = new Map<number, Range[]>()
  let i = 0
  while (i < lines.length) {
    if (lines[i].kind !== 'del') {
      i++
      continue
    }
    const dels: number[] = []
    while (i < lines.length && lines[i].kind === 'del') dels.push(i++)
    const adds: number[] = []
    while (i < lines.length && lines[i].kind === 'add') adds.push(i++)
    const pairs = Math.min(dels.length, adds.length)
    for (let k = 0; k < pairs; k++) {
      const { del, add } = wordDiff(lines[dels[k]].text, lines[adds[k]].text)
      if (del.length) map.set(dels[k], del)
      if (add.length) map.set(adds[k], add)
    }
  }
  return map
}

export interface SplitCell {
  idx: number
  no: number | null
  text: string
  kind: 'del' | 'add' | 'ctx'
}
export interface SplitRow {
  hunk?: string
  hunkIdx?: number
  /** Full-file rows only: this row is the first line of hunk N, which has no
   *  @@ bar of its own to carry the stage button. */
  startsHunk?: number
  left?: SplitCell
  right?: SplitCell
}

/**
 * Build side-by-side rows: context lines mirror both sides; each deletion run is
 * zipped with the following addition run (leftovers become one-sided rows).
 * With `fullFile`, @@ lines become no row at all — a bar every few lines would
 * break the file up — and the row after one is tagged with `startsHunk`.
 */
export function buildSplitRows(lines: DiffLine[], fullFile = false): SplitRow[] {
  const rows: SplitRow[] = []
  let pendingHunk: number | undefined
  const push = (row: SplitRow): void => {
    if (pendingHunk !== undefined) row.startsHunk = pendingHunk
    pendingHunk = undefined
    rows.push(row)
  }
  let i = 0
  while (i < lines.length) {
    const l = lines[i]
    if (l.kind === 'meta') {
      i++
      continue
    }
    if (l.kind === 'hunk') {
      if (fullFile) pendingHunk = l.hunkIdx
      else rows.push({ hunk: l.text, hunkIdx: l.hunkIdx })
      i++
      continue
    }
    if (l.kind === 'ctx') {
      push({
        left: { idx: i, no: l.oldNo, text: l.text, kind: 'ctx' },
        right: { idx: i, no: l.newNo, text: l.text, kind: 'ctx' }
      })
      i++
      continue
    }
    const dels: number[] = []
    while (i < lines.length && lines[i].kind === 'del') dels.push(i++)
    const adds: number[] = []
    while (i < lines.length && lines[i].kind === 'add') adds.push(i++)
    const n = Math.max(dels.length, adds.length)
    for (let k = 0; k < n; k++) {
      const li = dels[k]
      const ri = adds[k]
      push({
        left: li != null ? { idx: li, no: lines[li].oldNo, text: lines[li].text, kind: 'del' } : undefined,
        right: ri != null ? { idx: ri, no: lines[ri].newNo, text: lines[ri].text, kind: 'add' } : undefined
      })
    }
  }
  return rows
}

const HUNK_RANGE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

/** Whitespace-blind line comparison: an `-w` diff prints context lines that
 *  may differ from the file in indentation only, and CRLF files keep a `\r`. */
const sameLine = (a: string, b: string): boolean => a.replace(/\s+/g, '') === b.replace(/\s+/g, '')

/**
 * Expand a hunks-only diff to the whole file. Everything outside the hunks is
 * unchanged by definition, so it is read from the new side's full text and
 * spliced in as context, numbered on both sides. Hunk lines stay in place so
 * the caller still knows where each one starts, and every original line keeps
 * its `hunkIdx`, so staging a hunk works exactly as before.
 *
 * Returns null when there is nothing to expand, or when the text disagrees
 * with the diff's own context lines (a stale read, or a fallback version of a
 * file the ref no longer has). Showing hunks is better than showing the wrong
 * file.
 */
export function fillContext(lines: DiffLine[], newText: string): DiffLine[] | null {
  if (!lines.some((l) => l.kind === 'hunk')) return null
  // A deleted file has no new side; whatever the read fell back to is not it.
  const deleted = lines.some((l) => l.kind === 'meta' && (l.text === '+++ /dev/null' || l.text.startsWith('deleted file mode')))
  const file = deleted || newText === '' ? [] : newText.replace(/\r?\n$/, '').split('\n')

  const out: DiffLine[] = []
  let nextOld = 1
  let nextNew = 1
  const gap = (upTo: number): boolean => {
    for (let n = nextNew; n <= upTo; n++) {
      const text = file[n - 1]
      if (text === undefined) return false
      out.push({ kind: 'ctx', text: text.replace(/\r$/, ''), oldNo: n + nextOld - nextNew, newNo: n, hunkIdx: -1 })
    }
    return true
  }

  for (const l of lines) {
    if (l.kind === 'hunk') {
      const m = HUNK_RANGE.exec(l.text)
      if (!m) return null
      const [oldStart, oldCount, newStart, newCount] = [+m[1], m[2] === undefined ? 1 : +m[2], +m[3], m[4] === undefined ? 1 : +m[4]]
      // An empty side's start is the line the change sits after, not on.
      const firstOld = oldCount === 0 ? oldStart + 1 : oldStart
      const firstNew = newCount === 0 ? newStart + 1 : newStart
      if (firstOld - nextOld !== firstNew - nextNew || !gap(firstNew - 1)) return null
      nextOld = firstOld + oldCount
      nextNew = firstNew + newCount
    } else if (l.kind === 'ctx' && l.newNo !== null) {
      const text = file[l.newNo - 1]
      if (text === undefined || !sameLine(text, l.text)) return null
    }
    out.push(l)
  }
  return gap(file.length) ? out : null
}

/** A run of consecutive changed rows — what ↑/↓ steps through and what the
 *  overview ruler marks. `end` is exclusive. */
export interface ChangeBlock {
  start: number
  end: number
  /** Whether the run removes lines (a mark on the left) or adds them (right). */
  del: boolean
  add: boolean
}

export function changeBlocks(rows: SplitRow[]): ChangeBlock[] {
  const blocks: ChangeBlock[] = []
  let cur: ChangeBlock | null = null
  rows.forEach((r, i) => {
    const del = r.left?.kind === 'del'
    const add = r.right?.kind === 'add'
    if (!del && !add) {
      cur = null
      return
    }
    if (!cur) {
      cur = { start: i, end: i + 1, del, add }
      blocks.push(cur)
    } else {
      cur.end = i + 1
      cur.del ||= del
      cur.add ||= add
    }
  })
  return blocks
}

/** Leading whitespace of one line, in columns. */
function leadingColumns(text: string, tabSize: number): number {
  let col = 0
  for (const ch of text) {
    if (ch === ' ') col++
    else if (ch === '\t') col += tabSize - (col % tabSize)
    else break
  }
  return col
}

/**
 * The indent step a file is written in, guessed from how much the indentation
 * changes between neighbouring lines — the way an editor detects it. A step of
 * one is ignored: that is the ` * ` of a block comment, not a level.
 */
export function detectIndentUnit(texts: string[], tabSize: number): number {
  const counts = new Map<number, number>()
  let prev: number | null = null
  for (const t of texts) {
    if (!t.trim()) continue
    if (t.startsWith('\t')) return tabSize
    const col = leadingColumns(t, tabSize)
    if (prev !== null) {
      const d = Math.abs(col - prev)
      if (d > 1 && d <= 8) counts.set(d, (counts.get(d) ?? 0) + 1)
    }
    prev = col
  }
  let best = tabSize
  let bestN = 0
  for (const [d, n] of counts) if (n > bestN || (n === bestN && d < best)) [best, bestN] = [d, n]
  return best
}

/**
 * Indent of each row in columns, for indent guides. `null` is a row with no
 * line on this side (a hatched filler) and gets none. A blank line borrows the
 * smaller indent of its nearest non-blank neighbours, so a guide runs through
 * an empty line inside a block instead of breaking at it.
 */
export function indentColumns(texts: (string | null)[], tabSize: number): number[] {
  const raw = texts.map((t) => (t === null ? 0 : t.trim() ? leadingColumns(t, tabSize) : -1))
  // Two sweeps rather than a search per blank line: a run of thousands of
  // blank lines would otherwise go quadratic.
  const before: number[] = []
  let last = 0
  raw.forEach((c, i) => {
    if (c === -1) before[i] = last
    else if (texts[i] !== null) last = c
  })
  const out = raw.slice()
  last = 0
  for (let i = raw.length - 1; i >= 0; i--) {
    if (raw[i] === -1) out[i] = Math.min(before[i], last)
    else if (texts[i] !== null) last = raw[i]
  }
  return out
}

/** One changed region against the new (current) file, for the File view's
 *  change gutter. `lineStart`..`lineEnd` are new-file line numbers the bar
 *  spans; for a pure deletion (nothing added) they are equal and `edge` says
 *  which border of that line the marker sits on — the line the deletion
 *  happened before, or (deletion at EOF) after the last line. */
export interface GutterChange {
  index: number
  type: 'add' | 'mod' | 'del'
  lineStart: number
  lineEnd: number
  edge: 'before' | 'after'
  removed: string[]
  added: string[]
}

/** Parse a unified diff (current file vs. its last committed/staged version)
 *  into the regions a change gutter decorates. Consecutive del/add runs are
 *  zipped the same way {@link buildSplitRows} pairs them: a run with both is
 *  a modification, add-only is an insertion, del-only is a pure deletion
 *  anchored to the line it now sits next to. */
export function computeGutterChanges(diff: string): GutterChange[] {
  const lines = parseDiff(diff)
  const changes: GutterChange[] = []
  let lastNewNo = 0
  let i = 0
  while (i < lines.length) {
    const l = lines[i]
    if (l.kind === 'ctx' && l.newNo != null) lastNewNo = l.newNo
    if (l.kind !== 'del' && l.kind !== 'add') {
      i++
      continue
    }
    const dels: typeof lines = []
    while (i < lines.length && lines[i].kind === 'del') dels.push(lines[i++])
    const adds: typeof lines = []
    while (i < lines.length && lines[i].kind === 'add') {
      lastNewNo = lines[i].newNo ?? lastNewNo
      adds.push(lines[i++])
    }
    if (adds.length > 0) {
      changes.push({
        index: changes.length,
        type: dels.length > 0 ? 'mod' : 'add',
        lineStart: adds[0].newNo!,
        lineEnd: adds[adds.length - 1].newNo!,
        edge: 'before',
        removed: dels.map((d) => d.text),
        added: adds.map((a) => a.text)
      })
    } else if (dels.length > 0) {
      const next = lines[i]
      const atEof = !next || next.newNo == null
      changes.push({
        index: changes.length,
        type: 'del',
        lineStart: atEof ? Math.max(1, lastNewNo) : next.newNo!,
        lineEnd: atEof ? Math.max(1, lastNewNo) : next.newNo!,
        edge: atEof ? 'after' : 'before',
        removed: dels.map((d) => d.text),
        added: []
      })
    }
  }
  return changes
}

/** Every new-file line number a change touches, mapped back to that change —
 *  a multi-line insertion/modification marks each of its lines. */
export function gutterMarksByLine(changes: GutterChange[]): Map<number, GutterChange> {
  const map = new Map<number, GutterChange>()
  for (const c of changes) {
    for (let ln = c.lineStart; ln <= c.lineEnd; ln++) map.set(ln, c)
  }
  return map
}

/**
 * Which rows a windowed column renders: the ones in view plus `overscan` either
 * side, snapped outward to multiples of `chunk` so the window moves in steps —
 * a re-render every `chunk` rows scrolled, not every row. `end` is exclusive.
 */
export function visibleRange(
  scrollTop: number,
  viewHeight: number,
  rowH: number,
  total: number,
  overscan = 40,
  chunk = 20
): [number, number] {
  if (rowH <= 0 || total <= 0) return [0, 0]
  const first = Math.floor(Math.max(0, scrollTop) / rowH)
  const last = Math.ceil((Math.max(0, scrollTop) + viewHeight) / rowH)
  const start = Math.min(total, Math.max(0, Math.floor((first - overscan) / chunk) * chunk))
  const end = Math.min(total, Math.ceil((last + overscan) / chunk) * chunk)
  return [start, Math.max(start, end)]
}
