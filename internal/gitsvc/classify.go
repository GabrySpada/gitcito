package gitsvc

import (
	"reflect"
	"sort"
	"unicode"
)

// Method classification for the per-repo lock, copied from git.ts.
//
// Names are the TypeScript gitService method names (camelCase). A GitService
// Go method is named the same with the first letter upper-cased and nothing
// else changed — `fileDataUrl` is `FileDataUrl`, not `FileDataURL` — so the
// tables, the op log and the renderer bridge all agree on one name.
// tsMethodName converts back.

// appLevelMethods take no repository path as their first argument: they run
// before a repo exists locally, or against a remote URL. No lock, no
// repo-scoped log entry.
var appLevelMethods = map[string]struct{}{
	"clone":          {},
	"init":           {},
	"remoteBranches": {},
}

// readMethods are pure reads — safe to run concurrently with each other.
// Anything NOT listed is treated as a write (exclusive), so an unmapped/new
// method serializes rather than risking a concurrent mutation. Only add a
// method here if it never touches the index, working tree, refs, config, or
// hook files.
var readMethods = map[string]struct{}{
	"open":                {},
	"lastFetchAt":         {},
	"upstreamSuggestion":  {},
	"staleLocks":          {},
	"remoteHasBranches":   {},
	"log":                 {},
	"branches":            {},
	"status":              {},
	"stashes":             {},
	"remotes":             {},
	"stackInfo":           {},
	"stackLeaves":         {},
	"listDir":             {},
	"listDirAt":           {},
	"timelapseData":       {},
	"repoPulse":           {},
	"repoDetail":          {},
	"listFiles":           {},
	"listTrackedFiles":    {},
	"ignoredTrackedFiles": {},
	"commitSummary":       {},
	"filesToPush":         {},
	"commitsTouchingPath": {},
	"protectedBranches":   {},
	"repoConfig":          {},
	"repoDoctor":          {},
	"suggestRepoConfig":   {},
	"fileSizes":           {},
	"treeStatus":          {},
	"getCommitMessage":    {},
	"commitAuthor":        {},
	"resolveRev":          {},
	"commitTemplate":      {},
	"reflog":              {},
	"bisectStatus":        {},
	"getRemoteTags":       {},
	"remoteBranches":      {},
	"gitflowStatus":       {},
	"rerereStatus":        {},
	"subtrees":            {},
	"cleanPreview":        {},
	// Writing a bundle or an archive reads the object database and touches
	// nothing in the repository — the file it produces lives outside it.
	"bundleCreate":           {},
	"bundleInspect":          {},
	"archiveCreate":          {},
	"maintenanceStats":       {},
	"fsck":                   {},
	"objectRefs":             {},
	"gitObject":              {},
	"attributeFiles":         {},
	"checkAttributes":        {},
	"credentialStatus":       {},
	"replacements":           {},
	"diffDrivers":            {},
	"diffDriverSuggestions":  {},
	"filterDrivers":          {},
	"filterDryRun":           {},
	"conflictCommits":        {},
	"note":                   {},
	"notedCommits":           {},
	"historyPaths":           {},
	"historyPurgePreview":    {},
	"historyPurgeBackups":    {},
	"diffFile":               {},
	"commitFiles":            {},
	"stashFiles":             {},
	"stashFileDiff":          {},
	"commitFileDiff":         {},
	"formatPatch":            {},
	"stagedDiff":             {},
	"commitDiff":             {},
	"commitBranches":         {},
	"commitTags":             {},
	"fileContent":            {},
	"searchFileContents":     {},
	"searchFileMatches":      {},
	"searchCommitMatches":    {},
	"grepWorkingTree":        {},
	"searchHistory":          {},
	"fileDataUrl":            {},
	"imageDiff":              {},
	"blameFile":              {},
	"fileHistory":            {},
	"worktrees":              {},
	"submodules":             {},
	"signingConfig":          {},
	"hooksInfo":              {},
	"readHook":               {},
	"lfsInfo":                {},
	"sparseCheckoutInfo":     {},
	"getUser":                {},
	"mergeState":             {},
	"mergeMessage":           {},
	"conflictContext":        {},
	"conflictVersions":       {},
	"interactiveRebaseSteps": {},
	"compareBranches":        {},
	"mergePreview":           {},
	"teammateRadar":          {},
	"commitEditInfo":         {},
	"commitMenuProbe":        {},
	"blobAtCommit":           {},
	"semanticDiff":           {},
	"rangeDiff":              {},
	"refTips":                {},
	"absorbPlan":             {},
	"repoStats":              {},
	"repoInsights":           {},
	"cosmosData":             {},
	"generateChangelog":      {},
	"listSnapshots":          {},
	"contributors":           {},
	"version":                {},
}

// writeMethods are the methods that take the exclusive lock. TS had no such
// table — "not a read" meant "write" — and the dispatcher here keeps that rule
// (see isReadMethod). The table exists so a test can insist that every
// exported GitService method was classified on purpose: when you port a
// method, it must already be in readMethods, appLevelMethods or here.
//
// Seeded with every gitService method in git.ts that READ_METHODS and
// APP_LEVEL_METHODS do not list.
var writeMethods = map[string]struct{}{
	"absorbApply":             {},
	"addRemote":               {},
	"addToGitignore":          {},
	"addToGitignoreAt":        {},
	"amendCommitMessage":      {},
	"applyDoctorFix":          {},
	"applyPatch":              {},
	"applyRepoFileActions":    {},
	"attributeWrite":          {},
	"autosquash":              {},
	"bisectCancel":            {},
	"bisectMark":              {},
	"bisectReset":             {},
	"bisectRunScript":         {},
	"bisectStart":             {},
	"bundleFetch":             {},
	"checkout":                {},
	"checkoutRemote":          {},
	"cherryPick":              {},
	"cherryPickMany":          {},
	"clean":                   {},
	"clearLocks":              {},
	"commit":                  {},
	"commitEditApply":         {},
	"commitEditPreview":       {},
	"commitFixup":             {},
	"conflictOpAbort":         {},
	"conflictOpContinue":      {},
	"conflictTakeSide":        {},
	"createBranch":            {},
	"createSnapshot":          {},
	"createTag":               {},
	"deleteBranch":            {},
	"deleteHook":              {},
	"deleteRemoteBranch":      {},
	"deleteRemoteTag":         {},
	"deleteSnapshot":          {},
	"deleteTag":               {},
	"discard":                 {},
	"editRemote":              {},
	"fetchAll":                {},
	"fetchNotes":              {},
	"fetchRemote":             {},
	"forgetCredential":        {},
	"fsCreate":                {},
	"fsDelete":                {},
	"fsExisting":              {},
	"fsImport":                {},
	"fsMove":                  {},
	"fsRename":                {},
	"gitflowFinish":           {},
	"gitflowInit":             {},
	"gitflowStart":            {},
	"gitflowUndo":             {},
	"historyPurge":            {},
	"historyPurgeDropBackup":  {},
	"historyPurgeRestore":     {},
	"initHere":                {},
	"lfsPrune":                {},
	"lfsPull":                 {},
	"lfsTrack":                {},
	"lfsUntrack":              {},
	"maintenanceRun":          {},
	"maintenanceSchedule":     {},
	"merge":                   {},
	"mergeInto":               {},
	"previewRef":              {},
	"pull":                    {},
	"pullBranch":              {},
	"push":                    {},
	"pushAllTags":             {},
	"pushNotes":               {},
	"pushTag":                 {},
	"pushToRemotes":           {},
	"rebase":                  {},
	"rebaseAbort":             {},
	"rebaseOnto":              {},
	"removeNote":              {},
	"removeRemote":            {},
	"renameBranch":            {},
	"renameBranchRemote":      {},
	"renameStash":             {},
	"replaceDelete":           {},
	"replaceGraft":            {},
	"replaceObject":           {},
	"rerereClear":             {},
	"rerereForget":            {},
	"reset":                   {},
	"resolveConflict":         {},
	"resolveDivergedCheckout": {},
	"resolvePrRef":            {},
	"restoreFromCommit":       {},
	"restoreSnapshot":         {},
	"restoreUndoneCommit":     {},
	"revertCommit":            {},
	"runInteractiveRebase":    {},
	"setBranchRef":            {},
	"setCredentialHelper":     {},
	"setDiffDriver":           {},
	"setFilterDriver":         {},
	"setHookEnabled":          {},
	"setNote":                 {},
	"setProtectedBranches":    {},
	"setRepoConfig":           {},
	"setRerere":               {},
	"setSigningConfig":        {},
	"setUpstream":             {},
	"setUseReplaceRefs":       {},
	"setUser":                 {},
	"sparseCheckoutDisable":   {},
	"sparseCheckoutSet":       {},
	"squashCommits":           {},
	"stackClearParent":        {},
	"stackInsert":             {},
	"stackPruneMerged":        {},
	"stackReorder":            {},
	"stackRestack":            {},
	"stackSetParent":          {},
	"stackSetRoute":           {},
	"stage":                   {},
	"stageAll":                {},
	"stagePatch":              {},
	"stash":                   {},
	"stashApply":              {},
	"stashApplyFiles":         {},
	"stashApplyOverwrite":     {},
	"stashBaseShas":           {},
	"stashDrop":               {},
	"stashPop":                {},
	"stashPush":               {},
	"stashToBranch":           {},
	"submoduleAdd":            {},
	"submoduleDeinit":         {},
	"submoduleRemove":         {},
	"submoduleSetUrl":         {},
	"submoduleSync":           {},
	"submoduleUpdate":         {},
	"subtreeAdd":              {},
	"subtreeForget":           {},
	"subtreePull":             {},
	"subtreePush":             {},
	"subtreeSplit":            {},
	"undoCommit":              {},
	"unstage":                 {},
	"unstageAll":              {},
	"untrack":                 {},
	"worktreeAdd":             {},
	"worktreeRemove":          {},
	"writeChangelogFile":      {},
	"writeHook":               {},
}

// wailsInternalMethods are exported service methods Wails never binds
// (application.internalServiceMethods): lifecycle hooks, not git methods.
var wailsInternalMethods = map[string]struct{}{
	"ServiceName":     {},
	"ServiceStartup":  {},
	"ServiceShutdown": {},
	"ServeHTTP":       {},
}

// IsReadMethod reports whether a git method may share the repo lock with
// other reads — gitMethodIsRead() in git.ts.
func IsReadMethod(method string) bool {
	_, ok := readMethods[method]
	return ok
}

// IsAppLevelMethod reports whether a git method runs without a repository
// (and so without a lock).
func IsAppLevelMethod(method string) bool {
	_, ok := appLevelMethods[method]
	return ok
}

// isClassified reports whether a TS method name appears in any table.
func isClassified(method string) bool {
	_, r := readMethods[method]
	_, a := appLevelMethods[method]
	_, w := writeMethods[method]
	return r || a || w
}

// tsMethodName maps a Go method name to the TS one: first letter lower-cased.
func tsMethodName(goName string) string {
	if goName == "" {
		return ""
	}
	r := []rune(goName)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// unclassifiedMethods lists the exported methods of t (a *GitService type, or
// anything else in tests) that no table classifies, sorted. Wails-internal
// lifecycle methods are skipped: they are never bound.
func unclassifiedMethods(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumMethod(); i++ {
		name := t.Method(i).Name
		if _, internal := wailsInternalMethods[name]; internal {
			continue
		}
		if !isClassified(tsMethodName(name)) {
			out = append(out, name+" ("+tsMethodName(name)+")")
		}
	}
	sort.Strings(out)
	return out
}

// overlappingMethods lists names present in both the read and the write table,
// which would make the classification ambiguous.
func overlappingMethods() []string {
	var out []string
	for m := range writeMethods {
		if _, ok := readMethods[m]; ok {
			out = append(out, m)
		}
		if _, ok := appLevelMethods[m]; ok {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}
