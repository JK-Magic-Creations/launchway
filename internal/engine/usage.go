package engine

import (
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// StepUsage is what one step attempt really used, measured by the runner
// from the cgroup of the container the step ran in (see
// executor.Usage). Where nothing could be read, Measured is false and the
// numbers are zero: "not measured" is never turned into a guess.
//
// A container can be shared by steps of the run that use the same image and
// run at the same time; the numbers are then the container's, which is what
// the limit applies to, and Shared says so.
type StepUsage struct {
	Measured bool `json:"measured"`
	// Method is how it was read: cgroup2, cgroup1 or sampled (the peak is
	// the highest of samples taken every second, so a spike shorter than
	// that can be missed).
	Method string `json:"method,omitempty"`
	// Note says why a step was not measured, or a limit of what was.
	Note string `json:"note,omitempty"`
	// MemoryPeak is the most memory used, in bytes; CPUPeakMilli the most CPU
	// used over any second sampled, in thousandths of a CPU; CPUMillis the CPU
	// time used in all, in milliseconds.
	MemoryPeak   int64 `json:"memory_peak_bytes,omitempty"`
	CPUPeakMilli int64 `json:"cpu_peak_milli,omitempty"`
	CPUMillis    int64 `json:"cpu_ms,omitempty"`
	// ReservedMemory and ReservedCPUMilli are what was reserved for the
	// container the step ran in (0: nothing was).
	ReservedMemory   int64 `json:"reserved_memory_bytes,omitempty"`
	ReservedCPUMilli int64 `json:"reserved_cpu_milli,omitempty"`
	Attempt          int   `json:"attempt,omitempty"`
}

func clampUsage(u *StepUsage) {
	u.MemoryPeak, u.CPUPeakMilli, u.CPUMillis = max(u.MemoryPeak, 0), max(u.CPUPeakMilli, 0), max(u.CPUMillis, 0)
	u.ReservedMemory, u.ReservedCPUMilli = max(u.ReservedMemory, 0), max(u.ReservedCPUMilli, 0)
	if !u.Measured {
		u.MemoryPeak, u.CPUPeakMilli, u.CPUMillis, u.Method = 0, 0, 0, ""
	}
	u.Method = strings.TrimSpace(u.Method)
	if len(u.Method) > 32 {
		u.Method = u.Method[:32]
	}
	if len(u.Note) > 300 {
		u.Note = u.Note[:300]
	}
}

// endFinishedAttempt ends the lease of a step attempt that reported its
// result and records what the attempt used.
func (c *cmd) endFinishedAttempt(leaseID string, l leaseRef, res Result) error {
	if err := c.endFinishedLease(leaseID); err != nil {
		return err
	}
	return c.keepUsage(l, res)
}

// keepUsage records what the attempt used, with the step's result. A step
// that was never run (refused, waiting, lost) has none.
func (c *cmd) keepUsage(l leaseRef, res Result) error {
	if res.Usage == nil || !c.e.knows(schemaUsage) {
		return nil
	}
	switch res.Outcome {
	case "succeeded", "failed", "canceled":
	default:
		return nil
	}
	u := *res.Usage
	clampUsage(&u)
	return store.SaveStepUsage(c.tx, c.runID, store.UsageRow{Step: l.stepID, Attempt: l.attempt, Measured: u.Measured,
		Method: u.Method, Note: u.Note, MemoryPeak: u.MemoryPeak, CPUPeakMilli: u.CPUPeakMilli, CPUMillis: u.CPUMillis,
		ReservedMemory: u.ReservedMemory, ReservedCPUMilli: u.ReservedCPUMilli, Runner: c.st.Steps[l.stepID].Runner, At: c.now})
}

// stepUsages returns, per step, the usage of its latest attempt that has one.
func (e *Engine) stepUsages(runID string) (map[string]*StepUsage, error) {
	out := map[string]*StepUsage{}
	if !e.knows(schemaUsage) {
		return out, nil
	}
	rows, err := store.StepUsages(e.db, runID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Step] = &StepUsage{Measured: r.Measured, Method: r.Method, Note: r.Note, MemoryPeak: r.MemoryPeak, CPUPeakMilli: r.CPUPeakMilli,
			CPUMillis: r.CPUMillis, ReservedMemory: r.ReservedMemory, ReservedCPUMilli: r.ReservedCPUMilli, Attempt: r.Attempt} // later attempts replace earlier ones
	}
	return out, nil
}

// Sentence says what the step reserved and used, in a line: "reserved 4Gi
// memory, used 1.1Gi memory at most and 1.9 CPU at most". A step that was not
// measured says so, with the reason.
func (u *StepUsage) Sentence() string {
	if u == nil {
		return ""
	}
	if !u.Measured {
		line := "use not measured"
		if u.Note != "" {
			line += ": " + u.Note
		}
		return line
	}
	var parts []string
	if u.ReservedMemory > 0 {
		parts = append(parts, "reserved "+SizeText(u.ReservedMemory)+" memory")
	}
	parts = append(parts, fmt.Sprintf("used %s memory at most and %.2g CPU at most", SizeText(u.MemoryPeak), float64(u.CPUPeakMilli)/1000))
	return strings.Join(parts, ", ")
}

// SizeText writes a size in the units quantities use: 1.1Gi, 256Mi, 12Ki.
func SizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGi", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%dMi", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%dKi", n>>10)
	}
	return fmt.Sprintf("%d", n)
}
