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

// startHosting sets Git hosting up and starts its scheduled maintenance, or
// does nothing when forge.hosting is off.
func startHosting(ctx context.Context, cfg *config.Config, eng *engine.Engine, log *slog.Logger, srv *api.Server) error {
	f := cfg.Forge
	if !f.Hosting {
		return nil
	}
	if err := f.Check(); err != nil {
		return err
	}
	host, _ := os.Hostname()
	if f.MaxPushMiB == 0 {
		f.MaxPushMiB = 2048 // the database blob store's largest object
	}
	h, err := hosting.New(ctx, hosting.Options{
		Engine: eng, Data: cfg.Data, Server: host + fmt.Sprintf("-%d", os.Getpid()), BaseURL: f.PublicURL, Log: log,
		MaxRepoBytes: int64(f.MaxRepoMiB) << 20, MaxPackBytes: int64(f.MaxPushMiB) << 20,
		Provider: launchway.Options{DefaultHome: f.DefaultOwner, MaxPerHome: f.MaxPerOwner, CreatesPerMinute: f.CreatesPerMinute},
		Every:    f.MaintainInterval(), CacheBytes: int64(f.CacheGiB) << 30,
	})
	if err != nil {
		return fmt.Errorf("forge.hosting: %w", err)
	}
	srv.Forge = &api.ForgeHosting{Provider: h.Provider, RunTokens: h.RunTokens, AllowInsecureHTTP: f.AllowInsecureHTTP,
		Git: githttp.New(githttp.Options{Backend: h.Store, MaxPushBytes: int64(f.MaxPushMiB) << 20, MaxConcurrent: f.MaxConcurrent, Log: log})}
	if warns, err := h.Check(ctx); err == nil {
		for _, w := range warns {
			log.Warn("Git hosting: " + w)
		}
	}
	log.Info("Git hosting is on", "connection", h.Connection.Name)
	h.Start(ctx)
	return nil
}
