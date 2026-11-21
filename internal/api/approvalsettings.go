package api

import "net/http"

// ApprovalSettings is what a person deciding an approval needs to know about
// how this installation treats the person who started a run.
type ApprovalSettings struct {
	// AllowSelf is whether the person who started a run may approve it.
	AllowSelf bool `json:"allow_self"`
	// SignIn is whether people sign in; without it everyone is the same user.
	SignIn bool `json:"sign_in"`
}

func (s *Server) approvalSettingsRoutes() []route {
	return []route{{"GET /api/v1/settings/approvals", signedIn, "", s.getApprovalSettings}}
}

func (s *Server) getApprovalSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ApprovalSettings{AllowSelf: s.Engine.SelfApprovalAllowed(), SignIn: s.Auth != nil})
}
