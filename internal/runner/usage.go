package runner

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
)

// usageOf is what an executor measured, as the server records it. An
// executor that measured nothing says so; nothing is ever filled in.
func usageOf(u *executor.Usage) *engine.StepUsage {
	if u == nil {
		return nil
	}
	return &engine.StepUsage{Measured: u.Measured, Method: u.Method, Note: u.Note,
		MemoryPeak: u.MemoryPeak, CPUPeakMilli: u.CPUPeakMilli, CPUMillis: u.CPUMillis,
		ReservedMemory: u.ReservedMemory, ReservedCPUMilli: u.ReservedCPUMilli}
}

// usageLine is the log line that says what the step used; "" if it was not
// measured.
func usageLine(u *executor.Usage) string {
	if u == nil || !u.Measured {
		return ""
	}
	line := fmt.Sprintf("Used %s memory at most and %.2g CPU at most (%.1f CPU seconds in all)",
		engine.SizeText(u.MemoryPeak), float64(u.CPUPeakMilli)/1000, float64(u.CPUMillis)/1000)
	if u.ReservedMemory > 0 {
		line += fmt.Sprintf("; reserved %s memory", engine.SizeText(u.ReservedMemory))
	}
	return line
}
