// Package version is the single source of the app version. wails.yaml reads it
// through project.version_file/version_symbol; package.json mirrors it and a
// test keeps the two in step.
package version

// Version is the app version shown in About and compared by the updater.
const Version = "5.0.0"

// Commit is injected at build time (wails.yaml commit_symbol).
var Commit = ""

// UpdatesDisabled gates every network path of the updater and the release-notes
// feed. This tree is a fork built from source: the stock updater would poll the
// upstream feed and offer binaries this fork never produced, which is exactly
// the control building from a fork is meant to keep. Mirrors UPDATES_DISABLED in
// the Electron build. Flip only after repointing the feed at this fork.
const UpdatesDisabled = true

// ReleaseRepo is the GitHub owner/repo of the release feed.
const ReleaseRepo = "MyAppDesk/gitcito"
