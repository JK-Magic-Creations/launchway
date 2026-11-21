package debrief

import "sort"

// Timing is a test's typical duration, from history.
type Timing struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Suite   string `json:"suite,omitempty"`
	File    string `json:"file,omitempty"`
	AvgMs   int64  `json:"avg_ms"`
	Samples int    `json:"samples"`
	MaxMs   int64  `json:"max_ms,omitempty"`
}

// Shard is one part of a split.
type Shard struct {
	Index   int      `json:"index"` // from 0
	TotalMs int64    `json:"total_ms"`
	Tests   []string `json:"tests"` // the names a test tool is told to run, in order
	Files   []string `json:"files,omitempty"`
}

// Split divides tests among n shards so their total durations are close:
// longest first, each to the shard with the least so far (the greedy
// longest-processing-time rule, within a third of the best possible split).
// It is stable: the same timings and n give the same shards, ties broken by
// the name of the test, and a test with no history is given the average of
// those that have (or one second when none has). By default a test's
// "name" for the tool is its Suite and Name joined by the separator the
// caller chooses in nameOf.
func Split(timings []Timing, n int, nameOf func(Timing) string) []Shard {
	if n < 1 {
		n = 1
	}
	if nameOf == nil {
		nameOf = func(t Timing) string { return t.Name }
	}
	items := append([]Timing(nil), timings...)
	var sum, with int64
	for _, t := range items {
		if t.Samples > 0 {
			sum += t.AvgMs
			with++
		}
	}
	fill := int64(1000)
	if with > 0 {
		fill = max(sum/with, 1)
	}
	for i := range items {
		if items[i].Samples == 0 {
			items[i].AvgMs = fill
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].AvgMs != items[j].AvgMs {
			return items[i].AvgMs > items[j].AvgMs
		}
		return nameOf(items[i]) < nameOf(items[j])
	})
	shards := make([]Shard, n)
	for i := range shards {
		shards[i].Index = i
		shards[i].Tests = []string{}
	}
	files := make([]map[string]bool, n)
	for i := range files {
		files[i] = map[string]bool{}
	}
	for _, t := range items {
		best := 0
		for i := 1; i < n; i++ {
			if shards[i].TotalMs < shards[best].TotalMs {
				best = i
			}
		}
		shards[best].TotalMs += t.AvgMs
		shards[best].Tests = append(shards[best].Tests, nameOf(t))
		if t.File != "" && !files[best][t.File] {
			files[best][t.File] = true
			shards[best].Files = append(shards[best].Files, t.File)
		}
	}
	for i := range shards {
		sort.Strings(shards[i].Tests)
		sort.Strings(shards[i].Files)
	}
	return shards
}
