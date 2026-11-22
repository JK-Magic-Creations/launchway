package argocd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// A deployment that writes back by pull request: the change is on the work
// branch; the server opens the pull request into the branch, merges it when
// its rules are met (or leaves that to a person, as the environment says),
// and the step goes on with the commit that landed.

// PullCall is one question to the server about the deployment's pull request.
type PullCall struct {
	Token  string `json:"token"`
	Op     string `json:"op"` // open or status
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
}

// PullState is the server's answer.
type PullState struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	State      string `json:"state"` // open, merged or closed
	Commit     string `json:"commit,omitempty"`
	WaitingFor string `json:"waiting_for,omitempty"`
}

// Pulls asks the server about pull requests; the deploy runner's client does.
type Pulls interface {
	Pull(ctx context.Context, c PullCall) (PullState, error)
}

// landPull opens the deployment's pull request and waits until it is merged,
// returning the commit that landed on the branch. It fails, saying why, when
// the pull request is closed without merging; the step's own deadline ends
// the wait otherwise.
func (a *Agent) landPull(ctx context.Context, job *engine.Job, log func(string, ...any)) (string, error) {
	d := job.Deploy
	if a.Pulls == nil {
		return "", errors.New("this deploy runner cannot open pull requests: it has no way to reach the server")
	}
	subject, _, _ := strings.Cut(strings.TrimSpace(d.Message), "\n")
	body := strings.TrimSpace(d.Message) + "\n\nLaunchway-Deployment: " + d.Deployment + "\nLaunchway-Flight: " + job.Project + "\nLaunchway-Run: " + job.RunID + "\nLaunchway-Environment: " + d.Environment + "\n\nOpened by the deploy step of run #" + fmt.Sprint(job.RunNumber) + " of " + job.Project + "."
	st, err := a.Pulls.Pull(ctx, PullCall{Token: d.WriteBack.Token, Op: "open", Title: subject, Body: body})
	if err != nil {
		return "", fmt.Errorf("opening the pull request: %w", err)
	}
	log("Pull request #%d into %s: %s", st.Number, d.Branch, st.URL)
	every := a.PullEvery
	if every <= 0 {
		every = 5 * time.Second
	}
	last := ""
	for {
		switch st.State {
		case "merged":
			log("Pull request #%d was merged as %s", st.Number, short(st.Commit))
			return st.Commit, nil
		case "closed":
			return "", fmt.Errorf("pull request #%d (%s) was closed without being merged, so %s was not changed. Nothing was deployed; reopen and merge it, or run the deploy step again", st.Number, st.URL, d.Branch)
		}
		if w := "Waiting for pull request #" + fmt.Sprint(st.Number) + ": " + st.WaitingFor; w != last {
			log("%s", w)
			last = w
		}
		select {
		case <-time.After(every):
		case <-ctx.Done():
			return "", fmt.Errorf("pull request #%d (%s) was not merged in time; it waits for %s. The deploy step stops here; the pull request stays open", st.Number, st.URL, st.WaitingFor)
		}
		next, err := a.Pulls.Pull(ctx, PullCall{Token: d.WriteBack.Token, Op: "status", Number: st.Number})
		if err != nil {
			if ctx.Err() == nil {
				log("Could not ask the server about pull request #%d (%v); trying again", st.Number, err)
			}
			continue
		}
		st = next
	}
}
