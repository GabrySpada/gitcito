package gitsvc

import (
	"context"
	"errors"

	"github.com/GabrySpada/gitcito/internal/fileactions"
)

// The apply half of src/main/repoFileActions.ts as a gitService method: a
// repository-chat file batch that the main process already prepared
// (validated, hashed, previewed) is revalidated and installed under the git
// write lock, all or nothing. The logic lives in internal/fileactions.

// ApplyRepoFileActions applies a validated repository-chat file batch under
// the git write lock. A refusal is a result ({ ok: false, error }), not an
// error; anything unexpected is an error.
// gitsvc: applyRepoFileActions
func (s *GitService) ApplyRepoFileActions(ctx context.Context, repoPath string, actions []PreparedRepoChatFileAction) (RepoFileBatchResult, error) {
	return locked(s, ctx, "applyRepoFileActions", repoPath, func(ctx context.Context) (RepoFileBatchResult, error) {
		paths := []string{}
		for _, a := range actions {
			paths = append(paths, a.Path)
		}
		ignoredList, err := cIgnoredTrackedFiles(ctx, repoPath, cUnique(paths))
		if err != nil {
			return RepoFileBatchResult{}, err
		}
		ignored := map[string]bool{}
		for _, p := range ignoredList {
			ignored[p] = true
		}
		applied, err := fileactions.Apply(repoPath, actions, ignored, nil)
		if err != nil {
			var fe *fileactions.Error
			if errors.As(err, &fe) {
				p := fe.Paths
				if p == nil {
					p = []string{}
				}
				return RepoFileBatchResult{Ok: false, Error: &RepoFileBatchError{Code: fe.Code, Detail: fe.Message, Paths: p}}, nil
			}
			return RepoFileBatchResult{}, err
		}
		return RepoFileBatchResult{Ok: true, Applied: &applied}, nil
	})
}

// cJSReplaceFirst is `s.replace(search, replacement)` with a string pattern,
// under the name c_ops_test.go exercises.
func cJSReplaceFirst(s, search, replacement string) string {
	return fileactions.ReplaceFirst(s, search, replacement)
}
