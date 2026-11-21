package api

import "time"

// Rows of the permission table for the routes of releases (schema 57). Nobody signed in is
// refused, a person with no binding sees no such project, a viewer reads, an operator changes
// what an operator may (and an administrator what only one may), and the operator of another
// project sees nothing of pay-api. Where the person is let through to a thing that is not
// there the answer is the handler's 404 (no release 9.9.9 exists), and the dedicated
// release permission test (releases_permissions_test.go) repeats the interesting rows with
// a release that does.

func init() {
	const base = "/api/v1/projects/pay-api"
	for k, v := range map[string]matrixRow{
		"GET /api/v1/projects/{name}/release-settings":                     {"GET", base + "/release-settings", "", [6]int{401, 404, 200, 200, 404, 200}},
		"PUT /api/v1/projects/{name}/release-settings":                     {"PUT", base + "/release-settings", `{"scheme":"semver","tag_prefix":"v"}`, [6]int{401, 404, 403, 403, 404, 200}},
		"GET /api/v1/projects/{name}/releases":                             {"GET", base + "/releases", "", [6]int{401, 404, 200, 200, 404, 200}},
		"POST /api/v1/projects/{name}/releases":                            {"POST", base + "/releases", `{"version":"1.0.0"}`, [6]int{401, 404, 403, 201, 404, 409}},
		"GET /api/v1/projects/{name}/releases/next":                        {"GET", base + "/releases/next", "", [6]int{401, 404, 200, 200, 404, 200}},
		"GET /api/v1/projects/{name}/releases/notes":                       {"GET", base + "/releases/notes", "", [6]int{401, 404, 200, 200, 404, 200}},
		"GET /api/v1/projects/{name}/releases/compare":                     {"GET", base + "/releases/compare?a=1.0.0&b=1.1.0", "", [6]int{401, 404, 404, 404, 404, 404}},
		"GET /api/v1/projects/{name}/releases/{version}":                   {"GET", base + "/releases/9.9.9", "", [6]int{401, 404, 404, 404, 404, 404}},
		"PATCH /api/v1/projects/{name}/releases/{version}":                 {"PATCH", base + "/releases/9.9.9", `{"title":"x"}`, [6]int{401, 404, 403, 404, 404, 404}},
		"DELETE /api/v1/projects/{name}/releases/{version}":                {"DELETE", base + "/releases/9.9.9", "", [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/notes":            {"POST", base + "/releases/9.9.9/notes", `{}`, [6]int{401, 404, 403, 404, 404, 404}},
		"PUT /api/v1/projects/{name}/releases/{version}/assets/{asset}":    {"PUT", base + "/releases/9.9.9/assets/a.bin", "x", [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/assets":           {"POST", base + "/releases/9.9.9/assets", `{"run_id":"x"}`, [6]int{401, 404, 403, 404, 404, 404}},
		"DELETE /api/v1/projects/{name}/releases/{version}/assets/{asset}": {"DELETE", base + "/releases/9.9.9/assets/a.bin", "", [6]int{401, 404, 403, 404, 404, 404}},
		"GET /api/v1/projects/{name}/releases/{version}/assets/{asset}":    {"GET", base + "/releases/9.9.9/assets/a.bin", "", [6]int{401, 404, 404, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/candidate":        {"POST", base + "/releases/9.9.9/candidate", `{}`, [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/publish":          {"POST", base + "/releases/9.9.9/publish", `{}`, [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/yank":             {"POST", base + "/releases/9.9.9/yank", `{"reason":"x"}`, [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/changelog":        {"POST", base + "/releases/9.9.9/changelog", `{}`, [6]int{401, 404, 403, 404, 404, 404}},
		"GET /api/v1/releases":                                             {"GET", "/api/v1/releases", "", [6]int{401, 200, 200, 200, 200, 200}},
		"GET /api/v1/release-matrix":                                       {"GET", "/api/v1/release-matrix", "", [6]int{401, 200, 200, 200, 200, 200}},
		"GET /api/v1/release-calendar":                                     {"GET", "/api/v1/release-calendar", "", [6]int{401, 200, 200, 200, 200, 200}},
		"GET /api/v1/release-report":                                       {"GET", "/api/v1/release-report", "", [6]int{401, 200, 200, 200, 200, 200}},
		"GET /api/v1/release-installation":                                 {"GET", "/api/v1/release-installation", "", [6]int{401, 200, 200, 200, 200, 200}},
		"PUT /api/v1/release-installation":                                 {"PUT", "/api/v1/release-installation", `{"public_downloads":false}`, [6]int{401, 403, 403, 403, 403, 200}},
		"GET /api/v1/release-freezes":                                      {"GET", "/api/v1/release-freezes", "", [6]int{401, 200, 200, 200, 200, 200}},
		"POST /api/v1/release-freezes":                                     {"POST", "/api/v1/release-freezes", `{"to":"` + time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339) + `","reason":"x","project":"pay-api"}`, [6]int{401, 403, 403, 403, 403, 201}},
		"POST /api/v1/release-freezes/{id}/cancel":                         {"POST", "/api/v1/release-freezes/frz_nope/cancel", `{}`, [6]int{401, 404, 404, 404, 404, 404}},
		"GET /api/v1/projects/{name}/release-gates":                        {"GET", base + "/release-gates", "", [6]int{401, 404, 200, 200, 404, 200}},
		"PUT /api/v1/projects/{name}/release-gates/{airfield}":             {"PUT", base + "/release-gates/prod", `{}`, [6]int{401, 404, 403, 403, 404, 404}},
		"GET /api/v1/projects/{name}/releases/{version}/checklist":         {"GET", base + "/releases/9.9.9/checklist?airfield=prod", "", [6]int{401, 404, 404, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/clearances":       {"POST", base + "/releases/9.9.9/clearances", `{"airfield":"prod"}`, [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/promote":          {"POST", base + "/releases/9.9.9/promote", `{"airfield":"prod"}`, [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/projects/{name}/releases/{version}/plan":             {"POST", base + "/releases/9.9.9/plan", `{"airfield":"prod","at":"2099-01-01"}`, [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/projects/{name}/release-returns":                     {"POST", base + "/release-returns", `{"airfield":"prod","reason":"x"}`, [6]int{401, 404, 403, 404, 404, 404}},
		"GET /api/v1/release-formations":                                   {"GET", "/api/v1/release-formations", "", [6]int{401, 200, 200, 200, 200, 200}},
		"POST /api/v1/release-formations":                                  {"POST", "/api/v1/release-formations", `{"name":"x","releases":["pay-api@1.0.0","web@1.0.0"]}`, [6]int{401, 403, 403, 403, 403, 404}},
		"GET /api/v1/release-formations/{id}":                              {"GET", "/api/v1/release-formations/frm_nope", "", [6]int{401, 404, 404, 404, 404, 404}},
		"POST /api/v1/release-formations/{id}/promote":                     {"POST", "/api/v1/release-formations/frm_nope/promote", `{"airfield":"prod"}`, [6]int{401, 404, 404, 404, 404, 404}},
		"POST /api/v1/release-formations/{id}/advance":                     {"POST", "/api/v1/release-formations/frm_nope/advance", `{}`, [6]int{401, 404, 404, 404, 404, 404}},
		"GET /public/releases/{name}/{version}/{asset}":                    {"GET", "/public/releases/pay-api/1.0.0/a.bin", "", [6]int{404, 404, 404, 404, 404, 404}},
	} {
		permissionTable[k] = v
	}
	publicRoutes = append(publicRoutes, "GET /public/releases/{name}/{version}/{asset}")
}
