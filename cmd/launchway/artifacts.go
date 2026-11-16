package main

import (
	"log/slog"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// artifactSettings reads where artifacts are kept and the limits on them
// and on findings. A store that cannot be made (S3 credentials missing,
// say) leaves the server running without artifacts, saying why: runs that
// keep none are not affected.
func artifactSettings(cfg *config.Config, log *slog.Logger) (engine.ArtifactOptions, engine.FindingLimits, error) {
	opts, err := cfg.ArtifactLimits()
	if err != nil {
		return opts, engine.FindingLimits{}, err
	}
	limits, err := cfg.FindingLimits()
	if err != nil {
		return opts, limits, err
	}
	store, err := cfg.ArtifactStore()
	if err != nil {
		log.Error("artifacts are turned off on this server", "reason", err)
		opts.StoreProblem = err.Error()
		return opts, limits, nil
	}
	opts.Store = store
	log.Info("artifacts are kept in a store", "store", store.Name())
	return opts, limits, nil
}
