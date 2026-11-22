package engine

import (
	"fmt"
	"regexp"
	"slices"
)

// How a deploy step writes to a GitOps repository that Launchway itself
// hosts. An environment may say so (write_back): the repository, and one of
// two explicit modes, because a protected branch refuses a plain push:
//
//   - direct: the deploy step pushes to the branch with a short-lived
//     credential of its own (the server makes it when it hands the step to a
//     deploy runner, nothing is stored). The repository must have a pipeline
//     writer rule admitting the project to that branch for exactly the files
//     it may write; a push that touches anything else is refused.
//   - pull_request: the deploy step pushes its change to a branch of its own
//     (launchway/deploy/<deployment>) and opens a pull request into the
//     branch. The repository's merge rules apply as for anyone: with merge:
//     auto the pull request merges itself as soon as they are satisfied; with
//     merge: person it waits for a person to merge it. The step goes on when
//     it is merged and fails, saying why, when it is closed or too late.
//
// Without write_back the deploy runner uses the credentials of its own
// configuration, as before (and for any repository hosted elsewhere).

// WriteBack is an environment's way of writing to a hosted repository.
type WriteBack struct {
	// Repo is the hosted repository, owner/name.
	Repo string `json:"repo"`
	// Mode is direct or pull_request.
	Mode string `json:"mode"`
	// Merge, for pull_request, is auto (the default) or person.
	Merge string `json:"merge,omitempty"`
	// Method is how a pull request is merged: merge, squash or rebase; empty
	// is the repository's default.
	Method string `json:"method,omitempty"`
}

// WriteBackJob is what the deploy runner is told about the write-back. The
// server adds the credential (Token) when it leases the job; Error says why
// it could not (the repository is not hosted here, say), and the runner then
// fails the step with that sentence.
type WriteBackJob struct {
	WriteBack
	Token string `json:"token,omitempty"`
	// Outside is set when the repository is kept on another Git host
	// (write_back.repo is connection:owner/name): Token is then the host's
	// credential for the push, Username goes with it, and Handle is what the
	// step shows the server to open and follow its pull request.
	Outside    bool   `json:"outside,omitempty"`
	Username   string `json:"username,omitempty"`
	Handle     string `json:"handle,omitempty"`
	RepoID     string `json:"repo_id,omitempty"`
	WorkBranch string `json:"work_branch,omitempty"` // pull_request: where the change is pushed
	Error      string `json:"error,omitempty"`
}

var hostedRepoRe = regexp.MustCompile(`^([a-z0-9][a-z0-9._-]{0,62}:)?[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Write-back modes and merge policies.
const (
	WriteBackDirect      = "direct"
	WriteBackPullRequest = "pull_request"
)

func checkWriteBack(env string, w *WriteBack) error {
	switch {
	case w == nil:
		return nil
	case !hostedRepoRe.MatchString(w.Repo):
		return fmt.Errorf("environment %s: write_back.repo %q: write the hosted repository as owner/name, or a repository on an outside host as connection:owner/name", env, w.Repo)
	case w.Mode != WriteBackDirect && w.Mode != WriteBackPullRequest:
		return fmt.Errorf("environment %s: write_back.mode %q: write direct (a rule of the repository admits the pipeline) or pull_request (the step opens a pull request)", env, w.Mode)
	case w.Merge != "" && w.Mode != WriteBackPullRequest:
		return fmt.Errorf("environment %s: write_back.merge is for pull_request mode", env)
	case !slices.Contains([]string{"", "auto", "person"}, w.Merge):
		return fmt.Errorf("environment %s: write_back.merge %q: write auto (merge when the rules are met) or person (wait for a person)", env, w.Merge)
	case !slices.Contains([]string{"", "merge", "squash", "rebase"}, w.Method):
		return fmt.Errorf("environment %s: write_back.method %q: write merge, squash or rebase", env, w.Method)
	}
	return nil
}

// writeBack is the write-back part of a deploy runner's job for an
// environment, or nil.
func (c *cmd) writeBack(environment, deployment string) *WriteBackJob {
	env, ok := c.e.live().envs[environment]
	if !ok || env.WriteBack == nil {
		return nil
	}
	j := &WriteBackJob{WriteBack: *env.WriteBack}
	if j.Merge == "" && j.Mode == WriteBackPullRequest {
		j.Merge = "auto"
	}
	if j.Mode == WriteBackPullRequest {
		j.WorkBranch = "launchway/deploy/" + deployment
	}
	return j
}

// checkEnvironmentRest checks an environment's rollback and write-back settings.
func checkEnvironmentRest(env Environment) error {
	if err := checkEmergency(env.EmergencyRollback, "environment "+env.Name+": emergency_rollback"); err != nil {
		return err
	}
	return checkWriteBack(env.Name, env.WriteBack)
}
