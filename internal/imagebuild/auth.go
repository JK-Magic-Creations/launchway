package imagebuild

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	authutil "github.com/containerd/containerd/v2/core/remotes/docker/auth"
	remoteserrors "github.com/containerd/containerd/v2/core/remotes/errors"
	"github.com/moby/buildkit/session/auth"
	"google.golang.org/grpc"
)

// Registry credentials for the builder. The builder pulls base images and
// reads and writes the build cache; it asks the runner, over the build's
// session, for credentials. The runner answers with a token it fetched
// itself whenever the registry uses tokens (the builder then never sees the
// password), and with the user name and password only for registries that
// do not. It answers only for the registries this build may use, with the
// credentials this build may use (never push credentials for a pull
// request), and for nothing else.

// hostAuth is what the runner may tell the builder about one registry.
type hostAuth struct {
	username, secret string
	tls              *tls.Config // nil: the system's
	// insecure: the registry is configured insecure (plain HTTP), so its
	// token service may be too.
	insecure bool
}

// sessionAuth implements BuildKit's auth session service.
type sessionAuth struct {
	auth.UnimplementedAuthServer
	hosts map[string]hostAuth // by host as the builder names it
	key   []byte              // random, for this build's token authority
}

func newSessionAuth(hosts map[string]hostAuth) *sessionAuth {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &sessionAuth{hosts: hosts, key: key}
}

func (a *sessionAuth) Register(s *grpc.Server) { auth.RegisterAuthServer(s, a) }

func (a *sessionAuth) lookup(host string) hostAuth {
	if h, ok := a.hosts[host]; ok {
		return h
	}
	// Docker Hub is asked for as registry-1.docker.io.
	if host == "registry-1.docker.io" {
		return a.hosts["docker.io"]
	}
	return hostAuth{}
}

// Credentials answers for registries that do not use tokens.
func (a *sessionAuth) Credentials(_ context.Context, req *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	h := a.lookup(req.Host)
	return &auth.CredentialsResponse{Username: h.username, Secret: h.secret}, nil
}

// FetchToken fetches a registry token on the runner, so that the password
// stays here.
func (a *sessionAuth) FetchToken(ctx context.Context, req *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	h := a.lookup(req.Host)
	// The realm is the registry's word for where to send credentials: never
	// in the clear, unless the operator marked the registry insecure.
	if h.secret != "" && !h.insecure {
		u, err := url.Parse(req.Realm)
		if err != nil || u.Scheme != "https" {
			return nil, fmt.Errorf("registry %s asks for its credentials to be sent to %q, which is not https; refused "+
				"(mark the registry insecure: true only if it is on the runner's own machine)", req.Host, req.Realm)
		}
	}
	to := authutil.TokenOptions{Realm: req.Realm, Service: req.Service, Scopes: req.Scopes, Username: h.username, Secret: h.secret}
	client := &http.Client{Timeout: 60 * time.Second}
	if h.tls != nil {
		client.Transport = &http.Transport{TLSClientConfig: h.tls, Proxy: http.ProxyFromEnvironment}
	}
	if h.secret != "" {
		resp, err := authutil.FetchTokenWithOAuth(ctx, client, nil, "launchway", to)
		if err == nil {
			return tokenResponse(resp.AccessToken, resp.IssuedAt, resp.ExpiresInSeconds), nil
		}
		var st remoteserrors.ErrUnexpectedStatus
		if !errors.As(err, &st) || (st.StatusCode != http.StatusMethodNotAllowed && st.StatusCode != http.StatusNotFound &&
			st.StatusCode != http.StatusUnauthorized) {
			return nil, err
		}
		// Registries without the OAuth POST endpoint take a GET.
	}
	resp, err := authutil.FetchToken(ctx, client, nil, to)
	if err != nil {
		return nil, err
	}
	return tokenResponse(resp.Token, resp.IssuedAt, resp.ExpiresInSeconds), nil
}

func tokenResponse(token string, issued time.Time, expires int) *auth.FetchTokenResponse {
	if expires == 0 {
		expires = 60
	}
	r := &auth.FetchTokenResponse{Token: token, ExpiresIn: int64(expires)}
	if !issued.IsZero() {
		r.IssuedAt = issued.Unix()
	}
	return r
}

// authorityKey is the key this build signs the builder's token requests
// with, so that tokens the builder keeps are tied to these credentials: it
// differs for every set of credentials and every build.
func (a *sessionAuth) authorityKey(host string, salt []byte) ed25519.PrivateKey {
	h := a.lookup(host)
	mac := hmac.New(sha256.New, salt)
	if h.secret != "" {
		inner := hmac.New(sha256.New, a.key)
		inner.Write([]byte(host + "\x00" + h.username + "\x00" + h.secret))
		mac.Write(inner.Sum(nil))
	}
	return ed25519.NewKeyFromSeed(mac.Sum(nil)[:ed25519.SeedSize])
}

func (a *sessionAuth) GetTokenAuthority(_ context.Context, req *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	k := a.authorityKey(req.Host, req.Salt)
	return &auth.GetTokenAuthorityResponse{PublicKey: k.Public().(ed25519.PublicKey)}, nil
}

func (a *sessionAuth) VerifyTokenAuthority(_ context.Context, req *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	k := a.authorityKey(req.Host, req.Salt)
	// The builder checks the signature as NaCl's sign does: the signature
	// followed by the message.
	sig := ed25519.Sign(k, req.Payload)
	return &auth.VerifyTokenAuthorityResponse{Signed: append(sig, req.Payload...)}, nil
}

// trimHost turns a registry host as configured into the form the builder
// asks for.
func trimHost(h string) string { return strings.TrimSuffix(strings.ToLower(h), "/") }
