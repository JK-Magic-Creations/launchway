// Package fakeidp is an OpenID Connect provider for tests. It serves
// discovery, keys, and the authorize, token and userinfo endpoints, signs ID
// tokens with a key of its own, and signs in whoever the test chooses,
// without asking anything. A test can also make it misbehave in the ways a
// relying party must catch.
//
// It checks what a real provider checks: the client, the redirect URI, that
// a code is used once, and PKCE (S256).
package fakeidp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Failure makes the provider misbehave.
type Failure string

const (
	None          Failure = ""
	WrongNonce    Failure = "wrong-nonce"    // the ID token carries another nonce
	WrongAudience Failure = "wrong-audience" // the ID token is for another client
	WrongIssuer   Failure = "wrong-issuer"   // the ID token names another issuer
	Expired       Failure = "expired"        // the ID token expired an hour ago
	BadSignature  Failure = "bad-signature"  // the ID token is signed with a key not in the key set
	Deny          Failure = "deny"           // the person refuses: the provider redirects back with error=access_denied
	NoGroups      Failure = "no-groups"      // the ID token has no groups claim; the userinfo response does
)

// Provider is a running fake provider.
type Provider struct {
	URL          string // the issuer
	ClientID     string
	ClientSecret string
	Now          func() time.Time // the clock tokens are issued by; default time.Now

	srv   *httptest.Server
	key   *rsa.PrivateKey
	other *rsa.PrivateKey // for BadSignature

	mu      sync.Mutex
	user    string
	groups  []string
	fail    Failure
	codes   map[string]grant
	access  map[string]grant // access token → who it is for
	refused []string         // why requests were refused
}

type grant struct {
	redirectURI, challenge, nonce, user string
	groups                              []string
	fail                                Failure
}

// New starts a provider for the client "launchway" with secret
// "fake-client-secret", signing in "alice" with no groups.
func New() *Provider {
	p := &Provider{ClientID: "launchway", ClientSecret: "fake-client-secret", Now: time.Now,
		user: "alice", codes: map[string]grant{}, access: map[string]grant{}}
	var err error
	if p.key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	if p.other, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /keys", p.keys)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /userinfo", p.userinfo)
	p.srv = httptest.NewServer(mux)
	p.URL = p.srv.URL
	return p
}

// Refused returns why the provider refused requests, oldest first.
func (p *Provider) Refused() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.refused...)
}

// Close stops the provider.
func (p *Provider) Close() { p.srv.Close() }

// SignInAs chooses who the next sign-ins are.
func (p *Provider) SignInAs(user string, groups ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.user, p.groups = user, groups
}

// Fail makes the next sign-ins fail in the given way; None restores normal
// behaviour.
func (p *Provider) Fail(f Failure) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = f
}

func (p *Provider) refuse(w http.ResponseWriter, code int, errCode, why string) {
	p.mu.Lock()
	p.refused = append(p.refused, why)
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": errCode, "error_description": why})
}

func (p *Provider) discovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/keys",
		"userinfo_endpoint":                     p.URL + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *Provider) keys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

// authorize signs in the chosen person at once and sends the browser back.
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	switch {
	case q.Get("client_id") != p.ClientID:
		p.refuse(w, http.StatusBadRequest, "invalid_client", "unknown client "+q.Get("client_id"))
		return
	case redirect == "":
		p.refuse(w, http.StatusBadRequest, "invalid_request", "no redirect_uri")
		return
	case q.Get("response_type") != "code":
		p.refuse(w, http.StatusBadRequest, "unsupported_response_type", "response_type is not code")
		return
	case !strings.Contains(" "+q.Get("scope")+" ", " openid "):
		p.refuse(w, http.StatusBadRequest, "invalid_scope", "the openid scope is missing")
		return
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		p.refuse(w, http.StatusBadRequest, "invalid_request", "PKCE with S256 is required")
		return
	case q.Get("state") == "" || q.Get("nonce") == "":
		p.refuse(w, http.StatusBadRequest, "invalid_request", "state and nonce are required")
		return
	}
	back, err := url.Parse(redirect)
	if err != nil {
		p.refuse(w, http.StatusBadRequest, "invalid_request", "bad redirect_uri")
		return
	}
	v := back.Query()
	v.Set("state", q.Get("state"))
	p.mu.Lock()
	if p.fail == Deny {
		v.Set("error", "access_denied")
		v.Set("error_description", "the person refused")
	} else {
		code := random()
		p.codes[code] = grant{redirectURI: redirect, challenge: q.Get("code_challenge"), nonce: q.Get("nonce"),
			user: p.user, groups: append([]string(nil), p.groups...), fail: p.fail}
		v.Set("code", code)
	}
	p.mu.Unlock()
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// token trades a code for tokens.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		p.refuse(w, http.StatusBadRequest, "invalid_request", "bad form")
		return
	}
	id, secret, ok := r.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(p.ClientSecret)) != 1 {
		p.refuse(w, http.StatusUnauthorized, "invalid_client", "wrong client credentials")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		p.refuse(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code")
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	g, found := p.codes[code]
	delete(p.codes, code) // a code is used at most once, even when the exchange fails
	p.mu.Unlock()
	switch {
	case !found:
		p.refuse(w, http.StatusBadRequest, "invalid_grant", "unknown or used code")
		return
	case r.PostForm.Get("redirect_uri") != g.redirectURI:
		p.refuse(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match")
		return
	case s256(r.PostForm.Get("code_verifier")) != g.challenge:
		p.refuse(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	now := p.Now()
	claims := map[string]any{
		"iss": p.URL, "sub": "sub-" + g.user, "aud": p.ClientID, "nonce": g.nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"preferred_username": g.user, "email": g.user + "@example.com", "groups": g.groups,
	}
	key, kid := p.key, "k1"
	switch g.fail {
	case WrongNonce:
		claims["nonce"] = "not-the-nonce"
	case WrongAudience:
		claims["aud"] = "some-other-client"
	case WrongIssuer:
		claims["iss"] = "https://impostor.example.com"
	case Expired:
		claims["iat"], claims["exp"] = now.Add(-2*time.Hour).Unix(), now.Add(-time.Hour).Unix()
	case BadSignature:
		key = p.other
	case NoGroups:
		delete(claims, "groups")
	}
	idToken, err := sign(key, kid, claims)
	if err != nil {
		p.refuse(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	access := random()
	p.mu.Lock()
	p.access[access] = g
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
}

func (p *Provider) userinfo(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	g, ok := p.access[tok]
	p.mu.Unlock()
	if !ok {
		p.refuse(w, http.StatusUnauthorized, "invalid_token", "unknown access token")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"sub": "sub-" + g.user, "preferred_username": g.user, "groups": g.groups})
}

func sign(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return obj.CompactSerialize()
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func random() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
