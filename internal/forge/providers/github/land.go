package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Land is the group of merging. A connection does not merge for the host (merging stays under its branch
// protection); the one thing it drives is the host's own auto-merge of a pull request, through the GraphQL API:
// enablePullRequestAutoMerge and disablePullRequestAutoMerge
// (https://docs.github.com/en/graphql/reference/mutations#enablepullrequestautomerge).
func (p *Provider) Land() forge.Land { return land{p: p, baseLand: p.Base.Land()} }

type baseLand = forge.Land

type land struct {
	baseLand
	p *Provider
}

// graphqlPath is the GraphQL endpoint beside the REST API: /graphql on the public service, /api/graphql on a
// self-managed install whose REST API is at /api/v3 (https://docs.github.com/en/enterprise-server/graphql/guides/forming-calls-with-graphql).
func (c *Client) graphqlURL() string {
	api := strings.TrimSuffix(c.ep.API.String(), "/")
	if strings.HasSuffix(api, "/api/v3") {
		return strings.TrimSuffix(api, "/v3") + "/graphql"
	}
	return api + "/graphql"
}

func (l land) AutoLand(ctx context.Context, repo forge.Repository, number int, s forge.Strategy, enable bool) error {
	rp, err := l.p.repoPath(repo)
	if err != nil {
		return err
	}
	var pr apiPull
	// GET /repos/{o}/{r}/pulls/{n} gives the pull request's node id (https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request)
	if _, err := l.p.c.Call(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", rp, number), nil, &pr); err != nil {
		return err
	}
	if pr.NodeID == "" {
		return errors.New("the host did not give the pull request an id to enable auto-merge with")
	}
	method := map[forge.Strategy]string{forge.StrategyMerge: "MERGE", forge.StrategySquash: "SQUASH", forge.StrategyReplay: "REBASE"}[s]
	q, vars := `mutation($id: ID!) { disablePullRequestAutoMerge(input: {pullRequestId: $id}) { clientMutationId } }`, map[string]any{"id": pr.NodeID}
	if enable {
		if method == "" {
			return fmt.Errorf("%w: auto-merge by %q is not one of merge, squash or replay", forge.ErrInvalid, s)
		}
		q = `mutation($id: ID!, $m: PullRequestMergeMethod!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $m}) { clientMutationId } }`
		vars["m"] = method
	}
	b, _ := json.Marshal(map[string]any{"query": q, "variables": vars})
	a, err := l.p.c.do(ctx, http.MethodPost, l.p.c.graphqlURL(), bytes.NewReader(b), int64(len(b)), "application/json")
	if err != nil {
		return err
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := l.p.c.finish(a, &out, nil); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		// The usual refusal: auto-merge is off for the repository or the pull request is already clean.
		return fmt.Errorf("%w: the host would not change auto-merge: %s", forge.ErrInvalid, out.Errors[0].Message)
	}
	return nil
}
