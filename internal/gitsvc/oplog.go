package gitsvc

import "github.com/GabrySpada/gitcito/internal/logbook"

// eventForMethod maps a git method to the activity event it records on
// success. `commit` and `previewRef` are special-cased in eventForCall.
var eventForMethod = map[string]logbook.ActivityEvent{
	"push":                 logbook.EventPush,
	"pushToRemotes":        logbook.EventPush,
	"pushAllTags":          logbook.EventPush,
	"pull":                 logbook.EventPull,
	"fetchAll":             logbook.EventFetch,
	"fetchRemote":          logbook.EventFetch,
	"amendCommitMessage":   logbook.EventAmend,
	"createBranch":         logbook.EventBranchCreate,
	"gitflowStart":         logbook.EventBranchCreate,
	"gitflowFinish":        logbook.EventMerge,
	"deleteBranch":         logbook.EventBranchDelete,
	"deleteRemoteBranch":   logbook.EventBranchDelete,
	"merge":                logbook.EventMerge,
	"mergeInto":            logbook.EventMerge,
	"rebase":               logbook.EventRebase,
	"runInteractiveRebase": logbook.EventRebase,
	"stash":                logbook.EventStash,
	"stashPop":             logbook.EventStashPop,
	"resolveConflict":      logbook.EventConflictResolved,
	"conflictTakeSide":     logbook.EventConflictResolved,
	"createTag":            logbook.EventTagCreate,
	"cherryPick":           logbook.EventCherryPick,
	"revertCommit":         logbook.EventRevert,
	"open":                 logbook.EventRepoOpen,
	"clone":                logbook.EventClone,
	"init":                 logbook.EventInit,
	"initHere":             logbook.EventInit,
}

// eventForCall picks the activity event for one call; "" means the call is
// not logged. args are the TS positional arguments (args[0] = repo path).
func eventForCall(method string, args []any) logbook.ActivityEvent {
	switch method {
	case "commit":
		// commit(repoPath, message, amend): strictly `=== true`.
		if b, ok := argAt(args, 2).(bool); ok && b {
			return logbook.EventAmend
		}
		return logbook.EventCommit
	case "previewRef":
		// A preview either merges into HEAD or just parks a fetched ref on a branch.
		if s, ok := argAt(args, 3).(string); ok && s == "merge" {
			return logbook.EventMerge
		}
		return logbook.EventFetch
	}
	return eventForMethod[method]
}

func argAt(args []any, i int) any {
	if i < len(args) {
		return args[i]
	}
	return nil
}

// recordSuccess counts the event in analytics and appends an ok log entry.
// Unlogged methods (event "") record nothing.
func (s *GitService) recordSuccess(event logbook.ActivityEvent, repoPath string) {
	if event == "" {
		return
	}
	if s.activity != nil {
		s.activity.RecordEvent(event)
	}
	if s.log != nil {
		s.log.Record(event, repoPath, true, "")
	}
}

// recordFailure appends a failed log entry with the sanitized message.
// Failures are not counted as activity.
func (s *GitService) recordFailure(event logbook.ActivityEvent, repoPath, message string) {
	if event == "" || s.log == nil {
		return
	}
	s.log.Record(event, repoPath, false, message)
}
