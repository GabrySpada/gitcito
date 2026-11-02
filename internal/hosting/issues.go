package hosting

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

type ghIssueItem struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	User        *loginRef       `json:"user"`
	State       string          `json:"state"`
	HTMLURL     string          `json:"html_url"`
	Comments    int             `json:"comments"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// issuesOnly maps the issues endpoint's items, which also include pull
// requests — those are filtered out.
func issuesOnly(data []ghIssueItem) []IssueInfo {
	out := []IssueInfo{}
	for _, i := range data {
		if rawTruthy(i.PullRequest) {
			continue
		}
		out = append(out, IssueInfo{
			Number: i.Number, Title: i.Title, Author: loginOr(i.User, "unknown"),
			State: openOrClosed(i.State), URL: i.HTMLURL, Comments: i.Comments,
		})
	}
	return out
}

// rawTruthy is JS truthiness of an optional raw JSON property.
func rawTruthy(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	return jsTruthy(decodeAny(raw))
}

// githubOnlyList parses a remote for the GitHub-only list channels: nil
// provider for an unknown remote, the provider name otherwise.
func githubOnlyList(remoteURL string) (*ParsedRemote, *string, error) {
	parsed, err := ParseRemoteURL(remoteURL)
	if err != nil || parsed == nil {
		return nil, nil, err
	}
	return parsed, &parsed.Provider, nil
}

func (c *client) listIssues(ctx context.Context, remoteURL string, tokens HostTokens) (*IssueList, error) {
	parsed, provider, err := githubOnlyList(remoteURL)
	if err != nil {
		return nil, err
	}
	if parsed == nil || parsed.Provider != ProviderGitHub {
		return &IssueList{Provider: provider, Issues: []IssueInfo{}}, nil
	}
	auth := c.creds.apiToken(ctx, remoteURL, tokens.GitHub, FillOpts{})
	if auth == nil {
		return &IssueList{Provider: provider, Issues: []IssueInfo{}}, nil
	}
	var data []ghIssueItem
	if err := c.ghJSON(ctx, "GET", c.ghRepoAPI(parsed.Owner, parsed.Repo)+"/issues?state=open&per_page=50", auth.token, nil, &data); err != nil {
		return nil, err
	}
	return &IssueList{Provider: provider, Issues: issuesOnly(data)}, nil
}

func (c *client) listMilestones(ctx context.Context, remoteURL string, tokens HostTokens) (*MilestoneList, error) {
	parsed, provider, err := githubOnlyList(remoteURL)
	if err != nil {
		return nil, err
	}
	if parsed == nil || parsed.Provider != ProviderGitHub {
		return &MilestoneList{Provider: provider, Milestones: []MilestoneInfo{}}, nil
	}
	auth := c.creds.apiToken(ctx, remoteURL, tokens.GitHub, FillOpts{})
	if auth == nil {
		return &MilestoneList{Provider: provider, Milestones: []MilestoneInfo{}}, nil
	}
	var data []struct {
		Number       int     `json:"number"`
		Title        string  `json:"title"`
		Description  *string `json:"description"`
		State        string  `json:"state"`
		DueOn        *string `json:"due_on"`
		OpenIssues   int     `json:"open_issues"`
		ClosedIssues int     `json:"closed_issues"`
		HTMLURL      string  `json:"html_url"`
	}
	if err := c.ghJSON(ctx, "GET", c.ghRepoAPI(parsed.Owner, parsed.Repo)+"/milestones?state=all&per_page=50", auth.token, nil, &data); err != nil {
		return nil, err
	}
	out := make([]MilestoneInfo, 0, len(data))
	for _, m := range data {
		out = append(out, MilestoneInfo{
			Number: m.Number, Title: m.Title, Description: derefOr(m.Description, ""), State: openOrClosed(m.State),
			DueOn: m.DueOn, OpenIssues: m.OpenIssues, ClosedIssues: m.ClosedIssues, URL: m.HTMLURL,
		})
	}
	return &MilestoneList{Provider: provider, Milestones: out}, nil
}

// projectFieldsQuery asks for an issue's Projects v2 custom-field values.
const projectFieldsQuery = `query($owner:String!,$repo:String!,$number:Int!){
    repository(owner:$owner,name:$repo){
      issue(number:$number){
        projectItems(first:10){ nodes{
          project{ title }
          fieldValues(first:30){ nodes{
            __typename
            ... on ProjectV2ItemFieldTextValue { text field{ ... on ProjectV2FieldCommon { name } } }
            ... on ProjectV2ItemFieldNumberValue { number field{ ... on ProjectV2FieldCommon { name } } }
            ... on ProjectV2ItemFieldDateValue { date field{ ... on ProjectV2FieldCommon { name } } }
            ... on ProjectV2ItemFieldSingleSelectValue { name field{ ... on ProjectV2FieldCommon { name } } }
            ... on ProjectV2ItemFieldIterationValue { title field{ ... on ProjectV2FieldCommon { name } } }
          } }
        } }
      }
    }
  }`

// fetchProjectFields reads Projects v2 custom fields for an issue (Priority,
// Start/Target date, Effort, …). GraphQL-only and requires the token's
// `read:project` scope — best-effort, so any error (missing scope, no project)
// yields an empty list rather than failing.
func (c *client) fetchProjectFields(ctx context.Context, owner, repo string, number int, token string) []ProjectFieldGroup {
	groups := []ProjectFieldGroup{}
	body := jsonStringify(jsObject{
		{"query", projectFieldsQuery},
		{"variables", jsObject{{"owner", owner}, {"repo", repo}, {"number", number}}},
	})
	res, err := c.fetch(ctx, "POST", c.ep.GitHubAPI+"/graphql",
		[]header{{"Authorization", "Bearer " + token}, {"Content-Type", "application/json"}}, &body)
	if err != nil || !res.ok() {
		return groups
	}
	var root any
	if res.json(&root) != nil {
		return groups
	}
	items, _ := prop(prop(prop(prop(prop(root, "data"), "repository"), "issue"), "projectItems"), "nodes").([]any)
	for _, item := range items {
		fields := []ProjectField{}
		nodes, _ := prop(prop(item, "fieldValues"), "nodes").([]any)
		for _, fv := range nodes {
			name := prop(prop(fv, "field"), "name")
			if !jsTruthy(name) {
				continue // skip non-custom values (no field name)
			}
			// `??` chain: the first of text/name/title/date that is not null or
			// undefined, else a numeric value rendered with String().
			var value any
			for _, k := range []string{"text", "name", "title", "date"} {
				if v := prop(fv, k); v != nil {
					value = v
					break
				}
			}
			if value == nil {
				if n, ok := prop(fv, "number").(float64); ok {
					value = jsNumberString(n)
				}
			}
			if value != nil && value != "" {
				fields = append(fields, ProjectField{Name: jsString(name), Value: jsString(value)})
			}
		}
		if len(fields) > 0 {
			title := "Project"
			if t := prop(prop(item, "project"), "title"); t != nil {
				title = jsString(t)
			}
			groups = append(groups, ProjectFieldGroup{Project: title, Fields: fields})
		}
	}
	return groups
}

func (c *client) issueDetail(ctx context.Context, remoteURL string, tokens HostTokens, number int) (*IssueDetail, error) {
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return nil, err
	}
	api := c.api(r)
	n := strconv.Itoa(number)
	var issue struct {
		Number    int               `json:"number"`
		Title     string            `json:"title"`
		Body      *string           `json:"body"`
		User      *loginRef         `json:"user"`
		State     string            `json:"state"`
		HTMLURL   string            `json:"html_url"`
		CreatedAt string            `json:"created_at"`
		Labels    []json.RawMessage `json:"labels"`
		Assignees []loginRef        `json:"assignees"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
	}
	var comments []ghComment
	var timeline []struct {
		Event  string `json:"event"`
		Source *struct {
			Issue *struct {
				Number      int             `json:"number"`
				Title       string          `json:"title"`
				HTMLURL     string          `json:"html_url"`
				State       string          `json:"state"`
				PullRequest json.RawMessage `json:"pull_request"`
			} `json:"issue"`
		} `json:"source"`
	}
	var projectFields []ProjectFieldGroup
	err = all(
		func() error { return c.ghJSON(ctx, "GET", api+"/issues/"+n, r.token, nil, &issue) },
		func() error {
			return c.ghJSON(ctx, "GET", api+"/issues/"+n+"/comments?per_page=100", r.token, nil, &comments)
		},
		func() error {
			if c.ghJSON(ctx, "GET", api+"/issues/"+n+"/timeline?per_page=100", r.token, nil, &timeline) != nil {
				timeline = nil
			}
			return nil
		},
		func() error {
			projectFields = c.fetchProjectFields(ctx, r.owner, r.repo, number, r.token)
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	linked := []LinkedPr{}
	linkedAt := map[int]int{}
	for _, ev := range timeline {
		if ev.Event != "cross-referenced" || ev.Source == nil || ev.Source.Issue == nil {
			continue
		}
		si := ev.Source.Issue
		if !rawTruthy(si.PullRequest) {
			continue
		}
		pr := LinkedPr{Number: si.Number, Title: si.Title, URL: si.HTMLURL, State: si.State}
		// A Map keeps the first insertion's position and the last value.
		if i, ok := linkedAt[si.Number]; ok {
			linked[i] = pr
		} else {
			linkedAt[si.Number] = len(linked)
			linked = append(linked, pr)
		}
	}

	labels := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		var s string
		if json.Unmarshal(l, &s) == nil {
			labels = append(labels, s)
			continue
		}
		var o struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(l, &o)
		labels = append(labels, o.Name)
	}
	assignees := make([]string, 0, len(issue.Assignees))
	for _, a := range issue.Assignees {
		assignees = append(assignees, a.Login)
	}
	var milestone *string
	if issue.Milestone != nil {
		milestone = strPtr(issue.Milestone.Title)
	}
	return &IssueDetail{
		Number: issue.Number, Title: issue.Title, Body: derefOr(issue.Body, ""), Author: loginOr(issue.User, "unknown"),
		State: openOrClosed(issue.State), URL: issue.HTMLURL, Labels: labels, Assignees: assignees,
		Milestone: milestone, CreatedAt: issue.CreatedAt, Comments: mapComments(comments),
		LinkedPrs: linked, ProjectFields: projectFields,
	}, nil
}

// milestoneIssues lists the issues belonging to a milestone (open + closed).
// GitHub only.
func (c *client) milestoneIssues(ctx context.Context, remoteURL string, tokens HostTokens, number int) ([]IssueInfo, error) {
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return nil, err
	}
	var data []ghIssueItem
	if err := c.ghJSON(ctx, "GET", c.api(r)+"/issues?milestone="+strconv.Itoa(number)+"&state=all&per_page=100", r.token, nil, &data); err != nil {
		return nil, err
	}
	return issuesOnly(data), nil
}

func (c *client) setIssueState(ctx context.Context, remoteURL string, tokens HostTokens, number int, state string) error {
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return err
	}
	payload := jsonStringify(jsObject{{"state", state}})
	return c.ghJSON(ctx, "PATCH", c.api(r)+"/issues/"+strconv.Itoa(number), r.token, &payload, nil)
}

// createIssue creates a new issue. GitHub only. Returns its number + web URL.
func (c *client) createIssue(ctx context.Context, remoteURL string, tokens HostTokens, opts CreateIssueOpts) (*CreatedIssue, error) {
	r, err := c.ghRepoOf(ctx, remoteURL, tokens.GitHub)
	if err != nil {
		return nil, err
	}
	payload := jsonStringify(jsObject{{"title", opts.Title}, {"body", opts.Body}})
	var d struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.ghJSON(ctx, "POST", c.api(r)+"/issues", r.token, &payload, &d); err != nil {
		return nil, err
	}
	return &CreatedIssue{Number: d.Number, URL: d.HTMLURL}, nil
}

// ── notifications ───────────────────────────────────────────────────────────

var allDigits = regexp.MustCompile(`^\d+$`)

// listNotifications lists GitHub notifications for the authenticated user,
// across every repo the token can see. Token-level (no remote needed).
// all=false ⇒ unread only.
func (c *client) listNotifications(ctx context.Context, token string, all bool) ([]GitHubNotification, error) {
	// Same precedence as every other call: a typed token, else the credential
	// git's helper already holds for github.com. Silent (this backs a poll).
	auth := c.tokenForProvider(ctx, ProviderGitHub, token, "")
	if auth == nil {
		return []GitHubNotification{}, nil
	}
	var data []struct {
		ID        string `json:"id"`
		Reason    string `json:"reason"`
		Unread    bool   `json:"unread"`
		UpdatedAt string `json:"updated_at"`
		Subject   struct {
			Title string  `json:"title"`
			URL   *string `json:"url"`
			Type  string  `json:"type"`
		} `json:"subject"`
		Repository struct {
			FullName string `json:"full_name"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
	}
	if err := c.ghJSON(ctx, "GET", c.ep.GitHubAPI+"/notifications?all="+strconv.FormatBool(all)+"&per_page=50", auth.token, nil, &data); err != nil {
		return nil, err
	}
	out := make([]GitHubNotification, 0, len(data))
	for _, n := range data {
		// subject.url is an API url (…/repos/o/r/issues/5 or …/pulls/5). Derive the
		// trailing number and a browser URL — note PRs use the singular /pull/.
		tail := ""
		if n.Subject.URL != nil {
			parts := strings.Split(*n.Subject.URL, "/")
			tail = parts[len(parts)-1]
		}
		var number *int64
		if allDigits.MatchString(tail) {
			f, _ := strconv.ParseFloat(tail, 64)
			v := int64(f)
			number = &v
		}
		url := n.Repository.HTMLURL
		if number != nil {
			switch n.Subject.Type {
			case "PullRequest":
				url = n.Repository.HTMLURL + "/pull/" + strconv.FormatInt(*number, 10)
			case "Issue":
				url = n.Repository.HTMLURL + "/issues/" + strconv.FormatInt(*number, 10)
			}
		} else if n.Subject.Type == "Release" {
			url = n.Repository.HTMLURL + "/releases"
		}
		out = append(out, GitHubNotification{
			ID: n.ID, Reason: n.Reason, Title: n.Subject.Title, Type: n.Subject.Type,
			RepoFullName: n.Repository.FullName, RepoURL: n.Repository.HTMLURL, Number: number,
			Unread: n.Unread, UpdatedAt: unixSeconds(n.UpdatedAt), URL: url,
		})
	}
	return out, nil
}

// unixSeconds is `Math.floor(new Date(s).getTime() / 1000)` for the ISO 8601
// timestamps GitHub sends; nil where JS would produce NaN.
func unixSeconds(s string) *int64 {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		if layout == "2006-01-02T15:04:05" {
			// A date-time without an offset is local time in JS.
			t, _ = time.ParseInLocation(layout, s, time.Local)
		}
		v := int64(math.Floor(float64(t.UnixMilli()) / 1000))
		return &v
	}
	return nil
}

func (c *client) markNotificationRead(ctx context.Context, token, id string) error {
	auth := c.tokenForProvider(ctx, ProviderGitHub, token, "")
	if auth == nil {
		return nil
	}
	// Already read / gone — non-fatal.
	_ = c.ghJSON(ctx, "PATCH", c.ep.GitHubAPI+"/notifications/threads/"+id, auth.token, nil, nil)
	return nil
}

func (c *client) markAllNotificationsRead(ctx context.Context, token string) error {
	auth := c.tokenForProvider(ctx, ProviderGitHub, token, "")
	if auth == nil {
		return nil
	}
	// The PUT /notifications endpoint returns 202 with an empty body; ghJSON would
	// choke parsing JSON, so call fetch directly. Failures are non-fatal.
	body := jsonStringify(jsObject{{"read", true}})
	_, _ = c.fetch(ctx, "PUT", c.ep.GitHubAPI+"/notifications", []header{
		{"Accept", "application/vnd.github+json"},
		{"Authorization", "Bearer " + auth.token},
		{"Content-Type", "application/json"},
	}, &body)
	return nil
}

// uploadSshKey registers an SSH public key on the user's GitHub account. The
// only place the host token and the SSH world meet: the token is what
// authorises adding a key the transport will then use. Only the public half is
// ever sent.
func (c *client) uploadSshKey(ctx context.Context, token, title, publicKey string) (*SSHKeyResult, error) {
	payload := jsonStringify(jsObject{{"title", title}, {"key", gitx.TrimJS(publicKey)}})
	var out SSHKeyResult
	if err := c.ghJSON(ctx, "POST", c.ep.GitHubAPI+"/user/keys", token, &payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
