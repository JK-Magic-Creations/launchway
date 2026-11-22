package pulls

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// NotReadyError is returned by Merge when the rules are not met: Why says in
// one sentence what is missing.
type NotReadyError struct{ Readiness Readiness }

func (e *NotReadyError) Error() string {
	return "this pull request cannot be merged yet: " + e.Readiness.Why
}

// Unwrap lets callers test errors.Is(err, ErrConflict).
func (e *NotReadyError) Unwrap() error { return ErrConflict }

// ExpandTemplate fills a commit message template. The tokens are {number},
// {title}, {body}, {source}, {target}, {author} and {repository}; any other
// text stays as written, including braces that are not a token.
func ExpandTemplate(tpl string, p PullRequest, repoPath string) string {
	r := strings.NewReplacer("{number}", strconv.Itoa(p.Number), "{title}", p.Title, "{body}", p.Body, "{source}", p.SourceBranch,
		"{target}", p.TargetBranch, "{author}", p.Author, "{repository}", repoPath)
	return strings.TrimRight(r.Replace(tpl), " \n") + "\n"
}

// MergeInput is a request to merge.
type MergeInput struct {
	// Method is merge, squash or rebase; empty is the repository's default.
	Method string
	// Message, when set, is the whole commit message (merge and squash).
	Message string
	// DeleteBranch overrides the repository's setting for this merge.
	DeleteBranch *bool
	// ExpectHead is the head the person was looking at: a merge of a pull
	// request whose head moved since is refused.
	ExpectHead string
	// Bypass is the reason an administrator gives for merging past the rules.
	Bypass string
	// Actor and ActorRole say who merges and with what role on the repository.
	Actor, ActorRole string
	// RepoPath is the repository's path (owner/name).
	RepoPath string
	// Queue says the merge comes from the merge queue, the only way into a
	// branch whose rule asks for one (an administrator's bypass excepted).
	Queue bool
}

// MergeResult is what a merge did.
type MergeResult struct {
	Pull PullRequest `json:"pull"`
	// Commit is the new tip of the target branch.
	Commit string `json:"commit"`
	// Updates are the refs that moved, so callers can tell webhooks and
	// pipelines (a merge is a push to the target).
	Updates       []RefUpdate `json:"-"`
	BranchDeleted bool        `json:"branch_deleted"`
	// Bypassed lists what the rules asked that an administrator waived.
	Bypassed []string `json:"bypassed,omitempty"`
}

const mergeAttempts = 5

// Merge lands a pull request on its target branch. It is made on the server
// through the ledger's compare-and-swap: if a push reaches the target while
// the merge is being made, the swap fails and the merge is made again on the
// new tip (up to five times, then it fails cleanly and says so); a push is
// never lost, and a merge never half-lands.
func (s *Service) Merge(ctx context.Context, repoID string, p PullRequest, in MergeInput) (MergeResult, error) {
	p, err := s.Store.ByID(ctx, p.ID)
	if err != nil {
		return MergeResult{}, err
	}
	g, err := s.mergeGate(ctx, repoID, p, in)
	if err != nil {
		return MergeResult{}, err
	}
	merged, ups, err := s.land(ctx, repoID, p, in, g)
	if err != nil {
		return MergeResult{}, err
	}
	return s.recordMerge(ctx, repoID, p, in, g, merged, ups)
}

// gate is what a merge was allowed to do.
type gate struct {
	method       string
	set          Settings
	rule         Rule
	covered      bool
	deleteBranch bool
	bypassed     []string
}

// mergeGate decides whether a merge may be made at all: the pull request is
// open and is the one the person saw, the method is allowed, a fresh judgment
// says it merges, and the rules are met or an administrator waives them.
func (s *Service) mergeGate(ctx context.Context, repoID string, p PullRequest, in MergeInput) (gate, error) {
	var g gate
	if p.State != Open {
		return g, conflict("this pull request is already %s", p.State)
	}
	if in.ExpectHead != "" && in.ExpectHead != p.HeadSHA {
		return g, conflict("the pull request changed while you were looking at it (new commits arrived); look again before merging")
	}
	if s.MergeCheck != nil {
		if err := s.MergeCheck(ctx, repoID, p, in.Actor); err != nil {
			return g, err
		}
	}
	set, err := s.Store.Settings(ctx, repoID)
	if err != nil {
		return g, err
	}
	g.set, g.method = set, in.Method
	if g.method == "" {
		g.method = set.DefaultMethod
	}
	if !set.allows(g.method) {
		return g, invalid("this repository does not allow merging by %s; it allows %s", g.method, strings.Join(set.Allowed(), ", "))
	}
	// A judgment made for these very tips, now, not the stored one: the one
	// thing that must never be stale is "it merges cleanly".
	m, err := s.Judge(ctx, repoID, p)
	if err != nil {
		return g, err
	}
	p.Mergeability = m
	ready, err := s.Readiness(ctx, repoID, in.RepoPath, p, in.ActorRole)
	if err != nil {
		return g, err
	}
	if !ready.CanMerge {
		if !ready.CanBypass {
			return g, &NotReadyError{Readiness: ready}
		}
		if strings.TrimSpace(in.Bypass) == "" {
			return g, invalid("an administrator may merge past these rules, but must say why: %s", ready.Why)
		}
		for _, b := range ready.Blockers {
			g.bypassed = append(g.bypassed, b.Why)
		}
	}
	g.rule, g.covered = RuleFor(mustRules(ctx, s.Store, repoID), p.TargetBranch)
	if g.covered && g.rule.MergeQueue && !in.Queue && strings.TrimSpace(in.Bypass) == "" {
		return g, conflict("%s merges through the merge queue; add the pull request to the queue instead of merging it", p.TargetBranch)
	}
	g.deleteBranch = set.DeleteBranch
	if in.DeleteBranch != nil {
		g.deleteBranch = *in.DeleteBranch
	}
	return g, nil
}

// land makes the merge commit and moves the refs, trying again on a new tip
// when a push got there first.
func (s *Service) land(ctx context.Context, repoID string, p PullRequest, in MergeInput, g gate) (string, []storage.RefUpdate, error) {
	var merged string
	var ups []storage.RefUpdate
	var lastErr error
	for attempt := 0; attempt < mergeAttempts; attempt++ {
		ups = nil
		_, lastErr = s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
			tgt, src := branchRef(p.TargetBranch), branchRef(p.SourceBranch)
			base, head := refs[tgt], refs[src]
			switch {
			case base == "":
				return nil, conflict("the target branch %s no longer exists", p.TargetBranch)
			case head == "":
				return nil, conflict("the source branch %s no longer exists; reopen it by pushing it again", p.SourceBranch)
			case head != p.HeadSHA:
				return nil, conflict("the branch %s moved since this pull request last looked at it; look again before merging", p.SourceBranch)
			}
			gr := s.runner(scratch, true)
			if g.covered && g.rule.RequireUpToDate && len(g.bypassed) == 0 {
				if up, err := gr.isAncestor(ctx, base, head); err != nil {
					return nil, err
				} else if !up {
					return nil, conflict("%s moved while merging and this pull request must be up to date with it; update the branch", p.TargetBranch)
				}
			}
			if s.beforeCommit != nil {
				s.beforeCommit()
			}
			var err error
			merged, err = s.makeMerge(ctx, gr, p, g.method, base, head, in, g.set)
			if err != nil {
				return nil, err
			}
			ups = []storage.RefUpdate{{Name: tgt, Old: base, New: merged}}
			if g.deleteBranch && s.canDeleteSource(ctx, repoID, p) {
				ups = append(ups, storage.RefUpdate{Name: src, Old: head, New: strings.Repeat("0", 40)})
			}
			return ups, nil
		})
		var stale *storage.ErrStale
		if !errors.As(lastErr, &stale) {
			break
		}
		s.Log.Info("a push reached the target while a pull request was being merged; merging again", "pull", p.Number, "attempt", attempt+1)
	}
	var stale *storage.ErrStale
	if errors.As(lastErr, &stale) {
		return "", nil, conflict("%s kept moving while merging; nothing was changed, try again in a moment", p.TargetBranch)
	}
	return merged, ups, lastErr
}

// recordMerge marks the pull request merged, notes a bypass on it, and tells
// the rest of the system.
func (s *Service) recordMerge(ctx context.Context, repoID string, p PullRequest, in MergeInput, g gate, merged string, ups []storage.RefUpdate) (MergeResult, error) {
	res := MergeResult{Bypassed: g.bypassed, Commit: merged}
	if err := s.Store.SetMerged(ctx, p.ID, merged, g.method, in.Actor); err != nil {
		return MergeResult{}, err
	}
	for _, u := range ups {
		res.Updates = append(res.Updates, RefUpdate{Name: u.Name, Old: u.Old, New: u.New})
		if zero(u.New) {
			res.BranchDeleted = true
		}
	}
	if len(res.Bypassed) > 0 {
		note := fmt.Sprintf("Merged by @%s past the rules (%s). Reason: %s", in.Actor, strings.Join(res.Bypassed, "; "), strings.TrimSpace(in.Bypass))
		_, _ = s.Store.AddComment(ctx, p.ID, Comment{Author: in.Actor, Body: note})
	}
	res.Pull, _ = s.Store.ByID(ctx, p.ID)
	detail := map[string]string{"method": g.method, "commit": merged, "branch_deleted": fmt.Sprint(res.BranchDeleted)}
	if len(res.Bypassed) > 0 {
		detail["bypass"] = strings.TrimSpace(in.Bypass)
	}
	s.emit(ctx, "merged", repoID, res.Pull, in.Actor, detail)
	// The target moved: other pull requests into it are brought up to date.
	s.Pushed(ctx, repoID, in.Actor, res.Updates)
	return res, nil
}

func mustRules(ctx context.Context, st *Store, repoID string) []Rule {
	r, _ := st.Rules(ctx, repoID)
	return r
}

// canDeleteSource says whether the source branch may go with the merge: not
// the default or a protected branch, and not the source of another open pull
// request.
func (s *Service) canDeleteSource(ctx context.Context, repoID string, p PullRequest) bool {
	rules := mustRules(ctx, s.Store, repoID)
	if _, protected := RuleFor(rules, p.SourceBranch); protected {
		return false
	}
	if others, err := s.Store.OpenFromSource(ctx, repoID, p.SourceBranch); err != nil || len(others) > 1 {
		return false
	}
	if p.SourceBranch == p.TargetBranch {
		return false
	}
	return true
}

// makeMerge makes the new tip of the target for a method, from the live tips.
func (s *Service) makeMerge(ctx context.Context, g gitRunner, p PullRequest, method, base, head string, in MergeInput, set Settings) (string, error) {
	if contained, err := g.isAncestor(ctx, head, base); err != nil {
		return "", err
	} else if contained {
		return "", conflict("everything in %s is already in %s; there is nothing to merge", p.SourceBranch, p.TargetBranch)
	}
	g.env = ident(in.Actor, s.now())
	switch method {
	case MethodMerge, MethodSquash:
		tree, files, clean, err := g.trial(ctx, base, head)
		if err != nil {
			return "", err
		}
		if !clean {
			return "", conflict("it has conflicts in %s; update the branch and resolve them", strings.Join(files, ", "))
		}
		msg := in.Message
		parents := []string{"-p", base, "-p", head}
		if method == MethodSquash {
			parents = []string{"-p", base}
			if msg == "" {
				msg = ExpandTemplate(set.SquashTemplate, p, in.RepoPath)
			}
			msg = strings.TrimRight(msg, "\n") + s.coAuthors(ctx, g, base, head, in.Actor)
		} else if msg == "" {
			msg = ExpandTemplate(set.MergeTemplate, p, in.RepoPath)
		}
		args := append([]string{"commit-tree", tree}, parents...)
		return g.out(ctx, append(args, "-m", strings.TrimRight(msg, "\n")+"\n")...)
	case MethodRebase:
		return s.rebase(ctx, g, base, head, in.Actor)
	}
	return "", invalid("unknown merge method %q", method)
}

// coAuthors is the trailer lines naming the other authors of a squashed pull
// request, so the squash credits everyone.
func (s *Service) coAuthors(ctx context.Context, g gitRunner, base, head, actor string) string {
	cs, err := g.commits(ctx, base, head, 200)
	if err != nil {
		return ""
	}
	seen := map[string]bool{}
	var lines []string
	for _, c := range cs {
		k := c.Author + " <" + c.Email + ">"
		if !seen[k] {
			seen[k] = true
			lines = append(lines, "Co-authored-by: "+k)
		}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(lines, "\n")
}

// rebase replays the pull request's commits on the target's tip, one by one,
// keeping each commit's author, date and message. When the target is already
// contained in the head it is a plain fast-forward.
func (s *Service) rebase(ctx context.Context, g gitRunner, base, head, actor string) (string, error) {
	if up, err := g.isAncestor(ctx, base, head); err != nil {
		return "", err
	} else if up {
		return head, nil
	}
	out, err := g.raw(ctx, "", "rev-list", "--reverse", "--topo-order", "--parents", head, "^"+base)
	if err != nil {
		return "", err
	}
	cur := base
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return "", conflict("this pull request contains a merge commit, which cannot be rebased; merge or squash it instead")
		}
		c, parent := f[0], f[1]
		tree, files, clean, err := g.trial(ctx, cur, c, "--merge-base="+parent)
		if err != nil {
			return "", err
		}
		if !clean {
			return "", conflict("rebasing commit %s conflicts in %s; update the branch and resolve them", c[:10], strings.Join(files, ", "))
		}
		meta, err := g.raw(ctx, "", "show", "-s", "--format=%an%x00%ae%x00%aI%x00%B", c)
		if err != nil {
			return "", err
		}
		m := strings.SplitN(meta, "\x00", 4)
		if len(m) < 4 {
			return "", fmt.Errorf("unexpected commit data for %s", c)
		}
		env := append(ident(actor, s.now())[3:], "GIT_AUTHOR_NAME="+m[0], "GIT_AUTHOR_EMAIL="+m[1], "GIT_AUTHOR_DATE="+m[2])
		cg := g
		cg.env = env
		cur, err = cg.out(ctx, "commit-tree", tree, "-p", cur, "-m", strings.TrimRight(m[3], "\n")+"\n")
		if err != nil {
			return "", err
		}
	}
	return cur, nil
}

// Close closes a pull request without merging it.
func (s *Service) Close(ctx context.Context, repoID string, p PullRequest, actor string) error {
	if p.State != Open {
		return conflict("this pull request is already %s", p.State)
	}
	if err := s.Store.SetState(ctx, p.ID, Closed, actor); err != nil {
		return err
	}
	np, _ := s.Store.ByID(ctx, p.ID)
	s.emit(ctx, "closed", repoID, np, actor, nil)
	return nil
}

// Reopen opens a closed pull request again, at the source branch's tip now.
func (s *Service) Reopen(ctx context.Context, repoID string, p PullRequest, actor string) error {
	switch p.State {
	case Merged:
		return conflict("a merged pull request cannot be reopened; revert it, or open a new one")
	case Open:
		return conflict("this pull request is already open")
	}
	refs, err := s.Git.Refs(ctx, repoID)
	if err != nil {
		return err
	}
	head, base := refs[branchRef(p.SourceBranch)], refs[branchRef(p.TargetBranch)]
	switch {
	case head == "":
		return conflict("the branch %s no longer exists; push it again to reopen this pull request", p.SourceBranch)
	case base == "":
		return conflict("the target branch %s no longer exists; choose another target first", p.TargetBranch)
	}
	if dup, _ := s.Store.OpenFromSource(ctx, repoID, p.SourceBranch); len(dup) > 0 {
		for _, d := range dup {
			if d.TargetBranch == p.TargetBranch {
				return conflict("pull request #%d already goes from %s to %s", d.Number, p.SourceBranch, p.TargetBranch)
			}
		}
	}
	if err := s.Store.SetState(ctx, p.ID, Open, actor); err != nil {
		return err
	}
	np, _ := s.Store.ByID(ctx, p.ID)
	s.syncTips(ctx, repoID, np, head, base, actor)
	np, _ = s.Store.ByID(ctx, p.ID)
	s.emit(ctx, "reopened", repoID, np, actor, nil)
	return nil
}

// syncTips brings a pull request's recorded head and base to the live ones.
func (s *Service) syncTips(ctx context.Context, repoID string, p PullRequest, head, base, actor string) {
	if p.HeadSHA != head {
		s.headMoved(ctx, repoID, p, head, actor)
		p, _ = s.Store.ByID(ctx, p.ID)
	}
	if p.BaseSHA != base {
		s.baseMoved(ctx, repoID, p, base, actor)
		return
	}
	s.schedule(ctx, repoID, p.ID)
}

// Retarget changes the branch a pull request goes into.
func (s *Service) Retarget(ctx context.Context, repoID string, p PullRequest, target, actor string) error {
	if p.State != Open {
		return conflict("this pull request is %s", p.State)
	}
	if target == p.TargetBranch {
		return nil
	}
	if target == p.SourceBranch {
		return invalid("%s is the pull request's own branch", target)
	}
	refs, err := s.Git.Refs(ctx, repoID)
	if err != nil {
		return err
	}
	base := refs[branchRef(target)]
	if base == "" {
		return invalid("the branch %s does not exist", target)
	}
	if dup, _ := s.Store.OpenFromSource(ctx, repoID, p.SourceBranch); len(dup) > 0 {
		for _, d := range dup {
			if d.TargetBranch == target {
				return conflict("pull request #%d already goes from %s to %s", d.Number, p.SourceBranch, target)
			}
		}
	}
	var mb string
	var contained bool
	if err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		if contained, err = g.isAncestor(ctx, p.HeadSHA, base); err != nil {
			return err
		}
		mb, err = g.mergeBase(ctx, base, p.HeadSHA)
		return err
	}); err != nil {
		return err
	}
	if contained {
		return invalid("everything in %s is already in %s; there would be nothing to merge", p.SourceBranch, target)
	}
	if err := s.Store.Apply(ctx, p.ID, Edit{Target: &target}); err != nil {
		return err
	}
	if err := s.Store.MoveTips(ctx, p.ID, p.HeadSHA, base, mb); err != nil {
		return err
	}
	_ = s.Store.AddRevision(ctx, p.ID, p.HeadSHA, base, false, actor)
	np, _ := s.Store.ByID(ctx, p.ID)
	s.schedule(ctx, repoID, p.ID)
	s.emit(ctx, "edited", repoID, np, actor, map[string]string{"changes": "base", "from": p.TargetBranch, "to": target})
	return nil
}

// Edit changes the title, description, draft state or labels.
func (s *Service) Edit(ctx context.Context, repoID string, p PullRequest, e Edit, actor string) (PullRequest, error) {
	if p.State == Merged {
		return p, conflict("a merged pull request cannot be edited; comment on it instead")
	}
	if e.Target != nil {
		return p, invalid("use retarget to change the target branch")
	}
	if err := s.Store.Apply(ctx, p.ID, e); err != nil {
		return p, err
	}
	np, err := s.Store.ByID(ctx, p.ID)
	if err != nil {
		return np, err
	}
	switch {
	case e.Draft != nil && *e.Draft && !p.Draft:
		s.emit(ctx, "converted_to_draft", repoID, np, actor, nil)
	case e.Draft != nil && !*e.Draft && p.Draft:
		s.emit(ctx, "ready_for_review", repoID, np, actor, nil)
		s.AutoAssign(ctx, repoID, np, "system")
		s.autoMerge(ctx, repoID, p.ID)
	default:
		s.emit(ctx, "edited", repoID, np, actor, nil)
	}
	return np, nil
}

// Revert makes a new pull request that undoes a merged one: a commit on a new
// branch that restores what the merge changed, opened against the same target.
func (s *Service) Revert(ctx context.Context, repoID string, p PullRequest, actor string) (PullRequest, error) {
	if p.State != Merged || p.MergeCommit == "" {
		return PullRequest{}, conflict("only a merged pull request can be reverted")
	}
	if p.MergeMethod == "manual" {
		return PullRequest{}, conflict("this pull request was merged by hand; revert the commits yourself")
	}
	branch := ""
	var made string
	_, err := s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		tip := refs[branchRef(p.TargetBranch)]
		if tip == "" {
			return nil, conflict("the target branch %s no longer exists", p.TargetBranch)
		}
		for i := 1; ; i++ {
			branch = fmt.Sprintf("revert-pr-%d", p.Number)
			if i > 1 {
				branch += "-" + strconv.Itoa(i)
			}
			if refs[branchRef(branch)] == "" {
				break
			}
		}
		g := s.runner(scratch, true)
		undo := p.MergeCommit + "^1"
		if p.MergeMethod == MethodRebase {
			n, err := g.out(ctx, "rev-list", "--count", p.MergeBase+".."+p.HeadSHA)
			if err != nil {
				return nil, err
			}
			undo = p.MergeCommit + "~" + n
		}
		tree, files, clean, err := g.trial(ctx, tip, undo, "--merge-base="+p.MergeCommit)
		if err != nil {
			return nil, err
		}
		if !clean {
			return nil, conflict("the merge cannot be reverted automatically: later changes touch %s; revert it by hand", strings.Join(files, ", "))
		}
		g.env = ident(actor, s.now())
		msg := fmt.Sprintf("Revert %q\n\nThis reverts pull request #%d (%s).\n", p.Title, p.Number, p.MergeCommit[:10])
		made, err = g.out(ctx, "commit-tree", tree, "-p", tip, "-m", msg)
		if err != nil {
			return nil, err
		}
		return []storage.RefUpdate{{Name: branchRef(branch), New: made}}, nil
	})
	if err != nil {
		return PullRequest{}, err
	}
	np, err := s.Open(ctx, repoID, actor, New{Title: fmt.Sprintf("Revert %q", p.Title), Body: fmt.Sprintf("This reverts pull request #%d, merged as %s.", p.Number, p.MergeCommit[:10]),
		SourceBranch: branch, TargetBranch: p.TargetBranch, Reverts: p.Number})
	if err != nil {
		return np, err
	}
	s.emit(ctx, "commented", repoID, p, actor, map[string]string{"reverted_by": strconv.Itoa(np.Number)})
	return np, nil
}

// SetAutoMerge asks for a pull request to be merged by method as soon as its
// rules pass ("" turns it off).
func (s *Service) SetAutoMerge(ctx context.Context, repoID string, p PullRequest, method, by string) error {
	if p.State != Open {
		return conflict("this pull request is %s", p.State)
	}
	if method != "" {
		set, err := s.Store.Settings(ctx, repoID)
		if err != nil {
			return err
		}
		if !set.allows(method) {
			return invalid("this repository does not allow merging by %s; it allows %s", method, strings.Join(set.Allowed(), ", "))
		}
	}
	if err := s.Store.SetAutoMerge(ctx, p.ID, method, by); err != nil {
		return err
	}
	np, _ := s.Store.ByID(ctx, p.ID)
	if method == "" {
		s.emit(ctx, "auto_merge_disabled", repoID, np, by, nil)
		return nil
	}
	s.emit(ctx, "auto_merge_enabled", repoID, np, by, map[string]string{"method": method})
	s.autoMerge(ctx, repoID, p.ID)
	return nil
}

// autoMerge merges a pull request that asked for it once nothing blocks it.
// It runs after a judgment, a review or a draft change, and from Sweep for
// what only a run's end can change (checks).
func (s *Service) autoMerge(ctx context.Context, repoID, id string) {
	p, err := s.Store.ByID(ctx, id)
	if err != nil || p.State != Open || p.AutoMerge == "" || s.Paths == nil {
		return
	}
	path, err := s.Paths(ctx, repoID)
	if err != nil {
		return
	}
	if rule, ok := RuleFor(mustRules(ctx, s.Store, repoID), p.TargetBranch); ok && rule.MergeQueue {
		// A branch with a queue takes the pull request into it, with the method it asked for.
		if e, _ := s.Store.QueueEntryOf(ctx, p.ID); e == nil || e.State == QueueRemoved {
			if _, err := s.Enqueue(ctx, repoID, path, p, p.AutoMerge, p.AutoMergeBy, RoleNone, 0); err == nil {
				_ = s.Store.SetAutoMerge(ctx, p.ID, "", "")
			}
		}
		return
	}
	ready, err := s.Readiness(ctx, repoID, path, p, RoleNone)
	if err != nil || !ready.CanMerge {
		return
	}
	// Merging is its own request's work: it must not run under the caller's
	// deadline, nor hold up the judgment that triggered it.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if _, err := s.Merge(mctx, repoID, p, MergeInput{Method: p.AutoMerge, ExpectHead: p.HeadSHA, Actor: p.AutoMergeBy, RepoPath: path}); err != nil {
		s.Log.Info("auto-merge could not merge a pull request", "pull", p.Number, "err", err)
		return
	}
	if s.AfterMerge != nil {
		np, _ := s.Store.ByID(mctx, p.ID)
		s.AfterMerge(mctx, repoID, np)
	}
}

// Sweep tries every pull request that asked for auto-merge: the checks of
// a run finish without telling anyone, so a periodic look closes that gap.
func (s *Service) Sweep(ctx context.Context) {
	s.SweepQueue(ctx)
	rows, err := s.Store.AutoMergeCandidates(ctx)
	if err != nil {
		return
	}
	for _, c := range rows {
		s.autoMerge(ctx, c.RepoID, c.ID)
	}
}
