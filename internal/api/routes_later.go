package api

// laterRoutes are the routes added after schema 14: images, policies,
// reports, MCP, verifications and step resources. They are kept apart from
// the first table so that neither grows without bound.
func (s *Server) laterRoutes() []route {
	return []route{
		// Images (schema 15).
		{"GET /api/v1/runs/{id}/images", runRead, "", s.runImages},
		{"GET /api/v1/images", signedIn, "", s.searchImages},

		// Policies and waivers (schema 16). Waiving needs the role
		// policy.waivers.allow names, which the handlers check.
		{"GET /api/v1/policies", readAny, "", s.listPolicies},
		{"GET /api/v1/runs/{id}/policy", runRead, "", s.runPolicy},
		{"GET /api/v1/waivers", signedIn, "", s.listWaivers},
		{"POST /api/v1/projects/{name}/waivers", projectRead, "policy.waive", s.grantWaiver},
		{"POST /api/v1/projects/{name}/waivers/{waiver}/revoke", projectRead, "policy.revoke_waiver", s.revokeWaiver},

		// Richer test results, coverage and HTML reports (schema 18).
		{"GET /api/v1/runs/{id}/tests/compare", runRead, "", s.runTestCompare},
		{"GET /api/v1/runs/{id}/tests/cases/{case}/attachments/{n}", runRead, "", s.testAttachment},
		{"GET /api/v1/runs/{id}/coverage", runRead, "", s.runCoverage},
		{"GET /api/v1/runs/{id}/coverage/files", runRead, "", s.runCoverageFiles},
		{"GET /api/v1/projects/{name}/coverage", projectRead, "", s.projectCoverage},
		{"GET /api/v1/runs/{id}/reports", runRead, "", s.runReports},
		// A hosted report takes no session: its signed link is the
		// permission (see reports.go).
		{"GET /hosted/{link}/{path...}", public, "", s.hosted},

		// The MCP endpoint for agents. It checks callers itself (only bearer
		// tokens, with limits of their own; see mcp.go), and its tools call
		// the routes above in process, through guard.
		{"POST /mcp", public, "", s.mcpEndpoint},
		{"GET /mcp", public, "", s.mcpEndpoint},
		{"DELETE /mcp", public, "", s.mcpEndpoint},
		{"GET /.well-known/oauth-protected-resource/mcp", public, "", s.protectedResourceMetadata},

		// Verifications and AI models (schema 19).
		{"GET /api/v1/notifications", signedIn, "", s.listNotifications},
		{"POST /api/runner/v1/deploy/pull-request", runnerOnly, "", s.deployPullRequest},
		{"POST /api/runner/v1/deploy/verify/lease", runnerOnly, "", s.verifyLease},
		{"POST /api/runner/v1/deploy/verify/interval", runnerOnly, "", s.verifyInterval},
		{"GET /api/v1/runs/{id}/verifications", runRead, "", s.runVerifications},
		{"GET /api/v1/verifications", signedIn, "", s.listVerifications},
		{"GET /api/v1/runs/{id}/steps/{step}/verify/controls", runRead, "", s.verifyControls},
		{"POST /api/v1/runs/{id}/steps/{step}/verify/override", runOperate, "verification.override", s.overrideVerify},
		{"GET /api/v1/health-sources", signedIn, "", s.listHealthSources},
		{"GET /api/v1/ai/models", signedIn, "", s.listModels},
		{"GET /api/v1/runs/{id}/steps/{step}/explanations", runRead, "", s.stepExplanations},
		{"POST /api/v1/runs/{id}/steps/{step}/explain", runOperate, "step.explain", s.explainStep},

		// Step resources (schema 22): anyone signed in reads the defaults
		// and limits; an administrator changes them.
		{"GET /api/v1/step-resources", signedIn, "", s.getStepResources},
		{"GET /api/v1/step-resources/limits", signedIn, "", s.getStepResourceLimits},
		{"PUT /api/v1/step-resources/limits", administer, "step_resources.set_limits", s.setStepResourceLimits},
		{"GET /api/v1/step-resources/defaults/{kind}", signedIn, "", s.getStepResourceDefault},
		{"PUT /api/v1/step-resources/defaults/{kind}", administer, "step_resources.set_default", s.setStepResourceDefault},
	}
}
