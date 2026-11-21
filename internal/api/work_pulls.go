package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/work"
)

// Where pull requests meet work: a pull request that names work links to it
// and moves it by the level's rule; an agent's pull request meets the merge
// gate; what became of an agent's change is counted for the pause rule.

// agentOf is the registered agent a pull request user name stands for, or "".
func agentNameOf(user string) string { return strings.TrimPrefix(user, "agent:") }

func isAgentUser(user string) bool { return strings.HasPrefix(user, "agent:") }

// workOnPull is called for every pull request event.
func (s *Server) workOnPull(ctx context.Context, repo forge.Repository, e pulls.Event) {
	st := s.work()
	home := work.Scope{Kind: work.ScopeProject, ID: repo.Path}
	state := string(e.Pull.State)
	switch e.Action {
	case "opened", "reopened", "edited", "merged", "closed", "synchronize":
	default:
		return
	}
	by := work.System("git")
	if !isAgentUser(e.Actor) && e.Actor != "" && e.Actor != "system" {
		by = work.Caller{Actor: work.Actor{Name: e.Actor, Kind: work.KindPerson, Via: "git"}, Read: func(work.Scope) bool { return true }, Write: func(work.Scope) bool { return true }, Maintain: func(work.Scope) bool { return false }}
	} else if isAgentUser(e.Actor) {
		by = work.Caller{Actor: work.Actor{Name: agentNameOf(e.Actor), Kind: work.KindAgent, Via: "git"}}
	}
	var commits []string
	if e.Action == "opened" || e.Action == "merged" || e.Action == "edited" {
		if cs, err := s.Forge.Pulls.Commits(ctx, repo.ID, e.Pull); err == nil {
			for _, c := range cs {
				commits = append(commits, c.Subject+"\n"+c.Body)
			}
		}
	}
	ch := work.PullChange{Repo: repo.Path, Number: e.Pull.Number, Title: e.Pull.Title, Body: e.Pull.Body, Branch: e.Pull.SourceBranch, State: state, Commits: commits, By: by,
		MayClose: func(sc work.Scope) bool { return s.mayCloseFor(e.Actor, sc) }}
	if _, err := st.ApplyPull(ctx, home, ch); err != nil {
		s.Log.Warn("linking a pull request to work", "pull", e.Pull.Number, "err", err)
		return
	}
	if e.Action == "merged" && e.Pull.MergeCommit != "" {
		items, _ := st.ItemsForCode(ctx, work.System("git"), work.CodePull, repo.Path, fmt.Sprint(e.Pull.Number))
		for _, it := range items {
			_, _ = st.LinkCode(ctx, work.System("git"), it.ID, work.CodeLink{Kind: work.CodeCommit, Repo: repo.Path, Ref: e.Pull.MergeCommit, Title: e.Pull.Title, State: "merged", Source: "merge"})
		}
		if isAgentUser(e.Pull.Author) {
			_ = st.RecordAgentChange(ctx, home, agentNameOf(e.Pull.Author), repo.Path, fmt.Sprint(e.Pull.Number), "merged")
			if n, _ := st.AgentReviews(ctx, repo.Path, fmt.Sprint(e.Pull.Number)); len(n) > 0 {
				_, _ = st.SampleMerge(ctx, home, repo.Path, fmt.Sprint(e.Pull.Number))
			}
		}
	}
	if e.Action == "opened" && e.Pull.Reverts > 0 {
		if orig, err := s.Forge.Pulls.Store.Get(ctx, repo.ID, e.Pull.Reverts); err == nil && isAgentUser(orig.Author) {
			_ = st.RecordAgentChange(ctx, home, agentNameOf(orig.Author), repo.Path, fmt.Sprint(orig.Number), "reverted")
		}
	}
}

// mayCloseFor says whether who may close work of a scope (for a closing
// keyword in a pull request of another project).
func (s *Server) mayCloseFor(user string, sc work.Scope) bool {
	if isAgentUser(user) || user == "" || user == "system" {
		return false
	}
	return s.workAllows(&identity{User: user, Via: viaSession}, sc, auth.Operate)
}

// agentChangeOf describes a pull request for the merge gate and the agents'
// reviews, as the server itself reads it.
func (s *Server) agentChangeOf(ctx context.Context, repo forge.Repository, p pulls.PullRequest) (work.AgentChange, []string, []string, error) {
	st := s.work()
	ch := work.AgentChange{Scope: work.Scope{Kind: work.ScopeProject, ID: repo.Path}, Repo: repo.Path, Pull: fmt.Sprint(p.Number), ItemType: "issue"}
	d, err := s.Forge.Pulls.Files(ctx, repo.ID, p, pulls.DefaultLimits)
	if err != nil {
		return ch, nil, nil, err
	}
	for _, f := range d.Files {
		ch.Files = append(ch.Files, work.ChangedFile{Path: f.Path, Deleted: f.Status == "deleted"})
		if f.OldPath != "" {
			ch.Files = append(ch.Files, work.ChangedFile{Path: f.OldPath, Deleted: f.Status == "renamed" && work.IsTestPath(f.OldPath) && !work.IsTestPath(f.Path)})
		}
	}
	if d.Truncated {
		// Not every file was read: it is not one a policy may wave through.
		ch.Files = append(ch.Files, work.ChangedFile{Path: ".launchway/(more files than were read)"})
	}
	ch.Lines = d.Additions + d.Deletions
	if pt, err := s.Forge.Pulls.PatchOf(ctx, repo.ID, d.From, d.To, nil, true, pulls.DefaultLimits); err == nil {
		var added []string
		for _, l := range strings.Split(pt.Diff, "\n") {
			if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
				added = append(added, l[1:])
			}
		}
		ch.TestsSkipped = work.SkipsTests(added) || pt.Truncated || pt.Held
	}
	if rd, err := s.Forge.Pulls.Readiness(ctx, repo.ID, repo.Path, p, pulls.RoleNone); err == nil {
		required := 0
		ch.ChecksPassed = true
		for _, c := range rd.Checks {
			if !c.Required {
				continue
			}
			required++
			ch.ChecksRequired++
			if c.Status == pulls.CheckSkipped {
				ch.ChecksSkipped = true
			}
			if c.Status != pulls.CheckPassed {
				ch.ChecksPassed = false
			}
		}
		_ = required
	}
	rs, _ := s.Forge.Pulls.Store.Reviews(ctx, p.ID)
	for _, r := range rs {
		if r.State == "submitted" && r.Verdict == "approve" && r.CommitSHA == p.HeadSHA && !isAgentUser(r.Author) && r.Author != p.Author {
			ch.HumanApproved = true
		}
	}
	authors := []string{}
	if isAgentUser(p.Author) {
		authors = append(authors, agentNameOf(p.Author))
	}
	if revs, err := s.Forge.Pulls.Store.Revisions(ctx, p.ID); err == nil {
		for _, r := range revs {
			if isAgentUser(r.Actor) && !contains(authors, agentNameOf(r.Actor)) {
				authors = append(authors, agentNameOf(r.Actor))
			}
		}
	}
	var itemIDs []string
	items, _ := st.ItemsForCode(ctx, work.System("git"), work.CodePull, repo.Path, fmt.Sprint(p.Number))
	for _, it := range items {
		itemIDs = append(itemIDs, it.ID)
		ch.ItemType = it.Type
		for _, a := range it.Assignees {
			if a.Kind == work.KindAgent && !contains(authors, a.Name) {
				// Whoever worked the item is treated as an author: it is not independent of the change.
				authors = append(authors, a.Name)
			}
		}
	}
	return ch, authors, itemIDs, nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// workMergeCheck is the merge gate for agents, asked by every merge (a
// request's or auto-merge's): an agent's pull request lands only when the
// policy and its checks and its approvals allow, and an agent merges only
// where it is allowed to.
func (s *Server) workMergeCheck(ctx context.Context, repoID string, p pulls.PullRequest, actor string) error {
	if !isAgentUser(p.Author) && !isAgentUser(actor) {
		return nil
	}
	repo, err := s.Forge.RepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	ch, _, _, err := s.agentChangeOf(ctx, repo, p)
	if err != nil {
		return pulls.Refuse("this agent's change could not be read, so it cannot be judged: %v", err)
	}
	d, err := s.work().Gate(ctx, ch)
	if err != nil {
		return err
	}
	if isAgentUser(actor) {
		a, err := s.work().AgentByName(ctx, agentNameOf(actor))
		if err != nil || !a.MayMerge || a.Disabled {
			return pulls.Refuse("an agent does not merge unless it is allowed to; ask a person to merge it")
		}
		if !d.AutoMerge {
			return pulls.Refuse("an agent may merge only a change the autonomy policy lets through: %s", strings.Join(d.Reasons, "; "))
		}
		return nil
	}
	if !d.CanMerge {
		return pulls.Refuse("this is an agent's change and it needs a person's approval first (or an independent agent's, where the policy allows it for a change like this): %s", strings.Join(d.Reasons, "; "))
	}
	return nil
}

// workOnPush links the work that the commit messages of a push name. A closing
// keyword on a commit that reaches the default branch directly is treated like
// a merge, by a person only.
func (s *Server) workOnPush(ctx context.Context, c gitCaller, repo forge.Repository, updates []githttp.RefUpdate) {
	if s.Forge == nil || s.Forge.Pulls == nil {
		return
	}
	st := s.work()
	home := work.Scope{Kind: work.ScopeProject, ID: repo.Path}
	for _, u := range updates {
		if !strings.HasPrefix(u.Name, "refs/heads/") || zeroSHA(u.New) {
			continue
		}
		branch := strings.TrimPrefix(u.Name, "refs/heads/")
		base := u.Old
		if zeroSHA(base) {
			base = repo.DefaultBranch
			if base == branch {
				continue
			}
		}
		cs, err := s.Forge.Pulls.CommitsBetween(ctx, repo.ID, base, u.New, 50)
		if err != nil {
			continue
		}
		by := work.Caller{Actor: work.Actor{Name: c.user, Kind: work.KindPerson, Via: "git"}}
		if isAgentUser(c.user) {
			by = work.Caller{Actor: work.Actor{Name: agentNameOf(c.user), Kind: work.KindAgent, Via: "git"}}
		}
		for _, ci := range cs {
			_, _ = st.ApplyCommit(ctx, home, work.CommitChange{Repo: repo.Path, SHA: ci.SHA, Message: ci.Subject + "\n" + ci.Body, Branch: branch, Default: branch == repo.DefaultBranch,
				By: by, MayClose: func(sc work.Scope) bool { return s.mayCloseFor(c.user, sc) }})
		}
	}
}
