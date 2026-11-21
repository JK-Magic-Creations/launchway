package api

// The setup guide's way to give Launchway the token of another Git server once: the token is kept
// SEALED in the database under the root key (internal/sealing), and the connection refers to it by
// name (forge.SecretRef "sealed:NAME"), like any other credential reference. It is never returned,
// never logged and never audited; removing the connection removes the token. Without a root key the
// paste is refused with the reason and the way to get one: nothing is offered that would keep a
// token in the clear.

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

var connName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// pasteUnavailable says why a pasted token cannot be kept here, or "" when it can.
func (s *Server) pasteUnavailable() string {
	svc, err := s.secretStore()
	if err != nil || !svc.Encrypted() {
		return "This server has no root key, so a pasted token would be kept in the clear and is refused. Create a root key : " + keyring.HowToCreateARootKey + " or give the name of an environment variable or the path of a file that holds the token instead."
	}
	return ""
}

type connTokenIn struct {
	Name     string     `json:"name"`
	Kind     forge.Kind `json:"kind"`
	BaseURL  string     `json:"base_url"`
	Token    string     `json:"token"`
	Username string     `json:"username,omitempty"`
}

// POST /api/v1/connections/token
func (s *Server) createConnectionWithToken(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	var in connTokenIn
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, in.Name, "")
	bad := func(msg, next string) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg, "code": "invalid", "next": next})
	}
	if why := s.pasteUnavailable(); why != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": why, "code": "unavailable", "next": "Add the connection with a credential reference (launchway repos connection add NAME --credential-env VARIABLE)."})
		return
	}
	if !connName.MatchString(in.Name) {
		bad("the name must be lower case letters, digits, - and _ (at most 63)", "Choose a short name such as oldgit.")
		return
	}
	tok := strings.TrimSpace(in.Token)
	if tok == "" || len(tok) > 4096 || strings.ContainsAny(tok, "\r\n\x00") {
		bad("the token is empty, too long or has line breaks", "Paste the token exactly as the other server shows it, on one line.")
		return
	}
	svc, _ := s.secretStore()
	if err := svc.PutConnectionToken(r.Context(), in.Name, tok); err != nil {
		if errors.Is(err, sealing.ErrTokenExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a connection called " + in.Name + " already has a token", "code": "conflict", "next": "Choose another name, or remove the connection first."})
			return
		}
		s.fail(w, err)
		return
	}
	v, err := ms.AddConnection(r.Context(), in.Name, in.Kind, in.BaseURL, forge.Sealed(in.Name), identityFrom(r).User)
	if err != nil {
		_ = svc.DeleteConnectionToken(r.Context(), in.Name)
		s.failMirror(w, err)
		return
	}
	setAudit(r, v.Name, v.Kind+" "+v.BaseURL+" with a pasted token kept sealed")
	writeJSON(w, http.StatusCreated, v)
}
