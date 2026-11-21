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

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/launchway"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// ConnectionName is the name of the connection of kind launchway, made when
// hosting is first turned on.
const ConnectionName = "launchway"

// MinGit is the oldest git that can host (the reference-transaction hook is
// the push's commit point).
var MinGit = [2]int{2, 28}

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
	// Every is how often maintenance runs (default 15 minutes); CacheBytes is
	// what the caches may take before the least recently used are removed
	// (0: no limit).
	Every      time.Duration
	CacheBytes int64
	Log        *slog.Logger
}

// Hosting is hosting, set up.
type Hosting struct {
	Store      *storage.Store
	Forge      *store.Store
	Provider   *launchway.Provider
	Connection forge.Connection
	o          Options
	log        *slog.Logger
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
	v, err := gitexec.Installed(ctx)
	if err != nil {
		return nil, fmt.Errorf("forge.hosting needs git on this server: %w", err)
	}
	if !v.AtLeast(MinGit[0], MinGit[1]) {
		return nil, fmt.Errorf("forge.hosting needs git %d.%d or newer; this server has %s", MinGit[0], MinGit[1], v)
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
	po := o.Provider
	po.BaseURL = o.BaseURL
	return &Hosting{Store: sto, Forge: fs, Provider: launchway.New(conn, fs, sto, po), Connection: conn, o: o, log: o.Log}, nil
}

// Resolve finds a live repository by its owner and name.
func (h *Hosting) Resolve(ctx context.Context, path string) (forge.Repository, error) {
	return h.Provider.Repositories().Get(ctx, path)
}

// Start runs maintenance until ctx ends: consolidate packs, collect what is
// retired or abandoned, verify a sample, and evict old caches. Each server
// does its own; the storage's leases keep two from repacking one repository.
func (h *Hosting) Start(ctx context.Context) {
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
	if h.o.CacheBytes > 0 {
		if _, err := h.Store.EvictCaches(h.o.CacheBytes); err != nil {
			h.log.Warn("hosting maintenance: evicting caches", "err", err)
		}
	}
}
