package api

// configRoutes are the routes of the configuration kept in the database,
// apart from the table of the rest so that neither grows without bound.
func (s *Server) configRoutes() []route {
	return []route{
		// Configuration in the database (schema 23): every object an
		// administrator manages, with its history. The overview names the
		// levels for everyone; the rest needs the admin role everywhere.
		{"GET /api/v1/config", readAny, "", s.configOverview},
		{"GET /api/v1/config/history", administer, "", s.configHistory},
		{"GET /api/v1/config/export", administer, "", s.exportConfig},
		{"POST /api/v1/config/import", administer, "config.import", s.importConfig},
		{"GET /api/v1/config/imports", administer, "", s.configImports},
		{"POST /api/v1/config/validate", administer, "", s.validateConfig},
		{"POST /api/v1/config/check/repository", administer, "config.check_repository", s.checkRepository},
		{"POST /api/v1/config/check/signin", administer, "config.check_signin", s.checkSignIn},
		{"POST /api/v1/config/apply", administer, "config.apply", s.applyConfig},
		{"GET /api/v1/config/effective/{project}", administer, "", s.effectiveConfig},
		{"GET /api/v1/config/{kind}", administer, "", s.listConfig},
		{"POST /api/v1/config/{kind}", administer, "config.create", s.createConfig},
		{"GET /api/v1/config/{kind}/{obj}", administer, "", s.getConfig},
		{"PUT /api/v1/config/{kind}/{obj}", administer, "config.put", s.putConfig},
		{"DELETE /api/v1/config/{kind}/{obj}", administer, "config.delete", s.deleteConfig},
		{"POST /api/v1/config/{kind}/{obj}/restore", administer, "config.restore", s.restoreConfig},
		{"GET /api/v1/config/{kind}/{obj}/history", administer, "", s.configHistory},
	}
}
