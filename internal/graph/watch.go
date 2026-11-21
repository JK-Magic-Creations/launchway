package graph

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Watcher keeps the graph current without being told: it looks, cheaply and
// often, at what changed in the runs and deployments the engine keeps, and
// reads again only the scopes that moved. A push is told to the graph at once
// (Pushed); everything else is seen within one interval.
type Watcher struct {
	Runner *Runner
	DB     *engine.Database
	Every  time.Duration // default 30 seconds
	Log    *slog.Logger

	mu       sync.Mutex
	projects map[string]int64
	deploy   string
	busy     map[string]bool
	dirty    map[string]bool
}

func (w *Watcher) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

// Run ticks until the context ends.
func (w *Watcher) Run(ctx context.Context) {
	every := w.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			w.log().Warn("the software graph could not look at runs and deployments", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick is one look. The first look reads everything that exists, which is
// how a server that starts with a graph from an earlier run catches up.
func (w *Watcher) Tick(ctx context.Context) error {
	rows, err := w.DB.Query(ctx, `SELECT project, max(created_at) FROM images GROUP BY project ORDER BY project`)
	if err != nil {
		return err
	}
	now := map[string]int64{}
	for rows.Next() {
		var p string
		var at int64
		if err := rows.Scan(&p, &at); err != nil {
			rows.Close()
			return err
		}
		now[p] = at
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var n, since, opened int64
	if err := w.DB.QueryRow(ctx, `SELECT count(*), coalesce(max(since), 0) FROM environment_state`).Scan(&n, &since); err != nil {
		return err
	}
	if err := w.DB.QueryRow(ctx, `SELECT coalesce(max(opened_at), 0) FROM deployments`).Scan(&opened); err != nil {
		return err
	}
	sig := time.UnixMilli(n).UTC().Format("15:04:05.000") + "|" + time.UnixMilli(since).UTC().String() + "|" + time.UnixMilli(opened).UTC().String()
	w.mu.Lock()
	first := w.projects == nil
	var changed []string
	for p, at := range now {
		if first || w.projects[p] != at {
			changed = append(changed, p)
		}
	}
	installChanged := first || sig != w.deploy
	w.projects, w.deploy = now, sig
	w.mu.Unlock()
	for _, p := range changed {
		if _, err := w.Runner.Update(ctx, Scope{ScopeProject, p}); err != nil {
			return err
		}
	}
	if installChanged || len(changed) > 0 {
		if _, err := w.Runner.Update(ctx, Scope{ScopeInstallation, ""}); err != nil {
			return err
		}
	}
	return nil
}

// Pushed reads a repository again after a push, in the background. Pushes to
// the same repository that arrive while it is read are coalesced into one more
// read.
func (w *Watcher) Pushed(path string) {
	w.mu.Lock()
	if w.busy == nil {
		w.busy, w.dirty = map[string]bool{}, map[string]bool{}
	}
	if w.busy[path] {
		w.dirty[path] = true
		w.mu.Unlock()
		return
	}
	w.busy[path] = true
	w.mu.Unlock()
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if _, err := w.Runner.PushedRepository(ctx, path); err != nil {
				w.log().Warn("the software graph could not read a pushed repository", "repository", path, "error", err)
			}
			cancel()
			w.mu.Lock()
			if !w.dirty[path] {
				delete(w.busy, path)
				w.mu.Unlock()
				return
			}
			delete(w.dirty, path)
			w.mu.Unlock()
		}
	}()
}
