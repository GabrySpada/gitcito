---
title: Staging
category: Working with changes
order: 30
summary: Stage whole files, single hunks, or individual lines.
keywords: staging stage unstage discard hunk lines single line stage this line unstage line index partial add -p copy path relative
---

# Staging

The commit panel has three lists: **Conflicted**, **Unstaged** and **Staged**.
Each collapses, and each remembers whether you left it open.

![An unstaged diff, with the hunk and file controls beside it](../screenshots/line-staging.webp)

## Three levels of precision

| Level | How |
|---|---|
| **File** | Click the ✚ on the row, or select several rows and stage the lot |
| **Hunk** | Open the diff and use the button on the hunk header |
| **Line** | Hover a changed line in the diff and click its **+**, or select several lines and stage those |

Line staging is what makes it practical to keep a debug `console.log` out of a
commit without deleting it first.

## Staging single lines

Open an unstaged file's diff, in either the unified or the split view. Hover
any added or removed line and a small green **+** appears at its start: one
click stages that line and nothing else. The rest of the hunk stays unstaged,
exactly as if you had edited the hunk by hand in `git add -p`.

For more than one line at a time, click the lines themselves to select them —
<kbd>⇧</kbd>-click takes every changed line from the last one you clicked —
and press **Stage N line(s)** in the bar above the diff.

It works the other way round too. Open the **staged** version of a file and the
controls turn into a red **−**, **Unstage hunk** and **Unstage N line(s)**: they
take lines back out of the index and leave the working tree alone.

Take the last change off the side you are looking at and the diff follows the
file to the other side, instead of going blank.

Every line or hunk you stage or unstage this way can be taken back with the
toolbar's **Undo**, one click at a time.

| You pick | Staging it | Unstaging it |
|---|---|---|
| An added line | The index gains that line | The index loses that line |
| A removed line | The index loses that line | The line goes back into the index |
| Neither, in the same hunk | Stays as it is in the working tree only | Stays staged |

### Limits

- **Whitespace hidden, no staging.** While *Ignore whitespace* is on, the diff
  leaves out changes, so it cannot say which lines to stage; the controls hide
  until you turn it off.
- **Untracked files** stage whole. Stage the file first, then unstage the lines
  you do not want.
- **The last line of a file without a trailing newline** can be refused when
  you split its change from lines added after it — git cannot describe that
  half-state as a patch. Stage the two together.
- **Undo needs the index where you left it.** If you staged more of the same
  lines since, git refuses the undo rather than guessing, and says so.

## Discarding

Discard works at the same levels, and always asks. Untracked files are deleted;
tracked ones go back to their staged (or committed) state.

## Keyboard

<kbd>↑</kbd> <kbd>↓</kbd> (or <kbd>j</kbd> <kbd>k</kbd>) walk the file lists.
<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd> grows a selection from the last row you
clicked, <kbd>⇧</kbd>-click takes a range, and <kbd>⌘</kbd>/<kbd>Ctrl</kbd>-click
toggles individual files. Right-click the selection to stage, unstage, stash or
discard everything in it at once.

## Copying paths

Right-click an uncommitted file for **Copy File Path** (absolute, with the
platform's separators) and **Copy Relative File Path** (`src/index.ts`, no
leading `./`). Several selected files copy one path per line, in list order.
Deleted files stay enabled — those actions only copy text. Folders still copy
the folder path.

## Before you commit

Gitcito checks a few things and asks once, never silently:

- a file that looks like a **secret** (`.env`, `*.pem`, `id_rsa`…),
- a **very large** blob (threshold in Settings → Security),
- committing **straight to a protected branch** (`main`/`master` by default).

Each of those offers a one-click *Ignore & untrack*. See
[Security & secrets](security.md).

**See also:** [Committing](committing.md) · [Diffs](diffs.md) · [Absorb](absorb.md)
