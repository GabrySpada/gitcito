package gitsvc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// ─── Stacked branches ──────────────────────────────────────────────────────
// A "stack" is a chain of dependent branches where each is based on the one
// below it. The parent (and the parent tip last rebased onto) is persisted in
// git config under branch.<name>.gitcitoparent / .gitcitobase, so the metadata
// travels with the repo and never touches history.
//
// Stack methods call each other the way the TS methods did (unlocked, inside
// the caller's lock): the exported methods take the lock once and the c*
// functions do the work.

// RouteConflict is ROUTE_CONFLICT: the marker the renderer looks for, so a
// conflicted route edit can be worded in the user's language with the two
// branches in it.
const RouteConflict = "GITCITO_ROUTE_CONFLICT"

const (
	cParentKeyRe  = `^branch\..*\.gitcitoparent$`
	cBranchPrefix = "branch."
	cParentSuffix = ".gitcitoparent"
)

// cStackParentLine splits one `config --get-regexp` line into the branch name
// and the recorded parent; ok is false for a line without a space.
func cStackParentLine(line string) (name, parent string, ok bool) {
	sp := strings.IndexByte(line, ' ')
	if sp < 0 {
		return "", "", false
	}
	key := line[:sp] // branch.<name>.gitcitoparent
	return cJSSlice(key, len(cBranchPrefix), len(key)-len(cParentSuffix)), gitx.TrimJS(line[sp+1:]), true
}

// cJSSlice is String.prototype.slice(start, end) for byte offsets.
func cJSSlice(s string, start, end int) string {
	if start < 0 {
		start = max(len(s)+start, 0)
	}
	if end < 0 {
		end = max(len(s)+end, 0)
	}
	start = min(start, len(s))
	end = min(end, len(s))
	if start >= end {
		return ""
	}
	return s[start:end]
}

// cProtectedBranches is protectedBranches(): the per-repo protected list in
// `gitcito.protectedbranches` (comma-joined), main/master when never
// configured. It goes through runGit, not simple-git: `config --get` exits 1
// for a missing key, and simple-git reads that as an empty value — which
// would turn "never configured" into "nothing is protected".
func cProtectedBranches(ctx context.Context, repoPath string) []string {
	raw, err := gitx.RunGit(ctx, repoPath, []string{"config", "--get", "gitcito.protectedbranches"}, gitx.RunOpts{})
	if err != nil {
		return []string{"main", "master"} // never configured → sensible default
	}
	out := []string{}
	for _, b := range strings.Split(gitx.TrimJS(raw), ",") {
		if b = gitx.TrimJS(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// cRefuseProtected is the "Refusing to rebase protected …" guard.
func cRefuseProtected(guarded []string) error {
	if len(guarded) == 0 {
		return nil
	}
	noun := "branches"
	if len(guarded) == 1 {
		noun = "branch"
	}
	//lint:ignore ST1005 the message is shown to the user verbatim, as in TS
	return fmt.Errorf("Refusing to rebase protected %s: %s.", noun, strings.Join(guarded, ", "))
}

// StackSetParent records (or changes) a branch's stack parent, snapshotting
// the parent tip.
// gitsvc: stackSetParent
func (s *GitService) StackSetParent(ctx context.Context, repoPath, branch, parent string) error {
	return lockedErr(s, ctx, "stackSetParent", repoPath, func(ctx context.Context) error {
		return cStackSetParent(ctx, repoPath, branch, parent)
	})
}

func cStackSetParent(ctx context.Context, repoPath, branch, parent string) error {
	tip, err := cRevparse(ctx, repoPath, parent)
	if err != nil {
		return err
	}
	if _, err := cRaw(ctx, repoPath, "config", "branch."+branch+".gitcitoparent", parent); err != nil {
		return err
	}
	_, err = cRaw(ctx, repoPath, "config", "branch."+branch+".gitcitobase", tip)
	return err
}

// StackClearParent stops tracking a branch as part of a stack.
// gitsvc: stackClearParent
func (s *GitService) StackClearParent(ctx context.Context, repoPath, branch string) error {
	return lockedErr(s, ctx, "stackClearParent", repoPath, func(ctx context.Context) error {
		cStackClearParent(ctx, repoPath, branch)
		return nil
	})
}

func cStackClearParent(ctx context.Context, repoPath, branch string) {
	_, _ = cRaw(ctx, repoPath, "config", "--unset", "branch."+branch+".gitcitoparent")
	_, _ = cRaw(ctx, repoPath, "config", "--unset", "branch."+branch+".gitcitobase")
}

// StackInfo is the stack containing leaf (the current branch when empty):
// parent links walked down to the trunk, then each level reported bottom→top
// with its own commit count and whether its parent has moved (needs a
// restack).
// gitsvc: stackInfo
func (s *GitService) StackInfo(ctx context.Context, repoPath, leaf string) (StackInfo, error) {
	return locked(s, ctx, "stackInfo", repoPath, func(ctx context.Context) (StackInfo, error) {
		return cStackInfo(ctx, repoPath, leaf), nil
	})
}

func cStackInfo(ctx context.Context, repoPath, leaf string) StackInfo {
	empty := StackInfo{Trunk: "", Branches: []StackBranch{}}
	current := cRevparseOr(ctx, repoPath, "--abbrev-ref", "HEAD")
	head := leaf
	if head == "" {
		head = current
	}
	if head == "" || head == "HEAD" {
		return empty
	}

	// Read all recorded parents in one shot.
	parents := map[string]string{}
	for _, line := range strings.Split(cRawOr(ctx, repoPath, "", "config", "--get-regexp", cParentKeyRe), "\n") {
		if gitx.TrimJS(line) == "" {
			continue
		}
		if name, parent, ok := cStackParentLine(line); ok {
			parents[name] = parent
		}
	}

	// Walk down to the trunk, guarding against cycles.
	var chainTopDown []string
	seen := map[string]bool{}
	cur := head
	for cur != "" && parents[cur] != "" && !seen[cur] {
		seen[cur] = true
		chainTopDown = append(chainTopDown, cur)
		cur = parents[cur]
	}
	// A branch with no recorded parent is not on a stack. Saying so is the
	// honest answer: the alternative — a one-level stack whose trunk is the
	// branch itself — reads in the UI as "main lands on main", which is not a
	// thing, and invites edits to a route that does not exist.
	if len(chainTopDown) == 0 {
		return empty
	}
	trunk, ok := parents[chainTopDown[len(chainTopDown)-1]]
	if !ok {
		trunk = cur
	}

	branches := []StackBranch{}
	for i := len(chainTopDown) - 1; i >= 0; i-- { // bottom → top
		name := chainTopDown[i]
		b := StackBranch{Name: name, IsCurrent: name == current}
		if parent, ok := parents[name]; ok {
			b.Parent = &parent
		}
		if b.Parent != nil && *b.Parent != "" {
			parent := *b.Parent
			b.Ahead = cJSNumber(gitx.TrimJS(cRawOr(ctx, repoPath, "0", "rev-list", "--count", parent+".."+name)))
			parentTip := cRevparseOr(ctx, repoPath, parent)
			// Restack needed when the parent tip is not yet an ancestor of the
			// branch. (`merge-base --is-ancestor` answers with its exit code,
			// which the simple-git rules do not reliably fail on — so compare the
			// merge-base sha instead.)
			if parentTip != "" {
				base := gitx.TrimJS(cRawOr(ctx, repoPath, "", "merge-base", parentTip, name))
				b.NeedsRestack = base != parentTip
			}
		}
		branches = append(branches, b)
	}
	return StackInfo{Trunk: trunk, Branches: branches}
}

// cJSNumber is `Number(s)` for a count git printed: ” is 0, junk is 0 too
// (TS would carry NaN, which JSON writes as null — a count git never prints).
func cJSNumber(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// StackReorder re-links a whole stack to order (bottom → top, sitting on
// trunk) and replays it.
//
// Reordering is not "set some config": each level's commits have to move onto
// a different base, which is a rebase per level. What makes that survivable is
// the boundary snapshot below — a level's own commits are the ones after its
// recorded base, and once the parent links are rewritten the old merge-base is
// gone. So the boundary is written first, for every level that lacks one, and
// only then do the links change.
//
// A conflicting rebase fails the whole edit and leaves the repository as it
// was (cWithStackRollback).
// gitsvc: stackReorder
func (s *GitService) StackReorder(ctx context.Context, repoPath, trunk string, order []string) error {
	return lockedErr(s, ctx, "stackReorder", repoPath, func(ctx context.Context) error {
		return cStackReorder(ctx, repoPath, trunk, order)
	})
}

func cStackReorder(ctx context.Context, repoPath, trunk string, order []string) error {
	if len(order) == 0 {
		return nil
	}
	// Every stop in the route gets rebased. The branch the stack lands on is
	// not a stop, and neither is a protected branch — either one here would
	// quietly rewrite the history everyone else is working from.
	if cContains(order, trunk) {
		return errors.New(`"` + trunk + `" is where the stack lands — it cannot also be a stop on it.`)
	}
	if len(cUnique(order)) != len(order) {
		//lint:ignore ST1005 the message is shown to the user verbatim, as in TS
		return errors.New("A branch cannot be on the same stack twice.")
	}
	protected := cProtectedBranches(ctx, repoPath)
	var guarded []string
	for _, b := range order {
		if cContains(protected, b) {
			guarded = append(guarded, b)
		}
	}
	if err := cRefuseProtected(guarded); err != nil {
		return err
	}
	// Atomic: a reorder is a picker gesture, and one that half-lands and drops
	// the user into a mid-rebase is worse than one that refuses. The snapshot
	// is what makes "nothing happened" true after a conflict.
	return cWithStackRollback(ctx, repoPath, append(append([]string{}, order...), trunk), func(_ *[]string) error {
		for _, branch := range order {
			existing := gitx.TrimJS(cRawOr(ctx, repoPath, "", "config", "--get", "branch."+branch+".gitcitobase"))
			if existing != "" {
				continue
			}
			oldParent := gitx.TrimJS(cRawOr(ctx, repoPath, "", "config", "--get", "branch."+branch+".gitcitoparent"))
			if oldParent == "" {
				oldParent = trunk
			}
			base := gitx.TrimJS(cRawOr(ctx, repoPath, "", "merge-base", oldParent, branch))
			if base != "" {
				if _, err := cRaw(ctx, repoPath, "config", "branch."+branch+".gitcitobase", base); err != nil {
					return err
				}
			}
		}
		for i, branch := range order {
			parent := trunk
			if i > 0 {
				parent = order[i-1]
			}
			if _, err := cRaw(ctx, repoPath, "config", "branch."+branch+".gitcitoparent", parent); err != nil {
				return err
			}
		}
		return cStackRestack(ctx, repoPath, order[len(order)-1])
	})
}

// cUnique is `[...new Set(xs)]`: first occurrences, in order.
func cUnique(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// StackLeaves is the leaf of every stack in the repository: a branch that
// records a parent and that nothing else records as its parent.
//
// stackInfo answers for one branch, which is right when you are standing on a
// stack and useless when you are not — the modal opens on the trunk more often
// than not. This is how it finds the stacks that exist anyway.
// gitsvc: stackLeaves
func (s *GitService) StackLeaves(ctx context.Context, repoPath string) ([]string, error) {
	return locked(s, ctx, "stackLeaves", repoPath, func(ctx context.Context) ([]string, error) {
		var names []string
		parents := map[string]string{}
		for _, line := range strings.Split(cRawOr(ctx, repoPath, "", "config", "--get-regexp", cParentKeyRe), "\n") {
			name, parent, ok := cStackParentLine(line)
			if !ok || name == "" {
				continue
			}
			if _, dup := parents[name]; !dup {
				names = append(names, name)
			}
			parents[name] = parent
		}
		claimed := map[string]bool{}
		for _, p := range parents {
			claimed[p] = true
		}
		leaves := []string{}
		for _, n := range names {
			if !claimed[n] {
				leaves = append(leaves, n)
			}
		}
		sort.SliceStable(leaves, func(i, j int) bool { return cLocaleCompare(leaves[i], leaves[j]) < 0 })
		return leaves, nil
	})
}

// StackSetRoute sets the whole route in one move: order (bottom → top)
// sitting on trunk, and anything currently in the stack but missing from
// order untracked.
//
// The modal edits the stack as a list — swap a stop, drop one, change where it
// starts — and each of those is this call with a different list. Doing it in
// one operation is what keeps the undo entry honest: half a re-route is not a
// state the user asked for.
// gitsvc: stackSetRoute
func (s *GitService) StackSetRoute(ctx context.Context, repoPath, trunk string, order []string) error {
	return lockedErr(s, ctx, "stackSetRoute", repoPath, func(ctx context.Context) error {
		before := cStackInfo(ctx, repoPath, "")
		var dropped []string
		for _, b := range before.Branches {
			if !cContains(order, b.Name) {
				dropped = append(dropped, b.Name)
			}
		}
		all := append(append(append([]string{}, order...), dropped...), trunk)
		// The untracking is part of the same gesture, so it is inside the same
		// snapshot: a conflict half-way through the replay must not leave
		// levels detached from a stack that never changed.
		return cWithStackRollback(ctx, repoPath, cUnique(all), func(created *[]string) error {
			// A stop the user typed that is not a branch yet is created here, on
			// the tip of the stop below it — so "add a stop, then apply" is one
			// gesture and rolls back as one.
			for i, name := range order {
				exists := gitx.TrimJS(cRawOr(ctx, repoPath, "", "rev-parse", "--verify", "--quiet", "refs/heads/"+name))
				if exists != "" {
					continue
				}
				base := trunk
				if i > 0 {
					base = order[i-1]
				}
				if _, err := cRaw(ctx, repoPath, "branch", name, base); err != nil {
					return err
				}
				*created = append(*created, name)
			}
			for _, name := range dropped {
				cStackClearParent(ctx, repoPath, name)
			}
			if len(order) > 0 {
				return cStackReorder(ctx, repoPath, trunk, order)
			}
			return nil
		})
	})
}

// StackInsert adds a level directly above parent — `gh stack`'s `add`,
// including in the middle of a stack: whatever level sat on parent is
// re-pointed at the new branch, so the chain keeps its order and gains a
// floor.
//
// No restack follows, and that is deliberate: the branch is created at the
// parent's tip, so the level that moved onto it sees the same commits it saw
// before and has nothing to replay.
// gitsvc: stackInsert
func (s *GitService) StackInsert(ctx context.Context, repoPath, name, parent string) error {
	return lockedErr(s, ctx, "stackInsert", repoPath, func(ctx context.Context) error {
		child := ""
		for _, line := range strings.Split(cRawOr(ctx, repoPath, "", "config", "--get-regexp", cParentKeyRe), "\n") {
			n, p, ok := cStackParentLine(line)
			if !ok || p != parent {
				continue
			}
			child = n
			break
		}
		if _, err := cRaw(ctx, repoPath, "branch", name, parent); err != nil {
			return err
		}
		if err := cStackSetParent(ctx, repoPath, name, parent); err != nil {
			return err
		}
		// Only the link moves — the child's recorded base still marks where its
		// own commits start, and the new level's tip is the same commit it had
		// before.
		if child != "" && child != name {
			if _, err := cRaw(ctx, repoPath, "config", "branch."+child+".gitcitoparent", name); err != nil {
				return err
			}
		}
		return cCheckout(ctx, repoPath, name)
	})
}

var cConflictRe = regexp.MustCompile(`(?i)conflict`)

// StackRestack restacks the chain ending at leaf: bottom→top, each branch is
// rebased onto its parent's current tip using the recorded base (so parent
// rewrites don't duplicate commits). It leaves you on leaf, and fails on a
// conflict.
// gitsvc: stackRestack
func (s *GitService) StackRestack(ctx context.Context, repoPath, leaf string) error {
	return lockedErr(s, ctx, "stackRestack", repoPath, func(ctx context.Context) error {
		return cStackRestack(ctx, repoPath, leaf)
	})
}

func cStackRestack(ctx context.Context, repoPath, leaf string) error {
	info := cStackInfo(ctx, repoPath, leaf)
	// Defence in depth: a restack rebases every level it walks, and a parent
	// link that somehow names a protected branch would rewrite shared history
	// without anyone asking for it.
	protected := cProtectedBranches(ctx, repoPath)
	var guarded []string
	for _, b := range info.Branches {
		if b.Parent != nil && *b.Parent != "" && cContains(protected, b.Name) {
			guarded = append(guarded, b.Name)
		}
	}
	if err := cRefuseProtected(guarded); err != nil {
		return err
	}
	for _, b := range info.Branches {
		if b.Parent == nil || *b.Parent == "" {
			continue
		}
		parent := *b.Parent
		parentTip, err := cRevparse(ctx, repoPath, parent)
		if err != nil {
			return err
		}
		mergeBase := gitx.TrimJS(cRawOr(ctx, repoPath, "", "merge-base", parentTip, b.Name))
		if mergeBase == parentTip {
			// Parent tip already in this branch — nothing to replay.
			if _, err := cRaw(ctx, repoPath, "config", "branch."+b.Name+".gitcitobase", parentTip); err != nil {
				return err
			}
			continue
		}
		base := gitx.TrimJS(cRawOr(ctx, repoPath, "", "config", "--get", "branch."+b.Name+".gitcitobase"))
		if base == "" {
			mb, err := cRaw(ctx, repoPath, "merge-base", parent, b.Name)
			if err != nil {
				return err
			}
			base = gitx.TrimJS(mb)
		}
		// The 3-arg form checks out b.Name and rebases its commits since base
		// onto parentTip. A conflict here is the only failure a user meets
		// often, so it is named rather than left as git's own paragraph about
		// running `rebase --continue`.
		if _, err := cRaw(ctx, repoPath, "rebase", "--onto", parentTip, base, b.Name); err != nil {
			if !cConflictRe.MatchString(err.Error()) {
				return err
			}
			return errors.New(RouteConflict + ":" + b.Name + ":" + parent)
		}
		if _, err := cRaw(ctx, repoPath, "config", "branch."+b.Name+".gitcitobase", parentTip); err != nil {
			return err
		}
	}
	return cCheckout(ctx, repoPath, leaf)
}

// StackPruneMerged drops stack levels whose PR has landed: while the
// bottom-most tracked branch is already contained in the trunk (as seen by the
// last fetch when an origin/<trunk> exists), its child is reparented onto the
// trunk, it is untracked, and the branch is deleted (the current branch is
// left alone). Squash merges are invisible to the local ancestry check;
// callers pass branches with a host-verified merged PR in alsoMerged. It
// returns the pruned branch names, bottom first.
// gitsvc: stackPruneMerged
func (s *GitService) StackPruneMerged(ctx context.Context, repoPath string, alsoMerged []string, leaf string) ([]string, error) {
	return locked(s, ctx, "stackPruneMerged", repoPath, func(ctx context.Context) ([]string, error) {
		pruned := []string{}
		head := leaf
		for {
			info := cStackInfo(ctx, repoPath, head)
			if info.Trunk == "" || len(info.Branches) == 0 {
				break
			}
			bottom := info.Branches[0]
			trunkRef := info.Trunk
			if cRunGitOr(ctx, repoPath, "", "rev-parse", "--verify", "origin/"+info.Trunk) != "" {
				trunkRef = "origin/" + info.Trunk
			}
			// alsoMerged carries host-side proof (a merged PR) for squash merges,
			// which leave no ancestry a local git can see.
			merged := cContains(alsoMerged, bottom.Name)
			if !merged {
				_, err := gitx.RunGit(ctx, repoPath, []string{"merge-base", "--is-ancestor", bottom.Name, trunkRef}, gitx.RunOpts{})
				merged = err == nil
			}
			if !merged {
				break
			}
			if len(info.Branches) > 1 {
				if err := cStackSetParent(ctx, repoPath, info.Branches[1].Name, info.Trunk); err != nil {
					return nil, err
				}
			}
			cStackClearParent(ctx, repoPath, bottom.Name)
			// -D, not -d: git's own safety check measures against HEAD (the
			// leaf), which need not contain the bottom — the ancestor test above
			// already proved the trunk has everything this branch ever was.
			if !bottom.IsCurrent {
				_, _ = cRaw(ctx, repoPath, "branch", "-D", bottom.Name)
			}
			pruned = append(pruned, bottom.Name)
			// The leaf survives a prune of the bottom, so keep asking about the
			// same stack rather than falling back to whatever branch is checked
			// out.
			head = info.Branches[len(info.Branches)-1].Name
		}
		return pruned, nil
	})
}

// cStackSnapshot is one branch as cWithStackRollback found it.
type cStackSnapshot struct {
	branch, parent, base string
}

// cWithStackRollback runs a stack edit so that a failure leaves the
// repository exactly as it was.
//
// Reordering a stack is a series of rebases, and rebases conflict — two stops
// that touch the same lines cannot swap without a human. Git's own answer is
// to stop mid-rebase and wait, which is right for a rebase the user asked for
// by name and wrong for a dropdown they nudged: it strands them in a state
// they did not choose, with half the chain moved and the parent links already
// rewritten.
//
// So every branch this edit can touch is snapshotted first — tip and both
// config keys — and put back on the way out. What the user sees instead is a
// message naming the two branches that clash. fn appends the branches it
// creates to *created, so a rollback can take them away again.
func cWithStackRollback(ctx context.Context, repoPath string, branches []string, fn func(created *[]string) error) error {
	var names []string
	for _, b := range cUnique(branches) {
		if b != "" {
			names = append(names, b)
		}
	}
	head := cRevparseOr(ctx, repoPath, "--abbrev-ref", "HEAD")
	var created []string
	type tip struct{ branch, sha string }
	var tips []tip
	var saved []cStackSnapshot
	for _, b := range names {
		if sha := cRevparseOr(ctx, repoPath, b); sha != "" {
			tips = append(tips, tip{b, sha})
		}
		saved = append(saved, cStackSnapshot{
			branch: b,
			parent: gitx.TrimJS(cRawOr(ctx, repoPath, "", "config", "--get", "branch."+b+".gitcitoparent")),
			base:   gitx.TrimJS(cRawOr(ctx, repoPath, "", "config", "--get", "branch."+b+".gitcitobase")),
		})
	}
	err := fn(&created)
	if err == nil {
		// A route edit replays branches, and replaying checks them out.
		// Standing somewhere the user did not choose is its own kind of damage,
		// so the branch they were on is restored on the way out as well.
		if head != "" && head != "HEAD" {
			if now := cRevparseOr(ctx, repoPath, "--abbrev-ref", "HEAD"); now != head {
				_ = cCheckout(ctx, repoPath, head)
			}
		}
		return nil
	}
	// Order matters: end the rebase first, stand on the branch we started on,
	// and only then move refs — a ref moved under a checked-out branch leaves
	// the working tree describing a commit that is no longer there.
	_ = cRunGitOr(ctx, repoPath, "", "rebase", "--abort")
	if head != "" && head != "HEAD" {
		_ = cCheckout(ctx, repoPath, head)
	}
	for _, b := range created {
		_ = cRunGitOr(ctx, repoPath, "", "branch", "-D", b)
	}
	for _, t := range tips {
		if t.branch == head {
			_ = cRunGitOr(ctx, repoPath, "", "reset", "--hard", t.sha)
		} else {
			_ = cRunGitOr(ctx, repoPath, "", "update-ref", "refs/heads/"+t.branch, t.sha)
		}
	}
	for _, sv := range saved {
		for _, kv := range [][2]string{
			{"branch." + sv.branch + ".gitcitoparent", sv.parent},
			{"branch." + sv.branch + ".gitcitobase", sv.base},
		} {
			if kv[1] != "" {
				_, _ = cRaw(ctx, repoPath, "config", kv[0], kv[1])
			} else {
				_, _ = cRaw(ctx, repoPath, "config", "--unset", kv[0])
			}
		}
	}
	return err
}

// cLocaleCompare approximates String.prototype.localeCompare (ICU root
// collation) for the branch names it sorts: ignoring case first —
// punctuation, then digits, then letters — and only then lowercase before
// uppercase. Byte order would put `Zeta` before `alpha`; this does not.
func cLocaleCompare(a, b string) int {
	ka, kb := cCollationKey(a), cCollationKey(b)
	for i := 0; i < len(ka) && i < len(kb); i++ {
		if ka[i] != kb[i] {
			if ka[i] < kb[i] {
				return -1
			}
			return 1
		}
	}
	if len(ka) != len(kb) {
		if len(ka) < len(kb) {
			return -1
		}
		return 1
	}
	// Tertiary: the first case difference, lowercase first.
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if ra[i] != rb[i] {
			la, lb := cIsLower(ra[i]), cIsLower(rb[i])
			if la && !lb {
				return -1
			}
			if lb && !la {
				return 1
			}
			if ra[i] < rb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// cPunctuationOrder is the ICU root order of ASCII punctuation and symbols,
// all of which sort before digits and letters.
const cPunctuationOrder = "_-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$"

func cCollationKey(s string) []int {
	key := make([]int, 0, len(s))
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t':
			key = append(key, 0)
		case strings.ContainsRune(cPunctuationOrder, r):
			key = append(key, 10+strings.IndexRune(cPunctuationOrder, r))
		case r >= '0' && r <= '9':
			key = append(key, 100+int(r-'0'))
		case r >= 'a' && r <= 'z':
			key = append(key, 200+int(r-'a'))
		case r >= 'A' && r <= 'Z':
			key = append(key, 200+int(r-'A'))
		default:
			key = append(key, 1000+int(r))
		}
	}
	return key
}

func cIsLower(r rune) bool { return r >= 'a' && r <= 'z' }
