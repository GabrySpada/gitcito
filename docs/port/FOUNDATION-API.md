# Foundation API (internal/gitx, gitsvc, paths, logbook, testutil)

Summary from the foundation port. The code is authoritative; read it.

## Conventions for gitService method porters
- Go method name = TS name with the first letter upper-cased, nothing else changed (`FileDataUrl`, not `FileDataURL`).
- Every exported method takes `ctx context.Context` first, then the TS args in order (repo path first = lock key).
- Classification is **already complete**: `classify.go` holds `readMethods` (112, verbatim from READ_METHODS),
  `appLevelMethods` (clone, init, remoteBranches) and `writeMethods` (the other 150). Do not edit it.
  `TestEveryGitServiceMethodIsClassified` fails on any exported method not in a table.
- Wrap each method body with the unexported helpers in gitsvc:
  - `locked[T](s, ctx, "tsName", repoPath, func(ctx) (T, error), args...)` / `lockedErr(...)`
  - `lockedNoRepo[T](s, ctx, "version", fn)` for methods without a repo argument.
  - Pass `args` only where they decide the logged activity event (`commit`: args[2]==true → amend;
    `previewRef`: args[3]=="merge"). Order inside: lock → retry on git lock-file errors → recover panics → log.
  - Error messages are sanitized with `StripAnsi(RedactCredentials(...))` by the helper.
- Events: `s.cloneProgress()` returns `func(CloneProgress)`; `s.bisectOutput()` returns `func(string)`.
  Token lookup for authenticated remotes: `s.token` (a `gitx.TokenFunc`) with `gitx.WithRemoteAuth`.
- Per-repo state from git.ts (mergedCache, statusInFlight, gitDirCache, rerereReplayed, bisectRuns) and
  method-specific helpers (snapshot refs, guardSnapshot, undoRootCommit, cherryPickTree, rewriteWithEdits,
  absorb, buildBisectStatus, resolveDrop, staleLocks lockKind, prefixFor, bundledTextconvPath) are **not**
  ported yet: the slice owning the methods that use them ports them (as fields on GitService guarded by a
  mutex, added in your own file via a small embedded struct, or package-level with a mutex, slice-prefixed).

## gitx
Constants: `SEP "\x1f"`, `REC "\x1e"`, `CreationFactor`, `ActivityDays`, `FileTooLargePrefix
"GITCITO_FILE_TOO_LARGE:"`, `PreviewMaxBytes`, `TextMaxBytes`, `DefaultMaxBuffer 1MiB`, `LockRetryAttempts`,
`LockRetryStep`, host names, `ErrNoSubtree`, `FilterTimeout`, `CleanEntryCap`, `CleanWalkCap`, `ErrMaxBuffer`,
`KnownHooks`.

Runners:
```go
type RunOpts struct { Env []string; PlainEnv bool; Stdin []byte; MaxBuffer int /*0=1MiB,<0 unlimited*/
  Timeout time.Duration; OnStdout, OnStderr func([]byte); AllowUnsafe []string /*Raw only*/ }
type Result struct{ Stdout, Stderr string; ExitCode int }
type ExecError struct{ Cmd string; Code int; Signal string; Killed bool; Stdout, Stderr string; Err error }
type GitError struct{ Message string; Exec *ExecError }
func Exec(ctx, dir, name string, args []string, opts RunOpts) (Result, error)   // execFile/spawn of any binary
func Run(ctx, repoPath string, args []string, opts RunOpts) (Result, error)     // pexecFile('git',['-C',repo,...])
func RunGit(ctx, repoPath string, args []string, opts RunOpts) (string, error)  // runGit()
func RunGitFallback(ctx, repoPath string, args []string, opts RunOpts, fallback string) (string, error)
func Raw(ctx, repoPath string, args []string, opts RunOpts) (string, error)     // simpleGit(repo).raw(args)
func WithStdin(ctx, repoPath string, args []string, input string) string        // gitWithStdin()
func ReadBlob(ctx, repoPath, spec string, opts RunOpts) ([]byte, error)
func ExitCode(err error) (int, bool)
func CheckUnsafe(args []string, allow ...string) error
func WithLockRetry(ctx, op func(context.Context) error) error
func IsGitLockError(err error) bool
```
`Raw` copies simple-git: cwd=repo, plain env, no cap; error only when exit≠0 **and** stderr non-empty
(so `config --get <missing>` → `''`); message = stdout+stderr untrimmed; simple-git's unsafe-arg guard first.
Exit-code-as-result sites use `Run` + `ExitCode(err)`.

Env/PATH: `NoPromptEnv()`, `MergeEnv(base, overrides...)`, `EnvValue(env, key)`, `FixPath(ctx)`.
JS string helpers: `TrimJS`, `TrimEndJS`, `IsJSSpace`, `UTF16Len`, `SliceUTF16`, `RedactCredentials`, `StripAnsi`.
Cache: `LRU[V]`, `NewLRU`, `Get/Set/Len/Bytes`, `Memo`, `StrBytes`.
Parsers: `CommitAuthor`, `ParseCoAuthors`, `MapSignature`, `MapStatusCode`, `StashSubject`/`ParseStashSubject`,
`FileMime`, `Track`/`ParseTrack`, `EncodeSubtreeKey`/`DecodeSubtreeKey`, `PrePurgeKey`, `BackupStamp`,
`ContiguousRuns`, `ParseRerereResolved`, `MergeOptions`/`MergeArgs`, `SplitLines`, `SplitTrimmedLines`,
`HostFromURL`, `AuthedCloneURL`.
Git helpers: `ResolveHooksDir`, `LocalBranches`, `GetRemoteURL`, `BranchUpstream`, `UpstreamRemote`,
`TokenFunc`, `WithRemoteAuth`, `Divergence`, `Identity`/`CommitIdentity`/`AuthorEnv`, `BlobSize`,
`AssertUnderCap`, `ReadFileDataURL`, `IsDirty`, `WithAutoStash`, `RunSubtree`, `CleanCandidates`.
Binaries/disk: `HasBinary`, `CanExecute`, `ConverterAvailable`, `CredentialHelperExists`, `CredentialCandidate`,
`CredentialCandidates`, `RunFilterCommand`, `DiskBytes`, `DirBytes`.

## gitsvc
`Emitter{Emit(name string, data any)}`, `ActivityRecorder`, `Option`, `WithActivityRecorder`, `WithTokenFunc`,
`GitService`, `New(emitter, log, opts...)`, `CloneProgress`, `EventCloneProgress`, `EventBisectOutput`,
`CallError`, `IsPanic`, `IsReadMethod`, `IsAppLevelMethod`, `RWLock`.

## logbook
`FileName "gitcito-log.json"`, `ActivityEvent` (18 values as the TS union), `LogEntry`, `New(dir)`,
`Record(event, repoPath, ok, errMsg)`, `Entries()` (log:get), `Clear()` (log:clear), `Flush()`, `Path()`.

## paths
`AppID`, `DevSuffix ".dev"`, `EnvDataDir GITCITO_DATA_DIR`, `EnvKeychainSuffix`, `IsDev()` (build tag `dev`),
`DataDir()`, `File(name)`, `EnsureDataDir()`, `KeychainService()`.

## testutil
`RepoRoot`, `MustRepoRoot`, `PlaygroundDir`, `Playground`, `PlaygroundRepo(t, name)`, `ReadManifest`,
`CloneFixture(t, name)` (copies, keeps index/stashes/merges/hooks; rerere off), `Git(t, dir, args...)`,
`IsolateGitEnv(t)`, `InitRepo(t)`, `WriteFile(t, dir, rel, content)`, `NoNulls(t, v)`, `NullPaths(v)`.
Playground regenerates only when MANIFEST.tsv is missing (or `PLAYGROUND_FORCE=1`, run with `-p 1`).
Staticcheck: `go run honnef.co/go/tools/cmd/staticcheck@2026.2.2`.
