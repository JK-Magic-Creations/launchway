package api

import "time"

// Who may call each tool of releases (see mcpPermissionTable): a viewer is offered no tool that
// changes anything, an operator the tools of the projects they operate, and an administrator the
// ones that need the admin role. The release the rows name does not exist, so the permission check is
// what tells a person with no right (refused, or not found) from one who has it (a not found
// after the check, or the tool's own result).
func init() {
	const none, noV = mUnlisted, mUnlisted
	to := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	ref := map[string]any{"project": "pay-api", "version": "9.9.9"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range ref {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	all := func(w string) [5]string { return [5]string{w, w, w, w, w} }
	notFound := all(mNotFound)
	write := [5]string{none, noV, mNotFound, mNotFound, mNotFound}
	for k, v := range map[string]mcpRow{
		"list_releases":                {map[string]any{"project": "pay-api"}, false, all(mOK)},
		"get_release":                  {ref, false, notFound},
		"propose_next_release_version": {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
		"preview_release_notes":        {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
		"compare_releases":             {map[string]any{"project": "pay-api", "a": "9.9.8", "b": "9.9.9"}, false, notFound},
		"get_release_checklist":        {with(map[string]any{"airfield": "prod"}), false, notFound},
		"get_release_matrix":           {nil, false, all(mOK)},
		"get_release_calendar":         {nil, false, all(mOK)},
		"get_release_report":           {nil, false, all(mOK)},
		"get_release_settings":         {map[string]any{"project": "pay-api"}, false, [5]string{mNotFound, mOK, mOK, mNotFound, mOK}},
		"list_release_freezes":         {nil, false, all(mOK)},
		"get_release_formation":        {nil, false, all(mOK)},

		"create_release":            {map[string]any{"project": "pay-api", "version": "1.0.0"}, false, [5]string{none, noV, mOK, mNotFound, mAllowed}},
		"edit_release":              {with(map[string]any{"title": "x"}), false, write},
		"attach_release_assets":     {with(map[string]any{"run_id": "run_x", "images": true}), false, write},
		"upload_release_file":       {with(map[string]any{"name": "a.bin", "content_base64": "eA=="}), false, write},
		"remove_release_asset":      {with(map[string]any{"name": "a.bin"}), false, write},
		"make_release_candidate":    {ref, false, write},
		"publish_release":           {ref, false, write},
		"withdraw_release":          {with(map[string]any{"reason": "x"}), false, write},
		"delete_draft_release":      {ref, false, write},
		"propose_release_changelog": {ref, false, write},
		"promote_release":           {with(map[string]any{"airfield": "prod"}), false, write},
		"plan_release_promotion":    {with(map[string]any{"airfield": "prod", "at": to}), false, write},
		"give_release_clearance":    {with(map[string]any{"airfield": "prod"}), false, write},
		"return_to_base":            {map[string]any{"project": "pay-api", "airfield": "prod", "reason": "x"}, false, write},
		"set_release_settings":      {map[string]any{"project": "pay-api", "scheme": "semver"}, false, [5]string{none, noV, mRefused, mNotFound, mOK}},
		"set_release_checklist":     {map[string]any{"project": "pay-api", "airfield": "prod"}, false, [5]string{none, noV, mRefused, mNotFound, mNotFound}},
		"set_release_installation":  {map[string]any{"public_downloads": false}, false, [5]string{none, noV, mRefused, mRefused, mOK}},
		"create_release_freeze":     {map[string]any{"project": "pay-api", "to": to, "reason": "audit"}, false, [5]string{none, noV, mRefused, mRefused, mOK}},
		"cancel_release_freeze":     {map[string]any{"id": "frz_nope"}, false, write},
		"create_release_formation":  {map[string]any{"name": "x", "releases": []string{"pay-api@9.9.9", "web@9.9.9"}}, false, [5]string{none, noV, mNotFound, mRefused, mNotFound}},
		"promote_release_formation": {map[string]any{"name": "frm_nope", "airfield": "prod"}, false, write},
		"advance_release_formation": {map[string]any{"name": "frm_nope"}, false, write},
	} {
		mcpPermissionTable[k] = v
	}
}
