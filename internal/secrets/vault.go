package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultJWTFile is where Kubernetes mounts a pod's service account token.
const DefaultJWTFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// vaultTimeout bounds every request to Vault.
const vaultTimeout = 10 * time.Second

// Vault reads the KV version 2 secrets engine of HashiCorp Vault or OpenBao
// over its HTTP API. It logs in with a token, or with the Kubernetes auth
// method, and keeps the login token until shortly before it expires.
type Vault struct {
	addr      string // without a trailing slash
	mount     string
	namespace string
	client    *http.Client

	// One way to log in: tokenEnv, tokenFile, or role (Kubernetes auth).
	tokenEnv  string
	tokenFile string
	role      string
	authPath  string
	jwtFile   string

	now     func() time.Time
	mu      sync.Mutex
	token   string
	expires time.Time // zero: the token does not expire
}

// Get reads one field of the secret at key under the store's mount.
func (v *Vault) Get(ctx context.Context, key, field string) (string, error) {
	if field == "" {
		return "", notFound("vault secrets need a field, for example field: password")
	}
	if !cleanPath(key) {
		return "", notFound("key %q must be a path such as apps/harbor/robot, without empty, . or .. parts", key)
	}
	tok, err := v.loginToken(ctx)
	if err != nil {
		return "", err
	}
	val, err := v.read(ctx, tok, key, field)
	if err == errForbidden {
		// The token may have expired or been revoked: log in again, once.
		v.forget(tok)
		if tok, err = v.loginToken(ctx); err != nil {
			return "", err
		}
		val, err = v.read(ctx, tok, key, field)
	}
	if err == errForbidden {
		return "", notFound("permission denied reading %s/%s", v.mount, key)
	}
	return val, err
}

// errForbidden is a 403 from Vault, before logging in again.
var errForbidden = &NotFoundError{Reason: "permission denied"}

func (v *Vault) read(ctx context.Context, tok, key, field string) (string, error) {
	var body struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	status, err := v.do(ctx, http.MethodGet, "/v1/"+escapePath(v.mount)+"/data/"+escapePath(key), tok, nil, &body)
	switch {
	case err != nil:
		return "", err
	case status == http.StatusForbidden:
		return "", errForbidden
	case status == http.StatusNotFound:
		return "", notFound("there is no secret at %s/%s", v.mount, key)
	}
	raw, ok := body.Data.Data[field]
	if !ok {
		return "", notFound("the secret at %s/%s has no field %q", v.mount, key, field)
	}
	if s, ok := raw.(string); ok {
		return s, nil
	}
	b, _ := json.Marshal(raw) // a number, a boolean or an object, as JSON
	return string(b), nil
}

// loginToken returns the cached token, or logs in for a new one.
func (v *Vault) loginToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	tok, exp := v.token, v.expires
	v.mu.Unlock()
	if tok != "" && (exp.IsZero() || v.now().Before(exp)) {
		return tok, nil
	}
	tok, ttl, err := v.login(ctx)
	if err != nil {
		return "", err
	}
	v.mu.Lock()
	v.token, v.expires = tok, time.Time{}
	if ttl > 0 {
		// Renew a little early, so a token never expires mid-request.
		v.expires = v.now().Add(ttl - ttl/10)
	}
	v.mu.Unlock()
	return tok, nil
}

// forget drops tok from the cache, unless another lookup already replaced it.
func (v *Vault) forget(tok string) {
	v.mu.Lock()
	if v.token == tok {
		v.token = ""
	}
	v.mu.Unlock()
}

// login returns a token and how long it lasts (zero: unknown or forever).
func (v *Vault) login(ctx context.Context) (string, time.Duration, error) {
	switch {
	case v.tokenEnv != "":
		tok := strings.TrimSpace(os.Getenv(v.tokenEnv))
		if tok == "" {
			return "", 0, notFound("the vault token variable %s is empty", v.tokenEnv)
		}
		return tok, 0, nil
	case v.tokenFile != "":
		b, err := os.ReadFile(v.tokenFile)
		if err != nil {
			return "", 0, notFound("reading the vault token file: %v", err)
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", 0, notFound("the vault token file %s is empty", v.tokenFile)
		}
		return tok, 0, nil
	}
	jwt, err := os.ReadFile(v.jwtFile)
	if err != nil {
		return "", 0, notFound("reading the service account token for vault: %v", err)
	}
	req, _ := json.Marshal(map[string]string{"role": v.role, "jwt": strings.TrimSpace(string(jwt))})
	var body struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int64  `json:"lease_duration"` // seconds
		} `json:"auth"`
	}
	status, err := v.do(ctx, http.MethodPost, "/v1/auth/"+escapePath(v.authPath)+"/login", "", req, &body)
	switch {
	case err != nil:
		return "", 0, err
	case status == http.StatusForbidden:
		return "", 0, notFound("vault refused the kubernetes login for role %q", v.role)
	case status == http.StatusNotFound:
		return "", 0, notFound("vault has no auth method at auth/%s", v.authPath)
	case body.Auth.ClientToken == "":
		return "", 0, notFound("vault's kubernetes login returned no token")
	}
	return body.Auth.ClientToken, time.Duration(body.Auth.LeaseDuration) * time.Second, nil
}

// do sends one request. It returns the status for 200, 403 and 404, and an
// error for anything else: *UnavailableError when trying again may help
// (no answer, 429, 5xx, which includes a sealed Vault), *NotFoundError
// otherwise. A 200 body is decoded into out.
func (v *Vault) do(ctx context.Context, method, p, tok string, in []byte, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, vaultTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, v.addr+p, bytes.NewReader(in))
	if err != nil {
		return 0, notFound("building the vault request: %v", err)
	}
	if tok != "" {
		req.Header.Set("X-Vault-Token", tok)
	}
	if v.namespace != "" {
		req.Header.Set("X-Vault-Namespace", v.namespace)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return 0, unavailable("vault did not answer: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, unavailable("reading vault's answer: %v", err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.Unmarshal(b, out); err != nil {
			return 0, unavailable("vault's answer is not JSON")
		}
		return resp.StatusCode, nil
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound:
		return resp.StatusCode, nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return 0, unavailable("vault answered %s%s", resp.Status, vaultErrors(b))
	default:
		return 0, notFound("vault answered %s%s", resp.Status, vaultErrors(b))
	}
}

// vaultErrors returns the messages in an error answer, which name problems,
// never secret values.
func vaultErrors(b []byte) string {
	var e struct {
		Errors []string `json:"errors"`
	}
	if json.Unmarshal(b, &e) != nil || len(e.Errors) == 0 {
		return ""
	}
	s := strings.Join(e.Errors, "; ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return ": " + s
}

// cleanPath reports whether p is a relative slash-separated path with no
// empty, "." or ".." parts.
func cleanPath(p string) bool {
	if p == "" {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
