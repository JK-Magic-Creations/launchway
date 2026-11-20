package runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/jk-magic-creations/launchway/internal/runnerauth"
)

// A runner's identity (schema 21): the ID the server gave it when it
// enrolled with a join token, and the Ed25519 key it made then. The private
// key never leaves the runner: it lives in <data>/identity/key, readable by
// the runner's user only, and signs the short assertions that get the
// runner access tokens.

// Identity is an enrolled runner's ID and key.
type Identity struct {
	Server    string             `json:"server"`
	RunnerID  string             `json:"runner_id"`
	Pool      string             `json:"pool"`
	Ephemeral bool               `json:"ephemeral,omitempty"`
	Key       ed25519.PrivateKey `json:"-"`
	dir       string
}

// identityDir is where a runner keeps its identity.
func identityDir(data string) string { return filepath.Join(data, "identity") }

// LoadIdentity reads the identity in a runner's data directory; nil, nil
// if there is none. A key left by a rotation that did not finish is tried
// too (see IdentityAuth).
func LoadIdentity(data string) (*Identity, error) {
	dir := identityDir(data)
	b, err := os.ReadFile(filepath.Join(dir, "runner.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := &Identity{dir: dir}
	if err := json.Unmarshal(b, id); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "runner.json"), err)
	}
	if id.Key, err = readKey(filepath.Join(dir, "key")); err != nil {
		return nil, err
	}
	return id, nil
}

func readKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := runnerauthDecode(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: not a runner key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func runnerauthDecode(s string) ([]byte, error) {
	// The key file holds the key's seed, URL-safe base64, as public keys
	// are written.
	k, err := runnerauth.DecodeKey(s)
	return k, err
}

// writeFile writes a file atomically, readable by the runner's user only.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (id *Identity) save() error {
	if err := os.MkdirAll(id.dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(id, "", "  ")
	if err := writeFile(filepath.Join(id.dir, "key"), []byte(runnerauth.EncodeKey(ed25519.PublicKey(id.Key.Seed()))+"\n")); err != nil {
		return err
	}
	return writeFile(filepath.Join(id.dir, "runner.json"), append(b, '\n'))
}

// JoinFacts is what a runner says about itself when it enrols.
type JoinFacts struct {
	Name     string   `json:"name"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Version  string   `json:"version"`
	Executor string   `json:"executor"`
	Features string   `json:"features,omitempty"`
	Labels   []string `json:"labels,omitempty"`
}

// ErrNoEnrolment means the server does not enrol runners: it is older than
// schema 21. Such a server takes the shared runner token only.
var ErrNoEnrolment = errors.New("this server does not enrol runners (it is older than Launchway's schema 21); run with the shared runner token (--token) instead")

// Join enrols a runner with a join token: it makes a key, registers its
// public half, and saves the identity in the data directory.
func Join(ctx context.Context, hc *http.Client, server, joinToken, data, hostname string, facts JoinFacts) (*Identity, error) {
	server = strings.TrimRight(server, "/")
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	var out struct {
		RunnerID  string `json:"runner_id"`
		Pool      string `json:"pool"`
		Ephemeral bool   `json:"ephemeral"`
	}
	code, err := postJSON(ctx, hc, server+"/api/runner/v2/join", map[string]any{"join_token": joinToken,
		"public_key": runnerauth.EncodeKey(key.Public().(ed25519.PublicKey)), "proof": runnerauth.JoinProof(key, joinToken),
		"hostname": hostname, "facts": facts}, &out)
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		return nil, ErrNoEnrolment
	}
	if err != nil {
		return nil, fmt.Errorf("enrolling: %w", err)
	}
	id := &Identity{Server: server, RunnerID: out.RunnerID, Pool: out.Pool, Ephemeral: out.Ephemeral, Key: key, dir: identityDir(data)}
	if err := id.save(); err != nil {
		return nil, fmt.Errorf("saving the runner's identity: %w", err)
	}
	return id, nil
}

// postJSON posts a JSON body and decodes a JSON answer. It returns the
// status, and an error with the server's message for any status from 300.
func postJSON(ctx context.Context, hc *http.Client, url string, in, out any) (int, error) {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return resp.StatusCode, &authError{code: resp.StatusCode, msg: e.Error, body: data}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

type authError struct {
	code int
	msg  string
	body []byte
}

func (e *authError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.msg, e.code) }

// IdentityAuth gives an enrolled runner's requests their access tokens: it
// asks for one with an assertion signed by the runner's key, keeps it until
// shortly before it expires, and asks again when the server refuses it. It
// corrects for a clock that is off from the server's, and moves to a new
// key when the server asks.
type IdentityAuth struct {
	ID   *Identity
	HTTP *http.Client
	Log  *slog.Logger
	// RotateEvery moves to a new key this often even if the server does
	// not ask; 0 leaves it to the server (30 days).
	RotateEvery time.Duration

	mu      sync.Mutex
	token   string
	expires time.Time
	offset  time.Duration // the server's clock minus this one's
	keyAt   time.Time
}

// Token returns an access token, getting a new one if needed.
func (a *IdentityAuth) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Until(a.expires) > time.Minute {
		return a.token, nil
	}
	return a.refresh(ctx)
}

// Invalidate forgets a token the server refused, so that the next request
// gets a new one.
func (a *IdentityAuth) Invalidate(token string) {
	a.mu.Lock()
	if a.token == token {
		a.token = ""
	}
	a.mu.Unlock()
}

func (a *IdentityAuth) log() *slog.Logger {
	if a.Log == nil {
		return slog.Default()
	}
	return a.Log
}

func (a *IdentityAuth) now() time.Time { return time.Now().Add(a.offset) }

func (a *IdentityAuth) refresh(ctx context.Context) (string, error) {
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ServerTime  int64  `json:"server_time"`
		Rotate      bool   `json:"rotate"`
	}
	for try := 0; ; try++ {
		as, err := runnerauth.NewAssertion(a.ID.Key, a.ID.RunnerID, a.ID.Server, runnerauth.AudienceToken, a.now())
		if err != nil {
			return "", err
		}
		code, err := postJSON(ctx, a.HTTP, a.ID.Server+runnerauth.AudienceToken, map[string]string{"assertion": as}, &out)
		if err != nil && try == 0 && a.correctClock(err) {
			continue
		}
		if err != nil {
			if code == http.StatusNotFound {
				return "", ErrNoEnrolment
			}
			return "", fmt.Errorf("getting an access token: %w", err)
		}
		break
	}
	if out.ServerTime > 0 {
		a.offset = time.Until(time.Unix(out.ServerTime, 0)).Round(time.Second)
		if a.offset.Abs() < 2*time.Second {
			a.offset = 0
		}
	}
	a.token, a.expires = out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn)*time.Second)
	if a.keyAt.IsZero() {
		a.keyAt = time.Now()
	}
	if out.Rotate || (a.RotateEvery > 0 && time.Since(a.keyAt) > a.RotateEvery) {
		if err := a.rotate(ctx); err != nil {
			// The old key still works; try again with the next token.
			a.log().Warn("could not move to a new key; will try again", "err", err)
		} else {
			return a.refresh(ctx)
		}
	}
	return a.token, nil
}

// correctClock reads the server's time from a refusal for clock skew and
// sets the offset; it reports whether it did.
func (a *IdentityAuth) correctClock(err error) bool {
	var he *authError
	if !errors.As(err, &he) || he.code != http.StatusUnauthorized {
		return false
	}
	var body struct {
		Skew       bool  `json:"skew"`
		ServerTime int64 `json:"server_time"`
	}
	if json.Unmarshal(he.body, &body) != nil || !body.Skew || body.ServerTime == 0 {
		return false
	}
	a.offset = time.Until(time.Unix(body.ServerTime, 0))
	a.log().Warn("this machine's clock is off from the server's; correcting", "by", a.offset.Round(time.Second))
	return true
}

// rotate moves the runner to a new key. The new key is written next to
// the old one first, so a crash in between loses nothing: LoadIdentity's
// caller keeps the old key, which the server still has unless the rotation
// went through, and RecoverRotation picks the new one up then.
func (a *IdentityAuth) rotate(ctx context.Context) error {
	_, nk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	pub := runnerauth.EncodeKey(nk.Public().(ed25519.PublicKey))
	now := a.now()
	as, err := runnerauth.Sign(a.ID.Key, runnerauth.Claims{Issuer: a.ID.RunnerID, Subject: a.ID.RunnerID,
		Audience: a.ID.Server + runnerauth.AudienceRotate, IssuedAt: now.Unix(), Expires: now.Add(runnerauth.AssertionMaxLife).Unix(),
		ID: runnerauth.Random(16), NewKey: pub})
	if err != nil {
		return err
	}
	next := filepath.Join(a.ID.dir, "key.next")
	if a.ID.dir != "" {
		if err := writeFile(next, []byte(runnerauth.EncodeKey(ed25519.PublicKey(nk.Seed()))+"\n")); err != nil {
			return err
		}
	}
	if _, err := postJSON(ctx, a.HTTP, a.ID.Server+runnerauth.AudienceRotate, map[string]string{"assertion": as, "public_key": pub,
		"proof": runnerauth.RotateProof(nk, as)}, nil); err != nil {
		_ = os.Remove(next)
		return err
	}
	a.ID.Key, a.token, a.keyAt = nk, "", time.Now()
	if a.ID.dir != "" {
		if err := os.Rename(next, filepath.Join(a.ID.dir, "key")); err != nil {
			return err
		}
	}
	a.log().Info("moved to a new key", "runner", a.ID.RunnerID)
	return nil
}

// RecoverRotation finishes a rotation that was cut short: if a new key was
// written but not moved into place, it is tried; whichever key the server
// accepts is kept.
func (a *IdentityAuth) RecoverRotation(ctx context.Context) {
	next := filepath.Join(a.ID.dir, "key.next")
	nk, err := readKey(next)
	if err != nil {
		return
	}
	if _, err := a.Token(ctx); err == nil {
		_ = os.Remove(next) // the old key still works: the rotation never happened
		return
	}
	old := a.ID.Key
	a.ID.Key = nk
	if _, err := a.Token(ctx); err == nil {
		_ = os.Rename(next, filepath.Join(a.ID.dir, "key"))
		return
	}
	a.ID.Key = old
}

// HTTPOptions say how a runner reaches its server.
type HTTPOptions struct {
	// Proxy is the proxy for every request to the server, overriding
	// HTTPS_PROXY and HTTP_PROXY; NoProxy lists hosts reached directly,
	// overriding NO_PROXY. Unset, the environment's are used.
	Proxy, NoProxy string
	// CAFile adds certificates (PEM) to the system's, for a server or a
	// proxy whose certificates a private authority signed, such as one
	// that inspects TLS.
	CAFile string
	// Timeout bounds each request; default 60s.
	Timeout time.Duration
}

// NewHTTPClient makes the client a runner talks to its server with. The
// environment's proxy settings are read when it is made.
func NewHTTPClient(o HTTPOptions) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	cfg := httpproxy.FromEnvironment()
	if o.Proxy != "" {
		cfg.HTTPProxy, cfg.HTTPSProxy = o.Proxy, o.Proxy
	}
	if o.NoProxy != "" {
		cfg.NoProxy = o.NoProxy
	}
	pf := cfg.ProxyFunc()
	tr.Proxy = func(r *http.Request) (*url.URL, error) { return pf(r.URL) }
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("ca_file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file: %s holds no PEM certificate", o.CAFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	return &http.Client{Transport: tr, Timeout: o.Timeout}, nil
}
