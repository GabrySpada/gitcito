// Partial patches for staging and unstaging single lines or hunks — pure, so
// the exact patch text can be applied with real git in the tests.

import type { DiffLine } from './diff'

/** `stage`: the diff is working tree vs index, applied forwards to the index.
 *  `unstage`: the diff is index vs HEAD, applied in reverse to the index. */
export type PatchDirection = 'stage' | 'unstage'

const HUNK_RANGE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

/** The file header: every line above the first `@@`. */
export function diffHeader(diff: string): string {
  const out: string[] = []
  for (const line of diff.split('\n')) {
    if (line.startsWith('@@')) break
    out.push(line)
  }
  return out.join('\n')
}

/** The `\ No newline at end of file` marker, which parseDiff keeps as an
 *  unnumbered context line. A real context line can start with `\` too. */
const isEofMarker = (l: DiffLine): boolean => l.kind === 'ctx' && l.oldNo === null && l.newNo === null

/** Whether a line can be picked for staging on its own. */
export const isChangeLine = (l: DiffLine): boolean => l.kind === 'add' || l.kind === 'del'

/** Every change line of one hunk — what the hunk button stages. */
export function hunkLines(lines: DiffLine[], hunkIdx: number): Set<number> {
  const out = new Set<number>()
  lines.forEach((l, i) => {
    if (l.hunkIdx === hunkIdx && isChangeLine(l)) out.add(i)
  })
  return out
}

/**
 * A new or deleted file's header claims the whole file. Part of it staged is
 * an ordinary modification of a file that exists on both sides, so the
 * /dev/null side becomes the real path and the mode line goes.
 */
function partialHeader(header: string): string {
  const paths = /^diff --git a\/(.+) b\/(.+)$/m.exec(header)
  if (!paths) return header
  return header
    .split('\n')
    .filter((l) => !l.startsWith('new file mode ') && !l.startsWith('deleted file mode '))
    .map((l) => (l === '--- /dev/null' ? `--- a/${paths[1]}` : l === '+++ /dev/null' ? `+++ b/${paths[2]}` : l))
    .join('\n')
}

/**
 * Build a patch holding only the selected +/- lines (indices into `lines`),
 * the same shape `git add -p` produces when you edit a hunk by hand.
 *
 * Staging applies forwards to the index, which holds the old side: an
 * unselected deletion stays as context, an unselected addition is dropped.
 * Unstaging applies in reverse to the index, which holds the new side, so it
 * is the mirror image — an unselected addition stays, an unselected deletion
 * is dropped. Either way the side git anchors on is untouched, and the other
 * side's start lines are shifted by what earlier hunks no longer change.
 *
 * Returns null when nothing selected is a change line.
 */
export function buildLinePatch(
  lines: DiffLine[],
  header: string,
  selected: Set<number>,
  direction: PatchDirection = 'stage'
): string | null {
  const keepAsContext = direction === 'stage' ? 'del' : 'add'
  const heads = new Map<number, { o: number; oc: number; n: number; nc: number }>()
  const byHunk = new Map<number, number[]>()
  lines.forEach((l, i) => {
    if (l.kind === 'hunk') {
      const m = HUNK_RANGE.exec(l.text)
      if (m) heads.set(l.hunkIdx, { o: +m[1], oc: m[2] === undefined ? 1 : +m[2], n: +m[3], nc: m[4] === undefined ? 1 : +m[4] })
    } else if (l.kind !== 'meta' && l.hunkIdx >= 0) {
      if (!byHunk.has(l.hunkIdx)) byHunk.set(l.hunkIdx, [])
      byHunk.get(l.hunkIdx)!.push(i)
    }
  })

  const parts: string[] = []
  let everything = true
  // How far the unanchored side has drifted: the original hunks' net line
  // change minus the patch's, summed over every hunk so far.
  let drift = 0
  for (const [hunkIdx, idxs] of byHunk) {
    const head = heads.get(hunkIdx)
    if (!head) continue
    const origDelta = head.nc - head.oc
    if (!idxs.some((i) => selected.has(i) && isChangeLine(lines[i]))) {
      if (idxs.some((i) => isChangeLine(lines[i]))) everything = false
      drift += origDelta
      continue
    }
    const body: string[] = []
    let oldC = 0
    let newC = 0
    // A marker belongs to the line above it, and goes wherever that line goes.
    let lastKept = false
    for (const i of idxs) {
      const l = lines[i]
      if (isEofMarker(l)) {
        if (lastKept) body.push(l.text)
        continue
      }
      if (l.kind === 'ctx') {
        body.push(` ${l.text}`)
        oldC++
        newC++
        lastKept = true
      } else if (selected.has(i)) {
        body.push(`${l.kind === 'add' ? '+' : '-'}${l.text}`)
        if (l.kind === 'add') newC++
        else oldC++
        lastKept = true
      } else if (l.kind === keepAsContext) {
        everything = false
        body.push(` ${l.text}`)
        oldC++
        newC++
        lastKept = true
      } else {
        everything = false
        lastKept = false
      }
    }
    const [o, n] = direction === 'stage' ? [head.o, head.n - drift] : [head.o + drift, head.n]
    parts.push(`@@ -${o},${oldC} +${n},${newC} @@`)
    parts.push(...body)
    drift += origDelta - (newC - oldC)
  }
  if (parts.length === 0) return null
  return `${everything ? header : partialHeader(header)}\n${parts.join('\n')}\n`
}

/** How many lines a patch adds or removes — the count its toast reports.
 *  Only below the first `@@`: a removed `-- comment` reads `--- comment`. */
export function countPatchLines(patch: string): number {
  let n = 0
  let inHunk = false
  for (const line of patch.split('\n')) {
    if (line.startsWith('@@')) inHunk = true
    else if (inHunk && (line.startsWith('+') || line.startsWith('-'))) n++
  }
  return n
}
