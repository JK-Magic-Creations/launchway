package api

// Reporting back to an outside Git host: a commit status for each run (and
// each step of it), and one summary comment on the pull request, edited in
// place. Both are per Flight switches (hosts.Flight.ReportStatuses and
// ReportComment). The reporter follows the same live updates the screens do.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/hub"
)

// reportedStates remembers what was last posted for a run, so a burst of live
// updates posts each state once. It is bounded.
type reportedStates struct {
	mu sync.Mutex
	m  map[string]string
}

func (r *reportedStates) changed(key, state string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil || len(r.m) > 4096 {
		r.m = map[string]string{}
	}
	if r.m[key] == state {
		return false
	}
	r.m[key] = state
	return true
}

// StartOutsideReports follows the engine's live updates until ctx ends and
// reports each run of an attached Flight to its host. A failed report is
// logged and never fails the run.
func (s *Server) StartOutsideReports(ctx context.Context) {
	if s.Hosts == nil || s.Engine == nil {
		return
	}
	ch, stop := s.Engine.Hub().Subscribe(func(m hub.Message) bool { return m.Type == "run" })
	rep := &reportedStates{}
	go func() {
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				sum, _ := m.Data.(*engine.RunSummary)
				if sum == nil {
					continue
				}
				s.reportRun(ctx, rep, sum)
			}
		}
	}()
}

// runResult is a run's state as a result of the host's kind.
func runResult(sum *engine.RunSummary, link string) forge.Result {
	state, text := forge.Pending, "Run #%d is running"
	switch sum.Status {
	case engine.RunSucceeded:
		state, text = forge.Passing, "Run #%d passed"
	case engine.RunFailed:
		state, text = forge.Failing, "Run #%d failed"
	case engine.RunCanceled:
		state, text = forge.Errored, "Run #%d was canceled"
	case engine.RunQueued:
		text = "Run #%d is queued"
	}
	return forge.Result{Producer: "launchway", Name: "launchway/" + sum.Pipeline, SHA: sum.SHA, State: state, Summary: fmt.Sprintf(text, sum.Number), URL: link}
}

func (s *Server) reportRun(ctx context.Context, rep *reportedStates, sum *engine.RunSummary) {
	if sum.Draft || len(sum.SHA) != 40 {
		return
	}
	link := strings.TrimSuffix(s.Hosts.PublicURL, "/")
	if link != "" {
		link += "/runs/" + sum.ID
	}
	res := runResult(sum, link)
	if !rep.changed(sum.ID, string(res.State)) {
		return
	}
	if ok, err := s.Hosts.ReportRun(ctx, sum.Project, res); err != nil {
		s.Log.Warn("a commit status could not be written to the outside host", "project", sum.Project, "run", sum.Number, "error", err)
		return
	} else if !ok {
		return
	}
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "run:" + sum.ID, Action: "outside_host.status", Project: sum.Project,
		Target: clip(sum.SHA, maxTarget), Outcome: "ok", Detail: res.Name + " " + string(res.State)})
	for _, st := range sum.Steps { // each step is its own context, on the finished run only
		if !sum.Status.Terminal() || st.Status != engine.StepFailed || st.Allowed {
			continue
		}
		sr := forge.Result{Producer: "launchway", Name: res.Name + "/" + st.ID, SHA: sum.SHA, State: forge.Failing, Summary: st.Reason, URL: link}
		if _, err := s.Hosts.ReportRun(ctx, sum.Project, sr); err != nil {
			s.Log.Warn("a step status could not be written to the outside host", "project", sum.Project, "error", err)
		}
	}
	if sum.PullRequest > 0 && sum.Status.Terminal() {
		if ok, err := s.Hosts.ReportSummary(ctx, sum.Project, sum.PullRequest, summaryBody(sum, link)); err != nil {
			s.Log.Warn("the pull request summary could not be written to the outside host", "project", sum.Project, "error", err)
		} else if ok {
			s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "run:" + sum.ID, Action: "outside_host.comment", Project: sum.Project,
				Target: fmt.Sprintf("pull request %d", sum.PullRequest), Outcome: "ok", Detail: "summary updated in place"})
		}
	}
}

// summaryBody is the one comment of a pull request: plain Markdown, no
// secrets, the same words every time so an edit replaces the last.
func summaryBody(sum *engine.RunSummary, link string) string {
	counts := map[engine.StepStatus]int{}
	var failed []string
	for _, st := range sum.Steps {
		counts[st.Status]++
		if st.Status == engine.StepFailed && !st.Allowed {
			failed = append(failed, st.ID)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Launchway: run #%d %s** on `%s`\n\n", sum.Number, sum.Status, clip(sum.SHA, 7))
	fmt.Fprintf(&b, "Steps: %d passed, %d failed, %d skipped.\n", counts[engine.StepSucceeded], counts[engine.StepFailed], counts[engine.StepSkippedS])
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\nFailed: %s\n", strings.Join(failed, ", "))
	}
	if link != "" {
		fmt.Fprintf(&b, "\n[Open the run](%s)\n", link)
	}
	return b.String()
}
