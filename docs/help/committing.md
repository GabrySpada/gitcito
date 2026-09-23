---
title: Committing
category: Working with changes
order: 31
summary: Message styles, templates, co-authors and the linter.
keywords: commit message composer conventional gitmoji ticket amend template co-author linter undo reset author reset-author wrong author
---

# Committing

## Message styles

Pick one in Settings; the composer adapts to it.

| Style | Looks like |
|---|---|
| **Conventional** | `feat(api)!: add rate limiting` — with a type dropdown |
| **Gitmoji** | `✨ add rate limiting` — with an emoji picker |
| **Ticket** | `ABC-123: add rate limiting` — seeded from the branch name |
| **Plain** · **Auto** | Whatever you type; Auto lets the AI decide the shape |
| **Caveman** · **Haiku** | Exactly what they sound like |

![Composer prefilled from a commit template](../screenshots/commit-template.webp)

## Things the composer does for you

- <kbd>↑</kbd> <kbd>↓</kbd> recalls your **recent messages**.
- A **co-author picker** adds `Co-authored-by:` trailers from the repository's
  own contributors.
- `commit.template` / `.gitmessage` **prefills** the message, comment lines
  stripped.
- During a merge, cherry-pick or revert, the message is **pre-filled** the way
  git would.
- Drafts **persist** per repository, so switching tabs never loses a message.

## The linter

A live, non-blocking check: subject length (with a character counter), a
trailing period, a non-imperative or lowercase subject, over-wide body lines.
Hints, never a gate — it will not stop you committing.

## Amend

Amend rewrites the last commit with whatever is staged. Gitcito shows you the
existing message first so you are editing, not retyping.

**Amend Commit…** on a graph row does the same thing for HEAD: it loads the
full message, switches the composer into amend mode, and focuses it. A HEAD
that was already pushed can still be amended, but Gitcito warns that updating
the remote will take a force push.

### Amending someone else's commit

An amend keeps the original commit's **author and author date** — git's rule,
not Gitcito's. That is right when you fix a typo in a colleague's commit, and
wrong when you fold your own new work into it: the result is credited to them,
and the graph shows their name on code they never wrote.

So when HEAD's author email differs from your `user.email`, the composer says
whose commit you are amending and offers **Make me the author**. Ticking it
amends with `--reset-author`: you become the author, dated now. Leave it off to
keep their name.

![Amend mode on a commit written by someone else](../screenshots/amend-author.webp)

It compares emails, so one person committing as `Elisa` and `elisa` counts as
the same author. It falls back to the name only when an email is missing, and
says nothing if the repository has no identity configured at all. It checks
only the commit being amended. A cherry-pick or rebase that later carries the
commit elsewhere keeps whatever author it has by then.

⌘Z after an amend puts the amended commit back. Your newly staged changes stay
staged, just as they were before you amended. It does not step back to that
commit's parent.

**Undo Commit…** is the sibling for an unpushed HEAD: mixed reset to the
parent, working-tree changes kept, message restored to the composer. The
initial commit has a dedicated path that leaves an unborn branch instead of
destroying the files.

**See also:** [Staging](staging.md) · [Absorb](absorb.md) · [Changelog generator](changelog.md)
