package engine

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// targetOf is the target a queued deploy, rollback or verify step waits for a deploy runner to serve ("" for any
// other step).
func targetOf(s *StepState) string {
	switch {
	case s == nil:
		return ""
	case s.Dep != nil && s.Dep.Target != "":
		return s.Dep.Target
	case s.Ver != nil:
		return s.Ver.Target
	}
	return ""
}

// deployWait says in plain words why a queued deploy, rollback or verify step has not been taken: the deploy runners that
// serve its target (every poll of a runner records when it was last seen) have not asked for work lately, or are busy.
// It returns nothing when a runner is free: the step is about to be taken.
func (e *Engine) deployWait(target string) (string, string) {
	all, err := e.Runners()
	if err != nil {
		return "", ""
	}
	var serving []RunnerView
	for _, r := range all {
		if r.Kind == PoolDeploy && slices.Contains(r.Targets, target) {
			serving = append(serving, r)
		}
	}
	if len(serving) == 0 {
		return fmt.Sprintf("waiting for a deploy runner serving target %s: none has asked for work lately, and none is known", target), WaitNoRunner
	}
	pools := map[string]bool{}
	for _, r := range serving {
		pools[r.Pool] = true
	}
	names := make([]string, 0, len(pools))
	for p := range pools {
		names = append(names, p)
	}
	sort.Strings(names)
	of := "of pool " + strings.Join(names, " and ")
	if len(names) > 1 {
		of = "of pools " + strings.Join(names, " and ")
	}
	if names[0] == "" && len(names) == 1 {
		of = "serving target " + target
	}

	online, busy, actions := 0, 0, 0
	var last int64
	for _, r := range serving {
		last = max(last, r.LastSeen)
		if r.Online {
			online++
			if len(r.Work) > 0 {
				busy++
				actions += len(r.Work)
			}
		}
	}
	switch {
	case online == 0:
		return fmt.Sprintf("waiting for a deploy runner %s: none has asked for work since %s", of, e.clockText(last)), WaitNoRunner
	case busy == online && online == 1:
		return fmt.Sprintf("waiting for a deploy runner %s: its runner is busy with %s", of, countOf(actions, "action")), WaitRunnersBusy
	case busy == online:
		return fmt.Sprintf("waiting for a deploy runner %s: all %d runners are busy with %s", of, online, countOf(actions, "action")), WaitRunnersBusy
	}
	return "", ""
}

func countOf(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// clockText is a time of day in UTC, with the date when it is not today.
func (e *Engine) clockText(ms int64) string {
	t, now := time.UnixMilli(ms).UTC(), e.now().UTC()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04 UTC")
	}
	return t.Format("2 Jan 15:04 UTC")
}
