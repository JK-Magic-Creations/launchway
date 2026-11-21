package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/work"
)

// workDelivered says where an item's change is running, from what has really
// been deployed: which Airfields have a deployment of a commit that holds the
// item's merged change, and which do not yet.
type workDelivered struct {
	Merged       bool          `json:"merged"`
	Environments []workEnvLine `json:"environments"`
	Sentence     string        `json:"sentence"`
}

type workEnvLine struct {
	Environment string `json:"environment"`
	Running     bool   `json:"running"`
	Since       string `json:"since,omitempty"`
	Deployment  string `json:"deployment,omitempty"`
	Run         string `json:"run,omitempty"`
	Version     string `json:"version,omitempty"`
}

func (s *Server) workDelivery(w http.ResponseWriter, r *http.Request) {
	it, err := s.work().Get(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	code, _ := s.work().CodeLinks(r.Context(), s.workCaller(r), it.ID)
	writeJSON(w, http.StatusOK, s.deliveryOf(r, it, code))
}

// deliveryOf works out the delivery status of an item. It is nil for an item
// with nothing merged (there is nothing to deliver yet).
func (s *Server) deliveryOf(r *http.Request, it *work.Item, code []work.CodeLink) *workDelivered {
	var commits []work.CodeLink
	merged := false
	for _, l := range code {
		if l.Kind == work.CodePull && l.State == "merged" {
			merged = true
		}
		if l.Kind == work.CodeCommit && l.Repo != "" && l.State == "merged" {
			commits = append(commits, l)
		}
	}
	if !merged && len(commits) == 0 {
		return nil
	}
	out := &workDelivered{Merged: merged, Environments: []workEnvLine{}}
	envs := s.Engine.Environments()
	if len(envs) == 0 {
		out.Sentence = "Merged. No environments are configured, so there is nothing to show about where it runs."
		return out
	}
	deps, err := s.Engine.Deployments(engine.DeploymentFilter{Limit: 200})
	if err != nil {
		out.Sentence = "Merged. Where it runs could not be read."
		return out
	}
	byEnv := map[string][]engine.DeploymentView{}
	for _, d := range deps {
		if d.Phase == "succeeded" || d.Phase == "deployed" || d.DeployedAt > 0 {
			byEnv[d.Environment] = append(byEnv[d.Environment], d)
		}
	}
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, e.Name)
	}
	sort.SliceStable(names, func(i, j int) bool { return false })
	var running, missing []string
	for _, env := range names {
		line := workEnvLine{Environment: env}
		for _, d := range byEnv[env] { // newest first: the first one that holds the fix is the earliest since... the newest
			if d.Commit == "" || d.Repo == "" {
				continue
			}
			for _, c := range commits {
				if c.Repo == d.Repo && s.holdsCommit(r, d.Repo, c.Ref, d.Commit) {
					line.Running, line.Deployment, line.Run, line.Version = true, d.ID, d.RunID, d.Version
					line.Since = time.UnixMilli(d.DeployedAt).UTC().Format(time.RFC3339)
				}
			}
		}
		// The earliest deployment that holds it is when it began to run there.
		if line.Running {
			for _, d := range byEnv[env] {
				for _, c := range commits {
					if d.Repo == c.Repo && d.Commit != "" && s.holdsCommit(r, d.Repo, c.Ref, d.Commit) && d.DeployedAt > 0 {
						line.Since = time.UnixMilli(d.DeployedAt).UTC().Format(time.RFC3339)
						line.Deployment, line.Run, line.Version = d.ID, d.RunID, d.Version
					}
				}
			}
			running = append(running, fmt.Sprintf("on %s since %s", env, line.Since[11:16]))
		} else {
			missing = append(missing, env)
		}
		out.Environments = append(out.Environments, line)
	}
	switch {
	case len(running) == 0:
		out.Sentence = "Merged, not yet on any environment."
	case len(missing) == 0:
		out.Sentence = "Merged, " + strings.Join(running, ", ") + "."
	default:
		out.Sentence = "Merged, " + strings.Join(running, ", ") + ", not yet on " + strings.Join(missing, " or ") + "."
	}
	return out
}

// holdsCommit says whether the commit a deployment ran contains the commit
// that merged the change (the same commit, or a descendant of it).
func (s *Server) holdsCommit(r *http.Request, repoPath, merged, deployed string) bool {
	if merged == "" || deployed == "" {
		return false
	}
	if merged == deployed || strings.HasPrefix(deployed, merged) || strings.HasPrefix(merged, deployed) {
		return true
	}
	if s.Forge == nil || s.Forge.Pulls == nil {
		return false
	}
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), repoPath)
	if err != nil {
		return false
	}
	ok, err := s.Forge.Pulls.Contains(r.Context(), repo.ID, merged, deployed)
	return err == nil && ok
}
