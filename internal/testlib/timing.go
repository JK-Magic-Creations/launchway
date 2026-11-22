package testlib

import (
	"sync"
	"sync/atomic"
	"time"
)

// RaceFactor is how much longer a test binary built with the race detector may take
// than the same code without it, for the limits that Slow scales. The detector makes
// code 5 to 20 times slower (more on memory-heavy work), and hosted runners are slower
// again; ten times keeps a limit that still catches work that grows without bound.
const RaceFactor = 10

// Slow scales a wall-clock limit for the race detector: unchanged in a normal build
// (the limit is exactly as strict as written), RaceFactor times longer under -race.
// Use it for every "must finish within" assertion; a limit on the least a thing must
// take (a wait that has to happen) is not scaled.
func Slow(d time.Duration) time.Duration {
	if RaceEnabled {
		return RaceFactor * d
	}
	return d
}

// SkipTimingUnderRace reports whether a scale or speed test must not assert its
// wall-clock limits: it is true under the race detector, where the same query on
// the same data takes 2 to 40 times longer depending on the work, so no single factor
// is honest. The test still runs its code and its correctness assertions and logs the
// times; its limits are asserted in the normal build, which is the one that proves them.
func SkipTimingUnderRace() bool { return RaceEnabled }

// MachineFactor says how much slower than the reference machine this one is right now, as a
// factor of at least 1 (1 means as fast or faster: the limit stays as written). It times a fixed
// piece of CPU work on two goroutines at once, so a machine with fewer cores than it reports, a
// CPU quota, or a busy neighbour shows as a factor above 1. It is measured once per test binary
// (the best of three runs, so a single hiccup does not count) and capped at MaxMachineFactor.
// Scale and speed tests use it through Scaled; the limits they assert stay what they are on a
// machine at least as fast as the reference.
func MachineFactor() float64 {
	machineOnce.Do(func() {
		best := time.Duration(1<<63 - 1)
		for i := 0; i < 3; i++ {
			start := time.Now()
			var wg sync.WaitGroup
			for g := 0; g < 2; g++ {
				wg.Add(1)
				go func() { defer wg.Done(); spin(calibrationRounds) }()
			}
			wg.Wait()
			if d := time.Since(start); d < best {
				best = d
			}
		}
		machineFactor = float64(best) / float64(calibrationReference)
		if machineFactor < 1 {
			machineFactor = 1
		}
		if machineFactor > MaxMachineFactor {
			machineFactor = MaxMachineFactor
		}
	})
	return machineFactor
}

// MaxMachineFactor bounds MachineFactor: a limit is never relaxed by more than this, so work that
// grows without bound still fails.
const MaxMachineFactor = 8

// Scaled is Slow for a scale or speed test: the limit, times RaceFactor under the race detector,
// times MachineFactor on a machine slower than the reference.
func Scaled(d time.Duration) time.Duration {
	return time.Duration(float64(Slow(d)) * MachineFactor())
}

const (
	calibrationRounds = 30_000_000
	// calibrationReference is how long spin(calibrationRounds) takes on two goroutines on the
	// reference machine (a 2020s laptop core, not capped, not loaded). Measure it with
	// TestMachineFactorReference when the helper changes.
	calibrationReference = 55 * time.Millisecond
)

var (
	machineOnce   sync.Once
	machineFactor float64
)

var spinSink uint64

// spin is integer work that the compiler cannot remove.
func spin(n int) {
	x := uint64(88172645463325252)
	for i := 0; i < n; i++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
	}
	atomic.AddUint64(&spinSink, x)
}
