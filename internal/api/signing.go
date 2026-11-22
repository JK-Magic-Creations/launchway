package api

// Verified commits and tags (internal/forge/signing): people's signing keys,
// the verdict on commits and tags of hosted repositories, the installation's
// own signing key, and the rule that a push brings only verified commits.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/signing"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// WellKnownSigningKey is where the installation's public signing key is
// published.
const WellKnownSigningKey = "/.well-known/launchway/signing-key.pub"

func (s *Server) signingRoutes() []route {
	const repo = "/api/v1/repositories/{owner}/{name}"
	return []route{
		{"GET /api/v1/user/signing-keys", signedIn, "", s.listSigningKeys},
		{"POST /api/v1/user/signing-keys", signedIn, "signing_key.add", s.addSigningKey},
		{"DELETE /api/v1/user/signing-keys/{id}", signedIn, "signing_key.delete", s.deleteSigningKey},
		{"POST /api/v1/user/signing-keys/{id}/revoke", signedIn, "signing_key.revoke", s.revokeSigningKey},
		{"GET " + repo + "/signatures", repoRead, "", s.repoSignatures},
		{"GET /api/v1/signing/installation-key", signedIn, "", s.installationKey},
		{"GET " + WellKnownSigningKey, public, "", s.wellKnownSigningKey},
	}
}

// EnableSigning turns on verified commits: it makes the signing service and
// gives the pull request service the means to sign what it makes and to judge
// what a merge brings. It does nothing without Git hosting.
func (s *Server) EnableSigning() {
	if s.Forge == nil || s.Forge.Pulls == nil || s.Forge.Pulls.Git == nil {
		return
	}
	svc := &signing.Service{
		Store: signing.NewStore(s.Engine.Database()),
		Git:   s.Forge.Pulls.Git,
		Clock: s.Now,
		Seal: func(context.Context) signing.Sealer {
			ss, err := s.secretStore()
			if err != nil || !ss.Encrypted() {
				return nil
			}
			return ss.InstallationSigning()
		},
		Emails: func(_ context.Context, user string) []string {
			_, prof, err := s.Engine.LastSignIn(user)
			if err != nil || prof.Email == "" {
				return nil
			}
			return []string{prof.Email}
		},
	}
	s.Signing = svc
	p := s.Forge.Pulls
	p.Sign = svc.SignPayload
	if s.Forge.Forks != nil {
		s.Forge.Forks.Sign = svc.SignPayload
	}
	p.Verify = func(ctx context.Context, objs []storage.PushObject) []signing.Result {
		return svc.VerifyRaw(ctx, objs, "")
	}
}

func (s *Server) signingOn(w http.ResponseWriter) bool {
	if s.Signing == nil {
		writeFeatureOff(w)
		return false
	}
	return true
}

func (s *Server) failSigning(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, signing.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such signing key")
	case errors.Is(err, signing.ErrExists):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "that key is already registered: "))
	case errors.Is(err, signing.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid signing key: "))
	default:
		s.fail(w, err)
	}
}

func (s *Server) listSigningKeys(w http.ResponseWriter, r *http.Request) {
	if !s.signingOn(w) {
		return
	}
	ks, err := s.Signing.Store.List(r.Context(), identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ks})
}

func (s *Server) addSigningKey(w http.ResponseWriter, r *http.Request) {
	if !s.signingOn(w) {
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
	k, err := s.Signing.Store.Add(r.Context(), signing.NewKey{Owner: id.User, Name: in.Name, PublicKey: in.PublicKey, ExpiresAt: exp})
	if err != nil {
		s.failSigning(w, err)
		return
	}
	setAudit(r, id.User, "signing key "+k.Name+" "+k.Fingerprint)
	writeJSON(w, http.StatusCreated, k)
}

// ownSigningKey finds one of the caller's keys; someone else's does not exist.
func (s *Server) ownSigningKey(r *http.Request) (signing.Key, error) {
	k, err := s.Signing.Store.Get(r.Context(), r.PathValue("id"))
	if err == nil && k.Owner != identityFrom(r).User {
		err = signing.ErrNotFound
	}
	return k, err
}

func (s *Server) deleteSigningKey(w http.ResponseWriter, r *http.Request) {
	if !s.signingOn(w) {
		return
	}
	k, err := s.ownSigningKey(r)
	if err == nil {
		err = s.Signing.Store.Delete(r.Context(), k.ID)
	}
	if err != nil {
		s.failSigning(w, err)
		return
	}
	setAudit(r, k.Owner, "signing key "+k.Name+" "+k.Fingerprint)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeSigningKey(w http.ResponseWriter, r *http.Request) {
	if !s.signingOn(w) {
		return
	}
	k, err := s.ownSigningKey(r)
	if err == nil {
		err = s.Signing.Store.Revoke(r.Context(), k.ID)
	}
	if err != nil {
		s.failSigning(w, err)
		return
	}
	k, _ = s.Signing.Store.Get(r.Context(), k.ID)
	setAudit(r, k.Owner, "signing key "+k.Name+" "+k.Fingerprint)
	writeJSON(w, http.StatusOK, k)
}

// repoSignatures answers, for commits and annotated tags of a repository
// named by the query's shas (comma separated), what is known of their
// signatures.
func (s *Server) repoSignatures(w http.ResponseWriter, r *http.Request) {
	if !s.signingOn(w) || !s.forgeOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var shas []string
	for _, p := range strings.Split(r.URL.Query().Get("shas"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			shas = append(shas, p)
		}
	}
	if len(shas) == 0 {
		writeErr(w, http.StatusBadRequest, "say which commits or tags: shas=ID,ID")
		return
	}
	res, err := s.Signing.VerifyRepo(r.Context(), repo.ID, shas)
	if err != nil {
		if strings.Contains(err.Error(), "at most") || strings.Contains(err.Error(), "not an object name") {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.fail(w, err)
		return
	}
	if res == nil {
		res = []signing.Result{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": res})
}

type installationKeyOut struct {
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	Address     string `json:"address"`
}

func (s *Server) installationKey(w http.ResponseWriter, r *http.Request) {
	if !s.signingOn(w) {
		return
	}
	text, fp, err := s.Signing.PublicKey(r.Context())
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, installationKeyOut{PublicKey: text, Fingerprint: fp, Address: WellKnownSigningKey})
}

func (s *Server) wellKnownSigningKey(w http.ResponseWriter, r *http.Request) {
	if s.Signing == nil {
		http.NotFound(w, r)
		return
	}
	text, _, err := s.Signing.PublicKey(r.Context())
	if err != nil {
		http.Error(w, "the installation has no signing key: "+err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	fmt.Fprintln(w, text)
}

// verifiedPush refuses a push that brings a commit that is not verified to a
// branch whose rule asks for verified commits only.
func (s *Server) verifiedPush(ctx context.Context, c gitCaller, repo forge.Repository, ups []githttp.RefUpdate) error {
	if s.Signing == nil || s.Forge == nil || s.Forge.Pulls == nil {
		return nil
	}
	rules, err := s.Forge.Pulls.Store.Rules(ctx, repo.ID)
	if err != nil {
		return errors.New("the repository's branch rules could not be read; nothing was changed")
	}
	var watched []githttp.RefUpdate
	for _, u := range ups {
		br, ok := strings.CutPrefix(u.Name, "refs/heads/")
		if !ok || strings.Trim(u.New, "0") == "" {
			continue
		}
		if rule, covered := pulls.RuleFor(rules, br); covered && rule.VerifiedOnlyPush {
			watched = append(watched, u)
		}
	}
	if len(watched) == 0 {
		return nil
	}
	read := githttp.NewObjectsFrom(ctx)
	if read == nil {
		return errors.New("this branch takes only verified commits, and the pushed commits could not be read to check them; nothing was changed")
	}
	const most = 500
	objs, more, err := read(watched, most)
	if err != nil {
		return fmt.Errorf("this branch takes only verified commits, and checking yours failed (%v); nothing was changed", err)
	}
	if more {
		return fmt.Errorf("this branch takes only verified commits, and the push brings more than %d new commits, which cannot be checked at once; push in smaller parts", most)
	}
	var bad []string
	for _, res := range s.Signing.VerifyRaw(ctx, objs, c.user) {
		if res.Kind == "commit" && !res.OK() {
			bad = append(bad, fmt.Sprintf("%s (%s)", res.Object[:min(10, len(res.Object))], strings.ReplaceAll(string(res.State), "_", " ")))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	if len(bad) > 5 {
		bad = append(bad[:5], fmt.Sprintf("and %d more", len(bad)-5))
	}
	return fmt.Errorf("this branch takes only verified commits, and the push brings commits that are not: %s. Sign them with a key registered under Account, Signing keys (git config gpg.format ssh; git config user.signingkey ~/.ssh/id_ed25519.pub; git commit -S), or rewrite them signed (git rebase --exec 'git commit --amend --no-edit -S' ...)", strings.Join(bad, ", "))
}
