package main

import (
	"context"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/togglestore"
)

// newToggles builds the feature toggle service of a server: the engine's
// database, the units the settings put projects in (looked up at most every
// few seconds), and the engine's environments for what is protected and who
// may approve there.
func newToggles(eng *engine.Engine, store *settings.Service) *togglestore.Service {
	var mu sync.Mutex
	var units map[string]string
	var at time.Time
	unitOf := func(project string) string {
		mu.Lock()
		defer mu.Unlock()
		if units == nil || time.Since(at) > 5*time.Second {
			units = map[string]string{}
			if store != nil && store.Available() {
				if lay, err := store.Levels(); err == nil {
					units = lay.ProjectUnit
				}
			}
			at = time.Now()
		}
		return units[project]
	}
	return togglestore.New(togglestore.Deps{
		DB:     eng.Database(),
		UnitOf: unitOf,
		Environment: func(name string) togglestore.Env {
			for _, e := range eng.Environments() {
				if e.Name != name {
					continue
				}
				out := togglestore.Env{Known: true, Protected: e.Protected, MinApprovals: e.MinApprovals}
				if e.Reviewers.Restricts() {
					rv := e.Reviewers
					out.MayApprove = rv.Allows
				}
				return out
			}
			return togglestore.Env{}
		},
		SelfApproval: eng.SelfApprovalAllowed,
	})
}

// startToggles gives the server its feature toggles and starts what keeps them current: snapshots follow changes
// made through other servers, counts are written, scheduled changes run.
func startToggles(ctx context.Context, eng *engine.Engine, srv *api.Server) {
	srv.Toggles = newToggles(eng, srv.Settings)
	go srv.Toggles.Run(ctx)
}
