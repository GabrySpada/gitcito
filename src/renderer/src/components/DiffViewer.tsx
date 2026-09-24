import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import {
  SplitSquareHorizontal,
  Columns2,
  Rows2,
  UnfoldVertical,
  Pilcrow,
  Search,
  ArrowUp,
  ArrowDown,
  ChevronUp,
  ChevronDown,
  WrapText,
  Link2,
  Unlink2,
  X
} from 'lucide-react'
import { highlightHtml, buildQueryRegExp, type HighlightLayer } from './FileSearchBar'
import { highlightLine } from '../lib/highlight'
import { maskSecretLine } from '../lib/secrets'
import { registerRowLookup } from '../lib/reveal'
import { useT, interp } from '../i18n'
import { useSettingsStore } from '../stores/settings'
import { useHoverExplain } from './HoverExplain'
import type { NumberedLine } from '../../../shared/types'
import {
  parseDiff,
  wordRangesByLine,
  buildSplitRows,
  fillContext,
  changeBlocks,
  detectIndentUnit,
  indentColumns,
  visibleRange,
  type ChangeBlock,
  type DiffLine,
  type Range,
  type SplitCell,
  type SplitRow
} from '../lib/diff'

/**
 * Wrap the given decoded-character ranges in <mark class> within an HTML string
 * (post-hljs/search-highlight), tracking a running decoded offset across all
 * tag-free segments so marks never break tags or entities.
 */
function markRanges(html: string, ranges: Range[], cls: string): string {
  if (ranges.length === 0) return html
  const inRange = (idx: number): boolean => ranges.some(([s, e]) => idx >= s && idx < e)
  let pos = 0
  return html.replace(/(<[^>]+>)|([^<]+)/g, (_m, tag, text) => {
    if (tag) return tag
    const tokens = (text as string).match(/&[a-zA-Z][a-zA-Z0-9]*;|&#\d+;|&#x[0-9a-fA-F]+;|[\s\S]/g) ?? []
    let out = ''
    let open = false
    for (const tok of tokens) {
      const hit = inRange(pos)
      if (hit && !open) {
        out += `<mark class="${cls}">`
        open = true
      } else if (!hit && open) {
        out += '</mark>'
        open = false
      }
      out += tok
      pos++
    }
    if (open) out += '</mark>'
    return out
  })
}

function extractHunks(diff: string): { header: string; hunks: string[] } {
  const rawLines = diff.split('\n')
  const headerLines: string[] = []
  const hunks: string[] = []
  let currentHunk: string[] | null = null

  for (const line of rawLines) {
    if (line.startsWith('@@')) {
      if (currentHunk) hunks.push(currentHunk.join('\n'))
      currentHunk = [line]
    } else if (currentHunk !== null) {
      currentHunk.push(line)
    } else {
      headerLines.push(line)
    }
  }
  if (currentHunk) hunks.push(currentHunk.join('\n'))

  return { header: headerLines.join('\n'), hunks }
}

/**
 * Build a partial patch containing only the selected +/- lines (by index into
 * `lines`). Unselected deletions become context (kept), unselected additions are
 * dropped, and each affected hunk's @@ counts are recomputed — the same shape
 * `git add -p` produces, applied to the index via `git apply --cached`.
 */
function buildLinePatch(lines: DiffLine[], header: string, selected: Set<number>): string {
  const starts = new Map<number, { o: number; n: number }>()
  for (const l of lines) {
    if (l.kind === 'hunk') {
      const m = /@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(l.text)
      if (m) starts.set(l.hunkIdx, { o: +m[1], n: +m[2] })
    }
  }
  const hunks = new Map<number, number[]>()
  lines.forEach((l, i) => {
    if (l.kind === 'add' || l.kind === 'del' || l.kind === 'ctx') {
      if (!hunks.has(l.hunkIdx)) hunks.set(l.hunkIdx, [])
      hunks.get(l.hunkIdx)!.push(i)
    }
  })

  const parts: string[] = []
  for (const [hunkIdx, idxs] of hunks) {
    if (!idxs.some((i) => selected.has(i))) continue
    const start = starts.get(hunkIdx) ?? { o: 1, n: 1 }
    const body: string[] = []
    let oldC = 0
    let newC = 0
    for (const i of idxs) {
      const l = lines[i]
      if (l.text.startsWith('\\')) {
        body.push(l.text) // "\ No newline at end of file" — keep verbatim, don't count
        continue
      }
      if (l.kind === 'ctx') {
        body.push(` ${l.text}`)
        oldC++
        newC++
      } else if (l.kind === 'del') {
        if (selected.has(i)) {
          body.push(`-${l.text}`)
          oldC++
        } else {
          body.push(` ${l.text}`) // keep this deletion out of the stage
          oldC++
          newC++
        }
      } else if (selected.has(i)) {
        body.push(`+${l.text}`)
        newC++
      }
      // unselected additions are omitted
    }
    parts.push(`@@ -${start.o},${oldC} +${start.n},${newC} @@`)
    parts.push(...body)
  }
  return `${header}\n${parts.join('\n')}\n`
}

/** Above this many parsed lines the viewer gets `is-huge` — the browser skips
 *  layout/paint for offscreen rows (`content-visibility: auto`) while the DOM
 *  stays complete, so find, staging clicks and selection behave as before. */
const HUGE_LINES = 5000

/** Split view renders tabs this wide, and indent guides are measured in it. */
const TAB_SIZE = 4

/** The split view's line height, as a multiple of the font size. The CSS uses
 *  the same number; here it turns the font size into a whole-pixel row. */
const DV_LINE_HEIGHT = 1.4

/** Rows a windowed column renders beyond the visible ones, each way — enough
 *  that a fast wheel flick does not outrun the next render. */
const OVERSCAN = 60

/**
 * The overview ruler, GitKraken's: a strip beside the right column showing the
 * whole file — a lane for removals (red), one for additions (green) — and a box
 * for the part on screen. Press or drag anywhere on it to scroll there. Rows
 * are one line tall in this mode, so a row's share of the list is its share of
 * the strip. It follows the right column; linked, the left one follows too.
 */
function DiffOverview({
  blocks,
  total,
  scrollerRef,
  measured = false
}: {
  blocks: ChangeBlock[]
  total: number
  scrollerRef: React.RefObject<HTMLDivElement>
  /** Wrap on: rows differ in height, so a change's place on the strip is read
   *  off its rendered row (wrap mode renders them all) rather than its index. */
  measured?: boolean
}): React.JSX.Element {
  const stripRef = useRef<HTMLDivElement>(null)
  // Fractions of the whole scroll height, per block.
  const [spans, setSpans] = useState<{ top: number; height: number }[] | null>(null)
  useLayoutEffect(() => {
    const el = scrollerRef.current
    if (!measured || !el) {
      setSpans(null)
      return
    }
    const measure = (): void => {
      const base = el.getBoundingClientRect().top - el.scrollTop
      const h = el.scrollHeight || 1
      const rowAt = (i: number): Element | null =>
        el.querySelector(`[data-row="${i}"]`)?.closest('.diff-split-row, .diff-split-hunk') ?? null
      setSpans(
        blocks.map((b) => {
          const first = rowAt(b.start)?.getBoundingClientRect()
          const last = rowAt(b.end - 1)?.getBoundingClientRect()
          if (!first || !last) return { top: b.start / total, height: (b.end - b.start) / total }
          return { top: (first.top - base) / h, height: (last.bottom - first.top) / h }
        })
      )
    }
    measure()
    // Re-wrapping at a new width moves every row.
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    if (el.firstElementChild) ro.observe(el.firstElementChild)
    return () => ro.disconnect()
  }, [measured, blocks, total, scrollerRef])
  // Its own state, so scrolling re-renders the strip and not the whole diff.
  const [view, setView] = useState({ top: 0, height: 1 })
  useEffect(() => {
    const el = scrollerRef.current
    if (!el) return
    const update = (): void => {
      const h = el.scrollHeight || 1
      setView({ top: el.scrollTop / h, height: Math.min(1, el.clientHeight / h) })
    }
    update()
    el.addEventListener('scroll', update, { passive: true })
    const ro = new ResizeObserver(update)
    ro.observe(el)
    return () => {
      el.removeEventListener('scroll', update)
      ro.disconnect()
    }
  }, [scrollerRef, total])

  const onPointerDown = (e: React.PointerEvent): void => {
    const strip = stripRef.current
    const el = scrollerRef.current
    if (!strip || !el || e.button !== 0) return
    e.preventDefault()
    const frac = (y: number): number => {
      const r = strip.getBoundingClientRect()
      return (y - r.top) / r.height
    }
    // On the box, drag it from where it was grabbed; elsewhere, centre it on
    // the pointer first — a click on a mark lands on that change.
    const at = frac(e.clientY)
    const grab = at >= view.top && at <= view.top + view.height ? at - view.top : view.height / 2
    const scrollTo = (y: number): void => {
      el.scrollTop = (frac(y) - grab) * el.scrollHeight
    }
    scrollTo(e.clientY)
    const move = (ev: PointerEvent): void => scrollTo(ev.clientY)
    const up = (): void => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  const pct = (n: number): string => `${(n / total) * 100}%`
  return (
    <div className="diff-overview" ref={stripRef} aria-hidden="true" onPointerDown={onPointerDown}>
      {blocks.map((b, i) => {
        const span = spans?.[i]
        const pos = span
          ? { top: `${span.top * 100}%`, height: `max(2px, ${span.height * 100}%)` }
          : { top: pct(b.start), height: `max(2px, ${pct(b.end - b.start)})` }
        return (
          <Fragment key={i}>
            {b.del && <span className="diff-overview-mark del" style={pos} />}
            {b.add && <span className="diff-overview-mark add" style={pos} />}
          </Fragment>
        )
      })}
      <span className="diff-overview-view" style={{ top: `${view.top * 100}%`, height: `${view.height * 100}%` }} />
    </div>
  )
}

export function DiffViewer({
  diff,
  file = '',
  lang = '',
  highlightLayers = [],
  maskValues = false,
  ignoreWs = false,
  onToggleIgnoreWs,
  onStageHunk,
  loadNewText,
  sourceKey,
  toolbarStart,
  findSignal = 0
}: {
  diff: string
  /** Path of the file being diffed — labels the hover-explain card. */
  file?: string
  lang?: string
  highlightLayers?: HighlightLayer[]
  /** Mask secret values (KEY=••••) in displayed lines — secret files only. */
  maskValues?: boolean
  /** Whether the diff was fetched ignoring whitespace (drives the toggle state). */
  ignoreWs?: boolean
  /** Re-fetch the diff with/without `-w`. When absent, the toggle is hidden. */
  onToggleIgnoreWs?: () => void
  onStageHunk?: (patch: string) => void
  /** Reads the new side's whole text, for the split view's full-file mode.
   *  Absent (a multi-file diff, a snapshot), split view shows hunks only. */
  loadNewText?: () => Promise<string>
  /** What is being diffed, beyond the path: the same file at another commit is
   *  another view. Decides when full-file mode re-lands on the first change and
   *  when it may keep showing the previous text meanwhile. Defaults to `file`. */
  sourceKey?: string
  /** The host's own controls, first in the toolbar strip — the semantic
   *  summary's button, for one. Rendering nothing leaves no gap. */
  toolbarStart?: React.ReactNode
  /** Bumped by the host to open this viewer's find bar — its own ⌘F, which
   *  knows about rows a windowed split view has not rendered. */
  findSignal?: number
}): React.JSX.Element {
  const t = useT()
  const lines = useMemo(() => parseDiff(diff), [diff])
  const viewKey = sourceKey ?? file

  const [splitView, setSplitView] = useState(() => localStorage.getItem('gitcito-split-diff') === 'on')
  useEffect(() => localStorage.setItem('gitcito-split-diff', splitView ? 'on' : 'off'), [splitView])

  // Full file in split view, the way GitKraken shows it: the change in the
  // place it sits, rather than three lines either side of it. On by default.
  const [fullFile, setFullFile] = useState(() => localStorage.getItem('gitcito-diff-full-file') !== 'off')
  useEffect(() => localStorage.setItem('gitcito-diff-full-file', fullFile ? 'on' : 'off'), [fullFile])

  // The loader closes over the viewer's current source, so it is new on every
  // render; the diff and file it belongs to are what decide a re-read.
  const loadRef = useRef(loadNewText)
  loadRef.current = loadNewText
  const wantFull = splitView && fullFile && !!loadNewText
  const [newText, setNewText] = useState<{ diff: string; text: string | null } | null>(null)
  useEffect(() => {
    const load = loadRef.current
    if (!wantFull || !load) return
    let cancelled = false
    load().then(
      (text) => !cancelled && setNewText({ diff, text }),
      // Too large, unreadable: the hunks are still a correct diff.
      () => !cancelled && setNewText({ diff, text: null })
    )
    return () => {
      cancelled = true
    }
  }, [wantFull, diff, viewKey])
  // Null until the text for *this* diff is in, and whenever it does not agree
  // with the diff — split view shows hunks meanwhile.
  const fullNow = useMemo(
    () => (wantFull && newText?.diff === diff && newText.text !== null ? fillContext(lines, newText.text) : null),
    [wantFull, newText, diff, lines]
  )
  // The same file re-diffed (an edit on disk, a refresh) keeps showing its
  // previous full view until the new text is in — blanking it would flicker,
  // and remounting the columns would lose the reader's place.
  const lastFull = useRef<{ key: string; lines: DiffLine[] } | null>(null)
  if (fullNow) lastFull.current = { key: viewKey, lines: fullNow }
  const fullLines =
    fullNow ?? (wantFull && newText?.diff !== diff && lastFull.current?.key === viewKey ? lastFull.current.lines : null)
  // What the split view renders; the unified view and staging stay on `lines`.
  const viewLines = splitView && fullLines ? fullLines : lines
  // Waiting on the new side's text. The split body stays empty meanwhile:
  // drawing the hunks, then the file from line 1, then the landing, is three
  // frames of flicker where GitKraken shows one. A failed read ends the wait
  // too (text: null), and the hunks show.
  const fullPending = wantFull && newText?.diff !== diff && !fullLines

  // ─── Hover-to-explain over the diff's own lines ───
  const aiEnabled = useSettingsStore((s) => s.activeProfile().ai.enabled !== false)
  const hoverOn = useSettingsStore((s) => s.activeProfile().ai.hoverExplain !== false)
  const hoverKey = useSettingsStore((s) => s.activeProfile().ai.hoverExplainKey ?? 'shift')
  // Hunks only (unless split view has the whole file), so the window can have
  // gaps. Numbers come from the new side where there is one, and from the old
  // side for removed lines.
  const hoverLines = useMemo<NumberedLine[]>(
    () =>
      viewLines
        .filter((l) => l.kind === 'add' || l.kind === 'del' || l.kind === 'ctx')
        .map((l) => ({ no: l.newNo ?? l.oldNo ?? 0, text: l.text }))
        .filter((l) => l.no > 0),
    [viewLines]
  )
  const { hoverProps, armed: hoverArmed, card: hoverCard } = useHoverExplain({
    enabled: aiEnabled && hoverOn && !maskValues,
    file,
    lang,
    modifier: hoverKey,
    getLines: () => hoverLines,
    lineOf: (el) => {
      const row = el.closest('.diff-line, .diff-split-cell')
      if (!row) return null
      const gutters = [...row.querySelectorAll('.diff-gutter')].map((g) => Number(g.textContent?.trim()))
      // Unified rows carry old and new; the new number is the one to cite.
      const no = gutters.reverse().find((n) => Number.isInteger(n) && n > 0)
      return no ?? null
    }
  })
  const hunkData = useMemo(() => (onStageHunk ? extractHunks(diff) : null), [diff, onStageHunk])

  const [wordDiffOn, setWordDiffOn] = useState(() => localStorage.getItem('gitcito-word-diff') !== 'off')
  useEffect(() => localStorage.setItem('gitcito-word-diff', wordDiffOn ? 'on' : 'off'), [wordDiffOn])

  // Split-view line wrapping. Off by default: one line per row keeps the two
  // sides row-for-row comparable, which is the point of split view. On, long
  // lines fold inside their column instead of scrolling.
  const [wrapOn, setWrapOn] = useState(() => localStorage.getItem('gitcito-diff-wrap') === 'on')
  useEffect(() => localStorage.setItem('gitcito-diff-wrap', wrapOn ? 'on' : 'off'), [wrapOn])

  // Scrolling with wrap off: each column fully on its own, or the two locked
  // together both ways — vertically and sideways. Locked is the default: the
  // sides are worth comparing at the same row and column, and one that drifts
  // is the reason to look away from a split view.
  const [linkScroll, setLinkScroll] = useState(() => localStorage.getItem('gitcito-diff-link-scroll') !== 'off')
  useEffect(() => localStorage.setItem('gitcito-diff-link-scroll', linkScroll ? 'on' : 'off'), [linkScroll])

  // Wrap off in split view: one scroller per side, every row one line tall.
  const isCols = splitView && !wrapOn

  // Per-line changed-character ranges (for word-level highlighting), keyed by
  // index into whichever line list is on screen.
  const wordRanges = useMemo(() => wordRangesByLine(viewLines), [viewLines])

  // Side-by-side rows (ctx mirrored, del-runs zipped with following add-runs).
  const splitRows = useMemo(() => buildSplitRows(viewLines, !!fullLines), [viewLines, fullLines])

  // Runs of changed rows: what ↑/↓ steps through and the ruler marks. The
  // unified view has its own rows, so its runs are counted over `lines`.
  const blocks = useMemo<ChangeBlock[]>(() => {
    if (splitView) return changeBlocks(splitRows)
    const out: ChangeBlock[] = []
    lines.forEach((l, i) => {
      const changed = l.kind === 'add' || l.kind === 'del'
      const prev = lines[i - 1]
      if (changed && !(prev && (prev.kind === 'add' || prev.kind === 'del'))) {
        out.push({ start: i, end: i + 1, del: false, add: false })
      }
      if (changed) {
        const b = out[out.length - 1]
        b.end = i + 1
        b.del ||= l.kind === 'del'
        b.add ||= l.kind === 'add'
      }
    })
    return out
  }, [splitView, splitRows, lines])
  const blockAt = useMemo(() => new Map(blocks.map((b, i) => [b.start, i])), [blocks])

  // Indent guides, per side: the step the file is written in, and each row's
  // indent. Only the split view draws them.
  const guides = useMemo(() => {
    if (!splitView) return null
    const leftTexts = splitRows.map((r) => (r.left ? r.left.text : null))
    const rightTexts = splitRows.map((r) => (r.right ? r.right.text : null))
    const unit = detectIndentUnit(
      rightTexts.filter((x): x is string => x !== null),
      TAB_SIZE
    )
    return { unit, left: indentColumns(leftTexts, TAB_SIZE), right: indentColumns(rightTexts, TAB_SIZE) }
  }, [splitView, splitRows])
  // The widest line number decides the gutter, so the rule never moves as you scroll.
  const lnDigits = useMemo(
    () => String(viewLines.reduce((m, l) => Math.max(m, l.oldNo ?? 0, l.newNo ?? 0), 0)).length,
    [viewLines]
  )

  // ── In-diff find (⌘F) ──
  const viewerRef = useRef<HTMLDivElement>(null)
  const findInputRef = useRef<HTMLInputElement>(null)
  const [findOpen, setFindOpen] = useState(false)
  const [findQuery, setFindQuery] = useState('')
  const [matchIdx, setMatchIdx] = useState(0)
  const [matchCount, setMatchCount] = useState(0)
  const findRe = useMemo(
    () => (findQuery.trim() ? buildQueryRegExp({ query: findQuery.trim(), caseSensitive: false, wholeWord: false, regex: false }, true) : null),
    [findQuery]
  )
  // The find layer rides on top of any externally-supplied search layers.
  const layers = useMemo<HighlightLayer[]>(
    () => (findRe ? [...highlightLayers, { re: findRe, className: 'diff-find-hit' }] : highlightLayers),
    [highlightLayers, findRe]
  )

  // Render one cell's HTML: syntax highlight → search layers → word marks (or
  // secret mask). Shared by unified and split views. Cached: a windowed column
  // re-renders every few rows scrolled, and highlighting the same lines again
  // each time would put the cost straight back. A new cache whenever anything
  // that shapes the HTML changes.
  const htmlCache = useMemo(
    () => new Map<string, string>(),
    [lang, layers, maskValues, wordDiffOn, wordRanges]
  )
  const cellHtml = (text: string, idx: number, kind: 'add' | 'del' | 'ctx'): string => {
    const key = `${idx}\0${kind}\0${text}`
    const hit = htmlCache.get(key)
    if (hit !== undefined) return hit
    const t = maskValues ? maskSecretLine(text) : text
    let html = highlightHtml(highlightLine(t, lang), layers)
    const wr = !maskValues && wordDiffOn && kind !== 'ctx' ? wordRanges.get(idx) : undefined
    if (wr) html = markRanges(html, wr, kind === 'add' ? 'word-add' : 'word-del')
    const out = html || '&nbsp;'
    htmlCache.set(key, out)
    return out
  }

  // Column-major split view scrolls each side on its own, so the position has
  // to be mirrored by hand. A mirrored write comes back as a scroll event on
  // the other column a frame later; recognised by the exact position written,
  // it is dropped. Mirrored back instead, it would write a stale position onto
  // a column still animating — a smooth jump down a long file stopped halfway.
  const leftColRef = useRef<HTMLDivElement>(null)
  const rightColRef = useRef<HTMLDivElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const mirrored = useRef(new WeakMap<Element, { top: number; left: number }>())
  const syncScroll = (from: React.RefObject<HTMLDivElement>, to: React.RefObject<HTMLDivElement>): void => {
    const src = from.current
    const dst = to.current
    if (!linkScroll || !src || !dst) return
    const echo = mirrored.current.get(src)
    mirrored.current.delete(src)
    if (echo && echo.top === src.scrollTop && echo.left === src.scrollLeft) return
    if (dst.scrollTop === src.scrollTop && dst.scrollLeft === src.scrollLeft) return
    dst.scrollTop = src.scrollTop
    dst.scrollLeft = src.scrollLeft
    // Read back: the browser may have clamped what was written.
    mirrored.current.set(dst, { top: dst.scrollTop, left: dst.scrollLeft })
  }

  // ── Windowed rendering (wrap off) ──
  // A full file is thousands of rows a side, and the browser's per-frame cost
  // while scrolling grows with every row in the DOM: `git.ts` in full ran at
  // 25 fps. With wrap off every row is exactly `rowH` tall, so row i sits at
  // i × rowH and each column renders only the rows in view (plus overscan),
  // padded to the full height so the scrollbar and the ruler stay true.
  // Measured from a 1em probe in whole pixels: a fractional row would drift a
  // pixel every few hundred rows, and a code-font-size change re-measures.
  const emProbeRef = useRef<HTMLSpanElement>(null)
  const [rowH, setRowH] = useState(17)
  useLayoutEffect(() => {
    const probe = emProbeRef.current
    if (!probe) return
    const measure = (): void => setRowH(Math.max(1, Math.round(probe.getBoundingClientRect().height * DV_LINE_HEIGHT)))
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(probe)
    return () => ro.disconnect()
  }, [splitView])

  const [ranges, setRanges] = useState<{ left: [number, number]; right: [number, number] }>({
    left: [0, 2 * OVERSCAN],
    right: [0, 2 * OVERSCAN]
  })
  // Each column from its own scroll position: unlinked, they can be far apart.
  const syncRanges = (): void => {
    const rangeOf = (col: HTMLDivElement | null): [number, number] =>
      col ? visibleRange(col.scrollTop, col.clientHeight, rowH, splitRows.length, OVERSCAN) : [0, 0]
    const left = rangeOf(leftColRef.current)
    const right = rangeOf(rightColRef.current)
    setRanges((r) =>
      r.left[0] === left[0] && r.left[1] === left[1] && r.right[0] === right[0] && r.right[1] === right[1]
        ? r
        : { left, right }
    )
  }
  // Before paint whenever the rows, their height or the mode change; and when
  // a column is resized, which changes how many rows are in view.
  useLayoutEffect(() => {
    if (!isCols) return
    syncRanges()
    const cols = [leftColRef.current, rightColRef.current].filter((c): c is HTMLDivElement => !!c)
    const ro = new ResizeObserver(() => syncRanges())
    cols.forEach((c) => ro.observe(c))
    return () => ro.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isCols, splitRows, rowH, fullPending])

  // Every row's width is known up front — monospace, tabs expanded — so a
  // column is as wide as its longest line even while that line is not rendered;
  // otherwise the sideways scrollbar would grow and shrink as you scroll down.
  const colWidth = useMemo(() => {
    const widest = (side: 'left' | 'right'): number =>
      splitRows.reduce((m, r) => {
        const text = side === 'left' && r.hunk !== undefined ? r.hunk : r[side]?.text ?? ''
        return Math.max(m, text.replace(/\t/g, ' '.repeat(TAB_SIZE)).length)
      }, 0)
    // Margin (digits, marker, 14px padding, 1px rule) plus the text's 20px padding.
    const width = (cols: number): string => `calc((${lnDigits} + 1.6 + ${cols}) * 1ch + 35px)`
    return { left: width(widest('left')), right: width(widest('right')) }
  }, [splitRows, lnDigits])

  // Search results and AI citations reveal a line by finding its row in the
  // DOM. A windowed column may not hold it: scroll there, render, hand it over.
  const newLineRow = useMemo(() => {
    const m = new Map<number, number>()
    splitRows.forEach((r, i) => {
      if (r.right?.no) m.set(r.right.no, i)
    })
    return m
  }, [splitRows])
  const lookupRef = useRef<(line: number) => Element | null>(() => null)
  lookupRef.current = (line) => {
    const right = rightColRef.current
    const i = newLineRow.get(line)
    if (!isCols || !right || i === undefined) return null
    for (const col of [leftColRef.current, right]) {
      if (col) col.scrollTop = Math.max(0, i * rowH - col.clientHeight / 2)
    }
    flushSync(syncRanges)
    return right.querySelector(`[data-row="${i}"]`)
  }
  useEffect(() => registerRowLookup((line) => lookupRef.current(line)), [])

  // One side of a split row. An absent side is a hatched filler rather than a
  // gap, so the two columns keep the same number of rows. The margin — line
  // number, then the −/+ marker — stays put while the code scrolls sideways.
  const splitCell = (r: SplitRow, i: number, side: 'left' | 'right', key: string): React.JSX.Element => {
    const c: SplitCell | undefined = side === 'left' ? r.left : r.right
    const indent = guides ? guides[side][i] : 0
    // No guide at column 0: the margin's rule already sits there.
    const guideWidth = guides ? indent - guides.unit : 0
    const stage = side === 'right' && r.startsHunk !== undefined ? stageButton(r.startsHunk) : null
    return (
      <div
        key={key}
        className={`diff-split-cell ${c ? c.kind : 'empty'}`}
        data-chg={blockAt.get(i)}
        data-row={i}
        // A filler's hatching is offset by its row, so the stripes line up with the fillers above.
        style={c ? undefined : ({ '--r': i } as React.CSSProperties)}
      >
        <span className="diff-margin">
          <span className="diff-gutter">{c?.no ?? ''}</span>
          <span className="diff-marker">{c?.kind === 'del' ? '−' : c?.kind === 'add' ? '+' : ''}</span>
        </span>
        {c ? (
          <span
            className="diff-text"
            style={guideWidth > 0 ? ({ '--gw': `${guideWidth}ch` } as React.CSSProperties) : undefined}
            dangerouslySetInnerHTML={{ __html: cellHtml(c.text, c.idx, c.kind) }}
          />
        ) : (
          <span className="diff-text" />
        )}
        {stage}
      </div>
    )
  }

  const stageButton = (hunkIdx: number): React.JSX.Element | null =>
    onStageHunk && hunkData ? (
      <button
        className="btn ghost tiny diff-stage-hunk"
        onClick={() => onStageHunk(`${hunkData.header}\n${hunkData.hunks[hunkIdx] ?? ''}\n`)}
      >
        {t('diff.stageHunk')}
      </button>
    ) : null

  // The @@ bar. Column-major mode draws it in both columns — the left carries
  // the header text, the right the stage button, and together they read as one
  // bar without either column losing a row.
  const splitHunk = (r: SplitRow, key: number, side?: 'left' | 'right'): React.JSX.Element => (
    <div key={key} className="diff-split-hunk" data-row={key}>
      <span className="diff-text">{side === 'right' ? '' : r.hunk}</span>
      {side !== 'left' && r.hunkIdx !== undefined && stageButton(r.hunkIdx)}
    </div>
  )

  // Line-level staging selection (only when staging is enabled). Keyed by index
  // into `lines`; cleared whenever the diff changes.
  const [selected, setSelected] = useState<Set<number>>(new Set())
  useEffect(() => setSelected(new Set()), [diff])

  const toggleLine = (i: number): void =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(i)) next.delete(i)
      else next.add(i)
      return next
    })

  const stageSelected = (): void => {
    if (!onStageHunk || !hunkData || selected.size === 0) return
    onStageHunk(buildLinePatch(lines, hunkData.header, selected))
    setSelected(new Set())
  }

  // With wrap off only the rows in view exist, so matches are counted in the
  // row data instead of the DOM — a match three thousand lines down counts,
  // and stepping to it scrolls there first. Row by row, left before right.
  const colMatches = useMemo(() => {
    if (!isCols || !findRe) return null
    const re = new RegExp(findRe.source, findRe.flags.includes('g') ? findRe.flags : `${findRe.flags}g`)
    const out: { row: number; side: 'left' | 'right'; k: number }[] = []
    splitRows.forEach((r, row) => {
      for (const side of ['left', 'right'] as const) {
        const c = r[side]
        if (!c) continue
        const n = (maskValues ? maskSecretLine(c.text) : c.text).match(re)?.length ?? 0
        for (let k = 0; k < n; k++) out.push({ row, side, k })
      }
    })
    return out
  }, [isCols, findRe, splitRows, maskValues])

  // Recount find matches whenever the query or rendered content changes.
  useEffect(() => {
    const root = viewerRef.current
    if (!root || !findRe) {
      setMatchCount(0)
      return
    }
    const n = colMatches ? colMatches.length : root.querySelectorAll('.diff-find-hit').length
    setMatchCount(n)
    setMatchIdx((i) => (n ? Math.min(i, n - 1) : 0))
  }, [findRe, diff, splitView, wordDiffOn, maskValues, colMatches])

  // Highlight + scroll the active match into view.
  useEffect(() => {
    const root = viewerRef.current
    if (!root) return
    if (colMatches) {
      const m = colMatches[matchIdx]
      if (!m) return
      for (const col of [leftColRef.current, rightColRef.current]) {
        if (col) col.scrollTop = Math.max(0, m.row * rowH - col.clientHeight / 2)
      }
      syncRanges()
      return
    }
    const hits = root.querySelectorAll<HTMLElement>('.diff-find-hit')
    hits.forEach((h, i) => h.classList.toggle('current', i === matchIdx))
    hits[matchIdx]?.scrollIntoView({ block: 'center', behavior: 'smooth' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [matchIdx, matchCount])

  // Windowed: the current match's element may only just have been rendered, or
  // re-rendered, so mark it after every commit. One query, only while finding.
  useLayoutEffect(() => {
    if (!colMatches) return
    viewerRef.current?.querySelectorAll('.diff-find-hit.current').forEach((h) => h.classList.remove('current'))
    const m = colMatches[matchIdx]
    const col = m && (m.side === 'left' ? leftColRef.current : rightColRef.current)
    col?.querySelectorAll(`[data-row="${m.row}"] .diff-find-hit`)[m.k]?.classList.add('current')
  })

  const stepMatch = (dir: 1 | -1): void => {
    if (matchCount === 0) return
    setMatchIdx((i) => (i + dir + matchCount) % matchCount)
  }

  const openFind = (): void => {
    setFindOpen(true)
    requestAnimationFrame(() => findInputRef.current?.select())
  }
  const closeFind = (): void => {
    setFindOpen(false)
    setFindQuery('')
  }
  useEffect(() => {
    if (findSignal > 0) openFind()
  }, [findSignal])

  // ── Change navigation (↑/↓ and the ruler) ──
  const [navIdx, setNavIdx] = useState(-1)
  // A layout effect declared before the landing, so the landing's "at change
  // 0" is the last word and the first ↓ goes to change 1.
  useLayoutEffect(() => setNavIdx(-1), [diff, splitView, fullLines])

  const scrollToBlock = (i: number, smooth = true): void => {
    const root = viewerRef.current
    if (!root || !blocks[i]) return
    setNavIdx(i)
    const behavior: ScrollBehavior = smooth ? 'smooth' : 'auto'
    if (isCols) {
      // A third of the way down leaves the lines above the change in sight. By
      // row index, not element: the change may not be rendered yet. Smooth and
      // linked, only the right column animates and the left mirrors it — two
      // animations each writing the other's scrollTop cancel each other halfway
      // down a long file. Unlinked, the other would stay put, so both; instant,
      // both, so the first frame already has both windows right.
      // Far away, it jumps: Chrome's smooth scroll takes seconds to cross
      // thousands of rows, and gliding past them shows nothing worth seeing.
      const right = rightColRef.current
      const target = right ? Math.max(0, blocks[i].start * rowH - right.clientHeight / 3) : 0
      const glide = smooth && !!right && Math.abs(target - right.scrollTop) < 3 * right.clientHeight
      const cols = glide && linkScroll ? [right] : [leftColRef.current, right]
      for (const col of cols) {
        col?.scrollTo({ top: Math.max(0, blocks[i].start * rowH - col.clientHeight / 3), behavior: glide ? 'smooth' : 'auto' })
      }
      // Instant (the landing, before paint; a long jump): render the new
      // window now, or a frame would show the rows from the old position.
      if (!glide) syncRanges()
    } else {
      root.querySelector<HTMLElement>(`[data-chg="${i}"]`)?.scrollIntoView({ block: 'center', behavior })
    }
  }

  const stepChange = (dir: 1 | -1): void => {
    if (blocks.length === 0) return
    const n = blocks.length
    scrollToBlock(navIdx < 0 ? (dir === 1 ? 0 : n - 1) : (navIdx + dir + n) % n)
  }

  // A full file opens at line 1, which can be two thousand lines above the
  // change you came for. Land on the first one — in a layout effect, before the
  // browser paints, so the file never shows at the top and then jumps. Once per
  // view: a refresh of the same one must not yank the reader back. Turning full
  // file off forgets it, so turning it on again lands again. A search result
  // being revealed still wins: that reveal runs on the next frame, after this.
  const landedFor = useRef<string | null>(null)
  useLayoutEffect(() => {
    if (!fullLines) {
      landedFor.current = null
      return
    }
    if (landedFor.current === viewKey) return
    landedFor.current = viewKey
    if (blocks.length > 0) scrollToBlock(0, false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fullLines, viewKey])

  const onViewerKeyDown = (e: React.KeyboardEvent): void => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'f') {
      e.preventDefault()
      e.stopPropagation()
      openFind()
    } else if (e.key === 'Escape' && findOpen) {
      closeFind()
    }
  }

  if (!diff.trim()) return <div className="diff-empty">{t('diff.noChanges')}</div>

  const hasWordDiffs = wordRanges.size > 0

  // One icon button of the toolbar strip: the label is the tooltip.
  const tool = (
    id: string,
    icon: React.JSX.Element,
    label: string,
    title: string,
    onClick: () => void,
    on?: boolean,
    disabled?: boolean
  ): React.JSX.Element => (
    <button
      key={id}
      className={`diff-tool${on ? ' on' : ''}`}
      data-tool={id}
      title={title}
      aria-label={label}
      aria-pressed={on}
      disabled={disabled}
      onClick={onClick}
    >
      {icon}
    </button>
  )

  return (
    <div
      className={`diff-viewer hljs ${splitView ? 'is-split' : ''} ${isCols ? 'is-nowrap' : ''} ${
        lines.length > HUGE_LINES ? 'is-huge' : ''
      } ${hoverArmed ? 'hover-armed' : ''}`}
      style={
        { '--ln-digits': lnDigits, '--iu': `${guides?.unit ?? TAB_SIZE}ch`, '--dv-row': `${rowH}px` } as React.CSSProperties
      }
      ref={viewerRef}
      tabIndex={0}
      onKeyDown={onViewerKeyDown}
      {...hoverProps}
    >
      {hoverCard}
      {splitView && <span className="diff-em-probe" ref={emProbeRef} aria-hidden="true" />}
      <div className="diff-toolbar">
        <div className="diff-tool-group">{toolbarStart}</div>
        <div className="diff-tool-group">
          {tool('prev-change', <ArrowUp size={14} />, t('diff.prevChange'), t('diff.prevChange'), () => stepChange(-1), false, blocks.length === 0)}
          {tool('next-change', <ArrowDown size={14} />, t('diff.nextChange'), t('diff.nextChange'), () => stepChange(1), false, blocks.length === 0)}
        </div>
        <div className="diff-tool-group segmented">
          {tool('unified', <Rows2 size={14} />, t('diff.unified'), t('diff.unifiedTitle'), () => setSplitView(false), !splitView)}
          {tool('split', <Columns2 size={14} />, t('diff.split'), t('diff.splitTitle'), () => setSplitView(true), splitView)}
        </div>
        <div className="diff-tool-group">
          {/* Needs the new side's text, which only a single-file viewer can read. */}
          {splitView &&
            loadNewText &&
            tool('full-file', <UnfoldVertical size={14} />, t('diff.fullFile'), t('diff.fullFileTitle'), () => setFullFile((v) => !v), fullFile)}
          {/* Only meaningful side-by-side: the unified view has the full width to
              scroll a long line through, a 50% column does not. */}
          {splitView && tool('wrap', <WrapText size={14} />, t('diff.wrap'), t('diff.wrapTitle'), () => setWrapOn((v) => !v), wrapOn)}
          {/* Only means anything when each column has its own scroller. */}
          {isCols &&
            tool(
              'linked',
              linkScroll ? <Link2 size={14} /> : <Unlink2 size={14} />,
              t('diff.linkScroll'),
              t('diff.linkScrollTitle'),
              () => setLinkScroll((v) => !v),
              linkScroll
            )}
          {onToggleIgnoreWs &&
            tool('whitespace', <Pilcrow size={14} />, t('diff.whitespace'), t('diff.whitespaceTitle'), onToggleIgnoreWs, ignoreWs)}
          {hasWordDiffs &&
            tool(
              'word-diff',
              <SplitSquareHorizontal size={14} />,
              t('diff.wordDiff'),
              t('diff.wordDiffTitle'),
              () => setWordDiffOn((v) => !v),
              wordDiffOn
            )}
          {tool('find', <Search size={14} />, t('diff.find'), t('diff.findTitle'), openFind, findOpen)}
        </div>
      </div>
      {findOpen && (
        <div className="diff-find">
          <Search size={13} className="diff-find-icon" />
          <input
            ref={findInputRef}
            className="diff-find-input"
            placeholder={t('diff.findPlaceholder')}
            value={findQuery}
            onChange={(e) => setFindQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                stepMatch(e.shiftKey ? -1 : 1)
              } else if (e.key === 'Escape') {
                e.preventDefault()
                closeFind()
              }
            }}
          />
          <span className="diff-find-count">{matchCount ? `${matchIdx + 1}/${matchCount}` : findQuery ? '0/0' : ''}</span>
          <button className="diff-find-btn" title={t('diff.prevMatch')} disabled={matchCount === 0} onClick={() => stepMatch(-1)}>
            <ChevronUp size={14} />
          </button>
          <button className="diff-find-btn" title={t('diff.nextMatch')} disabled={matchCount === 0} onClick={() => stepMatch(1)}>
            <ChevronDown size={14} />
          </button>
          <button className="diff-find-btn" title={t('diff.closeFind')} onClick={closeFind}>
            <X size={14} />
          </button>
        </div>
      )}
      {!splitView && onStageHunk && selected.size > 0 && (
        <div className="diff-select-bar">
          <span>{interp(t('diff.linesSelected'), { n: selected.size, s: selected.size === 1 ? '' : 's' })}</span>
          <button className="btn ghost tiny" onClick={() => setSelected(new Set())}>
            {t('diff.clearSelection')}
          </button>
          <button className="btn primary tiny" onClick={stageSelected}>
            {interp(t('diff.stageLines'), { n: selected.size, s: selected.size === 1 ? '' : 's' })}
          </button>
        </div>
      )}
      {splitView && fullPending ? (
        <div className="diff-scroll" />
      ) : splitView ? (
        wrapOn ? (
          <div className="diff-wrap-body">
            <div className="diff-scroll" ref={scrollRef}>
              <div className="diff-split">
                {splitRows.map((r, i) =>
                  r.hunk !== undefined ? (
                    splitHunk(r, i)
                  ) : (
                    <div key={i} className="diff-split-row">
                      {splitCell(r, i, 'left', `${i}l`)}
                      {splitCell(r, i, 'right', `${i}r`)}
                    </div>
                  )
                )}
              </div>
            </div>
            {splitRows.length > 0 && (
              <DiffOverview blocks={blocks} total={splitRows.length} scrollerRef={scrollRef} measured />
            )}
          </div>
        ) : (
          // Wrap off: a scrollbar per side, which needs one scroller per side —
          // hence column-major DOM. Each column scrolls both ways; the vertical
          // scroll is mirrored so the sides stay row-for-row, and the horizontal
          // one is not, which is the whole point. Rows line up because every
          // cell is exactly one line tall and the hunk bar spans both columns.
          <div className="diff-split is-cols">
            {(['left', 'right'] as const).map((side) => {
              const ref = side === 'left' ? leftColRef : rightColRef
              const other = side === 'left' ? rightColRef : leftColRef
              return (
                <div key={side} className={`diff-split-pane ${side}`}>
                  <div
                    className="diff-split-col"
                    ref={ref}
                    onScroll={() => {
                      syncScroll(ref, other)
                      syncRanges()
                    }}
                  >
                    {/* The inner block is as wide as the column's longest line, and every
                        row fills it — a row sized to its own text would leave its
                        background behind as soon as you scrolled past it. It is as tall
                        as every row; only the window is rendered, below a padding. */}
                    <div
                      className="diff-split-inner"
                      style={{
                        height: splitRows.length * rowH,
                        paddingTop: ranges[side][0] * rowH,
                        minWidth: `max(100%, ${colWidth[side]})`
                      }}
                    >
                      {splitRows.slice(ranges[side][0], ranges[side][1]).map((r, k) => {
                        const i = ranges[side][0] + k
                        return r.hunk !== undefined ? splitHunk(r, i, side) : splitCell(r, i, side, `${i}${side[0]}`)
                      })}
                    </div>
                  </div>
                </div>
              )
            })}
            {splitRows.length > 0 && <DiffOverview blocks={blocks} total={splitRows.length} scrollerRef={rightColRef} />}
          </div>
        )
      ) : (
        <div className="diff-scroll" ref={scrollRef}>
          <div className="diff-lines">
            {lines.map((l, i) => {
              if (l.kind === 'meta') return null
              if (l.kind === 'hunk') {
                return (
                  <div key={i} className="diff-line hunk">
                    <span className="diff-gutter" />
                    <span className="diff-gutter" />
                    <span className="diff-text">{l.text}</span>
                    {stageButton(l.hunkIdx)}
                  </div>
                )
              }
              const selectable = onStageHunk && (l.kind === 'add' || l.kind === 'del')
              return (
                <div
                  key={i}
                  className={`diff-line ${l.kind} ${selectable ? 'selectable' : ''} ${selected.has(i) ? 'line-selected' : ''}`}
                  data-chg={blockAt.get(i)}
                  onClick={selectable ? () => toggleLine(i) : undefined}
                  title={selectable ? t('diff.stageLinesTitle') : undefined}
                >
                  <span className="diff-gutter">{l.oldNo ?? ''}</span>
                  <span className="diff-gutter">{l.newNo ?? ''}</span>
                  <span className="diff-sign">{l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : ' '}</span>
                  <span
                    className="diff-text"
                    dangerouslySetInnerHTML={{ __html: cellHtml(l.text, i, l.kind as 'add' | 'del' | 'ctx') }}
                  />
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
