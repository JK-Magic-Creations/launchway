package api

// Encryption at rest, as an administrator sees it: which holder keeps the
// root key, the key versions, how many stored secrets are sealed and how
// many remain, and whether a copy of the database alone could reveal a
// secret. It is read-only: the root key is a bootstrap setting held outside
// the database, and rotating or replacing it is done where the key is
// (`launchway encryption`), never through a request.

import (
	"net/http"
)

func (s *Server) encryptionRoutes() []route {
	return []route{
		{"GET /api/v1/encryption", administer, "", s.getEncryption},
	}
}

func (s *Server) getEncryption(w http.ResponseWriter, r *http.Request) {
	svc, err := s.secretStore()
	if err != nil {
		s.fail(w, err)
		return
	}
	rep, err := svc.Report(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
