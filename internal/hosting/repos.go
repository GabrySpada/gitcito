package hosting

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

//lint:ignore ST1005 shown to the user verbatim, byte-identical to the TS message
var errNoAzureOrg = errors.New("Enter your Azure DevOps organization.")

func boolPtr(b bool) *bool { return &b }

// listRepositories lists the repositories the account can clone.
func (c *client) listRepositories(ctx context.Context, provider, token, org string) ([]RemoteRepo, error) {
	auth := c.tokenForProvider(ctx, provider, token, org)
	if auth == nil {
		return nil, c.noCredential(ctx, provider, ProviderBaseURL(provider, org))
	}

	switch provider {
	case ProviderGitHub:
		res, err := c.fetch(ctx, "GET",
			c.ep.GitHubAPI+"/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member",
			[]header{{"Accept", "application/vnd.github+json"}, {"Authorization", "Bearer " + auth.token}}, nil)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, errors.New("GitHub API error (" + strconv.Itoa(res.status) + ")")
		}
		var data []struct {
			FullName    string  `json:"full_name"`
			CloneURL    string  `json:"clone_url"`
			Private     bool    `json:"private"`
			Description *string `json:"description"`
			Owner       *struct {
				AvatarURL *string `json:"avatar_url"`
			} `json:"owner"`
		}
		if err := res.json(&data); err != nil {
			return nil, err
		}
		out := make([]RemoteRepo, 0, len(data))
		for _, r := range data {
			repo := RemoteRepo{Name: r.FullName, URL: r.CloneURL, Private: boolPtr(r.Private), Description: r.Description}
			if r.Owner != nil {
				repo.AvatarURL = r.Owner.AvatarURL
			}
			out = append(out, repo)
		}
		return out, nil

	case ProviderGitLab:
		res, err := c.fetch(ctx, "GET",
			c.glAPI()+"/projects?membership=true&per_page=100&order_by=last_activity_at&simple=true",
			[]header{gitlabAuth(auth)}, nil)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, errors.New("GitLab API error (" + strconv.Itoa(res.status) + ")")
		}
		var data []struct {
			PathWithNamespace string  `json:"path_with_namespace"`
			HTTPURLToRepo     string  `json:"http_url_to_repo"`
			Visibility        string  `json:"visibility"`
			Description       *string `json:"description"`
			AvatarURL         *string `json:"avatar_url"`
			Namespace         *struct {
				AvatarURL *string `json:"avatar_url"`
			} `json:"namespace"`
		}
		if err := res.json(&data); err != nil {
			return nil, err
		}
		out := make([]RemoteRepo, 0, len(data))
		for _, r := range data {
			repo := RemoteRepo{
				Name: r.PathWithNamespace, URL: r.HTTPURLToRepo,
				Private: boolPtr(r.Visibility != "public"), Description: r.Description,
				AvatarURL: r.AvatarURL,
			}
			if repo.AvatarURL == nil && r.Namespace != nil {
				repo.AvatarURL = r.Namespace.AvatarURL
			}
			out = append(out, repo)
		}
		return out, nil

	case ProviderBitbucket:
		res, err := c.fetch(ctx, "GET",
			c.ep.BitbucketAPI+"/2.0/repositories?role=member&pagelen=100&sort=-updated_on",
			[]header{{"Authorization", bitbucketAuthFor(auth)}}, nil)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, errors.New("Bitbucket API error (" + strconv.Itoa(res.status) + ")")
		}
		var data struct {
			Values []struct {
				FullName    string `json:"full_name"`
				IsPrivate   bool   `json:"is_private"`
				Description string `json:"description"`
				Links       struct {
					Clone  []bbLink `json:"clone"`
					Avatar *struct {
						Href string `json:"href"`
					} `json:"avatar"`
				} `json:"links"`
			} `json:"values"`
		}
		if err := res.json(&data); err != nil {
			return nil, err
		}
		out := make([]RemoteRepo, 0, len(data.Values))
		for _, r := range data.Values {
			repo := RemoteRepo{Name: r.FullName, URL: httpsClone(r.Links.Clone), Private: boolPtr(r.IsPrivate)}
			if r.Description != "" {
				repo.Description = strPtr(r.Description)
			}
			if r.Links.Avatar != nil {
				repo.AvatarURL = strPtr(r.Links.Avatar.Href)
			}
			out = append(out, repo)
		}
		return out, nil
	}

	// Azure DevOps — lists every repo across all projects in the organization.
	if gitx.TrimJS(org) == "" {
		return nil, errNoAzureOrg
	}
	base := ProviderBaseURL(ProviderAzure, org)
	var data struct {
		Value []struct {
			Name      string `json:"name"`
			RemoteURL string `json:"remoteUrl"`
			Project   struct {
				Name string `json:"name"`
			} `json:"project"`
		} `json:"value"`
	}
	err := c.withAzureRetry(ctx, base, auth, func(t string) error {
		return c.adoJSON(ctx, "GET", c.azureOrgAPI(org)+"/_apis/git/repositories?api-version=7.1", t, nil, &data)
	})
	if err != nil {
		return nil, err
	}
	out := make([]RemoteRepo, 0, len(data.Value))
	for _, r := range data.Value {
		out = append(out, RemoteRepo{Name: r.Project.Name + "/" + r.Name, URL: r.RemoteURL})
	}
	return out, nil
}

// azureOrgAPI is the organization's API root — providerBaseUrl('azure', org)
// on the configured endpoint.
func (c *client) azureOrgAPI(org string) string {
	return c.ep.Azure + "/" + encodeURIComponent(gitx.TrimJS(org))
}

type bbLink struct {
	Name string `json:"name"`
	Href string `json:"href"`
}

// httpsClone is `clone.find(c => c.name === 'https')?.href ?? clone[0]?.href ?? ”`.
func httpsClone(links []bbLink) string {
	for _, l := range links {
		if l.Name == "https" {
			return l.Href
		}
	}
	if len(links) > 0 {
		return links[0].Href
	}
	return ""
}

// listOwners lists the accounts a new repo can be created under: the
// authenticated user plus their orgs/groups.
func (c *client) listOwners(ctx context.Context, provider, token, org string) ([]RemoteOwner, error) {
	auth := c.tokenForProvider(ctx, provider, token, org)
	if auth == nil {
		return nil, c.noCredential(ctx, provider, ProviderBaseURL(provider, org))
	}

	switch provider {
	case ProviderGitHub:
		var user struct {
			Login     string  `json:"login"`
			AvatarURL *string `json:"avatar_url"`
		}
		if err := c.ghJSON(ctx, "GET", c.ep.GitHubAPI+"/user", auth.token, nil, &user); err != nil {
			return nil, err
		}
		var orgs []struct {
			Login     string  `json:"login"`
			AvatarURL *string `json:"avatar_url"`
		}
		if err := c.ghJSON(ctx, "GET", c.ep.GitHubAPI+"/user/orgs?per_page=100", auth.token, nil, &orgs); err != nil {
			return nil, err
		}
		out := []RemoteOwner{{ID: user.Login, Login: user.Login, AvatarURL: user.AvatarURL, Type: "user"}}
		for _, o := range orgs {
			out = append(out, RemoteOwner{ID: o.Login, Login: o.Login, AvatarURL: o.AvatarURL, Type: "org"})
		}
		return out, nil

	case ProviderGitLab:
		// No status check here in TS either: an error body maps to an owner of
		// undefined fields, and a non-array groups body fails on `.map`.
		hs := []header{gitlabAuth(auth)}
		userRes, err := c.fetch(ctx, "GET", c.glAPI()+"/user", hs, nil)
		if err != nil {
			return nil, err
		}
		var user map[string]any
		if err := userRes.json(&user); err != nil {
			return nil, err
		}
		groupsRes, err := c.fetch(ctx, "GET", c.glAPI()+"/groups?min_access_level=30&per_page=100", hs, nil)
		if err != nil {
			return nil, err
		}
		var groupsAny any
		if err := groupsRes.json(&groupsAny); err != nil {
			return nil, err
		}
		groups, isArray := groupsAny.([]any)
		if !isArray {
			return nil, errors.New("groups.map is not a function")
		}
		out := []RemoteOwner{{
			ID: jsStringProp(user, "id"), Login: jsStringValue(user["username"]),
			AvatarURL: nullishString(user["avatar_url"]), Type: "user",
		}}
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			out = append(out, RemoteOwner{
				ID: jsStringProp(gm, "id"), Login: jsStringValue(gm["full_path"]),
				AvatarURL: nullishString(gm["avatar_url"]), Type: "org",
			})
		}
		return out, nil

	case ProviderBitbucket:
		res, err := c.fetch(ctx, "GET", c.ep.BitbucketAPI+"/2.0/workspaces?pagelen=100",
			[]header{{"Authorization", bitbucketAuthFor(auth)}}, nil)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, errors.New("Bitbucket API error (" + strconv.Itoa(res.status) + ")")
		}
		var data struct {
			Values []bbWorkspace `json:"values"`
		}
		if err := res.json(&data); err != nil {
			return nil, err
		}
		out := make([]RemoteOwner, 0, len(data.Values))
		for _, w := range data.Values {
			out = append(out, RemoteOwner{ID: w.Slug, Login: w.Slug, AvatarURL: w.avatar(), Type: "org"})
		}
		return out, nil
	}

	// Azure DevOps — projects under the given organization act as "owners" for new repos.
	if gitx.TrimJS(org) == "" {
		return nil, errNoAzureOrg
	}
	base := ProviderBaseURL(ProviderAzure, org)
	var data struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}
	err := c.withAzureRetry(ctx, base, auth, func(t string) error {
		return c.adoJSON(ctx, "GET", c.azureOrgAPI(org)+"/_apis/projects?api-version=7.1", t, nil, &data)
	})
	if err != nil {
		return nil, err
	}
	out := make([]RemoteOwner, 0, len(data.Value))
	for _, p := range data.Value {
		out = append(out, RemoteOwner{ID: p.ID, Login: p.Name, Type: "org"})
	}
	return out, nil
}

type bbWorkspace struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Links *struct {
		Avatar *struct {
			Href string `json:"href"`
		} `json:"avatar"`
		HTML *struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func (w bbWorkspace) avatar() *string {
	if w.Links == nil || w.Links.Avatar == nil {
		return nil
	}
	return strPtr(w.Links.Avatar.Href)
}

func (w bbWorkspace) html() *string {
	if w.Links == nil || w.Links.HTML == nil {
		return nil
	}
	return strPtr(w.Links.HTML.Href)
}

// jsStringProp is `String(o.key)`, "undefined" when the key is absent.
func jsStringProp(o map[string]any, key string) string {
	v, ok := o[key]
	if !ok {
		return "undefined"
	}
	return jsString(v)
}

// jsStringValue reads a property expected to be a string; anything else
// (absent, null) becomes "".
func jsStringValue(v any) string {
	s, _ := v.(string)
	return s
}

// nullishString is `v ?? undefined` for a string-valued property.
func nullishString(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// fetchConnectedAccount resolves the authenticated user behind a stored token,
// for display in Settings → Integrations.
func (c *client) fetchConnectedAccount(ctx context.Context, provider, token, org string, interactive bool) (*ConnectedAccount, error) {
	// Only an explicit "sign in" may open the helper's login window. Merely opening
	// the Settings page must not prompt for four providers at once.
	auth := c.creds.apiToken(ctx, ProviderBaseURL(provider, org), token, FillOpts{Interactive: interactive})
	if auth == nil {
		return nil, c.noCredential(ctx, provider, ProviderBaseURL(provider, org))
	}

	switch provider {
	case ProviderGitHub:
		var user struct {
			Login     string  `json:"login"`
			Name      *string `json:"name"`
			AvatarURL *string `json:"avatar_url"`
			HTMLURL   *string `json:"html_url"`
		}
		if err := c.ghJSON(ctx, "GET", c.ep.GitHubAPI+"/user", auth.token, nil, &user); err != nil {
			return nil, err
		}
		var orgs []struct {
			Login     string  `json:"login"`
			AvatarURL *string `json:"avatar_url"`
		}
		if c.ghJSON(ctx, "GET", c.ep.GitHubAPI+"/user/orgs?per_page=100", auth.token, nil, &orgs) != nil {
			orgs = nil
		}
		acct := &ConnectedAccount{Login: user.Login, Name: user.Name, AvatarURL: user.AvatarURL, ProfileURL: user.HTMLURL,
			Orgs: make([]ConnectedOrg, 0, len(orgs))}
		for _, o := range orgs {
			acct.Orgs = append(acct.Orgs, ConnectedOrg{Login: o.Login, AvatarURL: o.AvatarURL, URL: strPtr(webGitHub + "/" + o.Login)})
		}
		return acct, nil

	case ProviderGitLab:
		hs := []header{gitlabAuth(auth)}
		userRes, err := c.fetch(ctx, "GET", c.glAPI()+"/user", hs, nil)
		if err != nil {
			return nil, err
		}
		if !userRes.ok() {
			return nil, errors.New("GitLab API error (" + strconv.Itoa(userRes.status) + ")")
		}
		var user struct {
			Username  string  `json:"username"`
			Name      *string `json:"name"`
			AvatarURL *string `json:"avatar_url"`
			WebURL    *string `json:"web_url"`
		}
		if err := userRes.json(&user); err != nil {
			return nil, err
		}
		groupsRes, err := c.fetch(ctx, "GET", c.glAPI()+"/groups?min_access_level=10&per_page=100", hs, nil)
		if err != nil {
			return nil, err
		}
		var groups []struct {
			FullPath  string  `json:"full_path"`
			AvatarURL *string `json:"avatar_url"`
			WebURL    *string `json:"web_url"`
		}
		if groupsRes.ok() {
			if err := groupsRes.json(&groups); err != nil {
				return nil, err
			}
		}
		acct := &ConnectedAccount{Login: user.Username, Name: user.Name, AvatarURL: user.AvatarURL, ProfileURL: user.WebURL,
			Orgs: make([]ConnectedOrg, 0, len(groups))}
		for _, g := range groups {
			acct.Orgs = append(acct.Orgs, ConnectedOrg{Login: g.FullPath, AvatarURL: g.AvatarURL, URL: g.WebURL})
		}
		return acct, nil

	case ProviderBitbucket:
		bb := []header{{"Authorization", bitbucketAuthFor(auth)}}
		userRes, err := c.fetch(ctx, "GET", c.ep.BitbucketAPI+"/2.0/user", bb, nil)
		if err != nil {
			return nil, err
		}
		if !userRes.ok() {
			return nil, errors.New("Bitbucket API error (" + strconv.Itoa(userRes.status) + ")")
		}
		var user struct {
			Username    string  `json:"username"`
			DisplayName *string `json:"display_name"`
			Links       *struct {
				Avatar *struct {
					Href string `json:"href"`
				} `json:"avatar"`
				HTML *struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		}
		if err := userRes.json(&user); err != nil {
			return nil, err
		}
		wsRes, err := c.fetch(ctx, "GET", c.ep.BitbucketAPI+"/2.0/workspaces?pagelen=100", bb, nil)
		if err != nil {
			return nil, err
		}
		var ws struct {
			Values []bbWorkspace `json:"values"`
		}
		if wsRes.ok() {
			if err := wsRes.json(&ws); err != nil {
				return nil, err
			}
		}
		acct := &ConnectedAccount{Login: user.Username, Name: user.DisplayName, Orgs: make([]ConnectedOrg, 0, len(ws.Values))}
		if user.Links != nil && user.Links.Avatar != nil {
			acct.AvatarURL = strPtr(user.Links.Avatar.Href)
		}
		if user.Links != nil && user.Links.HTML != nil {
			acct.ProfileURL = strPtr(user.Links.HTML.Href)
		}
		for _, w := range ws.Values {
			url := w.html()
			if url == nil {
				url = strPtr(webBitbucket + "/" + w.Slug)
			}
			acct.Orgs = append(acct.Orgs, ConnectedOrg{Login: w.Slug, AvatarURL: w.avatar(), URL: url})
		}
		return acct, nil
	}

	// Azure DevOps — prefer an organization-scoped check. The global profile service
	// (app.vssps.visualstudio.com) returns 401 for Entra/guest accounts even with a valid
	// org-scoped PAT, so when we know the organization we validate against connectionData,
	// which works with a plain Code-scoped PAT and reflects the token's real access.
	if orgName := gitx.TrimJS(org); orgName != "" {
		base := ProviderBaseURL(ProviderAzure, orgName)
		var data struct {
			AuthenticatedUser *struct {
				ProviderDisplayName *string `json:"providerDisplayName"`
				Properties          *struct {
					Account *struct {
						Value *string `json:"$value"`
					} `json:"Account"`
				} `json:"properties"`
			} `json:"authenticatedUser"`
		}
		err := c.withAzureRetry(ctx, base, auth, func(t string) error {
			return c.adoJSON(ctx, "GET", c.azureOrgAPI(orgName)+"/_apis/connectionData?api-version=7.1-preview", t, nil, &data)
		})
		if err != nil {
			var ae *azureError
			if errors.As(err, &ae) {
				return nil, errors.New(azureAuthError(ae.status, orgName))
			}
			return nil, err
		}
		var email, displayName *string
		if u := data.AuthenticatedUser; u != nil {
			displayName = u.ProviderDisplayName
			if u.Properties != nil && u.Properties.Account != nil {
				email = u.Properties.Account.Value
			}
		}
		login := orgName
		if email != nil {
			login = *email
		} else if displayName != nil {
			login = *displayName
		}
		return &ConnectedAccount{
			Login: login,
			Name:  displayName,
			Orgs:  []ConnectedOrg{{Login: orgName, URL: strPtr(webAzure + "/" + encodeURIComponent(orgName))}},
		}, nil
	}

	// No organization provided — fall back to the global profile service (best effort).
	var profile struct {
		DisplayName  string  `json:"displayName"`
		EmailAddress *string `json:"emailAddress"`
		ID           string  `json:"id"`
	}
	err := c.adoJSON(ctx, "GET", c.ep.AzureProfile+"/_apis/profile/profiles/me?api-version=7.1", auth.token, nil, &profile)
	if err != nil {
		var ae *azureError
		if errors.As(err, &ae) {
			return nil, errors.New(azureAuthError(ae.status, ""))
		}
		return nil, err
	}
	return &ConnectedAccount{Login: derefOr(profile.EmailAddress, profile.DisplayName), Name: strPtr(profile.DisplayName)}, nil
}

// jsWhitespaceRun is JavaScript's /\s+/.
var jsWhitespaceRun = regexp.MustCompile(`[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+`)

// createRepository creates a new repository on the host and returns its clone URL.
func (c *client) createRepository(ctx context.Context, provider, token string, opts CreateRepoOpts, org string) (*RemoteRepo, error) {
	if gitx.TrimJS(opts.Name) == "" {
		//lint:ignore ST1005 shown to the user verbatim, byte-identical to the TS message
		return nil, errors.New("Repository name is required.")
	}
	auth := c.tokenForProvider(ctx, provider, token, org)
	if auth == nil {
		return nil, c.noCredential(ctx, provider, ProviderBaseURL(provider, org))
	}

	switch provider {
	case ProviderGitHub:
		url := c.ep.GitHubAPI + "/user/repos"
		if opts.OwnerType == "org" {
			url = c.ep.GitHubAPI + "/orgs/" + encodeURIComponent(opts.Owner) + "/repos"
		}
		body := jsonStringify(jsObject{{"name", opts.Name}, {"description", orUndefined(opts.Description)}, {"private", opts.Private}})
		var repo struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
			Private  bool   `json:"private"`
		}
		if err := c.ghJSON(ctx, "POST", url, auth.token, &body, &repo); err != nil {
			return nil, err
		}
		return &RemoteRepo{Name: repo.FullName, URL: repo.CloneURL, Private: boolPtr(repo.Private)}, nil

	case ProviderGitLab:
		visibility := "public"
		if opts.Private {
			visibility = "private"
		}
		var namespace any = undefined
		if opts.OwnerID != "" {
			namespace = jsNumber(jsToNumber(opts.OwnerID))
		}
		body := jsonStringify(jsObject{
			{"name", opts.Name}, {"description", orUndefined(opts.Description)},
			{"visibility", visibility}, {"namespace_id", namespace},
		})
		res, err := c.fetch(ctx, "POST", c.glAPI()+"/projects",
			[]header{gitlabAuth(auth), {"Content-Type", "application/json"}}, &body)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			if m, ok := rawProp(res.body, "message"); ok && jsTruthy(decodeAny(m)) {
				return nil, errors.New("GitLab: " + stringifyRaw(m))
			}
			return nil, errors.New("GitLab: " + strconv.Itoa(res.status))
		}
		var repo struct {
			PathWithNamespace string `json:"path_with_namespace"`
			HTTPURLToRepo     string `json:"http_url_to_repo"`
			Visibility        string `json:"visibility"`
		}
		if err := res.json(&repo); err != nil {
			return nil, err
		}
		return &RemoteRepo{Name: repo.PathWithNamespace, URL: repo.HTTPURLToRepo, Private: boolPtr(repo.Visibility != "public")}, nil

	case ProviderBitbucket:
		slug := jsWhitespaceRun.ReplaceAllString(strings.ToLower(gitx.TrimJS(opts.Name)), "-")
		body := jsonStringify(jsObject{{"scm", "git"}, {"is_private", opts.Private}, {"description", orUndefined(opts.Description)}})
		res, err := c.fetch(ctx, "POST", c.ep.BitbucketAPI+"/2.0/repositories/"+opts.Owner+"/"+slug,
			[]header{{"Authorization", bitbucketAuthFor(auth)}, {"Content-Type", "application/json"}}, &body)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, errors.New("Bitbucket API error (" + strconv.Itoa(res.status) + ")")
		}
		var repo struct {
			FullName  string `json:"full_name"`
			IsPrivate bool   `json:"is_private"`
			Links     struct {
				Clone []bbLink `json:"clone"`
			} `json:"links"`
		}
		if err := res.json(&repo); err != nil {
			return nil, err
		}
		return &RemoteRepo{Name: repo.FullName, URL: httpsClone(repo.Links.Clone), Private: boolPtr(repo.IsPrivate)}, nil
	}

	// Azure DevOps — create a repo inside a project of the organization.
	if gitx.TrimJS(org) == "" {
		return nil, errNoAzureOrg
	}
	if gitx.TrimJS(opts.Project) == "" {
		//lint:ignore ST1005 shown to the user verbatim, byte-identical to the TS message
		return nil, errors.New("Select an Azure DevOps project.")
	}
	base := ProviderBaseURL(ProviderAzure, org)
	body := jsonStringify(jsObject{{"name", opts.Name}, {"project", jsObject{{"id", opts.Owner}}}})
	var repo struct {
		Name      string `json:"name"`
		RemoteURL string `json:"remoteUrl"`
		Project   struct {
			Name string `json:"name"`
		} `json:"project"`
	}
	err := c.withAzureRetry(ctx, base, auth, func(t string) error {
		return c.adoJSON(ctx, "POST", c.azureOrgAPI(org)+"/"+encodeURIComponent(opts.Project)+"/_apis/git/repositories?api-version=7.1",
			t, &body, &repo)
	})
	if err != nil {
		return nil, err
	}
	return &RemoteRepo{Name: repo.Project.Name + "/" + repo.Name, URL: repo.RemoteURL}, nil
}
