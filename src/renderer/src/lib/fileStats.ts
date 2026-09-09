import type { TranslationKey } from '../i18n'

/** Change counts for a set of files, bucketed exactly as `statusClass` colours
 *  them — so a summary and the file tree below it never disagree. */
export interface FileStats {
  add: number
  mod: number
  del: number
  ren: number
  conflict: number
}

export type StatBucket = keyof FileStats

/** Untracked ('?') and copied ('C') files count as additions, and anything
 *  unrecognised as a modification: a file the summary cannot name is still a
 *  file that changed, and dropping it would make the chips undercount. */
export function bucketOf(status: string): StatBucket {
  switch (status) {
    case 'A':
    case 'C':
    case '?':
      return 'add'
    case 'D':
      return 'del'
    case 'R':
      return 'ren'
    case 'U':
      return 'conflict'
    default:
      return 'mod'
  }
}

export function fileStats(files: { status: string }[]): FileStats {
  const s: FileStats = { add: 0, mod: 0, del: 0, ren: 0, conflict: 0 }
  for (const f of files) s[bucketOf(f.status)]++
  return s
}

export interface StatChip {
  /** Shares the `tb-*` palette with the collapsed-folder badges. */
  cls: string
  labelKey: TranslationKey
  n: number
}

// Modifications lead because they dominate a typical commit; conflicts trail
// because they are the exception a reader scans to the end for.
const CHIPS: { bucket: StatBucket; cls: string; one: TranslationKey; many: TranslationKey }[] = [
  { bucket: 'mod', cls: 'tb-modified', one: 'chg.modifiedOne', many: 'chg.modified' },
  { bucket: 'add', cls: 'tb-added', one: 'chg.addedOne', many: 'chg.added' },
  { bucket: 'del', cls: 'tb-deleted', one: 'chg.deletedOne', many: 'chg.deleted' },
  { bucket: 'ren', cls: 'tb-renamed', one: 'chg.renamedOne', many: 'chg.renamed' },
  { bucket: 'conflict', cls: 'tb-conflicted', one: 'chg.conflictedOne', many: 'chg.conflicted' }
]

/** Keys, not strings — the caller renders them, so this stays testable against
 *  stable keys and never freezes copy at import time. */
export function summaryChips(s: FileStats): StatChip[] {
  return CHIPS.filter((c) => s[c.bucket] > 0).map((c) => ({
    cls: c.cls,
    labelKey: s[c.bucket] === 1 ? c.one : c.many,
    n: s[c.bucket]
  }))
}
