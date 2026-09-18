import type { TranslationKey } from '../i18n'

/**
 * A span of history the graph draws a divider for — "today", "2 days ago",
 * "a week ago". Like {@link TimeAgo} it carries a key and its variable rather
 * than finished text, so the label follows a language switch instead of
 * freezing at whatever was active when the graph was laid out.
 */
export interface DateBucket {
  /** Stable within a bucket, different across buckets. The boundary test. */
  id: string
  key: TranslationKey
  /** The number the key interpolates. Zero for the keys that take none. */
  n: number
  /**
   * How far back the bucket sits, in nominal days, so one bucket can be ordered
   * against another. Shared by every row in a bucket — which a row's own day
   * count is not, since a bucket spans several days once past "yesterday".
   */
  rank: number
}

/** A graph row as the divider pass sees it. Both times are unix seconds. */
export interface DividerRow {
  date: number
  /**
   * True for a row whose date is not its place in history — the WIP row is
   * synthesized at "now", and a stash is re-anchored beside the commit it was
   * taken from. Such a row neither carries a divider nor splits the run of
   * real commits around it.
   */
  skip?: boolean
}

/** Whole calendar days between two instants, in local time. */
function calendarDaysBetween(at: number, now: number): number {
  const midnight = (secs: number): number => {
    const d = new Date(secs * 1000)
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
  }
  return Math.round((midnight(now) - midnight(at)) / 86_400_000)
}

/** Whole calendar months between two instants, in local time. */
function calendarMonthsBetween(at: number, now: number): number {
  const a = new Date(at * 1000)
  const b = new Date(now * 1000)
  return (b.getFullYear() - a.getFullYear()) * 12 + (b.getMonth() - a.getMonth())
}

/**
 * The bucket a commit falls in, coarsening as it recedes: today, yesterday,
 * N days, a week, N weeks, a month, N months, a year, N years. Coarsening is
 * what keeps dividers rare in deep history — a per-day rule over a year of
 * commits is a wall, not a landmark.
 */
export function dateBucket(at: number, now: number): DateBucket {
  const days = calendarDaysBetween(at, now)
  // A commit dated ahead of the clock — skew, or a rewritten date — reads as
  // today rather than as a negative age.
  if (days <= 0) return { id: 'today', key: 'graph.bucket.today', n: 0, rank: 0 }
  if (days === 1) return { id: 'yesterday', key: 'graph.bucket.yesterday', n: 1, rank: 1 }
  if (days < 7) return { id: `d${days}`, key: 'graph.bucket.daysAgo', n: days, rank: days }
  if (days < 30) {
    const weeks = Math.floor(days / 7)
    return weeks === 1
      ? { id: 'w1', key: 'graph.bucket.weekAgo', n: 1, rank: 7 }
      : { id: `w${weeks}`, key: 'graph.bucket.weeksAgo', n: weeks, rank: weeks * 7 }
  }
  // Thirty days can still sit inside one calendar month (1–31 January), which
  // would otherwise read as "0 months ago" or tumble on into the year branch.
  const months = Math.max(1, calendarMonthsBetween(at, now))
  if (months < 12) {
    return months === 1
      ? { id: 'mo1', key: 'graph.bucket.monthAgo', n: 1, rank: 30 }
      : { id: `mo${months}`, key: 'graph.bucket.monthsAgo', n: months, rank: months * 30 }
  }
  const years = Math.floor(months / 12)
  return years === 1
    ? { id: 'y1', key: 'graph.bucket.yearAgo', n: 1, rank: 365 }
    : { id: `y${years}`, key: 'graph.bucket.yearsAgo', n: years, rank: years * 365 }
}

/**
 * Row index → the bucket that ends there, for every bucket the rows close.
 *
 * The divider sits at the *bottom* of a bucket's last row and carries that
 * bucket's label, so the newest bucket needs no rule above it and nothing
 * collides with the column header. The final row is deliberately left open:
 * its bucket may continue into commits that have not been loaded yet, and a
 * rule drawn there would jump as soon as they arrive.
 *
 * A run ends only where the next row is genuinely *older*. `--date-order` puts
 * a merge above its own parents, so a merge dated earlier than the branch tip
 * it merges leaves a newer commit sitting below an older one; treating that as
 * a boundary would ladder the dividers back and forth. Such a row joins the run
 * it lands in instead, which keeps them marching newer-to-older down the graph.
 */
export function dateDividers(rows: DividerRow[], now: number): Map<number, DateBucket> {
  const out = new Map<number, DateBucket>()
  let open: { row: number; bucket: DateBucket } | null = null
  for (let i = 0; i < rows.length; i++) {
    if (rows[i].skip) continue
    const bucket = dateBucket(rows[i].date, now)
    if (open && bucket.rank > open.bucket.rank) {
      out.set(open.row, open.bucket)
      open = { row: i, bucket }
    } else if (open) {
      open.row = i // same bucket, or an inversion absorbed into it
    } else {
      open = { row: i, bucket }
    }
  }
  return out
}
