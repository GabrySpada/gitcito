import { AlertTriangle, GitBranch, Star } from 'lucide-react'
import { useUIStore } from '../stores/ui'
import { repositoryMenuItems } from '../lib/repositoryMenuItems'
import type { RepoRow } from '../lib/repoSections'
import { useT } from '../i18n'

export interface RepositoryRowProps {
  row: RepoRow
  onOpen: (row: RepoRow) => void
  onToggleFavourite: (path: string) => void
  /** Opens the "remove from list" confirm — the folder on disk is untouched. */
  onForget: (path: string, label: string) => void
  /** Only reachable when the row is missing: picks a new folder and re-keys it. */
  onLocate: (path: string, label: string) => void
}

/**
 * One row of the Repositories page: name, owner, branch (or the missing
 * state), a star toggle, and the same repository context menu used on five
 * other surfaces (tabs, chips, the launcher, the toolbar dropdown) — with
 * Star, and Locate when the folder has moved, appended as `extras`.
 */
export function RepositoryRow({
  row,
  onOpen,
  onToggleFavourite,
  onForget,
  onLocate
}: RepositoryRowProps): React.JSX.Element {
  const t = useT()
  const openContextMenu = useUIStore((s) => s.openContextMenu)

  return (
    <div
      className={`repos-row${row.repo.missing ? ' repos-row-missing' : ''}`}
      title={row.repo.path}
      onContextMenu={(e) => {
        e.preventDefault()
        e.stopPropagation()
        openContextMenu(
          e.clientX,
          e.clientY,
          repositoryMenuItems(row.repo.path, () => onForget(row.repo.path, row.label), [
            {
              label: t(row.favourite ? 'repos.unstar' : 'repos.star'),
              onClick: () => onToggleFavourite(row.repo.path)
            },
            ...(row.repo.missing
              ? [{ label: t('repos.locate'), onClick: () => onLocate(row.repo.path, row.label) }]
              : [])
          ])
        )
      }}
    >
      <button
        className="repos-star"
        aria-pressed={row.favourite}
        title={t(row.favourite ? 'repos.unstar' : 'repos.star')}
        onClick={(e) => {
          e.stopPropagation()
          onToggleFavourite(row.repo.path)
        }}
      >
        <Star size={12} fill={row.favourite ? 'currentColor' : 'none'} />
      </button>
      <button
        className="repos-row-open"
        disabled={row.repo.missing}
        title={t('repos.openInTab')}
        onClick={() => onOpen(row)}
      >
        <span className="repos-row-name">{row.label}</span>
        <span className="repos-row-owner">{row.repo.owner ?? t('repos.noOwner')}</span>
        {row.repo.missing ? (
          <span className="repos-row-missing-tag">
            <AlertTriangle size={11} /> {t('repos.missing')}
          </span>
        ) : (
          <span className="repos-row-branch">
            <GitBranch size={11} /> {row.repo.branch ?? ''}
          </span>
        )}
      </button>
      {row.repo.missing && (
        <span className="repos-row-fix">
          <button className="repos-btn" onClick={() => onLocate(row.repo.path, row.label)}>
            {t('repos.locate')}
          </button>
          <button className="repos-btn" onClick={() => onForget(row.repo.path, row.label)}>
            {t('repos.forgetAction')}
          </button>
        </span>
      )}
    </div>
  )
}
