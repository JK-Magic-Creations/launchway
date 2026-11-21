package api

// The permission rows of the first-run guide's routes (guide_routes.go). Hosting is off in this test
// installation, so the token paste answers 404 "feature off" to an administrator.
func init() {
	admin := func(method, path, body string, ok int) matrixRow {
		return matrixRow{method, path, body, [6]int{401, 403, 403, 403, 403, ok}}
	}
	permissionTable["GET /api/v1/setup-guide"] = admin("GET", "/api/v1/setup-guide", "", 200)
	permissionTable["POST /api/v1/signin/test"] = admin("POST", "/api/v1/signin/test", "{}", 400)
	permissionTable["GET /api/v1/signin/test"] = admin("GET", "/api/v1/signin/test", "", 200)
	permissionTable["GET /auth/signin-test"] = matrixRow{"GET", "/auth/signin-test", "", [6]int{200, 200, 200, 200, 200, 200}}
	permissionTable["GET /api/v1/encryption/root-key-copy"] = admin("GET", "/api/v1/encryption/root-key-copy", "", 200)
	permissionTable["POST /api/v1/encryption/root-key-copy"] = admin("POST", "/api/v1/encryption/root-key-copy", "{}", 409)
	permissionTable["POST /api/v1/connections/token"] = admin("POST", "/api/v1/connections/token", `{"name":"old","kind":"plain"}`, 404)
	publicRoutes = append(publicRoutes, "GET /auth/signin-test")
}
