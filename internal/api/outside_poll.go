package api

import (
	"context"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// How the watcher looks after Flights whose host does not tell Launchway what
// happens: every poll interval, a Flight whose connection has received no
// webhook within the quiet period has its host asked about the pull requests
// of its repository (the provider's Poll). The answer is remembered for the
// poller of the project to carry into the runs it starts, and the project is
// looked at now.
const (
	outsidePollEvery = time.Minute
	outsideQuiet     = 5 * time.Minute
)

// notePullRequestEvent hands what a webhook or poll said about a pull request
// to the project's poller. Only a proposal event with a head is of use.
func (s *Server) notePullRequestEvent(project string, e forge.Event) {
	if s.Projects == nil || e.Number == 0 || e.Head == "" {
		return
	}
	s.Projects.NotePullRequest(project, source.PullRequestInfo{Number: e.Number, Head: e.Head, Base: e.Base, Title: e.Title,
		Author: e.Author, Draft: e.Draft, Fork: e.Fork, URL: e.URL})
}

// StartOutsidePolls runs the watcher until ctx ends.
func (s *Server) StartOutsidePolls(ctx context.Context) {
	if s.Hosts == nil || s.Projects == nil {
		return
	}
	cursors := &sync.Map{}
	go func() {
		t := time.NewTicker(outsidePollEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.pollQuietFlights(ctx, cursors, outsideQuiet)
			}
		}
	}()
}

// pollQuietFlights is one round of the watcher.
func (s *Server) pollQuietFlights(ctx context.Context, cursors *sync.Map, quiet time.Duration) {
	flights, err := s.Hosts.QuietFlights(ctx, s.Now().Add(-quiet))
	if err != nil {
		s.Log.Warn("listing the Flights to poll", "err", err)
		return
	}
	for _, f := range flights {
		cur, _ := cursors.Load(f.Project)
		cs, _ := cur.(string)
		evs, next, err := s.Hosts.PollEvents(ctx, f.Project, cs)
		if err != nil {
			s.Log.Warn("polling the pull requests of an outside repository", "project", f.Project, "err", redact.URLs(err.Error()))
			continue
		}
		cursors.Store(f.Project, next)
		for _, e := range evs {
			s.notePullRequestEvent(f.Project, e)
		}
		if len(evs) > 0 {
			s.Projects.PollNow(f.Project)
		}
	}
}
