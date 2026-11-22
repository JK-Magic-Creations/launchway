package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

type runView struct {
	Estimate   *engine.RunEstimate `json:"estimate"`
	ID         string              `json:"id"`
	Project    string              `json:"project"`
	Number     int                 `json:"number"`
	Status     string              `json:"status"`
	Event      string              `json:"event"`
	Branch     string              `json:"branch"`
	Tag        string              `json:"tag"`
	SHA        string              `json:"sha"`
	Actor      string              `json:"actor"`
	Message    string              `json:"message"`
	CreatedAt  int64               `json:"created_at"`
	FinishedAt int64               `json:"finished_at"`
	// CancelReason says why a run was canceled when not by hand, such as
	// "run timeout of 2h reached"; DeadlineAt is when its timeout cancels it.
	CancelReason string `json:"cancel_reason"`
	DeadlineAt   int64  `json:"deadline_at"`
	// AllowedFailures counts the steps that failed and were allowed to.
	AllowedFailures     int      `json:"allowed_failures"`
	AllowedFailureSteps []string `json:"allowed_failure_steps"`
	// PullRequest and PullRequestInto: a pull-request run, and the branch
	// it targets.
	PullRequest     int    `json:"pull_request"`
	PullRequestInto string `json:"pull_request_into"`
	// Lane is the run's place in its pipeline's lane, if it has one.
	Lane *struct {
		Label    string `json:"label"`
		State    string `json:"state"`
		Position int    `json:"position"`
		Behind   *struct {
			Number int `json:"number"`
		} `json:"behind"`
		Reason string `json:"reason"`
	} `json:"lane"`
	// Actions are what the server says this caller may do with the run now.
	Actions []engine.Action `json:"actions"`
	// Levels are the stages: the step IDs grouped by dependency depth; the steps of one run together.
	Levels   [][]string `json:"levels"`
	StepList []struct {
		Estimate *engine.StepEstimate `json:"estimate"`
		ID       string               `json:"id"`
		Status   string               `json:"status"`
		Attempt  int                  `json:"attempt"`
		Error    string               `json:"error"`
		Reason   string               `json:"reason"`
		Cause    *engine.StepCause    `json:"cause"`
		ExitCode *int                 `json:"exit_code"`
		// AllowedFailure: failed, and its pipeline allows that.
		AllowedFailure bool `json:"allowed_failure"`
		// NotBefore is when a retry that waits is handed to a runner;
		// FailureCause why the last attempt failed.
		NotBefore    int64       `json:"not_before"`
		FailureCause string      `json:"failure_cause"`
		Retry        int         `json:"retry"`
		Caches       []cacheView `json:"caches"`
		// Usage is what the step reserved and used, when it ran.
		Usage *engine.StepUsage `json:"usage"`
		// Runner is the runner that took the step; Workspace is set for a
		// step with a workspace of its own; WaitingFor says what a queued
		// step waits for when no runner may take it.
		Runner     string `json:"runner"`
		Workspace  string `json:"workspace"`
		WaitingFor string `json:"waiting_for"`
	} `json:"step_list"`
	TriggerDetail *struct {
		Inputs   map[string]string `json:"inputs"`
		Schedule string            `json:"schedule"`
		Webhook  string            `json:"webhook"`
	} `json:"trigger_detail"`
}

// runRef says what a run is of: its branch or tag, or its pull request and
// the branch that targets.
func runRef(r runView) string {
	if r.PullRequest > 0 {
		ref := fmt.Sprintf("pull request #%d", r.PullRequest)
		if r.PullRequestInto != "" {
			ref += " into " + r.PullRequestInto
		}
		return ref
	}
	if r.Branch != "" {
		return r.Branch
	}
	return r.Tag
}

func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "canceled"
}

// cacheView is what one build cache of a step did, as a run's JSON says.
type cacheView struct {
	Name     string `json:"name"`
	Result   string `json:"result"`
	Restored string `json:"restored"`
	Bytes    int64  `json:"bytes"`
	Scope    string `json:"scope"`
	Reason   string `json:"reason"`
	Key      string `json:"key"`
	// EntryKey, Generation and AgeSeconds are the entry restored.
	EntryKey        string `json:"entry_key"`
	Generation      int    `json:"generation"`
	AgeSeconds      int64  `json:"age_seconds"`
	MaxAgeSeconds   int64  `json:"max_age_seconds"`
	SavedGeneration int    `json:"saved_generation"`
	KeyParts        []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"key_parts"`
}

// usualNote is what a step that has not finished usually takes, for runs show.
func usualNote(e *engine.StepEstimate, runStatus, stepStatus string) string {
	switch stepStatus {
	case "running", "queued", "pending", "leased":
	default:
		return ""
	}
	if u := e.Usual(); u != "" && !terminal(runStatus) {
		return " [" + u + "]"
	}
	return ""
}

// durationText is a count of seconds as days, hours or minutes.
func durationText(s int64) string {
	d := time.Duration(s) * time.Second
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return "under 2 minutes"
}

// printCache says what one build cache of a step did.
func (cl *client) printCache(c cacheView) {
	what := strings.ReplaceAll(c.Result, "_", " ")
	if c.Result == "saved" && c.Restored != "" {
		what = "saved (restored: " + c.Restored + ")"
	}
	if c.Bytes > 0 {
		what += ", " + byteText(c.Bytes)
	}
	if c.Scope != "" {
		what += ", scope " + c.Scope
	}
	if c.Reason != "" {
		what += ": " + c.Reason
	}
	fmt.Fprintf(cl.out, "             cache %s: %s\n", c.Name, what)
	if c.EntryKey != "" {
		used := "entry restored"
		if c.Result == "expired" {
			used = "entry found too old"
		}
		fmt.Fprintf(cl.out, "               %s: key %s, generation %d, %s old (a cache may be %s)\n", used, c.EntryKey, c.Generation, durationText(c.AgeSeconds), durationText(c.MaxAgeSeconds))
	}
	if c.SavedGeneration > 0 {
		fmt.Fprintf(cl.out, "               saved generation %d\n", c.SavedGeneration)
	}
	if c.Key != "" {
		fmt.Fprintf(cl.out, "               key %s, made of:\n", c.Key)
		for _, p := range c.KeyParts {
			fmt.Fprintf(cl.out, "                 %s: %s\n", p.Label, p.Value)
		}
	}
}
