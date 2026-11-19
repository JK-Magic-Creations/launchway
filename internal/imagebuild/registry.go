package imagebuild

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// remoteRegistry is a registry the runner itself talks to: to push what
// was built, its signatures and attestations.
type remoteRegistry struct {
	name string
	cfg  RegistryConfig
	user string
	pass string
	tls  *tls.Config
}

func newRemote(name string, cfg RegistryConfig, user, pass string) (*remoteRegistry, error) {
	r := &remoteRegistry{name: name, cfg: cfg, user: user, pass: pass}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("registry %s: ca_file: %w", name, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("registry %s: ca_file %s holds no certificate", name, cfg.CAFile)
		}
		r.tls = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return r, nil
}

func (r *remoteRegistry) nameOpts() []name.Option {
	opts := []name.Option{name.StrictValidation}
	if r.cfg.Insecure {
		opts = append(opts, name.Insecure)
	}
	return opts
}

func (r *remoteRegistry) tag(repo, tag string) (name.Tag, error) {
	return name.NewTag(r.cfg.Host+"/"+repo+":"+tag, r.nameOpts()...)
}

func (r *remoteRegistry) digest(repo, digest string) (name.Digest, error) {
	return name.NewDigest(r.cfg.Host+"/"+repo+"@"+digest, r.nameOpts()...)
}

func (r *remoteRegistry) options(ctx context.Context) []remote.Option {
	var a authn.Authenticator = authn.Anonymous
	if r.pass != "" {
		a = authn.FromConfig(authn.AuthConfig{Username: r.user, Password: r.pass})
	}
	return []remote.Option{remote.WithContext(ctx), remote.WithAuth(a), remote.WithTransport(r.transport()), remote.WithUserAgent("launchway")}
}

// transport is the HTTP transport to the registry. Unless the registry is
// configured insecure, it refuses plain HTTP: the registry library would
// otherwise fall back to HTTP for private and loopback addresses when TLS
// fails, sending the credentials in the clear.
func (r *remoteRegistry) transport() http.RoundTripper {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if r.tls != nil {
		tr.TLSClientConfig = r.tls
	}
	if r.cfg.Insecure {
		return tr
	}
	return httpsOnly{tr}
}

type httpsOnly struct{ next http.RoundTripper }

// errPlainHTTP is what a plain HTTP request to a registry not configured
// insecure fails with.
var errPlainHTTP = errors.New("refused to fall back to plain HTTP")

func (h httpsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, errPlainHTTP
	}
	return h.next.RoundTrip(req)
}

// hostAuth is what the builder may be told about this registry.
func (r *remoteRegistry) hostAuth() hostAuth {
	return hostAuth{username: r.user, secret: r.pass, tls: r.tls, insecure: r.cfg.Insecure}
}

// explain turns an error talking to a registry into one a person can act
// on, naming the registry as the pipeline does.
func (r *remoteRegistry) explain(what string, err error) error {
	if err == nil {
		return nil
	}
	var te *transport.Error
	if errors.As(err, &te) {
		switch te.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("registry %s (%s) refused the credentials while %s: check images.registries.%s.credentials (%s)",
				r.name, r.cfg.Host, what, r.name, oneLine(te.Error()))
		case http.StatusForbidden:
			return fmt.Errorf("registry %s (%s) does not let these credentials do this (%s): %s", r.name, r.cfg.Host, what, oneLine(te.Error()))
		case http.StatusNotFound:
			return fmt.Errorf("registry %s (%s): not found while %s: %s", r.name, r.cfg.Host, what, oneLine(te.Error()))
		}
		return fmt.Errorf("registry %s (%s) answered %d while %s: %s", r.name, r.cfg.Host, te.StatusCode, what, oneLine(te.Error()))
	}
	var ne net.Error
	var oe *net.OpError
	var ce *tls.CertificateVerificationError
	var ue x509.UnknownAuthorityError
	msg := err.Error()
	switch {
	case errors.As(err, &ce) || errors.As(err, &ue) || strings.Contains(msg, "x509: ") || strings.Contains(msg, "tls: failed to verify"):
		return fmt.Errorf("registry %s (%s): its TLS certificate is not trusted (set images.registries.%s.ca_file): %s",
			r.name, r.cfg.Host, r.name, oneLine(err.Error()))
	case errors.As(err, &oe) || errors.As(err, &ne):
		return fmt.Errorf("registry %s (%s) cannot be reached while %s: %s", r.name, r.cfg.Host, what, oneLine(err.Error()))
	case strings.Contains(msg, "server gave HTTP response to HTTPS client") || strings.Contains(msg, "first record does not look like a TLS handshake"):
		return fmt.Errorf("registry %s (%s) speaks plain HTTP; if that is intended (a local test registry), set insecure: true", r.name, r.cfg.Host)
	}
	return fmt.Errorf("registry %s (%s), %s: %s", r.name, r.cfg.Host, what, oneLine(err.Error()))
}

// checkPush asks the registry, before anything is built, whether these
// credentials may push to the repository, so that a wrong password or an
// unreachable registry fails the step in seconds.
func (r *remoteRegistry) checkPush(repo string) error {
	ref, err := r.tag(repo, "launchway-check")
	if err != nil {
		return err
	}
	var a authn.Authenticator = authn.Anonymous
	if r.pass != "" {
		a = authn.FromConfig(authn.AuthConfig{Username: r.user, Password: r.pass})
	}
	return r.explain("checking that it may push to "+r.cfg.Host+"/"+repo, remote.CheckPushPermission(ref, staticKeychain{a}, r.transport()))
}

type staticKeychain struct{ a authn.Authenticator }

func (k staticKeychain) Resolve(authn.Resource) (authn.Authenticator, error) { return k.a, nil }

// pushed is what pushing wrote.
type pushedRef struct {
	Ref    string // host/repo:tag
	Digest name.Digest
}

// pushIndex writes an index (or single image) to each tag, in order. The
// blobs go once per repository.
func (r *remoteRegistry) push(ctx context.Context, t remote.Taggable, repo string, tags []string) ([]pushedRef, error) {
	var out []pushedRef
	opts := r.options(ctx)
	for i, tg := range tags {
		ref, err := r.tag(repo, tg)
		if err != nil {
			return out, fmt.Errorf("tag %s/%s:%s: %w", r.cfg.Host, repo, tg, err)
		}
		switch v := t.(type) {
		case v1.ImageIndex:
			if i == 0 {
				err = remote.WriteIndex(ref, v, opts...)
			} else {
				err = remote.Tag(ref, v, opts...)
			}
		case v1.Image:
			if i == 0 {
				err = remote.Write(ref, v, opts...)
			} else {
				err = remote.Tag(ref, v, opts...)
			}
		default:
			err = errors.New("nothing to push")
		}
		if err != nil {
			return out, r.explain("pushing "+ref.String(), err)
		}
		dg, _ := t.(interface{ Digest() (v1.Hash, error) }).Digest()
		d, _ := r.digest(repo, dg.String())
		out = append(out, pushedRef{Ref: ref.String(), Digest: d})
	}
	return out, nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
