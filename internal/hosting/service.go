package hosting

// Service serves the renderer's hosting channels:
//   hosting:listRepos, hosting:listOwners, hosting:whoAmI, hosting:createRepo,
//   hosting:listPRs, hosting:listReleases, hosting:ciStatuses,
//   hosting:openCreatePR, hosting:createPR, hosting:prDetail, hosting:prComment,
//   hosting:prReplyReviewComment, hosting:prFiles, hosting:prChecks,
//   hosting:prReview, hosting:prMerge, hosting:mergedPrHeads,
//   hosting:ensureStack, hosting:updatePR, hosting:listNotifications,
//   hosting:markNotificationRead, hosting:markAllNotificationsRead,
//   hosting:listIssues, hosting:listMilestones, hosting:milestoneIssues,
//   hosting:uploadSshKey, hosting:issueDetail, hosting:setIssueState,
//   hosting:createIssue, hosting:applyPrMeta
// It mirrors registerHostingHandlers in src/main/hosting.ts. Every exported
// method is callable from the page, so nothing else is exported here; the
// work happens on the unexported client.

import (
	"context"
	"encoding/json"
	"fmt"
)

// Service is the bound facade over the hosting client.
type Service struct {
	c *client
}

// NewService builds the facade. main.go passes OpenURL (the browser opener);
// everything else defaults to production behaviour.
func NewService(opts Options) *Service {
	return &Service{c: newClient(opts)}
}

// recoverErr turns a panic into an error: nothing may panic across the bridge.
func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("hosting: internal error: %v", r)
	}
}

// ListRepos lists the repositories the account behind token (or git's
// credential for the provider) can clone. org is the Azure DevOps organization.
// channel: hosting:listRepos
func (s *Service) ListRepos(ctx context.Context, provider, token, org string) (out []RemoteRepo, err error) {
	defer recoverErr(&err)
	return s.c.listRepositories(ctx, provider, token, org)
}

// ListOwners lists the accounts a new repository can be created under.
// channel: hosting:listOwners
func (s *Service) ListOwners(ctx context.Context, provider, token, org string) (out []RemoteOwner, err error) {
	defer recoverErr(&err)
	return s.c.listOwners(ctx, provider, token, org)
}

// WhoAmI resolves the account behind a token for Settings → Integrations.
// interactive lets git's credential helper open its sign-in window.
// channel: hosting:whoAmI
func (s *Service) WhoAmI(ctx context.Context, provider, token, org string, interactive bool) (out *ConnectedAccount, err error) {
	defer recoverErr(&err)
	return s.c.fetchConnectedAccount(ctx, provider, token, org, interactive)
}

// CreateRepo creates a repository on the host and returns its clone URL.
// channel: hosting:createRepo
func (s *Service) CreateRepo(ctx context.Context, provider, token string, opts CreateRepoOpts, org string) (out *RemoteRepo, err error) {
	defer recoverErr(&err)
	return s.c.createRepository(ctx, provider, token, opts, org)
}

// ListPRs lists the open pull/merge requests of a remote.
// channel: hosting:listPRs
func (s *Service) ListPRs(ctx context.Context, remoteURL string, tokens HostTokens) (out *PRList, err error) {
	defer recoverErr(&err)
	return s.c.listPullRequests(ctx, remoteURL, tokens)
}

// ListReleases lists a GitHub remote's releases.
// channel: hosting:listReleases
func (s *Service) ListReleases(ctx context.Context, remoteURL string, tokens HostTokens) (out *ReleaseList, err error) {
	defer recoverErr(&err)
	return s.c.listReleases(ctx, remoteURL, tokens)
}

// CiStatuses reads the GitHub check runs of up to 40 commits, keyed by sha.
// channel: hosting:ciStatuses
func (s *Service) CiStatuses(ctx context.Context, remoteURL string, shas []string, token string) (out map[string]CiStatus, err error) {
	defer recoverErr(&err)
	return s.c.fetchCiStatuses(ctx, remoteURL, shas, token)
}

// OpenCreatePR opens the host's "new pull request" page for source → target
// in the browser; false for an unrecognised remote.
// channel: hosting:openCreatePR
func (s *Service) OpenCreatePR(ctx context.Context, remoteURL, source, target string) (ok bool, err error) {
	defer recoverErr(&err)
	url, err := createPullRequestURL(remoteURL, source, target)
	if err != nil {
		return false, err
	}
	if url != "" && s.c.openURL != nil {
		// Fire and forget, as shell.openExternal was.
		_ = s.c.openURL(url)
	}
	return url != "", nil
}

// CreatePR opens a pull/merge request.
// channel: hosting:createPR
func (s *Service) CreatePR(ctx context.Context, remoteURL string, tokens HostTokens, opts CreatePrOpts) (out *CreatePrResult, err error) {
	defer recoverErr(&err)
	return s.c.createPullRequest(ctx, remoteURL, tokens, opts)
}

// PrDetail reads a pull/merge request with its conversation and reviews.
// channel: hosting:prDetail
func (s *Service) PrDetail(ctx context.Context, remoteURL string, tokens HostTokens, number int) (out *PrDetail, err error) {
	defer recoverErr(&err)
	return s.c.pullRequestDetail(ctx, remoteURL, tokens, number)
}

// PrComment adds a conversation comment.
// channel: hosting:prComment
func (s *Service) PrComment(ctx context.Context, remoteURL string, tokens HostTokens, number int, body string) (err error) {
	defer recoverErr(&err)
	return s.c.commentOnPr(ctx, remoteURL, tokens, number, body)
}

// PrReplyReviewComment replies to an inline review thread. inReplyTo is the
// GitHub root comment id (a number) or the GitLab discussion id (a string).
// channel: hosting:prReplyReviewComment
func (s *Service) PrReplyReviewComment(ctx context.Context, remoteURL string, tokens HostTokens, number int, inReplyTo json.RawMessage, body string) (err error) {
	defer recoverErr(&err)
	return s.c.replyReviewComment(ctx, remoteURL, tokens, number, inReplyTo, body)
}

// PrFiles lists a pull request's changed files.
// channel: hosting:prFiles
func (s *Service) PrFiles(ctx context.Context, remoteURL string, tokens HostTokens, number int) (out []PrFile, err error) {
	defer recoverErr(&err)
	return s.c.pullRequestFiles(ctx, remoteURL, tokens, number)
}

// PrChecks lists the CI checks on a pull request's head.
// channel: hosting:prChecks
func (s *Service) PrChecks(ctx context.Context, remoteURL string, tokens HostTokens, number int) (out []PrCheck, err error) {
	defer recoverErr(&err)
	return s.c.pullRequestChecks(ctx, remoteURL, tokens, number)
}

// PrReview submits a review: APPROVE, REQUEST_CHANGES or COMMENT.
// channel: hosting:prReview
func (s *Service) PrReview(ctx context.Context, remoteURL string, tokens HostTokens, number int, event, body string) (err error) {
	defer recoverErr(&err)
	return s.c.reviewPr(ctx, remoteURL, tokens, number, event, body)
}

// PrMerge merges a pull request with merge, squash or rebase.
// channel: hosting:prMerge
func (s *Service) PrMerge(ctx context.Context, remoteURL string, tokens HostTokens, number int, method string) (err error) {
	defer recoverErr(&err)
	return s.c.mergePr(ctx, remoteURL, tokens, number, method)
}

// MergedPrHeads reports which head branches have a merged pull request.
// channel: hosting:mergedPrHeads
func (s *Service) MergedPrHeads(ctx context.Context, remoteURL string, tokens HostTokens, branches []string) (out []string, err error) {
	defer recoverErr(&err)
	return s.c.mergedPrHeads(ctx, remoteURL, tokens, branches)
}

// EnsureStack registers the pull requests (bottom → top) as a native GitHub
// stack; nil when the repository is not in the preview.
// channel: hosting:ensureStack
func (s *Service) EnsureStack(ctx context.Context, remoteURL string, tokens HostTokens, numbers []int) (out *GithubStackInfo, err error) {
	defer recoverErr(&err)
	return s.c.ensureGithubStack(ctx, remoteURL, tokens, numbers)
}

// UpdatePR retargets, retitles or refreshes the stack section of a pull request.
// channel: hosting:updatePR
func (s *Service) UpdatePR(ctx context.Context, remoteURL string, tokens HostTokens, number int, patch PrPatch) (err error) {
	defer recoverErr(&err)
	return s.c.updatePullRequest(ctx, remoteURL, tokens, number, patch)
}

// ListNotifications lists GitHub notifications (unread only unless all).
// channel: hosting:listNotifications
func (s *Service) ListNotifications(ctx context.Context, token string, all bool) (out []GitHubNotification, err error) {
	defer recoverErr(&err)
	return s.c.listNotifications(ctx, token, all)
}

// MarkNotificationRead marks one notification thread read.
// channel: hosting:markNotificationRead
func (s *Service) MarkNotificationRead(ctx context.Context, token, id string) (err error) {
	defer recoverErr(&err)
	return s.c.markNotificationRead(ctx, token, id)
}

// MarkAllNotificationsRead marks every notification read.
// channel: hosting:markAllNotificationsRead
func (s *Service) MarkAllNotificationsRead(ctx context.Context, token string) (err error) {
	defer recoverErr(&err)
	return s.c.markAllNotificationsRead(ctx, token)
}

// ListIssues lists a GitHub remote's open issues.
// channel: hosting:listIssues
func (s *Service) ListIssues(ctx context.Context, remoteURL string, tokens HostTokens) (out *IssueList, err error) {
	defer recoverErr(&err)
	return s.c.listIssues(ctx, remoteURL, tokens)
}

// ListMilestones lists a GitHub remote's milestones.
// channel: hosting:listMilestones
func (s *Service) ListMilestones(ctx context.Context, remoteURL string, tokens HostTokens) (out *MilestoneList, err error) {
	defer recoverErr(&err)
	return s.c.listMilestones(ctx, remoteURL, tokens)
}

// MilestoneIssues lists the issues in a milestone.
// channel: hosting:milestoneIssues
func (s *Service) MilestoneIssues(ctx context.Context, remoteURL string, tokens HostTokens, number int) (out []IssueInfo, err error) {
	defer recoverErr(&err)
	return s.c.milestoneIssues(ctx, remoteURL, tokens, number)
}

// UploadSshKey registers an SSH public key on the GitHub account.
// channel: hosting:uploadSshKey
func (s *Service) UploadSshKey(ctx context.Context, token, title, publicKey string) (out *SSHKeyResult, err error) {
	defer recoverErr(&err)
	return s.c.uploadSshKey(ctx, token, title, publicKey)
}

// IssueDetail reads an issue with its conversation, linked PRs and project fields.
// channel: hosting:issueDetail
func (s *Service) IssueDetail(ctx context.Context, remoteURL string, tokens HostTokens, number int) (out *IssueDetail, err error) {
	defer recoverErr(&err)
	return s.c.issueDetail(ctx, remoteURL, tokens, number)
}

// SetIssueState opens or closes an issue.
// channel: hosting:setIssueState
func (s *Service) SetIssueState(ctx context.Context, remoteURL string, tokens HostTokens, number int, state string) (err error) {
	defer recoverErr(&err)
	return s.c.setIssueState(ctx, remoteURL, tokens, number, state)
}

// CreateIssue creates an issue.
// channel: hosting:createIssue
func (s *Service) CreateIssue(ctx context.Context, remoteURL string, tokens HostTokens, opts CreateIssueOpts) (out *CreatedIssue, err error) {
	defer recoverErr(&err)
	return s.c.createIssue(ctx, remoteURL, tokens, opts)
}

// ApplyPrMeta best-effort applies reviewers, labels and assignees to a PR.
// channel: hosting:applyPrMeta
func (s *Service) ApplyPrMeta(ctx context.Context, remoteURL string, tokens HostTokens, number int, meta PrMeta) (err error) {
	defer recoverErr(&err)
	return s.c.applyPrMeta(ctx, remoteURL, tokens, number, meta)
}
