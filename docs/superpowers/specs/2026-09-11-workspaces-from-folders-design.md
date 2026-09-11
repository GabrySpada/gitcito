# Workspaces from folders — design

Date: 2026-09-11
Status: awaiting review

## The problem

A developer's code folder already encodes the grouping they want. `~/Code`
holds `Arduino`, `Collins`, `personal`, `top-solution`; each is a context they
switch between. Gitcito has exactly the right structure for that — workspaces,
each with its own tab strip — and no way to get from one to the other except by
building every workspace by hand, one repository at a time.

Scanning already walks that tree. `scanForRepos` descends from a root, stops at
each repository, and returns absolute paths. Today all that structural knowledge
is thrown away: `scanRoots` flattens the result into a registry keyed by path,
and the shape of the folder tree is lost.

This is about keeping it.

## What we are building

**Add scan folder…** on the Repositories page gains a second phase. After the
scan indexes what it found, a dialog offers the root's immediate subfolders as
candidate workspaces. You tick the ones you want; Gitcito creates a workspace
per ticked folder, with one repository tab per repository beneath it.

Nothing is created without that confirmation, and nothing that already exists is
ever removed or reordered.

### Decisions taken

| Question | Decision |
|---|---|
| Which folders are offered | Immediate children of the scan root that contain at least one repository, at any depth beneath them, plus one row for repositories sitting loose at the root itself |
| What a workspace contains | One `repo` tab per repository found beneath the folder, at any depth — not a group tab |
| Repeat scans | Merge. New repositories are appended to the matching workspace; nothing is removed, renamed or reordered |
| When it happens | After the scan, in a picker dialog that is also the preview. Never automatic |

The one-tab-per-repository decision was taken with its cost stated: a folder of
fifty repositories produces a fifty-tab strip. That is accepted. The workspace
switcher, not the tab strip, is expected to carry navigation at that size.

## Where the logic lives

**Entirely in the renderer, as a pure function.** The main process does not
change: `scanForRepos` already returns absolute paths, and `relative(root, repo)`
recovers everything the grouping needs. No new git method, no new IPC channel,
no `READ_METHODS` entry.

New file, `src/renderer/src/lib/workspacePlan.ts`:

```ts
export interface WorkspaceCandidate {
  /** Folder name — and the workspace name. For the loose-repos row, the
   *  root's own basename. */
  name: string
  /** Absolute folder path. The row's identity, and what gets written to
   *  `Workspace.sourcePath`. */
  path: string
  /** Every repository beneath this folder, absolute, sorted by name. */
  repoPaths: string[]
  /** Of those, the ones not already a tab in `existingWorkspaceId`. Equal to
   *  `repoPaths` when there is no existing workspace. */
  newRepoPaths: string[]
  /** Set when this folder maps onto a workspace that already exists — the
   *  dialog says so, and creation merges rather than adds. */
  existingWorkspaceId?: string
  /** True for the row covering repositories directly inside the root. */
  loose: boolean
}

export function planWorkspaces(input: {
  root: string
  /** Absolute repository paths, typically the registry filtered to this root. */
  repoPaths: string[]
  workspaces: Workspace[]
}): WorkspaceCandidate[]
```

Being pure and path-shaped, it is tested in `pureLogic.test.ts` against string
arrays — no filesystem, no fixtures, no git. That is the cheapest coverage in
the repo and this is exactly the kind of logic that belongs there.

### Attribution rule

A repository is attributed to the **first path segment below the root**, however
deep it actually sits. `~/Code/Collins/microtecnica/ilcm` belongs to `Collins`.
A repository whose path has no segment below the root — `~/Code/loose-repo` —
goes to the loose row.

Candidates with no repositories are not returned. A folder of documents is not
offered as an empty workspace.

## Matching an existing workspace

Matching by name alone breaks the moment a workspace is renamed: the next scan
sees no match and creates a duplicate. So `Workspace` gains one optional field:

```ts
/** The folder this workspace was generated from, when it was. Lets a rescan
 *  find it again after a rename, which matching on name alone cannot. */
sourcePath?: string
```

Matching is then: `sourcePath` first, falling back to an exact name match for
workspaces made by hand. The fallback is what lets a scan merge into a `Collins`
workspace the user built themselves — and because the dialog states which rows
will merge and into what, that behaviour is visible before it happens rather
than surprising afterwards.

## Creating them

`createWorkspace(name)` cannot be reused. It **switches to** the new workspace
and clears the live tab strip, so calling it in a loop would leave the user
inside whichever folder came last, with their previous tabs mirrored away. A new
bulk action is needed:

```ts
/** Create or extend workspaces from a scan plan. Appends without switching:
 *  the active workspace and its live tab strip are untouched. */
applyWorkspacePlan(candidates: WorkspaceCandidate[]): { created: number; merged: number; repos: number }
```

For each candidate:

- **No existing workspace** — append a `Workspace` with `sourcePath` set and one
  `RepoTab` per path in `repoPaths`, `activeTabId` pointing at the first.
- **Existing workspace** — append a `RepoTab` per path in `newRepoPaths` to its
  existing `tabs`. Order, names, colours and `activeTabId` are left alone.

One `update()` call for the whole plan, not one per workspace, so the settings
file is written once.

Tabs are ordinary `RepoTab`s — `{ id, name, kind: 'repo', repos: [{ path, name }],
activeRepoPath: path }` — indistinguishable from ones opened by hand. Nothing
about a generated workspace is special afterwards; it can be renamed, reordered,
recoloured and deleted like any other.

## The dialog

A new modal kind, `scan-workspaces`. Its content is a checkbox list, one row per
candidate:

```
Create workspaces from ~/Code

  ☑ Arduino          50 repositories
  ☑ Collins          21 repositories        6 new — merges into "Collins"
  ☑ personal          6 repositories
  ☑ top-solution     13 repositories
  ☐ Code              2 repositories        loose at the root

         [ Cancel ]  [ Create workspaces ]
```

- Every row with new repositories starts ticked; the loose row starts unticked,
  since a folder of odds and ends is rarely a context worth switching to.
- A row whose repositories are **all** already present is shown, disabled, and
  labelled as such — silence about an unchanged folder reads as a bug.
- The confirm button is disabled when nothing is ticked.
- Cancelling creates nothing. The scan itself is not undone: the repositories
  stay indexed, which is what the button did before this change and is useful on
  its own.

Needs an entry in `scripts/docs-map.json` pointing at the handbook page, or
`npm run lint:docs` fails.

## Scope boundary

**Settings' "Scan now" is not changed.** It rescans every configured root at
once, where a per-root dialog makes no sense, and it is reached from a settings
panel rather than a management page. It keeps today's index-only behaviour.

Only **Add scan folder…** on the Repositories page gains the second phase, and
only for the single root just chosen.

## Testing

`planWorkspaces` in `pureLogic.test.ts`:

- Groups repositories by their first segment below the root
- Attributes a deeply nested repository to that top-level folder, not its parent
- Puts a repository directly under the root in the loose row, flagged `loose`
- Excludes folders containing no repositories
- Computes `newRepoPaths` against an existing workspace's tabs
- Matches an existing workspace by `sourcePath` after it has been renamed
- Falls back to a name match for a hand-made workspace with no `sourcePath`
- Returns nothing for a root with no repositories at all

`applyWorkspacePlan` touches the settings store rather than pure logic; it is
covered by asserting the produced tab shape for a small plan, including that the
active workspace and `activeTabId` are unchanged.

## What this does not do

- **No un-create.** Cancelling before confirming creates nothing, but a plan you
  confirmed and then regret is undone by deleting the workspaces by hand. An
  undo entry spanning several workspaces has no precedent in the store.
- **No removal, ever.** A repository deleted from disk leaves its tab in place,
  showing as missing. A rescan never prunes.
- **No nesting.** Folder structure below the first level is flattened into the
  workspace's tab strip. `GroupTab.folders` could carry it, but the chosen tab
  shape is one repository per tab, which has nowhere to put a folder.
- **No colours.** Generated workspaces get no colour, the same as hand-made
  ones.

## Cost and risks

The scan itself is unchanged, so the walk costs what it costs today. Creating
fifty tabs is a single settings write; the tabs are inert until their workspace
is activated, so there is no git work at creation time.

The real risk is a user pointing at `~` and ticking everything, producing a
dozen workspaces and hundreds of tabs. The dialog is the mitigation: counts are
shown per row before anything is created, and the default is per-row opt-in
rather than a single yes.

## Work, in order

1. `planWorkspaces` + its tests — the risky part, cheapest to verify
2. `Workspace.sourcePath` in `shared/types.ts`
3. `applyWorkspacePlan` in `stores/settings.ts`
4. The `scan-workspaces` modal and its strings in all sixteen locales
5. Wire `runAddScanRoot` to open it after the scan
6. `docs/help/repositories.md`, `scripts/docs-map.json`, README line
7. Screenshot of the dialog via `npm run screenshots`
