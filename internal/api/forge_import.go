package api

import (
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// Getting existing repositories into Launchway: connections to the servers
// they are on, import (one repository, an owner or everything a connection
// can see) and verification that every ref equals the source's. All need the
// admin role on every project: they make repositories under any owner.

func (s *Server) importRoutes() []route {
	return []route{
		{"GET /api/v1/connections", administer, "", s.listConnections},
		{"POST /api/v1/connections", administer, "connection.create", s.createConnection},
		{"DELETE /api/v1/connections/{name}", administer, "connection.delete", s.deleteConnection},
		{"POST /api/v1/imports", administer, "repository.import", s.importRepos},
		{"POST /api/v1/imports/verify", administer, "repository.verify", s.verifyRepos},
	}
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	cs, err := ms.Connections(r.Context())
	if err != nil {
		s.failMirror(w, err)
		return
	}
	writeJSON(w, http.StatusOK, forge.Page[mirror.ConnectionView]{Items: cs})
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	var in struct {
		Name       string          `json:"name"`
		Kind       forge.Kind      `json:"kind"`
		BaseURL    string          `json:"base_url"`
		Credential forge.SecretRef `json:"credential"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, in.Name, "")
	v, err := ms.AddConnection(r.Context(), in.Name, in.Kind, in.BaseURL, in.Credential, identityFrom(r).User)
	if err != nil {
		s.failMirror(w, err)
		return
	}
	setAudit(r, v.Name, v.Kind+" "+v.BaseURL+auditCredential(v.Credential))
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	setAudit(r, r.PathValue("name"), "")
	// A pasted token is kept sealed under the connection's name; it goes with the connection.
	var sealedName string
	if cs, err := ms.Connections(r.Context()); err == nil {
		for _, c := range cs {
			if c.Name == r.PathValue("name") && strings.HasPrefix(c.Credential, "sealed:") {
				sealedName = strings.TrimPrefix(c.Credential, "sealed:")
			}
		}
	}
	if err := ms.RemoveConnection(r.Context(), r.PathValue("name")); err != nil {
		s.failMirror(w, err)
		return
	}
	if sealedName != "" {
		if svc, err := s.secretStore(); err == nil {
			if err := svc.DeleteConnectionToken(r.Context(), sealedName); err != nil {
				s.Log.Warn("the sealed token of a removed connection could not be deleted", "connection", sealedName, "error", err)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) importRepos(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	var in mirror.ImportRequest
	if !decode(w, r, &in) {
		return
	}
	what := in.Connection
	if what == "" {
		what = "a list of addresses"
	}
	setAudit(r, what, "")
	res, err := ms.Import(r.Context(), in, identityFrom(r).User)
	if err != nil {
		s.failMirror(w, err)
		return
	}
	var names []string
	for _, it := range res.Items {
		if it.State != mirror.StateSkipped {
			names = append(names, it.Repository+" "+it.State)
		}
	}
	detail := map[bool]string{true: "dry run: ", false: ""}[in.DryRun] + strings.Join(names, ", ")
	if len(detail) > 600 {
		detail = detail[:600] + "…"
	}
	setAudit(r, what, detail)
	writeJSON(w, http.StatusOK, res)
}
