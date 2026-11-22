package argocd

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Asking the controller to REFRESH an application (to re-read its sources) is not a sync: it changes nothing in the
// cluster. The controller caches what a branch points at for minutes on top of its reconcile interval, so a step that
// waits for a commit to be noticed would wait for that cache, and with sync.request: never nothing else would end it.
// While a step waits for a commit to be noticed, the runner therefore asks for a hard refresh, again and again with a
// growing pause, where its credentials allow it (patch on the Application). Where they do not, it says exactly which
// refresh a person should request, keeps waiting, and picks up by itself. sync.refresh: never turns it off.

// refreshEvery is the pause before the second ask (it doubles each time, up to refreshMax).
const (
	refreshFirst = 10 * time.Second
	refreshMax   = 3 * time.Minute
)

type refreshState struct {
	asked  int
	next   time.Time
	denied bool
}

type refresher struct {
	mu sync.Mutex
	st map[string]*refreshState
}

// try asks for a hard refresh of app if the pause since the last ask has passed. key says what is waiting (a
// deployment's watch, or a parent application). It returns how many times it has asked so far and whether the runner
// may not (the right to change the application is missing).
func (r *refresher) try(ctx context.Context, cl *Cluster, key, app string, first time.Duration) (asked int, denied bool, err error) {
	r.mu.Lock()
	if r.st == nil {
		r.st = map[string]*refreshState{}
	}
	s := r.st[key]
	if s == nil {
		s = &refreshState{}
		r.st[key] = s
	}
	if s.denied || time.Now().Before(s.next) {
		asked, denied = s.asked, s.denied
		r.mu.Unlock()
		return asked, denied, nil
	}
	r.mu.Unlock()
	rerr := cl.RefreshHard(ctx, app)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case errors.Is(rerr, ErrNoPatchRight):
		s.denied = true
		return s.asked, true, rerr
	case rerr != nil:
		s.next = time.Now().Add(first)
		return s.asked, false, rerr
	}
	pause := first << min(s.asked, 5)
	s.asked++
	s.next = time.Now().Add(min(pause, max(refreshMax, first)))
	return s.asked, false, nil
}

// forget ends what is kept for a key once the commit is noticed.
func (r *refresher) forget(key string) {
	r.mu.Lock()
	delete(r.st, key)
	r.mu.Unlock()
}

func (a *Agent) refreshFirst() time.Duration {
	if a.RefreshEvery > 0 {
		return a.RefreshEvery
	}
	return refreshFirst
}

// howToRefresh is what a person requests when the runner may not.
func howToRefresh(app string) string {
	return fmt.Sprintf("request a hard refresh of %s (set the annotation argocd.argoproj.io/refresh=hard on the Application, or run: argocd app get %s --hard-refresh)", app, app)
}

// noticeCommit runs whenever a deployment's watch is read while Argo CD has not noticed its commit: it asks for a
// refresh (see above) and puts in the snapshot what it did, so the step says it.
func (a *Agent) noticeCommit(ctx context.Context, cl *Cluster, t Target, it engine.WatchItem, app *deploy.App) {
	key := "watch:" + it.Key + ":" + it.Application
	if it.Kind != "" || it.Spec.Commit == "" || !app.Found {
		return
	}
	if app.Contains != nil && *app.Contains {
		a.refresh.forget(key)
		return
	}
	if it.Spec.Refresh == "never" || !t.Refresh() {
		return
	}
	n, denied, _ := a.refresh.try(ctx, cl, key, it.Application, a.refreshFirst())
	switch {
	case denied:
		app.RefreshNeeded = "Launchway may not refresh " + it.Application + " (it needs the right to patch the Application), so Argo CD finds the commit only when it re-reads the repository by itself, which can take minutes: you can " + howToRefresh(it.Application) + "; this step picks up by itself"
	case n > 0:
		app.RefreshAsked = fmt.Sprintf("Launchway asked Argo CD to re-read the repository for %s (a hard refresh, which syncs nothing) %d time(s) so that it notices the commit", it.Application, n)
	}
}
