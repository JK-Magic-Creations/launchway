package tfstate

import (
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // the protocol's Content-MD5 integrity check, not security
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Errors an Authenticator returns.
var (
	ErrUnauthorized = errors.New("this address needs credentials")
	ErrForbidden    = errors.New("these credentials may not use this state")
)

// Authenticator decides who a request is and whether it may read or write
// the state of a project's workspace.
type Authenticator interface {
	Authenticate(r *http.Request, project, workspace string, write bool) (Actor, error)
}

// Handler speaks the HTTP backend protocol of Terraform and OpenTofu:
//
//	GET    {address}   the current state, or 404
//	POST   {address}   a new state (?ID=<lock id> when the client holds a lock)
//	DELETE {address}   remove the state
//	LOCK   {lock}      take the lock; the body is the lock document;
//	                   423 with the holder's document when it is taken
//	UNLOCK {lock}      release it
//
// The address is /state/v1/{project}/{workspace}; the lock address is the
// same with /lock. The same instance serves both.
type Handler struct {
	Store *Store
	Auth  Authenticator
	// Audit is told of every change that is not routine (delete, a lock
	// taken, released), with a short word and the actor.
	Audit func(r *http.Request, action, project, workspace string, who Actor, detail string)
	// Log is told of failures the client is not shown the reason for.
	Log func(msg string, err error)
}

// Serve handles one request for project and workspace; lock says that the
// request is for the lock address.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, project, workspace string, lock bool) {
	write := r.Method != http.MethodGet
	who, err := h.Auth.Authenticate(r, project, workspace, write)
	switch {
	case errors.Is(err, ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", `Basic realm="launchway state"`)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	case err != nil:
		http.Error(w, ErrForbidden.Error(), http.StatusForbidden)
		return
	}
	ctx := r.Context()
	switch {
	case lock && r.Method == "LOCK":
		info, ok := readBody(w, r, 1<<20)
		if !ok {
			return
		}
		l, err := h.Store.Lock(ctx, project, workspace, info, who)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.audit(r, "state.lock", project, workspace, who, l.ID)
		w.WriteHeader(http.StatusOK)
	case lock && r.Method == "UNLOCK":
		info, ok := readBody(w, r, 1<<20)
		if !ok {
			return
		}
		var in struct {
			ID string `json:"ID"`
		}
		_ = json.Unmarshal(info, &in)
		if err := h.Store.Unlock(ctx, project, workspace, in.ID); err != nil {
			h.fail(w, err)
			return
		}
		h.audit(r, "state.unlock", project, workspace, who, in.ID)
		w.WriteHeader(http.StatusOK)
	case lock:
		http.Error(w, "the lock address takes LOCK and UNLOCK", http.StatusMethodNotAllowed)
	case r.Method == http.MethodGet:
		_, body, err := h.Store.Pull(ctx, project, workspace, 0)
		if err != nil {
			h.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	case r.Method == http.MethodPost || r.Method == http.MethodPut:
		body, ok := readBody(w, r, MaxStateBytes)
		if !ok {
			return
		}
		if want := r.Header.Get("Content-MD5"); want != "" {
			sum := md5.Sum(body) //nolint:gosec
			if base64.StdEncoding.EncodeToString(sum[:]) != want {
				http.Error(w, "the body does not match Content-MD5", http.StatusBadRequest)
				return
			}
		}
		if _, err := h.Store.Put(ctx, project, workspace, r.URL.Query().Get("ID"), body, who, "write", false); err != nil {
			h.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete:
		if err := h.Store.Delete(ctx, project, workspace); err != nil {
			h.fail(w, err)
			return
		}
		h.audit(r, "state.delete", project, workspace, who, "")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) audit(r *http.Request, action, project, ws string, who Actor, detail string) {
	if h.Audit != nil {
		h.Audit(r, action, project, ws, who, detail)
	}
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		http.Error(w, "reading the body: "+err.Error(), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return b, true
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	var le *LockedError
	switch {
	case errors.As(err, &le):
		// The protocol: the holder's lock document, so the client prints it.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusLocked)
		_, _ = w.Write(le.Held.Info)
	case errors.Is(err, ErrNotFound):
		http.Error(w, "no state yet", http.StatusNotFound)
	case errors.Is(err, ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrNoKey):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		if h.Log != nil {
			h.Log("the state backend failed", err)
		}
		http.Error(w, "the state could not be stored or read", http.StatusInternalServerError)
	}
}

// ---- run-scoped credentials ----

// TokenPrefix starts every state credential, so a log scanner and a person
// can tell what it is.
const TokenPrefix = "lwst_"

// Claims are what a state credential is good for: one project's one
// workspace, for one step of one run, until it expires.
type Claims struct {
	Project   string `json:"p"`
	Workspace string `json:"w"`
	RunID     string `json:"r"`
	Step      string `json:"s"`
	Expires   int64  `json:"e"` // unix seconds
	ReadOnly  bool   `json:"ro,omitempty"`
}

// Tokens makes and checks state credentials with a key the servers of an
// installation share. A credential is stateless, so it needs no row; it dies
// with its expiry, which is the step's lease.
type Tokens struct {
	Key []byte
	Now func() time.Time
}

func (t Tokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Mint makes a credential valid for ttl.
func (t Tokens) Mint(c Claims, ttl time.Duration) string {
	c.Expires = t.now().Add(ttl).Unix()
	raw, _ := json.Marshal(c)
	body := base64.RawURLEncoding.EncodeToString(raw)
	return TokenPrefix + body + "." + t.sign(body)
}

func (t Tokens) sign(body string) string {
	m := hmac.New(sha256.New, t.Key)
	m.Write([]byte("launchway-state-credential\x00" + body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify checks a credential and returns its claims.
func (t Tokens) Verify(tok string) (Claims, error) {
	var c Claims
	rest, ok := strings.CutPrefix(tok, TokenPrefix)
	if !ok {
		return c, ErrUnauthorized
	}
	body, sig, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(t.sign(body))) {
		return c, ErrUnauthorized
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return c, ErrUnauthorized
	}
	if t.now().Unix() > c.Expires {
		return c, errors.New("the state credential has expired")
	}
	return c, nil
}
