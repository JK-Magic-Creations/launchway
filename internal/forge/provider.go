package forge

import (
	"context"
	"net/http"
)

// Page is one page of a listing.
type Page[T any] struct {
	Items []T `json:"items"`
	// Next is the cursor for the next page, empty at the end.
	Next string `json:"next,omitempty"`
}

// Whoami is what a credential turns out to be.
type Whoami struct {
	Principal Principal `json:"principal"`
	Scopes    []string  `json:"scopes,omitempty"`
	// RateRemaining is -1 when the forge reports no rate limit.
	RateRemaining int `json:"rate_remaining"`
}

// NewRepository is a request to create a repository.
type NewRepository struct {
	Home          string     `json:"home,omitempty"`
	Name          string     `json:"name"`
	DefaultBranch string     `json:"default_branch,omitempty"`
	Visibility    Visibility `json:"visibility"`
}

// Provider is a backend. It is small groups of operations, not one fat
// interface, and says up front which groups it has through Capabilities. A
// group a backend lacks returns *ErrUnsupported from every method.
//
// Providers are reached through a Registry and the Service; the API, MCP
// server, command line and screens never import one.
type Provider interface {
	Kind() Kind
	// Capabilities says, per feature, native, emulated or none, with the
	// reason and the workaround in words.
	Capabilities() Capabilities
	// Maturity is the label the provider carries until it has passed the
	// conformance suite against a live service.
	Maturity() Maturity

	Identity() Identity
	Repositories() Repositories
	Content() Content
	Proposals() Proposals
	Reviews() Reviews
	Results() Results
	Land() Land
	Rules() Rules
	Events() Events
	Access() Access
}

// Identity validates the credential.
type Identity interface {
	Whoami(ctx context.Context) (Whoami, error)
}

// Repositories lists and manages repositories.
type Repositories interface {
	List(ctx context.Context, cursor string) (Page[Repository], error)
	Get(ctx context.Context, path string) (Repository, error)
	Create(ctx context.Context, r NewRepository) (Repository, error)
	Delete(ctx context.Context, path string) error
	SetDefaultBranch(ctx context.Context, path, branch string) error
}

// Content reads code. Launchway computes this locally from its own copy for
// every provider; a provider is asked only where it must be (a proposal head
// that cannot be fetched).
type Content interface {
	Refs(ctx context.Context, repo Repository) ([]Ref, error)
	Commit(ctx context.Context, repo Repository, id string) (Commit, error)
	Tree(ctx context.Context, repo Repository, rev, dir string) ([]TreeEntry, error)
	Blob(ctx context.Context, repo Repository, rev, path string, limit int64) ([]byte, error)
	Compare(ctx context.Context, repo Repository, base, head string) (Comparison, error)
}

// ProposalPatch changes a proposal; nil fields stay as they are.
type ProposalPatch struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	Draft *bool   `json:"draft,omitempty"`
}

// NewProposal is a request to open a proposal.
type NewProposal struct {
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Source Ref    `json:"source"`
	Target Ref    `json:"target"`
	Draft  bool   `json:"draft,omitempty"`
}

// Proposals is a placeholder group: its shape is final, nothing implements it
// yet but the unsupported answer.
type Proposals interface {
	List(ctx context.Context, repo Repository, state ProposalState, cursor string) (Page[Proposal], error)
	Get(ctx context.Context, repo Repository, number int) (Proposal, error)
	Create(ctx context.Context, repo Repository, p NewProposal) (Proposal, error)
	Update(ctx context.Context, repo Repository, number int, patch ProposalPatch) (Proposal, error)
	Close(ctx context.Context, repo Repository, number int) error
	Reopen(ctx context.Context, repo Repository, number int) error
	Revisions(ctx context.Context, repo Repository, number int) ([]Revision, error)
}

// Reviews reads and writes reviews and threads.
type Reviews interface {
	Reviews(ctx context.Context, repo Repository, number int) ([]Review, error)
	Submit(ctx context.Context, repo Repository, number int, v Verdict, body string) (Review, error)
	Threads(ctx context.Context, repo Repository, number int) ([]Thread, error)
	Reply(ctx context.Context, repo Repository, number int, thread, body string) (Thread, error)
	Resolve(ctx context.Context, repo Repository, number int, thread string, resolved bool) error
}

// Results publishes and lists results for a commit.
type Results interface {
	Publish(ctx context.Context, repo Repository, r Result) error
	List(ctx context.Context, repo Repository, sha string) ([]Result, error)
}

// Land brings a proposal's commits into its target.
type Land interface {
	Readiness(ctx context.Context, repo Repository, number int) (Mergeability, error)
	Strategies(ctx context.Context, repo Repository) ([]Strategy, error)
	Land(ctx context.Context, repo Repository, number int, s Strategy) error
	AutoLand(ctx context.Context, repo Repository, number int, s Strategy, enable bool) error
}

// Rules reads and writes ref rules.
type Rules interface {
	List(ctx context.Context, repo Repository) ([]RefRule, error)
	Set(ctx context.Context, repo Repository, rules []RefRule) error
}

// Events says how events arrive and turns them into normalised ones.
type Events interface {
	Mode() EventMode
	// Verify checks a webhook delivery's authenticity and parses it. A
	// delivery that fails verification returns an error and no events.
	Verify(r *http.Request, secret []byte) ([]Event, error)
	// Poll returns events since the cursor, and the next cursor.
	Poll(ctx context.Context, repo Repository, cursor string) ([]Event, string, error)
}

// Credential is a short-lived credential for one clone or push.
type Credential struct {
	Username string `json:"username"`
	Secret   string `json:"-"` // never serialised
}

// Access says how to clone and push.
type Access interface {
	CloneAddresses(ctx context.Context, repo Repository) (CloneAddresses, error)
	// Credential mints a credential for read (write false) or write access
	// that expires; "" username and secret means none is needed.
	Credential(ctx context.Context, repo Repository, write bool) (Credential, error)
}
