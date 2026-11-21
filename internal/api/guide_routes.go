package api

// The routes of the first-run guide (guide_*.go). Everything but the provider test's return address needs
// the admin role: the guide is the administrator's walk-through.
func (s *Server) guideRoutes() []route {
	return []route{
		{"GET /api/v1/setup-guide", administer, "", s.setupGuide},
		{"POST /api/v1/signin/test", administer, "signin.test_begin", s.signinTestBegin},
		{"GET /api/v1/signin/test", administer, "", s.signinTestStatus},
		{"GET " + testPath, public, "", s.signinTestCallback},
		{"GET /api/v1/encryption/root-key-copy", administer, "", s.rootKeyCopyStatus},
		{"POST /api/v1/encryption/root-key-copy", administer, "encryption.root_key_copy", s.rootKeyCopy},
		{"POST /api/v1/connections/token", administer, "connection.create", s.createConnectionWithToken},
	}
}
