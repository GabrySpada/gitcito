import { useEffect, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, FolderGit2, Search } from 'lucide-react'
import { useSettingsStore } from '../stores/settings'
import { useReposStore } from '../stores/repos'
import { useUIStore } from '../stores/ui'
import { buildSections, filterSections, type RepoRow, type RepoSection, type SectionKind } from '../lib/repoSections'
import { RepositoryRow } from './RepositoryRow'
import { shellApi } from '../infrastructure/api'
import { tabRepos } from '../../../shared/types'
import { useT, interp, type TranslationKey } from '../i18n'

/** Section headings live here as keys, not strings: a module-level constant
 *  holding translated text freezes at whatever language was active on import. */
const SECTION_TITLE: Record<Exclude<SectionKind, 'workspace'>, TranslationKey> = {
  open: 'repos.sectionOpen',
  favourites: 'repos.sectionFavourites',
  recent: 'repos.sectionRecent',
  all: 'repos.sectionAll'
}

function sectionKey(section: RepoSection): string {
  return section.kind === 'workspace' ? `workspace:${section.workspaceId}` : section.kind
}

/**
 * The Repositories page — every repository Gitcito knows about, whether or not
 * it is open, grouped into sections you can collapse.
 *
 * Rows are deliberately cheap: name, owner and branch come from the registry,
 * which read them from files in `.git`. Nothing here spawns a git process, and
 * nothing refreshes on a timer — this is a page you open to find something.
 */
export function RepositoriesPage(): React.JSX.Element {
  const t = useT()
  const entries = useReposStore((s) => s.entries)
  const loading = useReposStore((s) => s.loading)
  const load = useReposStore((s) => s.load)
  const settings = useSettingsStore((s) => s.settings)
  const openRepoTab = useSettingsStore((s) => s.openRepoTab)
  const forget = useReposStore((s) => s.forget)
  const locate = useReposStore((s) => s.locate)
  const toggleFavouriteRepo = useSettingsStore((s) => s.toggleFavouriteRepo)
  const repathRepo = useSettingsStore((s) => s.repathRepo)
  const openModal = useUIStore((s) => s.openModal)

  const [query, setQuery] = useState('')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  // "Forget" sits next to a repository name, where it reads as "delete". The
  // confirm says what it does and does not do, rather than relying on the verb.
  const confirmForget = (path: string, label: string): void => {
    openModal({
      kind: 'confirm',
      title: t('repos.forget'),
      message: interp(t('repos.forgetConfirm'), { name: label }),
      danger: true,
      confirmLabel: t('repos.forgetAction'),
      onConfirm: () => void forget(path)
    })
  }

  const runLocate = async (path: string, label: string): Promise<void> => {
    const chosen = await shellApi.selectDirectory(interp(t('repos.locateTitle'), { name: label }))
    if (!chosen) return
    await locate(path, chosen)
    // The registry moved; the star, alias and profile binding are keyed by
    // path in settings and have to move with it.
    repathRepo(path, chosen)
  }

  useEffect(() => {
    void load()
  }, [load])

  const sections = useMemo(() => {
    const workspaceRepoPaths: Record<string, string[]> = {}
    for (const ws of settings.workspaces ?? []) {
      workspaceRepoPaths[ws.id] = ws.tabs.flatMap((tab) => tabRepos(tab).map((r) => r.path))
    }
    return filterSections(
      buildSections({
        registry: entries,
        openPaths: settings.tabs.flatMap((tab) => tabRepos(tab).map((r) => r.path)),
        favourites: settings.favouriteRepos ?? [],
        workspaces: settings.workspaces ?? [],
        workspaceRepoPaths,
        aliases: settings.repoAliases ?? {}
      }),
      query
    )
  }, [entries, settings, query])

  const toggle = (key: string): void => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  return (
    <div className="repos-page">
      <header className="repos-header">
        <h1 className="repos-title">
          <FolderGit2 size={16} /> {t('repos.title')}
        </h1>
        <div className="repos-search">
          <Search size={13} />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('repos.search')}
            aria-label={t('repos.search')}
          />
        </div>
        <button className="repos-btn" onClick={() => setCollapsed(new Set())}>
          {t('repos.expandAll')}
        </button>
        <button
          className="repos-btn"
          onClick={() => setCollapsed(new Set(sections.map(sectionKey)))}
        >
          {t('repos.collapseAll')}
        </button>
      </header>

      {!loading && entries.length === 0 ? (
        <p className="repos-empty">{t('repos.empty')}</p>
      ) : (
        <div className="repos-sections">
          {sections.map((section) => {
            const key = sectionKey(section)
            const isCollapsed = collapsed.has(key)
            const title =
              section.kind === 'workspace'
                ? (section.workspaceName ?? '')
                : t(SECTION_TITLE[section.kind])
            return (
              <section className="repos-section" key={key}>
                <button className="repos-section-head" onClick={() => toggle(key)} aria-expanded={!isCollapsed}>
                  {isCollapsed ? <ChevronRight size={13} /> : <ChevronDown size={13} />}
                  <span className="repos-section-title">{title}</span>
                  <span className="repos-section-count">{section.rows.length}</span>
                </button>
                {!isCollapsed && (
                  <div className="repos-rows">
                    {section.rows.length === 0 ? (
                      <p className="repos-none">{query ? t('repos.noMatches') : t('repos.emptySection')}</p>
                    ) : (
                      section.rows.map((row) => (
                        <RepositoryRow
                          key={row.repo.path}
                          row={row}
                          onOpen={(r: RepoRow) => openRepoTab({ path: r.repo.path, name: r.repo.name })}
                          onToggleFavourite={toggleFavouriteRepo}
                          onForget={confirmForget}
                          onLocate={(path, label) => void runLocate(path, label)}
                        />
                      ))
                    )}
                  </div>
                )}
              </section>
            )
          })}
        </div>
      )}
    </div>
  )
}
