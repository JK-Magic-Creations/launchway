package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/refrules"
)

// What the branch list says beyond the names: the last pipeline result of each branch, whether it is stale (no commit
// for the hosting setting's days) and merged (it has nothing its base lacks), and the bulk removal of merged stale
// branches. The removal lists first and deletes only what the caller names from that list.

// branchRun is the newest run of a branch, in one line.
type branchRun struct {
	ID       string `json:"id"`
	Number   int    `json:"number"`
	Project  string `json:"project"`
	Pipeline string `json:"pipeline"`
	Status   string `json:"status"`
}

func isStale(committedAt string, days int, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, committedAt)
	return err == nil && now.Sub(t) > time.Duration(days)*24*time.Hour
}

// lastRunOfBranch is the newest run on a branch in the Flights that watch the repository; nil when there is none.
func (s *Server) lastRunOfBranch(repoPath, branch string) *branchRun {
	if s.Projects == nil || s.Engine == nil {
		return nil
	}
	var best *engine.RunSummary
	for _, pr := range s.Projects.List() {
		if !addressIsRepo(pr.URL, repoPath) {
			continue
		}
		if r, err := s.Engine.LastRunOn(pr.Name, branch); err == nil && (best == nil || r.CreatedAt > best.CreatedAt) {
			best = r
		}
	}
	if best == nil {
		return nil
	}
	return &branchRun{ID: best.ID, Number: best.Number, Project: best.Project, Pipeline: best.Pipeline, Status: string(best.Status)}
}

type staleBranchesIn struct {
	// Confirm false only lists what would go; true deletes the Names that are on that list.
	Confirm bool     `json:"confirm"`
	Names   []string `json:"names"`
}

type staleBranchesOut struct {
	Days       int      `json:"stale_days"`
	Candidates []string `json:"candidates"`
	Deleted    []string `json:"deleted"`
	// Skipped says why a named branch was left: it is not a merged stale branch any more, or the delete was refused.
	Skipped   map[string]string `json:"skipped"`
	Truncated bool              `json:"truncated,omitempty"`
}

// staleCandidates are the branches that are merged into the default branch, stale, not the default, not protected and
// without an open pull request.
func (s *Server) staleCandidates(r *http.Request, repo forge.Repository) ([]string, int, bool, error) {
	days := s.hostingSettings().StaleBranchDays
	items, total, err := s.filesService().Refs(r.Context(), repo.ID, "branch", "", repo.DefaultBranch, 500)
	if err != nil {
		return nil, days, false, err
	}
	pats, _ := s.protectedPatterns(r, repo)
	out := []string{}
	for _, it := range items {
		if it.Name == repo.DefaultBranch || it.Ahead != 0 || !isStale(it.CommittedAt, days, s.Now()) {
			continue
		}
		if _, prot := refrules.Match(pats, "refs/heads/"+it.Name); prot {
			continue
		}
		if s.Forge.Pulls != nil {
			if open, err := s.Forge.Pulls.Store.OpenFromSource(r.Context(), repo.ID, it.Name); err != nil || len(open) > 0 {
				continue
			}
		}
		out = append(out, it.Name)
	}
	sort.Strings(out)
	return out, days, total > len(items), nil
}

func (s *Server) deleteStaleBranches(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	var in staleBranchesIn
	if !decode(w, r, &in) {
		return
	}
	cands, days, trunc, err := s.staleCandidates(r, repo)
	if err != nil {
		s.failFiles(w, err)
		return
	}
	out := staleBranchesOut{Days: days, Candidates: cands, Deleted: []string{}, Skipped: map[string]string{}, Truncated: trunc}
	if !in.Confirm {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if len(in.Names) == 0 {
		writeErr(w, http.StatusBadRequest, "name the branches to delete: the ones the list showed (confirm is true only after a person has seen them)")
		return
	}
	if len(in.Names) > 200 {
		writeErr(w, http.StatusBadRequest, "at most 200 branches at a time")
		return
	}
	allowed := map[string]bool{}
	for _, c := range cands {
		allowed[c] = true
	}
	for _, name := range in.Names {
		if !allowed[name] {
			out.Skipped[name] = "not a merged, stale, unprotected branch without an open pull request now"
			continue
		}
		if _, err := s.removeBranch(r, repo, name); err != nil {
			out.Skipped[name] = err.Error()
			continue
		}
		out.Deleted = append(out.Deleted, name)
	}
	setAudit(r, repo.Path, fmt.Sprintf("deleted %d merged stale branches: %s", len(out.Deleted), strings.Join(out.Deleted, ", ")))
	writeJSON(w, http.StatusOK, out)
}
