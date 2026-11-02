package wiki

// Wire types from src/shared/types.ts ("Generated repo wiki"). JSON tags match
// the TS property names; slices are never nil on the way out.

// PagePlan is WikiPagePlan: one planned page, before anything is written.
type PagePlan struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	// Archetype is a WikiArchetype: 'overview' | 'module' | 'workflow' | 'reference'.
	Archetype string `json:"archetype"`
	// ScopePaths are the repo-relative files this page is about.
	ScopePaths []string `json:"scopePaths"`
}

// Claim is WikiClaim: one statement, with the files that back it up.
type Claim struct {
	Text        string   `json:"text"`
	SourcePaths []string `json:"sourcePaths"`
}

// Section is WikiSection.
type Section struct {
	Heading string  `json:"heading"`
	Claims  []Claim `json:"claims"`
}

// Page is WikiPage.
type Page struct {
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Archetype string    `json:"archetype"`
	Summary   string    `json:"summary"`
	Sections  []Section `json:"sections"`
	// Related are the slugs of other pages in the same wiki.
	Related []string `json:"related"`
	// Markdown is rendered by the app from the sections above.
	Markdown string `json:"markdown"`
}

// TechItem is one dependency, placed and explained by the model.
type TechItem struct {
	Dep  string `json:"dep"`
	Role string `json:"role"`
}

// TechGroup is TechGroup.
type TechGroup struct {
	Name  string     `json:"name"`
	Items []TechItem `json:"items"`
}

// TechStack is the model's reading of the declared dependencies.
type TechStack struct {
	Summary string      `json:"summary"`
	Groups  []TechGroup `json:"groups"`
}

// RepoWiki is a generated wiki for one repository, as stored on disk.
type RepoWiki struct {
	HeadSha       string `json:"headSha"`
	GeneratedAt   int64  `json:"generatedAt"`
	Model         string `json:"model"`
	PromptVersion string `json:"promptVersion"`
	Pages         []Page `json:"pages"`
	// Stack is the model's grouping of the dependencies the manifests declare.
	Stack *TechStack `json:"stack"`
}

// StoredWiki is wiki:get's answer.
type StoredWiki struct {
	Wiki      *RepoWiki `json:"wiki"`
	Freshness string    `json:"freshness"`
	// HeadSha is HEAD at the time of asking, so the UI can say how far behind
	// the wiki is.
	HeadSha string `json:"headSha"`
}

// Progress is WikiProgress, pushed on `wiki:progress` while a wiki is being
// generated: {phase:'planning'} | {phase:'page', slug, title, done, total} |
// {phase:'done'} | {phase:'error', message}.
type Progress struct {
	Phase   string `json:"phase"`
	Slug    string `json:"slug,omitempty"`
	Title   string `json:"title,omitempty"`
	Done    *int   `json:"done,omitempty"`
	Total   *int   `json:"total,omitempty"`
	Message string `json:"message,omitempty"`
}

// ProgressEvent is the `wiki:progress` payload.
type ProgressEvent struct {
	RepoPath string   `json:"repoPath"`
	Progress Progress `json:"progress"`
}

// normalized returns the page with nil slices replaced by empty ones.
func (p Page) normalized() Page {
	if p.Sections == nil {
		p.Sections = []Section{}
	}
	for i := range p.Sections {
		if p.Sections[i].Claims == nil {
			p.Sections[i].Claims = []Claim{}
		}
		for j := range p.Sections[i].Claims {
			if p.Sections[i].Claims[j].SourcePaths == nil {
				p.Sections[i].Claims[j].SourcePaths = []string{}
			}
		}
	}
	if p.Related == nil {
		p.Related = []string{}
	}
	return p
}
