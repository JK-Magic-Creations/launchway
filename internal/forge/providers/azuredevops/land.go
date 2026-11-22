package azuredevops

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type baseLand = forge.Land

// land is the group of merging. A connection does not complete a pull request
// for the host; the one thing it drives is the host's own auto-complete.
type land struct {
	baseLand
	p *Provider
}

// mergeStrategy maps Launchway's strategies to the host's documented values
// (GitPullRequestMergeStrategy: noFastForward, squash, rebase, rebaseMerge).
func mergeStrategy(s forge.Strategy) string {
	return map[forge.Strategy]string{forge.StrategyMerge: "noFastForward", forge.StrategySquash: "squash", forge.StrategyReplay: "rebase"}[s]
}

// AutoLand: enabling is PATCH .../pullrequests/{id} with autoCompleteSetBy.id
// (the identity that enables it; the pull request's author, which for a
// write-back pull request is the connection's own account) and
// completionOptions.mergeStrategy and deleteSourceBranch (scope vso.code_write;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update,
// "Enable auto-completion and set other completion options"). Auto-complete
// waits for every required policy. The reference names no call to cancel it, so
// disabling is refused with the way to do it on the host.
func (l land) AutoLand(ctx context.Context, repo forge.Repository, number int, s forge.Strategy, enable bool) error {
	if !enable {
		return fmt.Errorf("%w: the host's reference names no call that cancels auto-complete: cancel it on the pull request's page on Azure DevOps", forge.ErrInvalid)
	}
	ms := mergeStrategy(s)
	if ms == "" {
		return fmt.Errorf("%w: auto-complete by %q is not one of merge, squash or replay", forge.ErrInvalid, s)
	}
	a, err := proposals{l.p}.get(ctx, repo, number)
	if err != nil {
		return err
	}
	if a.CreatedBy.ID == "" {
		return fmt.Errorf("the host's answer names no author for pull request %d to set auto-complete with", number)
	}
	rp, err := l.p.repoPath(repo)
	if err != nil {
		return err
	}
	body := map[string]any{"autoCompleteSetBy": map[string]any{"id": a.CreatedBy.ID},
		"completionOptions": map[string]any{"mergeStrategy": ms, "deleteSourceBranch": false}}
	_, err = l.p.c.Call(ctx, http.MethodPatch, rp+"/pullrequests/"+strconv.Itoa(number)+qs(), body, nil)
	return err
}
