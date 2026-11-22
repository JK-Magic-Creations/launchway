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
	"attention":              {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"recent":                 {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"organize":               {map[string]any{"action": "list", "area": "pipelines"}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"get_project":            {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_runs":              {map[string]any{"project": "pay-api"}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"get_run":                {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"wait_for_run":           {map[string]any{"run_id": "{pay}", "until": "waiting", "timeout_seconds": 5}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_step_log":           {map[string]any{"run_id": "{pay}", "step": "after"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_test_results":       {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_debrief":            {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_flaky_tests":       {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"split_tests":            {map[string]any{"project": "pay-api", "shards": 2}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_verification":       {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_security_findings":  {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_artifacts":         {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"read_artifact_file":     {map[string]any{"run_id": "{pay}", "artifact_id": "art_nope", "path": "a"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_images":            {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_deployments":        {map[string]any{"project": "pay-api"}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_approvals":         {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_notifications":     {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
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
	// Rollbacks: anyone who sees a project reads its options; the tools that
	// change need the operator role. {pay} is no rollback run and pay-api
	// has no deployments or environment prod: not found after the check.
	"get_rollback_options":  {map[string]any{"project": "pay-api", "environment": "prod"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_rollback_reviews": {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"rollback_deployment":   {map[string]any{"project": "pay-api", "environment": "prod", "reason": "x"}, true, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"review_rollback":       {map[string]any{"run_id": "{pay}", "comment": "x"}, true, [5]string{mUnlisted, mUnlisted, mAllowed, mNotFound, mAllowed}},
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
	"list_users":                 {nil, false, [5]string{mRefused, mRefused, mRefused, mRefused, mOK}},
	"list_service_accounts":      {nil, false, [5]string{mRefused, mRefused, mRefused, mRefused, mOK}},
	"get_ssh_info":               {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_ssh_keys":              {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"add_ssh_key":                {map[string]any{"public_key": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"delete_ssh_key":             {map[string]any{"id": "k"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_deploy_keys":           {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"add_deploy_key":             {map[string]any{"owner": "o", "name": "x", "public_key": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_deploy_key":          {map[string]any{"owner": "o", "name": "x", "id": "k"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_repository_commits":    {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"read_repository_file":       {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"write_repository_file":      {map[string]any{"owner": "o", "name": "x", "path": "a", "content": "b", "message": "c"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_repository_file":     {map[string]any{"owner": "o", "name": "x", "path": "a", "message": "c", "expected_sha": "d"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_repository_branches":   {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_repository_tags":       {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_repository_commit":      {map[string]any{"owner": "o", "name": "x", "sha": "abc"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_repository_diff":        {map[string]any{"owner": "o", "name": "x", "from": "root", "to": "abc"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"blame_repository_file":      {map[string]any{"owner": "o", "name": "x", "path": "a"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"search_repository_code":     {map[string]any{"owner": "o", "name": "x", "query": "a"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"create_repository_branch":   {map[string]any{"owner": "o", "name": "x", "branch": "b"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_repository_branch":   {map[string]any{"owner": "o", "name": "x", "branch": "b"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"upload_repository_files":    {map[string]any{"owner": "o", "name": "x", "message": "m", "files": []any{map[string]any{"path": "a", "content": "YQ=="}}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"rename_repository_branch":   {map[string]any{"owner": "o", "name": "x", "branch": "b", "new_name": "c"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"inspect_repository":         {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_repository_inspections": {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"create_repository_tag":      {map[string]any{"owner": "o", "name": "x", "tag": "b"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_repository_tag":      {map[string]any{"owner": "o", "name": "x", "tag": "b"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_archived_issues":       {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_archived_issue":         {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"import_archive":             {map[string]any{"owner": "o", "name": "x", "document": map[string]any{"issues": []any{}}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_protected_branches":     {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"set_protected_branches":     {map[string]any{"owner": "o", "name": "x", "patterns": []string{"main"}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	// Work tracking: the tools call the routes of the permission table.
	"list_work_items":        {map[string]any{"scope": "project:pay-api"}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"create_work_item":       {map[string]any{"scope": "project:pay-api", "title": "x"}, false, [5]string{mUnlisted, mUnlisted, mOK, mRefused, mOK}},
	"get_work_item":          {map[string]any{"item": "PAY-1"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"update_work_item":       {map[string]any{"item": "PAY-1", "title": "y"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"rank_work_item":         {map[string]any{"item": "PAY-1", "where": "top"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"move_work_item":         {map[string]any{"item": "PAY-1", "state": "done"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"take_over_work_item":    {map[string]any{"item": "PAY-1"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_work_history":       {map[string]any{"item": "PAY-1"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"comment_on_work_item":   {map[string]any{"body": "x", "item": "PAY-1"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"edit_work_comment":      {map[string]any{"body": "x", "id": "wc_nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_work_comment_edits": {map[string]any{"id": "wc_nope"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"link_work_items":        {map[string]any{"item": "PAY-1", "kind": "relates", "to": "PAY-2"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"unlink_work_items":      {map[string]any{"id": "wl_nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"link_work_code":         {map[string]any{"item": "PAY-1", "kind": "pull", "ref": "1", "repo": "o/x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"attach_work_file":       {map[string]any{"item": "PAY-1", "name": "a.txt", "text": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_work_attachment":    {map[string]any{"id": "wa_nope"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_work_views":        {map[string]any{"scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"save_work_view":         {map[string]any{"name": "v", "query": "state:open", "scope": "project:pay-api"}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	"delete_work_view":       {map[string]any{"id": "wv_nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"find_work_by_code":      {map[string]any{"kind": "pull", "ref": "1", "repo": "o/x"}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_work_board":        {map[string]any{"scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_work_milestones":   {map[string]any{"scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"create_work_milestone":  {map[string]any{"scope": "project:pay-api", "title": "m1"}, false, [5]string{mUnlisted, mUnlisted, mOK, mRefused, mAllowed}},
	"update_work_milestone":  {map[string]any{"id": "wm_nope", "title": "m"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_work_sprints":      {map[string]any{"scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"create_work_sprint":     {map[string]any{"end_at": "2026-01-14", "name": "s1", "scope": "project:pay-api", "start_at": "2026-01-01"}, false, [5]string{mUnlisted, mUnlisted, mOK, mRefused, mAllowed}},
	"get_work_sprint":        {map[string]any{"id": "ws_nope"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"update_work_sprint":     {map[string]any{"id": "ws_nope", "name": "s"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"start_work_sprint":      {map[string]any{"id": "ws_nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"complete_work_sprint":   {map[string]any{"id": "ws_nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_work_burndown":      {map[string]any{"id": "ws_nope"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_work_chart":         {map[string]any{"chart": "velocity", "scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_work_roadmap":       {map[string]any{"scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"release_notes":          {map[string]any{"scope": "project:pay-api"}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"get_work_settings":      {map[string]any{"scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"set_work_settings":      {map[string]any{"scope": "project:pay-api", "settings": map[string]any{}}, false, [5]string{mUnlisted, mUnlisted, mRefused, mRefused, mOK}},
	"get_work_inbox":         {map[string]any{}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"mark_work_inbox_read":   {map[string]any{"ids": []any{}}, false, [5]string{mUnlisted, mUnlisted, mOK, mOK, mOK}},
	"get_work_home":          {map[string]any{}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_work_agents":       {map[string]any{}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"create_work_agent":      {map[string]any{"name": "bot", "projects": []any{"pay-api"}, "scope": "project:pay-api"}, false, [5]string{mUnlisted, mUnlisted, mRefused, mRefused, mOK}},
	"get_work_agent":         {map[string]any{"name": "nobot"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"update_work_agent":      {map[string]any{"disabled": true, "name": "nobot"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_work_agent":      {map[string]any{"name": "nobot"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"rotate_agent_token":     {map[string]any{"name": "nobot"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_work_agent_actions": {map[string]any{"name": "nobot"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_work_spend":         {map[string]any{}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"clear_autonomy_pause":   {map[string]any{"reason": "looked", "scope": "project:pay-api"}, false, [5]string{mUnlisted, mUnlisted, mRefused, mRefused, mOK}},
	"list_work_samples":      {map[string]any{}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"complete_work_sample":   {map[string]any{"id": "wsm_nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_work_pull_reviews": {map[string]any{"repo": "o/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"import_work_stories":    {map[string]any{"items": []any{map[string]any{"key": "a1", "title": "t"}}, "preview": true, "scope": "project:pay-api"}, false, [5]string{mUnlisted, mUnlisted, mRefused, mRefused, mOK}},
	"promote_archive":        {map[string]any{"preview": true, "repository": "o/x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_work_proposals":    {map[string]any{"scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"propose_work":           {map[string]any{"scope": "project:pay-api", "source": "lint", "proposals": []any{}}, false, [5]string{mUnlisted, mUnlisted, mOK, mRefused, mOK}},
	"accept_work_proposals":  {map[string]any{"scope": "project:pay-api", "source": "lint", "groups": []any{"g"}}, false, [5]string{mUnlisted, mUnlisted, mOK, mRefused, mOK}},
	"dismiss_work_proposals": {map[string]any{"scope": "project:pay-api", "source": "lint", "groups": []any{"g"}, "reason": "x"}, false, [5]string{mUnlisted, mUnlisted, mOK, mRefused, mOK}},
	"merge_work_proposals":   {map[string]any{"scope": "project:pay-api", "source": "lint", "groups": []any{"g"}, "into": "h"}, false, [5]string{mUnlisted, mUnlisted, mOK, mRefused, mOK}},
	"run_repository_health":  {map[string]any{"repository": "o/x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_autonomy":           {map[string]any{"scope": "project:pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	// Pull requests: with hosting off every call that gets as far as the repository is not found.
	"list_pull_requests":      {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_pull_request":        {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"compare_branches":        {map[string]any{"owner": "o", "name": "x", "head": "f"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_pull_settings":       {map[string]any{"owner": "o", "name": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_pull_owners":         {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_pull_queue":          {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_pull_inbox":          {map[string]any{}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"check_owners_file":       {map[string]any{"owner": "o", "name": "x", "text": ""}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"queue_pull_request":      {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"update_pull_branch":      {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"mark_pull_files_viewed":  {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"create_pull_request":     {map[string]any{"owner": "o", "name": "x", "title": "t", "head": "f"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"update_pull_request":     {map[string]any{"owner": "o", "name": "x", "number": 1, "state": "closed"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"merge_pull_request":      {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"revert_pull_request":     {map[string]any{"owner": "o", "name": "x", "number": 1}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"comment_on_pull_request": {map[string]any{"owner": "o", "name": "x", "number": 1, "body": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"update_pull_comment":     {map[string]any{"owner": "o", "name": "x", "number": 1, "comment_id": "c", "action": "resolve"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"review_pull_request":     {map[string]any{"owner": "o", "name": "x", "number": 1, "verdict": "comment", "body": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"set_pull_settings":       {map[string]any{"owner": "o", "name": "x", "settings": map[string]any{"default_method": "merge"}}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
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
	// The built-in registry (this server has none, so what the permission check lets
	// through is answered 404 feature_off): everyone is told what the API tells them;
	// deleting and policy are offered to operators and refused by the API to all but
	// project admins; collection is offered to an administrator only.
	"registry_status":            {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_registry_images":       {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"inspect_registry_image":     {map[string]any{"project": "pay-api", "repository": "pay-api/x", "reference": "1"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_registry_policy":        {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"preview_registry_retention": {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"delete_registry_image":      {map[string]any{"project": "pay-api", "repository": "pay-api/x", "reference": "1"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"set_registry_policy":        {map[string]any{"project": "pay-api", "immutable_tags": []string{"v*"}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"run_registry_retention":     {map[string]any{"project": "pay-api"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"registry_gc":                {map[string]any{"dry_run": true}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"list_caches":                {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"delete_cache":               {map[string]any{"project": "pay-api", "id": "cch_nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_cache_policy":           {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"set_cache_policy":           {map[string]any{"idle_days": 9}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	// Feature toggles: viewers read; operators make toggles and change them (the
	// safeguards decide the rest); SDK keys are listed for operators and revoked by an admin.
	"list_toggles":                {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_toggle":                  {map[string]any{"project": "pay-api", "toggle": "nope"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"toggle_history":              {map[string]any{"project": "pay-api", "toggle": "nope"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"evaluate_toggle":             {map[string]any{"project": "pay-api", "toggle": "nope", "context": map[string]any{"key": "u"}}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_toggle_changes_waiting": {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"toggle_timeline":             {map[string]any{"project": "pay-api", "environment": "production"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_toggle_safeguards":       {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_toggle_keys":            {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mRefused, mOK, mNotFound, mOK}},
	"plan_toggles_file":           {map[string]any{"project": "pay-api", "file": "toggles: {}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"create_toggle":               {map[string]any{"project": "pay-api", "key": "mcp-made", "type": "boolean", "reason": "test"}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mAllowed}},
	"set_toggle_state":            {map[string]any{"project": "pay-api", "toggle": "nope", "environment": "dev", "reason": "x", "state": map[string]any{"on": true, "off_value": false, "fallthrough": map[string]any{"value": true}}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"revert_toggle_change":        {map[string]any{"project": "pay-api", "change": 999999, "reason": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"approve_toggle_change":       {map[string]any{"project": "pay-api", "change": 999999}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"reject_toggle_change":        {map[string]any{"project": "pay-api", "change": 999999}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"withdraw_toggle_change":      {map[string]any{"project": "pay-api", "change": 999999}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"revert_toggle_changes_since": {map[string]any{"project": "pay-api", "environment": "production", "since": 1, "dry_run": true}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	"update_toggle":               {map[string]any{"project": "pay-api", "toggle": "nope", "patch": map[string]any{"owner": "x"}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_toggle":               {map[string]any{"project": "pay-api", "toggle": "nope", "reason": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"save_toggle_segment":         {map[string]any{"project": "pay-api", "name": "staff", "segment": map[string]any{"included": []string{"a"}}, "reason": "x"}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	"apply_toggles_file":          {map[string]any{"project": "pay-api", "file": "toggles: {}", "reason": "x"}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	"get_toggle_usage":            {map[string]any{"project": "pay-api", "toggle": "nope"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_toggle_schedules":       {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_toggle_segments":        {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"export_toggles_file":         {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"cancel_toggle_schedule":      {map[string]any{"project": "pay-api", "schedule": 999999}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"lock_toggle":                 {map[string]any{"project": "pay-api", "toggle": "nope", "reason": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"unlock_toggle":               {map[string]any{"project": "pay-api", "toggle": "nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_toggle_segment":       {map[string]any{"project": "pay-api", "name": "gone", "reason": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"set_toggle_safeguards":       {map[string]any{"project": "pay-api", "settings": map[string]any{"require_reason": true}}, false, [5]string{mUnlisted, mUnlisted, mRefused, mNotFound, mOK}},
	"revoke_toggle_key":           {map[string]any{"project": "pay-api", "key": "nope"}, false, [5]string{mUnlisted, mUnlisted, mRefused, mNotFound, mNotFound}},
	"list_config_reports":         {map[string]any{"run": "{pay}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_config_report":           {map[string]any{"run": "{pay}", "step": "nope"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_configuration":           {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"list_state":                  {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"show_state":                  {map[string]any{"project": "pay-api", "workspace": "nope"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_state_versions":         {map[string]any{"project": "pay-api", "workspace": "nope"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"rerun_failed_hosts":          {map[string]any{"run": "{pay}", "step": "nope"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"rollback_state":              {map[string]any{"project": "pay-api", "workspace": "nope", "version": 1, "reason": "test"}, false, [5]string{mUnlisted, mUnlisted, mAllowed, mNotFound, mAllowed}},
	"lock_state":                  {map[string]any{"project": "pay-api", "workspace": "nope", "reason": "test"}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mAllowed}},
	"unlock_state":                {map[string]any{"project": "pay-api", "workspace": "nope", "reason": "test"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	// Step resources: anyone reads; only an administrator is offered the
	// tools that change them, as the API refuses everyone else.
	// Standing orders: the test server has none (404 after the permission check); tools that change are listed to operators.
	"get_standards_profile":    {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"get_standards":            {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_standards_settings":   {map[string]any{"level": "installation"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_standards_coverage":   {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_findings":            {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"export_findings":          {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_run_inspection":       {map[string]any{"run_id": "{pay}"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"accept_finding":           {map[string]any{"project": "pay-api", "fingerprint": "abc", "reason": "a good enough reason", "expires": "2027-01-01"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"reopen_finding":           {map[string]any{"project": "pay-api", "fingerprint": "abc"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"get_step_resources":       {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"set_step_resource_limits": {map[string]any{"memory_per_thread": "1Gi"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	// Configuration in the database: anyone reads the overview; the rest is
	// listed to an administrator only, as the API answers an administrator
	// only.
	"get_config_overview":            {nil, false, [5]string{mRefused, mOK, mOK, mOK, mOK}},
	"list_config":                    {map[string]any{"kind": "projects"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"get_config":                     {map[string]any{"kind": "libraries", "name": "none"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"put_config":                     {map[string]any{"kind": "libraries", "name": "mcp-lib", "spec": map[string]any{"repo": "/nonexistent/lib"}}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"delete_config":                  {map[string]any{"kind": "libraries", "name": "none", "overwrite": true}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"restore_config":                 {map[string]any{"kind": "libraries", "name": "none", "overwrite": true}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"get_config_history":             {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"validate_config":                {map[string]any{"changes": []any{map[string]any{"kind": "libraries", "name": "x", "spec": map[string]any{"repo": "/nonexistent/lib"}}}}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"apply_config":                   {map[string]any{"changes": []any{map[string]any{"kind": "libraries", "name": "y", "spec": map[string]any{"repo": "/nonexistent/lib2"}}}}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"get_effective_config":           {map[string]any{"project": "pay-api"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"export_config":                  {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"import_config":                  {map[string]any{"text": "libraries: []\n"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"list_config_imports":            {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"check_repository":               {map[string]any{"repo": "/nonexistent/none"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"check_signin":                   {map[string]any{"issuer": "not a url", "client_id": "x", "redirect_url": "y", "client_secret_env": "Z"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"list_queue":                     {map[string]any{}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"list_event_sources":             {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"test_event_source":              {map[string]any{"project": "pay-api", "source": "x", "body": map[string]any{}}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_event_source":            {map[string]any{"project": "pay-api", "source": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_run_triggers":              {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_graph_run_triggers":         {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_run_trigger_allows":        {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"create_run_trigger":             {map[string]any{"project": "pay-api", "name": "x", "upstream_project": "web"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mAllowed}},
	"follow_graph_with_run_triggers": {map[string]any{"project": "pay-api"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"delete_run_trigger":             {map[string]any{"project": "pay-api", "name": "x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"allow_run_triggers":             {map[string]any{"project": "pay-api", "downstream_project": "nothing"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"revoke_run_triggers":            {map[string]any{"project": "pay-api", "downstream_project": "web"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"list_events":                    {map[string]any{}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"get_event":                      {map[string]any{"event_id": "{evt}"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"replay_event":                   {map[string]any{"event_id": "{evt}"}, false, [5]string{mUnlisted, mUnlisted, mOK, mNotFound, mOK}},
	"trust_run":                      {map[string]any{"run_id": "{pay}"}, true, [5]string{mUnlisted, mUnlisted, mAllowed, mNotFound, mAllowed}},
	"queue_run":                      {map[string]any{"run_id": "{pay}", "action": "hold"}, true, [5]string{mUnlisted, mUnlisted, mAllowed, mNotFound, mAllowed}},
	"list_lanes":                     {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
	"get_run_limits":                 {nil, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"set_run_limits":                 {map[string]any{"max_run_timeout": "48h"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"get_encryption_status":          {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	"set_step_resource_default": {map[string]any{"kind": "step", "cpu": "1", "memory": "2Gi"}, false,
		[5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mOK}},
	// Mirrors of hosted repositories. The matrix server has Git hosting off, so
	// every call that is let through is answered feature_off, which is a not-found (the API's own
	// answer, passed on); a role on the repository decides the rest.
	"list_dependencies":       {map[string]any{"repository": "acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"preview_updates":         {map[string]any{"repository": "acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_mirrors":            {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"set_mirror":              {map[string]any{"repository": "acme/x", "url": "https://git.example.com/x.git"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"sync_mirror":             {map[string]any{"repository": "acme/x", "mirror_id": "mir_x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"stop_mirroring":          {map[string]any{"repository": "acme/x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"change_mirror":           {map[string]any{"repository": "acme/x", "mirror_id": "mir_x", "action": "pause"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"remove_connection":       {map[string]any{"name": "old"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"list_connections":        {nil, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"list_units":              {map[string]any{}, false, [5]string{mOK, mOK, mOK, mOK, mOK}},
	"get_unit":                {map[string]any{"name": "no-such-squadron"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"create_unit":             {map[string]any{"name": "matrix-squadron"}, false, [5]string{mRefused, mRefused, mRefused, mRefused, mOK}},
	"list_project_candidates": {map[string]any{}, false, [5]string{mRefused, mRefused, mRefused, mRefused, mOK}},
	"create_project_in_unit":  {map[string]any{"unit": "no-such-squadron", "repo": "x"}, false, [5]string{mRefused, mRefused, mRefused, mRefused, mNotFound}},
	"preview_project_move":    {map[string]any{"projects": []string{"pay-api"}}, false, [5]string{mRefused, mRefused, mRefused, mRefused, mNotFound}},
	"move_projects":           {map[string]any{"projects": []string{"pay-api"}}, false, [5]string{mRefused, mRefused, mRefused, mRefused, mNotFound}},
	"organize_into_units":     {map[string]any{}, false, [5]string{mRefused, mRefused, mRefused, mRefused, mOK}},
	"add_connection":          {map[string]any{"name": "old", "kind": "gitea", "base_url": "https://git.example.com"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"import_repositories":     {map[string]any{"connection": "old", "dry_run": true}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},
	"verify_repositories":     {map[string]any{"connection": "old"}, false, [5]string{mUnlisted, mUnlisted, mUnlisted, mUnlisted, mNotFound}},

	// The software graph: the matrix server has none, so every question is feature_off, a not-found; the
	// tools that change need the operator role to be listed.
	"graph_summary":               {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"search_graph":                {map[string]any{"query": "x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"get_graph_node":              {map[string]any{"ref": "repository:acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_impact":                {map[string]any{"ref": "repository:acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_where_running":         {map[string]any{"ref": "repository:acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_contents":              {map[string]any{"ref": "repository:acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_path":                  {map[string]any{"from": "repository:acme/x", "to": "repository:acme/y"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_build_order":           {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_rebuild_set":           {map[string]any{"repository": "acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"list_graph_overrides":        {nil, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_requirements":          {map[string]any{"ref": "repository:acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_placement":             {map[string]any{"ref": "repository:acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_machine_specification": {map[string]any{"ref": "repository:acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"graph_vulnerability_reach":   {map[string]any{"ref": "repository:acme/x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
	"add_graph_override":          {map[string]any{"kind": "ignore", "from": "acme/x", "to": "acme/y", "reason": "r"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"remove_graph_override":       {map[string]any{"id": 1}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"propose_requirements_file":   {map[string]any{"repository": "acme/x", "content": "version: 1\n"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"refresh_graph":               {map[string]any{"repository": "acme/x"}, false, [5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	"rescan_graph":                {nil, false, [5]string{mUnlisted, mUnlisted, mRefused, mRefused, mNotFound}},
	"rebuild_graph":               {nil, false, [5]string{mUnlisted, mUnlisted, mRefused, mRefused, mNotFound}},
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
	evs, err := e.eng.EventLog(engine.EventFilter{Project: "pay-api"})
	if err != nil || len(evs) == 0 {
		t.Fatalf("the run left no event: %v %v", evs, err)
	}
	evt := evs[0].ID
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
				if v == "{evt}" {
					v = evt
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
