package gitx

import (
	"context"
	"regexp"
	"time"
)

var credentialRe = regexp.MustCompile(`(://)[^/` + jsSpaceClass + `@]+(?::[^/` + jsSpaceClass + `@]*)?@`)

// RedactCredentials strips `user:secret@` out of every URL in a message.
//
// Git echoes the remote URL back in its errors, and WithRemoteAuth puts the
// active PAT into that URL for the duration of a network call — so without
// this an unlucky failure prints the token into the UI, the operation log, and
// any screenshot of either. Idempotent: redacting twice changes nothing.
func RedactCredentials(msg string) string {
	return credentialRe.ReplaceAllString(msg, "${1}***@")
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-_]")

// StripAnsi drops terminal escape sequences from a message. Hooks and the
// tools they run (lint-staged, vitest, …) colour their output even when git
// captures it, and the renderer shows that stderr as plain text — every colour
// code turns into `[31m` litter in the toast.
func StripAnsi(msg string) string {
	return ansiRe.ReplaceAllString(msg, "")
}

var lockErrRe = regexp.MustCompile(`(?i)\.lock['"]?:[` + jsSpaceClass + `]*File exists`)

// IsGitLockError is true for git's "Unable to create '...lock': File exists" —
// the index lock, but equally `packed-refs.lock` or a single
// `refs/remotes/<remote>/<branch>.lock` during a prune. Thrown when another
// process (this app's own queue is already serialized, but an IDE's git
// integration, a terminal, or another Git GUI open on the same repo) held the
// lock for the instant this command tried to start. A live lock is almost
// always released within milliseconds, so a short retry clears it without
// bothering the user — only a genuinely stuck/crashed lock survives all
// retries, and that one the user is offered a repair for.
func IsGitLockError(err error) bool {
	return err != nil && lockErrRe.MatchString(err.Error())
}

// LockRetryAttempts and LockRetryStep are withLockRetry's budget: up to five
// tries, sleeping 150ms × attempt between them.
const (
	LockRetryAttempts = 5
	LockRetryStep     = 150 * time.Millisecond
)

// WithLockRetry runs op, retrying while it fails with a git lock error —
// withLockRetry() in git.ts. Any other error, the last attempt's error, or a
// cancelled context ends it.
func WithLockRetry(ctx context.Context, op func(context.Context) error) error {
	return withLockRetry(ctx, op, LockRetryStep)
}

func withLockRetry(ctx context.Context, op func(context.Context) error, step time.Duration) error {
	for attempt := 1; ; attempt++ {
		err := op(ctx)
		if err == nil || attempt >= LockRetryAttempts || !IsGitLockError(err) {
			return err
		}
		t := time.NewTimer(step * time.Duration(attempt))
		select {
		case <-ctx.Done():
			t.Stop()
			return err
		case <-t.C:
		}
	}
}
