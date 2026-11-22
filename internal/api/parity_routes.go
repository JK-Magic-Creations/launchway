package api

// parityRoutes are the routes of the repository hosting and release gaps
// closed together: protected tags, release downloads, archives, badges,
// pull request templates, the repository trash, old repository addresses and
// the pull request and overview extras.
func (s *Server) parityRoutes() []route {
	return append(append(append(append(s.tagRuleRoutes(), s.archiveDownloadRoutes()...), s.settingsRoutes()...), s.pullTemplateRoutes()...), append(s.badgeRoutes(), []route{
		{"GET /public/releases/{name}/{version}", public, "", s.publicReleasePage},
	}...)...)
}
