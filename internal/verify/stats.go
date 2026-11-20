package verify

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The floors that keep a deviation finite when a reference barely varies.
// A reference standard deviation is never taken as smaller than relFloor
// times the reference mean (a series that is flat at 200 ms is treated as
// varying by at least 10 ms), nor smaller than absFloor (a series that is
// flat at zero).
const (
	relFloor = 0.05
	absFloor = 1e-9
	// maxZ caps a deviation so that a reference flat at zero still gives a
	// number JSON can carry.
	maxZ = 1e9
)

// Baseline sizes: a metric's baseline needs minBaselineValues earlier
// readings (one per interval with data) before it is compared with, and
// MergeBaselines keeps at most maxBaselineValues of them, the newest.
const (
	minBaselineValues = 10
	maxBaselineValues = 500
)

// Thresholds returns, for a sensitivity, how many standard deviations a
// reading must be from its reference to count (k) and how large the
// difference must be relative to the reference mean (minEffect, a
// fraction) so that a reference that barely varies does not fail on noise.
// Both must be exceeded for an interval to fail a stable or baseline
// comparison.
//
//	high:   k 2.0, minEffect 5%
//	medium: k 3.0, minEffect 10%   (also used for an empty or unknown sensitivity)
//	low:    k 4.5, minEffect 25%
//
// This is the place to tune how strict verification is.
func Thresholds(sensitivity string) (k, minEffect float64) {
	switch sensitivityOf(sensitivity) {
	case High:
		return 2.0, 0.05
	case Low:
		return 4.5, 0.25
	default:
		return 3.0, 0.10
	}
}

// MatchThreshold returns how similar a new log template must be to a
// reference template to count as the same cluster: the share of token
// positions that are equal or a variable (<*>) in either. Templates of a
// different number of tokens never match.
//
//	high 0.9, medium 0.75, low 0.6
func MatchThreshold(sensitivity string) float64 {
	switch sensitivityOf(sensitivity) {
	case High:
		return 0.9
	case Low:
		return 0.6
	default:
		return 0.75
	}
}

// RateFactor returns how many times more often than in the reference a
// known log cluster must appear now (and at least 5 times) to be classed
// as unexpected_frequency.
//
//	high 2, medium 3, low 5
func RateFactor(sensitivity string) float64 {
	switch sensitivityOf(sensitivity) {
	case High:
		return 2
	case Low:
		return 5
	default:
		return 3
	}
}

// sensitivityOf maps an empty or unknown sensitivity to medium.
func sensitivityOf(s string) string {
	switch s {
	case High, Low:
		return s
	}
	return Medium
}

// directionOf maps an empty or unknown direction to higher-is-worse.
func directionOf(s string) string {
	switch s {
	case LowerIsWorse, Either:
		return s
	}
	return HigherIsWorse
}

// acc accumulates readings for Stats with Welford's method, which stays
// accurate for large values and never overflows on a sum. Readings are
// always added in the same order, so the result is the same everywhere.
type acc struct {
	n             int
	mean, m2      float64
	lowest, upper float64
}

func (a *acc) add(x float64) {
	a.n++
	if a.n == 1 {
		a.lowest, a.upper = x, x
	}
	a.lowest = min(a.lowest, x)
	a.upper = max(a.upper, x)
	d := x - a.mean
	a.mean += d / float64(a.n)
	a.m2 += d * (x - a.mean)
}

// sd is the sample standard deviation (n-1); zero for fewer than two
// readings.
func (a *acc) sd() float64 {
	if a.n < 2 || a.m2 <= 0 {
		return 0
	}
	return math.Sqrt(a.m2 / float64(a.n-1))
}

func (a *acc) stats() Stats {
	if a.n == 0 {
		return Stats{}
	}
	return Stats{N: a.n, Mean: a.mean, SD: a.sd(), Min: a.lowest, Max: a.upper}
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// finitePoints returns the values of the points that are numbers.
func finitePoints(ps []Point) []float64 {
	out := make([]float64, 0, len(ps))
	for _, p := range ps {
		if finite(p.V) {
			out = append(out, p.V)
		}
	}
	return out
}

func meanOf(xs []float64) float64 {
	var a acc
	for _, x := range xs {
		a.add(x)
	}
	return a.mean
}

// deviation compares a reading with a reference mean and standard
// deviation. It returns how many (floored) standard deviations the reading
// is from the mean, and whether the difference is large enough to matter
// at all: not zero, and at least minEffect of the reference mean.
func deviation(value, refMean, refSD, minEffect float64) (z float64, large bool) {
	sd := max(refSD, absFloor, relFloor*math.Abs(refMean))
	diff := value - refMean
	z = diff / sd
	if !finite(z) || z > maxZ {
		z = math.Copysign(maxZ, diff)
	}
	if z < -maxZ {
		z = -maxZ
	}
	effect := math.Abs(diff) / max(math.Abs(refMean), absFloor)
	return z, diff != 0 && effect >= minEffect
}

// bad says whether a deviation z goes the wrong way by more than k.
func bad(direction string, z, k float64) bool {
	switch direction {
	case LowerIsWorse:
		return z < -k
	case Either:
		return math.Abs(z) > k
	}
	return z > k
}

// badness is how far z goes the wrong way (negative when it goes the good
// way), for reporting the worst interval.
func badness(direction string, z float64) float64 {
	switch direction {
	case LowerIsWorse:
		return -z
	case Either:
		return math.Abs(z)
	}
	return z
}

// fnum formats a number for a reason: three significant digits, whole
// numbers from a thousand up.
func fnum(x float64) string {
	ax := math.Abs(x)
	switch {
	case x == 0:
		return "0"
	case ax >= 1000 && ax < 1e15:
		return strconv.FormatFloat(math.Round(x), 'f', -1, 64)
	}
	return strconv.FormatFloat(x, 'g', 3, 64)
}

// zWords says how far and which way a deviation goes:
// "6.1 standard deviations above".
func zWords(z float64) string {
	way := "above"
	if z < 0 {
		way = "below"
	}
	az := math.Abs(z)
	if az >= 1000 {
		return "more than 1000 standard deviations " + way + ", as the reference barely varies"
	}
	return strconv.FormatFloat(az, 'f', 1, 64) + " standard deviations " + way
}

// intervalWords names 0-based interval indexes the way people count them:
// "interval 3", "intervals 3 and 4", "intervals 3, 4 and 5".
func intervalWords(idx []int) string {
	if len(idx) == 0 {
		return ""
	}
	names := make([]string, len(idx))
	for i, x := range idx {
		names[i] = strconv.Itoa(x + 1)
	}
	if len(names) == 1 {
		return "interval " + names[0]
	}
	return "intervals " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// clip cuts s to at most n bytes without splitting a UTF-8 character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// clipWords cuts s for a reason, marking the cut.
func clipWords(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return clip(s, n) + "…"
}

// Summarize returns the count, mean, sample standard deviation, minimum
// and maximum of the values that are numbers (NaN and infinities are
// left out); zero Stats for none.
func Summarize(vals []float64) Stats {
	var a acc
	for _, x := range vals {
		if finite(x) {
			a.add(x)
		}
	}
	return a.stats()
}
