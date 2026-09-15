---
title: Getting started
category: Start here
order: 1
summary: Open a repository, read the graph, make your first commit.
keywords: intro first steps open clone tabs graph commit
---

# Getting started

Gitcito opens a folder and shows you its history. Nothing is written to your
repository until you ask for it.

![A freshly opened repository with no commits yet](../screenshots/empty-repo.webp)

## Open a repository

- **Drag a folder** onto the window, or use **Open repository** on the welcome
  screen.
- **Clone** one from a URL or straight from your host — see [cloning](cloning.md)
  for the options that make a huge repository quick to clone.
- From a terminal, `gitcito .` opens the current folder in the running app —
  see [the command line](cli.md).
- A folder that is not a Git repository yet still opens, offering to
  initialise it.

## The three panes

| Pane | What it holds |
|---|---|
| Left | Branches, remotes, tags, stashes, worktrees — and the **Files** tab for the working tree |
| Middle | The commit graph, and whatever you select from it |
| Right | The commit composer, or the details of the selected commit |

## Finding everything else

Two routes, and they lead to the same places:

- **`⌘K`** (`Ctrl+K`) — the command palette. Type what you want; it also jumps to
  branches, commits and files.
- **Tools** in the toolbar — the same repository-scoped set as a menu, with the
  long tail folded into groups so it stays readable.

![The Tools menu: the frequent tools first, the rest grouped](../screenshots/tools-menu.webp)

The action bar keeps its buttons on the middle of the **window**, not in the gap
between the repository name and the search box — so they stay under the same
pixel as you move between repositories whose names are nothing like the same
length. Repository and branch names show in full; one long enough to threaten
the rest of the bar ellipsises, with the whole of it in the button's tooltip.

Room is measured outwards from that midpoint, and that is what holding it costs:
when the window gets narrow, or the repository name is very long, the bar gives
ground rather than drifting. The search field trades its box for a magnifier
first — click it to search, and it stays open for as long as a filter is in
effect. Then the buttons that no longer fit fold into a **More** dropdown at the
bar's end, in bar order and with their own submenus intact. Widen the window and
they come back out.

![The action bar on a narrow window: the search collapsed to a magnifier, the tail of the bar folded into More, and the buttons still centred](../screenshots/toolbar-narrow.webp)

Anything reachable by one is reachable by the other, so there is nothing that
only power users can find.

## Your first commit

1. Edit a file. It appears under **Unstaged**.
2. Stage it — the whole file, a hunk, or [single lines](staging.md).
3. Write a message and press **Commit**.

Everything else in Gitcito is optional.

