package verify

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// Kinds of generated application log lines.
const (
	kRequest = iota
	kLogin
	kCache
	kSlowQuery
	kJob
	kUpstreamTimeout
	kHealth
	kEmailFailed
	kOrder
	kDBConnect
	kNPE     // a new error the canary introduces
	kFeature // a new info line the canary introduces
	numKinds
)

// logLine generates one line of a kind with variable ids, durations and
// addresses.
func logLine(r *rand.Rand, kind int) string {
	ip := fmt.Sprintf("10.%d.%d.%d", r.IntN(256), r.IntN(256), 1+r.IntN(254))
	switch kind {
	case kRequest:
		return fmt.Sprintf("INFO request completed method=%s path=/api/orders/%d status=200 duration=%dms",
			[]string{"GET", "POST"}[r.IntN(2)], 10000+r.IntN(90000), 1+r.IntN(900))
	case kLogin:
		return fmt.Sprintf("INFO user %08x-%04x-%04x-%04x-%012x logged in from %s",
			r.Uint32(), r.IntN(1<<16), r.IntN(1<<16), r.IntN(1<<16), r.Uint64()&(1<<48-1), ip)
	case kCache:
		return fmt.Sprintf("DEBUG cache hit key=session:%016x", r.Uint64()|1<<60)
	case kSlowQuery:
		return fmt.Sprintf("WARN slow query took %dms table=%s", 500+r.IntN(4000), []string{"orders", "users"}[r.IntN(2)])
	case kJob:
		return fmt.Sprintf("INFO scheduled job %s finished in %d.%03ds", []string{"cleanup", "reindex", "report-export"}[r.IntN(3)], r.IntN(60), r.IntN(1000))
	case kUpstreamTimeout:
		return fmt.Sprintf("WARN request to upstream payments timed out after %dms", 3000+r.IntN(2000))
	case kHealth:
		return "INFO health check ok"
	case kEmailFailed:
		return fmt.Sprintf("ERROR failed to send email to user %d: connection refused", 1000+r.IntN(9000))
	case kOrder:
		return fmt.Sprintf("INFO order %d created total=%d.%02d", 100000+r.IntN(900000), r.IntN(500), r.IntN(100))
	case kDBConnect:
		return fmt.Sprintf("INFO connected to database at %s:5432 pool=%d", ip, 5+r.IntN(20))
	case kNPE:
		return fmt.Sprintf(`ERROR java.lang.NullPointerException: Cannot invoke "String.length()" because "name" is null at com.example.OrderService.validate(OrderService.java:%d)`, 100+r.IntN(200))
	case kFeature:
		return fmt.Sprintf("INFO feature flag %s enabled for tenant %d", []string{"new-checkout", "fast-search"}[r.IntN(2)], 1+r.IntN(5000))
	}
	panic("unknown kind")
}

// normalMix is how many lines of each kind a healthy interval has.
var normalMix = map[int]int{kRequest: 40, kLogin: 10, kCache: 15, kSlowQuery: 3, kJob: 2, kUpstreamTimeout: 2, kHealth: 6, kEmailFailed: 1, kOrder: 8, kDBConnect: 1}

// genLines makes an interval's lines from a mix, interleaved in a
// shuffled order.
func genLines(r *rand.Rand, mix map[int]int) []string {
	var lines []string
	for kind := range numKinds {
		for range mix[kind] {
			lines = append(lines, logLine(r, kind))
		}
	}
	r.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	return lines
}

func clustered(lines []string) ([]Cluster, int) {
	c := NewClusterer(Redact)
	for _, l := range lines {
		c.Add(l)
	}
	return c.Clusters(), c.Lines()
}

// logWindow makes n intervals of log query "app"; mix gives each
// interval's canary lines, and with stable set the stable version gets the
// normal mix.
func logWindow(r *rand.Rand, n int, mix func(i int) map[int]int, stable bool) []Interval {
	var out []Interval
	for i := range n {
		ld := LogData{Name: "app"}
		ld.Clusters, ld.Lines = clustered(genLines(r, mix(i)))
		if stable {
			ld.Stable, ld.StableN = clustered(genLines(r, normalMix))
		}
		out = append(out, Interval{Index: i, Start: now + int64(i)*minute, End: now + int64(i+1)*minute, Logs: []LogData{ld}})
	}
	return out
}

func with(extra map[int]int) func(int) map[int]int {
	m := map[int]int{}
	for k, v := range normalMix {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return func(int) map[int]int { return m }
}

type logCase struct {
	spec      Spec
	intervals []Interval
	base      Baseline
}

// logScenario is five intervals of a healthy log against a baseline of
// three earlier healthy runs.
func logScenario(t *testing.T, stable bool) logCase {
	t.Helper()
	r := rand.New(rand.NewPCG(11, 12))
	spec := Spec{Sensitivity: Medium, Intervals: 5, IntervalMS: minute, Logs: []LogSpec{{Name: "app", Baseline: true, Stable: stable}}}
	var recs []BaselineRecord
	for d := 1; d <= 3; d++ {
		m, l := BaselineData(spec, logWindow(r, 5, with(nil), false))
		recs = append(recs, BaselineRecord{Run: BaselineRun{Run: fmt.Sprintf("r%d", d), Number: d}, Metrics: m, Logs: l})
	}
	return logCase{spec: spec, intervals: logWindow(r, 5, with(nil), stable), base: MergeBaselines(recs)}
}

func TestDrainClustersAnApplicationLog(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	var lines []string
	for range 4 {
		lines = append(lines, genLines(r, normalMix)...)
	}
	clusters, n := clustered(lines)
	if n != len(lines) || n != 352 {
		t.Fatalf("lines %d, generated %d", n, len(lines))
	}
	var got []string
	for _, c := range clusters {
		got = append(got, c.Template)
	}
	want := []string{
		"INFO request completed <*> <*> status=<*> duration=<*>",
		"DEBUG cache hit <*>",
		"INFO user *** logged in from <*>", // Redact takes a UUID for a generated token
		"INFO order <*> created total=<*>",
		"INFO health check ok",
		"WARN slow query took <*> <*>",
		"INFO scheduled job <*> finished in <*>",
		"WARN request to upstream payments timed out after <*>",
		"ERROR failed to send email to user <*> connection refused",
		"INFO connected to database at <*> pool=<*>",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("templates:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if clusters[0].Count != 160 || len(clusters[0].Samples) != MaxSamples {
		t.Errorf("first cluster %d lines, %d samples", clusters[0].Count, len(clusters[0].Samples))
	}
}

func TestMaskToken(t *testing.T) {
	cases := map[string]string{
		"12345": wildcard, "-3.5e2": wildcard, "45%": wildcard, "12ms": wildcard, "1m30s": wildcard, "64KB": wildcard,
		"0xdeadbeef": wildcard, "deadbeefcafe": wildcard, "10.0.0.1": wildcard, "10.0.0.1:8080": wildcard, "fe80::1": wildcard,
		"[::1]:443": wildcard, "2026-11-19T14:00:00.123Z": wildcard, "14:00:01,250": wildcard, "v1.4.2": wildcard,
		"550e8400-e29b-41d4-a716-446655440000": wildcard, "id=42": "id=<*>", "took=1.5s,": "took=<*>",
		"pod-7f9c8d6b5-x2x4z": wildcard, "error": "error", "GET": "GET", "HTTP/1.1": "HTTP/1.1", "user=alice": "user=alice",
		"accepted": "accepted",
	}
	for in, want := range cases {
		if got := maskToken(in); got != want {
			t.Errorf("maskToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClustererBoundedMemory(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 32))
	word := func() string {
		b := make([]byte, 3+r.IntN(6))
		for i := range b {
			b[i] = byte('a' + r.IntN(26))
		}
		return string(b)
	}
	c := NewClusterer(nil)
	const n = 100_000
	for range n {
		c.Add(word() + " " + word() + " " + word() + " " + word())
	}
	clusters := c.Clusters()
	if len(clusters) > MaxClusters {
		t.Fatalf("%d clusters, at most %d allowed", len(clusters), MaxClusters)
	}
	total, overflow := 0, 0
	for _, cl := range clusters {
		total += cl.Count
		if cl.Template == overflowTemplate {
			overflow = cl.Count
		}
		if len(cl.Samples) > MaxSamples {
			t.Fatalf("%d samples", len(cl.Samples))
		}
	}
	if total != n || c.Lines() != n || overflow == 0 {
		t.Errorf("counts %d of %d lines, overflow %d", total, n, overflow)
	}
}

func TestClustererLimits(t *testing.T) {
	c := NewClusterer(nil)
	c.Add("   ")
	c.Add(strings.Repeat("é", 3000))           // one token longer than a template may be
	c.Add(strings.Repeat("word ", 1000) + "x") // many tokens
	if c.Lines() != 2 {
		t.Fatalf("lines %d", c.Lines())
	}
	for _, cl := range c.Clusters() {
		if len(cl.Template) > MaxTemplateBytes || !utf8Valid(cl.Template) {
			t.Errorf("template of %d bytes", len(cl.Template))
		}
		for _, s := range cl.Samples {
			if len(s) > MaxSampleBytes || !utf8Valid(s) {
				t.Errorf("sample of %d bytes", len(s))
			}
		}
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "?") == s }

func TestErrorWords(t *testing.T) {
	cases := map[string]bool{
		"ERROR failed to send":                     true,
		"java.lang.NullPointerException: at <*>":   true,
		"raise IOError(<*>)":                       true,
		"upstream timed out after <*>":             true,
		"dial tcp <*> connect: connection refused": true,
		"err=<*>":                                true,
		"INFO request completed with <*> errors": false,
		"INFO timed <*>":                         false,
		"failover complete":                      false,
		"WARN slow query":                        false,
	}
	for tmpl, want := range cases {
		if got := isErrorTemplate(tmpl); got != want {
			t.Errorf("isErrorTemplate(%q) = %v", tmpl, got)
		}
	}
}

func TestLogVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		stable  bool
		noRef   bool
		sens    string
		mix     func(int) map[int]int
		upto    int
		outcome string
		final   bool
		reason  string
		failing string // a piece of the failing cluster's template
		class   string
	}{
		{"healthy against the baseline", false, false, Medium, with(nil), 5, Passed, true, "compared with the baseline", "", ""},
		{"healthy against stable", true, false, Medium, with(nil), 5, Passed, true, "compared with the stable version", "", ""},
		{"new error fails at once", false, false, Medium, with(map[int]int{kNPE: 4}), 1, Failed, true,
			"a new error, \"ERROR java.lang.NullPointerException:", "NullPointerException", Unknown},
		{"new error against stable", true, false, Medium, with(map[int]int{kNPE: 4}), 1, Failed, true, "was not seen in the stable version", "NullPointerException", Unknown},
		{"one new error line is not enough at medium", false, false, Medium, func(i int) map[int]int {
			if i == 0 {
				return with(map[int]int{kNPE: 1})(i)
			}
			return normalMix
		}, 5, Passed, true, "", "", ""},
		{"one new error line fails at high", false, false, High, with(map[int]int{kNPE: 1}), 1, Failed, true, "a new error", "NullPointerException", Unknown},
		{"known timeout ten times as often", false, false, Medium, with(map[int]int{kUpstreamTimeout: 20}), 1, Failed, true,
			"times as often per minute as in the baseline (medium sensitivity fails at 3 times)", "timed out", UnexpectedFrequency},
		{"known timeout ten times as often, low", false, false, Low, with(map[int]int{kUpstreamTimeout: 20}), 5, Passed, true, "", "", ""},
		{"new info line does not fail", false, false, Medium, with(map[int]int{kFeature: 5}), 5, Passed, true, ", 1 new;", "", ""},
		{"no reference with errors", false, true, Medium, with(nil), 5, Inconclusive, true, "no baseline or stable logs to compare with; 15 error lines", "", ""},
		{"no reference without errors", false, true, Medium, with(map[int]int{kUpstreamTimeout: -2, kEmailFailed: -1}), 5, Passed, true, "no errors among them", "", ""},
		{"still running", false, false, Medium, with(nil), 3, Running, false, "nothing failing yet", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lc := logScenario(t, c.stable)
			spec := lc.spec
			spec.Sensitivity = c.sens
			if c.noRef {
				spec.Logs = []LogSpec{{Name: "app"}}
			}
			r := rand.New(rand.NewPCG(41, 42))
			v := Evaluate(spec, lc.base, logWindow(r, c.upto, c.mix, c.stable))
			mustJSON(t, v)
			l := v.Logs[0]
			if l.Outcome != c.outcome || v.Outcome != c.outcome || v.Final != c.final {
				t.Fatalf("got %s (verdict %s, final %v), want %s: %s", l.Outcome, v.Outcome, v.Final, c.outcome, l.Reason)
			}
			if !strings.Contains(l.Reason, c.reason) {
				t.Errorf("reason %q lacks %q", l.Reason, c.reason)
			}
			if c.failing != "" {
				f := l.Clusters[0]
				if !f.Failing || !f.Error || f.Class != c.class || !strings.Contains(f.Template, c.failing) {
					t.Errorf("first cluster %+v", f)
				}
				if v.Summary != sentence(l.Reason) {
					t.Errorf("summary %q", v.Summary)
				}
			}
			if len(l.Intervals) != c.upto {
				t.Errorf("%d interval checks for %d intervals", len(l.Intervals), c.upto)
			}
			for i, ic := range l.Intervals {
				if ic.Index != i || ic.Value <= 0 || (ic.Outcome == Failed) != (c.failing != "") {
					t.Errorf("interval check %d: %+v", i, ic)
				}
			}
			for _, cv := range l.Clusters {
				if cv.Failing && c.outcome != Failed {
					t.Errorf("failing cluster in a %s verdict: %+v", c.outcome, cv)
				}
				if strings.HasPrefix(c.name, "healthy") && cv.Error && cv.Class != Known {
					t.Errorf("error cluster not known in a pass: %+v", cv)
				}
			}
		})
	}
}

func TestClusterVerdictOrder(t *testing.T) {
	cs := []ClusterVerdict{
		{Template: "b", Count: 9, Class: Known},
		{Template: "a", Count: 1, Class: Unknown},
		{Template: "c", Count: 5, Class: UnexpectedFrequency},
		{Template: "d", Count: 2, Class: Unknown, Failing: true},
		{Template: "e", Count: 3, Class: Unknown},
		{Template: "f", Count: 3, Class: NoReference},
	}
	sortClusterVerdicts(cs)
	var got []string
	for _, c := range cs {
		got = append(got, c.Template)
	}
	if strings.Join(got, "") != "deacfb" {
		t.Errorf("order %v", got)
	}
}

func TestSecretsNeverReachTemplatesOrSamples(t *testing.T) {
	// Credentials are built here, never written whole in the source.
	pw := "hunter" + "2-" + strings.Repeat("Zq9", 4)
	tok := "abc" + strings.Repeat("9fK2", 10)
	aws := "AKIA" + strings.Repeat("Q", 16)
	jwt := "eyJ" + strings.Repeat("hbGci", 3) + "." + "eyJ" + strings.Repeat("zdWIi", 4) + "." + strings.Repeat("sig_", 6)
	lines := []string{
		"INFO login user=alice password=" + pw + " ok",
		"DEBUG outgoing request Authorization: Bearer " + tok,
		"WARN retrying with key " + aws,
		`ERROR bad session {"token":"` + jwt + `","user":"bob"}`,
		"INFO connecting to postgres://app:" + pw + "@db:5432/app",
	}
	c := NewClusterer(Redact)
	for range 3 {
		for _, l := range lines {
			c.Add(l)
		}
	}
	for _, cl := range c.Clusters() {
		for _, s := range append([]string{cl.Template}, cl.Samples...) {
			for _, secret := range []string{pw, tok, aws, jwt, "hunter2", "9fK29fK2", "AKIAQQQQ", "eyJhbGci"} {
				if strings.Contains(s, secret) {
					t.Errorf("%q leaks %q", s, secret)
				}
			}
		}
	}
}

func TestRedact(t *testing.T) {
	long := "x" + strings.Repeat("a1B2", 10)
	cases := []struct{ in, want string }{
		{"Authorization: Bearer " + long, "Authorization: Bearer ***"},
		{`"Authorization": "Basic dXNlcjpwYXNz"`, `"Authorization": "Basic ***"`},
		{"sent bearer " + "Ab3" + strings.Repeat("x", 10), "sent bearer ***"},
		{"basic authentication failed", "basic authentication failed"},
		{"password=s3cret&user=bob", "password=***&user=bob"},
		{"db_passwd: open-sesame next", "db_passwd: *** next"},
		{`{"api_key":"k-` + "123" + `","n":1}`, `{"api_key":"***","n":1}`},
		{"apikey='abcd' client_secret=xyz access_key=QWER", "apikey='***' client_secret=*** access_key=***"},
		{"https://deploy:" + "pa55" + "@git.example.com/repo.git", "https://***@git.example.com/repo.git"},
		{"key " + "AKIA" + strings.Repeat("Z", 16) + " used", "key *** used"},
		{"jwt " + "eyJ" + strings.Repeat("a", 10) + "." + strings.Repeat("b", 10) + "." + strings.Repeat("c", 10), "jwt ***"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY----- after", "*** after"},
		{"id " + long, "id ***"},
		{"plain " + strings.Repeat("abcdef", 6), "plain " + strings.Repeat("abcdef", 6)},
		{"nothing secret here status=200", "nothing secret here status=200"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestMaskWith(t *testing.T) {
	secret := "plain" + "word" + "7"
	mask := MaskWith([]string{secret, "abc", "", secret + "-longer"})
	got := mask("using " + secret + "-longer and " + secret + " and abc token=zzz")
	if want := "using *** and *** and abc token=***"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if MaskWith(nil)("x") != "x" {
		t.Error("an empty mask changes text")
	}
}

func TestBaselineDataAndMerge(t *testing.T) {
	spec := Spec{Intervals: 3, IntervalMS: 2 * minute, Metrics: []MetricSpec{{Name: "m", MinPoints: 2}}, Logs: []LogSpec{{Name: "app"}}}
	ivs := []Interval{
		{Index: 2, Metrics: []MetricData{{Name: "m", Canary: pts(5, 7)}}, Logs: []LogData{{Name: "app", Lines: 4, Clusters: []Cluster{{Template: "a", Count: 3, Samples: []string{"a"}}, {Template: "b", Count: 1}}}}},
		{Index: 0, Metrics: []MetricData{{Name: "m", Canary: pts(1, 3)}}, Logs: []LogData{{Name: "app", Lines: 2, Clusters: []Cluster{{Template: "a", Count: 2}}}}},
		{Index: 1, Metrics: []MetricData{{Name: "m", Canary: pts(9)}}, Logs: []LogData{{Name: "app", Error: "down"}}},
	}
	m, l := BaselineData(spec, ivs)
	if !slices.Equal(m["m"], []float64{2, 6}) {
		t.Errorf("metric values %v", m["m"])
	}
	app := l["app"]
	if app.Lines != 6 || app.Minutes != 4 || len(app.Clusters) != 2 || app.Clusters[0].Template != "a" || app.Clusters[0].Count != 5 {
		t.Errorf("log baseline %+v", app)
	}
	if app.Clusters[0].Samples != nil {
		t.Errorf("samples kept: %v", app.Clusters[0].Samples)
	}

	// Merge: 600 values over three runs keep the newest 500, in time order.
	seq := func(from, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = float64(from + i)
		}
		return out
	}
	recs := []BaselineRecord{ // newest first
		{Run: BaselineRun{Number: 3}, Metrics: map[string][]float64{"m": seq(400, 200)}, Logs: map[string]BaselineLog{"app": {Clusters: []Cluster{{Template: "a", Count: 1}}, Lines: 1, Minutes: 1}}},
		{Run: BaselineRun{Number: 2}, Metrics: map[string][]float64{"m": seq(200, 200)}},
		{Run: BaselineRun{Number: 1}, Metrics: map[string][]float64{"m": seq(0, 200)}, Logs: map[string]BaselineLog{"app": {Clusters: []Cluster{{Template: "a", Count: 2}, {Template: "z", Count: 4}}, Lines: 6, Minutes: 2}}},
	}
	b := MergeBaselines(recs)
	if got := b.Metrics["m"]; len(got) != 500 || got[0] != 100 || got[499] != 599 {
		t.Errorf("merged %d values from %v to %v", len(got), got[0], got[len(got)-1])
	}
	if len(b.Runs) != 3 || b.Runs[0].Number != 3 {
		t.Errorf("runs %+v", b.Runs)
	}
	if got := b.Logs["app"]; got.Lines != 7 || got.Minutes != 3 || clusterList(got.Clusters) != "z:4 a:3" {
		t.Errorf("merged log %+v", got)
	}
	if s := Summarize([]float64{1, 2, 3, nan()}); s != (Stats{N: 3, Mean: 2, SD: 1, Min: 1, Max: 3}) {
		t.Errorf("summarize %+v", s)
	}
	if Summarize(nil) != (Stats{}) {
		t.Error("summarize of nothing is not zero")
	}
}

func TestDowngrade(t *testing.T) {
	cases := []struct {
		from, model, want string
	}{
		{Passed, Inconclusive, Inconclusive},
		{Passed, Failed, Failed},
		{Passed, Passed, Passed},
		{Inconclusive, Failed, Failed},
		{Inconclusive, Passed, Inconclusive},
		{Failed, Passed, Failed},
		{Failed, Inconclusive, Failed},
		{Running, Failed, Running},
		{Passed, "approve", Passed},
	}
	for _, c := range cases {
		in := Verdict{Outcome: c.from, Final: true, Summary: "All 1 metrics passed over 3 intervals."}
		got := Downgrade(in, "claude-x", c.model, "the error log shows a new stack trace")
		if got.Outcome != c.want {
			t.Errorf("%s + model %s = %s, want %s", c.from, c.model, got.Outcome, c.want)
		}
		changed := got.Outcome != c.from
		if changed != (got.Summary != in.Summary) {
			t.Errorf("%s -> %s: summary %q", c.from, got.Outcome, got.Summary)
		}
		if changed && !strings.Contains(got.Summary, "model claude-x changed it to "+c.want+": the error log shows a new stack trace.") {
			t.Errorf("summary %q", got.Summary)
		}
	}
}

func clusterList(cs []Cluster) string {
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s:%d", c.Template, c.Count))
	}
	return strings.Join(out, " ")
}
