package forge

import (
	"context"
	"net/http"
)

// Base is a provider that has nothing: every group answers ErrUnsupported
// with the declared workaround. A provider embeds it and overrides the groups
// it has.
type Base struct {
	K    Kind
	Caps Capabilities
	Mat  Maturity
}

// Kind returns the kind.
func (b Base) Kind() Kind { return b.K }

// Capabilities returns the declarations.
func (b Base) Capabilities() Capabilities { return b.Caps }

// Maturity returns the label; Experimental unless set.
func (b Base) Maturity() Maturity {
	if b.Mat == "" {
		return Experimental
	}
	return b.Mat
}

// ProposalRefs returns the zero layout: no proposal refs.
func (b Base) ProposalRefs() RefLayout { return RefLayout{} }

func (b Base) Identity() Identity         { return unIdentity{b.Caps} }
func (b Base) Repositories() Repositories { return unRepositories{b.Caps} }
func (b Base) Content() Content           { return unContent{b.Caps} }
func (b Base) Proposals() Proposals       { return unProposals{b.Caps} }
func (b Base) Reviews() Reviews           { return unReviews{b.Caps} }
func (b Base) Results() Results           { return unResults{b.Caps} }
func (b Base) Land() Land                 { return unLand{b.Caps} }
func (b Base) Rules() Rules               { return unRules{b.Caps} }
func (b Base) Events() Events             { return unEvents{b.Caps} }
func (b Base) Access() Access             { return unAccess{b.Caps} }

// The un* types answer every call of one group with the ErrUnsupported of
// its capability.

type unIdentity struct{ c Capabilities }

func (u unIdentity) Whoami(ctx context.Context) (Whoami, error) {
	return Whoami{}, unsup(u.c, CapIdentity)
}

type unRepositories struct{ c Capabilities }

func (u unRepositories) List(ctx context.Context, cursor string) (Page[Repository], error) {
	return Page[Repository]{}, unsup(u.c, CapRepositories)
}
func (u unRepositories) Get(ctx context.Context, path string) (Repository, error) {
	return Repository{}, unsup(u.c, CapRepositories)
}
func (u unRepositories) Create(ctx context.Context, r NewRepository) (Repository, error) {
	return Repository{}, unsup(u.c, CapRepositories)
}
func (u unRepositories) Delete(ctx context.Context, path string) error {
	return unsup(u.c, CapRepositories)
}
func (u unRepositories) SetDefaultBranch(ctx context.Context, path, branch string) error {
	return unsup(u.c, CapRepositories)
}

type unContent struct{ c Capabilities }

func (u unContent) Refs(ctx context.Context, repo Repository) ([]Ref, error) {
	return nil, unsup(u.c, CapContent)
}
func (u unContent) Commit(ctx context.Context, repo Repository, id string) (Commit, error) {
	return Commit{}, unsup(u.c, CapContent)
}
func (u unContent) Tree(ctx context.Context, repo Repository, rev, dir string) ([]TreeEntry, error) {
	return nil, unsup(u.c, CapContent)
}
func (u unContent) Blob(ctx context.Context, repo Repository, rev, path string, limit int64) ([]byte, error) {
	return nil, unsup(u.c, CapContent)
}
func (u unContent) Compare(ctx context.Context, repo Repository, base, head string) (Comparison, error) {
	return Comparison{}, unsup(u.c, CapContent)
}

type unProposals struct{ c Capabilities }

func (u unProposals) List(ctx context.Context, repo Repository, state ProposalState, cursor string) (Page[Proposal], error) {
	return Page[Proposal]{}, unsup(u.c, CapProposals)
}
func (u unProposals) Get(ctx context.Context, repo Repository, number int) (Proposal, error) {
	return Proposal{}, unsup(u.c, CapProposals)
}
func (u unProposals) Create(ctx context.Context, repo Repository, p NewProposal) (Proposal, error) {
	return Proposal{}, unsup(u.c, CapProposals)
}
func (u unProposals) Update(ctx context.Context, repo Repository, number int, patch ProposalPatch) (Proposal, error) {
	return Proposal{}, unsup(u.c, CapProposals)
}
func (u unProposals) Close(ctx context.Context, repo Repository, number int) error {
	return unsup(u.c, CapProposals)
}
func (u unProposals) Reopen(ctx context.Context, repo Repository, number int) error {
	return unsup(u.c, CapProposals)
}
func (u unProposals) Revisions(ctx context.Context, repo Repository, number int) ([]Revision, error) {
	return nil, unsup(u.c, CapProposals)
}

type unReviews struct{ c Capabilities }

func (u unReviews) Reviews(ctx context.Context, repo Repository, number int) ([]Review, error) {
	return nil, unsup(u.c, CapReviews)
}
func (u unReviews) Submit(ctx context.Context, repo Repository, number int, v Verdict, body string) (Review, error) {
	return Review{}, unsup(u.c, CapReviews)
}
func (u unReviews) Threads(ctx context.Context, repo Repository, number int) ([]Thread, error) {
	return nil, unsup(u.c, CapReviews)
}
func (u unReviews) Reply(ctx context.Context, repo Repository, number int, thread, body string) (Thread, error) {
	return Thread{}, unsup(u.c, CapReviews)
}
func (u unReviews) Resolve(ctx context.Context, repo Repository, number int, thread string, resolved bool) error {
	return unsup(u.c, CapReviews)
}

type unResults struct{ c Capabilities }

func (u unResults) Publish(ctx context.Context, repo Repository, r Result) error {
	return unsup(u.c, CapResults)
}
func (u unResults) List(ctx context.Context, repo Repository, sha string) ([]Result, error) {
	return nil, unsup(u.c, CapResults)
}

type unLand struct{ c Capabilities }

func (u unLand) Readiness(ctx context.Context, repo Repository, number int) (Mergeability, error) {
	return Mergeability{}, unsup(u.c, CapLand)
}
func (u unLand) Strategies(ctx context.Context, repo Repository) ([]Strategy, error) {
	return nil, unsup(u.c, CapLand)
}
func (u unLand) Land(ctx context.Context, repo Repository, number int, s Strategy) error {
	return unsup(u.c, CapLand)
}
func (u unLand) AutoLand(ctx context.Context, repo Repository, number int, s Strategy, enable bool) error {
	return unsup(u.c, CapLand)
}

type unRules struct{ c Capabilities }

func (u unRules) List(ctx context.Context, repo Repository) ([]RefRule, error) {
	return nil, unsup(u.c, CapRules)
}
func (u unRules) Set(ctx context.Context, repo Repository, rules []RefRule) error {
	return unsup(u.c, CapRules)
}

type unEvents struct{ c Capabilities }

func (u unEvents) Mode() EventMode { return EventMode("") }
func (u unEvents) Verify(r *http.Request, secret []byte) ([]Event, error) {
	return nil, unsup(u.c, CapEvents)
}
func (u unEvents) Poll(ctx context.Context, repo Repository, cursor string) ([]Event, string, error) {
	return nil, "", unsup(u.c, CapEvents)
}

type unAccess struct{ c Capabilities }

func (u unAccess) CloneAddresses(ctx context.Context, repo Repository) (CloneAddresses, error) {
	return CloneAddresses{}, unsup(u.c, CapAccess)
}
func (u unAccess) Credential(ctx context.Context, repo Repository, write bool) (Credential, error) {
	return Credential{}, unsup(u.c, CapAccess)
}

// unsup is the error of a group the provider did not implement: the declared
// one, or, when the provider declares the feature but left the method to the
// base, a plain one.
func unsup(c Capabilities, feat Capability) error {
	if err := c.Err(feat); err != nil {
		return err
	}
	return &ErrUnsupported{Capability: feat, Reason: "this provider declares the feature but has not implemented it yet",
		Workaround: "use a provider that implements it"}
}
