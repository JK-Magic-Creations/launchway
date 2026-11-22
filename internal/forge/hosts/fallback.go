package hosts

import (
	"context"
	"errors"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// How a run's result reaches a host that cannot take a commit status from this
// connection (the roadmap's "hosts without webhooks or statuses"): a host
// whose provider declares no results, or whose answer to a status says the
// token may not write one (403 or 404), gets the result in the pull request's
// one summary comment instead, updated at every change of state; a host with
// neither statuses nor comments gets nothing and the Flight's page says so.
// Webhooks need no counterpart here: a Flight whose connection has received no
// delivery is polled (QuietFlights), which is how a host with no webhooks, or
// a provider that declares polling only, is followed.

// Via names how a result was delivered.
type Via string

// The ways a result is delivered.
const (
	ViaStatus  Via = "status"
	ViaComment Via = "comment"
	ViaNone    Via = "none"
)

// isDenied says a host's answer was a refusal of the token's permission. The
// providers word every refusal the same way ("the host answered 403 ...").
func isDenied(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "the host answered 403") || strings.Contains(m, "the host answered 404")
}

func (s *Service) deniedKey(connID, repo string) string { return connID + "|" + repo }

// via says how a result for this repository is delivered now.
func (s *Service) via(p forge.Provider, connID, repo string) Via {
	statuses := p.Capabilities().Has(forge.CapResults)
	if statuses {
		if _, no := s.denied.Load(s.deniedKey(connID, repo)); no {
			statuses = false
		}
	}
	switch {
	case statuses:
		return ViaStatus
	case p.Capabilities().Has(forge.CapProposalNote):
		return ViaComment
	}
	return ViaNone
}

// ReportResult delivers a run's result: as a commit status when the host takes
// one, and otherwise, for a pull request (number > 0), in its summary comment
// (body is the comment's text). It returns how it was delivered; ViaNone with
// a nil error means nothing was sent (not attached, the switch is off, or the
// host has neither). A refused status is remembered, so later results go to the
// comment without asking again, and is retried as a comment at once.
func (s *Service) ReportResult(ctx context.Context, project string, r forge.Result, number int, body string) (Via, error) {
	p, f, repo, err := s.repoOf(ctx, project)
	if errors.Is(err, ErrNotAttached) {
		return ViaNone, nil
	}
	if err != nil {
		return ViaNone, err
	}
	if !f.ReportStatuses {
		return ViaNone, nil
	}
	how := s.via(p, f.connID, repo.Path)
	if how == ViaStatus {
		err := p.Results().Publish(ctx, repo, r)
		if err == nil {
			return ViaStatus, nil
		}
		if !isDenied(err) {
			return ViaNone, err
		}
		s.denied.Store(s.deniedKey(f.connID, repo.Path), true)
		how = s.via(p, f.connID, repo.Path)
	}
	if how != ViaComment || number <= 0 {
		return ViaNone, nil
	}
	if _, err := s.upsertSummary(ctx, p, f, repo, number, body); err != nil {
		return ViaNone, err
	}
	return ViaComment, nil
}

// StatusVia says how results of a Flight reach its host now.
func (s *Service) StatusVia(ctx context.Context, project string) Via {
	p, f, repo, err := s.repoOf(ctx, project)
	if err != nil {
		return ViaNone
	}
	return s.via(p, f.connID, repo.Path)
}

// resultsNote is what a Flight's page says when results do not go to the host as statuses.
func resultsNote(v Via) (forge.Declaration, bool) {
	switch v {
	case ViaComment:
		return forge.Declaration{Support: forge.Emulated, Reason: "the host takes no commit status from this connection (it has none, or the token may not write one)",
			Workaround: "Launchway posts each result in the pull request's one summary comment instead (a commit with no pull request has nowhere to say it, and its result is read in Launchway); for a status, give the token the permission to write commit statuses"}, true
	case ViaNone:
		return forge.Declaration{Support: forge.None, Reason: "the host has neither commit statuses nor comments Launchway can use",
			Workaround: "Read results in Launchway"}, true
	}
	return forge.Declaration{}, false
}
