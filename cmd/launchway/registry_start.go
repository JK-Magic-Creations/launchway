package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/registry"
	"github.com/jk-magic-creations/launchway/internal/registry/enginedb"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

// startRegistry sets the built-in registry up and starts its scheduled
// maintenance, or does nothing when registry.enabled is false or the server
// has no blob store. Blobs live in the artifact store (artifacts.store), so a
// registry shared by several servers needs a store they all share, exactly as
// Git hosting does.
func startRegistry(ctx context.Context, cfg *config.Config, eng *engine.Engine, log *slog.Logger, srv *api.Server) error {
	r := cfg.Registry
	if !r.On() {
		return nil
	}
	if err := r.Check(); err != nil {
		return err
	}
	arts := eng.ArtifactOptions()
	if arts.Store == nil {
		log.Warn("the built-in registry is off: this server has no artifact store to keep blobs in", "reason", arts.StoreProblem)
		return nil
	}
	sec := srv.Secrets
	if sec == nil {
		var err error
		if sec, err = sealing.Start(ctx, sealing.Options{DB: eng.Database(), Log: log}); err != nil {
			return err
		}
	}
	key, err := sec.SharedValueContext(ctx, "registry_token_key", auth.NewKey())
	if err != nil {
		return fmt.Errorf("the key for registry credentials: %w", err)
	}
	host, _ := os.Hostname()
	lim := registry.Limits{MaxBlobBytes: int64(r.MaxBlobMiB) << 20, MaxManifestBytes: int64(r.MaxManifestKiB) << 10,
		RequestsPerMinute: r.RequestsPerMinute, MaxRepositoriesPerProject: r.MaxRepositoriesPerProject, MaxTagsPerRepository: r.MaxTagsPerRepository}
	switch {
	case r.DefaultQuotaGiB < 0:
		lim.DefaultQuotaBytes = -1
	case r.DefaultQuotaGiB > 0:
		lim.DefaultQuotaBytes = int64(r.DefaultQuotaGiB) << 30
	}
	for _, d := range []struct {
		in  string
		out *time.Duration
	}{{r.GCGrace, &lim.GCGrace}, {r.UploadTTL, &lim.UploadTTL}} {
		if v, err := time.ParseDuration(d.in); err == nil {
			*d.out = v
		}
	}
	var svc *registry.Service
	svc = registry.New(registry.Options{DB: enginedb.New(eng.Database()), Blobs: arts.Store, Limits: lim, Log: log,
		Server: fmt.Sprintf("%s-%d", host, os.Getpid()), OnEvent: srv.RegistryEvent,
		Protected: func(ctx context.Context, repo, digest string) bool { return deployedImage(ctx, eng, svc, repo, digest) }})
	public := r.PublicURL
	if public == "" {
		public = cfg.Forge.PublicURL
	}
	srv.Registry = &api.RegistryHosting{Svc: svc, Signer: registry.Signer{Key: []byte(key)}, PublicURL: public,
		AllowInsecureHTTP: r.AllowInsecureHTTP, StoreName: arts.Store.Name()}
	if every := r.Maintain(); every > 0 {
		go svc.Maintain(ctx, every)
	}
	log.Info("the built-in registry is on", "store", arts.Store.Name(), "address", public)
	return nil
}

// deployedImage reports whether an environment runs this image: some
// environment's current version is the image's digest, or one of its tags (or
// repository:tag). Retention and deletion never remove such an image. A
// version that merely looks like one of the tags protects it too: keeping too
// much is the safe side.
func deployedImage(ctx context.Context, eng *engine.Engine, svc *registry.Service, repo, digest string) bool {
	versions := []any{digest}
	if tags, err := svc.TagsOf(ctx, repo, digest); err == nil {
		for _, t := range tags {
			versions = append(versions, t, repo+":"+t)
		}
	}
	q := `SELECT count(*) FROM environment_state WHERE current_version IN (?` + strings.Repeat(", ?", len(versions)-1) + `)`
	var n int
	if err := eng.Database().QueryRow(ctx, q, versions...).Scan(&n); err != nil {
		return true // cannot tell: keep it
	}
	return n > 0
}
