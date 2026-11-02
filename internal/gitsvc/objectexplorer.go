package gitsvc

// Object explorer. Everything git does is four object types and some refs
// pointing at them. These two reads expose that layer as it actually is — no
// porcelain, no interpretation, and nothing that can change a byte.
//
// Renderer channels served (through the 'git' dispatcher): objectRefs,
// gitObject.

import (
	"bytes"
	"context"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// bObjectBlobPreview is how much of a blob the object explorer shows before
// saying "truncated".
const bObjectBlobPreview = 200_000

// RefObject is RefObject in src/shared/types.ts.
type RefObject struct {
	// Name is the full ref name, e.g. `refs/heads/main`, or `HEAD`.
	Name string `json:"name"`
	Sha  string `json:"sha"`
	// Kind is the GitObjectKind the ref names — annotated tags point at a `tag`.
	Kind string `json:"kind"`
}

// gitsvc: objectRefs
//
// Every ref in the repository, plus HEAD, with what it points at.
func (s *GitService) ObjectRefs(ctx context.Context, repoPath string) ([]RefObject, error) {
	return locked(s, ctx, "objectRefs", repoPath, func(ctx context.Context) ([]RefObject, error) {
		out := bGitOr(ctx, repoPath, "", "for-each-ref",
			"--format=%(refname)"+gitx.SEP+"%(objectname)"+gitx.SEP+"%(objecttype)")
		refs := []RefObject{}
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(line, gitx.SEP)
			if len(f) < 2 || f[0] == "" || f[1] == "" {
				continue
			}
			kind := "commit"
			if len(f) > 2 {
				kind = f[2]
			}
			refs = append(refs, RefObject{Name: f[0], Sha: f[1], Kind: kind})
		}
		// HEAD is a ref too, and the one people start from — for-each-ref omits it.
		if head := gitx.TrimJS(bGitOr(ctx, repoPath, "", "rev-parse", "HEAD")); head != "" {
			refs = append([]RefObject{{Name: "HEAD", Sha: head, Kind: "commit"}}, refs...)
		}
		return refs, nil
	})
}

// TreeChild is TreeChild in src/shared/types.ts: one entry of a tree object.
type TreeChild struct {
	// Mode is six digits: 100644 file, 100755 executable, 040000 tree, 120000 symlink, 160000 gitlink.
	Mode string `json:"mode"`
	Kind string `json:"kind"`
	Sha  string `json:"sha"`
	Name string `json:"name"`
	// Size is the blob size in bytes; null for trees and submodule gitlinks.
	Size *int64 `json:"size"`
}

// GitObjectCommit is GitObject.commit.
type GitObjectCommit struct {
	Tree      string   `json:"tree"`
	Parents   []string `json:"parents"`
	Author    string   `json:"author"`
	Committer string   `json:"committer"`
	Message   string   `json:"message"`
}

// GitObjectTag is GitObject.tag.
type GitObjectTag struct {
	Object  string `json:"object"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Tagger  string `json:"tagger"`
	Message string `json:"message"`
}

// GitObjectBlob is GitObject.blob.
type GitObjectBlob struct {
	// Text is the decoded text, or null when the blob is binary.
	Text *string `json:"text"`
	// Truncated is true when only the head of the blob is here.
	Truncated bool `json:"truncated"`
}

// GitObject is GitObject in src/shared/types.ts: a raw object from the
// database, decoded enough to be walkable. Exactly one of the optional parts
// is set, by kind. Tree is a pointer so an empty tree still crosses as [].
type GitObject struct {
	Sha    string           `json:"sha"`
	Kind   string           `json:"kind"`
	Size   int64            `json:"size"`
	Rev    string           `json:"rev"`
	Commit *GitObjectCommit `json:"commit,omitempty"`
	Tag    *GitObjectTag    `json:"tag,omitempty"`
	Tree   *[]TreeChild     `json:"tree,omitempty"`
	Blob   *GitObjectBlob   `json:"blob,omitempty"`
}

// bHeaderValues is every `^<key> (.*)$` match of a multiline JS regex over an
// object header, trimmed: lines are split on all four JS line terminators.
func bHeaderValues(header, key string) []string {
	var out []string
	lines := strings.FieldsFunc(header, func(r rune) bool {
		return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029
	})
	for _, line := range lines {
		if strings.HasPrefix(line, key+" ") {
			out = append(out, gitx.TrimJS(line[len(key)+1:]))
		}
	}
	return out
}

func bHeaderField(header, key string) string {
	if v := bHeaderValues(header, key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// bSplitObjectBody splits `cat-file -p` output into its header and the
// message after it, as `body.split('\n\n')[0]` and
// `body.slice(header.length)` with its leading newlines stripped did.
func bSplitObjectBody(body string) (header, message string) {
	header = body
	if i := strings.Index(body, "\n\n"); i >= 0 {
		header = body[:i]
	}
	return header, strings.TrimLeft(body[len(header):], "\n")
}

// gitsvc: gitObject
//
// Resolve any revision expression to the object it names, decoded.
//
// `rev` is whatever `git rev-parse` accepts: a sha, `HEAD~3`, `main^{tree}`,
// `v1.0^{}`, or `HEAD:src/app.ts`. Being able to type those is half the point
// — the explorer is a way to learn the model, not just to browse it.
func (s *GitService) GitObject(ctx context.Context, repoPath, rev string) (GitObject, error) {
	return locked(s, ctx, "gitObject", repoPath, func(ctx context.Context) (GitObject, error) {
		wanted := gitx.TrimJS(rev)
		if wanted == "" {
			wanted = "HEAD"
		}
		shaOut, err := bGit(ctx, repoPath, "rev-parse", "--verify", wanted)
		if err != nil {
			return GitObject{}, err
		}
		sha := gitx.TrimJS(shaOut)
		kindOut, err := bGit(ctx, repoPath, "cat-file", "-t", sha)
		if err != nil {
			return GitObject{}, err
		}
		kind := gitx.TrimJS(kindOut)
		size := bJSInt(bGitOr(ctx, repoPath, "0", "cat-file", "-s", sha))
		object := GitObject{Sha: sha, Kind: kind, Size: size, Rev: wanted}

		if kind == "tree" {
			listing := bGitOr(ctx, repoPath, "", "ls-tree", "-l", "-z", sha)
			tree := []TreeChild{}
			for _, entry := range strings.Split(listing, "\x00") {
				if entry == "" {
					continue
				}
				// "<mode> <type> <sha> <size>\t<name>" — size is '-' for trees.
				parts := strings.Split(entry, "\t")
				name := ""
				if len(parts) > 1 {
					name = parts[1]
				}
				meta := bFields(strings.Join(bSplitWSKeepEmpty(gitx.TrimJS(parts[0])), " "), " ", 4)
				child := TreeChild{Mode: meta[0], Kind: meta[1], Sha: meta[2], Name: name}
				if raw := meta[3]; raw != "" && raw != "-" {
					n := bJSInt(raw)
					child.Size = &n
				}
				tree = append(tree, child)
			}
			object.Tree = &tree
			return object, nil
		}

		body := bGitOr(ctx, repoPath, "", "cat-file", "-p", sha)
		switch kind {
		case "commit":
			header, message := bSplitObjectBody(body)
			object.Commit = &GitObjectCommit{
				Tree:      bHeaderField(header, "tree"),
				Parents:   bNonNil(bHeaderValues(header, "parent")),
				Author:    bHeaderField(header, "author"),
				Committer: bHeaderField(header, "committer"),
				Message:   message,
			}
			return object, nil
		case "tag":
			header, message := bSplitObjectBody(body)
			object.Tag = &GitObjectTag{
				Object:  bHeaderField(header, "object"),
				Type:    bHeaderField(header, "type"),
				Name:    bHeaderField(header, "tag"),
				Tagger:  bHeaderField(header, "tagger"),
				Message: message,
			}
			return object, nil
		}

		// A blob: show the head of it, and say plainly when it is not text
		// rather than spraying control characters into the pane.
		res, err := gitx.Run(ctx, repoPath, []string{"cat-file", "-p", sha}, gitx.RunOpts{MaxBuffer: 16 * 1024 * 1024})
		buf := []byte(res.Stdout)
		if err != nil {
			buf = nil
		}
		head := buf
		if len(head) > bObjectBlobPreview {
			head = head[:bObjectBlobPreview]
		}
		blob := &GitObjectBlob{Truncated: len(buf) > len(head)}
		if bytes.IndexByte(head, 0) < 0 {
			text := bDecodeUTF8(string(head))
			blob.Text = &text
		}
		object.Blob = blob
		return object, nil
	})
}

// bSplitWSKeepEmpty is `s.split(/\s+/)`: unlike bSplitWS, an empty input is
// one empty field, as in JS.
func bSplitWSKeepEmpty(s string) []string {
	if s == "" {
		return []string{""}
	}
	return bSplitWS(s)
}
