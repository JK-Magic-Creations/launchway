package deploy

import (
	"math"
	"testing"
)

func TestClampIntNeverWraps(t *testing.T) {
	for in, want := range map[int64]int{0: 0, 7: 7, -3: -3, 1 << 40: math.MaxInt32, -(1 << 40): math.MinInt32, math.MaxInt64: math.MaxInt32} {
		if got := clampInt(in); got != want {
			t.Errorf("clampInt(%d) = %d, want %d", in, got, want)
		}
	}
	// 2^32 + 5 would wrap to 5 on a 32-bit platform with a plain int().
	if clampInt(1<<32+5) == 5 {
		t.Error("wrapped")
	}
}
