package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// Mirrors of hosted repositories (see internal/forge/mirror): a pull mirror
// keeps a repository equal to an external address; a push mirror copies
// every accepted update to one. Setting one up needs the repository's admin
// role; running one now needs the operator role; reading status needs read.
// Credentials are references (credential.env or credential.file) and are
// never accepted as values, stored or shown.

func (s *Server) mirrorRoutes() []route {
	return []route{
		{"GET /api/v1/mirrors", signedIn, "", s.listAllMirrors},
		{"GET /api/v1/repositories/{owner}/{name}/mirrors", repoRead, "", s.listMirrors},
		{"POST /api/v1/repositories/{owner}/{name}/mirrors", repoAdmin, "mirror.create", s.createMirror},
		{"PATCH /api/v1/repositories/{owner}/{name}/mirrors/{id}", repoAdmin, "mirror.update", s.updateMirror},
		{"DELETE /api/v1/repositories/{owner}/{name}/mirrors/{id}", repoAdmin, "mirror.delete", s.deleteMirror},
		{"POST /api/v1/repositories/{owner}/{name}/mirrors/{id}/sync", repoWrite, "mirror.sync", s.syncMirror},
		{"POST /api/v1/repositories/{owner}/{name}/stop-mirroring", repoAdmin, "mirror.promote", s.promoteMirror},
	}
}

func (s *Server) mirrors(w http.ResponseWriter) (*mirror.Service, bool) {
	if s.Forge == nil || s.Forge.Mirrors == nil {
		writeFeatureOff(w)
		return nil, false
	}
	return s.Forge.Mirrors, true
}

func (s *Server) failMirror(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, forge.ErrNotFound):
		writeErr(w, http.StatusNotFound, strings.TrimPrefix(err.Error(), "not found: "))
	case errors.Is(err, mirror.ErrBusy):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, forge.ErrExists), errors.Is(err, forge.ErrQuota):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "already exists: "))
	case errors.Is(err, forge.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
	default:
		s.fail(w, err)
	}
}

func (s *Server) listAllMirrors(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	all, err := ms.AllViews(r.Context())
	if err != nil {
		s.failMirror(w, err)
		return
	}
	id := identityFrom(r)
	out := make([]mirror.View, 0, len(all))
	for _, v := range all {
		if s.authorize(id, auth.Read, v.Repository) && (r.URL.Query().Get("behind") != "true" || v.Behind) {
			out = append(out, v)
		}
	}
	s.outsideStates(out)
	writeJSON(w, http.StatusOK, forge.Page[mirror.View]{Items: out})
}

// mirrorView is a mirror as shown, with how its releases stand on the outside host.
func (s *Server) mirrorView(ctx context.Context, ms *mirror.Service, m mirror.Mirror) mirror.View {
	vs := []mirror.View{ms.View(ctx, m)}
	s.outsideStates(vs)
	return vs[0]
}

func (s *Server) listMirrors(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	vs, err := ms.Views(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
	if err != nil {
		s.failMirror(w, err)
		return
	}
	s.outsideStates(vs)
	writeJSON(w, http.StatusOK, forge.Page[mirror.View]{Items: vs})
}

// mirrorIn is the body that sets a mirror up.
type mirrorIn struct {
	// Direction is "pull" (the default) or "push".
	Direction string `json:"direction"`
	mirror.Spec
}

func (s *Server) createMirror(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	var in mirrorIn
	if !decode(w, r, &in) {
		return
	}
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	setAudit(r, path, "")
	var m mirror.Mirror
	var err error
	if in.OutsideAPI != "" {
		if err := ms.Guard().Check(r.Context(), in.OutsideAPI); err != nil {
			writeErr(w, http.StatusBadRequest, "outside_api: "+err.Error())
			return
		}
	}
	switch in.Direction {
	case "", mirror.Pull:
		m, err = ms.SetPull(r.Context(), path, in.Spec, identityFrom(r).User)
	case mirror.Push:
		m, err = ms.SetPush(r.Context(), path, in.Spec, identityFrom(r).User)
	default:
		writeErr(w, http.StatusBadRequest, `direction must be "pull" or "push"`)
		return
	}
	if err != nil {
		s.failMirror(w, err)
		return
	}
	v := s.mirrorView(r.Context(), ms, m)
	detail := v.Direction + " mirror of " + v.URL + auditCredential(v.Credential)
	if v.PublishReleases {
		detail += ", publishes releases"
	}
	setAudit(r, path, detail)
	writeJSON(w, http.StatusCreated, v)
}

func auditCredential(ref string) string {
	if ref == "" {
		return ", no credential"
	}
	return ", credential read from " + ref
}

// mirrorOf finds a mirror and checks it belongs to the repository the request
// names, so that admin rights on one repository never reach another's mirror.
func (s *Server) mirrorOf(w http.ResponseWriter, r *http.Request, ms *mirror.Service) (mirror.Mirror, bool) {
	m, err := ms.Get(r.Context(), r.PathValue("id"))
	if err == nil {
		var views []mirror.View
		views, err = ms.Views(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
		found := false
		for _, v := range views {
			found = found || v.ID == m.ID
		}
		if err == nil && !found {
			err = forge.ErrNotFound
		}
	}
	if err != nil {
		s.failMirror(w, err)
		return m, false
	}
	return m, true
}

func (s *Server) updateMirror(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	var in struct {
		Enabled         *bool  `json:"enabled"`
		IntervalSeconds *int64 `json:"interval_seconds"`
		Prune           *bool  `json:"prune"`
		// PublishReleases and OutsideAPI are the outside-release settings of a push mirror.
		PublishReleases *bool   `json:"publish_releases"`
		OutsideAPI      *string `json:"outside_api"`
	}
	if !decode(w, r, &in) {
		return
	}
	m, ok := s.mirrorOf(w, r, ms)
	if !ok {
		return
	}
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	setAudit(r, path, m.Direction+" mirror of "+m.URL)
	var out *mirror.OutsideChange
	if in.PublishReleases != nil || in.OutsideAPI != nil {
		out = &mirror.OutsideChange{PublishReleases: in.PublishReleases, OutsideAPI: in.OutsideAPI}
		if in.OutsideAPI != nil && *in.OutsideAPI != "" {
			if err := ms.Guard().Check(r.Context(), *in.OutsideAPI); err != nil {
				writeErr(w, http.StatusBadRequest, "outside_api: "+err.Error())
				return
			}
		}
		if in.PublishReleases != nil {
			setAudit(r, path, fmt.Sprintf("%s mirror of %s: publish_releases %t", m.Direction, m.URL, *in.PublishReleases))
		}
	}
	got, err := ms.Update(r.Context(), m.ID, in.Enabled, in.IntervalSeconds, in.Prune, out)
	if err != nil {
		s.failMirror(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.mirrorView(r.Context(), ms, got))
}

func (s *Server) deleteMirror(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	m, ok := s.mirrorOf(w, r, ms)
	if !ok {
		return
	}
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	setAudit(r, path, m.Direction+" mirror of "+m.URL)
	if _, err := ms.Remove(r.Context(), m.ID); err != nil {
		s.failMirror(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncMirror(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	m, ok := s.mirrorOf(w, r, ms)
	if !ok {
		return
	}
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	setAudit(r, path, m.Direction+" mirror of "+m.URL)
	got, err := ms.SyncNow(r.Context(), m.ID)
	if err != nil && (errors.Is(err, mirror.ErrBusy) || errors.Is(err, forge.ErrNotFound)) {
		s.failMirror(w, err)
		return
	}
	v := s.mirrorView(r.Context(), ms, got)
	if err != nil {
		// The run failed: the mirror's status says why, redacted.
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": v.LastError, "mirror": v})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) promoteMirror(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	var in struct {
		// FinalSync runs the mirror once more first; if that fails nothing changes.
		FinalSync bool `json:"final_sync"`
	}
	if !decode(w, r, &in) {
		return
	}
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	setAudit(r, path, "")
	m, err := ms.Promote(r.Context(), path, in.FinalSync, identityFrom(r).User)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			writeErr(w, http.StatusConflict, "this repository is not a mirror")
			return
		}
		s.failMirror(w, err)
		return
	}
	detail := "stopped mirroring " + m.URL + "; the repository takes pushes now"
	if in.FinalSync {
		detail += " (after a final sync)"
	}
	setAudit(r, path, detail)
	got, err := s.Forge.Provider.Repositories().Get(r.Context(), path)
	if err != nil {
		s.failForge(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}
