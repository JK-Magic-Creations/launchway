package signing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// MaxVerify is the most objects one request may ask about.
const MaxVerify = 200

// Service verifies objects of hosted repositories and signs for the
// installation.
type Service struct {
	Store *Store
	// Git reads hosted repositories (nil: only VerifyRaw works).
	Git *storage.Store
	// Seal returns what seals the installation key (nil result: no root key).
	Seal func(ctx context.Context) Sealer
	// Emails lists the addresses a person commits under (nil: none known).
	Emails func(ctx context.Context, user string) []string
	Clock  func() time.Time

	mu     sync.Mutex
	signer ssh.Signer
}

// Directory methods, so the service is what the verifier looks things up in.

// KeyByFingerprint finds a registered key or the installation's own.
func (s *Service) KeyByFingerprint(ctx context.Context, fp string) (Key, bool, error) {
	if k, ok, err := s.Store.ByFingerprint(ctx, fp); err != nil || ok {
		return k, ok, err
	}
	if k, ok := s.Store.InstallationKey(ctx); ok && k.Fingerprint == fp {
		return k, true, nil
	}
	return Key{}, false, nil
}

// Emails lists the addresses of a person.
func (s *Service) emails(ctx context.Context, user string) []string {
	if s.Emails == nil {
		return nil
	}
	return s.Emails(ctx, user)
}

type dir struct{ s *Service }

func (d dir) KeyByFingerprint(ctx context.Context, fp string) (Key, bool, error) {
	return d.s.KeyByFingerprint(ctx, fp)
}
func (d dir) Emails(ctx context.Context, user string) []string { return d.s.emails(ctx, user) }
func (d dir) GetCached(ctx context.Context, o string) (Cached, bool) {
	return d.s.Store.GetCached(ctx, o)
}
func (d dir) PutCached(ctx context.Context, c Cached) { d.s.Store.PutCached(ctx, c) }

func (s *Service) verifier() *Verifier { return &Verifier{Dir: dir{s}, Now: s.Clock} }

// VerifyRaw judges objects a caller already holds (the objects of a push).
func (s *Service) VerifyRaw(ctx context.Context, objs []storage.PushObject, pusher string) []Result {
	v := s.verifier()
	out := make([]Result, 0, len(objs))
	for _, o := range objs {
		out = append(out, v.Verify(ctx, o.SHA, o.Kind, o.Raw, pusher))
	}
	return out
}

// VerifyRepo judges commits and annotated tags of a hosted repository by
// their object names. A name that is not a commit or tag of the repository
// is reported as an error for that name.
func (s *Service) VerifyRepo(ctx context.Context, repoID string, shas []string) ([]Result, error) {
	if s.Git == nil {
		return nil, errors.New("git hosting is off")
	}
	if len(shas) > MaxVerify {
		return nil, fmt.Errorf("ask about at most %d objects at a time", MaxVerify)
	}
	var objs []storage.PushObject
	err := s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := gitReader{store: s.Git, dir: dir}
		for _, sha := range shas {
			if !validName(sha) {
				return fmt.Errorf("%q is not an object name", sha)
			}
			kind, err := g.run(ctx, "cat-file", "-t", sha)
			if err != nil {
				continue // not in this repository: left out of the answer
			}
			kind = strings.TrimSpace(kind)
			if kind != "commit" && kind != "tag" {
				continue
			}
			raw, err := g.run(ctx, "cat-file", kind, sha)
			if err != nil {
				continue
			}
			objs = append(objs, storage.PushObject{SHA: sha, Kind: kind, Raw: []byte(raw)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.VerifyRaw(ctx, objs, ""), nil
}

func validName(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Signer returns the installation's signing key, making it on first use.
func (s *Service) Signer(ctx context.Context) (ssh.Signer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signer != nil {
		return s.signer, nil
	}
	var seal Sealer
	if s.Seal != nil {
		seal = s.Seal(ctx)
	}
	sg, err := s.Store.InstallationSigner(ctx, seal)
	if err != nil {
		return nil, err
	}
	s.signer = sg
	return sg, nil
}

// SignPayload signs a commit's payload with the installation's key (the
// function the pull request service calls for the commits it makes).
func (s *Service) SignPayload(ctx context.Context, payload []byte) ([]byte, error) {
	sg, err := s.Signer(ctx)
	if err != nil {
		return nil, err
	}
	return SignSSH(sg, payload)
}

// PublicKey is the installation's public signing key in authorized_keys
// form, with its fingerprint, making the key if there is none yet.
func (s *Service) PublicKey(ctx context.Context) (text, fingerprint string, err error) {
	if _, err := s.Signer(ctx); err != nil {
		return "", "", err
	}
	k, ok := s.Store.InstallationKey(ctx)
	if !ok {
		return "", "", errors.New("the installation's signing key is missing")
	}
	return k.PublicKey + " launchway-installation", k.Fingerprint, nil
}
