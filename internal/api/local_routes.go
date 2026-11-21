package api

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
		{"POST /api/v1/account/password", signedIn, "", s.changePassword},
		{"POST /api/v1/account/second-factor", signedIn, "", s.secondFactorBegin},
		{"POST /api/v1/account/second-factor/confirm", signedIn, "", s.secondFactorConfirm},
		{"DELETE /api/v1/account/second-factor", signedIn, "", s.secondFactorRemove},
		{"POST /api/v1/account/recovery-codes", signedIn, "", s.newRecoveryCodes},
		{"POST /api/v1/account/sign-out-everywhere", signedIn, "", s.signOutEverywhere},

		{"GET /api/v1/users", administer, "", s.listUsers},
		{"POST /api/v1/users", administer, "", s.inviteUser},
		{"POST /api/v1/users/{name}/reset", administer, "", s.resetUser},
		{"POST /api/v1/users/{name}", administer, "", s.changeUser},
		{"DELETE /api/v1/users/{name}", administer, "", s.deleteUser},
		{"POST /api/v1/users/{name}/sign-out", administer, "", s.signOutUser},
	}
}
