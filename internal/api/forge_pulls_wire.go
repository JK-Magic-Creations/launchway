package api

import (
	"context"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/refrules"
)

// WirePulls connects the pull request service to the rest of the server: the
// events it raises (webhooks, the audit trail, the projects that watch the
// repository), the runs that are its checks, and the repositories it names by ID.
func (s *Server) WirePulls() {
	if s.Forge == nil || s.Forge.Pulls == nil {
		return
	}
	s.EnableSigning()
	p := s.Forge.Pulls
	if s.Projects != nil {
		s.Projects.SetForkGate(s.forkGate)
	}
	p.OnEvent = s.pullEvent
	p.Runs = pullRuns{s}
	p.MergeCheck = s.workMergeCheck
	p.Paths = func(ctx context.Context, id string) (string, error) {
		r, err := s.Forge.RepoByID(ctx, id)
		return r.Path, err
	}
	p.AfterBranchMoved = func(ctx context.Context, id, actor string, ups []pulls.RefUpdate) {
		if r, err := s.Forge.RepoByID(ctx, id); err == nil {
			s.pullRefsMoved(ctx, actor, r, ups)
		}
	}
	p.ProtectedBranch = func(ctx context.Context, id, branch string) bool {
		rules, _ := s.Forge.Rules.Patterns(ctx, id)
		return len(rules) > 0 && refrulesCheck(rules, branch, false) != nil
	}
	p.AfterMerge = func(ctx context.Context, id string, np pulls.PullRequest) {
		if r, err := s.Forge.RepoByID(ctx, id); err == nil {
			s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: np.AutoMergeBy, Action: "pull.auto_merge", Target: r.Path, Outcome: "ok",
				Detail: fmt.Sprintf("#%d by %s into %s as %s", np.Number, np.MergeMethod, np.TargetBranch, short(np.MergeCommit))})
			// Auto-merge made the merge: the target moved like after a push.
			s.pullRefsMoved(ctx, np.AutoMergeBy, r, []pulls.RefUpdate{{Name: "refs/heads/" + np.TargetBranch, New: np.MergeCommit}})
		}
	}
}

// pullsPushed brings pull requests up to date after a push.
func (s *Server) pullsPushed(ctx context.Context, c gitCaller, repo forge.Repository, updates []githttp.RefUpdate) {
	if s.Forge == nil || s.Forge.Pulls == nil {
		return
	}
	ups := make([]pulls.RefUpdate, len(updates))
	for i, u := range updates {
		ups[i] = pulls.RefUpdate{Name: u.Name, Old: u.Old, New: u.New}
	}
	s.Forge.Pulls.Pushed(ctx, repo.ID, c.user, ups)
}

// pullRuns finds the runs of a pull request: runs of event pull_request and
// its number, in the projects that watch the repository.
type pullRuns struct{ s *Server }

func (p pullRuns) RunsOf(_ context.Context, _, repoPath string, number int) ([]pulls.RunInfo, error) {
	s := p.s
	if s.Projects == nil || s.Engine == nil {
		return nil, nil
	}
	var out []pulls.RunInfo
	for _, pr := range s.Projects.List() {
		if !addressIsRepo(pr.URL, repoPath) {
			continue
		}
		runs, err := s.Engine.ListRuns(engine.RunFilter{Project: pr.Name, Limit: 200})
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			if r.Event != "pull_request" || r.PullRequest != number {
				continue
			}
			ri := pulls.RunInfo{ID: r.ID, Project: r.Project, Number: r.Number, Pipeline: r.Pipeline, Status: string(r.Status), SHA: r.SHA, Created: r.CreatedAt}
			for _, st := range r.Steps {
				ri.Steps = append(ri.Steps, pulls.StepInfo{Name: st.ID, Status: string(st.Status), Allowed: st.Allowed, SkipKind: st.SkipKind})
			}
			out = append(out, ri)
		}
		for _, sha := range s.Engine.PathSkippedHeads(pr.Name, fmt.Sprintf("refs/pull/%d/head", number)) {
			out = append(out, pulls.RunInfo{Project: pr.Name, SHA: sha, PathSkipped: true})
		}
	}
	return out, nil
}

func refrulesCheck(patterns []string, branch string, admin bool) error {
	return refrules.Check(patterns, []refrules.Update{{Name: "refs/heads/" + branch, Old: "1", New: "2"}}, admin)
}
