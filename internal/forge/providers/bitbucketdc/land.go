package bitbucketdc

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type baseLand = forge.Land

// land is the group of merging. A connection does not merge for the host; the
// one thing it drives is the host's own auto-merge of a pull request.
type land struct {
	baseLand
	p *Provider
}

// strategyID maps Launchway's strategies to the host's merge strategy ids; the
// reference's examples name "no-ff" (a merge commit, "Always create a merge
// commit") and "squash". Others (fast-forward, rebase) are not named by the
// reference's pages here and are refused rather than guessed.
func strategyID(s forge.Strategy) string {
	return map[forge.Strategy]string{forge.StrategyMerge: "no-ff", forge.StrategySquash: "squash"}[s]
}

// AutoLand: enabling is POST .../pull-requests/{id}/merge?version=N with
// autoMerge true and the strategyId ("Merge the specified pull request
// immediately or set the pull request to auto-merge when all the merge checks
// pass by setting autoMerge"; REPO_WRITE). The repository's own setting must
// allow it (GET .../settings/auto-merge, enabled), which is checked first so the
// message names the cause. Disabling is DELETE .../pull-requests/{id}/auto-merge
// (cancels a request that has not merged yet; REPO_WRITE).
func (l land) AutoLand(ctx context.Context, repo forge.Repository, number int, s forge.Strategy, enable bool) error {
	rp, err := l.p.repoPath(repo)
	if err != nil {
		return err
	}
	pull := rp + "/pull-requests/" + strconv.Itoa(number)
	if !enable {
		_, err := l.p.c.Call(ctx, http.MethodDelete, pull+"/auto-merge", nil, nil)
		return err
	}
	id := strategyID(s)
	if id == "" {
		return fmt.Errorf("%w: auto-merge by %q is not one of merge or squash on this host", forge.ErrInvalid, s)
	}
	var set struct {
		Enabled bool `json:"enabled"`
	}
	if _, err := l.p.c.Get(ctx, rp+"/settings/auto-merge", &set); err != nil {
		return err
	}
	if !set.Enabled {
		return fmt.Errorf("%w: auto-merge is not enabled for this repository: turn it on in the repository's or project's settings on Bitbucket, or set write_back.merge to person", forge.ErrInvalid)
	}
	cur, err := proposals{l.p}.get(ctx, repo, number)
	if err != nil {
		return err
	}
	_, err = l.p.c.Call(ctx, http.MethodPost, pull+"/merge?version="+strconv.Itoa(cur.Version), map[string]any{"autoMerge": true, "strategyId": id}, nil)
	return err
}
