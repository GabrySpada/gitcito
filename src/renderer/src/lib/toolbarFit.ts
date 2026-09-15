/**
 * Arithmetic for the action bar's layout: how much room a window-centred block
 * has, how many of its items fit there, and when the search field gives up its
 * box for an icon. Pure so it can be tested without a DOM — the Toolbar feeds
 * it measured rects.
 */

/** A horizontal span — what a DOMRect reduces to for this arithmetic. */
export interface Span {
  left: number
  right: number
}

/** One measured slot on the bar. Separators trim differently, hence the flag. */
export interface FitItem {
  width: number
  sep?: boolean
}

/**
 * Width below which the graph search trades its field for an icon. Above the
 * window's own 1100px minimum (`minWidth` in main/index.ts) on purpose — a
 * threshold under that floor could never fire — and below the ~1400px at which
 * the whole bar fits centred anyway, so a roomy window keeps its field.
 */
export const SEARCH_COLLAPSE_AT = 1280

/**
 * Room a block centred on `bar`'s midpoint has before it touches either rail.
 * Symmetric by construction: the tighter side sets the half-width, so the block
 * stays on the midpoint instead of borrowing space from the shorter rail — which
 * is exactly what made it drift with the repository name's length.
 */
export function centeredRoom(bar: Span, left: Span, right: Span, gutter: number): number {
  const mid = (bar.left + bar.right) / 2
  const half = Math.min(mid - left.right, right.left - mid) - gutter
  return Math.max(0, half * 2)
}

/**
 * How many leading items of `items` fit in `room`. Everything fits or the tail
 * folds into a "More" button, which has to pay for its own width first.
 */
export function visibleCount(items: FitItem[], room: number, gap: number, moreWidth: number): number {
  const span = (it: FitItem): number => it.width + gap
  const total = items.reduce((sum, it) => sum + span(it), 0)
  if (total <= room) return items.length
  let used = moreWidth
  let shown = 0
  for (const it of items) {
    if (used + span(it) > room) break
    used += span(it)
    shown++
  }
  // A separator with nothing behind it is a stray line, not a divider.
  while (shown > 0 && items[shown - 1].sep) shown--
  return shown
}

/**
 * Whether the search shows as an icon rather than a field. Keyed to the bar's
 * own width rather than to whether the centred block fits, so the two never
 * feed back into each other and oscillate. A filter in effect always keeps the
 * field open: a graph filtered by an invisible control reads as a bug.
 */
export function searchCollapsed(barWidth: number, filter: string, expanded: boolean): boolean {
  if (filter.trim() || expanded) return false
  // Zero means "not measured yet" — collapsing then would flash on first paint.
  return barWidth > 0 && barWidth < SEARCH_COLLAPSE_AT
}
