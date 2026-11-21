package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/sshkeys"
)

// SSH keys, deploy keys and protected branches. A person's keys are theirs:
// they list, add and delete only their own, and a key someone else owns is
// reported as one that does not exist. A repository's deploy keys are its
// administrators'.

func (s *Server) keyRoutes() []route {
	const dk = "/api/v1/repositories/{owner}/{name}/deploy-keys"
	return []route{
		{"GET /api/v1/ssh", signedIn, "", s.sshInfo},
		{"GET /api/v1/user/ssh-keys", signedIn, "", s.listMyKeys},
		{"POST /api/v1/user/ssh-keys", signedIn, "ssh_key.add", s.addMyKey},
		{"DELETE /api/v1/user/ssh-keys/{id}", signedIn, "ssh_key.delete", s.deleteMyKey},
		{"GET " + dk, repoAdmin, "", s.listDeployKeys},
		{"POST " + dk, repoAdmin, "deploy_key.add", s.addDeployKey},
		{"DELETE " + dk + "/{id}", repoAdmin, "deploy_key.delete", s.deleteDeployKey},
		{"GET /api/v1/repositories/{owner}/{name}/protected-branches", repoRead, "", s.getProtected},
		{"PUT /api/v1/repositories/{owner}/{name}/protected-branches", repoAdmin, "repository.protect", s.setProtected},
	}
}

func (s *Server) failKey(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sshkeys.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such key")
	case errors.Is(err, sshkeys.ErrExists):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "that key is already registered: "))
	case errors.Is(err, sshkeys.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid key: "))
	default:
		s.fail(w, err)
	}
}

// sshInfo says whether Git over SSH is on, where, and the host keys to verify.
func (s *Server) sshInfo(w http.ResponseWriter, r *http.Request) {
	out := struct {
		SSHInfo
		HostKeys []sshkeys.HostKey `json:"host_keys"`
	}{SSHInfo: s.sshState(), HostKeys: []sshkeys.HostKey{}}
	if out.Enabled {
		hk, err := s.Forge.Keys.HostKeys(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		out.HostKeys = hk
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sshState() SSHInfo {
	if s.Forge == nil {
		return SSHInfo{}
	}
	return s.Forge.SSH
}

type keyBody struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Write     bool   `json:"write"`
	ExpiresAt string `json:"expires_at"` // RFC 3339, optional
}

func (b keyBody) expiry() (time.Time, error) {
	if b.ExpiresAt == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, b.ExpiresAt)
	if err != nil {
		return t, errors.New("expires_at must be a date and time such as 2027-06-30T00:00:00Z")
	}
	return t, nil
}

func (s *Server) keysOn(w http.ResponseWriter) bool {
	if !s.forgeOn(w) {
		return false
	}
	if s.Forge.Keys == nil {
		writeErr(w, http.StatusNotFound, "SSH keys are not available on this server")
		return false
	}
	return true
}

func (s *Server) listMyKeys(w http.ResponseWriter, r *http.Request) {
	if !s.keysOn(w) {
		return
	}
	ks, err := s.Forge.Keys.List(r.Context(), sshkeys.User, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ks})
}

func (s *Server) addMyKey(w http.ResponseWriter, r *http.Request) {
	if !s.keysOn(w) {
		return
	}
	var in keyBody
	if !decode(w, r, &in) {
		return
	}
	exp, err := in.expiry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := identityFrom(r)
	k, err := s.Forge.Keys.Add(r.Context(), sshkeys.NewKey{Kind: sshkeys.User, Owner: id.User, Name: in.Name, PublicKey: in.PublicKey,
		Groups: id.Groups, CreatedBy: id.User, ExpiresAt: exp, Now: s.Now()})
	if err != nil {
		s.failKey(w, err)
		return
	}
	setAudit(r, id.User, "ssh key "+k.Name+" "+k.Fingerprint)
	writeJSON(w, http.StatusCreated, k)
}

func (s *Server) deleteMyKey(w http.ResponseWriter, r *http.Request) {
	if !s.keysOn(w) {
		return
	}
	k, err := s.Forge.Keys.Get(r.Context(), r.PathValue("id"))
	if err == nil && (k.Kind != sshkeys.User || k.Owner != identityFrom(r).User) {
		err = sshkeys.ErrNotFound // someone else's key does not exist to this caller
	}
	if err == nil {
		err = s.Forge.Keys.Delete(r.Context(), k.ID)
	}
	if err != nil {
		s.failKey(w, err)
		return
	}
	setAudit(r, k.Owner, "ssh key "+k.Name+" "+k.Fingerprint)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) repoOf(w http.ResponseWriter, r *http.Request) (forge.Repository, bool) {
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
	if err != nil {
		s.failForge(w, err)
		return repo, false
	}
	return repo, true
}

func (s *Server) listDeployKeys(w http.ResponseWriter, r *http.Request) {
	if !s.keysOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	ks, err := s.Forge.Keys.List(r.Context(), sshkeys.Deploy, repo.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ks})
}

func (s *Server) addDeployKey(w http.ResponseWriter, r *http.Request) {
	if !s.keysOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in keyBody
	if !decode(w, r, &in) {
		return
	}
	exp, err := in.expiry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	k, err := s.Forge.Keys.Add(r.Context(), sshkeys.NewKey{Kind: sshkeys.Deploy, RepoID: repo.ID, Name: in.Name, PublicKey: in.PublicKey,
		CanWrite: in.Write, CreatedBy: identityFrom(r).User, ExpiresAt: exp, Now: s.Now()})
	if err != nil {
		s.failKey(w, err)
		return
	}
	access := "read-only"
	if k.CanWrite {
		access = "read and write"
	}
	setAudit(r, repo.Path, "deploy key "+k.Name+" "+k.Fingerprint+" ("+access+")")
	writeJSON(w, http.StatusCreated, k)
}

func (s *Server) deleteDeployKey(w http.ResponseWriter, r *http.Request) {
	if !s.keysOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	k, err := s.Forge.Keys.Get(r.Context(), r.PathValue("id"))
	if err == nil && (k.Kind != sshkeys.Deploy || k.RepoID != repo.ID) {
		err = sshkeys.ErrNotFound
	}
	if err == nil {
		err = s.Forge.Keys.Delete(r.Context(), k.ID)
	}
	if err != nil {
		s.failKey(w, err)
		return
	}
	setAudit(r, repo.Path, "deploy key "+k.Name+" "+k.Fingerprint)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getProtected(w http.ResponseWriter, r *http.Request) {
	if !s.forgeOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	pats, err := s.Forge.Rules.Patterns(r.Context(), repo.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patterns": pats})
}

func (s *Server) setProtected(w http.ResponseWriter, r *http.Request) {
	if !s.forgeOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Patterns []string `json:"patterns"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Forge.Rules.Set(r.Context(), repo.ID, in.Patterns, identityFrom(r).User); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pats, _ := s.Forge.Rules.Patterns(r.Context(), repo.ID)
	setAudit(r, repo.Path, "protected branches: "+strings.Join(pats, ", "))
	writeJSON(w, http.StatusOK, map[string]any{"patterns": pats})
}
