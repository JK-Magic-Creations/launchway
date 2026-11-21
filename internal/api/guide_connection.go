package api

// The setup guide's way to give Launchway the token of another Git server once: the token is written
// to a file that only the server's account can read, and the connection then reads it like any other
// file reference (forge.SecretRef{File}). It is never kept in the database, never returned, never
// audited. Where the server has no disk of its own (a Kubernetes pod) the paste is refused with the
// reason, and the guide shows the reference form instead: nothing is offered that cannot work.
//
// AT MERGE TIME with integrate/release: store the token sealed under the root key (internal/sealing)
// and drop the file; the request and response shapes can stay.

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

var (
	credDirMu sync.RWMutex
	credDir   string
)

// SetCredentialDir says where pasted connection tokens are kept: a directory only the server's account
// may read. Empty turns pasting off.
func SetCredentialDir(dir string) {
	credDirMu.Lock()
	credDir = dir
	credDirMu.Unlock()
}

func credentialDir() string {
	credDirMu.RLock()
	defer credDirMu.RUnlock()
	return credDir
}

var connName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// pasteUnavailable says why a pasted token cannot be kept here, or "" when it can.
func pasteUnavailable() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "This server runs in a Kubernetes pod with no disk of its own, so a pasted token would be lost at the next restart. Put the token in a Secret, expose it to the server as an environment variable, and give the variable's name instead."
	}
	if credentialDir() == "" {
		return "This server has no data folder to keep a token in. Give the name of an environment variable or the path of a file that holds it instead."
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
	if why := pasteUnavailable(); why != "" {
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
	dir := credentialDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.fail(w, err)
		return
	}
	path := filepath.Join(dir, "connection-"+in.Name+".token")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a connection called " + in.Name + " already has a token", "code": "conflict", "next": "Choose another name, or remove the connection first."})
			return
		}
		s.fail(w, err)
		return
	}
	_, werr := f.WriteString(tok + "\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		s.fail(w, firstErr(werr, cerr))
		return
	}
	v, err := ms.AddConnection(r.Context(), in.Name, in.Kind, in.BaseURL, forge.SecretRef{File: path}, identityFrom(r).User)
	if err != nil {
		_ = os.Remove(path)
		s.failMirror(w, err)
		return
	}
	setAudit(r, v.Name, v.Kind+" "+v.BaseURL+" with a pasted token kept in a file only the server can read")
	writeJSON(w, http.StatusCreated, v)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
