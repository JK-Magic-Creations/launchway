// Package plain is provider zero: today's watcher of a Git address, behind
// the forge interface. A repository on a plain connection is a Git address
// Launchway mirrors and polls (internal/source). Plain Git has no proposals,
// results, reviews or rules, and the provider says so, in words, wherever
// it is asked; it does have refs and content, which come from the mirror, and
// events, which are what the poller decided and the store kept.
package plain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// Watcher is what the provider needs of the source manager.
type Watcher interface {
	// Refs fetches (when stale) and lists a project's branches and tags.
	Refs(ctx context.Context, project string) ([]source.Ref, error)
	// Mirror returns the mirror's directory, fetched when stale.
	Mirror(ctx context.Context, project string) (string, error)
}

const noProposals = "plain Git has no proposals, results or rules"
const workaround = "Open proposals and report results on the Git host itself, or connect the host (GitHub, GitLab, Gitea and others) for those"

// Capabilities of the plain provider.
func Capabilities() forge.Capabilities {
	c := forge.Capabilities{}
	for _, f := range forge.AllCapabilities {
		c[f] = forge.Declaration{Support: forge.None, Reason: noProposals, Workaround: workaround}
	}
	c[forge.CapRepositories] = forge.Declaration{Support: forge.Native}
	c[forge.CapContent] = forge.Declaration{Support: forge.Native}
	c[forge.CapEvents] = forge.Declaration{Support: forge.Native}
	c[forge.CapAccess] = forge.Declaration{Support: forge.Native}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a plain connection watches a Git address that already exists; it cannot create repositories there",
		Workaround: "Create the repository on the Git host, then add it as a project"}
	c[forge.CapProposalRef] = forge.Declaration{Support: forge.Emulated,
		Reason:     "plain Git does not know whether the host publishes refs/pull/<n>/head; if it does, pull requests are found there",
		Workaround: "Nothing to do: pull request runs start when the host publishes the heads"}
	return c
}

// Provider is the plain provider for one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	w    Watcher
	st   *store.Store
}

// Register adds the plain kind to a registry.
func Register(r *forge.Registry, w Watcher, st *store.Store) error {
	return r.Register(forge.KindPlain, func(c forge.Connection) (forge.Provider, error) { return New(c, w, st), nil })
}

// New makes the provider of a connection.
func New(c forge.Connection, w Watcher, st *store.Store) *Provider {
	return &Provider{Base: forge.Base{K: forge.KindPlain, Caps: Capabilities(), Mat: forge.Supported}, conn: c, w: w, st: st}
}

// ProposalRefs: refs/pull/<n>/head, if the host publishes it. This is what
// Launchway has always assumed of a plain Git address.
func (p *Provider) ProposalRefs() forge.RefLayout { return forge.PullRefLayout }

// Repositories reads the connection's repositories from the store.
func (p *Provider) Repositories() forge.Repositories { return repos{p} }

// Content reads refs and history from the mirror.
func (p *Provider) Content() forge.Content { return content{p} }

// Events reads what the poller decided.
func (p *Provider) Events() forge.Events { return events{p} }

// Access gives the project's clone address.
func (p *Provider) Access() forge.Access { return access{p} }

// ---- repositories ----

type repos struct{ p *Provider }

func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	all, err := r.p.st.Repositories(ctx, r.p.conn.ID)
	if err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	return forge.Page[forge.Repository]{Items: all}, nil
}

func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	return r.p.st.RepositoryByPath(ctx, r.p.conn.ID, path)
}

func (r repos) Create(context.Context, forge.NewRepository) (forge.Repository, error) {
	return forge.Repository{}, r.p.Caps.Err(forge.CapRepoCreate)
}

func (r repos) Delete(context.Context, string) error { return r.p.Caps.Err(forge.CapRepoCreate) }

func (r repos) SetDefaultBranch(context.Context, string, string) error {
	return r.p.Caps.Err(forge.CapRepoCreate)
}

// ---- content ----

type content struct{ p *Provider }

// project finds the watched project a repository stands for.
func (c content) project(ctx context.Context, repo forge.Repository) (string, error) {
	// The repository's name is the project's: SyncPlain makes it so.
	if repo.Path == "" {
		return "", fmt.Errorf("%w: repository has no path", forge.ErrInvalid)
	}
	return repo.Path, nil
}

func (c content) git(ctx context.Context, repo forge.Repository, args ...string) (string, error) {
	name, err := c.project(ctx, repo)
	if err != nil {
		return "", err
	}
	dir, err := c.p.w.Mirror(ctx, name)
	if err != nil {
		return "", err
	}
	res, err := gitexec.Run(ctx, gitexec.Spec{GitDir: dir, Args: args, Timeout: time.Minute, MaxOutput: 64 << 20})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return "", errors.New(strings.TrimSpace(ee.Stderr))
		}
		return "", err
	}
	return res.Stdout, nil
}

func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	name, err := c.project(ctx, repo)
	if err != nil {
		return nil, err
	}
	refs, err := c.p.w.Refs(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Ref, 0, len(refs))
	for _, r := range refs {
		out = append(out, forge.Ref{Name: r.FullRef(), Commit: r.SHA})
	}
	return out, nil
}

func (c content) Commit(ctx context.Context, repo forge.Repository, id string) (forge.Commit, error) {
	if !gitexec.ValidSHA(id) {
		return forge.Commit{}, fmt.Errorf("%w: a commit is named by its full ID", forge.ErrInvalid)
	}
	out, err := c.git(ctx, repo, "log", "-1", "--format=%H%x00%P%x00%an%x00%ae%x00%ct%x00%B", id, "--")
	if err != nil {
		return forge.Commit{}, err
	}
	f := strings.SplitN(strings.TrimRight(out, "\n"), "\x00", 6)
	if len(f) != 6 {
		return forge.Commit{}, fmt.Errorf("unexpected git output for commit %s", id)
	}
	ts, _ := strconv.ParseInt(f[4], 10, 64)
	cm := forge.Commit{ID: f[0], Author: forge.Principal{Login: f[3], Name: f[2], Unlinked: true}, Message: strings.TrimSpace(f[5]), CreatedAt: time.Unix(ts, 0).UTC()}
	if f[1] != "" {
		cm.Parents = strings.Fields(f[1])
	}
	return cm, nil
}

func (c content) Tree(ctx context.Context, repo forge.Repository, rev, dir string) ([]forge.TreeEntry, error) {
	if err := gitexec.ValidRef(rev); err != nil && !gitexec.ValidSHA(rev) && !validShortRev(rev) {
		return nil, fmt.Errorf("%w: %v", forge.ErrInvalid, err)
	}
	spec := rev + ":" + strings.Trim(dir, "/")
	if strings.Trim(dir, "/") == "" {
		spec = rev
	}
	out, err := c.git(ctx, repo, "ls-tree", "-l", "-z", spec, "--")
	if err != nil {
		return nil, err
	}
	var entries []forge.TreeEntry
	for _, rec := range strings.Split(out, "\x00") {
		meta, name, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 {
			continue
		}
		e := forge.TreeEntry{Name: name, Mode: f[0], ID: f[2]}
		switch f[1] {
		case "tree":
			e.Kind = "dir"
		case "commit":
			e.Kind = "submodule"
		default:
			e.Kind = "file"
			if f[0] == "120000" {
				e.Kind = "link"
			}
			e.Size, _ = strconv.ParseInt(f[3], 10, 64)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// validShortRev lets a plain name (a branch or tag as listed) through when it
// is not a full ref name.
func validShortRev(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && gitexec.ValidRef("refs/heads/"+s) == nil
}

func (c content) Blob(ctx context.Context, repo forge.Repository, rev, path string, limit int64) ([]byte, error) {
	if !validShortRev(rev) && !gitexec.ValidSHA(rev) {
		return nil, fmt.Errorf("%w: %q is not a ref or commit", forge.ErrInvalid, rev)
	}
	lit, err := gitexec.Literal(path)
	if err != nil {
		return nil, err
	}
	_ = lit
	size, err := c.git(ctx, repo, "cat-file", "-s", rev+":"+path)
	if err != nil {
		return nil, err
	}
	if n, _ := strconv.ParseInt(strings.TrimSpace(size), 10, 64); limit > 0 && n > limit {
		return nil, fmt.Errorf("%w: the file is %d bytes, over the limit of %d", forge.ErrInvalid, n, limit)
	}
	out, err := c.git(ctx, repo, "cat-file", "blob", rev+":"+path)
	return []byte(out), err
}

func (c content) Compare(ctx context.Context, repo forge.Repository, base, head string) (forge.Comparison, error) {
	for _, r := range []string{base, head} {
		if !gitexec.ValidSHA(r) && !validShortRev(r) {
			return forge.Comparison{}, fmt.Errorf("%w: %q is not a ref or commit", forge.ErrInvalid, r)
		}
	}
	out, err := c.git(ctx, repo, "rev-list", "--left-right", "--count", base+"..."+head, "--")
	if err != nil {
		return forge.Comparison{}, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return forge.Comparison{}, fmt.Errorf("unexpected git output comparing %s and %s", base, head)
	}
	behind, _ := strconv.Atoi(f[0])
	ahead, _ := strconv.Atoi(f[1])
	cmp := forge.Comparison{Base: base, Head: head, Ahead: ahead, Behind: behind}
	ids, err := c.git(ctx, repo, "rev-list", "--max-count=100", base+".."+head, "--")
	if err != nil {
		return cmp, err
	}
	for _, id := range strings.Fields(ids) {
		cm, err := c.Commit(ctx, repo, id)
		if err != nil {
			return cmp, err
		}
		cmp.Commits = append(cmp.Commits, cm)
	}
	return cmp, nil
}

// ---- events ----

type events struct{ p *Provider }

func (events) Mode() forge.EventMode { return forge.EventsPolling }

// Verify: plain Git has no webhook of its own to verify. A project's own hook
// (see the project settings) only makes the poller run at once.
func (e events) Verify(*http.Request, []byte) ([]forge.Event, error) {
	return nil, &forge.ErrUnsupported{Capability: forge.CapEvents, Reason: "plain Git has no webhook format",
		Workaround: "Launchway polls the repository; a project's webhook only makes it poll at once"}
}

// Poll returns what the poller decided since the cursor (a sequence number
// as text; "" for the newest), oldest first, and the cursor to continue from.
func (e events) Poll(ctx context.Context, repo forge.Repository, cursor string) ([]forge.Event, string, error) {
	var after int64
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 0 {
			return nil, cursor, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
		after = n
	}
	all, err := e.p.st.Events(ctx, e.p.conn.ID, repo.ID, 0, 1000)
	if err != nil {
		return nil, cursor, err
	}
	var out []forge.Event
	next := after
	for i := len(all) - 1; i >= 0; i-- {
		ev := all[i]
		if ev.Seq <= after {
			continue
		}
		out = append(out, forge.Event{DeliveryID: strconv.FormatInt(ev.Seq, 10), Kind: ev.Kind, Connection: ev.Connection,
			Repository: ev.Repository, Ref: ev.Ref, Old: ev.Old, Commit: ev.New, At: ev.At})
		next = ev.Seq
	}
	return out, strconv.FormatInt(next, 10), nil
}

// ---- access ----

type access struct{ p *Provider }

func (a access) CloneAddresses(_ context.Context, repo forge.Repository) (forge.CloneAddresses, error) {
	return repo.Clone, nil
}

// Credential: a plain connection hands out nothing; whatever git needs is in
// the server's own settings.
func (a access) Credential(context.Context, forge.Repository, bool) (forge.Credential, error) {
	return forge.Credential{}, nil
}
