package api

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// What a tool call came to, for the MCP permission matrix.
const (
	mOK       = "ok"        // it did what was asked
	mAllowed  = "allowed"   // the permission check let it through; the handler said no for another reason (409, 400)
	mNotFound = "not found" // as if the run or project did not exist
	mRefused  = "refused"   // 403, with the reason
	mUnlisted = "unlisted"  // not offered to this token at all
)

// mcpMatrixPeople are the people of the matrix, in the order of each row's
// expectations (see testPeople): no binding, a viewer of pay-*, an operator
// of pay-*, an operator of web only, and an admin.
var mcpMatrixPeople = []string{"nobody", "vic", "otto", "dana", "ada"}

type mcpRow struct {
	args map[string]any // {pay} is a run of pay-api waiting at its gate
	// fresh gives each person a new run, for calls that change it.
	fresh bool
	want  [5]string
}

// mcpPermissionTable has a row for every tool; TestMCPEveryToolIsInTheTable
// fails for a tool without one.
var mcpPermissionTable = map[string]mcpRow{
	"list_projects":          {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"get_project":            {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_runs":              {map[string]any{"project": "pay-api"}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"get_run":                {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"wait_for_run":           {map[string]any{"run_id": "{pay}", "until": "waiting", "timeout_seconds": 5}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_step_log":           {map[string]any{"run_id": "{pay}", "step": "after"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_test_results":       {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_verification":       {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_security_findings":  {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_artifacts":         {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"read_artifact_file":     {map[string]any{"run_id": "{pay}", "artifact_id": "art_nope", "path": "a"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_images":            {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_deployments":        {map[string]any{"project": "pay-api"}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_approvals":         {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"validate_pipeline":      {map[string]any{"text": "pipeline: x\nsteps:\n  a: {image: alpine, run: true}\n"}, false, [5]string{mRefused, mOK, mOK, mOK, mOK}},
	"get_pipeline_reference": {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_templates":         {nil, false, [5]string{mRefused, mOK, mOK, mOK, mOK}},
	"start_run":              {map[string]any{"project": "pay-api", "ref": "main"}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	"cancel_run":             {map[string]any{"run_id": "{pay}"}, true, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	// Resuming a run that is still going is refused after the check.
	"retry_run":       {map[string]any{"run_id": "{pay}"}, true, [5]string{mUnlisted, mUnlisted, mAllowed, mNotFound, mAllowed}},
	"decide_approval": {map[string]any{"run_id": "{pay}", "step": "gate", "decision": "reject"}, true, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	// {pay} has no deployment dep_x: 404 after the permission check, as
	// on the API (see permissionTable).
	// {pay} has no verify step: not found after the permission check, as
	// on the API.
	"mark_verification": {map[string]any{"run_id": "{pay}", "step": "gate", "outcome": "passed", "reason": "checked by hand"}, true,
		[5]string{mUnlisted, mUnlisted, mAllowed, mNotFound, mAllowed}},
	"control_rollout": {map[string]any{"run_id": "{pay}", "deployment": "dep_x", "action": "promote"}, true, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	// Hosted repositories, with Git hosting off in this server: every call that
	// is offered answers feature_off (not found). Operators are offered the
	// tools that change one, only an administrator the one that creates.
	"list_repositories": {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_repository":    {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"update_repository": {map[string]any{"owner": "o", "name": "x", "visibility": "public"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_repository": {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"create_repository": {map[string]any{"name": "x"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	// Git over SSH, deploy keys, protected branches and webhooks, with hosting off: the SSH status says so plainly; every other call that
	// is offered answers feature_off. Operators are offered the tools that change a repository's keys, branches and hooks.
	"get_ssh_info":            {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_ssh_keys":           {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"add_ssh_key":             {map[string]any{"public_key": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"delete_ssh_key":          {map[string]any{"id": "k"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_deploy_keys":        {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"add_deploy_key":          {map[string]any{"owner": "o", "name": "x", "public_key": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_deploy_key":       {map[string]any{"owner": "o", "name": "x", "id": "k"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_protected_branches":  {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"set_protected_branches":  {map[string]any{"owner": "o", "name": "x", "patterns": []string{"main"}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_webhooks":           {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_webhook_deliveries": {map[string]any{"owner": "o", "name": "x", "id": "h"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"add_webhook":             {map[string]any{"owner": "o", "name": "x", "url": "https://x.example/", "events": []string{"push"}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"update_webhook":          {map[string]any{"owner": "o", "name": "x", "id": "h", "active": false}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_webhook":          {map[string]any{"owner": "o", "name": "x", "id": "h"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"test_webhook":            {map[string]any{"owner": "o", "name": "x", "id": "h"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"redeliver_webhook":       {map[string]any{"owner": "o", "name": "x", "id": "h", "delivery": "d"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	// Build caches: viewers list, operators delete, only an administrator is
	// offered the tool that changes the policy.
	// Variables at levels: viewers read; operators set at a project they operate;
	// locking needs the admin role, which the API decides (tools run in name
	// order, so otto's set_variable follows his refused lock and ada's lock
	// finds his variable).
	"get_effective_variables": {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_variables":          {map[string]any{"level": "installation"}, false, [5]string{mRefused, mOK, mOK, mOK, mOK}},
	"get_run_variables":       {map[string]any{"run": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"set_variable":            {map[string]any{"name": "x", "level": "project", "scope": "pay-api", "type": "string", "value": "v"}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	"delete_variable":         {map[string]any{"name": "gone", "level": "project", "scope": "pay-api"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"lock_variable":           {map[string]any{"name": "x", "level": "project", "scope": "pay-api", "reason": "fixed"}, false, [5]string{mUnlisted, mUnlisted, mRefused, mNotFound, mOK}},
	"unlock_variable":         {map[string]any{"name": "x", "level": "project", "scope": "pay-api"}, false, [5]string{mUnlisted, mUnlisted, mRefused, mNotFound, mOK}},
	"list_caches":             {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"delete_cache":            {map[string]any{"project": "pay-api", "id": "cch_nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_cache_policy":        {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"set_cache_policy":        {map[string]any{"idle_days": 9}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	// Step resources: anyone reads; only an administrator is offered the
	// tools that change them, as the API refuses everyone else.
	"get_step_resources":       {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"set_step_resource_limits": {map[string]any{"memory_per_thread": "1Gi"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	// Configuration in the database: anyone reads the overview; the rest is
	// listed to an administrator only, as the API answers an administrator
	// only.
	"get_config_overview":   {nil, false, [5]string{mRefused, mOK, mOK, mOK, mOK}},
	"list_config":           {map[string]any{"kind": "projects"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"get_config":            {map[string]any{"kind": "libraries", "name": "none"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"put_config":            {map[string]any{"kind": "libraries", "name": "mcp-lib", "spec": map[string]any{"repo": "/nonexistent/lib"}}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"delete_config":         {map[string]any{"kind": "libraries", "name": "none", "overwrite": true}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"restore_config":        {map[string]any{"kind": "libraries", "name": "none", "overwrite": true}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"get_config_history":    {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"validate_config":       {map[string]any{"changes": []any{map[string]any{"kind": "libraries", "name": "x", "spec": map[string]any{"repo": "/nonexistent/lib"}}}}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"apply_config":          {map[string]any{"changes": []any{map[string]any{"kind": "libraries", "name": "y", "spec": map[string]any{"repo": "/nonexistent/lib2"}}}}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"get_effective_config":  {map[string]any{"project": "pay-api"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"export_config":         {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"import_config":         {map[string]any{"text": "libraries: []\n"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"list_config_imports":   {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"check_repository":      {map[string]any{"repo": "/nonexistent/none"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"check_signin":          {map[string]any{"issuer": "not a url", "client_id": "x", "redirect_url": "y", "client_secret_env": "Z"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"list_lanes":            {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_run_limits":        {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"set_run_limits":        {map[string]any{"max_run_timeout": "48h"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"get_encryption_status": {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"set_step_resource_default": {map[string]any{"kind": "step", "cpu": "1", "memory": "2Gi"}, false,
		[5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	// Mirrors of hosted repositories. The matrix server has Git hosting off, so
	// every call that is let through is answered feature_off, which is a not-found (the API's own
	// answer, passed on); a role on the repository decides the rest.
	"list_mirrors":        {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"set_mirror":          {map[string]any{"repository": "acme/x", "url": "https://git.example.com/x.git"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"sync_mirror":         {map[string]any{"repository": "acme/x", "mirror_id": "mir_x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"stop_mirroring":      {map[string]any{"repository": "acme/x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"change_mirror":       {map[string]any{"repository": "acme/x", "mirror_id": "mir_x", "action": "pause"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"remove_connection":   {map[string]any{"name": "old"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"list_connections":    {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"add_connection":      {map[string]any{"name": "old", "kind": "gitea", "base_url": "https://git.example.com"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"import_repositories": {map[string]any{"connection": "old", "dry_run": true}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"verify_repositories": {map[string]any{"connection": "old"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
}

func outcome(errText string) string {
	switch {
	case errText == "":
		return mOK
	case strings.Contains(errText, "unknown tool"):
		return mUnlisted
	case strings.HasPrefix(errText, "not found"):
		return mNotFound
	case strings.HasPrefix(errText, "refused:"):
		return mRefused
	case strings.HasPrefix(errText, "protocol error"), strings.HasPrefix(errText, "the token was refused"), strings.HasPrefix(errText, "the server failed"):
		return "error: " + errText
	}
	return mAllowed
}

// Every tool, called by each person with their own token, does exactly
// what the API lets that person do: the same answer for the same reason.
func TestMCPPermissionMatrix(t *testing.T) {
	e := mcpEnv(t, envOpts{mcp: &MCPOptions{RequestsPerMinute: 2000}})
	pay := e.startRun(t, "pay-api")
	e.startRun(t, "web")
	tools := make([]string, 0, len(mcpPermissionTable))
	for name := range mcpPermissionTable {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	for i, user := range mcpMatrixPeople {
		cs := mcpSession(t, e.mcpURL(), e.tokenFor(t, user, "matrix"), nil, nil)
		listed := toolNames(t, cs)
		for _, name := range tools {
			row := mcpPermissionTable[name]
			run := pay
			if row.fresh {
				run = e.startRun(t, "pay-api")
			}
			args := map[string]any{}
			for k, v := range row.args {
				if v == "{pay}" {
					v = run
				}
				args[k] = v
			}
			_, errText := call(t, cs, name, args)
			got := outcome(errText)
			want := row.want[i]
			if got != want {
				t.Errorf("%s: %s gave %q (%s), want %q", user, name, got, errText, want)
			}
			if (want == mUnlisted) == slices.Contains(listed, name) {
				t.Errorf("%s: %s listed is %v", user, name, slices.Contains(listed, name))
			}
		}
	}
}

// Every tool an admin sees is in the matrix, and the matrix lists no tool
// that does not exist.
func TestMCPEveryToolIsInTheTable(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	cs := mcpSession(t, e.mcpURL(), e.tokenFor(t, "ada", "all"), nil, nil)
	names := toolNames(t, cs)
	for _, n := range names {
		if _, ok := mcpPermissionTable[n]; !ok {
			t.Errorf("the tool %s is not in mcpPermissionTable; add a row saying who may call it", n)
		}
	}
	for n := range mcpPermissionTable {
		if !slices.Contains(names, n) {
			t.Errorf("mcpPermissionTable lists %s, which is not a tool", n)
		}
	}
}

// A token's limits bound its tools: a viewer token of an admin sees no tool
// that changes anything, and one limited to projects sees nothing outside
// them.
func TestMCPTokenLimits(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	web := e.startRun(t, "web")
	ada := e.signIn(t, "ada")
	newToken := func(body string) string {
		r := ada.do("POST", e.srv.URL+"/api/v1/tokens", body)
		var out struct{ Token string }
		r.json(t, &out)
		if out.Token == "" {
			t.Fatalf("making a token: %d %s", r.status, r.body)
		}
		return out.Token
	}
	viewer := mcpSession(t, e.mcpURL(), newToken(`{"name":"look","role":"viewer"}`), nil, nil)
	for _, n := range toolNames(t, viewer) {
		if row := mcpPermissionTable[n]; row.want[4] != mOK && row.want[1] == mUnlisted {
			t.Errorf("a viewer token sees %s", n)
		}
	}
	if _, errText := call(t, viewer, "start_run", map[string]any{"project": "web"}); outcome(errText) != mUnlisted {
		t.Errorf("a viewer token starting a run: %s", errText)
	}
	payOnly := mcpSession(t, e.mcpURL(), newToken(`{"name":"pay","projects":["pay-*"]}`), nil, nil)
	if _, errText := call(t, payOnly, "get_run", map[string]any{"run_id": web}); outcome(errText) != mNotFound {
		t.Errorf("a pay-* token reading a web run: %s", errText)
	}
	if _, errText := call(t, payOnly, "cancel_run", map[string]any{"run_id": web}); outcome(errText) != mNotFound {
		t.Errorf("a pay-* token canceling a web run: %s", errText)
	}
	if out := mustCall(t, payOnly, "list_projects", nil); strings.Contains(asJSON(out), `"web"`) {
		t.Errorf("a pay-* token sees web: %s", asJSON(out))
	}
	// A refusal says why, and is in the audit trail as made through MCP.
	otto := mcpSession(t, e.mcpURL(), e.tokenFor(t, "otto", "otto's agent"), nil, nil)
	own := mustCall(t, otto, "start_run", map[string]any{"project": "pay-api"})
	_, errText := call(t, otto, "decide_approval", map[string]any{"run_id": str(own, "run_id"), "step": "gate", "decision": "approve"})
	if outcome(errText) != mRefused || !strings.Contains(errText, "approve") {
		t.Fatalf("approving one's own run: %s", errText)
	}
	entries, _ := e.eng.AuditLog(engine.AuditFilter{Action: "approval.approve", Limit: 10})
	if len(entries) != 1 || entries[0].Outcome != "denied" || !strings.HasPrefix(entries[0].Detail, `via MCP, token "otto's agent", tool decide_approval`) {
		t.Fatalf("audit: %+v", entries)
	}
}
