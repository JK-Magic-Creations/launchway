package runner

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/executor"
)

func TestUsageLineAndRecord(t *testing.T) {
	u := &executor.Usage{Measured: true, Method: "cgroup2", MemoryPeak: 1181116006, CPUPeakMilli: 1900, CPUMillis: 42000, ReservedMemory: 4 << 30}
	line := usageLine(u)
	for _, want := range []string{"Used 1.1Gi memory at most", "1.9 CPU at most", "42.0 CPU seconds", "reserved 4.0Gi memory"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q: missing %q", line, want)
		}
	}
	if r := usageOf(u); !r.Measured || r.MemoryPeak != u.MemoryPeak || r.ReservedMemory != 4<<30 {
		t.Fatalf("%+v", r)
	}
	// Not measured: no line, and the record says so with the reason.
	n := &executor.Usage{Note: "no cgroup"}
	if usageLine(n) != "" || usageOf(n).Measured || usageOf(n).Note != "no cgroup" {
		t.Fatal("an unmeasured step must say so and carry no numbers")
	}
	if usageOf(nil) != nil {
		t.Fatal("nil")
	}
}
