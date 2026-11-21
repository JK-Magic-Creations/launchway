package main

// Linking the storage package is what makes the binary answer git's hooks of
// hosted repositories: they are symlinks to it, and the package registers the
// entry that handles them (see gitexec.RegisterEntry, which main runs first).
import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/launchway"
	_ "github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// hostedForge is Git hosting when forge.hosting is on; nil when it is off,
// and a nil one starts nothing.
type hostedForge struct{ h *hosting.Hosting }

// newHosting sets hosting up, or returns nil when it is off.
func newHosting(cfg *config.Config, eng *engine.Engine, log *slog.Logger, srv *api.Server) (*hostedForge, error) {
	f := cfg.Forge
	if !f.Hosting {
		return nil, nil
	}
	if err := f.Check(); err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	if f.MaxPushMiB == 0 {
		f.MaxPushMiB = 2048 // the database blob store's largest object
	}
	h, err := hosting.New(context.Background(), hosting.Options{
		Engine: eng, Data: cfg.Data, Server: host + fmt.Sprintf("-%d", os.Getpid()), BaseURL: f.PublicURL, Log: log,
		MaxRepoBytes: int64(f.MaxRepoMiB) << 20, MaxPackBytes: int64(f.MaxPushMiB) << 20,
		Provider: launchway.Options{DefaultHome: f.DefaultOwner, MaxPerHome: f.MaxPerOwner, CreatesPerMinute: f.CreatesPerMinute},
		Every:    f.MaintainInterval(), CacheBytes: int64(f.CacheGiB) << 30,
	})
	if err != nil {
		return nil, err
	}
	srv.Forge = &api.ForgeHosting{Provider: h.Provider, RunTokens: h.RunTokens, AllowInsecureHTTP: f.AllowInsecureHTTP,
		Git: githttp.New(githttp.Options{Backend: h.Store, MaxPushBytes: int64(f.MaxPushMiB) << 20, MaxConcurrent: f.MaxConcurrent, Log: log})}
	if warns, err := h.Check(context.Background()); err == nil {
		for _, w := range warns {
			log.Warn("Git hosting: " + w)
		}
	}
	log.Info("Git hosting is on", "connection", h.Connection.Name)
	return &hostedForge{h}, nil
}

// Start runs the scheduled maintenance.
func (f *hostedForge) Start(ctx context.Context) {
	if f != nil {
		f.h.Start(ctx)
	}
}
