package gitea

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type baseLand = forge.Land

// land is the group of merging. A connection does not merge for the host; the
// one thing it drives is the host's scheduled merge of a pull request.
type land struct {
	baseLand
	p *Provider
}

// AutoLand: enabling is POST /repos/{o}/{r}/pulls/{index}/merge with Do
// (merge, squash, rebase) and merge_when_checks_succeed=true
// (repoMergePullRequest); disabling is DELETE the same address
// (repoCancelScheduledAutoMerge). The host answers 405 when the pull request
// cannot be merged and 409 on a conflict; a 404 on cancel means nothing was scheduled.
func (l land) AutoLand(ctx context.Context, repo forge.Repository, number int, s forge.Strategy, enable bool) error {
	rp, err := l.p.repoPath(repo)
	if err != nil {
		return err
	}
	pull := rp + "/pulls/" + strconv.Itoa(number) + "/merge"
	if !enable {
		_, err := l.p.c.Call(ctx, http.MethodDelete, pull, nil, nil, http.StatusNotFound)
		return err
	}
	do := map[forge.Strategy]string{forge.StrategyMerge: "merge", forge.StrategySquash: "squash", forge.StrategyReplay: "rebase"}[s]
	if do == "" {
		return fmt.Errorf("%w: auto-merge by %q is not one of merge, squash or replay", forge.ErrInvalid, s)
	}
	st, err := l.p.c.Call(ctx, http.MethodPost, pull, map[string]any{"Do": do, "merge_when_checks_succeed": true}, nil, http.StatusMethodNotAllowed, http.StatusConflict)
	if err != nil {
		return err
	}
	if st == http.StatusMethodNotAllowed || st == http.StatusConflict {
		return fmt.Errorf("%w: the host would not schedule the merge (%d): the pull request cannot be merged yet, or the merge method is off for the repository", forge.ErrInvalid, st)
	}
	return nil
}
