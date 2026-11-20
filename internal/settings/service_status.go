package settings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// ServerState is what one server last said about the configuration.
type ServerState struct {
	Server string `json:"server"`
	// Revision is the revision it applied last, or tried and could not.
	Revision int64  `json:"revision"`
	State    string `json:"state"` // applied or failed
	Problem  string `json:"problem,omitempty"`
	At       int64  `json:"at"`
}

// Status says what this server runs with, and what the others do.
type Status struct {
	// Server is this server's name.
	Server string `json:"server"`
	// Revision is the revision this server applied; Stored is the revision
	// in the database. They differ while a change is on its way, or cannot
	// be applied (Problem says why; it is tried again every few seconds).
	Revision int64  `json:"revision"`
	Stored   int64  `json:"stored_revision"`
	Problem  string `json:"problem,omitempty"`
	// Safe: the stored configuration could not be loaded at start-up, and
	// this server runs with something safer until it can.
	Safe *Safe `json:"safe_mode,omitempty"`
	// Servers is each server's last report (from the audit trail), this one
	// included: which have applied the stored revision and which have not.
	Servers []ServerState `json:"servers,omitempty"`
}

// Status returns the revision in effect and why a newer one is not, if not.
// It reads the database for the stored revision and the servers' reports.
func (s *Service) Status() Status {
	s.mu.Lock()
	st := Status{Server: s.server, Revision: s.rev, Problem: s.lastErr, Safe: s.safe}
	s.mu.Unlock()
	if s.Available() {
		st.Stored, _ = s.eng.ConfigRevision()
		st.Servers = s.serverStates()
	}
	return st
}

// ConfigServerAction is the audit action each server records when it applies
// a revision, or cannot.
const ConfigServerAction = "config.server_state"

// recordState writes this server's state to the audit trail when it changes,
// so every server can see which have applied which revision.
func (s *Service) recordState(rev int64, err error) {
	state, detail, outcome := "applied", "", "ok"
	if err != nil {
		state, detail, outcome = "failed", clipText(redactText(err.Error()), 400), "failed"
	}
	key := fmt.Sprintf("%d/%s/%s", rev, state, detail)
	if key == s.recorded {
		return
	}
	s.recorded = key
	e := engine.AuditEntry{At: time.Now().UnixMilli(), User: s.server, Source: "server", Action: ConfigServerAction,
		Target: fmt.Sprintf("revision %d", rev), Outcome: outcome, Detail: detail}
	if err := s.eng.Audit(e); err != nil {
		s.log.Warn("could not record the configuration state of this server", "err", err)
	}
}

func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func redactText(s string) string { return redact.URLs(s) }

// serverStates reads each server's newest report.
func (s *Service) serverStates() []ServerState {
	list, err := s.eng.AuditLog(engine.AuditFilter{Action: ConfigServerAction, Limit: 200})
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []ServerState
	for _, a := range list { // newest first
		if seen[a.User] {
			continue
		}
		seen[a.User] = true
		var rev int64
		rev, _ = strconv.ParseInt(strings.TrimPrefix(a.Target, "revision "), 10, 64)
		st := ServerState{Server: a.User, Revision: rev, State: "applied", At: a.At}
		if a.Outcome != "ok" {
			st.State, st.Problem = "failed", a.Detail
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Server < out[j].Server })
	return out
}

// pruneHistory removes the changes older than history.retain, at most once
// an hour. Every server may do it; the removal is the same.
func (s *Service) pruneHistory() {
	s.mu.Lock()
	cur, last := s.cur, s.lastPrune
	s.mu.Unlock()
	if cur == nil || cur.History.Retain == "" || time.Since(last) < time.Hour {
		return
	}
	d, ok := pipeline.ParseRetain(cur.History.Retain)
	if !ok {
		return
	}
	s.mu.Lock()
	s.lastPrune = time.Now()
	s.mu.Unlock()
	if n, err := s.eng.PruneConfigHistory(time.Now().Add(-d).UnixMilli()); err != nil {
		s.log.Warn("could not remove old configuration history", "err", err)
	} else if n > 0 {
		s.log.Info("removed configuration history older than the retention setting", "changes", n, "retain", cur.History.Retain)
	}
}
