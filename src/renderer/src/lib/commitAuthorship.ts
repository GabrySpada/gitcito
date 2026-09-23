import type { CommitAuthor } from '../../../shared/types'

/**
 * Whether amending a commit written by `author` would credit someone other
 * than `me` — `git commit --amend` keeps the original author and author date.
 * Email is the identity git and every forge match on; the name is only a
 * fallback for a repo with no `user.email` configured. With nothing to compare
 * we stay quiet rather than warn on a guess.
 */
export function isForeignAuthor(author: CommitAuthor | null, me: CommitAuthor | null): boolean {
  if (!author || !me) return false
  const theirs = author.email.trim().toLowerCase()
  const mine = me.email.trim().toLowerCase()
  if (theirs && mine) return theirs !== mine
  const theirName = author.name.trim()
  const myName = me.name.trim()
  return Boolean(theirName && myName) && theirName !== myName
}
