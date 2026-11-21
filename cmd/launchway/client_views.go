package main

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

type runView struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	Number     int    `json:"number"`
	Status     string `json:"status"`
	Event      string `json:"event"`
	Branch     string `json:"branch"`
	Tag        string `json:"tag"`
	SHA        string `json:"sha"`
	Actor      string `json:"actor"`
	Message    string `json:"message"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at"`
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
		ID       string            `json:"id"`
		Status   string            `json:"status"`
		Attempt  int               `json:"attempt"`
		Error    string            `json:"error"`
		Reason   string            `json:"reason"`
		Cause    *engine.StepCause `json:"cause"`
		ExitCode *int              `json:"exit_code"`
		// AllowedFailure: failed, and its pipeline allows that.
		AllowedFailure bool `json:"allowed_failure"`
		// NotBefore is when a retry that waits is handed to a runner;
		// FailureCause why the last attempt failed.
		NotBefore    int64  `json:"not_before"`
		FailureCause string `json:"failure_cause"`
		Retry        int    `json:"retry"`
		Caches       []struct {
			Name     string `json:"name"`
			Result   string `json:"result"`
			Restored string `json:"restored"`
			Bytes    int64  `json:"bytes"`
			Scope    string `json:"scope"`
			Reason   string `json:"reason"`
			Key      string `json:"key"`
			KeyParts []struct {
				Label string `json:"label"`
				Value string `json:"value"`
			} `json:"key_parts"`
		} `json:"caches"`
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
