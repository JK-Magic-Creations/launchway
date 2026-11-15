package api

// Rows of the permission table for the routes of run inputs, schedules,
// webhooks and token administration. They are added here, beside the
// table in access_test.go, rather than in it.

func init() {
	for k, v := range map[string]matrixRow{
		"GET /api/v1/projects/{name}/inputs": {"GET", "/api/v1/projects/pay-api/inputs?ref=main", "", [6]int{401, 404, 200, 200, 404, 200}},
		"GET /api/v1/tokens/limits":          {"GET", "/api/v1/tokens/limits", "", [6]int{401, 200, 200, 200, 200, 200}},
		"GET /api/v1/admin/tokens":           {"GET", "/api/v1/admin/tokens", "", [6]int{401, 403, 403, 403, 403, 200}},
		// No such token: 404 after the permission check.
		"DELETE /api/v1/admin/tokens/{id}": {"DELETE", "/api/v1/admin/tokens/tok_nope", "", [6]int{401, 403, 403, 403, 403, 404}},
	} {
		permissionTable[k] = v
	}
}
