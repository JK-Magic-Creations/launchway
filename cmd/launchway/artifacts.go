package main

import (
	"log/slog"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// artifactSettings reads where artifacts are kept and the limits on them
// and on findings. A store that cannot be made (S3 credentials missing,
// say) leaves the server running without artifacts, saying why: runs that
// keep none are not affected. The database store, and the choice made when
// nothing is configured, are settled by the engine once the database is
// open (see logStore).
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
		if opts.Choice != engine.StoreAuto {
			log.Error("artifacts are turned off on this server", "reason", err)
		}
		opts.StoreProblem = err.Error()
		return opts, limits, nil
	}
	opts.Store = store
	return opts, limits, nil
}

// logStore says where the engine keeps artifacts. (When it keeps none,
// the engine or artifactSettings has said why already.)
func logStore(eng *engine.Engine, log *slog.Logger) {
	o := eng.ArtifactOptions()
	if o.Store == nil {
		if o.Choice == engine.StoreAuto {
			// artifactSettings left this to the engine, which may have
			// chosen the database instead of the directory it could not make.
			log.Error("artifacts are turned off on this server", "reason", o.StoreProblem)
		}
		return
	}
	log.Info("artifacts are kept in a store", "store", o.Store.Name(), "why", eng.StoreWhy())
}
