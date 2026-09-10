import { useEffect, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, FolderGit2, Search } from 'lucide-react'
import { useSettingsStore } from '../stores/settings'
import { useReposStore } from '../stores/repos'
import { useUIStore } from '../stores/ui'
import { buildSections, filterSections, type RepoRow, type RepoSection, type SectionKind } from '../lib/repoSections'
import { RepositoryRow } from './RepositoryRow'
import { gitApi, shellApi } from '../infrastructure/api'
import { tabRepos, type RepoPulse } from '../../../shared/types'
import { useT, interp, type TranslationKey } from '../i18n'
import { openRepositoryDialog } from '../appCommands'

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

/** Uncommitted work of any kind — staged, unstaged or untracked. */
function dirtyCount(pulse: RepoPulse): number {
  return pulse.staged + pulse.unstaged + pulse.untracked
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
  const updateSettings = useSettingsStore((s) => s.update)
  const scanning = useReposStore((s) => s.scanning)
  const scan = useReposStore((s) => s.scan)
  const toast = useUIStore((s) => s.toast)

  const [query, setQuery] = useState('')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [wip, setWip] = useState(false)
  const [pulses, setPulses] = useState<Record<string, RepoPulse>>({})

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

  const runAddScanRoot = async (): Promise<void> => {
    const chosen = await shellApi.selectDirectory()
    if (!chosen) return
    const roots = [...settings.repoScanRoots, { path: chosen, depth: 3 }]
    updateSettings((s) => ({ ...s, repoScanRoots: roots }))
    // scan() replaces `entries` with the whole merged registry, not a delta —
    // the toast is "found N this scan", so diff the count around the await.
    const before = useReposStore.getState().entries.length
    await scan(roots)
    const found = useReposStore.getState().entries.length - before
    toast('success', interp(t('repos.scanFound'), { n: Math.max(0, found) }))
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

  // Status is opt-in because it is expensive: repoPulse spawns roughly five git
  // processes per repository, and this page can list every repo on the machine.
  // Only expanded sections are fetched, only once per visit, and never on a
  // timer — you open this page to find something, not to watch it.
  useEffect(() => {
    if (!wip) return
    let cancelled = false
    const wanted = [
      ...new Set(
        sections
          .filter((s) => !collapsed.has(sectionKey(s)))
          .flatMap((s) => s.rows)
          .filter((r) => !r.repo.missing)
          .map((r) => r.repo.path)
      )
    ].filter((p) => !(p in pulses))

    void (async () => {
      for (let i = 0; i < wanted.length; i += 8) {
        if (cancelled) return
        const batch = wanted.slice(i, i + 8)
        const results = await Promise.all(batch.map((p) => gitApi.repoPulse(p).catch(() => null)))
        if (cancelled) return
        setPulses((prev) => {
          const next = { ...prev }
          batch.forEach((p, n) => {
            const pulse = results[n]
            if (pulse) next[p] = pulse
          })
          return next
        })
      }
    })()

    return () => {
      cancelled = true
    }
  }, [wip, sections, collapsed, pulses])

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
        <div className="repos-actions">
          <button className="repos-btn" onClick={openRepositoryDialog}>
            {t('repos.openFolder')}
          </button>
          <button
            className="repos-btn"
            onClick={() => openModal({ kind: 'clone', onClone: (repo) => openRepoTab(repo) })}
          >
            {t('repos.clone')}
          </button>
          <button className="repos-btn" onClick={() => void runAddScanRoot()} disabled={scanning}>
            {scanning ? t('repos.scanning') : t('repos.addScanRoot')}
          </button>
        </div>
      </header>

      <div className="repos-toolbar">
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
        <label className="repos-wip-toggle" title={t('repos.wipTitle')}>
          <input type="checkbox" checked={wip} onChange={(e) => setWip(e.target.checked)} />
          {t('repos.wip')}
        </label>
      </div>

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
                      section.rows.map((row) => {
                        const pulse = pulses[row.repo.path]
                        return (
                          <RepositoryRow
                            key={row.repo.path}
                            row={row}
                            onOpen={(r: RepoRow) => openRepoTab({ path: r.repo.path, name: r.repo.name })}
                            onToggleFavourite={toggleFavouriteRepo}
                            onForget={confirmForget}
                            onLocate={(path, label) => void runLocate(path, label)}
                            wipPill={
                              wip && pulse ? (
                                <span className="repos-row-wip">
                                  {pulse.ahead > 0 && <span>↑{pulse.ahead}</span>}
                                  {pulse.behind > 0 && <span>↓{pulse.behind}</span>}
                                  {dirtyCount(pulse) > 0 ? (
                                    <span>●{dirtyCount(pulse)}</span>
                                  ) : (
                                    pulse.ahead === 0 && pulse.behind === 0 && <span>{t('repos.clean')}</span>
                                  )}
                                </span>
                              ) : undefined
                            }
                          />
                        )
                      })
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
