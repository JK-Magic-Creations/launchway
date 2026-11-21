package api

// The permission rows of the built-in accounts' routes. This test installation
// signs in through an identity provider only, so a built-in route that passes
// its permission check answers 404 ("not turned on here"); the people are
// anonymous, nobody, vic, otto, dana and ada, as in access_test.go.
func init() {
	off := [6]int{404, 404, 404, 404, 404, 404}
	pub := func(method, path string) matrixRow { return matrixRow{method, path, "{}", off} }
	admin := func(method, path string, ok int) matrixRow {
		return matrixRow{method, path, "{}", [6]int{401, 403, 403, 403, 403, ok}}
	}
	signed := func(method, path string) matrixRow {
		return matrixRow{method, path, "{}", [6]int{401, 404, 404, 404, 404, 404}}
	}
	rows := map[string]matrixRow{
		"GET /api/v1/setup":                          {"GET", "/api/v1/setup", "", [6]int{200, 200, 200, 200, 200, 200}},
		"POST /api/v1/setup/begin":                   pub("POST", "/api/v1/setup/begin"),
		"POST /api/v1/setup/complete":                pub("POST", "/api/v1/setup/complete"),
		"POST /api/v1/auth/local/signin":             pub("POST", "/api/v1/auth/local/signin"),
		"POST /api/v1/auth/local/second-factor":      pub("POST", "/api/v1/auth/local/second-factor"),
		"POST /api/v1/auth/link":                     pub("POST", "/api/v1/auth/link"),
		"POST /api/v1/auth/link/begin":               pub("POST", "/api/v1/auth/link/begin"),
		"POST /api/v1/auth/link/complete":            pub("POST", "/api/v1/auth/link/complete"),
		"GET /api/v1/account":                        {"GET", "/api/v1/account", "", [6]int{401, 404, 404, 404, 404, 404}},
		"POST /api/v1/account/password":              signed("POST", "/api/v1/account/password"),
		"POST /api/v1/account/second-factor":         signed("POST", "/api/v1/account/second-factor"),
		"POST /api/v1/account/second-factor/confirm": signed("POST", "/api/v1/account/second-factor/confirm"),
		"DELETE /api/v1/account/second-factor":       signed("DELETE", "/api/v1/account/second-factor"),
		"POST /api/v1/account/recovery-codes":        signed("POST", "/api/v1/account/recovery-codes"),
		"POST /api/v1/account/sign-out-everywhere":   signed("POST", "/api/v1/account/sign-out-everywhere"),
		"GET /api/v1/users":                          {"GET", "/api/v1/users", "", [6]int{401, 403, 403, 403, 403, 200}},
		"POST /api/v1/users":                         admin("POST", "/api/v1/users", 404),
		"POST /api/v1/users/{name}/reset":            admin("POST", "/api/v1/users/someone/reset", 404),
		"POST /api/v1/users/{name}":                  admin("POST", "/api/v1/users/someone", 404),
		"DELETE /api/v1/users/{name}":                admin("DELETE", "/api/v1/users/someone", 404),
		"POST /api/v1/users/{name}/sign-out":         admin("POST", "/api/v1/users/someone/sign-out", 404),
	}
	for k, v := range rows {
		permissionTable[k] = v
	}
	publicRoutes = append(publicRoutes, "GET /api/v1/setup", "POST /api/v1/setup/begin", "POST /api/v1/setup/complete", "POST /api/v1/auth/local/signin",
		"POST /api/v1/auth/local/second-factor", "POST /api/v1/auth/link", "POST /api/v1/auth/link/begin", "POST /api/v1/auth/link/complete")
}
