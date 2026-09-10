# Repositories page — design

Date: 2026-09-10
Status: approved, ready for an implementation plan

## The problem

Gitcito can only show you a repository it already has open. Mission control
derives its rows from `settings.tabs`, so it answers "which of the repos I am
working on needs me?" — a monitoring question. It cannot answer the management
question that comes first: *where is that repo, and how do I get it open?*

Today the answers are scattered. `recentRepos` holds eight entries and feeds the
launcher and the app menu. The clone modal lives in the command palette. A repo
you cloned three months ago and have not opened since is, as far as Gitcito is
concerned, gone.

GitKraken's Repository Management pane is the shape of the answer: sections for
open, favourite, recent and workspace repositories, a registry of everything it
knows about, and the entry points for getting a new repo in.

## What we are building

A new **Repositories page tab** — a launcher and manager, sitting alongside
Insights, Vault and Logs. Mission control is not modified.

The division of labour is deliberate and should stay legible:

| | Mission control | Repositories |
|---|---|---|
| Question | Which repo needs me? | Where is that repo? |
| Scope | Active workspace | Every repo Gitcito knows about |
| Surface | Toggled overlay (`missionOpen`) | Page tab |
| Cost | Full `repoPulse` per repo, refreshed every 30s | Free rows; status opt-in, never on a timer |

## Data model

Three pieces of state, split by whether they can be regenerated. Losing the
registry costs a rescan; losing a favourite costs information that exists
nowhere else. That difference decides where each lives.

### The registry — `gitcito-repos.json` in userData, owned by main

```ts
/** One repository Gitcito knows about. Everything here except `path` is a
 *  cache: lose the file and a rescan rebuilds it. */
export interface RegistryRepo {
  /** Canonical absolute path — the identity, and the key used everywhere else. */
  path: string
  /** Folder name at index time. `repoAliases` still wins for display. */
  name: string
  /** First path segment after the host in origin's URL: 'top-solution' for a
   *  GitLab group, the org for GitHub. Null with no remote or an unparseable URL. */
  owner: string | null
  /** Branch as of the last index. Refreshed by reading .git/HEAD, not by spawning git. */
  branch: string | null
  /** How it got here — a scanned repo the user has never opened still lists. */
  source: 'opened' | 'scanned'
  /** Unix seconds. Drives the Recent section, which is therefore not capped at 8. */
  lastOpenedAt: number
  /** Set when the folder was absent at the last existence check. Stored, not
   *  computed: stat-ing 200 paths belongs on page load, never in a render. */
  missing: boolean
}
```

### Scan roots — in `AppSettings`

```ts
export interface RepoScanRoot {
  path: string
  /** How deep to descend. 3 covers ~/Code/<client>/<repo>; deeper gets slow
   *  fast, and a repo nested further is almost always vendored. */
  depth: number
}
```

A preference, not an index: it is a statement of where the user keeps their
work, and it should survive to a new machine.

### Favourites — `favouriteRepos: string[]` in `AppSettings`

Path-keyed, alongside the existing `repoAliases` and `repoProfiles` maps, which
are keyed the same way for the same reason — the same folder reached through two
tabs must not diverge.

### What does not change

`recentRepos` keeps its shape, its 8-entry cap and both its consumers (the
launcher and `useAppMenu`). The Recent *section* on the new page reads
`lastOpenedAt` from the registry instead, so it can show more than eight without
touching anything that exists today.

## Main process — `src/main/repoRegistry.ts`

Modelled on `src/main/vault.ts`: one module owning one JSON file in userData,
exposing `registerRepoRegistryHandlers()` called from `main/index.ts`.

| Handler | Does |
|---|---|
| `repos:list` | Read the file, stat every path, update `missing`, return the list |
| `repos:remember` | Upsert on open — bumps `lastOpenedAt`, refreshes `owner` and `branch` |
| `repos:forget` | Drop one entry from the registry. Never touches disk |
| `repos:scan` | Walk each scan root to its depth, upsert what is found as `source: 'scanned'` |
| `repos:locate` | Re-point a missing entry at a new path, carrying favourite and alias across |
| `repos:refresh` | Re-read `branch` for a set of paths |

### Why not a `gitService` method

Registry calls are not repo-scoped git operations. They take no lock, write no
operation-log entry, and trigger no refresh — and `repos:locate` and `repos:scan`
are handed paths that may not be repositories at all, so the git dispatcher's
"first argument is the lock key" contract would be a lie. Own domain, own
channel, per CLAUDE.md's one-handler-module-per-domain rule.

### Three implementation constraints

**`branch` reads `.git/HEAD` directly.** A file read instead of a process spawn
is the entire reason rows are free. Worktrees and submodules have a `.git` *file*
containing a `gitdir:` pointer; the reader follows it.

**`owner` parses `.git/config`.** Again a file read. SSH (`git@host:group/repo.git`),
HTTPS, and nested GitLab groups (`top-solution/sub/repo` → `top-solution`) all
normalise to the first path segment after the host. No remote, or an
unrecognisable URL, yields `null` and the column stays empty — never a guess.

**Scanning prunes aggressively.** Stop descending the moment a `.git` is found —
a repository's own contents are never walked. Skip `node_modules`, `.git`, and
dot-directories outright. Without this, one `~/Code` scan walks a million files.

### Security

Paths from the registry cross back into git operations, so `repos:remember`,
`repos:scan` and `repos:locate` sanity-check them before any path reaches the
filesystem: absolute, non-empty, no NUL, length-capped, and — for `locate` — the
target must actually contain a `.git`.

Deliberately **not** `isSafeRepoPath` (`main/aiSchemas.ts`), despite CLAUDE.md §5:
that helper guards *repo-relative paths produced by a model*, which are joined
onto a repo root, and it rejects every absolute path. Registry paths are absolute
by definition and originate from the user's own folder picker or from scanning
folders they configured — no model or CLI output reaches this surface. `repos:forget` removes an index
entry and nothing else; the wording in the UI must make that unambiguous, since
"remove" next to a repository name invites the other reading.

## Renderer

### `stores/repos.ts`

A new slice: `entries`, `loading`, `scanning`, and the actions `load()`,
`scan()`, `forget()`, `locate()`. `toggleFavourite()` writes to the settings
store rather than this one, because that is where favourites live.

The page loads the store on mount. Nothing subscribes to it in the background.

### `lib/repoSections.ts`

Pure, no React, no IPC, tested in `pureLogic.test.ts`. This holds the logic worth
testing:

- assembling the sections from registry + tabs + workspaces + favourites
- the search filter, applied within each section, so a section with no matches
  says so rather than vanishing
- sort within a section (name, owner, last opened)

**Duplication policy: a repo appears in every section it qualifies for.** Open,
favourited and present in two workspaces means four rows. This mirrors GitKraken
and keeps each section a complete, independently readable answer to its own
question — "what is open?" is wrong if it omits an open repo because that repo
happened to be starred. The cost is a longer page; collapsible sections and the
search box are the mitigation.

### `components/RepositoriesPage.tsx`

**Sections**, each collapsible with a count badge and a header action row:

1. **Open repositories** — repos in tabs in the active workspace. Header: Close all tabs, Fetch all.
2. **Favourites** — `favouriteRepos`, across every workspace.
3. **Recent** — registry entries by `lastOpenedAt`, uncapped.
4. **One per workspace** — each saved workspace's repos. Header: open that workspace.
5. **All repositories** — the whole registry.

**A row** carries: star toggle · name (alias-aware) · owner · branch chip ·
open-in-tab · the existing repository context menu, extended with Star, Forget
and Locate…. Full path on hover. A `missing` row is greyed, marked, and offers
Locate… and Forget inline.

Reusing the shared repository context menu matters — it is already the same menu
on five surfaces, and a sixth that differs would be a papercut on every one of
them.

**Top bar**: Open folder… · Clone… (both reusing the existing commands in
`appCommands.ts`) · Add scan folder… · search · Collapse/Expand all · **WIP
summary** checkbox.

**WIP summary**, when ticked, calls `gitApi.repoPulse` for rows in *expanded*
sections only, in batches, and never on a timer. `repoPulse` costs roughly five
process spawns per repository; on a 40-repo registry an always-on refresh would
be 200 spawns a cycle. Mission control auto-refreshes because you leave it open
to watch. This page you open to find something, and then leave.

## Wiring

- `PageContent` gains `{ type: 'repositories' }`; the page router renders it.
- **Entry points:** a folder button in `WorkspaceSwitcher.tsx` beside the
  mission-control gauge, and a `repositories` command-palette entry.
- **i18n:** roughly 35 new keys across **16 locale dictionaries** — the largest
  mechanical cost in the feature, and the part most likely to be quietly
  half-finished. Real translations, not English copied across (CLAUDE.md §3).
- **Docs:** a new `docs/help/repositories.md`; `scripts/docs-map.json` gains
  `pages.covered.repositories` and `commands.covered.repositories`; one line in
  `README.md`; a cross-link from `workspaces.md` and `mission-control.md`.
- **Screenshot:** the page is a new surface, so it needs one. An entry in
  `examples/screenshots/shots.config.mjs`, with a playground scenario that
  populates several sections deterministically — a scan root with a handful of
  repos under it, so the shot is reproducible rather than a picture of whatever
  was on the machine that day.

## Testing

- `pureLogic.test.ts` — section assembly (including a repo qualifying for four
  sections at once), search filtering, per-section sort.
- A git-backed test for the scanner against `cloneFixture` repos: nested
  repositories are pruned, a `.git`-file worktree resolves, a repo with no
  remote yields `owner: null`, `node_modules` is skipped.
- A registry round-trip test: remember, forget, locate carrying a favourite
  across, and a missing entry surviving a reload rather than being pruned.
- `i18n.test.ts` picks up key parity and placeholder consistency automatically.

## Build order

1. Shared types, `main/repoRegistry.ts`, handlers, scanner tests. No UI.
2. Preload namespace, `reposApi` in `api.ts`, `stores/repos.ts`.
3. `lib/repoSections.ts` and its tests — pure logic before any component.
4. `RepositoriesPage.tsx`: sections and rows, cheap rows only.
5. Star, Forget, Locate, missing state.
6. WIP summary toggle.
7. Entry points, i18n across 16 locales, docs, screenshot, README.

Steps 1–3 have no UI to look at, which is why they come first: the section
assembly has cases ("open and favourited and in two workspaces") that are far
cheaper to pin down in a test than to discover by clicking a half-built page.

## Deliberately out of scope

- **Init a new repository.** Considered and dropped; Gitcito has no init flow and
  this feature does not need one to be useful.
- **Workspace management from this page.** `WorkspaceSwitcher` already owns
  create, rename, reorder and delete. Two surfaces doing it would drift.
- **Cloud workspaces and an Integrations button.** Gitcito's hosting model is
  per-repository, not per-workspace; there is nothing behind these yet.
- **Dragging a repo into a workspace.** A reasonable follow-up, not part of this.
- **Any change to mission control.**

## Risks

**Scan cost on a large tree** is the one thing that can make this feel bad. Depth
limits and pruning mitigate it, but the first scan of a deep `~/Code` will take a
visible moment and needs a progress affordance, not a frozen page.

**Sixteen locales times thirty-five keys.** The guard enforces presence, not
quality; the temptation to paste English is real and the rule against it is
explicit.

**Two lists of repositories can drift** in naming and ordering. Both read
`repoAliases`, and the row context menu is literally shared code, which is most
of the defence.
