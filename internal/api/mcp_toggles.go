package api

// The MCP tools for feature toggles (schema 41). Each calls the same routes as
// the API, so the answer for a caller is the API's: the same permissions, the
// same safeguards (a reason, an approval where one is on), the same audit.
// No tool makes, rotates or shows an SDK key: a secret must not pass through
// an agent's context. Keys are made in the web page or with `launchway toggles keys`.

import (
	"context"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tgProject struct {
	Project string `json:"project" jsonschema:"the Flight's name, as list_projects gives it"`
}

type tgKey struct {
	tgProject
	Toggle string `json:"toggle" jsonschema:"the toggle's key, such as checkout-v2"`
}

type tgChange struct {
	tgProject
	Change int64  `json:"change" jsonschema:"the change's number, from toggle_history or list_toggle_changes_waiting"`
	Reason string `json:"reason,omitempty" jsonschema:"why; required to revert"`
}

// tg calls a toggle route and returns what it answers.
func tg(ctx context.Context, c *mcpCall, tool, method, path string, body any) (map[string]any, error) {
	var out map[string]any
	err := c.apiJSON(ctx, tool, method, path, body, &out)
	return out, err
}

func addToggleTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_toggles", Annotations: readOnly("List a Flight's Switches"),
		Description: "The Switches a Flight sees, each with its state in every Airfield (off, on, targeted by rules, rollout), its owner, stale mentions and changes waiting for approval. A toggle is a value an application reads at run time, so that something can be turned on, off or rolled out to a share of users without a deployment."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgProject) (map[string]any, error) {
			return tg(ctx, c, "list_toggles", http.MethodGet, apiPath("/api/v1/projects/%s/toggles", in.Project), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "get_toggle", Annotations: readOnly("Get a Switch"),
		Description: "One toggle with its definition, its state and rules in each Airfield, lock, stale mentions and when it was last evaluated."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgKey) (map[string]any, error) {
			return tg(ctx, c, "get_toggle", http.MethodGet, apiPath("/api/v1/projects/%s/toggles/%s", in.Project, in.Toggle), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "toggle_history", Annotations: readOnly("A toggle's history"),
		Description: "Every change to a toggle, newest first: who, when, why, how (api, cli, mcp, ui, pipeline, config, schedule, rollback), what it was before and after, whether it was approved and who reverted it."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgKey
			Environment string `json:"environment,omitempty" jsonschema:"only this environment"`
		}) (map[string]any, error) {
			return tg(ctx, c, "toggle_history", http.MethodGet, apiPath("/api/v1/projects/%s/toggles/%s/history", in.Project, in.Toggle)+"?"+url.Values{"environment": {in.Environment}}.Encode(), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "evaluate_toggle", Annotations: readOnly("Try a context against a toggle"),
		Description: "What a toggle serves for a context ({\"key\": \"user-1\", \"plan\": \"pro\"}) in each Airfield, as it is now, with the reason and the rule that matched. Nothing is counted or stored. Use it to check a rule before relying on it."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgKey
			Context     map[string]any `json:"context" jsonschema:"what the application would say about who is asking: key (what percentages are taken over) and attributes"`
			Environment string         `json:"environment,omitempty" jsonschema:"only this environment; all by default"`
		}) (map[string]any, error) {
			return tg(ctx, c, "evaluate_toggle", http.MethodPost, apiPath("/api/v1/projects/%s/toggles/%s/evaluate", in.Project, in.Toggle),
				map[string]any{"context": in.Context, "environment": in.Environment})
		})
	addTool(srv, c, &mcp.Tool{Name: "list_toggle_changes_waiting", Annotations: readOnly("Toggle changes waiting for approval"),
		Description: "Changes to toggles that a safeguard holds until someone other than the person who asked approves them."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgProject) (map[string]any, error) {
			return tg(ctx, c, "list_toggle_changes_waiting", http.MethodGet, apiPath("/api/v1/projects/%s/toggle-changes", in.Project), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "toggle_timeline", Annotations: readOnly("Toggle changes in an Airfield"),
		Description: "The toggle changes in one Airfield, newest first: what the Airfield's page shows beside its deployments, so that \"what changed before it broke?\" has an answer."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			Environment string `json:"environment" jsonschema:"the environment, such as production"`
		}) (map[string]any, error) {
			return tg(ctx, c, "toggle_timeline", http.MethodGet, apiPath("/api/v1/projects/%s/toggle-timeline", in.Project)+"?"+url.Values{"environment": {in.Environment}}.Encode(), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "get_toggle_safeguards", Annotations: readOnly("Toggle safeguards"),
		Description: "The safeguards that apply to changing toggles in each Airfield (a reason, an approval, a watch window), and which level set each. They are on by default; a team may turn them down at its level with a reason, and a level above may lock one."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgProject) (map[string]any, error) {
			return tg(ctx, c, "get_toggle_safeguards", http.MethodGet, apiPath("/api/v1/projects/%s/toggle-safeguards", in.Project), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "list_toggle_keys", Annotations: readOnly("List a Flight's SDK keys"),
		Description: "The SDK keys of a Flight's environments: kind, name, prefix, expiry, last use. Never a secret; keys are made and rotated in the web page or with the CLI. Needs the operator role."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgProject) (map[string]any, error) {
			return tg(ctx, c, "list_toggle_keys", http.MethodGet, apiPath("/api/v1/projects/%s/toggle-keys", in.Project), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "plan_toggles_file", Annotations: readOnly("Compare a toggles file with the live toggles"),
		Description: "How a toggles file (YAML, the same model as the API) differs from the Flight's live toggles: toggles and Airfields to add, to change, and ones that exist only live. Changes nothing."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			File string `json:"file" jsonschema:"the file's text"`
		}) (map[string]any, error) {
			return tg(ctx, c, "plan_toggles_file", http.MethodPost, apiPath("/api/v1/projects/%s/toggle-plan", in.Project), map[string]any{"file": in.File})
		})
	addToggleMoreReadTools(srv, c)
}

func addToggleMoreReadTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_toggle_usage", Annotations: readOnly("Where a toggle is evaluated"),
		Description: "How often applications evaluated a toggle in each Airfield over the last days, per value served, and when it was last evaluated. Counts only: nothing about who was evaluated is stored."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgKey) (map[string]any, error) {
			return tg(ctx, c, "get_toggle_usage", http.MethodGet, apiPath("/api/v1/projects/%s/toggles/%s/usage", in.Project, in.Toggle), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "list_toggle_schedules", Annotations: readOnly("Scheduled toggle changes"),
		Description: "The changes to toggles that are scheduled for a later time, with when each runs and, for those that ran, what happened."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgProject) (map[string]any, error) {
			return tg(ctx, c, "list_toggle_schedules", http.MethodGet, apiPath("/api/v1/projects/%s/toggle-schedules", in.Project)+"?all=true", nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "list_toggle_segments", Annotations: readOnly("List toggle segments"),
		Description: "The segments a Flight sees: named, reusable sets of rules that toggles' rules can name."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgProject) (map[string]any, error) {
			return tg(ctx, c, "list_toggle_segments", http.MethodGet, apiPath("/api/v1/projects/%s/toggle-segments", in.Project), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "export_toggles_file", Annotations: readOnly("Export a Flight's toggles as a file"),
		Description: "The Flight's own toggles and segments as a YAML file (the .launchway/toggles.yaml a repository can hold), for review in a pull request."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgProject) (map[string]any, error) {
			return tg(ctx, c, "export_toggles_file", http.MethodGet, apiPath("/api/v1/projects/%s/toggle-file", in.Project), nil)
		})
	if c.mayOperate {
		addToggleChangeTools(srv, c)
	}
}

func addToggleChangeTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "create_toggle", Annotations: changes("Create a feature toggle", false, false),
		Description: "Make a toggle in a Flight: key, type (boolean, string, number, json), default (what it serves while off), kind (release, kill, experiment, operational, permission), owner, description, an expected removal date (expires, 2027-03-01). It starts off in every Airfield. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			Key         string `json:"key" jsonschema:"lower-case letters, digits and - _ ."`
			Type        string `json:"type" jsonschema:"boolean, string, number or json"`
			Kind        string `json:"kind,omitempty" jsonschema:"release, kill, experiment, operational or permission; release when not given"`
			Default     any    `json:"default,omitempty" jsonschema:"what it serves while off, in its type; false, empty text or 0 when not given"`
			SafeValue   any    `json:"safe_value,omitempty" jsonschema:"for a kill switch, the value that is safe"`
			Description string `json:"description,omitempty" jsonschema:"one line saying what the toggle is for"`
			Owner       string `json:"owner,omitempty" jsonschema:"a person or team who answers for the toggle and will remove it"`
			Expires     string `json:"expires,omitempty" jsonschema:"the date it is expected to be removed, such as 2027-03-01"`
			Reason      string `json:"reason,omitempty" jsonschema:"why the toggle is made"`
		}) (map[string]any, error) {
			body := map[string]any{"key": in.Key, "type": in.Type, "reason": in.Reason}
			for k, v := range map[string]any{"kind": in.Kind, "default": in.Default, "safe_value": in.SafeValue, "description": in.Description, "owner": in.Owner, "expires": in.Expires} {
				if v != nil && v != "" {
					body[k] = v
				}
			}
			return tg(ctx, c, "create_toggle", http.MethodPost, apiPath("/api/v1/projects/%s/toggles", in.Project), body)
		})
	addTool(srv, c, &mcp.Tool{Name: "set_toggle_state", Annotations: changes("Change a toggle in an Airfield", false, true),
		Description: "Set what a toggle does in an environment: state = {on, off_value, prerequisites, rules, fallthrough}. A rule is {id, clauses: [{attribute, op, values, negate}], serve: {value} or {rollout: {by, buckets: [{value, weight}]}}}; weights are hundredths of a percent and add up to 10000. Ops: equals, in, prefix, suffix, contains, exists, in_segment, semver_eq/lt/lte/gt/gte, number_eq/lt/lte/gt/gte. A reason is required, and in a protected environment the change waits for another person's approval (the answer says so). `at` (RFC 3339) schedules it. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgKey
			Environment string         `json:"environment" jsonschema:"the environment, such as production"`
			State       map[string]any `json:"state" jsonschema:"the whole state in the environment: on, off_value, prerequisites, rules, fallthrough"`
			Reason      string         `json:"reason" jsonschema:"why; safeguards require it"`
			At          string         `json:"at,omitempty" jsonschema:"a time like 2026-12-01T09:00:00Z to make the change then instead of now"`
		}) (map[string]any, error) {
			return tg(ctx, c, "set_toggle_state", http.MethodPut, apiPath("/api/v1/projects/%s/toggles/%s/environments/%s", in.Project, in.Toggle, in.Environment),
				map[string]any{"state": in.State, "reason": in.Reason, "at": in.At})
		})
	for _, a := range []struct{ name, route, title, desc string }{
		{"revert_toggle_change", "revert", "Revert a toggle change", "Go back over a change: the Airfield (or the definition) returns to what it was before the change. A reason is required; a revert is never held for approval. The answer says how many later changes it also undid. Audited."},
		{"approve_toggle_change", "approve", "Approve a toggle change", "Approve a change that waits. Someone other than the person who asked, with the operator role in that Airfield. With enough approvals the change is made. Audited."},
		{"reject_toggle_change", "reject", "Reject a toggle change", "Turn a waiting change down, with a comment. Audited."},
		{"withdraw_toggle_change", "withdraw", "Withdraw a toggle change", "Take back a change you asked for that still waits. Audited."},
	} {
		a := a
		addTool(srv, c, &mcp.Tool{Name: a.name, Annotations: changes(a.title, false, false), Description: a.desc},
			mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgChange) (map[string]any, error) {
				return tg(ctx, c, a.name, http.MethodPost, apiPath("/api/v1/projects/%s/toggle-changes/%d/%s", in.Project, in.Change, a.route), map[string]any{"reason": in.Reason, "comment": in.Reason})
			})
	}
	addTool(srv, c, &mcp.Tool{Name: "revert_toggle_changes_since", Annotations: changes("Revert the toggle changes since a time", true, false),
		Description: "Revert every toggle change made in an Airfield since a time (Unix milliseconds; a rollback page gives it), newest first. dry_run lists them without reverting. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			Environment string `json:"environment" jsonschema:"the environment, such as production"`
			Since       int64  `json:"since" jsonschema:"the time, in Unix milliseconds, after which changes are reverted; a rollback page gives it"`
			Reason      string `json:"reason,omitempty" jsonschema:"why they are reverted; safeguards require it"`
			DryRun      bool   `json:"dry_run,omitempty" jsonschema:"only list what would be reverted"`
		}) (map[string]any, error) {
			return tg(ctx, c, "revert_toggle_changes_since", http.MethodPost, apiPath("/api/v1/projects/%s/toggle-revert-since", in.Project),
				map[string]any{"environment": in.Environment, "since": in.Since, "reason": in.Reason, "dry_run": in.DryRun})
		})
	addTool(srv, c, &mcp.Tool{Name: "update_toggle", Annotations: changes("Change a toggle's definition", false, true),
		Description: "Change a toggle's description, owner, kind, expected removal date (expires), tags, default, safe value, or whether client applications may read it (client_visible). Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgKey
			Patch  map[string]any `json:"patch" jsonschema:"the fields to change: description, owner, kind, expires, tags, default, safe_value, client_visible"`
			Reason string         `json:"reason,omitempty" jsonschema:"why it changes"`
		}) (map[string]any, error) {
			body := map[string]any{"reason": in.Reason}
			for k, v := range in.Patch {
				body[k] = v
			}
			return tg(ctx, c, "update_toggle", http.MethodPatch, apiPath("/api/v1/projects/%s/toggles/%s", in.Project, in.Toggle), body)
		})
	addTool(srv, c, &mcp.Tool{Name: "delete_toggle", Annotations: changes("Delete a feature toggle", true, false),
		Description: "Remove a toggle with all its Airfield states. The removal is kept in the history and can be reverted. A reason is required. Audited. Remove the check from the code first."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgKey
			Reason string `json:"reason" jsonschema:"why it is removed"`
		}) (map[string]any, error) {
			return tg(ctx, c, "delete_toggle", http.MethodDelete, apiPath("/api/v1/projects/%s/toggles/%s", in.Project, in.Toggle)+"?"+url.Values{"reason": {in.Reason}}.Encode(), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "save_toggle_segment", Annotations: changes("Save a toggle segment", false, true),
		Description: "Make or replace a segment: a named, reusable set of rules. {included: [keys], excluded: [keys], rules: [{clauses: [...]}]}. A rule that lists people's identifiers can hold personal data: prefer rules on attributes. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			Name    string         `json:"name" jsonschema:"the segment name: lower-case letters, digits and -"`
			Segment map[string]any `json:"segment" jsonschema:"included, excluded (lists of keys) and rules ([{clauses: [...]}])"`
			Reason  string         `json:"reason,omitempty" jsonschema:"why it is saved"`
		}) (map[string]any, error) {
			body := map[string]any{"reason": in.Reason}
			for k, v := range in.Segment {
				body[k] = v
			}
			return tg(ctx, c, "save_toggle_segment", http.MethodPut, apiPath("/api/v1/projects/%s/toggle-segments/%s", in.Project, in.Name), body)
		})
	addTool(srv, c, &mcp.Tool{Name: "apply_toggles_file", Annotations: changes("Apply a toggles file", false, true),
		Description: "Make a Flight's toggles match a file, through the same safeguards as any change: a reason, and an approval where one is on. Never removes what the file leaves out. Run plan_toggles_file first. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			File   string `json:"file" jsonschema:"the toggles file text"`
			Reason string `json:"reason" jsonschema:"why the file is applied; safeguards require it"`
			Origin string `json:"origin,omitempty" jsonschema:"where the file came from, such as config:<commit>"`
		}) (map[string]any, error) {
			return tg(ctx, c, "apply_toggles_file", http.MethodPost, apiPath("/api/v1/projects/%s/toggle-apply", in.Project), map[string]any{"file": in.File, "reason": in.Reason, "origin": in.Origin})
		})
	addToggleAdminTools(srv, c)
}

func addToggleAdminTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "cancel_toggle_schedule", Annotations: changes("Cancel a scheduled toggle change", true, true),
		Description: "Drop a scheduled change that has not run yet. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			Schedule int64 `json:"schedule" jsonschema:"the scheduled change's number, from list_toggle_schedules"`
		}) (map[string]any, error) {
			return tg(ctx, c, "cancel_toggle_schedule", http.MethodDelete, apiPath("/api/v1/projects/%s/toggle-schedules/%d", in.Project, in.Schedule), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "lock_toggle", Annotations: changes("Lock a shared toggle", true, true),
		Description: "Lock a toggle that is shared from a level above its Flights, with a reason: no nearer level may define one of the same key. Needs an administrator of that level. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgKey
			Reason string `json:"reason" jsonschema:"why; it is shown wherever the lock stops someone"`
		}) (map[string]any, error) {
			return tg(ctx, c, "lock_toggle", http.MethodPut, apiPath("/api/v1/projects/%s/toggles/%s/lock", in.Project, in.Toggle), map[string]any{"reason": in.Reason})
		})
	addTool(srv, c, &mcp.Tool{Name: "unlock_toggle", Annotations: changes("Unlock a shared toggle", true, true),
		Description: "Remove the lock of a shared toggle. Needs an administrator of its level. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in tgKey) (map[string]any, error) {
			return tg(ctx, c, "unlock_toggle", http.MethodDelete, apiPath("/api/v1/projects/%s/toggles/%s/lock", in.Project, in.Toggle), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "delete_toggle_segment", Annotations: changes("Delete a toggle segment", true, true),
		Description: "Remove a segment that no toggle's rule names. A reason is required. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			Name   string `json:"name" jsonschema:"the segment's name"`
			Reason string `json:"reason" jsonschema:"why it is removed"`
		}) (map[string]any, error) {
			return tg(ctx, c, "delete_toggle_segment", http.MethodDelete, apiPath("/api/v1/projects/%s/toggle-segments/%s", in.Project, in.Name)+"?"+url.Values{"reason": {in.Reason}}.Encode(), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "set_toggle_safeguards", Annotations: changes("Set toggle safeguards", true, true),
		Description: "Set the safeguards for changing toggles in a Flight's Airfield (or all of them): require_reason, require_approval, approvals (how many people), watch_minutes, and locked (names of safeguards that levels below may not change). Turning one down needs a reason and is recorded. Needs the admin role on the Flight."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			Environment string         `json:"environment,omitempty" jsonschema:"only this environment; all of them when empty"`
			Settings    map[string]any `json:"settings" jsonschema:"require_reason, require_approval (booleans), approvals, watch_minutes (numbers), locked (a list of safeguard names)"`
			Reason      string         `json:"reason,omitempty" jsonschema:"why; required when a safeguard is turned down"`
		}) (map[string]any, error) {
			body := map[string]any{"environment": in.Environment, "reason": in.Reason}
			for k, v := range in.Settings {
				body[k] = v
			}
			return tg(ctx, c, "set_toggle_safeguards", http.MethodPut, apiPath("/api/v1/projects/%s/toggle-safeguards", in.Project), body)
		})
	addTool(srv, c, &mcp.Tool{Name: "revoke_toggle_key", Annotations: changes("Revoke an SDK key", true, true),
		Description: "End an SDK key at once (an application using it stops getting toggles and falls back to its defaults). Needs the admin role. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			tgProject
			Key string `json:"key" jsonschema:"the key's id, from list_toggle_keys"`
		}) (map[string]any, error) {
			return tg(ctx, c, "revoke_toggle_key", http.MethodDelete, apiPath("/api/v1/projects/%s/toggle-keys/%s", in.Project, in.Key), nil)
		})
}
