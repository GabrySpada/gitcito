import type { GraphCommit } from '../../../shared/types'

/**
 * ⌘/Ctrl-click on a graph row: toggle `hash` in the multi-selection.
 *
 * The first modifier-click starts from the commit that is already selected
 * (`current`), as in any file manager. Without the seed, plain-click A then
 * ⌘-click B leaves only B in the set while A still looks selected, so the
 * right-click menu silently falls back to the single-commit one.
 */
export function toggleMultiSelect(multi: ReadonlySet<string>, hash: string, current: string | null): Set<string> {
  const next = new Set(multi)
  if (next.size === 0 && current && current !== hash) next.add(current)
  if (next.has(hash)) next.delete(hash)
  else next.add(hash)
  return next
}

/** Why a multi-selection cannot be squashed. The caller renders it via `t()`. */
export type SquashBlockedKey =
  | 'commit.squashBlocked.notTip'
  | 'commit.squashBlocked.gap'
  | 'commit.squashBlocked.root'

export type SquashCheck = { run: string[] } | { run: null; reason: SquashBlockedKey }

/**
 * The selection as a squashable run, newest-first — or why it is not one.
 *
 * Squash is a soft reset to the oldest commit's parent, so the selection must be
 * exactly the first N commits of HEAD's first-parent history. That is checked
 * against the parent chain, not against display rows: a stash row or a commit
 * from another lane can sit between two consecutive commits of the branch, and
 * row adjacency can also pair commits that are not related at all.
 *
 * Expects at least two selected commits.
 */
export function squashableRun(
  selected: ReadonlySet<string>,
  headHash: string | undefined,
  commitByHash: ReadonlyMap<string, GraphCommit>
): SquashCheck {
  // Commits on a branch that is not checked out are the common miss: HEAD is
  // elsewhere, and resetting it would rewrite the wrong branch.
  if (!headHash || !selected.has(headHash)) return { run: null, reason: 'commit.squashBlocked.notTip' }
  const run: string[] = []
  let hash: string | undefined = headHash
  while (hash && selected.has(hash) && run.length < selected.size) {
    run.push(hash)
    hash = commitByHash.get(hash)?.parents[0]
  }
  if (run.length !== selected.size) return { run: null, reason: 'commit.squashBlocked.gap' }
  // The oldest needs a parent to reset onto, so a run reaching the root can't go.
  const oldest = commitByHash.get(run[run.length - 1])
  return oldest && oldest.parents.length > 0 ? { run } : { run: null, reason: 'commit.squashBlocked.root' }
}
