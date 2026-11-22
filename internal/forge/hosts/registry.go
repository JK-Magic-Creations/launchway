package hosts

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/github"
)

// Prober is the permission probe of a provider: the connection test. It
// reads only, and says which of the features the token can serve.
type Prober interface {
	Probe(ctx context.Context) (forge.HostReport, error)
}

// Entry is everything the service needs to know about one host kind. A new
// host is one provider package plus one Entry registered here; nothing else in
// this package, the API or the pages names a kind.
type Entry struct {
	Kind forge.Kind
	// Label is the host's name as a person says it.
	Label string
	// New makes the provider of a connection (its value implements Prober).
	New func(c forge.Connection, g *egress.Guard) (forge.Provider, error)
	// Endpoints lists every address the connection will reach (API, upload,
	// GraphQL, ...): each is egress-checked before the connection is saved.
	Endpoints func(c forge.Connection) ([]string, error)
	// DeliveryHeader names the webhook header that carries a delivery's unique
	// ID, used to drop a replay; "" when the host sends none.
	DeliveryHeader string
	// Probe says in plain words what the connection test reads.
	Probe string
}

var (
	regMu   sync.RWMutex
	entries = map[forge.Kind]Entry{}
)

// Register adds a host kind. It is meant for init-time use by the packages
// that wire a provider in; a duplicate kind panics (a programming error).
func Register(e Entry) {
	if e.Kind == "" || e.New == nil || e.Endpoints == nil {
		panic("hosts: an entry needs a kind, a constructor and its endpoints")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := entries[e.Kind]; dup {
		panic("hosts: kind registered twice: " + string(e.Kind))
	}
	entries[e.Kind] = e
}

func entryOf(k forge.Kind) (Entry, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	e, ok := entries[k]
	return e, ok
}

// Kinds lists the registered host kinds, sorted.
func Kinds() []forge.Kind {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]forge.Kind, 0, len(entries))
	for k := range entries {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func kindNames() string {
	s := ""
	for i, k := range Kinds() {
		if i > 0 {
			s += ", "
		}
		s += string(k)
	}
	return s
}

func init() {
	Register(Entry{
		Kind:  forge.KindGitHub,
		Label: "GitHub",
		New: func(c forge.Connection, g *egress.Guard) (forge.Provider, error) {
			p, err := github.New(c, g)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) {
			ep, err := github.EndpointsOf(c.BaseURL, c.UploadURL)
			if err != nil {
				return nil, err
			}
			return []string{ep.API.String(), ep.Upload.String()}, nil
		},
		DeliveryHeader: "X-GitHub-Delivery",
		Probe:          "who the token is, its expiry, the repositories it reaches with their push permission, and one branch protection read",
	})
}

func (s *Service) newProvider(c forge.Connection) (forge.Provider, error) {
	e, ok := entryOf(c.Kind)
	if !ok {
		return nil, fmt.Errorf("%w: no host of kind %q is built in", forge.ErrInvalid, c.Kind)
	}
	return e.New(c, s.guard)
}
