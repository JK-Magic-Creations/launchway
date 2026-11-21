package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// MCP tools for rolling back after a successful deployment (see
// rollbacks.go and engine/rollbacks.go): the same routes, the same refusals.

func addRollbackReadTools(srv *mcp.Server, c *mcpCall) {
	addReleaseReadTools(srv, c) // releases ride with the delivery tools
	addTool(srv, c, &mcp.Tool{Name: "get_rollback_options", Annotations: readOnly("Get rollback options"),
		Description: "Whether a service of a project can be rolled back in an environment after its deployment succeeded, and to what: the earlier successful deployments (version, when, which run) with, for each, what would be reverted and the consequence and suggestions. When it cannot, the reason and what would help; when another deployment holds the lock, queueable says rollback_deployment with queue true waits behind it."},
		mcpCallTimeout, getRollbackOptions)
	addTool(srv, c, &mcp.Tool{Name: "list_rollback_reviews", Annotations: readOnly("List rollbacks to review"),
		Description: "Emergency rollbacks of protected environments that no approver has acknowledged yet, with whether this token may acknowledge each."},
		mcpCallTimeout, listRollbackReviews)
}

func addRollbackChangeTools(srv *mcp.Server, c *mcpCall) {
	addReleaseChangeTools(srv, c)
	addTool(srv, c, &mcp.Tool{Name: "rollback_deployment", Annotations: changes("Roll back a deployment", true, false),
		Description: "Roll a service back to an earlier successful deployment, after its deployment succeeded. It is a run of its own that reverts in Git, newest first, every deployment after the version asked for, and waits until the application is healthy. A reason is required and recorded. It never rolls back a database or configuration. On a protected environment it is an emergency action: no approval first, and an approver reviews it afterwards (unless a setting requires approval first). get_rollback_options says what is possible and what each choice does."},
		mcpCallTimeout, rollbackDeployment)
	addTool(srv, c, &mcp.Tool{Name: "review_rollback", Annotations: changes("Review a rollback", false, false),
		Description: "Acknowledge an emergency rollback that needed review, with a comment saying what you checked. The person who asked for it usually may not."},
		mcpCallTimeout, reviewRollback)
}

type rollbackOptionsIn struct {
	Project     string `json:"project" jsonschema:"the project's name"`
	Environment string `json:"environment" jsonschema:"the environment's name, such as prod"`
	Service     string `json:"service,omitempty" jsonschema:"default the project's name"`
}

func getRollbackOptions(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in rollbackOptionsIn) (engine.RollbackPlan, error) {
	var plan engine.RollbackPlan
	if in.Project == "" || in.Environment == "" {
		return plan, errors.New("say which project and environment")
	}
	q := url.Values{"environment": {in.Environment}}
	if in.Service != "" {
		q.Set("service", in.Service)
	}
	err := c.apiJSON(ctx, "get_rollback_options", http.MethodGet, apiPath("/api/v1/projects/%s/rollback?", in.Project)+q.Encode(), nil, &plan)
	return plan, err
}

type rollbackIn struct {
	Project     string `json:"project" jsonschema:"the project's name"`
	Environment string `json:"environment" jsonschema:"the environment's name, such as prod"`
	Service     string `json:"service,omitempty" jsonschema:"default the project's name"`
	To          string `json:"to,omitempty" jsonschema:"the deployment (dep_…) or version to restore; default the previous successful deployment"`
	Reason      string `json:"reason" jsonschema:"why, in words; required and recorded"`
	Queue       bool   `json:"queue,omitempty" jsonschema:"wait behind a deployment that holds the environment's lock"`
}

type rollbackOut struct {
	RunID   string `json:"run_id"`
	Number  int    `json:"number"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
}

func rollbackDeployment(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in rollbackIn) (rollbackOut, error) {
	if in.Project == "" || in.Environment == "" {
		return rollbackOut{}, errors.New("say which project and environment")
	}
	var run engine.RunSummary
	body := map[string]any{"environment": in.Environment, "service": in.Service, "to": in.To, "reason": in.Reason, "queue": in.Queue}
	if err := c.apiJSON(ctx, "rollback_deployment", http.MethodPost, apiPath("/api/v1/projects/%s/rollbacks", in.Project), body, &run); err != nil {
		return rollbackOut{}, err
	}
	return rollbackOut{RunID: run.ID, Number: run.Number, URL: c.runURL(run.ID),
		Summary: fmt.Sprintf("Started rollback run #%d: %s. %s Call wait_for_run with run_id %s.", run.Number, untrusted(run.Message, 400), engine.RollbackNote, run.ID)}, nil
}

type reviewListOut struct {
	Reviews []engine.ReviewItem `json:"reviews"`
}

func listRollbackReviews(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ struct{}) (reviewListOut, error) {
	out := reviewListOut{Reviews: []engine.ReviewItem{}}
	err := c.apiJSON(ctx, "list_rollback_reviews", http.MethodGet, "/api/v1/rollbacks/review", nil, &out.Reviews)
	return out, err
}

type reviewIn struct {
	RunID   string `json:"run_id" jsonschema:"the rollback run"`
	Comment string `json:"comment" jsonschema:"what you checked; required and recorded"`
}

func reviewRollback(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in reviewIn) (okOut, error) {
	if in.RunID == "" {
		return okOut{}, errors.New("say which rollback run: run_id")
	}
	if err := c.apiJSON(ctx, "review_rollback", http.MethodPost, apiPath("/api/v1/runs/%s/review", in.RunID), map[string]string{"comment": in.Comment}, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: "Acknowledged the rollback; it is no longer waiting for review."}, nil
}
