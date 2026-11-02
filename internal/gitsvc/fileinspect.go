package gitsvc

// File inspection: the file view, blame and per-file history.
//
// Renderer channels served (through the 'git' dispatcher): fileContent,
// fileDataUrl, blameFile, fileHistory.

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: fileContent
//
// ref "" (undefined in TS) reads the working tree. Without force, a file over
// gitx.TextMaxBytes is refused with the FILE_TOO_LARGE marker the renderer
// turns into "load anyway".
func (s *GitService) FileContent(ctx context.Context, repoPath, file, ref string, force bool) (string, error) {
	return locked(s, ctx, "fileContent", repoPath, func(ctx context.Context) (string, error) {
		if !force {
			if err := gitx.AssertUnderCap(ctx, repoPath, file, ref, gitx.TextMaxBytes); err != nil {
				return "", err
			}
		}
		onDisk := filepath.Join(repoPath, file)
		if ref == "" {
			// Working-tree read; empty if the file was deleted on disk.
			text, err := bReadText(onDisk)
			if err != nil {
				return "", nil
			}
			return text, nil
		}
		if out, err := bRaw(ctx, repoPath, "show", ref+":"+file); err == nil {
			return out, nil
		}
		// The ref view may be missing — e.g. a staged deletion isn't in the
		// index (':0'). Fall back to the on-disk copy, then the last committed
		// version, so File view shows something instead of a fatal error.
		if text, err := bReadText(onDisk); err == nil {
			return text, nil
		}
		return bRawOr(ctx, repoPath, "", "show", "HEAD:"+file), nil
	})
}

// gitsvc: fileDataUrl
func (s *GitService) FileDataUrl(ctx context.Context, repoPath, file, ref string, force bool) (string, error) {
	return locked(s, ctx, "fileDataUrl", repoPath, func(ctx context.Context) (string, error) {
		if !force {
			if err := gitx.AssertUnderCap(ctx, repoPath, file, ref, gitx.PreviewMaxBytes); err != nil {
				return "", err
			}
		}
		url, ok := gitx.ReadFileDataURL(ctx, repoPath, file, ref)
		if !ok {
			return "", bErr("Cannot read image: " + file)
		}
		return url, nil
	})
}

// BlameLine is BlameLine in src/shared/types.ts.
type BlameLine struct {
	Sha    string `json:"sha"`
	Author string `json:"author"`
	Date   int64  `json:"date"`
	LineNo int    `json:"lineNo"`
	Text   string `json:"text"`
}

var bBlameShaRe = regexp.MustCompile(`^[0-9a-f]{40} `)

// gitsvc: blameFile
func (s *GitService) BlameFile(ctx context.Context, repoPath, file, ref string) ([]BlameLine, error) {
	return locked(s, ctx, "blameFile", repoPath, func(ctx context.Context) ([]BlameLine, error) {
		args := []string{"blame", "--line-porcelain"}
		if ref != "" {
			args = append(args, ref)
		}
		out, err := bRaw(ctx, repoPath, append(args, "--", file)...)
		if err != nil {
			return nil, err
		}
		result := []BlameLine{}
		sha, author := "", ""
		var date int64
		lineNo := 1
		for _, l := range strings.Split(out, "\n") {
			switch {
			case bBlameShaRe.MatchString(l):
				sha = l[:40]
			case strings.HasPrefix(l, "author "):
				author = l[7:]
			case strings.HasPrefix(l, "author-time "):
				date = bJSInt(l[12:])
			case strings.HasPrefix(l, "\t"):
				result = append(result, BlameLine{Sha: sha, Author: author, Date: date, LineNo: lineNo, Text: l[1:]})
				lineNo++
			}
		}
		return result, nil
	})
}

// FileHistoryEntry is FileHistoryEntry in src/shared/types.ts.
type FileHistoryEntry struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    int64  `json:"date"`
	Subject string `json:"subject"`
}

// gitsvc: fileHistory
func (s *GitService) FileHistory(ctx context.Context, repoPath, file string) ([]FileHistoryEntry, error) {
	return locked(s, ctx, "fileHistory", repoPath, func(ctx context.Context) ([]FileHistoryEntry, error) {
		out, err := bRaw(ctx, repoPath, "log", "--follow", "--max-count=200",
			"--pretty=format:%H"+gitx.SEP+"%an"+gitx.SEP+"%at"+gitx.SEP+"%s", "--", file)
		if err != nil {
			return nil, err
		}
		entries := []FileHistoryEntry{}
		for _, line := range gitx.SplitLines(out) {
			f := bFields(line, gitx.SEP, 4)
			entries = append(entries, FileHistoryEntry{Hash: f[0], Author: f[1], Date: bJSInt(f[2]), Subject: f[3]})
		}
		return entries, nil
	})
}

// bFields is `s.split(sep)` destructured into n names: missing fields are ""
// (undefined in TS, which the git formats here never produce).
func bFields(s, sep string, n int) []string {
	parts := strings.Split(s, sep)
	for len(parts) < n {
		parts = append(parts, "")
	}
	return parts
}
