package api

// parityRoutes are the routes of the repository hosting and release gaps
// closed together: protected tags, release downloads, archives, badges,
// pull request templates, the repository trash, old repository addresses and
// the pull request and overview extras.
func (s *Server) parityRoutes() []route {
	return append(s.tagRuleRoutes(), []route{
		{"GET /public/releases/{name}/{version}", public, "", s.publicReleasePage},
	}...)
}
