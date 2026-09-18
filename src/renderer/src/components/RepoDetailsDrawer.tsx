import { useEffect, useState } from 'react'
import { motion } from 'framer-motion'
import { FolderGit2, Globe, Loader2, X } from 'lucide-react'
import { gitApi } from '../infrastructure/api'
import { remoteWebUrl } from '../lib/autolink'
import { pickReadme } from '../lib/readme'
import { renderMarkdown } from '../preview/markdown'
import type { RepoRow } from '../lib/repoSections'
import type { RemoteInfo } from '../../../shared/types'
import { useT, interp } from '../i18n'

type Readme =
  | { status: 'loading' }
  | { status: 'ready'; html: string }
  | { status: 'none' }
  | { status: 'error'; message: string }

/** The remote's web home, and the host to name the button after. Prefers
 *  `origin`: a repo with three remotes still has one place it lives. */
function webHome(remotes: RemoteInfo[]): { url: string; host: string } | null {
  const pick = remotes.find((r) => r.name === 'origin') ?? remotes[0]
  const url = remoteWebUrl(pick?.url)
  if (!url) return null
  try {
    return { url, host: new URL(url).hostname.replace(/^www\./, '') }
  } catch {
    return null
  }
}

/**
 * The repository's front page, without opening it: name, where it lives, and
 * its README rendered — enough to tell two similarly named folders apart
 * before committing a tab to one.
 *
 * Read-only and disposable, so it holds no store state and takes the exclusive
 * lock never: three existing reads (`listDir`, `fileContent`, `remotes`) fired
 * on open, and nothing at all once closed.
 */
export function RepoDetailsDrawer({
  row,
  onClose,
  onOpen
}: {
  row: RepoRow
  onClose: () => void
  onOpen: (row: RepoRow) => void
}): React.JSX.Element {
  const t = useT()
  const [readme, setReadme] = useState<Readme>({ status: 'loading' })
  const [home, setHome] = useState<{ url: string; host: string } | null>(null)
  const path = row.repo.path

  useEffect(() => {
    let cancelled = false
    setReadme({ status: 'loading' })
    setHome(null)
    void (async () => {
      try {
        const entries = await gitApi.listDir(path)
        const file = pickReadme(entries)
        if (cancelled) return
        if (!file) {
          setReadme({ status: 'none' })
          return
        }
        const text = await gitApi.fileContent(path, file)
        // A repo's README is untrusted content, so no `allowFileMedia`: remote
        // badges render, `docs/shot.webp` does not. See the handbook.
        if (!cancelled) setReadme({ status: 'ready', html: renderMarkdown(text) })
      } catch (e) {
        if (!cancelled) setReadme({ status: 'error', message: (e as Error).message })
      }
    })()
    // The host button is decoration: a repo with no remote simply loses it.
    void gitApi
      .remotes(path)
      .then((remotes) => {
        if (!cancelled) setHome(webHome(remotes))
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [path])

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const title = row.repo.owner ? `${row.repo.owner}/${row.label}` : row.label
  // The panel is pinned to the inline end by the stylesheet; under RTL that is
  // the left edge, so the slide has to come from the same side.
  const offscreen = document.documentElement.dir === 'rtl' ? '-100%' : '100%'

  return (
    <motion.div
      className="repo-details-backdrop"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.15 }}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <motion.aside
        className="repo-details"
        role="dialog"
        aria-label={t('repos.detailsTitle')}
        initial={{ x: offscreen }}
        animate={{ x: 0 }}
        exit={{ x: offscreen }}
        transition={{ type: 'tween', ease: 'easeOut', duration: 0.2 }}
      >
        <header className="repo-details-head">
          <div className="repo-details-titles">
            {/* The owner and folder are user data, not copy — never translated. */}
            <h3>{title}</h3>
            <p className="repo-details-path">{path}</p>
          </div>
          <button className="repos-icon-btn" title={t('common.close')} aria-label={t('common.close')} onClick={onClose}>
            <X size={15} />
          </button>
        </header>

        <div className="repo-details-actions">
          <button className="repos-btn" disabled={row.repo.missing} onClick={() => onOpen(row)}>
            <FolderGit2 size={13} />
            {t('repos.detailsOpen')}
          </button>
          {home && (
            <a className="repos-btn" href={home.url}>
              <Globe size={13} />
              {interp(t('repos.detailsOpenHost'), { host: home.host })}
            </a>
          )}
        </div>

        <div className="repo-details-body">
          {readme.status === 'loading' && (
            <p className="repo-details-note">
              <Loader2 size={13} className="spin" /> {t('repos.detailsLoading')}
            </p>
          )}
          {readme.status === 'none' && <p className="repo-details-note">{t('repos.detailsNoReadme')}</p>}
          {readme.status === 'error' && (
            <p className="repo-details-note">{interp(t('repos.detailsError'), { error: readme.message })}</p>
          )}
          {readme.status === 'ready' && (
            <div className="md-preview" dangerouslySetInnerHTML={{ __html: readme.html }} />
          )}
        </div>
      </motion.aside>
    </motion.div>
  )
}
