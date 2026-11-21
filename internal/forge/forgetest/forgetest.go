// Package forgetest is the conformance suite every forge provider must pass,
// the internal one included (doc 26 section 3.4). This is its first part:
// the capability scenarios. A scenario for a capability runs unless the
// provider declares it none, and when it does declare it none, the suite
// asserts that every call of that feature returns ErrUnsupported with a
// non-empty workaround: a declaration cannot hide a failure. Further
// scenarios (proposals, reviews, results, landing, events) are added by the
// stories that build those groups, and run against every provider.
package forgetest

import (
	"context"
	"net/http"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Harness is a provider and a repository on it that the scenarios may read.
type Harness struct {
	Provider forge.Provider
	// Repo is an existing repository of the provider (read-only use).
	Repo forge.Repository
}

// probe calls one feature, the way a client would, and returns the error.
type probe func(ctx context.Context, p forge.Provider, repo forge.Repository) error

// probes maps each capability to calls that exercise it. A capability with no
// callable operation (forks, lfs) has none and is only checked for a complete
// declaration.
var probes = map[forge.Capability][]probe{
	forge.CapIdentity: {func(ctx context.Context, p forge.Provider, _ forge.Repository) error {
		_, err := p.Identity().Whoami(ctx)
		return err
	}},
	forge.CapRepositories: {
		func(ctx context.Context, p forge.Provider, _ forge.Repository) error {
			_, err := p.Repositories().List(ctx, "")
			return err
		},
		func(ctx context.Context, p forge.Provider, r forge.Repository) error {
			_, err := p.Repositories().Get(ctx, r.Path)
			return err
		},
	},
	forge.CapRepoCreate: {
		func(ctx context.Context, p forge.Provider, _ forge.Repository) error {
			_, err := p.Repositories().Create(ctx, forge.NewRepository{Name: "forgetest-scratch", Visibility: forge.Private})
			return err
		},
		func(ctx context.Context, p forge.Provider, _ forge.Repository) error {
			return p.Repositories().Delete(ctx, "forgetest-scratch")
		},
	},
	forge.CapContent: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		_, err := p.Content().Refs(ctx, r)
		return err
	}},
	forge.CapEvents: {
		func(ctx context.Context, p forge.Provider, r forge.Repository) error {
			_, _, err := p.Events().Poll(ctx, r, "")
			return err
		},
	},
	forge.CapAccess: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		_, err := p.Access().CloneAddresses(ctx, r)
		return err
	}},
	forge.CapProposals: {
		func(ctx context.Context, p forge.Provider, r forge.Repository) error {
			_, err := p.Proposals().List(ctx, r, forge.ProposalOpen, "")
			return err
		},
		func(ctx context.Context, p forge.Provider, r forge.Repository) error {
			_, err := p.Proposals().Get(ctx, r, 1)
			return err
		},
	},
	forge.CapDrafts: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		_, err := p.Proposals().Create(ctx, r, forge.NewProposal{Title: "forgetest", Draft: true})
		return err
	}},
	forge.CapReviews: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		_, err := p.Reviews().Reviews(ctx, r, 1)
		return err
	}},
	forge.CapResults: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		_, err := p.Results().List(ctx, r, "0000000000000000000000000000000000000000")
		return err
	}},
	forge.CapResultsGate: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		_, err := p.Land().Readiness(ctx, r, 1)
		return err
	}},
	forge.CapLand: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		_, err := p.Land().Strategies(ctx, r)
		return err
	}},
	forge.CapAutoLand: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		return p.Land().AutoLand(ctx, r, 1, forge.StrategyMerge, true)
	}},
	forge.CapRules: {func(ctx context.Context, p forge.Provider, r forge.Repository) error {
		_, err := p.Rules().List(ctx, r)
		return err
	}},
}

// Run runs the capability scenarios against a provider.
func Run(t *testing.T, h Harness) {
	t.Helper()
	p := h.Provider
	caps := p.Capabilities()
	t.Run("capabilities are complete", func(t *testing.T) {
		if err := caps.Validate(); err != nil {
			t.Fatal(err)
		}
		if p.Maturity() != forge.Experimental && p.Maturity() != forge.Supported {
			t.Errorf("unknown maturity %q", p.Maturity())
		}
	})
	ctx := context.Background()
	for _, feat := range forge.AllCapabilities {
		t.Run(string(feat), func(t *testing.T) {
			d := caps[feat]
			for i, call := range probes[feat] {
				err := call(ctx, p, h.Repo)
				u, unsupported := forge.IsUnsupported(err)
				if d.Support == forge.None {
					// Declared none: the call must say so, and say what to do.
					if !unsupported || u.Workaround == "" || u.Reason == "" {
						t.Errorf("probe %d: declared none, got %v; want ErrUnsupported with a reason and a workaround", i, err)
					} else if u.Capability != feat {
						// A sibling capability's answer is fine only when
						// that one is declared none as well.
						if caps.Has(u.Capability) {
							t.Errorf("probe %d: answered with %s, which the provider declares it has", i, u.Capability)
						}
					}
					continue
				}
				if unsupported {
					t.Errorf("probe %d: declared %s but the call says it is unsupported: %v", i, d.Support, err)
				}
			}
		})
	}
	t.Run("proposal refs agree with the declaration", func(t *testing.T) {
		layout := p.ProposalRefs()
		if caps[forge.CapProposalRef].Support == forge.None && !layout.IsZero() {
			t.Error("declared no proposal refs but a layout is given")
		}
		if caps.Has(forge.CapProposalRef) && layout.IsZero() {
			t.Error("declared proposal refs but the layout is empty")
		}
	})
	t.Run("webhook with no signature is refused", func(t *testing.T) {
		if caps[forge.CapEvents].Support == forge.None {
			t.Skip("declared none")
		}
		ev := p.Events()
		if ev.Mode() == forge.EventsPolling {
			// A polling-only provider verifies nothing; it must refuse, not accept.
			if events, err := ev.Verify(&http.Request{Header: http.Header{}}, []byte("secret")); err == nil || len(events) != 0 {
				t.Errorf("a polling provider accepted a webhook: %v %v", events, err)
			}
		}
	})
}

// RunRegistry fails for every registered kind that has no harness: a
// provider cannot be added without running the suite.
func RunRegistry(t *testing.T, reg *forge.Registry, harnesses map[forge.Kind]Harness) {
	t.Helper()
	for _, k := range reg.Kinds() {
		h, ok := harnesses[k]
		if !ok {
			t.Errorf("kind %q is registered but has no conformance harness", k)
			continue
		}
		t.Run(string(k), func(t *testing.T) { Run(t, h) })
	}
}
