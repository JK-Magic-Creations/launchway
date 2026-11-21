package runner

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

// What a runner checks in every job before it runs anything: the hard
// rules that hold for every runner taking work from another machine, and
// its accept: section. A server that has been taken over can still send
// anything; these are the runner's own answer to it.

// serverID is what IDs from the server look like (internal/ids): a prefix,
// an underscore and 20 characters. A job's IDs become paths on the runner,
// so anything else is refused rather than joined into one.
var serverID = regexp.MustCompile(`^[a-z]{1,16}_[0-9a-z]{20}$`)

// refusal says why the runner will not run a job, or "" if it will.
// executorName is what runs it: the runner's executor, buildkit for an
// image step, deploy for a deploy action. remote is true for a runner on
// another machine than the server (launchway runner), which never uses a
// directory of its own host as a run's source.
func refusal(c *accept.Config, job *engine.Job, executorName string, remote bool) string {
	for _, id := range []string{job.RunID, job.LeaseID} {
		if !serverID.MatchString(id) {
			return fmt.Sprintf("the server sent an ID that is not one of its own (%q); it is not used as a path", clipID(id))
		}
	}
	if remote && job.Deploy == nil && job.Source.Kind == "dir" {
		return "the server asked to use a directory of this host as the run's source, which only `launchway run` does"
	}
	if c.Empty() {
		return ""
	}
	if why := c.Project(job.Project); why != "" {
		return why
	}
	if why := c.Event(job.Event); why != "" {
		return why
	}
	if job.Source.Kind == "git" {
		if why := c.Repository(job.Source.URL); why != "" {
			return redact.URLs(why)
		}
	}
	for _, l := range job.Source.Libraries {
		if l.URL == starters.URL {
			continue // carried by the runner itself; nothing is fetched
		}
		if why := c.Repository(l.URL); why != "" {
			return "library " + l.Name + ": " + redact.URLs(why)
		}
	}
	if job.Deploy == nil {
		if why := c.Image(job.Image); why != "" {
			return why
		}
		for _, ct := range job.Containers {
			if why := c.Image(ct.Image); why != "" {
				return "container " + ct.Name + ": " + why
			}
		}
	}
	if why := c.Executor(executorName); why != "" {
		return why
	}
	if job.Deploy != nil {
		if why := c.Environment(job.Deploy.Environment); why != "" {
			return why
		}
	}
	if max := c.Timeout(); max > 0 {
		if job.Timeout.D() <= 0 {
			return fmt.Sprintf("the step has no time limit, and accept.max_timeout is %s", max)
		}
		if why := c.StepTimeout(job.Timeout.D()); why != "" {
			return why
		}
	}
	names := make([]string, 0, len(job.Env)+len(job.Secrets))
	for k := range job.Env {
		names = append(names, k)
	}
	for k := range job.Secrets {
		names = append(names, k)
	}
	for _, ct := range job.Containers {
		for k := range ct.Env {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if why := c.EnvName(n); why != "" {
			return why
		}
	}
	return ""
}

// verifyRefusal says why a deploy runner will not read a verification, or
// "" if it will: the server's IDs are its own, and the project, event and
// environment are ones its accept: section allows. It is checked before
// the runner asks any health source anything.
func verifyRefusal(c *accept.Config, job *engine.Job) string {
	for _, id := range []string{job.RunID, job.LeaseID} {
		if !serverID.MatchString(id) {
			return fmt.Sprintf("the server sent an ID that is not one of its own (%q); it is not used as a path", clipID(id))
		}
	}
	if job.Verify == nil {
		return "the server sent a verification without saying what to verify"
	}
	if why := c.Project(job.Project); why != "" {
		return why
	}
	if why := c.Event(job.Event); why != "" {
		return why
	}
	return c.Environment(job.Verify.Environment)
}

func clipID(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

// refuse tells the server the runner will not run a job, and why: it
// fails the step without retries, and the step's log says so.
func refuse(ctx context.Context, c *Client, job *engine.Job, why string) error {
	_ = c.Logs(ctx, job.LeaseID, []engine.LogLine{{Stream: "system", Text: "This runner refused the step: " + why}})
	return c.Finish(ctx, job.LeaseID, engine.Result{Outcome: engine.OutcomeRefused, Error: why})
}
