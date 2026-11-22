package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type baseLand = forge.Land

// land is the group of merging. A connection does not merge for the host; the
// one thing it drives is the host's own auto-merge of a merge request.
type land struct {
	baseLand
	p *Provider
}

// AutoLand: enabling is PUT /projects/:id/merge_requests/:iid/merge with
// auto_merge=true ("merges when checks pass"; merge_when_pipeline_succeeds is
// deprecated in 17.11) and squash, which is the squash strategy; disabling is
// POST .../cancel_auto_merge (introduced in 19.5), and on a host that does not
// have it yet, the deprecated POST .../cancel_merge_when_pipeline_succeeds
// (https://docs.gitlab.com/api/merge_requests/ , "Merge a merge request",
// "Cancel auto merge"). The host answers 405 when the merge request cannot be
// merged and 409 when there is nothing to cancel.
func (l land) AutoLand(ctx context.Context, repo forge.Repository, number int, s forge.Strategy, enable bool) error {
	pp, err := l.p.projectPath(repo)
	if err != nil {
		return err
	}
	mr := pp + "/merge_requests/" + strconv.Itoa(number)
	if enable {
		body := map[string]any{"auto_merge": true}
		switch s {
		case forge.StrategyMerge:
		case forge.StrategySquash:
			body["squash"] = true
		default:
			return fmt.Errorf("%w: auto-merge by %q is not one of merge or squash on this host (the merge endpoint has no rebase option)", forge.ErrInvalid, s)
		}
		st, err := l.p.c.Call(ctx, http.MethodPut, mr+"/merge", body, nil, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity)
		if err != nil {
			return err
		}
		if st == http.StatusMethodNotAllowed || st == http.StatusUnprocessableEntity {
			return fmt.Errorf("%w: the host would not set auto-merge (%d): the merge request cannot be merged yet or auto-merge is off for the project", forge.ErrInvalid, st)
		}
		return nil
	}
	st, err := l.p.c.Call(ctx, http.MethodPost, mr+"/cancel_auto_merge", nil, nil, http.StatusNotFound, http.StatusConflict)
	if err != nil {
		return err
	}
	if st == http.StatusNotFound {
		_, err = l.p.c.Call(ctx, http.MethodPost, mr+"/cancel_merge_when_pipeline_succeeds", nil, nil, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotAcceptable)
	}
	return err
}
