---
title: The commit graph
category: Repository & history
order: 10
summary: Reading history: lanes, refs, columns, filters and multi-select.
keywords: graph history commits lanes branches merges columns filter linear first-parent amend undo reset github stash stashes order ordering placement spur date dividers divider separator relative dates today yesterday week month year timeline
---

# The commit graph

Branches, merges and octopus merges drawn properly, in light or dark. Rendering
is windowed, so a repository with a hundred thousand commits scrolls like one
with a hundred.

| | |
|---|---|
| ![Commit graph, light](../screenshots/graph-light.webp) | ![Commit graph, dark](../screenshots/graph-dark.webp) |

## Moving around

- <kbd>↑</kbd> <kbd>↓</kbd> (or <kbd>j</kbd> <kbd>k</kbd>) walk the selection.
- <kbd>⌘</kbd>/<kbd>Ctrl</kbd>-click toggles a commit into a **multi-selection**,
  starting from the commit already selected; <kbd>⇧</kbd>-click takes a range.
  With several selected, right-click one of them to cherry-pick them onto the
  current branch, export one combined patch, or copy their SHAs.
- **Squash** is always in that menu, but it runs only when the selection is the
  tip of the checked-out branch and the commits right below it, none skipped —
  it works by a soft reset to the oldest one's parent. Stash rows in between do
  not matter. Otherwise it is greyed out; hover it for the reason. The usual one
  is commits on a branch you have not checked out: check that branch out first.
  To squash commits further down, use [interactive rebase](rebase.md). Commit
  hooks do not run, just as in a rebase: the commits already exist, and a
  failing hook can no longer leave the branch half-reset.
- Commits that arrived in your **last fetch or pull** are flagged as new. Ones
  that have not joined the checked-out branch yet stay slightly translucent
  until a pull brings them in.
- Right-click a commit for **Amend**, **Undo**, **Reset to Commit…** and
  **View on GitHub**, plus checkout, cherry-pick, revert, branch, tag and
  copy. Unsafe actions stay visible and disable.

## Making it show what you want

- **Graph focus** decides how much history is drawn — Settings → Themes →
  **Graph**, or the gear menu in the graph header. *Everything* draws it all;
  *Linear history* (first-parent) leaves only the trunk; *Hide merged branches*
  keeps the trunk plus the branches that are still unmerged; *Solo* keeps your
  branch, your starred branches and the default branch.

  It filters only what the log has already loaded. *Hide merged branches* trusts
  git's own "already contained in the current branch" answer, so checking out a
  different branch changes what it hides — and it keeps every commit a tag or an
  unrecognised ref still points at, which is exactly what a deleted branch leaves
  behind. *Linear history* and *Solo* are blunter: a tag or a stash sitting on a
  commit they drop goes with it.

- **Filter by path**: right-click a file or folder → *Filter graph by this
  path*, and only the commits that touched it stay lit.

![Graph filtered down to one path](../screenshots/graph-path-filter.webp)

- **Columns**: show, hide, resize and reorder branch, message, author, date,
  SHA, signature and deployment columns.
- **Style**: Settings → Themes → **Graph** — lane palette (9 built-ins, custom,
  or AI-generated), corner style, row density and line thickness, with a live
  mini-graph preview.

![Graph style settings with live preview](../screenshots/settings-graph.webp)

## Date dividers

Scrolling a long history, the question is rarely "what is this commit's exact
timestamp" — the date column already answers that. It is "roughly where am I".
Date dividers answer it: a hairline across the graph with a relative label on
the right, marking where one span of history ends and an older one begins.

The spans coarsen as you go back — today, yesterday, a few days, a week, a
couple of weeks, months, years. That coarsening is the whole point. A rule at
every calendar-day change would put one under almost every row in an active
repository and under nothing at all in a quiet one; widening the spans with
distance keeps a divider meaning "you have travelled a noticeable distance"
wherever you are in the log.

![Date dividers marking spans of history in the graph](../screenshots/graph-date-dividers.webp)

A divider sits at the **bottom** of the last row of a span and is labelled with
the span it closes, so it describes the rows above it. The newest span carries
no rule above it, and the oldest row on screen carries none below it — its span
may well continue into commits that have not been loaded yet, and a rule there
would jump as soon as *Load more* brought them in.

What they will not do:

- **They follow the graph's order, not the calendar.** Commits are listed in
  `--date-order`, which shows a merge above the commits it merges. A branch
  merged long after it was written therefore puts older commits above newer
  ones. Rather than ladder back and forth, a row dated newer than the span it
  lands in simply joins that span — dividers only ever run newer-to-older down
  the graph.
- **They ignore rows whose date is not their place in history**: the
  uncommitted-changes row, and stashes, which sit beside the commit they were
  taken from rather than in date order (see below). Neither carries a divider
  nor breaks the span it sits in.
- **They are relative to now, not to a fixed point.** Leave the app open across
  midnight and a span labelled "today" is not relabelled until the next refresh
  of the reference clock, which happens every five minutes.
- **There is no setting.** Dividers are always on.

## Where stashes sit

A stash is drawn as its own row, hanging off the commit it was taken from on a
dashed spur so it never displaces the trunk. It is placed in the row **directly
above that parent commit**, not in the slot its own timestamp would earn it.

Its marker is an archive box in a dotted frame — the same archive symbol the
stash list, command palette and details header use, so the graph names a stash
the way the rest of the app does. The dotted frame is what separates it from a
commit: sitting one row above its parent, shape has to carry "this is not part
of the branch".

A stash is nearly always newer than the commit it sits on, so ordering by date
would float it up among unrelated commits and stretch its tether across half the
graph. The parent is the only row a stash actually relates to, so that is the one
it stays next to.

The limit: a stash whose parent commit is not in the loaded window — pruned, or
scrolled past the end of the log — has nothing to anchor to, and falls back to
date order until its parent loads. The *Linear history* and *Solo* focus modes
drop a stash whose parent they drop, as noted above.

## Commit details

Selecting a commit shows its changed files (tree or flat), author, SHA,
co-authors, and its signature. `#123` references and `@mentions` are autolinked
to your host.

Above the file list, a **change summary** breaks the commit down by kind rather
than giving a single total — *5 modified*, *1 added*, *1 deleted*, plus
*renamed* and *conflicted* when a commit has them. Each is coloured to match the
status glyph on the rows below and the count badges on collapsed folders, so the
same colour means the same thing everywhere in the panel. Kinds with no files
are left out entirely, so an ordinary edit shows one entry rather than five.
Hover the summary for the plain "*n* changed files" total.

![Change summary above a commit's file list: 6 modified, 2 added, 1 deleted, 1 renamed](../screenshots/change-summary.webp)

Two things it deliberately does not tell you. It counts **files, not lines** — a
one-character fix and a rewrite both read as *1 modified*; the diff itself is
where churn is visible. And it counts every file in the commit, not the subset
matching an active filter or search.

The same summary heads the staging panel and the stash file list. In the staging
panel, untracked files are counted as additions, so *added* means "not in the
last commit" rather than "already staged".

The file list multi-selects with the usual gestures
(<kbd>⌘</kbd>/<kbd>Ctrl</kbd>-click, <kbd>⇧</kbd>-click,
<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd>). Right-click the selection → *Restore
{n} files to working tree* takes those files exactly as this commit had them:
after one confirmation it overwrites the working copies, and touches neither
HEAD nor the index.

![Walking through commit details](../screenshots/clip-commit-details.webp)

**See also:** [Blame & file history](blame.md) · [Search](search.md) · [Time machine](time-machine.md) · [Author avatars](avatars.md)
