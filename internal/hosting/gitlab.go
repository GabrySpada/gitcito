package hosting

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// GitLab merge request review. Mirrors the GitHub review surface, normalised
// into the shared PR types so the renderer never has to branch on provider
// vocabulary.

// glProjectOf resolves a GitLab remote to its project API base + credential.
func (c *client) glProjectOf(ctx context.Context, remoteURL, token string) (string, *apiAuth, error) {
	p, err := ParseRemoteURL(remoteURL)
	if err != nil {
		return "", nil, err
	}
	if p == nil || p.Provider != ProviderGitLab {
		//lint:ignore ST1005 shown to the user verbatim, byte-identical to the TS message
		return "", nil, errors.New("Not a GitLab repository.")
	}
	auth := c.creds.apiToken(ctx, remoteURL, token, FillOpts{})
	if auth == nil {
		return "", nil, c.noCredential(ctx, "GitLab", remoteURL)
	}
	return c.glAPI() + "/projects/" + encodeURIComponent(p.Owner+"/"+p.Repo), auth, nil
}

type glNote struct {
	ID        int          `json:"id"`
	System    bool         `json:"system"`
	Author    *usernameRef `json:"author"`
	Body      string       `json:"body"`
	CreatedAt string       `json:"created_at"`
	Position  *struct {
		NewPath *string `json:"new_path"`
		OldPath *string `json:"old_path"`
		NewLine *int    `json:"new_line"`
		OldLine *int    `json:"old_line"`
	} `json:"position"`
}

func (c *client) glMergeRequestDetail(ctx context.Context, remoteURL, token string, number int) (*PrDetail, error) {
	base, auth, err := c.glProjectOf(ctx, remoteURL, token)
	if err != nil {
		return nil, err
	}
	mrURL := base + "/merge_requests/" + strconv.Itoa(number)
	var mr struct {
		IID            int          `json:"iid"`
		Title          string       `json:"title"`
		Description    *string      `json:"description"`
		Author         *usernameRef `json:"author"`
		SourceBranch   string       `json:"source_branch"`
		TargetBranch   string       `json:"target_branch"`
		Draft          bool         `json:"draft"`
		WorkInProgress bool         `json:"work_in_progress"`
		State          string       `json:"state"`
		MergeStatus    string       `json:"merge_status"`
		WebURL         string       `json:"web_url"`
	}
	var discussions []struct {
		ID    string   `json:"id"`
		Notes []glNote `json:"notes"`
	}
	var approvals struct {
		ApprovedBy []struct {
			User *usernameRef `json:"user"`
		} `json:"approved_by"`
	}
	err = all(
		func() error { return c.glJSON(ctx, "GET", mrURL, auth, nil, &mr) },
		func() error {
			if c.glJSON(ctx, "GET", mrURL+"/discussions?per_page=100", auth, nil, &discussions) != nil {
				discussions = nil
			}
			return nil
		},
		func() error {
			if c.glJSON(ctx, "GET", mrURL+"/approvals", auth, nil, &approvals) != nil {
				approvals.ApprovedBy = nil
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	// Discussions hold both kinds of comment: a note with a position is an inline
	// review thread, one without is plain conversation. System notes are noise.
	comments := []PrComment{}
	threads := []PrReviewThread{}
	for _, d := range discussions {
		var notes []glNote
		for _, n := range d.Notes {
			if !n.System {
				notes = append(notes, n)
			}
		}
		if len(notes) == 0 {
			continue
		}
		if pos := notes[0].Position; pos != nil {
			path := derefOr(pos.NewPath, "")
			if path == "" {
				path = derefOr(pos.OldPath, "")
			}
			line := pos.NewLine
			if line == nil {
				line = pos.OldLine
			}
			th := PrReviewThread{
				Path: path, Line: line,
				// GitLab notes carry a file position but not the surrounding hunk.
				DiffHunk: "", RootID: d.ID, Comments: []PrReviewComment{},
			}
			for _, n := range notes {
				th.Comments = append(th.Comments, PrReviewComment{ID: n.ID, Author: usernameOr(n.Author, "unknown"), Body: n.Body, CreatedAt: n.CreatedAt})
			}
			threads = append(threads, th)
			continue
		}
		for _, n := range notes {
			comments = append(comments, PrComment{Author: usernameOr(n.Author, "unknown"), Body: n.Body, CreatedAt: n.CreatedAt})
		}
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].CreatedAt < comments[j].CreatedAt })

	var mergeable *bool
	switch mr.MergeStatus {
	case "can_be_merged":
		mergeable = boolPtr(true)
	case "cannot_be_merged":
		mergeable = boolPtr(false)
	}
	reviews := []PrReview{}
	for _, a := range approvals.ApprovedBy {
		reviews = append(reviews, PrReview{Author: usernameOr(a.User, "unknown"), State: "APPROVED"})
	}
	state := "closed"
	if mr.State == "opened" {
		state = "open"
	}
	return &PrDetail{
		Number: mr.IID, Title: mr.Title, Body: derefOr(mr.Description, ""), Author: usernameOr(mr.Author, "unknown"),
		Source: mr.SourceBranch, Target: mr.TargetBranch, Draft: mr.Draft || mr.WorkInProgress,
		State: state, Merged: mr.State == "merged",
		Mergeable: mergeable, URL: mr.WebURL, Comments: comments, Reviews: reviews, ReviewThreads: threads,
	}, nil
}

// GlJob is a GitLab pipeline job as the API returns it.
type GlJob struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	WebURL *string `json:"web_url"`
}

var glJobStates = map[string][2]string{
	"created":              {"queued", ""},
	"pending":              {"queued", ""},
	"waiting_for_resource": {"queued", ""},
	"preparing":            {"queued", ""},
	"scheduled":            {"queued", ""},
	"running":              {"in_progress", ""},
	"success":              {"completed", "success"},
	"failed":               {"completed", "failure"},
	"canceled":             {"completed", "cancelled"},
	"canceling":            {"completed", "cancelled"},
	"skipped":              {"completed", "skipped"},
	"manual":               {"completed", "action_required"},
}

// objectProtoKeys are the names a plain JS object literal inherits; looking
// one up in the TS status table returned the inherited member, not undefined.
var objectProtoKeys = map[string]bool{
	"constructor": true, "__proto__": true, "hasOwnProperty": true, "isPrototypeOf": true,
	"propertyIsEnumerable": true, "toLocaleString": true, "toString": true, "valueOf": true,
	"__defineGetter__": true, "__defineSetter__": true, "__lookupGetter__": true, "__lookupSetter__": true,
}

// GlJobToCheck maps a GitLab job status onto the check-run vocabulary the
// renderer already renders.
func GlJobToCheck(job GlJob) PrCheck {
	url := derefOr(job.WebURL, "")
	if objectProtoKeys[job.Status] {
		return PrCheck{Name: job.Name, URL: url, protoKey: true}
	}
	m, ok := glJobStates[job.Status]
	if !ok {
		m = [2]string{"completed", "neutral"}
	}
	var conclusion *string
	if m[1] != "" {
		conclusion = strPtr(m[1])
	}
	return PrCheck{Name: job.Name, Status: m[0], Conclusion: conclusion, URL: url}
}

func (c *client) glMergeRequestChecks(ctx context.Context, remoteURL, token string, number int) ([]PrCheck, error) {
	base, auth, err := c.glProjectOf(ctx, remoteURL, token)
	if err != nil {
		return nil, err
	}
	// Pipelines come newest-first; the head pipeline's jobs are the checks.
	var pipelines []struct {
		ID int `json:"id"`
	}
	if c.glJSON(ctx, "GET", base+"/merge_requests/"+strconv.Itoa(number)+"/pipelines?per_page=1", auth, nil, &pipelines) != nil {
		pipelines = nil
	}
	if len(pipelines) == 0 {
		return []PrCheck{}, nil
	}
	var jobs []GlJob
	if c.glJSON(ctx, "GET", base+"/pipelines/"+strconv.Itoa(pipelines[0].ID)+"/jobs?per_page=100", auth, nil, &jobs) != nil {
		jobs = nil
	}
	out := make([]PrCheck, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, GlJobToCheck(j))
	}
	return out, nil
}

// DiffLineCounts counts added/removed lines in a unified diff body (file
// headers excluded).
func DiffLineCounts(diff string) (additions, deletions int) {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			additions++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			deletions++
		}
	}
	return additions, deletions
}

func (c *client) glMergeRequestFiles(ctx context.Context, remoteURL, token string, number int) ([]PrFile, error) {
	base, auth, err := c.glProjectOf(ctx, remoteURL, token)
	if err != nil {
		return nil, err
	}
	var diffs []struct {
		OldPath     string  `json:"old_path"`
		NewPath     string  `json:"new_path"`
		NewFile     bool    `json:"new_file"`
		RenamedFile bool    `json:"renamed_file"`
		DeletedFile bool    `json:"deleted_file"`
		Diff        *string `json:"diff"`
	}
	if c.glJSON(ctx, "GET", base+"/merge_requests/"+strconv.Itoa(number)+"/diffs?per_page=100", auth, nil, &diffs) != nil {
		diffs = nil
	}
	out := make([]PrFile, 0, len(diffs))
	for _, d := range diffs {
		name := d.NewPath
		if name == "" {
			name = d.OldPath
		}
		status := "modified"
		switch {
		case d.NewFile:
			status = "added"
		case d.DeletedFile:
			status = "removed"
		case d.RenamedFile:
			status = "renamed"
		}
		add, del := DiffLineCounts(derefOr(d.Diff, ""))
		out = append(out, PrFile{Filename: name, Status: status, Additions: add, Deletions: del})
	}
	return out, nil
}

func (c *client) glReplyReviewComment(ctx context.Context, remoteURL, token string, number int, discussionID, body string) error {
	base, auth, err := c.glProjectOf(ctx, remoteURL, token)
	if err != nil {
		return err
	}
	payload := jsonStringify(jsObject{{"body", body}})
	return c.glJSON(ctx, "POST", base+"/merge_requests/"+strconv.Itoa(number)+"/discussions/"+encodeURIComponent(discussionID)+"/notes",
		auth, &payload, nil)
}

func (c *client) glCommentOnMr(ctx context.Context, remoteURL, token string, number int, body string) error {
	base, auth, err := c.glProjectOf(ctx, remoteURL, token)
	if err != nil {
		return err
	}
	payload := jsonStringify(jsObject{{"body", body}})
	return c.glJSON(ctx, "POST", base+"/merge_requests/"+strconv.Itoa(number)+"/notes", auth, &payload, nil)
}

func (c *client) glReviewMr(ctx context.Context, remoteURL, token string, number int, event, body string) error {
	base, auth, err := c.glProjectOf(ctx, remoteURL, token)
	if err != nil {
		return err
	}
	mr := base + "/merge_requests/" + strconv.Itoa(number)
	// GitLab has no single "submit review" call: approval is its own endpoint,
	// and requesting changes is an unapproval plus a comment.
	switch event {
	case "APPROVE":
		if err := c.glJSON(ctx, "POST", mr+"/approve", auth, nil, nil); err != nil {
			return err
		}
	case "REQUEST_CHANGES":
		// A 404 here just means "you had not approved" — nothing worth surfacing.
		_ = c.glJSON(ctx, "POST", mr+"/unapprove", auth, nil, nil)
	}
	if body != "" {
		payload := jsonStringify(jsObject{{"body", body}})
		return c.glJSON(ctx, "POST", mr+"/notes", auth, &payload, nil)
	}
	return nil
}

func (c *client) glMergeMr(ctx context.Context, remoteURL, token string, number int, method string) error {
	base, auth, err := c.glProjectOf(ctx, remoteURL, token)
	if err != nil {
		return err
	}
	// GitLab picks merge-commit vs fast-forward from the project settings; the
	// API only takes a squash flag. The renderer hides "rebase" for GitLab.
	payload := jsonStringify(jsObject{{"squash", method == "squash"}})
	return c.glJSON(ctx, "PUT", base+"/merge_requests/"+strconv.Itoa(number)+"/merge", auth, &payload, nil)
}
