package hosting

import "encoding/json"

// Wire types. JSON tags match src/shared/types.ts exactly: `?:` fields are
// omitempty pointers (or omitzero slices), `T | null` fields are pointers
// without omitempty, and slices the renderer maps over are never nil.

// PullRequest mirrors PullRequest.
type PullRequest struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Author       string  `json:"author"`
	SourceBranch string  `json:"sourceBranch"`
	TargetBranch string  `json:"targetBranch"`
	URL          string  `json:"url"`
	IsDraft      bool    `json:"isDraft"`
	StackNumber  *int    `json:"stackNumber,omitempty"`
	State        string  `json:"state,omitempty"` // open | closed | merged
	HeadSha      *string `json:"headSha,omitempty"`
	CI           string  `json:"ci,omitempty"`
	CISummary    string  `json:"ciSummary,omitempty"`
}

// PRList is the hosting:listPRs result. Provider is null for an unrecognised remote.
type PRList struct {
	Provider *string       `json:"provider"`
	PRs      []PullRequest `json:"prs"`
}

// ReleaseInfo mirrors ReleaseInfo.
type ReleaseInfo struct {
	ID          int     `json:"id"`
	Tag         *string `json:"tag"`
	Name        *string `json:"name"`
	Body        *string `json:"body"`
	PublishedAt *string `json:"publishedAt"`
	URL         string  `json:"url"`
	Prerelease  bool    `json:"prerelease"`
	Draft       bool    `json:"draft"`
}

// ReleaseList is the hosting:listReleases result.
type ReleaseList struct {
	Provider *string       `json:"provider"`
	Releases []ReleaseInfo `json:"releases"`
}

// CiJob mirrors CiJob.
type CiJob struct {
	Name  string  `json:"name"`
	State string  `json:"state"` // success | failure | pending | neutral
	URL   *string `json:"url,omitempty"`
}

// CiStatus mirrors CiStatus.
type CiStatus struct {
	State string  `json:"state"`
	Jobs  []CiJob `json:"jobs"`
}

// CreatePrOpts mirrors CreatePrOpts.
type CreatePrOpts struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Source string `json:"source"` // head branch
	Target string `json:"target"` // base branch
	Draft  bool   `json:"draft"`
}

// CreatePrResult mirrors CreatePrResult.
type CreatePrResult struct {
	URL    string `json:"url"`
	Number int    `json:"number"`
}

// PrComment mirrors PrComment.
type PrComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

// PrReview mirrors PrReview.
type PrReview struct {
	Author string `json:"author"`
	State  string `json:"state"`
}

// PrReviewComment mirrors PrReviewComment.
type PrReviewComment struct {
	ID        int    `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

// PrReviewThread mirrors PrReviewThread. RootID is the reply target: a GitHub
// root comment id (number) or a GitLab discussion id (string).
type PrReviewThread struct {
	Path     string            `json:"path"`
	Line     *int              `json:"line"`
	DiffHunk string            `json:"diffHunk"`
	RootID   any               `json:"rootId"`
	Comments []PrReviewComment `json:"comments"`
}

// PrDetail mirrors PrDetail.
type PrDetail struct {
	Number        int              `json:"number"`
	Title         string           `json:"title"`
	Body          string           `json:"body"`
	Author        string           `json:"author"`
	Source        string           `json:"source"`
	Target        string           `json:"target"`
	Draft         bool             `json:"draft"`
	State         string           `json:"state"` // open | closed
	Merged        bool             `json:"merged"`
	Mergeable     *bool            `json:"mergeable"`
	URL           string           `json:"url"`
	Comments      []PrComment      `json:"comments"`
	Reviews       []PrReview       `json:"reviews"`
	ReviewThreads []PrReviewThread `json:"reviewThreads"`
}

// PrCheck mirrors PrCheck.
type PrCheck struct {
	Name string `json:"name"`
	// Status is queued | in_progress | completed.
	Status string `json:"status"`
	// Conclusion is null while running.
	Conclusion *string `json:"conclusion"`
	URL        string  `json:"url"`

	// protoKey marks a GitLab status that named an Object.prototype member:
	// the TS lookup table returned that member, so status and conclusion were
	// undefined and fell out of the JSON.
	protoKey bool
}

// MarshalJSON leaves out status and conclusion for protoKey checks.
func (c PrCheck) MarshalJSON() ([]byte, error) {
	if c.protoKey {
		return json.Marshal(struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}{c.Name, c.URL})
	}
	type plain PrCheck
	return json.Marshal(plain(c))
}

// PrFile mirrors PrFile.
type PrFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// IssueInfo mirrors IssueInfo.
type IssueInfo struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	State    string `json:"state"`
	URL      string `json:"url"`
	Comments int    `json:"comments"`
}

// IssueList is the hosting:listIssues result.
type IssueList struct {
	Provider *string     `json:"provider"`
	Issues   []IssueInfo `json:"issues"`
}

// MilestoneInfo mirrors MilestoneInfo.
type MilestoneInfo struct {
	Number       int     `json:"number"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	State        string  `json:"state"`
	DueOn        *string `json:"dueOn"`
	OpenIssues   int     `json:"openIssues"`
	ClosedIssues int     `json:"closedIssues"`
	URL          string  `json:"url"`
}

// MilestoneList is the hosting:listMilestones result.
type MilestoneList struct {
	Provider   *string         `json:"provider"`
	Milestones []MilestoneInfo `json:"milestones"`
}

// ProjectField is one custom-field value.
type ProjectField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ProjectFieldGroup mirrors ProjectFieldGroup.
type ProjectFieldGroup struct {
	Project string         `json:"project"`
	Fields  []ProjectField `json:"fields"`
}

// LinkedPr mirrors LinkedPr.
type LinkedPr struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// IssueDetail mirrors IssueDetail.
type IssueDetail struct {
	Number        int                 `json:"number"`
	Title         string              `json:"title"`
	Body          string              `json:"body"`
	Author        string              `json:"author"`
	State         string              `json:"state"`
	URL           string              `json:"url"`
	Labels        []string            `json:"labels"`
	Assignees     []string            `json:"assignees"`
	Milestone     *string             `json:"milestone"`
	CreatedAt     string              `json:"createdAt"`
	Comments      []PrComment         `json:"comments"`
	LinkedPrs     []LinkedPr          `json:"linkedPrs"`
	ProjectFields []ProjectFieldGroup `json:"projectFields"`
}

// CreatedIssue is the hosting:createIssue result.
type CreatedIssue struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// CreateIssueOpts is the hosting:createIssue input.
type CreateIssueOpts struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// PrMeta is the hosting:applyPrMeta input.
type PrMeta struct {
	Reviewers []string `json:"reviewers,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
}

// PrPatch is the hosting:updatePR input. StackSection is a pointer because an
// empty section still rewrites the body; only an absent one leaves it alone.
type PrPatch struct {
	Base         string  `json:"base,omitempty"`
	Title        string  `json:"title,omitempty"`
	StackSection *string `json:"stackSection,omitempty"`
}

// GithubStackInfo mirrors GithubStackInfo.
type GithubStackInfo struct {
	Number int     `json:"number"`
	URL    *string `json:"url,omitempty"`
	Added  int     `json:"added"`
}

// GitHubNotification mirrors GitHubNotification. UpdatedAt is null where the
// TS produced NaN (an unparseable timestamp), which JSON carries as null.
type GitHubNotification struct {
	ID           string `json:"id"`
	Reason       string `json:"reason"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	RepoFullName string `json:"repoFullName"`
	RepoURL      string `json:"repoUrl"`
	Number       *int64 `json:"number"`
	Unread       bool   `json:"unread"`
	UpdatedAt    *int64 `json:"updatedAt"`
	URL          string `json:"url"`
}

// SSHKeyResult is the hosting:uploadSshKey result.
type SSHKeyResult struct {
	ID int64 `json:"id"`
}

// RemoteRepo mirrors RemoteRepo.
type RemoteRepo struct {
	Name        string  `json:"name"`
	URL         string  `json:"url"`
	Private     *bool   `json:"private,omitempty"`
	Description *string `json:"description,omitempty"`
	AvatarURL   *string `json:"avatarUrl,omitempty"`
}

// RemoteOwner mirrors RemoteOwner.
type RemoteOwner struct {
	ID        string  `json:"id"`
	Login     string  `json:"login"`
	AvatarURL *string `json:"avatarUrl,omitempty"`
	Type      string  `json:"type"` // user | org
}

// ConnectedOrg mirrors ConnectedOrg.
type ConnectedOrg struct {
	Login     string  `json:"login"`
	AvatarURL *string `json:"avatarUrl,omitempty"`
	URL       *string `json:"url,omitempty"`
}

// ConnectedAccount mirrors ConnectedAccount.
type ConnectedAccount struct {
	Login      string         `json:"login"`
	Name       *string        `json:"name,omitempty"`
	AvatarURL  *string        `json:"avatarUrl,omitempty"`
	ProfileURL *string        `json:"profileUrl,omitempty"`
	Orgs       []ConnectedOrg `json:"orgs,omitzero"`
}

// CreateRepoOpts mirrors CreateRepoOpts.
type CreateRepoOpts struct {
	Owner       string `json:"owner"`
	OwnerType   string `json:"ownerType"` // user | org
	OwnerID     string `json:"ownerId,omitempty"`
	Project     string `json:"project,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Private     bool   `json:"private"`
}

// loginRef is a `{ login }` object that may be null.
type loginRef struct {
	Login string `json:"login"`
}

func loginOr(u *loginRef, def string) string {
	if u == nil {
		return def
	}
	return u.Login
}

type usernameRef struct {
	Username string `json:"username"`
}

func usernameOr(u *usernameRef, def string) string {
	if u == nil {
		return def
	}
	return u.Username
}

// derefOr is `p ?? def`.
func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func openOrClosed(state string) string {
	if state == "closed" {
		return "closed"
	}
	return "open"
}
