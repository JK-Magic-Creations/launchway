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
	// Sizing says where the step's numbers came from: declared, learned,
	// default or raised (see sizing.go), SizingRuns how many measured runs a
	// learned figure rests on, SizingText the same in words. Killed says the
	// attempt was killed for memory.
	Sizing     string `json:"sizing,omitempty"`
	SizingRuns int    `json:"sizing_runs,omitempty"`
	SizingText string `json:"sizing_text,omitempty"`
	Killed     bool   `json:"killed_for_memory,omitempty"`
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
	if !c.e.knows(schemaUsage) {
		return nil
	}
	if res.Usage == nil && memoryKilled(res) {
		res.Usage = &StepUsage{Note: "killed for memory; what the step used was not measured"}
	}
	if res.Usage == nil {
		return nil
	}
	switch res.Outcome {
	case "succeeded", "failed", "canceled":
	default:
		return nil
	}
	u := *res.Usage
	clampUsage(&u)
	u.Killed = memoryKilled(res)
	return store.SaveStepUsage(c.tx, c.runID, store.UsageRow{Step: l.stepID, Attempt: l.attempt, Measured: u.Measured, Killed: u.Killed,
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
		out[r.Step] = usageView(r) // later attempts replace earlier ones
	}
	return out, nil
}

func usageView(r store.UsageRow) *StepUsage {
	u := &StepUsage{Measured: r.Measured, Method: r.Method, Note: r.Note, MemoryPeak: r.MemoryPeak, CPUPeakMilli: r.CPUPeakMilli,
		CPUMillis: r.CPUMillis, ReservedMemory: r.ReservedMemory, ReservedCPUMilli: r.ReservedCPUMilli, Attempt: r.Attempt,
		Sizing: r.Sizing, SizingRuns: r.SizingRuns, Killed: r.Killed}
	u.SizingText = Sizing{Source: r.Sizing, Runs: r.SizingRuns}.Words()
	return u
}

// keepSizing records, as a step attempt is handed out, where its numbers came
// from, so that the run page can say it while the step runs and after.
func (c *cmd) keepSizing(step string, attempt int, sz *Sizing) {
	if sz == nil || !c.e.knows(schemaUsage) {
		return
	}
	if err := store.SaveStepSizing(c.tx, c.st.ID, step, attempt, sz.Source, sz.Runs, c.now); err != nil {
		c.err = err
	}
}

// Sentence says what the step reserved and used, in a line: "reserved 4Gi
// memory, used 1.1Gi memory at most and 1.9 CPU at most". A step that was not
// measured says so, with the reason.
func (u *StepUsage) Sentence() string {
	if u == nil {
		return ""
	}
	var parts []string
	switch {
	case u.Measured:
		if u.ReservedMemory > 0 {
			parts = append(parts, "reserved "+SizeText(u.ReservedMemory)+" memory")
		}
		parts = append(parts, fmt.Sprintf("used %s memory at most and %.2g CPU at most", SizeText(u.MemoryPeak), float64(u.CPUPeakMilli)/1000))
	case u.Note != "":
		parts = append(parts, "use not measured: "+u.Note)
	case u.Attempt > 0 && u.Sizing == "":
		parts = append(parts, "use not measured")
	}
	if u.Killed {
		parts = append(parts, "killed for memory")
	}
	if u.SizingText != "" {
		parts = append(parts, "numbers: "+u.SizingText)
	}
	return strings.Join(parts, "; ")
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

// StepUsageRecord is what one step of a run used and was given, for each of
// its attempts, oldest first.
type StepUsageRecord struct {
	Step     string      `json:"step"`
	Attempts []StepUsage `json:"attempts"`
}

// RunUsage returns what each step of a run used and where its memory and CPU
// came from, attempt by attempt; step, if not empty, limits it to one step.
// A run that was never measured has no records.
func (e *Engine) RunUsage(runID, step string) ([]StepUsageRecord, error) {
	out := []StepUsageRecord{}
	if _, err := e.State(runID); err != nil {
		return nil, err
	}
	if !e.knows(schemaUsage) {
		return out, nil
	}
	rows, err := store.StepUsages(e.db, runID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if step != "" && r.Step != step {
			continue
		}
		if n := len(out); n == 0 || out[n-1].Step != r.Step {
			out = append(out, StepUsageRecord{Step: r.Step, Attempts: []StepUsage{}})
		}
		last := &out[len(out)-1]
		last.Attempts = append(last.Attempts, *usageView(r))
	}
	return out, nil
}
