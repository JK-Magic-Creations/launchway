package api

// routes is every route of the server, from each file that lists some.
func (s *Server) routes() []route {
	var all []route
	for _, part := range [][]route{s.mainRoutes(), s.laterRoutes(), s.configRoutes(), s.flowRoutes(), s.cacheRoutes(), s.launchRoutes(),
		s.approvalSettingsRoutes(), s.forgeRoutes(), s.rollbackRoutes(), s.localRoutes()} {
		all = append(all, part...)
	}
	return all
}

// The routes of built-in accounts (local_*.go). The sign-in, setup and link
// routes are public: they are how someone becomes signed in, and each checks
// its own secret (a password, the setup code, a one-time link) and is
// rate limited.
func (s *Server) localRoutes() []route {
	return []route{
		{"GET /api/v1/setup", public, "", s.setupStatus},
		{"POST /api/v1/setup/begin", public, "", s.setupBegin},
		{"POST /api/v1/setup/complete", public, "", s.setupComplete},
		{"POST /api/v1/auth/local/signin", public, "", s.localSignIn},
		{"POST /api/v1/auth/local/second-factor", public, "", s.localSecondFactor},
		{"POST /api/v1/auth/link", public, "", s.linkInfo},
		{"POST /api/v1/auth/link/begin", public, "", s.acceptBegin},
		{"POST /api/v1/auth/link/complete", public, "", s.acceptComplete},

		{"GET /api/v1/account", signedIn, "", s.accountView},
		{"POST /api/v1/account/password", signedIn, "auth.local.password_changed", s.changePassword},
		{"POST /api/v1/account/second-factor", signedIn, "auth.local.second_factor_begin", s.secondFactorBegin},
		{"POST /api/v1/account/second-factor/confirm", signedIn, "auth.local.second_factor_added", s.secondFactorConfirm},
		{"DELETE /api/v1/account/second-factor", signedIn, "auth.local.second_factor_removed", s.secondFactorRemove},
		{"POST /api/v1/account/recovery-codes", signedIn, "auth.local.recovery_codes_renewed", s.newRecoveryCodes},
		{"POST /api/v1/account/sign-out-everywhere", signedIn, "auth.sign_out_everywhere", s.signOutEverywhere},

		{"GET /api/v1/users", administer, "", s.listUsers},
		{"POST /api/v1/users", administer, "user.invite", s.inviteUser},
		{"POST /api/v1/users/{name}/reset", administer, "user.reset", s.resetUser},
		{"POST /api/v1/users/{name}", administer, "user.change", s.changeUser},
		{"DELETE /api/v1/users/{name}", administer, "user.delete", s.deleteUser},
		{"POST /api/v1/users/{name}/sign-out", administer, "user.sign_out", s.signOutUser},
	}
}
