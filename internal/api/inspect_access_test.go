package api

// Rows of the permission table for the routes that look into a delivery
// (inspect.go): the tree and manifests are for viewers, logs and controls
// for operators; the runner routes open to the runner token only.

func init() {
	for k, v := range map[string]matrixRow{
		"GET /api/v1/runs/{id}/deployments/{dep}/inspect":             {"GET", "/api/v1/runs/{pay}/deployments/dep_x/inspect", "", [6]int{401, 404, 404, 404, 404, 404}},
		"GET /api/v1/runs/{id}/deployments/{dep}/inspect/manifest":    {"GET", "/api/v1/runs/{pay}/deployments/dep_x/inspect/manifest?object=ConfigMap/a/b", "", [6]int{401, 404, 404, 404, 404, 404}},
		"GET /api/v1/runs/{id}/deployments/{dep}/inspect/logs":        {"GET", "/api/v1/runs/{pay}/deployments/dep_x/inspect/logs?pod=p", "", [6]int{401, 404, 403, 404, 404, 404}},
		"GET /api/v1/runs/{id}/deployments/{dep}/inspect/logs/stream": {"GET", "/api/v1/runs/{pay}/deployments/dep_x/inspect/logs/stream?pod=p", "", [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/v1/runs/{id}/deployments/{dep}/inspect/control":    {"POST", "/api/v1/runs/{pay}/deployments/dep_x/inspect/control", `{"action":"restart","object":"Rollout/a/b","reason":"x"}`, [6]int{401, 404, 403, 404, 404, 404}},
		"POST /api/runner/v1/deploy/inspect":                          {"POST", "/api/runner/v1/deploy/inspect", "{}", [6]int{401, 401, 401, 401, 401, 401}},
		"POST /api/runner/v1/deploy/inspect-chunk":                    {"POST", "/api/runner/v1/deploy/inspect-chunk", "{}", [6]int{401, 401, 401, 401, 401, 401}},
		"POST /api/runner/v1/deploy/inspect-done":                     {"POST", "/api/runner/v1/deploy/inspect-done", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	} {
		permissionTable[k] = v
	}
}

// Rows of the MCP permission table for the delivery tools. The matrix run
// has no deployment dep_x: not found after the permission check, and a
// viewer is refused the logs.
func init() {
	for k, v := range map[string]mcpRow{
		"get_delivery":          {map[string]any{"run_id": "{pay}", "deployment": "dep_x"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
		"get_delivery_manifest": {map[string]any{"run_id": "{pay}", "deployment": "dep_x", "object": "ConfigMap/a/b"}, false, [5]string{mNotFound, mNotFound, mNotFound, mNotFound, mNotFound}},
		"get_delivery_logs":     {map[string]any{"run_id": "{pay}", "deployment": "dep_x", "pod": "p"}, false, [5]string{mNotFound, mRefused, mNotFound, mNotFound, mNotFound}},
		"control_delivery": {map[string]any{"run_id": "{pay}", "deployment": "dep_x", "action": "restart", "object": "Rollout/a/b", "reason": "x", "confirm": true}, false,
			[5]string{mUnlisted, mUnlisted, mNotFound, mNotFound, mNotFound}},
	} {
		mcpPermissionTable[k] = v
	}
}
