package runner

import (
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/metrics"
)

func TestVitalsAreExposedAsMetrics(t *testing.T) {
	v := newVitals(time.Minute)
	l := v.loop("lease")
	l.last.Store(time.Now().Add(-90 * time.Second).UnixNano())
	end := v.begin("step x", time.Time{})
	v.abandoned.Add(2)
	reg := metrics.NewRegistry()
	before := metrics.RunnerActionsAbandoned.Value()
	v.publish(reg)
	if err := reg.Write(discard{}); err != nil {
		t.Fatal(err)
	}
	if age := metrics.RunnerPollAge.Value("lease"); age < 89 || age > 120 {
		t.Fatalf("poll age %v", age)
	}
	if n := metrics.RunnerActionsRunning.Value(); n != 1 {
		t.Fatalf("running %v", n)
	}
	if d := metrics.RunnerActionsAbandoned.Value() - before; d != 2 {
		t.Fatalf("abandoned %v", d)
	}
	end()
	_ = reg.Write(discard{})
	if n := metrics.RunnerActionsRunning.Value(); n != 0 {
		t.Fatalf("running %v", n)
	}
	_ = reg.Write(discard{})
	if d := metrics.RunnerActionsAbandoned.Value() - before; d != 2 {
		t.Fatalf("abandoned counted twice: %v", d)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
