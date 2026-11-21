package runner

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
)

// Pull asks the server to open the pull request of a deployment that writes
// back by pull request, or what became of it (argocd.Pulls).
func (c *Client) Pull(ctx context.Context, in argocd.PullCall) (argocd.PullState, error) {
	var out argocd.PullState
	_, err := c.call(ctx, "deploy/pull-request", in, &out)
	return out, err
}
