// Package hosting is the composition of Launchway's own Git hosting: the
// repository storage on the engine's database and blob store, the forge
// tables, the connection of kind launchway and its provider, and the
// scheduled maintenance. cmd/launchway builds one when forge.hosting is on;
// the API serves Git over HTTP from it.
package hosting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	mirroregress "github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/launchway"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/refrules"
	"github.com/jk-magic-creations/launchway/internal/forge/sshkeys"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

// ConnectionName is the name of the connection of kind launchway, made when
// hosting is first turned on.
const ConnectionName = "launchway"

// Options configure Hosting.
type Options struct {
	Engine *engine.Engine
	// Blobs holds the packs; default the engine's artifact store.
	Blobs blobstore.Store
	// Data is the server's data directory; the cache lives in <Data>/repos.
	Data string
	// Server names this server (the holder of maintenance leases).
	Server string
	// BaseURL is the address clients reach this server at.
	BaseURL string
	// Storage limits; zero is the storage package's default.
	MaxRepoBytes, MaxPackBytes int64
	Limits                     gitexec.Limits
	// Provider options (quotas, rate limit).
	Provider launchway.Options
	// MirrorAllow lists the internal hosts and networks mirrors may reach
	// (forge.mirror_allow); everything internal is refused otherwise. Guard,
	// when set, replaces it (tests).
	MirrorAllow []string
	Guard       *mirroregress.Guard
	// Mirror configures the mirror service (limits, timings).
	Mirror mirror.Options
	// Every is how often maintenance runs (default 15 minutes); CacheBytes is
	// what the caches may take before the least recently used are removed
	// (0: no limit).
	Every      time.Duration
	CacheBytes int64
	Log        *slog.Logger
	// Egress judges the addresses webhooks are sent to (default egress.Default,
	// which the installation's egress.allow configures). Hooks tunes
	// retries and the delivery client; zero values are the defaults.
	Egress *egress.Guard
	Hooks  webhooks.Options
	// Secrets seals the secrets hosting stores (the run token key, webhook
	// secrets) under the installation's root key. Nil: a service with no
	// root key is made, which keeps the run token key in the clear and
	// refuses webhook secrets, as a server with no root key does.
	Secrets *sealing.Service
}

// Hosting is hosting, set up.
type Hosting struct {
	Store      *storage.Store
	Forge      *store.Store
	Provider   *launchway.Provider
	Connection forge.Connection
	// Mirrors runs pull and push mirrors and imports.
	Mirrors *mirror.Service
	// RunTokens makes and checks the read-only tokens of runs.
	RunTokens githttp.RunTokens
	// Keys is the SSH and deploy keys, Rules the protected branches.
	Keys  *sshkeys.Store
	Rules *refrules.Store
	// Hooks sends the webhooks of repositories.
	Hooks *webhooks.Service
	// Pulls is pull requests and code review.
	Pulls *pulls.Service
	o     Options
	log   *slog.Logger
}

// New refuses (with the reason) when this server cannot host: git too old,
// no blob store, or a blob store that is not the one the database was set up
// with. Otherwise it makes the storage and, the first time, the connection.
func New(ctx context.Context, o Options) (*Hosting, error) {
	if o.Engine == nil {
		return nil, errors.New("hosting needs the engine")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	v, err := CheckGit(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range v.Missing() {
		o.Log.Warn("git is too old for part of hosting", "git", v.String(), "missing", m)
	}
	blobs := o.Blobs
	if blobs == nil {
		blobs = o.Engine.ArtifactOptions().Store
	}
	if blobs == nil {
		return nil, errors.New("forge.hosting needs a blob store for the packs, and this server has none (artifacts are off: " + o.Engine.ArtifactOptions().StoreProblem + ")")
	}
	sto, err := storage.New(ctx, storage.Options{DB: o.Engine.Database(), Blobs: blobs, CacheDir: filepath.Join(o.Data, "repos"),
		Server: o.Server, MaxRepoBytes: o.MaxRepoBytes, MaxPackBytes: o.MaxPackBytes, Limits: o.Limits, Log: o.Log})
	if err != nil {
		return nil, err
	}
	fs := store.New(o.Engine.Database())
	conn, err := fs.ConnectionByName(ctx, ConnectionName)
	if errors.Is(err, store.ErrNotFound) {
		conn, err = fs.CreateConnection(ctx, forge.Connection{Name: ConnectionName, Kind: forge.KindLaunchway, Events: forge.EventsInProcess, Enabled: true}, "system")
	}
	if err != nil {
		return nil, fmt.Errorf("the %s connection: %w", ConnectionName, err)
	}
	if conn.Kind != forge.KindLaunchway {
		return nil, fmt.Errorf("a connection named %s exists and is of kind %s; rename it before turning hosting on", ConnectionName, conn.Kind)
	}
	sec := o.Secrets
	if sec == nil {
		if sec, err = sealing.Start(ctx, sealing.Options{DB: o.Engine.Database(), Log: o.Log}); err != nil {
			return nil, err
		}
	}
	key, err := sec.SharedValueContext(ctx, "git_run_token_key", auth.NewKey())
	if err != nil {
		return nil, fmt.Errorf("the key for run tokens: %w", err)
	}
	rt := githttp.RunTokens{Key: []byte(key)}
	po := o.Provider
	po.BaseURL = o.BaseURL
	if po.Minter == nil {
		po.Minter = rt
	}
	guard := o.Guard
	if guard == nil {
		if guard, err = mirroregress.New(o.MirrorAllow); err != nil {
			return nil, fmt.Errorf("forge.mirror_allow: %w", err)
		}
	}
	mo := o.Mirror
	mo.Guard, mo.Server, mo.Limits, mo.Log = guard, o.Server, o.Limits, o.Log
	ms := mirror.New(o.Engine.Database(), fs, sto, conn, mo)
	pullSvc := pulls.NewService(pulls.NewStore(o.Engine.Database()), sto, o.Log)
	po.Pulls = pullSvc // the provider answers for proposals with the pull-request service
	prov := launchway.New(conn, fs, sto, po)
	ms.SetCreator(prov.Repositories())
	db := o.Engine.Database()
	hk := o.Hooks
	hk.DB, hk.Log = db, o.Log
	if hk.Sealer == nil {
		hk.Sealer = sec.Webhooks()
	}
	if hk.Client == nil {
		g := o.Egress
		if g == nil {
			g = egress.Default
		}
		hk.Client = g.HTTPClient(10 * time.Second)
	}
	return &Hosting{Store: sto, Forge: fs, Provider: prov, Connection: conn, Mirrors: ms, RunTokens: rt,
		Keys: sshkeys.New(db), Rules: refrules.New(db), Hooks: webhooks.New(hk), Pulls: pullSvc, o: o, log: o.Log}, nil
}

// Resolve finds a live repository by its owner and name.
func (h *Hosting) Resolve(ctx context.Context, path string) (forge.Repository, error) {
	return h.Provider.Repositories().Get(ctx, path)
}

// Start runs maintenance until ctx ends: consolidate packs, collect what is
// retired or abandoned, verify a sample, and evict old caches. Each server
// does its own; the storage's leases keep two from repacking one repository.
func (h *Hosting) Start(ctx context.Context) {
	h.Mirrors.Start(ctx)
	go h.Hooks.Run(ctx)
	go h.sweepPulls(ctx)
	every := h.o.Every
	if every <= 0 {
		every = 15 * time.Minute
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for n := 1; ; n++ {
			h.Maintain(ctx, n%4 == 0)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Maintain runs one round over every live repository. verify adds the
// integrity check (re-reading packs against their SHA-256).
func (h *Hosting) Maintain(ctx context.Context, verify bool) {
	page, err := h.Provider.Repositories().List(ctx, "")
	if err != nil {
		h.log.Warn("hosting maintenance: listing repositories", "err", err)
		return
	}
	for _, r := range page.Items {
		if ctx.Err() != nil {
			return
		}
		if _, err := h.Store.Maintain(ctx, r.ID, 0); err != nil {
			h.log.Warn("hosting maintenance: consolidating", "repository", r.Path, "err", err)
		}
		if _, err := h.Store.Collect(ctx, r.ID, storage.RetiredGrace, storage.PendingAge); err != nil {
			h.log.Warn("hosting maintenance: collecting", "repository", r.Path, "err", err)
		}
		if verify {
			rep, err := h.Store.Verify(ctx, r.ID, 3)
			if err != nil || len(rep.Problems) > 0 {
				h.log.Error("hosting maintenance: verification found a problem", "repository", r.Path, "problems", rep.Problems, "err", err)
			}
		}
	}
	h.PurgeExpired(ctx)
	if h.o.CacheBytes > 0 {
		if _, err := h.Store.EvictCaches(h.o.CacheBytes); err != nil {
			h.log.Warn("hosting maintenance: evicting caches", "err", err)
		}
	}
}

// Check lists what an operator should know: repositories whose owner is a
// name Launchway keeps for its own paths, so that the address other Git hosts
// use (/<owner>/<name>.git) cannot reach them. Names are checked when a
// repository is made, so this finds only data that came in another way.
func (h *Hosting) Check(ctx context.Context) ([]string, error) {
	page, err := h.Provider.Repositories().List(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range page.Items {
		if launchway.ReservedOwner(r.Home) {
			out = append(out, fmt.Sprintf("the owner of %s is %q, a path Launchway uses itself: the repository is reachable at /git/%s.git only; rename the owner", r.Path, r.Home, r.Path))
		}
	}
	return out, nil
}

// sweepPulls tries the pull requests that asked for auto-merge now and then:
// a run that finishes does not tell the pull request, so a periodic look
// closes that gap.
func (h *Hosting) sweepPulls(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.Pulls.Stop()
			return
		case <-t.C:
			h.Pulls.Sweep(ctx)
		}
	}
}
