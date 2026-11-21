package release

import (
	"sort"
	"time"
)

// Launch is one delivery to an Airfield, as the measures read it.
type Launch struct {
	Project  string
	Airfield string
	Version  string
	// Merged is when the change reached the main branch (the release commit's
	// time); zero if unknown.
	Merged time.Time
	// At is when the delivery reached the Airfield (it became the thing running).
	At time.Time
	// Failed: the delivery did not succeed, or was returned to base afterwards.
	Failed bool
	// Restored is when service was healthy again after a failed delivery (the
	// next good delivery, or the return to base finishing); zero if not yet.
	Restored time.Time
}

// Measures are the four delivery measures for a period.
type Measures struct {
	From, To time.Time
	Launches int `json:"launches"`
	Failures int `json:"failures"`
	// FrequencyPerWeek is how many successful launches a week.
	FrequencyPerWeek float64 `json:"frequency_per_week"`
	// LeadTimeMedianSec is the median time from merge to the Airfield, over launches with a merge time.
	LeadTimeMedianSec float64 `json:"lead_time_median_s"`
	LeadTimeCount     int     `json:"lead_time_count"`
	// ChangeFailureRate is failures divided by launches (0 with none).
	ChangeFailureRate float64 `json:"change_failure_rate"`
	// RestoreMedianSec is the median time from a failed launch to restore; Unrestored is how many have not been.
	RestoreMedianSec float64 `json:"time_to_restore_median_s"`
	RestoreCount     int     `json:"restore_count"`
	Unrestored       int     `json:"unrestored"`
}

// Compute works out the measures of the launches whose At lies in [from, to).
func Compute(launches []Launch, from, to time.Time) Measures {
	m := Measures{From: from, To: to}
	var lead, restore []float64
	ok := 0
	for _, l := range launches {
		if l.At.Before(from) || !l.At.Before(to) {
			continue
		}
		m.Launches++
		if l.Failed {
			m.Failures++
			if l.Restored.IsZero() {
				m.Unrestored++
			} else if l.Restored.After(l.At) {
				restore = append(restore, l.Restored.Sub(l.At).Seconds())
			}
		} else {
			ok++
		}
		if !l.Merged.IsZero() && l.At.After(l.Merged) {
			lead = append(lead, l.At.Sub(l.Merged).Seconds())
		}
	}
	if weeks := to.Sub(from).Hours() / (24 * 7); weeks > 0 {
		m.FrequencyPerWeek = float64(ok) / weeks
	}
	if m.Launches > 0 {
		m.ChangeFailureRate = float64(m.Failures) / float64(m.Launches)
	}
	m.LeadTimeMedianSec, m.LeadTimeCount = median(lead), len(lead)
	m.RestoreMedianSec, m.RestoreCount = median(restore), len(restore)
	return m
}

func median(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	sort.Float64s(x)
	if len(x)%2 == 1 {
		return x[len(x)/2]
	}
	return (x[len(x)/2-1] + x[len(x)/2]) / 2
}

// Series is the measures per period (weeks) over a span, oldest first.
func Series(launches []Launch, from, to time.Time, step time.Duration) []Measures {
	var out []Measures
	for a := from; a.Before(to); a = a.Add(step) {
		b := a.Add(step)
		if b.After(to) {
			b = to
		}
		out = append(out, Compute(launches, a, b))
	}
	return out
}
