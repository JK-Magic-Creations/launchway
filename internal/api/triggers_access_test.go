package api

// Rows of the permission table for the routes of run inputs, schedules,
// webhooks and token administration. They are added here, beside the
// table in access_test.go, rather than in it.

func init() {
	for k, v := range map[string]matrixRow{
		"GET /api/v1/projects/{name}/inputs": {"GET", "/api/v1/projects/pay-api/inputs?ref=main", "", [6]int{401, 404, 200, 200, 404, 200}},
	} {
		permissionTable[k] = v
	}
}
