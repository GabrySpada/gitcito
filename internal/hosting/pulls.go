package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/shared"
)

//lint:ignore ST1005 shown to the user verbatim, byte-identical to the TS message
var errGitHubOnly = errors.New("This action currently supports GitHub repositories only.")

// ghRepo is a GitHub remote resolved to owner/repo and a token.
type ghRepo struct{ owner, repo, token string }

// ghRepoOf resolves a GitHub remote to {owner, repo} or fails (these
// operations are GitHub-only for now).
func (c *client) ghRepoOf(ctx context.Context, remoteURL, token string) (*ghRepo, error) {
	p, err := ParseRemoteURL(remoteURL)
	if err != nil {
		return nil, err
	}
	if p == nil || p.Provider != ProviderGitHub {
		return nil, errGitHubOnly
	}
	auth := c.creds.apiToken(ctx, remoteURL, token, FillOpts{})
	if auth == nil {
		return nil, c.noCredential(ctx, "GitHub", remoteURL)
	}
	return &ghRepo{owner: p.Owner, repo: p.Repo, token: auth.token}, nil
}

func (c *client) api(r *ghRepo) string { return c.ghRepoAPI(r.owner, r.repo) }

// isGitLab: the PR surface branches to GitLab merge requests for GitLab remotes.
func isGitLab(remoteURL string) (bool, error) {
	p, err := ParseRemoteURL(remoteURL)
	if err != nil {
		return false, err
	}
	return p != nil && p.Provider == ProviderGitLab, nil
}

// githubStackMember is the shape of a stack member, as much of it as we rely on.
type githubStackMember struct {
	Number   int       `json:"number"`
	Title    *string   `json:"title"`
	HTMLURL  *string   `json:"html_url"`
	Draft    *bool     `json:"draft"`
	State    *string   `json:"state"`
	MergedAt *string   `json:"merged_at"`
	User     *loginRef `json:"user"`
	Head     *struct {
		Ref *string `json:"ref"`
		Sha *string `json:"sha"`
	} `json:"head"`
	Base *struct {
		Ref *string `json:"ref"`
	} `json:"base"`
}

func (c *client) listPullRequests(ctx context.Context, remoteURL string, tokens HostTokens) (*PRList, error) {
	parsed, err := ParseRemoteURL(remoteURL)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return &PRList{PRs: []PullRequest{}}, nil
	}
	provider := parsed.Provider
	auth := c.creds.apiToken(ctx, remoteURL, tokens.of(provider), FillOpts{})

	switch provider {
	case ProviderGitLab:
		// GitLab merge requests
		if auth == nil {
			return &PRList{Provider: &provider, PRs: []PullRequest{}}, nil
		}
		pid := encodeURIComponent(parsed.Owner + "/" + parsed.Repo)
		res, err := c.fetch(ctx, "GET", c.glAPI()+"/projects/"+pid+"/merge_requests?state=opened&per_page=30",
			[]header{gitlabAuth(auth)}, nil)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, errors.New("GitLab API error (" + strconv.Itoa(res.status) + ")")
		}
		var data []struct {
			IID            int          `json:"iid"`
			Title          string       `json:"title"`
			Author         *usernameRef `json:"author"`
			SourceBranch   string       `json:"source_branch"`
			TargetBranch   string       `json:"target_branch"`
			WebURL         string       `json:"web_url"`
			Draft          bool         `json:"draft"`
			WorkInProgress bool         `json:"work_in_progress"`
		}
		if err := res.json(&data); err != nil {
			return nil, err
		}
		prs := make([]PullRequest, 0, len(data))
		for _, p := range data {
			prs = append(prs, PullRequest{
				ID: p.IID, Title: p.Title, Author: usernameOr(p.Author, "unknown"),
				SourceBranch: p.SourceBranch, TargetBranch: p.TargetBranch, URL: p.WebURL,
				IsDraft: p.Draft || p.WorkInProgress,
			})
		}
		return &PRList{Provider: &provider, PRs: prs}, nil

	case ProviderBitbucket:
		// Bitbucket pull requests
		if auth == nil {
			return &PRList{Provider: &provider, PRs: []PullRequest{}}, nil
		}
		res, err := c.fetch(ctx, "GET",
			c.ep.BitbucketAPI+"/2.0/repositories/"+parsed.Owner+"/"+parsed.Repo+"/pullrequests?state=OPEN&pagelen=30",
			[]header{{"Authorization", bitbucketAuthFor(auth)}}, nil)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, errors.New("Bitbucket API error (" + strconv.Itoa(res.status) + ")")
		}
		type branchRef struct {
			Branch *struct {
				Name string `json:"name"`
			} `json:"branch"`
		}
		var data struct {
			Values []struct {
				ID     int    `json:"id"`
				Title  string `json:"title"`
				Author *struct {
					DisplayName string `json:"display_name"`
				} `json:"author"`
				Source      *branchRef `json:"source"`
				Destination *branchRef `json:"destination"`
				Links       struct {
					HTML struct {
						Href string `json:"href"`
					} `json:"html"`
				} `json:"links"`
			} `json:"values"`
		}
		if err := res.json(&data); err != nil {
			return nil, err
		}
		name := func(b *branchRef) string {
			if b == nil || b.Branch == nil {
				return ""
			}
			return b.Branch.Name
		}
		prs := make([]PullRequest, 0, len(data.Values))
		for _, p := range data.Values {
			author := "unknown"
			if p.Author != nil {
				author = p.Author.DisplayName
			}
			prs = append(prs, PullRequest{
				ID: p.ID, Title: p.Title, Author: author,
				SourceBranch: name(p.Source), TargetBranch: name(p.Destination),
				URL: p.Links.HTML.Href, IsDraft: false,
			})
		}
		return &PRList{Provider: &provider, PRs: prs}, nil

	case ProviderGitHub:
		return c.listGitHubPullRequests(ctx, remoteURL, parsed, auth)
	}

	// Azure DevOps
	if auth == nil {
		return nil, c.noCredential(ctx, "Azure DevOps", remoteURL)
	}
	apiBase := c.ep.Azure + "/" + parsed.Owner + "/" + encodeURIComponent(parsed.Project)
	webBase := webAzure + "/" + parsed.Owner + "/" + encodeURIComponent(parsed.Project)
	var data struct {
		Value []struct {
			PullRequestID int    `json:"pullRequestId"`
			Title         string `json:"title"`
			IsDraft       bool   `json:"isDraft"`
			CreatedBy     struct {
				DisplayName string `json:"displayName"`
			} `json:"createdBy"`
			SourceRefName string `json:"sourceRefName"`
			TargetRefName string `json:"targetRefName"`
		} `json:"value"`
	}
	err = c.withAzureRetry(ctx, remoteURL, auth, func(token string) error {
		return c.adoJSON(ctx, "GET",
			apiBase+"/_apis/git/repositories/"+encodeURIComponent(parsed.Repo)+"/pullrequests?searchCriteria.status=active&api-version=7.1",
			token, nil, &data)
	})
	if err != nil {
		return nil, err
	}
	prs := make([]PullRequest, 0, len(data.Value))
	for _, p := range data.Value {
		prs = append(prs, PullRequest{
			ID: p.PullRequestID, Title: p.Title, Author: p.CreatedBy.DisplayName,
			SourceBranch: strings.Replace(p.SourceRefName, "refs/heads/", "", 1),
			TargetBranch: strings.Replace(p.TargetRefName, "refs/heads/", "", 1),
			URL:          webBase + "/_git/" + encodeURIComponent(parsed.Repo) + "/pullrequest/" + strconv.Itoa(p.PullRequestID),
			IsDraft:      p.IsDraft,
		})
	}
	return &PRList{Provider: &provider, PRs: prs}, nil
}

func (c *client) listGitHubPullRequests(ctx context.Context, remoteURL string, parsed *ParsedRemote, auth *apiAuth) (*PRList, error) {
	provider := ProviderGitHub
	hs := []header{{"Accept", "application/vnd.github+json"}}
	if auth != nil {
		hs = append(hs, header{"Authorization", "Bearer " + auth.token})
	}
	api := c.ghRepoAPI(parsed.Owner, parsed.Repo)
	res, err := c.fetch(ctx, "GET", api+"/pulls?state=open&per_page=30", hs, nil)
	if err != nil {
		return nil, err
	}
	if !res.ok() {
		return nil, errors.New("GitHub API error (" + strconv.Itoa(res.status) + ")")
	}
	var data []struct {
		Number  int      `json:"number"`
		Title   string   `json:"title"`
		Draft   bool     `json:"draft"`
		HTMLURL string   `json:"html_url"`
		User    loginRef `json:"user"`
		Head    struct {
			Ref string `json:"ref"`
			Sha string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		// Present once GitHub's stacked pull requests are in play; absent on a
		// server without them, which is the same as "this one is on its own".
		Stack *struct {
			Number *int `json:"number"`
		} `json:"stack"`
	}
	if err := res.json(&data); err != nil {
		return nil, err
	}
	prs := make([]PullRequest, 0, len(data))
	for _, p := range data {
		pr := PullRequest{
			ID: p.Number, Title: p.Title, Author: p.User.Login, SourceBranch: p.Head.Ref,
			TargetBranch: p.Base.Ref, URL: p.HTMLURL, IsDraft: p.Draft, State: "open", HeadSha: strPtr(p.Head.Sha),
		}
		if p.Stack != nil {
			pr.StackNumber = p.Stack.Number
		}
		prs = append(prs, pr)
	}
	if auth == nil {
		return &PRList{Provider: &provider, PRs: prs}, nil
	}

	// A stack is only legible if the levels that are already closed or merged
	// are in the list too, and `state=open` hides exactly those. GitHub knows
	// the membership, so ask it — once per stack, not once per level.
	var stackNumbers []int
	seenStack := map[int]bool{}
	known := map[int]bool{}
	for _, p := range prs {
		known[p.ID] = true
		if p.StackNumber != nil && !seenStack[*p.StackNumber] {
			seenStack[*p.StackNumber] = true
			stackNumbers = append(stackNumbers, *p.StackNumber)
		}
	}
	for _, stackNumber := range stackNumbers {
		var members struct {
			PullRequests []*githubStackMember `json:"pull_requests"`
		}
		if c.ghJSON(ctx, "GET", api+"/stacks/"+strconv.Itoa(stackNumber), auth.token, nil, &members) != nil {
			continue
		}
		for _, m := range members.PullRequests {
			if m == nil || m.Number == 0 || known[m.Number] {
				continue
			}
			known[m.Number] = true
			n := strconv.Itoa(m.Number)
			pr := PullRequest{
				ID:          m.Number,
				Title:       derefOr(m.Title, "#"+n),
				Author:      "",
				URL:         derefOr(m.HTMLURL, webGitHub+"/"+parsed.Owner+"/"+parsed.Repo+"/pull/"+n),
				StackNumber: &stackNumber,
				State:       "open",
			}
			if m.User != nil {
				pr.Author = m.User.Login
			}
			if m.Head != nil {
				pr.SourceBranch = derefOr(m.Head.Ref, "")
				pr.HeadSha = m.Head.Sha
			}
			if m.Base != nil {
				pr.TargetBranch = derefOr(m.Base.Ref, "")
			}
			if m.Draft != nil {
				pr.IsDraft = *m.Draft
			}
			switch {
			case m.MergedAt != nil && *m.MergedAt != "":
				pr.State = "merged"
			case m.State != nil && *m.State == "closed":
				pr.State = "closed"
			}
			prs = append(prs, pr)
		}
	}

	// The checks on each head, in the same batched shape the graph already
	// uses — one request per pull request, only when the list is refreshed.
	var shas []string
	for _, p := range prs {
		if p.HeadSha != nil && *p.HeadSha != "" {
			shas = append(shas, *p.HeadSha)
		}
	}
	ci, err := c.fetchCiStatuses(ctx, remoteURL, shas, auth.token)
	if err != nil {
		ci = map[string]CiStatus{}
	}
	for i := range prs {
		if prs[i].HeadSha == nil {
			continue
		}
		status, ok := ci[*prs[i].HeadSha]
		if !ok {
			continue
		}
		prs[i].CI = status.State
		prs[i].CISummary = summariseChecks(status)
	}
	return &PRList{Provider: &provider, PRs: prs}, nil
}

func (c *client) listReleases(ctx context.Context, remoteURL string, tokens HostTokens) (*ReleaseList, error) {
	parsed, err := ParseRemoteURL(remoteURL)
	if err != nil {
		return nil, err
	}
	// Releases are a GitHub concept; Azure DevOps "releases" are pipelines, not this.
	if parsed == nil {
		return &ReleaseList{Releases: []ReleaseInfo{}}, nil
	}
	if parsed.Provider != ProviderGitHub {
		return &ReleaseList{Provider: &parsed.Provider, Releases: []ReleaseInfo{}}, nil
	}
	hs := []header{{"Accept", "application/vnd.github+json"}}
	if auth := c.creds.apiToken(ctx, remoteURL, tokens.GitHub, FillOpts{}); auth != nil {
		hs = append(hs, header{"Authorization", "Bearer " + auth.token})
	}
	res, err := c.fetch(ctx, "GET", c.ghRepoAPI(parsed.Owner, parsed.Repo)+"/releases?per_page=50", hs, nil)
	if err != nil {
		return nil, err
	}
	if !res.ok() {
		return nil, errors.New("GitHub API error (" + strconv.Itoa(res.status) + ")")
	}
	var data []struct {
		ID          int     `json:"id"`
		TagName     *string `json:"tag_name"`
		Name        *string `json:"name"`
		Body        *string `json:"body"`
		Draft       bool    `json:"draft"`
		Prerelease  bool    `json:"prerelease"`
		PublishedAt *string `json:"published_at"`
		HTMLURL     string  `json:"html_url"`
	}
	if err := res.json(&data); err != nil {
		return nil, err
	}
	provider := ProviderGitHub
	out := make([]ReleaseInfo, 0, len(data))
	for _, r := range data {
		tag := r.TagName
		if tag != nil && *tag == "" {
			tag = nil
		}
		out = append(out, ReleaseInfo{
			ID: r.ID, Tag: tag, Name: r.Name, Body: r.Body, PublishedAt: r.PublishedAt,
			URL: r.HTMLURL, Prerelease: r.Prerelease, Draft: r.Draft,
		})
	}
	return &ReleaseList{Provider: &provider, Releases: out}, nil
}

func ghCiState(conclusion *string, status string) string {
	if status != "completed" {
		return "pending"
	}
	switch {
	case conclusion != nil && *conclusion == "success":
		return "success"
	case conclusion != nil && (*conclusion == "neutral" || *conclusion == "skipped"):
		return "neutral"
	}
	return "failure"
}

// summariseChecks: "3 passed · 1 failing" — the row shows a dot, the tooltip shows this.
func summariseChecks(status CiStatus) string {
	count := func(state string) int {
		n := 0
		for _, j := range status.Jobs {
			if j.State == state {
				n++
			}
		}
		return n
	}
	var parts []string
	if n := count("success"); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" passed")
	}
	if n := count("failure"); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" failing")
	}
	if n := count("pending"); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" running")
	}
	if n := count("neutral"); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" skipped")
	}
	return strings.Join(parts, " · ")
}

// maxCiShas caps how many commits one refresh asks about.
const maxCiShas = 40

func (c *client) fetchCiStatuses(ctx context.Context, remoteURL string, shas []string, token string) (map[string]CiStatus, error) {
	result := map[string]CiStatus{}
	parsed, err := ParseRemoteURL(remoteURL)
	if err != nil {
		return nil, err
	}
	if parsed == nil || parsed.Provider != ProviderGitHub {
		return result, nil
	}
	auth := c.creds.apiToken(ctx, remoteURL, token, FillOpts{})
	if auth == nil {
		return result, nil
	}
	hs := []header{{"Accept", "application/vnd.github+json"}, {"Authorization", "Bearer " + auth.token}}
	if len(shas) > maxCiShas {
		shas = shas[:maxCiShas]
	}
	statuses := make([]*CiStatus, len(shas))
	fns := make([]func() error, len(shas))
	for i, sha := range shas {
		fns[i] = func() error {
			// A failed SHA is skipped, never fatal.
			res, err := c.fetch(ctx, "GET",
				c.ghRepoAPI(parsed.Owner, parsed.Repo)+"/commits/"+sha+"/check-runs?per_page=30", hs, nil)
			if err != nil || !res.ok() {
				return nil
			}
			var data struct {
				CheckRuns []struct {
					Name       string  `json:"name"`
					Status     string  `json:"status"`
					Conclusion *string `json:"conclusion"`
					HTMLURL    *string `json:"html_url"`
				} `json:"check_runs"`
			}
			if res.json(&data) != nil || len(data.CheckRuns) == 0 {
				return nil
			}
			jobs := make([]CiJob, 0, len(data.CheckRuns))
			for _, r := range data.CheckRuns {
				jobs = append(jobs, CiJob{Name: r.Name, State: ghCiState(r.Conclusion, r.Status), URL: r.HTMLURL})
			}
			statuses[i] = &CiStatus{State: overallCiState(jobs), Jobs: jobs}
			return nil
		}
	}
	_ = all(fns...)
	for i, sha := range shas {
		if statuses[i] != nil {
			result[sha] = *statuses[i]
		}
	}
	return result, nil
}

func overallCiState(jobs []CiJob) string {
	has := func(state string) bool {
		for _, j := range jobs {
			if j.State == state {
				return true
			}
		}
		return false
	}
	switch {
	case has("failure"):
		return "failure"
	case has("pending"):
		return "pending"
	}
	for _, j := range jobs {
		if j.State != "neutral" {
			return "success"
		}
	}
	return "neutral"
}

// githubErrorMessage is a GitHub API failure, said in words.
//
// The headline for a rejected PR is always "Validation Failed", and the entry
// that explains it usually carries no `message` at all — only a `field` and a
// `code`, which the user cannot be expected to read. So the two that actually
// happen get spelled out with the branch names in them, and anything else
// degrades to `field: code` rather than being dropped, which is how the
// useless headline used to be all that survived.
func githubErrorMessage(detail any, status int, opts CreatePrOpts) string {
	var parts []string
	errs, _ := prop(detail, "errors").([]any)
	for _, e := range errs {
		switch msg, field, code := prop(e, "message"), prop(e, "field"), prop(e, "code"); {
		case jsTruthy(msg):
			parts = append(parts, jsString(msg))
		case field == "head":
			// The commonest cause by far: the branch was never pushed, so as far as
			// GitHub is concerned there is nothing to open a pull request from.
			parts = append(parts, `the branch "`+opts.Source+`" is not on the remote — push it first`)
		case field == "base":
			parts = append(parts, `the base branch "`+opts.Target+`" is not on the remote`)
		default:
			var fc []string
			for _, v := range []any{field, code} {
				if jsTruthy(v) {
					fc = append(fc, jsString(v))
				}
			}
			parts = append(parts, strings.Join(fc, ": "))
		}
	}
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	listed := strings.Join(nonEmpty, "; ")
	headline := prop(detail, "message")
	if listed != "" && jsTruthy(headline) {
		return jsString(headline) + " — " + listed
	}
	if listed != "" {
		return listed
	}
	if jsTruthy(headline) {
		return jsString(headline)
	}
	return "API error (" + strconv.Itoa(status) + ")"
}

// createPullRequest opens a pull/merge request on any of the four hosts.
func (c *client) createPullRequest(ctx context.Context, remoteURL string, tokens HostTokens, opts CreatePrOpts) (*CreatePrResult, error) {
	parsed, err := ParseRemoteURL(remoteURL)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		//lint:ignore ST1005 shown to the user verbatim, byte-identical to the TS message
		return nil, errors.New("Unrecognized remote — PR creation supports GitHub, GitLab, Bitbucket and Azure DevOps.")
	}
	auth := c.creds.apiToken(ctx, remoteURL, tokens.of(parsed.Provider), FillOpts{})

	switch parsed.Provider {
	case ProviderGitLab:
		// GitLab merge request
		if auth == nil {
			return nil, c.noCredential(ctx, "GitLab", remoteURL)
		}
		pid := encodeURIComponent(parsed.Owner + "/" + parsed.Repo)
		title := opts.Title
		if opts.Draft {
			title = "Draft: " + opts.Title
		}
		body := jsonStringify(jsObject{
			{"source_branch", opts.Source}, {"target_branch", opts.Target}, {"title", title}, {"description", opts.Body},
		})
		res, err := c.fetch(ctx, "POST", c.glAPI()+"/projects/"+pid+"/merge_requests",
			[]header{gitlabAuth(auth), {"Content-Type", "application/json"}}, &body)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			if m, ok := prop(decodeAny(res.body), "message").(string); ok {
				return nil, errors.New("GitLab: " + m)
			}
			return nil, errors.New("GitLab: API error (" + strconv.Itoa(res.status) + ")")
		}
		var d struct {
			IID    int    `json:"iid"`
			WebURL string `json:"web_url"`
		}
		if err := res.json(&d); err != nil {
			return nil, err
		}
		return &CreatePrResult{URL: d.WebURL, Number: d.IID}, nil

	case ProviderBitbucket:
		// Bitbucket pull request
		if auth == nil {
			return nil, c.noCredential(ctx, "Bitbucket", remoteURL)
		}
		body := jsonStringify(jsObject{
			{"title", opts.Title}, {"description", opts.Body},
			{"source", jsObject{{"branch", jsObject{{"name", opts.Source}}}}},
			{"destination", jsObject{{"branch", jsObject{{"name", opts.Target}}}}},
		})
		res, err := c.fetch(ctx, "POST",
			c.ep.BitbucketAPI+"/2.0/repositories/"+parsed.Owner+"/"+parsed.Repo+"/pullrequests",
			[]header{{"Authorization", bitbucketAuthFor(auth)}, {"Content-Type", "application/json"}}, &body)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			if m := prop(prop(decodeAny(res.body), "error"), "message"); jsTruthy(m) {
				return nil, errors.New("Bitbucket: " + jsString(m))
			}
			return nil, errors.New("Bitbucket: API error (" + strconv.Itoa(res.status) + ")")
		}
		var d struct {
			ID    int `json:"id"`
			Links struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		}
		if err := res.json(&d); err != nil {
			return nil, err
		}
		return &CreatePrResult{URL: d.Links.HTML.Href, Number: d.ID}, nil

	case ProviderGitHub:
		if auth == nil {
			return nil, c.noCredential(ctx, "GitHub", remoteURL)
		}
		body := jsonStringify(jsObject{
			{"title", opts.Title}, {"head", opts.Source}, {"base", opts.Target}, {"body", opts.Body}, {"draft", opts.Draft},
		})
		res, err := c.fetch(ctx, "POST", c.ghRepoAPI(parsed.Owner, parsed.Repo)+"/pulls", []header{
			{"Accept", "application/vnd.github+json"},
			{"Authorization", "Bearer " + auth.token},
			{"Content-Type", "application/json"},
		}, &body)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, errors.New("GitHub: " + githubErrorMessage(decodeAny(res.body), res.status, opts))
		}
		var d struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
		}
		if err := res.json(&d); err != nil {
			return nil, err
		}
		return &CreatePrResult{URL: d.HTMLURL, Number: d.Number}, nil
	}

	// Azure DevOps
	if auth == nil {
		return nil, c.noCredential(ctx, "Azure DevOps", remoteURL)
	}
	apiBase := c.ep.Azure + "/" + parsed.Owner + "/" + encodeURIComponent(parsed.Project)
	webBase := webAzure + "/" + parsed.Owner + "/" + encodeURIComponent(parsed.Project)
	body := jsonStringify(jsObject{
		{"sourceRefName", "refs/heads/" + opts.Source}, {"targetRefName", "refs/heads/" + opts.Target},
		{"title", opts.Title}, {"description", opts.Body}, {"isDraft", opts.Draft},
	})
	var d struct {
		PullRequestID int `json:"pullRequestId"`
	}
	err = c.withAzureRetry(ctx, remoteURL, auth, func(token string) error {
		return c.adoJSON(ctx, "POST",
			apiBase+"/_apis/git/repositories/"+encodeURIComponent(parsed.Repo)+"/pullrequests?api-version=7.1", token, &body, &d)
	})
	if err != nil {
		return nil, err
	}
	n := strconv.Itoa(d.PullRequestID)
	return &CreatePrResult{URL: webBase + "/_git/" + encodeURIComponent(parsed.Repo) + "/pullrequest/" + n, Number: d.PullRequestID}, nil
}

type ghComment struct {
	User      *loginRef `json:"user"`
	Body      string    `json:"body"`
	CreatedAt string    `json:"created_at"`
}

func mapComments(in []ghComment) []PrComment {
	out := make([]PrComment, 0, len(in))
	for _, cm := range in {
		out = append(out, PrComment{Author: loginOr(cm.User, "unknown"), Body: cm.Body, CreatedAt: cm.CreatedAt})
	}
	return out
}

func (c *client) pullRequestDetail(ctx context.Context, remoteURL string, tokens HostTokens, number int) (*PrDetail, error) {
	if gl, err := isGitLab(remoteURL); err != nil {
		return nil, err
	} else if gl {
		return c.glMergeRequestDetail(ctx, remoteURL, tokens.GitLab, number)
	}
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return nil, err
	}
	api := c.api(r)
	n := strconv.Itoa(number)
	var pr struct {
		Number int      `json:"number"`
		Title  string   `json:"title"`
		Body   *string  `json:"body"`
		User   loginRef `json:"user"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Draft     bool   `json:"draft"`
		State     string `json:"state"`
		Merged    bool   `json:"merged"`
		Mergeable *bool  `json:"mergeable"`
		HTMLURL   string `json:"html_url"`
	}
	var comments []ghComment
	var reviews []struct {
		User  *loginRef `json:"user"`
		State string    `json:"state"`
	}
	var reviewComments []struct {
		ID           int       `json:"id"`
		User         *loginRef `json:"user"`
		Body         string    `json:"body"`
		CreatedAt    string    `json:"created_at"`
		Path         string    `json:"path"`
		Line         *int      `json:"line"`
		OriginalLine *int      `json:"original_line"`
		DiffHunk     string    `json:"diff_hunk"`
		InReplyToID  *int      `json:"in_reply_to_id"`
	}
	err = all(
		func() error { return c.ghJSON(ctx, "GET", api+"/pulls/"+n, r.token, nil, &pr) },
		func() error {
			return c.ghJSON(ctx, "GET", api+"/issues/"+n+"/comments?per_page=100", r.token, nil, &comments)
		},
		func() error {
			return c.ghJSON(ctx, "GET", api+"/pulls/"+n+"/reviews?per_page=100", r.token, nil, &reviews)
		},
		func() error {
			if c.ghJSON(ctx, "GET", api+"/pulls/"+n+"/comments?per_page=100", r.token, nil, &reviewComments) != nil {
				reviewComments = nil
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	// Group inline review comments into threads. Replies (in_reply_to_id) attach to
	// their root; roots are keyed by their own id. Ordered by first comment.
	sort.SliceStable(reviewComments, func(i, j int) bool { return reviewComments[i].CreatedAt < reviewComments[j].CreatedAt })
	threads := []PrReviewThread{}
	threadAt := map[int]int{}
	for _, rc := range reviewComments {
		rootID := rc.ID
		if rc.InReplyToID != nil {
			rootID = *rc.InReplyToID
		}
		cm := PrReviewComment{ID: rc.ID, Author: loginOr(rc.User, "unknown"), Body: rc.Body, CreatedAt: rc.CreatedAt}
		if i, ok := threadAt[rootID]; ok {
			threads[i].Comments = append(threads[i].Comments, cm)
			continue
		}
		line := rc.Line
		if line == nil {
			line = rc.OriginalLine
		}
		threadAt[rootID] = len(threads)
		threads = append(threads, PrReviewThread{Path: rc.Path, Line: line, DiffHunk: rc.DiffHunk, RootID: rootID, Comments: []PrReviewComment{cm}})
	}
	outReviews := []PrReview{}
	for _, rv := range reviews {
		if rv.State != "PENDING" {
			outReviews = append(outReviews, PrReview{Author: loginOr(rv.User, "unknown"), State: rv.State})
		}
	}
	return &PrDetail{
		Number: pr.Number, Title: pr.Title, Body: derefOr(pr.Body, ""), Author: pr.User.Login,
		Source: pr.Head.Ref, Target: pr.Base.Ref, Draft: pr.Draft, State: openOrClosed(pr.State),
		Merged: pr.Merged, Mergeable: pr.Mergeable, URL: pr.HTMLURL,
		Comments: mapComments(comments), Reviews: outReviews, ReviewThreads: threads,
	}, nil
}

// pullRequestChecks lists the CI checks on a PR's head commit (GitHub check-runs
// / GitLab pipeline jobs).
func (c *client) pullRequestChecks(ctx context.Context, remoteURL string, tokens HostTokens, number int) ([]PrCheck, error) {
	if gl, err := isGitLab(remoteURL); err != nil {
		return nil, err
	} else if gl {
		return c.glMergeRequestChecks(ctx, remoteURL, tokens.GitLab, number)
	}
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return nil, err
	}
	api := c.api(r)
	var pr struct {
		Head struct {
			Sha string `json:"sha"`
		} `json:"head"`
	}
	if err := c.ghJSON(ctx, "GET", api+"/pulls/"+strconv.Itoa(number), r.token, nil, &pr); err != nil {
		return nil, err
	}
	var data struct {
		CheckRuns []struct {
			Name       string  `json:"name"`
			Status     string  `json:"status"`
			Conclusion *string `json:"conclusion"`
			HTMLURL    *string `json:"html_url"`
			DetailsURL *string `json:"details_url"`
		} `json:"check_runs"`
	}
	if c.ghJSON(ctx, "GET", api+"/commits/"+pr.Head.Sha+"/check-runs?per_page=100", r.token, nil, &data) != nil {
		data.CheckRuns = nil
	}
	out := make([]PrCheck, 0, len(data.CheckRuns))
	for _, ch := range data.CheckRuns {
		url := derefOr(ch.HTMLURL, "")
		if url == "" {
			url = derefOr(ch.DetailsURL, "")
		}
		out = append(out, PrCheck{Name: ch.Name, Status: ch.Status, Conclusion: ch.Conclusion, URL: url})
	}
	return out, nil
}

// pullRequestFiles lists the changed files in a PR (for the file-by-file
// review checklist).
func (c *client) pullRequestFiles(ctx context.Context, remoteURL string, tokens HostTokens, number int) ([]PrFile, error) {
	if gl, err := isGitLab(remoteURL); err != nil {
		return nil, err
	} else if gl {
		return c.glMergeRequestFiles(ctx, remoteURL, tokens.GitLab, number)
	}
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return nil, err
	}
	var data []PrFile
	if c.ghJSON(ctx, "GET", c.api(r)+"/pulls/"+strconv.Itoa(number)+"/files?per_page=300", r.token, nil, &data) != nil {
		data = nil
	}
	if data == nil {
		data = []PrFile{}
	}
	return data, nil
}

// replyReviewComment replies to an inline review thread. GitHub threads reply
// to a numeric comment id; GitLab to a discussion id string.
func (c *client) replyReviewComment(ctx context.Context, remoteURL string, tokens HostTokens, number int, inReplyTo json.RawMessage, body string) error {
	target, err := replyTarget(inReplyTo)
	if err != nil {
		return err
	}
	if gl, err := isGitLab(remoteURL); err != nil {
		return err
	} else if gl {
		return c.glReplyReviewComment(ctx, remoteURL, tokens.GitLab, number, target.text, body)
	}
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return err
	}
	payload := jsonStringify(jsObject{{"body", body}, {"in_reply_to", target.value}})
	return c.ghJSON(ctx, "POST", c.api(r)+"/pulls/"+strconv.Itoa(number)+"/comments", r.token, &payload, nil)
}

// replyRef is an `inReplyTo` argument: number | string.
type replyRef struct {
	value any    // re-encoded as it came: a JSON number or string
	text  string // String(inReplyTo)
}

// replyTarget validates the number-or-string reply target the renderer sends.
func replyTarget(raw json.RawMessage) (replyRef, error) {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err == nil {
		switch x := v.(type) {
		case string:
			return replyRef{value: x, text: x}, nil
		case json.Number:
			f, ferr := x.Float64()
			if ferr == nil {
				return replyRef{value: jsNumber(f), text: jsNumberString(f)}, nil
			}
		}
	}
	return replyRef{}, errors.New("inReplyTo must be a comment id (number) or a discussion id (string)")
}

func (c *client) commentOnPr(ctx context.Context, remoteURL string, tokens HostTokens, number int, body string) error {
	if gl, err := isGitLab(remoteURL); err != nil {
		return err
	} else if gl {
		return c.glCommentOnMr(ctx, remoteURL, tokens.GitLab, number, body)
	}
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return err
	}
	payload := jsonStringify(jsObject{{"body", body}})
	return c.ghJSON(ctx, "POST", c.api(r)+"/issues/"+strconv.Itoa(number)+"/comments", r.token, &payload, nil)
}

func (c *client) reviewPr(ctx context.Context, remoteURL string, tokens HostTokens, number int, event, body string) error {
	if gl, err := isGitLab(remoteURL); err != nil {
		return err
	} else if gl {
		return c.glReviewMr(ctx, remoteURL, tokens.GitLab, number, event, body)
	}
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return err
	}
	payload := jsonStringify(jsObject{{"event", event}, {"body", orUndefined(body)}})
	return c.ghJSON(ctx, "POST", c.api(r)+"/pulls/"+strconv.Itoa(number)+"/reviews", r.token, &payload, nil)
}

func (c *client) mergePr(ctx context.Context, remoteURL string, tokens HostTokens, number int, method string) error {
	if gl, err := isGitLab(remoteURL); err != nil {
		return err
	} else if gl {
		return c.glMergeMr(ctx, remoteURL, tokens.GitLab, number, method)
	}
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return err
	}
	payload := jsonStringify(jsObject{{"merge_method", method}})
	return c.ghJSON(ctx, "PUT", c.api(r)+"/pulls/"+strconv.Itoa(number)+"/merge", r.token, &payload, nil)
}

// applyPrMeta best-effort applies reviewers / labels / assignees to a PR after
// creation. Each call is independent; a failure on one doesn't block the others.
func (c *client) applyPrMeta(ctx context.Context, remoteURL string, tokens HostTokens, number int, meta PrMeta) error {
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return err
	}
	api := c.api(r)
	n := strconv.Itoa(number)
	var tasks []func() error
	post := func(url string, body jsObject) func() error {
		payload := jsonStringify(body)
		return func() error {
			_ = c.ghJSON(ctx, "POST", url, r.token, &payload, nil)
			return nil
		}
	}
	if len(meta.Reviewers) > 0 {
		tasks = append(tasks, post(api+"/pulls/"+n+"/requested_reviewers", jsObject{{"reviewers", meta.Reviewers}}))
	}
	if len(meta.Labels) > 0 {
		tasks = append(tasks, post(api+"/issues/"+n+"/labels", jsObject{{"labels", meta.Labels}}))
	}
	if len(meta.Assignees) > 0 {
		tasks = append(tasks, post(api+"/issues/"+n+"/assignees", jsObject{{"assignees", meta.Assignees}}))
	}
	return all(tasks...)
}

// mergedPrHeads reports which of these head branches have a MERGED pull request
// (GitHub only). This is how squash-merged stack bottoms get detected: the
// squashed patch leaves no ancestry a local git can follow, but the host knows
// the PR landed. One request per branch, filtered server-side by head ref.
func (c *client) mergedPrHeads(ctx context.Context, remoteURL string, tokens HostTokens, branches []string) ([]string, error) {
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return nil, err
	}
	merged := []string{}
	for _, branch := range branches {
		var prs []struct {
			MergedAt *string `json:"merged_at"`
		}
		url := c.api(r) + "/pulls?state=closed&head=" + encodeURIComponent(r.owner+":"+branch) + "&per_page=5"
		if c.ghJSON(ctx, "GET", url, r.token, nil, &prs) != nil {
			continue
		}
		for _, p := range prs {
			if p.MergedAt != nil && *p.MergedAt != "" {
				merged = append(merged, branch)
				break
			}
		}
	}
	return merged, nil
}

// updatePullRequest mutates an open PR (GitHub only): retargets its base,
// retitles it, or maintains the marked stack-navigation section in its body.
// `stackSection` merges into the live body server-side (one GET) so a user's
// own description edits survive.
func (c *client) updatePullRequest(ctx context.Context, remoteURL string, tokens HostTokens, number int, patch PrPatch) error {
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return err
	}
	url := c.api(r) + "/pulls/" + strconv.Itoa(number)
	var body jsObject
	if patch.Base != "" {
		body = append(body, jsField{"base", patch.Base})
	}
	if patch.Title != "" {
		body = append(body, jsField{"title", patch.Title})
	}
	if patch.StackSection != nil {
		var current struct {
			Body *string `json:"body"`
		}
		if err := c.ghJSON(ctx, "GET", url, r.token, nil, &current); err != nil {
			return err
		}
		body = append(body, jsField{"body", shared.MergeStackSection(derefOr(current.Body, ""), *patch.StackSection)})
	}
	if len(body) == 0 {
		return nil
	}
	payload := jsonStringify(body)
	return c.ghJSON(ctx, "PATCH", url, r.token, &payload, nil)
}

// ensureGithubStack registers the chain as a **native GitHub stack**, and
// reports what it is now.
//
// Chaining the bases is what makes a stack reviewable anywhere — GitLab,
// Bitbucket and Azure have nothing else. GitHub, since its stacked pull
// requests preview, also has a stack of its own: a real object that draws the
// chain in the PR UI, rebases the levels above server-side, and lands the whole
// thing from one merge. That is worth having, and it is one call on top of the
// PRs we already opened.
//
// `numbers` arrive bottom → top, the order the API wants. A repository that is
// not in the preview answers 404/403 for the endpoint; that is not a failure of
// the submit, so it returns nil and the chained PRs stand on their own.
func (c *client) ensureGithubStack(ctx context.Context, remoteURL string, tokens HostTokens, numbers []int) (*GithubStackInfo, error) {
	if len(numbers) < 2 {
		return nil, nil // one PR is not a stack
	}
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return nil, err
	}
	api := c.api(r)

	// Whatever stack these PRs are already in, if any: the bottom one decides,
	// because a stack is extended upward.
	var bottom struct {
		Stack *struct {
			Number *int `json:"number"`
		} `json:"stack"`
	}
	// The field is absent entirely on a server without the feature — treat that
	// the same as "no stack" and let the POST below decide.
	var existing *int
	if c.ghJSON(ctx, "GET", api+"/pulls/"+strconv.Itoa(numbers[0]), r.token, nil, &bottom) == nil && bottom.Stack != nil {
		existing = bottom.Stack.Number
	}

	if existing == nil {
		payload := jsonStringify(jsObject{{"pull_requests", numbers}})
		var created struct {
			Number  int     `json:"number"`
			HTMLURL *string `json:"html_url"`
		}
		if c.ghJSON(ctx, "POST", api+"/stacks", r.token, &payload, &created) != nil {
			// Not enrolled in the preview, or the token cannot manage stacks. The PRs
			// are already chained and navigable; say nothing rather than fail the run.
			return nil, nil
		}
		return &GithubStackInfo{Number: created.Number, URL: created.HTMLURL, Added: len(numbers)}, nil
	}
	// Extend: only the levels the stack does not already carry, still bottom → top.
	var current struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	}
	stackURL := api + "/stacks/" + strconv.Itoa(*existing)
	if c.ghJSON(ctx, "GET", stackURL, r.token, nil, &current) != nil {
		return nil, nil
	}
	have := map[int]bool{}
	for _, p := range current.PullRequests {
		have[p.Number] = true
	}
	missing := []int{}
	for _, n := range numbers {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		payload := jsonStringify(jsObject{{"pull_requests", missing}})
		if c.ghJSON(ctx, "POST", stackURL+"/add", r.token, &payload, nil) != nil {
			return nil, nil
		}
	}
	return &GithubStackInfo{Number: *existing, Added: len(missing)}, nil
}
