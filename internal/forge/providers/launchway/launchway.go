// Package launchway is the internal provider: Launchway's own Git hosting
// behind the same interface as every forge (doc 26 section 3.4: it is not
// privileged, it is only faster). A repository is a row of the forge's tables
// and a repository in the storage (internal/forge/storage), named by the
// row's ID, never by its name.
//
// This is the core: repositories (create, rename, delete to a trash), refs
// and the clone address and credential. Content, proposals and the rest are
// declared none until the stories that build them.
package launchway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
)

// Storage is what the provider needs of the repository storage.
type Storage interface {
	Create(ctx context.Context, repo string) error
	Refs(ctx context.Context, repo string) (map[string]string, error)
}

// Minter makes the short-lived credential Access hands out.
type Minter interface {
	// Mint returns a token for one repository that expires after ttl.
	Mint(repoID string, write bool, ttl time.Duration) (string, error)
}

// Options configure the provider.
type Options struct {
	// BaseURL is where this server is reached (https://launchway.example.com),
	// the start of every clone address.
	BaseURL string
	// DefaultHome is the owner of a repository created without one. Default
	// "launchway".
	DefaultHome string
	// MaxPerHome caps the repositories under one owner (0: no cap).
	MaxPerHome int
	// CreatesPerMinute caps how fast repositories are made, in all (0: 60).
	CreatesPerMinute int
	// SSHPrefix is what an SSH clone address starts with when Git over SSH is
	// on ("ssh://git@host:2222/" or "git@host:"); empty when it is off.
	SSHPrefix string
	// CredentialTTL is how long Access.Credential's secret lasts (default 1h).
	CredentialTTL time.Duration
	Minter        Minter
	Now           func() time.Time
	// Pulls is the pull-request service; without it the provider declares no
	// proposals.
	Pulls *pulls.Service
	// ProposalAuthor is who proposals created or closed through the provider
	// are made as (default "launchway").
	ProposalAuthor string
}

// Capabilities of the internal provider so far.
func Capabilities() forge.Capabilities {
	const reason = "the built-in forge does not have this yet"
	const workaround = "Use the Git address of the repository with a client for it, or connect another forge for this"
	c := forge.NoCapabilities(reason, workaround)
	c[forge.CapRepositories] = forge.Declaration{Support: forge.Native}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.Native}
	c[forge.CapAccess] = forge.Declaration{Support: forge.Native}
	return c
}

// Provider is the internal provider of one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	st   *store.Store
	sto  Storage
	o    Options

	tags  atomic.Pointer[tagHolder]
	mu    sync.Mutex
	stamp []time.Time // recent creations, for the rate limit
}

// Register adds the launchway kind to a registry.
func Register(r *forge.Registry, st *store.Store, sto Storage, o Options) error {
	return r.Register(forge.KindLaunchway, func(c forge.Connection) (forge.Provider, error) { return New(c, st, sto, o), nil })
}

// New makes the provider of a connection.
func New(c forge.Connection, st *store.Store, sto Storage, o Options) *Provider {
	if o.DefaultHome == "" {
		o.DefaultHome = "launchway"
	}
	if o.CreatesPerMinute <= 0 {
		o.CreatesPerMinute = 60
	}
	if o.CredentialTTL <= 0 {
		o.CredentialTTL = time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	caps := Capabilities()
	if o.Pulls != nil {
		caps[forge.CapProposals] = forge.Declaration{Support: forge.Native}
		caps[forge.CapDrafts] = forge.Declaration{Support: forge.Native} // New.Draft is stored and honored
	}
	return &Provider{Base: forge.Base{K: forge.KindLaunchway, Caps: caps, Mat: forge.Supported}, conn: c, st: st, sto: sto, o: o}
}

// Connection is the connection this provider serves.
func (p *Provider) Connection() forge.Connection { return p.conn }

// Repositories manages repositories.
func (p *Provider) Repositories() forge.Repositories { return repos{p} }

// Access gives clone addresses and credentials.
func (p *Provider) Access() forge.Access { return access{p} }

// full completes a path with the default owner.
func (p *Provider) full(path string) string {
	if path != "" && !strings.Contains(path, "/") {
		return p.o.DefaultHome + "/" + path
	}
	return path
}

func (p *Provider) cloneOf(path string) forge.CloneAddresses {
	var c forge.CloneAddresses
	if p.o.BaseURL != "" {
		c.HTTPS = strings.TrimSuffix(p.o.BaseURL, "/") + "/git/" + path + ".git"
	}
	if p.o.SSHPrefix != "" {
		c.SSH = p.o.SSHPrefix + path + ".git"
	}
	return c
}

func (p *Provider) rateOK() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.o.Now()
	keep := p.stamp[:0]
	for _, t := range p.stamp {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	p.stamp = keep
	if len(p.stamp) >= p.o.CreatesPerMinute {
		return false
	}
	p.stamp = append(p.stamp, now)
	return true
}

// The errors of limits are the forge's, so clients can test for them.
var (
	ErrRateLimited = forge.ErrRateLimited
	ErrQuota       = forge.ErrQuota
)

type repos struct{ p *Provider }

func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	all, err := r.p.st.Repositories(ctx, r.p.conn.ID)
	if err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	live := all[:0]
	for _, x := range all {
		if !store.InTrash(x) {
			live = append(live, x)
		}
	}
	return forge.Page[forge.Repository]{Items: live}, nil
}

func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	path = r.p.full(path)
	if _, _, err := SplitPath(path); err != nil {
		return forge.Repository{}, fmt.Errorf("%w: repository %s", store.ErrNotFound, path)
	}
	return r.p.st.RepositoryByPath(ctx, r.p.conn.ID, path)
}

func (r repos) Create(ctx context.Context, n forge.NewRepository) (forge.Repository, error) {
	home := n.Home
	if home == "" {
		home = r.p.o.DefaultHome
	}
	if err := ValidSegment(home); err != nil {
		return forge.Repository{}, err
	}
	if err := ValidSegment(n.Name); err != nil {
		return forge.Repository{}, err
	}
	branch := n.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	if err := validBranch(branch); err != nil {
		return forge.Repository{}, err
	}
	if n.Visibility == "" {
		n.Visibility = forge.Private
	}
	if m := r.p.o.MaxPerHome; m > 0 {
		if c, err := r.p.st.CountRepositories(ctx, r.p.conn.ID, home); err != nil {
			return forge.Repository{}, err
		} else if c >= m {
			return forge.Repository{}, fmt.Errorf("%w (%d)", ErrQuota, m)
		}
	}
	if !r.p.rateOK() {
		return forge.Repository{}, ErrRateLimited
	}
	path := home + "/" + n.Name
	row, err := r.p.st.CreateRepository(ctx, forge.Repository{Connection: r.p.conn.ID, Home: home, Name: n.Name, Path: path,
		DefaultBranch: branch, Visibility: n.Visibility, Clone: r.p.cloneOf(path)})
	if err != nil {
		return row, err
	}
	// The row has an ID; the storage is made under it. A failure leaves a row
	// with no storage, which is removed rather than left to fail every push.
	if err := r.p.sto.Create(ctx, row.ID); err != nil {
		_ = r.p.st.TrashRepository(context.WithoutCancel(ctx), row.ID)
		return forge.Repository{}, fmt.Errorf("making the repository's storage: %w", err)
	}
	return row, nil
}

// Delete moves a repository to the trash: it stops being served and listed,
// and its data is kept.
func (r repos) Delete(ctx context.Context, path string) error {
	row, err := r.Get(ctx, path)
	if err != nil {
		return err
	}
	return r.p.st.TrashRepository(ctx, row.ID)
}

func (r repos) SetDefaultBranch(ctx context.Context, path, branch string) error {
	if err := validBranch(branch); err != nil {
		return err
	}
	row, err := r.Get(ctx, path)
	if err != nil {
		return err
	}
	return r.p.st.SetDefaultBranchOf(ctx, row.ID, branch)
}

// Rename moves a repository to a new owner and name; its ID, data and refs
// stay. The old clone address stops working.
func (p *Provider) Rename(ctx context.Context, path string, home, name string) (forge.Repository, error) {
	row, err := repos{p}.Get(ctx, path)
	if err != nil {
		return row, err
	}
	if home == "" {
		home = row.Home
	}
	if err := ValidSegment(home); err != nil {
		return row, err
	}
	if err := ValidSegment(name); err != nil {
		return row, err
	}
	np := home + "/" + name
	if err := p.st.MoveRepository(ctx, row.ID, home, name, np, p.cloneOf(np)); err != nil {
		return row, err
	}
	return p.st.Repository(ctx, row.ID)
}

// SetVisibility changes who may read a repository.
func (p *Provider) SetVisibility(ctx context.Context, path string, v forge.Visibility) error {
	row, err := repos{p}.Get(ctx, path)
	if err != nil {
		return err
	}
	return p.st.SetVisibility(ctx, row.ID, v)
}

// SetArchived archives a repository (pushes are refused) or lifts that.
func (p *Provider) SetArchived(ctx context.Context, path string, on bool) error {
	row, err := repos{p}.Get(ctx, path)
	if err != nil {
		return err
	}
	return p.st.SetArchived(ctx, row.ID, on)
}

// Restore brings a deleted repository back (by its ID).
func (p *Provider) Restore(ctx context.Context, id string) error {
	return p.st.RestoreRepository(ctx, id)
}

// Trash lists the deleted repositories.
func (p *Provider) Trash(ctx context.Context) ([]forge.Repository, error) {
	return p.st.Trashed(ctx, p.conn.ID)
}

// Refs lists a repository's refs of record, by name. (The content group is
// declared none until commits, trees and blobs are built, so refs are read
// here and not through Content.)
func (p *Provider) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	m, err := p.sto.Refs(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Ref, 0, len(m))
	for name, sha := range m {
		out = append(out, forge.Ref{Name: name, Commit: sha})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func validBranch(b string) error {
	if b == "" || strings.HasPrefix(b, "-") || strings.ContainsAny(b, " ~^:?*[\\\x00") || strings.Contains(b, "..") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") || strings.HasPrefix(b, "/") || len(b) > 200 {
		return fmt.Errorf("%w: %q is not a branch name", forge.ErrInvalid, b)
	}
	return nil
}

type access struct{ p *Provider }

func (a access) CloneAddresses(ctx context.Context, repo forge.Repository) (forge.CloneAddresses, error) {
	return a.p.cloneOf(repo.Path), nil
}

// Credential mints a read credential that expires; a write credential is for
// people (their own tokens), not for a run.
func (a access) Credential(ctx context.Context, repo forge.Repository, write bool) (forge.Credential, error) {
	if write {
		return forge.Credential{}, fmt.Errorf("%w: a short-lived credential is read-only; push with an API token", forge.ErrInvalid)
	}
	if a.p.o.Minter == nil {
		return forge.Credential{}, errors.New("this server has no credential minter")
	}
	tok, err := a.p.o.Minter.Mint(repo.ID, false, a.p.o.CredentialTTL)
	if err != nil {
		return forge.Credential{}, err
	}
	return forge.Credential{Username: "launchway", Secret: tok}, nil
}
