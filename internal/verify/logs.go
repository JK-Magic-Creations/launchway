package verify

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// maxClusterVerdicts caps how many clusters one log verdict lists.
const maxClusterVerdicts = 100

// maxRateRatio caps a rate ratio so it stays a plain number.
const maxRateRatio = 1e6

// clusterSet merges clusters by exact template, keeping first-seen order
// and up to MaxSamples distinct samples per template.
type clusterSet struct {
	index map[string]int
	list  []Cluster
}

func (s *clusterSet) add(c Cluster, samples bool) {
	if s.index == nil {
		s.index = map[string]int{}
	}
	i, ok := s.index[c.Template]
	if !ok {
		i = len(s.list)
		s.index[c.Template] = i
		s.list = append(s.list, Cluster{Template: c.Template})
	}
	dst := &s.list[i]
	dst.Count += max(c.Count, 0)
	if !samples {
		return
	}
	for _, smp := range c.Samples {
		if len(dst.Samples) >= MaxSamples {
			break
		}
		if !contains(dst.Samples, smp) {
			dst.Samples = append(dst.Samples, clip(smp, MaxSampleBytes))
		}
	}
}

func (s *clusterSet) total() int {
	t := 0
	for _, c := range s.list {
		t += c.Count
	}
	return t
}

// sorted returns the clusters by count, most first, then by template.
func (s *clusterSet) sorted() []Cluster {
	out := append([]Cluster(nil), s.list...)
	sortClusters(out)
	return out
}

func sortClusters(cs []Cluster) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Count != cs[j].Count {
			return cs[i].Count > cs[j].Count
		}
		return cs[i].Template < cs[j].Template
	})
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func findLog(ls []LogData, name string) *LogData {
	for i := range ls {
		if ls[i].Name == name {
			return &ls[i]
		}
	}
	return nil
}

// refCluster is a reference template split into tokens for matching.
type refCluster struct {
	template string
	tokens   []string
	count    int
}

// reference is what new clusters are compared with.
type reference struct {
	name    string // "stable version" or "baseline"
	byLen   map[int][]refCluster
	lines   int
	minutes float64 // zero for the stable version: rates are per line
}

func newReference(name string, clusters []Cluster, lines int, minutes float64) *reference {
	var set clusterSet
	for _, c := range clusters {
		set.add(c, false)
	}
	r := &reference{name: name, byLen: map[int][]refCluster{}, lines: max(lines, set.total()), minutes: minutes}
	list := set.list
	sort.Slice(list, func(i, j int) bool { return list[i].Template < list[j].Template })
	for _, c := range list {
		toks := strings.Fields(c.Template)
		r.byLen[len(toks)] = append(r.byLen[len(toks)], refCluster{template: c.Template, tokens: toks, count: c.Count})
	}
	return r
}

// match finds the reference template most similar to template, at least
// threshold similar; ties go to the lexicographically first template.
func (r *reference) match(template string, threshold float64) (refCluster, bool) {
	toks := strings.Fields(template)
	var best refCluster
	bestSim := -1.0
	for _, rc := range r.byLen[len(toks)] { // sorted by template
		if s := similarity(toks, rc.tokens); s >= threshold && s > bestSim {
			best, bestSim = rc, s
		}
	}
	return best, bestSim >= 0
}

// similarity is the share of positions where two templates of the same
// length agree: equal tokens, or a variable in either.
func similarity(a, b []string) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	same := 0
	for i := range a {
		if a[i] == b[i] || a[i] == wildcard || b[i] == wildcard {
			same++
		}
	}
	return float64(same) / float64(len(a))
}

// judgeLog judges one log query over the intervals so far.
func judgeLog(spec Spec, l LogSpec, base Baseline, judged []Interval, allIn bool) LogVerdict {
	sens := sensitivityOf(spec.Sensitivity)
	lv := LogVerdict{Name: l.Name}
	var canary, stable clusterSet
	lines, stableLines, withData := 0, 0, 0
	lastErr := ""
	for _, iv := range judged {
		ld := findLog(iv.Logs, l.Name)
		switch {
		case ld == nil:
			lastErr = "the report has no lines for this log query"
			continue
		case ld.Error != "":
			lastErr = clip(ld.Error, MaxErrorBytes)
			continue
		}
		withData++
		lines += max(ld.Lines, 0)
		for _, c := range ld.Clusters {
			canary.add(c, true)
		}
		if l.Stable {
			stableLines += max(ld.StableN, 0)
			for _, c := range ld.Stable {
				stable.add(c, false)
			}
		}
	}
	lines = max(lines, canary.total())
	stableLines = max(stableLines, stable.total())
	lv.Lines = lines

	if withData == 0 {
		if allIn {
			lv.Outcome = Inconclusive
			lv.Reason = fmt.Sprintf("%s: could not read the logs in any interval: %s", l.Name, lastErr)
		} else {
			lv.Outcome = Running
			lv.Reason = fmt.Sprintf("%s: no log lines read yet", l.Name)
			if lastErr != "" {
				lv.Reason += "; last error: " + lastErr
			}
		}
		return lv
	}

	var ref *reference
	switch {
	case l.Stable && stableLines > 0:
		ref = newReference("stable version", stable.list, stableLines, 0)
	case l.Baseline:
		if bl, ok := base.Logs[l.Name]; ok && bl.Lines > 0 {
			ref = newReference("baseline", bl.Clusters, bl.Lines, bl.Minutes)
		}
	}
	minutes := float64(withData) * float64(max(spec.IntervalMS, 0)) / 60000
	threshold, factor := MatchThreshold(sens), RateFactor(sens)

	errorLines, newClusters := 0, 0
	for _, c := range canary.sorted() {
		cv := ClusterVerdict{Template: c.Template, Count: c.Count, Samples: c.Samples,
			Error: l.Errors == "all" || isErrorTemplate(c.Template)}
		switch r, ok := matchIn(ref, c.Template, threshold); {
		case ref == nil:
			cv.Class = NoReference
		case !ok:
			cv.Class = Unknown
			newClusters++
		default:
			ratio := ref.ratio(c.Count, lines, minutes, r.count)
			cv.Reference = r.count
			cv.RateRatio = math.Round(ratio*100) / 100
			cv.Class = Known
			if c.Count >= 5 && ratio >= factor {
				cv.Class = UnexpectedFrequency
			}
		}
		if cv.Error {
			errorLines += cv.Count
		}
		cv.Failing = failingCluster(sens, cv, lines)
		lv.Clusters = append(lv.Clusters, cv)
	}
	sortClusterVerdicts(lv.Clusters)

	var failing []ClusterVerdict
	for _, cv := range lv.Clusters {
		if cv.Failing {
			failing = append(failing, cv)
		}
	}
	if len(lv.Clusters) > maxClusterVerdicts {
		lv.Clusters = lv.Clusters[:maxClusterVerdicts]
	}
	lv.Intervals = logIntervals(l, judged, failing)

	switch {
	case len(failing) > 0:
		lv.Outcome = Failed
		lv.Reason = failingReason(l.Name, sens, failing, ref)
	case !allIn:
		lv.Outcome = Running
		lv.Reason = fmt.Sprintf("%s: %s so far in %s; nothing failing yet", l.Name, plural(lines, "line", "lines"), plural(len(lv.Clusters), "cluster", "clusters"))
	case ref == nil && errorLines > 0:
		lv.Outcome = Inconclusive
		lv.Reason = fmt.Sprintf("%s: no baseline or stable logs to compare with; %s", l.Name, plural(errorLines, "error line", "error lines"))
	case ref == nil:
		lv.Outcome = Passed
		lv.Reason = fmt.Sprintf("%s: %s in %s and no errors among them (no baseline or stable logs to compare with)", l.Name, plural(lines, "line", "lines"), plural(len(canary.list), "cluster", "clusters"))
	default:
		lv.Outcome = Passed
		lv.Reason = fmt.Sprintf("%s: %s in %s, %d new; no new or unusually frequent errors compared with the %s", l.Name,
			plural(lines, "line", "lines"), plural(len(canary.list), "cluster", "clusters"), newClusters, ref.name)
	}
	return lv
}

// logIntervals checks each judged interval of a log query: no data when
// it could not be read, failed when it had lines of a failing cluster.
func logIntervals(l LogSpec, judged []Interval, failing []ClusterVerdict) []IntervalCheck {
	bad := map[string]bool{}
	for _, cv := range failing {
		bad[cv.Template] = true
	}
	var out []IntervalCheck
	for _, iv := range judged {
		ic := IntervalCheck{Index: iv.Index, Outcome: Passed}
		ld := findLog(iv.Logs, l.Name)
		switch {
		case ld == nil:
			ic.Outcome, ic.Reason = noData, "the report has no lines for this log query"
		case ld.Error != "":
			ic.Outcome, ic.Reason = noData, clip(ld.Error, MaxErrorBytes)
		default:
			n := 0
			for _, c := range ld.Clusters {
				n += max(c.Count, 0)
				if bad[c.Template] {
					ic.Outcome = Failed
				}
			}
			ic.Value = float64(max(ld.Lines, n))
			if ic.Outcome == Failed {
				ic.Reason = "lines of a failing cluster"
			}
		}
		out = append(out, ic)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

func matchIn(ref *reference, template string, threshold float64) (refCluster, bool) {
	if ref == nil {
		return refCluster{}, false
	}
	return ref.match(template, threshold)
}

// ratio is how many times more often a cluster appears now than in the
// reference: per minute against a baseline when both durations are known,
// otherwise per line read. A reference count of zero counts as one.
func (r *reference) ratio(count, lines int, minutes float64, refCount int) float64 {
	rc := float64(max(refCount, 1))
	var ratio float64
	if r.minutes > 0 && minutes > 0 {
		ratio = (float64(count) / minutes) / (rc / r.minutes)
	} else {
		ratio = (float64(count) / float64(max(lines, 1))) / (rc / float64(max(r.lines, 1)))
	}
	if !finite(ratio) || ratio > maxRateRatio {
		return maxRateRatio
	}
	return ratio
}

// failingCluster says whether a classified cluster fails its log query:
//
//	high:   any new (unknown) error cluster, or any error cluster seen
//	        unusually often
//	medium: a new error cluster of at least 3 lines, or any error
//	        cluster seen unusually often
//	low:    a new error cluster of at least 10 lines, or of at least 3
//	        lines that are at least 1% of the lines read
func failingCluster(sens string, cv ClusterVerdict, lines int) bool {
	if !cv.Error {
		return false
	}
	switch cv.Class {
	case Unknown:
		switch sens {
		case High:
			return true
		case Low:
			return cv.Count >= 10 || (cv.Count >= 3 && cv.Count*100 >= lines)
		}
		return cv.Count >= 3
	case UnexpectedFrequency:
		return sens != Low
	}
	return false
}

// classOrder ranks classes for listing: new first, known last.
var classOrder = map[string]int{Unknown: 0, UnexpectedFrequency: 1, NoReference: 2, Known: 3}

func sortClusterVerdicts(cs []ClusterVerdict) {
	sort.Slice(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Failing != b.Failing {
			return a.Failing
		}
		if classOrder[a.Class] != classOrder[b.Class] {
			return classOrder[a.Class] < classOrder[b.Class]
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Template < b.Template
	})
}

func failingReason(name, sens string, failing []ClusterVerdict, ref *reference) string {
	c := failing[0]
	tmpl := clipWords(c.Template, 120)
	var why string
	if c.Class == Unknown {
		why = fmt.Sprintf("%s: a new error, %q (%s), was not seen in the %s", name, tmpl, plural(c.Count, "line", "lines"), ref.name)
	} else {
		unit := "per minute"
		if ref.minutes == 0 {
			unit = "per line read"
		}
		why = fmt.Sprintf("%s: the error %q appeared %s, %.1f times as often %s as in the %s (%s sensitivity fails at %g times)",
			name, tmpl, plural(c.Count, "time", "times"), c.RateRatio, unit, ref.name, sens, RateFactor(sens))
	}
	if len(failing) > 1 {
		why += fmt.Sprintf(", and %s also failing", plural(len(failing)-1, "other cluster is", "other clusters are"))
	}
	return why
}

// errorWords are the words that make a template an error under
// errors: auto.
var errorWords = map[string]bool{
	"error": true, "err": true, "exception": true, "fatal": true, "panic": true,
	"fail": true, "failed": true, "failure": true, "traceback": true, "critical": true,
	"severe": true, "crash": true, "refused": true, "timeout": true,
}

// isErrorTemplate says whether a template reads as an error: it has one
// of errorWords, or "timed out", as a whole word, ignoring case. Words are
// split at anything that is not a letter and inside camel case, so
// "java.lang.NullPointerException" and "IOError" count.
func isErrorTemplate(t string) bool {
	words := templateWords(t)
	for i, w := range words {
		if errorWords[w] || (w == "timed" && i+1 < len(words) && words[i+1] == "out") {
			return true
		}
	}
	return false
}

// templateWords splits text into lower-case words at non-letters and at
// camel-case boundaries.
func templateWords(t string) []string {
	var words []string
	for _, f := range strings.FieldsFunc(t, func(r rune) bool { return !unicode.IsLetter(r) }) {
		rs := []rune(f)
		start := 0
		for i := 1; i < len(rs); i++ {
			lowerToUpper := unicode.IsLower(rs[i-1]) && unicode.IsUpper(rs[i])
			acronymEnd := unicode.IsUpper(rs[i-1]) && unicode.IsUpper(rs[i]) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if lowerToUpper || acronymEnd {
				words = append(words, strings.ToLower(string(rs[start:i])))
				start = i
			}
		}
		words = append(words, strings.ToLower(string(rs[start:])))
	}
	return words
}
