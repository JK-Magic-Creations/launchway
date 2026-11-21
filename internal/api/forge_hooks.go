package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
)

// Webhooks of repositories and of owners. A repository's hooks are its
// administrators'; an owner's hooks (every repository of an owner) are an
// administrator's. The secret is write-only: it is never in an answer, except
// that a secret Launchway made is shown once, in the answer that creates the hook.

func (s *Server) hookRoutes() []route {
	var out []route
	for _, sc := range []struct {
		base   string
		access access
	}{{"/api/v1/repositories/{owner}/{name}/hooks", repoAdmin}, {"/api/v1/owners/{owner}/hooks", administer}} {
		out = append(out,
			route{"GET " + sc.base, sc.access, "", s.listHooks},
			route{"POST " + sc.base, sc.access, "webhook.add", s.addHook},
			route{"PATCH " + sc.base + "/{id}", sc.access, "webhook.update", s.updateHook},
			route{"DELETE " + sc.base + "/{id}", sc.access, "webhook.delete", s.deleteHook},
			route{"POST " + sc.base + "/{id}/test", sc.access, "webhook.test", s.testHook},
			route{"GET " + sc.base + "/{id}/deliveries", sc.access, "", s.listDeliveries},
			route{"POST " + sc.base + "/{id}/deliveries/{delivery}/redeliver", sc.access, "webhook.redeliver", s.redeliverHook},
		)
	}
	return out
}

func (s *Server) hooksOn(w http.ResponseWriter) bool {
	if !s.forgeOn(w) {
		return false
	}
	if s.Forge.Hooks == nil {
		writeErr(w, http.StatusNotFound, "webhooks are not available on this server")
		return false
	}
	return true
}

func (s *Server) failHook(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, webhooks.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such webhook")
	case errors.Is(err, webhooks.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid webhook: "))
	default:
		s.fail(w, err)
	}
}

// hookScope is where a request's hooks live: a repository (name in the path)
// or an owner.
type hookScope struct {
	repo  forge.Repository
	owner string
	isOwn bool // an owner's hooks
}

func (s *Server) scopeOf(w http.ResponseWriter, r *http.Request) (hookScope, bool) {
	if r.PathValue("name") == "" {
		return hookScope{owner: r.PathValue("owner"), isOwn: true}, true
	}
	repo, ok := s.repoOf(w, r)
	return hookScope{repo: repo, owner: repo.Home}, ok
}

func (sc hookScope) subject() string {
	if sc.isOwn {
		return sc.owner
	}
	return sc.repo.Path
}

// hookOf finds a hook of this scope; one of another scope does not exist here.
func (s *Server) hookOf(w http.ResponseWriter, r *http.Request, sc hookScope) (webhooks.Hook, bool) {
	h, err := s.Forge.Hooks.Get(r.Context(), r.PathValue("id"))
	if err == nil && (sc.isOwn && (h.RepoID != "" || h.Owner != sc.owner) || !sc.isOwn && h.RepoID != sc.repo.ID) {
		err = webhooks.ErrNotFound
	}
	if err != nil {
		s.failHook(w, err)
		return h, false
	}
	if !sc.isOwn {
		h.Repository = sc.repo.Path
	}
	return h, true
}

func (s *Server) listHooks(w http.ResponseWriter, r *http.Request) {
	if !s.hooksOn(w) {
		return
	}
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return
	}
	var hs []webhooks.Hook
	var err error
	if sc.isOwn {
		hs, err = s.Forge.Hooks.ForOwner(r.Context(), sc.owner)
	} else {
		if hs, err = s.Forge.Hooks.ForRepository(r.Context(), sc.repo.ID); err == nil {
			for i := range hs {
				hs[i].Repository = sc.repo.Path
			}
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": hs, "events": webhooks.Events})
}

type hookBody struct {
	Name   *string  `json:"name"`
	URL    *string  `json:"url"`
	Events []string `json:"events"`
	Active *bool    `json:"active"`
	Secret *string  `json:"secret"`
}

func (s *Server) addHook(w http.ResponseWriter, r *http.Request) {
	if !s.hooksOn(w) {
		return
	}
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return
	}
	var in hookBody
	if !decode(w, r, &in) {
		return
	}
	n := webhooks.NewHook{Events: in.Events, Active: in.Active, CreatedBy: identityFrom(r).User}
	if in.Name != nil {
		n.Name = *in.Name
	}
	if in.URL != nil {
		n.URL = *in.URL
	}
	if in.Secret != nil {
		n.Secret = *in.Secret
	}
	if sc.isOwn {
		n.Owner = sc.owner
	} else {
		n.RepoID = sc.repo.ID
	}
	h, generated, err := s.Forge.Hooks.Create(r.Context(), n)
	if err != nil {
		s.failHook(w, err)
		return
	}
	if !sc.isOwn {
		h.Repository = sc.repo.Path
	}
	setAudit(r, sc.subject(), "webhook "+h.Name+" to "+hostOf(h.URL)+" for "+strings.Join(h.Events, ", "))
	out := map[string]any{"hook": h}
	if generated != "" {
		out["secret"] = generated
		out["secret_note"] = "This secret is shown once. Configure the receiver with it; each delivery carries an HMAC-SHA256 signature in X-Launchway-Signature-256."
	}
	writeJSON(w, http.StatusCreated, out)
}

func hostOf(raw string) string {
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	host, _, _ := strings.Cut(raw, "/")
	return host
}

func (s *Server) updateHook(w http.ResponseWriter, r *http.Request) {
	if !s.hooksOn(w) {
		return
	}
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return
	}
	old, ok := s.hookOf(w, r, sc)
	if !ok {
		return
	}
	var in hookBody
	if !decode(w, r, &in) {
		return
	}
	h, err := s.Forge.Hooks.Update(r.Context(), old.ID, webhooks.Patch{Name: in.Name, URL: in.URL, Events: in.Events, Active: in.Active, Secret: in.Secret})
	if err != nil {
		s.failHook(w, err)
		return
	}
	h.Repository = old.Repository
	detail := "webhook " + h.Name
	if in.Active != nil {
		detail += map[bool]string{true: " turned on", false: " turned off"}[*in.Active]
	}
	if in.Secret != nil {
		detail += " (secret replaced)"
	}
	setAudit(r, sc.subject(), detail)
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) deleteHook(w http.ResponseWriter, r *http.Request) {
	if !s.hooksOn(w) {
		return
	}
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return
	}
	h, ok := s.hookOf(w, r, sc)
	if !ok {
		return
	}
	if err := s.Forge.Hooks.Delete(r.Context(), h.ID); err != nil {
		s.failHook(w, err)
		return
	}
	setAudit(r, sc.subject(), "webhook "+h.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testHook(w http.ResponseWriter, r *http.Request) {
	if !s.hooksOn(w) {
		return
	}
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return
	}
	h, ok := s.hookOf(w, r, sc)
	if !ok {
		return
	}
	repo := webhooks.Repo{Owner: sc.owner}
	if !sc.isOwn {
		repo = s.hookRepo(sc.repo)
	}
	d, err := s.Forge.Hooks.Ping(r.Context(), h.ID, repo, identityFrom(r).User)
	if err != nil {
		s.failHook(w, err)
		return
	}
	setAudit(r, sc.subject(), "test delivery to "+hostOf(h.URL)+": "+d.State)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	if !s.hooksOn(w) {
		return
	}
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return
	}
	h, ok := s.hookOf(w, r, sc)
	if !ok {
		return
	}
	ds, err := s.Forge.Hooks.Deliveries(r.Context(), h.ID, 50)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ds})
}

func (s *Server) redeliverHook(w http.ResponseWriter, r *http.Request) {
	if !s.hooksOn(w) {
		return
	}
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return
	}
	h, ok := s.hookOf(w, r, sc)
	if !ok {
		return
	}
	old, err := s.Forge.Hooks.Delivery(r.Context(), r.PathValue("delivery"))
	if err == nil && old.HookID != h.ID {
		err = webhooks.ErrNotFound
	}
	if err != nil {
		s.failHook(w, err)
		return
	}
	d, err := s.Forge.Hooks.Redeliver(r.Context(), old.ID)
	if err != nil {
		s.failHook(w, err)
		return
	}
	setAudit(r, sc.subject(), "redelivery of "+old.Event+" to "+hostOf(h.URL)+": "+d.State)
	writeJSON(w, http.StatusOK, d)
}
