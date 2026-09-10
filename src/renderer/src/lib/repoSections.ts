import type { RegistryRepo, Workspace } from '../../../shared/types'

// Assembling the Repositories page. Pure: the page hands in the registry and
// what it knows about tabs and workspaces, and gets back the sections to draw.

export type SectionKind = 'open' | 'favourites' | 'recent' | 'workspace' | 'all'

export interface RepoRow {
  repo: RegistryRepo
  /** Alias if one is set, else the folder name — what the row displays. */
  label: string
  favourite: boolean
}

export interface RepoSection {
  kind: SectionKind
  /** Set only for `workspace` sections: which workspace this is. */
  workspaceId?: string
  /** Set only for `workspace` sections: its name, which is user data, not copy. */
  workspaceName?: string
  rows: RepoRow[]
}

export interface SectionInput {
  registry: RegistryRepo[]
  /** Paths open in a tab in the active workspace. */
  openPaths: string[]
  favourites: string[]
  workspaces: Workspace[]
  /** Repo paths per workspace id — the caller flattens tabs, since only it
   *  knows how a workspace's tabs are stored. */
  workspaceRepoPaths: Record<string, string[]>
  aliases: Record<string, string>
}

function rowsFor(paths: string[], input: SectionInput): RepoRow[] {
  const byPath = new Map(input.registry.map((r) => [r.path, r]))
  const favourites = new Set(input.favourites)
  const rows: RepoRow[] = []
  for (const path of paths) {
    const repo = byPath.get(path)
    if (!repo) continue // open but never indexed: the next `remember` fixes it
    rows.push({ repo, label: input.aliases[path] || repo.name, favourite: favourites.has(path) })
  }
  return rows
}

function byLabel(a: RepoRow, b: RepoRow): number {
  return a.label.localeCompare(b.label)
}

/**
 * The page's sections, in display order.
 *
 * A repository appears in **every** section it qualifies for — open, starred
 * and in two workspaces means four rows. Each section is then a complete answer
 * to its own question, which is what makes them independently readable; the
 * cost is a longer page, which collapsing and the filter address.
 */
export function buildSections(input: SectionInput): RepoSection[] {
  const sections: RepoSection[] = []

  sections.push({ kind: 'open', rows: rowsFor(input.openPaths, input).sort(byLabel) })
  sections.push({ kind: 'favourites', rows: rowsFor(input.favourites, input).sort(byLabel) })

  // Recent is the registry ordered by when it was last opened. A scanned repo
  // the user has never opened has no place here, however recently it was found.
  const recent = input.registry
    .filter((r) => r.lastOpenedAt > 0)
    .sort((a, b) => b.lastOpenedAt - a.lastOpenedAt)
    .map((r) => r.path)
  sections.push({ kind: 'recent', rows: rowsFor(recent, input) })

  for (const ws of input.workspaces) {
    sections.push({
      kind: 'workspace',
      workspaceId: ws.id,
      workspaceName: ws.name,
      rows: rowsFor(input.workspaceRepoPaths[ws.id] ?? [], input).sort(byLabel)
    })
  }

  sections.push({ kind: 'all', rows: rowsFor(input.registry.map((r) => r.path), input).sort(byLabel) })
  return sections
}

/** Apply the search box. Sections that match nothing are kept, empty, so the
 *  page can say "no matches" there rather than silently losing a heading. */
export function filterSections(sections: RepoSection[], query: string): RepoSection[] {
  const q = query.trim().toLowerCase()
  if (!q) return sections
  const matches = (row: RepoRow): boolean =>
    row.label.toLowerCase().includes(q) ||
    row.repo.name.toLowerCase().includes(q) ||
    (row.repo.owner?.toLowerCase().includes(q) ?? false) ||
    row.repo.path.toLowerCase().includes(q)
  return sections.map((s) => ({ ...s, rows: s.rows.filter(matches) }))
}
