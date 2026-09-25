// Which side of a working-tree file the viewer should show once the status
// moves on — pure, so the rule is tested without rendering the viewer.

import type { RepoStatus } from '../../../shared/types'

/** What to do with a view of `file`'s staged (or unstaged) diff:
 *  `stay`, move to the other side, or `gone` — no changes left on either. */
export type WipSideMove = { kind: 'stay' } | { kind: 'switch'; staged: boolean; untracked: boolean } | { kind: 'gone' }

/**
 * Staging from the diff can empty the side on screen — unstage every line and
 * nothing is left staged. The view follows the file to the side that still
 * lists it, the way the file lists do. A conflicted file stays where it is.
 */
export function wipSideAfter(status: RepoStatus, file: string, staged: boolean): WipSideMove {
  const has = (list: RepoStatus['staged']): boolean => list.some((f) => f.path === file)
  if (has(staged ? status.staged : status.unstaged) || has(status.conflicted)) return { kind: 'stay' }
  const other = (staged ? status.unstaged : status.staged).find((f) => f.path === file)
  return other ? { kind: 'switch', staged: !staged, untracked: !!other.untracked } : { kind: 'gone' }
}
