package testlib

import (
	"testing"
	"time"
)

func TestSlowIsExactWithoutTheRaceDetector(t *testing.T) {
	want := time.Second
	if RaceEnabled {
		want = RaceFactor * time.Second
	}
	if got := Slow(time.Second); got != want {
		t.Fatalf("Slow(1s) = %v, want %v", got, want)
	}
	if SkipTimingUnderRace() != RaceEnabled {
		t.Fatal("SkipTimingUnderRace and RaceEnabled differ")
	}
}

// Logs the measured factor and fails only if it is outside its bounds; run it with -v on the
// reference machine and set calibrationReference to the logged duration of two goroutines.
func TestMachineFactorReference(t *testing.T) {
	start := time.Now()
	spin(calibrationRounds)
	t.Logf("one goroutine: %v (the reference is for two at once, about the same on a free machine); factor now %.2f (reference %v for two)", time.Since(start), MachineFactor(), calibrationReference)
	if f := MachineFactor(); f < 1 || f > MaxMachineFactor {
		t.Fatalf("factor %v out of bounds", f)
	}
}
