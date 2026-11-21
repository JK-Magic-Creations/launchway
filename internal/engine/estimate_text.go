package engine

import (
	"fmt"
	"strings"
	"time"
)

// ApproxDuration says a duration in the rounded way estimates are shown:
// "40s", "about 2 min", "about 1 h 10 min".
func ApproxDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < 45*time.Second:
		return fmt.Sprintf("%ds", max(1, int((d+500*time.Millisecond)/time.Second)))
	case d < 90*time.Minute:
		m := int((d + 30*time.Second) / time.Minute)
		if m < 1 {
			m = 1
		}
		return fmt.Sprintf("%d min", m)
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / (5 * time.Minute) * 5)
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// Usual says what a step usually takes, in a short phrase; empty when
// there is no estimate.
func (s *StepEstimate) Usual() string {
	switch {
	case s == nil || s.Person:
		return ""
	case !s.Enough:
		if s.Samples == 0 {
			return "has not run before"
		}
		return fmt.Sprintf("only %d earlier runs: no estimate yet", s.Samples)
	case s.Bimodal != nil:
		return fmt.Sprintf("usually about %s, sometimes %s", ApproxDuration(s.TypicalMs), ApproxDuration(max(s.Bimodal.SlowMs, s.TypicalMs)))
	}
	return "usually about " + ApproxDuration(s.TypicalMs)
}

// Sentence says in words what the run is expected to take, or took; empty
// when there is nothing to say.
func (r *RunEstimate) Sentence() string {
	switch {
	case r == nil:
		return ""
	case r.TookMs > 0 && r.Known:
		how := "about as long as usual"
		switch {
		case r.DiffPercent <= -5:
			how = fmt.Sprintf("%d%% faster than usual", -r.DiffPercent)
		case r.DiffPercent >= 5:
			how = fmt.Sprintf("%d%% slower than usual", r.DiffPercent)
		}
		return fmt.Sprintf("Took %s, %s.", exactDuration(r.TookMs), how)
	case !r.Known:
		switch r.Reason {
		case "no_history":
			return "No estimate yet: these steps have not run before."
		case "too_few":
			return fmt.Sprintf("No estimate yet: a step needs %d earlier runs.", MinEstimateSamples)
		}
		return ""
	}
	var parts []string
	if r.RemainingMs > 0 {
		t := "about " + ApproxDuration(r.RemainingMs) + " left"
		if r.Overrun {
			t += " (a step is taking longer than usual, so less certain)"
		}
		parts = append(parts, t)
	}
	if len(r.WaitingForPerson) > 0 {
		parts = append(parts, "waiting for a decision: "+strings.Join(r.WaitingForPerson, ", "))
	}
	if r.Partial {
		parts = append(parts, "at least that: no history for "+strings.Join(r.UnknownSteps, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Estimate: " + strings.Join(parts, "; ") + "."
}

func exactDuration(ms int64) string {
	d := (time.Duration(ms) * time.Millisecond).Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%dh %02dm %02ds", int(d/time.Hour), int(d%time.Hour/time.Minute), int(d%time.Minute/time.Second))
	}
	return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
}
