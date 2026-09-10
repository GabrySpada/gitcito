import { useEffect, useMemo, useRef, useState } from 'react'
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
  // Paths already fetched, in flight, or tried and failed. A ref rather than
  // state because the effect below both reads and writes it: as a dependency it
  // would retrigger the very effect that filled it, and every batch after the
  // first would be issued twice. Failures are recorded too — otherwise a path
  // whose call rejects is never cached and the effect loops for the whole visit.
  const attempted = useRef<Set<string>>(new Set())

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
    const found = await scan(roots)
    toast('success', interp(t('repos.scanFound'), { n: found }))
  }

  useEffect(() => {
    // Read straight from the store: the seed is a one-off snapshot taken on the
    // first load, not something that should re-run when a tab opens or closes.
    const current = useSettingsStore.getState().settings
    const seed = [
      ...current.tabs.flatMap((tab) => tabRepos(tab).map((r) => r.path)),
      ...(current.recentRepos ?? []).map((r) => r.path)
    ]
    void load(seed)
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
    let stopped = false
    const tried = attempted.current
    const wanted = [
      ...new Set(
        sections
          .filter((s) => !collapsed.has(sectionKey(s)))
          .flatMap((s) => s.rows)
          .filter((r) => !r.repo.missing)
          .map((r) => r.repo.path)
      )
    ].filter((p) => !tried.has(p))
    if (wanted.length === 0) return

    void (async () => {
      for (let i = 0; i < wanted.length; i += 8) {
        if (stopped) return
        // Claimed one batch at a time, not all at once: a run stopped halfway
        // must leave the paths it never reached free for the next one.
        const batch = wanted.slice(i, i + 8).filter((p) => !tried.has(p))
        if (batch.length === 0) continue
        for (const p of batch) tried.add(p)
        const results = await Promise.all(batch.map((p) => gitApi.repoPulse(p).catch(() => null)))
        // Recorded even when the run was stopped meanwhile: these paths are
        // claimed, so dropping the answer would leave them blank all visit.
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
      stopped = true
    }
  }, [wip, sections, collapsed])

  // The handbook offers turning the summary off and on as the way to see
  // current state — which only works if switching it off forgets what was
  // fetched. Handled here rather than in the effect: it is an event, not a
  // consequence of rendering.
  const toggleWip = (on: boolean): void => {
    setWip(on)
    if (on) return
    attempted.current = new Set()
    setPulses({})
  }

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
          <input type="checkbox" checked={wip} onChange={(e) => toggleWip(e.target.checked)} />
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
