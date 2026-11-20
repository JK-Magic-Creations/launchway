package verify

import (
	"maps"
	"slices"
)

// maxBaselineClusters caps how many clusters a merged baseline keeps per
// log query: more than one run's MaxClusters, so a cluster that is rare
// in each run is not forgotten at once, but bounded.
const maxBaselineClusters = 2 * MaxClusters

// BaselineRecord is what one passed verification leaves for later baselines:
// BaselineData's result, with the verification it came from.
type BaselineRecord struct {
	Run     BaselineRun            `json:"run"`
	Metrics map[string][]float64   `json:"metrics,omitempty"`
	Logs    map[string]BaselineLog `json:"logs,omitempty"`
}

// BaselineData is what a passed verification contributes to later
// baselines. Per metric: the new version's mean in each interval that had
// data (at least MinPoints usable readings and no error), in interval
// order; one value per interval, not the raw points, because Evaluate
// compares interval means with them. Per log query: the new version's
// clusters merged by template (at most MaxClusters, most lines first,
// samples dropped), with the lines and minutes they were read over.
// Intervals are taken as Evaluate takes them.
func BaselineData(spec Spec, intervals []Interval) (metrics map[string][]float64, logs map[string]BaselineLog) {
	judged := judgedIntervals(intervals, windowOf(spec))
	metrics = map[string][]float64{}
	for _, m := range spec.Metrics {
		minPoints := max(m.MinPoints, 1)
		var vals []float64
		for _, iv := range judged {
			md := findMetric(iv.Metrics, m.Name)
			if md == nil || md.Error != "" {
				continue
			}
			if pts := finitePoints(md.Canary); len(pts) >= minPoints {
				vals = append(vals, meanOf(pts))
			}
		}
		if len(vals) > 0 {
			metrics[m.Name] = vals
		}
	}
	logs = map[string]BaselineLog{}
	for _, l := range spec.Logs {
		var set clusterSet
		lines, withData := 0, 0
		for _, iv := range judged {
			ld := findLog(iv.Logs, l.Name)
			if ld == nil || ld.Error != "" {
				continue
			}
			withData++
			lines += max(ld.Lines, 0)
			for _, c := range ld.Clusters {
				set.add(c, false)
			}
		}
		if withData == 0 {
			continue
		}
		cs := set.sorted()
		if len(cs) > MaxClusters {
			cs = cs[:MaxClusters]
		}
		logs[l.Name] = BaselineLog{Clusters: cs, Lines: max(lines, set.total()),
			Minutes: float64(withData) * float64(max(spec.IntervalMS, 0)) / 60000}
	}
	return metrics, logs
}

// MergeBaselines makes one Baseline from earlier recordings, newest first
// (as many as the caller chose to keep). Metric values are concatenated
// in time order, oldest first, keeping only the newest 500 values per
// metric. Log clusters are merged by exact template with their counts
// summed (at most 400 per log query, most lines first), and so are the
// lines and minutes. Runs lists the recordings in the order given.
func MergeBaselines(recordings []BaselineRecord) Baseline {
	var b Baseline
	for _, r := range recordings {
		b.Runs = append(b.Runs, r.Run)
	}
	// Metrics: walk newest to oldest until each metric is full, then
	// put the chunks back in time order.
	chunks := map[string][][]float64{}
	have := map[string]int{}
	var names []string
	for _, r := range recordings {
		for _, name := range slices.Sorted(maps.Keys(r.Metrics)) {
			vals := r.Metrics[name]
			room := maxBaselineValues - have[name]
			if room <= 0 || len(vals) == 0 {
				continue
			}
			if len(vals) > room {
				vals = vals[len(vals)-room:] // the run's latest values
			}
			if _, ok := chunks[name]; !ok {
				names = append(names, name)
			}
			chunks[name] = append(chunks[name], vals)
			have[name] += len(vals)
		}
	}
	if len(names) > 0 {
		b.Metrics = map[string][]float64{}
	}
	for _, name := range names {
		cs := chunks[name]
		all := make([]float64, 0, have[name])
		for i := len(cs) - 1; i >= 0; i-- {
			all = append(all, cs[i]...)
		}
		b.Metrics[name] = all
	}
	// Logs.
	sets := map[string]*clusterSet{}
	totals := map[string]*BaselineLog{}
	for _, r := range recordings {
		for _, name := range slices.Sorted(maps.Keys(r.Logs)) {
			bl := r.Logs[name]
			if sets[name] == nil {
				sets[name], totals[name] = &clusterSet{}, &BaselineLog{}
			}
			for _, c := range bl.Clusters {
				sets[name].add(c, false)
			}
			totals[name].Lines += max(bl.Lines, 0)
			totals[name].Minutes += max(bl.Minutes, 0)
		}
	}
	if len(sets) > 0 {
		b.Logs = map[string]BaselineLog{}
	}
	for name, set := range sets {
		cs := set.sorted()
		if len(cs) > maxBaselineClusters {
			cs = cs[:maxBaselineClusters]
		}
		t := totals[name]
		b.Logs[name] = BaselineLog{Clusters: cs, Lines: t.Lines, Minutes: t.Minutes}
	}
	return b
}
