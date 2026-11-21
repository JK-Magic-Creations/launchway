package forge

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Factory makes the provider for one connection.
type Factory func(c Connection) (Provider, error)

// Registry holds the providers wired in at start-up. It is the only way to
// reach a provider.
type Registry struct {
	mu        sync.RWMutex
	factories map[Kind]Factory
}

// NewRegistry makes an empty registry.
func NewRegistry() *Registry { return &Registry{factories: map[Kind]Factory{}} }

// Register adds the factory for a kind. A kind registers once.
func (r *Registry) Register(k Kind, f Factory) error {
	if !k.Valid() {
		return fmt.Errorf("%w: unknown forge kind %q", ErrInvalid, k)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.factories[k]; dup {
		return fmt.Errorf("a provider for %q is already registered", k)
	}
	r.factories[k] = f
	return nil
}

// Kinds lists the registered kinds, sorted.
func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Open makes the provider of a connection.
func (r *Registry) Open(c Connection) (Provider, error) {
	r.mu.RLock()
	f, ok := r.factories[c.Kind]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no provider is registered for kind %q", c.Kind)
	}
	p, err := f(c)
	if err != nil {
		return nil, err
	}
	if p.Kind() != c.Kind {
		return nil, fmt.Errorf("the provider for %q reports kind %q", c.Kind, p.Kind())
	}
	if err := p.Capabilities().Validate(); err != nil {
		return nil, fmt.Errorf("provider %q: %w", c.Kind, err)
	}
	return p, nil
}

// Service is the only caller of providers, and the only thing the API, MCP
// server, command line and screens use. It owns what must be identical
// everywhere: the permission check, the audit entry, the mapping of forge
// identities to Launchway users, caching with a staleness marker, and the
// publishing of results. Its implementation is story S34-06.
type Service interface {
	Connections(ctx context.Context) ([]Connection, error)
	Connection(ctx context.Context, id string) (Connection, error)
	Connect(ctx context.Context, c Connection) (Connection, error)
	Disconnect(ctx context.Context, id string) error
	Capabilities(ctx context.Context, connection string) (Capabilities, error)
	Repositories(ctx context.Context, connection, cursor string) (Page[Repository], error)
	Repository(ctx context.Context, connection, path string) (Repository, error)
	Refs(ctx context.Context, repo string) ([]Ref, error)
	PublishResult(ctx context.Context, repo string, r Result) error
}
