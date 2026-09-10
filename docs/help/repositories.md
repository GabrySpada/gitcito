---
title: Repositories
category: Sync & many repos
order: 52
summary: Every repository Gitcito knows about, open or not, in one searchable list.
keywords: repositories registry all repos favourites starred recent scan folder browse find open manage repository management
---

# Repositories

[Mission control](mission-control.md) answers "which of my open repositories
needs me?" It only knows about the active workspace's tabs. Repositories
answers a different question: **where is that repo, and is it even open
anywhere?** It covers everything Gitcito has ever seen — every workspace, every
tab, plus whatever it finds by scanning folders you point it at.

## The sections

A repository can appear in **more than one section** — deliberately, so each
section is a complete answer to its own question rather than a partial slice
of a single list.

| Section | What is in it |
|---|---|
| Open repositories | Every tab in the active workspace right now |
| Favourites | Starred repositories, across every workspace |
| Recent | Everything you have opened, newest first — **uncapped**, unlike the 8-entry recent list in the launcher |
| One per saved workspace | That workspace's tabs, so you can jump into a different workspace without switching to it first |
| All repositories | Every repository the registry knows about, open or not |

Search filters rows across every section at once; collapse a section you don't
care about right now, or **Expand all** / **Collapse all**.

## What makes a repository known

A row exists here once Gitcito has **opened it** at some point, or found it
under a **scan folder**. Nothing is indexed just because it exists on disk
somewhere you never told Gitcito about.

Scan folders are configured in Settings:

- **Depth** is how many directory levels the scan descends below the root
  (default 3, capped at 10).
- Scanning **stops at a repository** — a vendored checkout or a submodule
  inside a repo is not indexed as its own row.
- It never enters dot-directories, and skips `node_modules` and similar
  dependency folders.
- It **reads folder names only**: finding a `.git` directory is what makes
  something a repository here. Name, owner and branch come from files inside
  `.git` (`HEAD`, the config), never by running `git`.

## Rows

Each row shows a star toggle, the repository's name (alias-aware, if you have
renamed it), its owner (parsed from the origin remote's URL), and a branch
chip. Right-click for the same [repository context menu](repo-menu.md) as
everywhere else, extended with two entries specific to this page:

| Action | What it does |
|---|---|
| Star / unstar | Adds or removes the repository from Favourites |
| Locate… | Re-points a moved or renamed folder — the repo's alias, profile binding and star all carry over. If the destination already had its own settings, **the destination wins** |
| Forget | Removes the entry from this list. **Never touches the folder on disk** |

A repository whose folder no longer exists shows as **missing**, with inline
**Locate…** and **Forget** instead of the usual row actions.

## The action bar

**Open folder…**, **Clone…**, and **Add scan folder…** — three ways to bring a
repository into Gitcito's registry, from the same page you use to find one
that is already there.

## WIP summary

An opt-in checkbox. On, each **expanded** row runs a real `git status` and
shows uncommitted work and sync state — off, rows cost nothing beyond reading
files inside `.git`.

It is opt-in on purpose: a summary costs roughly five git processes per
repository, batched eight at a time so a large registry does not stall the UI.
Turning it on is a deliberate "check everything I can currently see," not a
standing cost.

## Limits

- **Nothing on this page refreshes on a timer.** Reopen the page, or toggle
  WIP summary off and on, to see current state.
- **WIP summary only covers expanded sections.** A collapsed section shows no
  status at all, checked or not.
- **A repository is only known once you have opened it, or scanned a folder
  that contains it.** There is no way to search the filesystem from here.
- **Forget removes the entry from the list — never from disk.** If the folder
  is still there, scanning the same root (or opening it again) brings it right
  back.

**See also:** [Mission control](mission-control.md) · [Workspaces, tabs & groups](workspaces.md)
