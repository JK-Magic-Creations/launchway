package api

// The MCP tools that change something. They are listed only to callers who
// may operate on some project; the API decides each call regardless, and
// records it in the audit trail as made through MCP.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func addChangeTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "start_run", Annotations: changes("Start a run", false, false),
		Description: "Start a run of a project's pipeline at a branch or tag, with input values. With pipeline_text, start a draft run instead: that uncommitted text runs against the branch's current commit, to try a pipeline before committing it (steps with a when run only if its event names draft; nothing deploys; secrets only if the project allows). Returns the run's ID; then call wait_for_run."},
		mcpCallTimeout, startRun)
	addTool(srv, c, &mcp.Tool{Name: "cancel_run", Annotations: changes("Cancel a run", true, true),
		Description: "Cancel a run that is still going: running steps are stopped and the rest do not start."},
		mcpCallTimeout, cancelRun)
	addTool(srv, c, &mcp.Tool{Name: "retry_run", Annotations: changes("Retry a run", false, false),
		Description: "Resume or re-run a finished run. Without from_step, from_steps and failed_only: RESUME it: the same run goes on, running its failed and canceled steps again, keeping what succeeded and the files its workspace holds (for a day); refused once a deployment opened. With from_step or from_steps: RE-RUN: start a new run of the same commit that runs those steps and every step after them again, reusing the others' results (outputs are kept, workspace files are not; a deploy step always runs again). With failed_only: re-run from the smallest set of steps that covers every failed and canceled step, which is what to use when two independent branches failed and a deployment opened, or when a fresh checkout is wanted. "},
		mcpCallTimeout, retryRun)
	addTool(srv, c, &mcp.Tool{Name: "decide_approval", Annotations: changes("Approve or reject", true, false),
		Description: "Approve or reject an approval step that is waiting. Approving lets the steps after it run; rejecting fails it. The person who started a run usually may not approve it."},
		mcpCallTimeout, decideApproval)
	addTool(srv, c, &mcp.Tool{Name: "control_rollout", Annotations: changes("Control a rollout", true, false),
		Description: "Give a command to a deployment's rollout: promote (to the next step), promote-full (skip the remaining steps), abort (back to the stable version) or retry (an aborted rollout). get_deployments with run_id and deployment says which are possible now. A deploy runner carries the command out."},
		mcpCallTimeout, controlRollout)
}

type startIn struct {
	Project      string            `json:"project"`
	Ref          string            `json:"ref,omitempty" jsonschema:"a branch or tag; default the project's default branch"`
	Inputs       map[string]any    `json:"inputs,omitempty" jsonschema:"values for the pipeline's inputs, by name (get_project lists them); the others take their defaults"`
	Libraries    map[string]string `json:"libraries,omitempty" jsonschema:"use other versions of libraries the pipeline names: library name to tag, branch or commit"`
	PipelineText string            `json:"pipeline_text,omitempty" jsonschema:"run this uncommitted pipeline text as a draft run instead of the committed file"`
}

type startOut struct {
	RunID   string `json:"run_id"`
	Number  int    `json:"number"`
	Draft   bool   `json:"draft,omitempty"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
}

func startRun(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in startIn) (startOut, error) {
	if in.Project == "" {
		return startOut{}, errors.New("say which project")
	}
	ref := in.Ref
	if ref == "" {
		var p projectView
		if err := c.apiJSON(ctx, "start_run", http.MethodGet, apiPath("/api/v1/projects/%s", in.Project), nil, &p); err != nil {
			return startOut{}, err
		}
		ref = p.DefaultBranch
	}
	inputs := map[string]json.RawMessage{}
	for k, v := range in.Inputs {
		data, err := json.Marshal(v)
		if err != nil {
			return startOut{}, fmt.Errorf("input %s: %w", k, err)
		}
		inputs[k] = data
	}
	body := map[string]any{"ref": ref, "libraries": in.Libraries, "inputs": inputs}
	target := apiPath("/api/v1/projects/%s/runs", in.Project)
	if in.PipelineText != "" {
		if len(in.PipelineText) > maxPipelineText {
			return startOut{}, fmt.Errorf("the pipeline text is larger than %d KiB", maxPipelineText>>10)
		}
		body["pipeline"] = in.PipelineText
		target = apiPath("/api/v1/projects/%s/draft-runs", in.Project)
	}
	var run engine.RunSummary
	if err := c.apiJSON(ctx, "start_run", http.MethodPost, target, body, &run); err != nil {
		return startOut{}, explainProblems(err)
	}
	out := startOut{RunID: run.ID, Number: run.Number, Draft: run.Draft, URL: c.runURL(run.ID)}
	out.Summary = fmt.Sprintf("Started run #%d of %s at %s (%s). Call wait_for_run with run_id %s to follow it.", run.Number, run.Project,
		nonEmpty(run.Branch, nonEmpty(run.Tag, ref)), shortSHA(run.SHA), run.ID)
	return out, nil
}

// explainProblems adds a pipeline's problems, line by line, to a refusal
// that carries them.
func explainProblems(err error) error {
	var ae *apiError
	if !errors.As(err, &ae) || ae.Detail == nil {
		return err
	}
	raw, ok := ae.Detail.(json.RawMessage)
	if !ok {
		return err
	}
	var ps pipeline.Problems
	if json.Unmarshal(raw, &ps) != nil || len(ps) == 0 {
		return err
	}
	return fmt.Errorf("%w:\n%s", err, untrusted(problemText(ps), 4000))
}

type okOut struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
}

func cancelRun(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in runIn) (okOut, error) {
	if in.RunID == "" {
		return okOut{}, errors.New("say which run: run_id")
	}
	if err := c.apiJSON(ctx, "cancel_run", http.MethodPost, apiPath("/api/v1/runs/%s/cancel", in.RunID), nil, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: "Canceling; running steps are being stopped. wait_for_run says when it has ended."}, nil
}

type retryIn struct {
	RunID      string   `json:"run_id"`
	FromStep   string   `json:"from_step,omitempty" jsonschema:"re-run from this step in a new run; without it (and without from_steps and failed_only), resume this run"`
	FromSteps  []string `json:"from_steps,omitempty" jsonschema:"re-run from several steps at once in a new run: each runs again with every step after it"`
	FailedOnly bool     `json:"failed_only,omitempty" jsonschema:"re-run in a new run from the smallest set of steps that covers every failed and canceled step"`
}

type retryOut struct {
	RunID   string `json:"run_id" jsonschema:"the run to follow: the same one when resuming, a new one when re-running from a step"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
}

func retryRun(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in retryIn) (retryOut, error) {
	if in.RunID == "" {
		return retryOut{}, errors.New("say which run: run_id")
	}
	if in.FromStep == "" && len(in.FromSteps) == 0 && !in.FailedOnly {
		if err := c.apiJSON(ctx, "retry_run", http.MethodPost, apiPath("/api/v1/runs/%s/resume", in.RunID), nil, nil); err != nil {
			return retryOut{}, err
		}
		return retryOut{RunID: in.RunID, URL: c.runURL(in.RunID), Summary: "Resumed: its failed and canceled steps run again. Call wait_for_run."}, nil
	}
	froms := in.FromSteps
	if in.FromStep != "" {
		froms = append([]string{in.FromStep}, froms...)
	}
	body := map[string]any{"from": froms, "failed": in.FailedOnly}
	var run engine.RunSummary
	if err := c.apiJSON(ctx, "retry_run", http.MethodPost, apiPath("/api/v1/runs/%s/rerun", in.RunID), body, &run); err != nil {
		return retryOut{}, err
	}
	from := strings.Join(run.RerunFroms, ", ")
	if from == "" {
		from = run.RerunFrom
	}
	return retryOut{RunID: run.ID, URL: c.runURL(run.ID),
		Summary: fmt.Sprintf("Started run #%d, re-running from %s. Call wait_for_run with run_id %s.", run.Number, from, run.ID)}, nil
}

type decideIn struct {
	RunID    string `json:"run_id"`
	Step     string `json:"step" jsonschema:"the approval step's ID"`
	Decision string `json:"decision" jsonschema:"approve or reject"`
	Comment  string `json:"comment,omitempty" jsonschema:"why, kept with the decision"`
}

func decideApproval(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in decideIn) (okOut, error) {
	var verb string
	switch strings.ToLower(in.Decision) {
	case "approve":
		verb = "approve"
	case "reject":
		verb = "reject"
	default:
		return okOut{}, errors.New("decision is approve or reject")
	}
	if in.RunID == "" || in.Step == "" {
		return okOut{}, errors.New("say which approval: run_id and step")
	}
	if len(in.Comment) > 1000 {
		return okOut{}, errors.New("the comment is longer than 1000 bytes")
	}
	if err := c.apiJSON(ctx, "decide_approval", http.MethodPost, apiPath("/api/v1/runs/%s/steps/%s/", in.RunID, in.Step)+verb,
		map[string]string{"comment": in.Comment}, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: verb + "d " + in.Step + "."}, nil
}

type rolloutIn struct {
	RunID      string `json:"run_id"`
	Deployment string `json:"deployment" jsonschema:"the deployment's ID (dep_…)"`
	Action     string `json:"action" jsonschema:"promote, promote-full, abort or retry"`
	Comment    string `json:"comment,omitempty" jsonschema:"why, kept with the command"`
}

func controlRollout(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in rolloutIn) (okOut, error) {
	switch in.Action {
	case "promote", "promote-full", "abort", "retry":
	default:
		return okOut{}, errors.New("action is promote, promote-full, abort or retry")
	}
	if in.RunID == "" || in.Deployment == "" {
		return okOut{}, errors.New("say which deployment: run_id and deployment")
	}
	if err := c.apiJSON(ctx, "control_rollout", http.MethodPost, apiPath("/api/v1/runs/%s/deployments/%s/", in.RunID, in.Deployment)+in.Action,
		map[string]string{"comment": in.Comment}, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: "Asked for " + in.Action + "; a deploy runner carries it out. get_deployments shows when it has."}, nil
}
