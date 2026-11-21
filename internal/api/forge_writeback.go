package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/refrules"
)

// GitOps write-back to a repository hosted here (internal/engine/writeback.go).
// The credential of a deploy step is made when the step is leased; what it
// may push is judged when the push arrives; and, in pull_request mode, the
// deploy runner asks here to open the pull request and to learn what became
// of it, with the same credential.

// mintWriteBack gives a leased deploy job its own credential for the
// repository its environment writes back to.
func (s *Server) mintWriteBack(ctx context.Context, job *engine.Job) {
	if job == nil || job.Deploy == nil || job.Deploy.WriteBack == nil {
		return
	}
	wb, d := job.Deploy.WriteBack, job.Deploy
	if s.Forge == nil || s.Forge.Provider == nil || s.Forge.RunTokens.Key == nil {
		wb.Error = "this Airfield writes back to a repository hosted by Launchway, but this server hosts no repositories"
		return
	}
	repo, err := s.Forge.Provider.Repositories().Get(ctx, wb.Repo)
	if err != nil {
		wb.Error = fmt.Sprintf("the Airfield writes back to %s, which is not a repository hosted by this server; create or import it, or take write_back out of the Airfield", wb.Repo)
		return
	}
	now := s.Now()
	until := 2 * time.Hour
	if d.Deadline > 0 {
		until = min(max(time.UnixMilli(d.Deadline).Sub(now)+time.Hour, 2*time.Hour), githttp.MaxDeployTokenUntil)
	}
	tok, err := s.Forge.RunTokens.MintForDeploy(githttp.DeployClaim{Repo: repo.ID, Project: job.Project, Deployment: d.Deployment, Branch: d.Branch,
		Mode: wb.Mode, Merge: wb.Merge, Method: wb.Method}, time.Hour, until)
	if err != nil {
		wb.Error = "a credential for the write-back could not be made: " + err.Error()
		return
	}
	wb.Token, wb.RepoID = tok, repo.ID
}

// pushPolicyPipeline judges a push made with a deploy token: only the
// branch the deployment is for (or, in pull_request mode, its own branch),
// never a deletion, and only files a pipeline writer rule admits.
func (s *Server) pushPolicyPipeline(c gitCaller, repo forge.Repository) func(context.Context, []githttp.RefUpdate) error {
	claim := *c.pipe
	return func(ctx context.Context, ups []githttp.RefUpdate) error {
		if !s.Forge.RunTokens.CanPush(claim) {
			return errors.New("this deploy credential has expired; it lasts an hour: run the deploy step again")
		}
		want := "refs/heads/" + claim.Branch
		if claim.Mode == githttp.ModePullRequest {
			want = "refs/heads/" + claim.WorkBranch()
		}
		for _, u := range ups {
			switch {
			case u.Name != want:
				return fmt.Errorf("this deploy credential may push only %s, not %s", strings.TrimPrefix(want, "refs/heads/"), strings.TrimPrefix(u.Name, "refs/heads/"))
			case strings.Trim(u.New, "0") == "":
				return errors.New("this deploy credential cannot delete a branch")
			}
		}
		if s.Forge == nil || s.Forge.Rules == nil {
			return errors.New("the repository's pipeline writer rules could not be read; nothing was changed")
		}
		ws, err := s.Forge.Rules.Writers(ctx, repo.ID)
		if err != nil {
			return errors.New("the repository's pipeline writer rules could not be read; nothing was changed")
		}
		changed := githttp.ChangedFilesFrom(ctx)
		if changed == nil {
			return errors.New("what the push changes could not be read; nothing was changed")
		}
		us := make([]githttp.RefUpdate, len(ups))
		copy(us, ups)
		files, err := changed(us)
		if err != nil {
			return errors.New("what the push changes could not be read; nothing was changed")
		}
		return refrules.Admits(ws, claim.Branch, claim.Project, files)
	}
}

// deployPullRequest is the deploy runner's way to open the pull request of a
// pull_request deployment and to learn what became of it.
func (s *Server) deployPullRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token  string `json:"token"`
		Op     string `json:"op"` // open or status
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.pullsOn(w) {
		return
	}
	claim, err := s.Forge.RunTokens.VerifyDeploy(in.Token)
	if err != nil || claim.Mode != githttp.ModePullRequest {
		writeErr(w, http.StatusForbidden, "this deploy credential is not valid for a pull request (it is expired, or the Airfield does not write back by pull request)")
		return
	}
	repo, err := s.Forge.RepoByID(r.Context(), claim.Repo)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx, svc := r.Context(), s.Forge.Pulls
	author := "pipeline " + claim.Project
	var p pulls.PullRequest
	switch in.Op {
	case "open":
		if !s.Forge.RunTokens.CanPush(claim) {
			writeErr(w, http.StatusForbidden, "this deploy credential has expired; run the deploy step again")
			return
		}
		p, err = s.openDeployPull(ctx, repo, claim, author, in.Title, in.Body)
	case "status":
		p, err = svc.Store.Get(ctx, repo.ID, in.Number)
		if err == nil && (p.SourceBranch != claim.WorkBranch() || p.TargetBranch != claim.Branch) {
			err = pulls.ErrNotFound // a credential speaks only of its own pull request
		}
	default:
		writeErr(w, http.StatusBadRequest, "op is open or status")
		return
	}
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.deployPullView(ctx, r, repo, claim, p))
}

func (s *Server) openDeployPull(ctx context.Context, repo forge.Repository, claim githttp.DeployClaim, author, title, body string) (pulls.PullRequest, error) {
	svc := s.Forge.Pulls
	p, err := svc.Open(ctx, repo.ID, author, pulls.New{Title: title, Body: body, SourceBranch: claim.WorkBranch(), TargetBranch: claim.Branch})
	if err != nil {
		// An earlier try opened it: the same pull request is the answer.
		dup, _ := svc.Store.OpenFromSource(ctx, repo.ID, claim.WorkBranch())
		i := slices.IndexFunc(dup, func(d pulls.PullRequest) bool { return d.TargetBranch == claim.Branch && d.SourceRepo == "" })
		if i < 0 {
			return pulls.PullRequest{}, err
		}
		p = dup[i]
	}
	if claim.Merge != "person" && p.AutoMerge == "" {
		method := claim.Method
		if method == "" {
			set, err := svc.Store.Settings(ctx, repo.ID)
			if err != nil {
				return p, err
			}
			method = set.DefaultMethod
		}
		if err := svc.SetAutoMerge(ctx, repo.ID, p, method, author); err != nil {
			return p, err
		}
		p, _ = svc.Store.ByID(ctx, p.ID)
	}
	return p, nil
}

// deployPullOut says what became of a pull request, for the deploy runner.
type deployPullOut struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"` // open, merged or closed
	// Commit is the new tip of the branch, once merged.
	Commit string `json:"commit,omitempty"`
	// WaitingFor says in a sentence what an open pull request waits for.
	WaitingFor string `json:"waiting_for,omitempty"`
}

func (s *Server) deployPullView(ctx context.Context, r *http.Request, repo forge.Repository, claim githttp.DeployClaim, p pulls.PullRequest) deployPullOut {
	out := deployPullOut{Number: p.Number, URL: s.baseURL(r) + "/repositories/" + repo.Path + "/pulls/" + fmt.Sprint(p.Number), State: string(p.State)}
	switch p.State {
	case pulls.Open:
		rd, err := s.Forge.Pulls.Readiness(ctx, repo.ID, repo.Path, p, pulls.RoleNone)
		switch {
		case err != nil:
			out.WaitingFor = "the pull request's rules could not be read just now"
		case rd.CanMerge && claim.Merge == "person":
			out.WaitingFor = "a person to merge it: the rules are met"
		case rd.CanMerge:
			out.WaitingFor = "the server to merge it: the rules are met"
		case claim.Merge == "person":
			out.WaitingFor = rd.Why + "; then a person to merge it"
		default:
			out.WaitingFor = rd.Why
		}
	case pulls.Merged:
		out.State, out.Commit = "merged", p.MergeCommit
	default:
		out.State = "closed"
	}
	return out
}
