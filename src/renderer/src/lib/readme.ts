import type { TreeEntry } from '../../../shared/types'

// Which README a repository's root offers, and in what order we would rather
// render them. Markdown first, then plain text, then an extensionless README —
// all three go through the markdown renderer, so the ranking is about which
// one a reader most likely thinks of as *the* README, not about parsing.
const README_EXTS = ['.md', '.markdown', '.mdown', '.txt', '']

/**
 * The README to preview, given the entries of a repository's root directory,
 * or null when it has none.
 *
 * Root only and exact-stem only: `docs/README.md` belongs to the docs folder
 * and `README_OLD.md` is somebody's leftover — neither is the front page.
 */
export function pickReadme(entries: TreeEntry[]): string | null {
  let best: { rank: number; path: string } | null = null
  for (const entry of entries) {
    if (entry.dir || entry.path.includes('/')) continue
    const lower = entry.name.toLowerCase()
    if (!lower.startsWith('readme')) continue
    const rank = README_EXTS.indexOf(lower.slice('readme'.length))
    if (rank < 0) continue
    // Strictly better only: two spellings of the same rank keep listing order.
    if (!best || rank < best.rank) best = { rank, path: entry.path }
  }
  return best?.path ?? null
}
