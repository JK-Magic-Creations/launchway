package debrief

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDetectFindsExpectedAndActual(t *testing.T) {
	cases := []struct{ name, msg, exp, act string }{
		{"junit", "expected:<42> but was:<41>", "42", "41"},
		{"testify", "Error: Not equal:\n expected: \"abc\"\n actual  : \"abd\"\nDiff:", "abc", "abd"},
		{"jest", "Expected: 5\nReceived: 6", "5", "6"},
		{"go got want", "TestX: got 3, want 4", "4", "3"},
		{"go want got", "want 7, got 8", "7", "8"},
		{"python", "AssertionError: 'a' != 'b'", "a", "b"},
		{"words", "expected 10 but got 11", "10", "11"},
	}
	for _, c := range cases {
		d := Detect(c.msg, "")
		if d == nil || d.Expected != c.exp || d.Actual != c.act {
			t.Errorf("%s: got %+v, want %q / %q", c.name, d, c.exp, c.act)
		}
	}
	if d := Detect("timed out after 30s", ""); d != nil {
		t.Errorf("no values to compare, got a diff: %+v", d)
	}
	if d := Detect("expected:<1> but was:<1>", ""); d != nil {
		t.Errorf("equal values are not a diff")
	}
}

func TestLineDiff(t *testing.T) {
	got := LineDiff("a\nb\nc", "a\nB\nc\nd")
	want := []DiffLine{{" ", "a"}, {"-", "b"}, {"+", "B"}, {" ", "c"}, {"+", "d"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	big := strings.Repeat("x\n", maxDiffLines+5)
	if n := len(LineDiff(big, "y")); n != maxDiffLines+6+1 {
		t.Errorf("a huge diff is listed without comparing, got %d lines", n)
	}
	p, a, b, s := InlineParts("hello world", "hello brave world")
	if p != "hello " || a != "" || b != "brave " || s != "world" {
		t.Errorf("inline parts %q %q %q %q", p, a, b, s)
	}
}

func obs(shaStatus ...string) []Observation {
	var out []Observation
	for i, s := range shaStatus {
		sha, st, _ := strings.Cut(s, ":")
		out = append(out, Observation{Run: sha, Number: i + 1, SHA: sha, Ref: "refs/heads/main", At: int64(i + 1), Status: st})
	}
	return out
}

func TestFlakeRules(t *testing.T) {
	// The same commit passed and failed.
	v := Score(obs("a:passed", "b:failed", "b:passed", "c:passed"))
	if !v.Flaky || v.SameCommit != 1 || v.Score != 33 || v.FirstSeen != 2 {
		t.Errorf("same commit: %+v", v)
	}
	// A retry that passed is the same.
	o := obs("a:passed", "b:passed")
	o[1].Retried = true
	if v := Score(o); !v.Flaky || v.SameCommit != 1 {
		t.Errorf("retry: %+v", v)
	}
	// A fix is not a flake: fails for a while and then passes.
	if v := Score(obs("a:passed", "b:failed", "c:failed", "d:passed", "e:passed")); v.Flaky {
		t.Errorf("a fixed break is flaky: %+v", v)
	}
	// A break that has not been fixed is not a flake.
	if v := Score(obs("a:passed", "b:failed", "c:failed", "d:failed")); v.Flaky {
		t.Errorf("a break is flaky: %+v", v)
	}
	// Alternating single-commit failures are.
	v = Score(obs("a:passed", "b:failed", "c:passed", "d:passed", "e:failed", "f:passed"))
	if !v.Flaky || v.Blips != 2 || v.Reason == "" || len(v.Branches) != 1 {
		t.Errorf("blips: %+v", v)
	}
	// One blip is not enough.
	if v := Score(obs("a:passed", "b:failed", "c:passed", "d:passed")); v.Flaky {
		t.Errorf("one blip is flaky: %+v", v)
	}
	if v := Score(nil); v.Flaky || v.Commits != 0 {
		t.Errorf("nothing: %+v", v)
	}
}

func TestSplitIsBalancedAndStable(t *testing.T) {
	var ts []Timing
	for i, ms := range []int64{900, 800, 700, 600, 500, 400, 300, 200, 100, 100} {
		ts = append(ts, Timing{Key: string(rune('a' + i)), Name: "T" + string(rune('a'+i)), AvgMs: ms, Samples: 3})
	}
	ts = append(ts, Timing{Key: "new", Name: "Tnew"}) // no history: gets the average
	s := Split(ts, 3, nil)
	if len(s) != 3 {
		t.Fatal(len(s))
	}
	lo, hi := s[0].TotalMs, s[0].TotalMs
	n := 0
	for _, sh := range s {
		lo, hi = min(lo, sh.TotalMs), max(hi, sh.TotalMs)
		n += len(sh.Tests)
	}
	if n != 11 || hi-lo > 900 {
		t.Errorf("shards %+v", s)
	}
	again := Split(ts, 3, nil)
	if !reflect.DeepEqual(s, again) {
		t.Error("a split is not stable")
	}
	if one := Split(nil, 0, nil); len(one) != 1 {
		t.Error("n below 1 is one shard")
	}
}

func TestQuarantine(t *testing.T) {
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	src := `quarantine:
  - test: "pkg/db::TestSlow*"
    owner: ana
    expires: 2026-12-31
    reason: network
  - test: TestOld
    owner: bo
    expires: 2026-11-20
  - test: NoOwner
    expires: 2026-12-01
  - test: Forever
    owner: cy
    expires: 2031-01-01
  - test: BadDay
    owner: di
    expires: soon
`
	q := ParseQuarantine([]byte(src), now)
	if len(q.Entries) != 2 || len(q.Problems) != 3 {
		t.Fatalf("%+v", q)
	}
	e, ok, exp := q.Match("pkg/db", "TestSlowQuery", now)
	if !ok || exp || e.Owner != "ana" {
		t.Errorf("match %+v %v %v", e, ok, exp)
	}
	if _, ok, exp := q.Match("x", "TestOld", now); !ok || !exp {
		t.Errorf("an expired entry must be found as expired: %v %v", ok, exp)
	}
	if _, ok, _ := q.Match("x", "TestOther", now); ok {
		t.Error("matched something else")
	}
	// The last day itself still counts.
	if _, _, exp := q.Match("pkg/db", "TestSlowX", time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)); exp {
		t.Error("expired on its last day")
	}
	if q := ParseQuarantine([]byte("quarantine: [\n"), now); len(q.Problems) != 1 {
		t.Error("bad YAML should be one problem")
	}
}

func TestRangesAndPatch(t *testing.T) {
	r := ParseRanges("1-3, 7,9-9,x,5-4,0")
	if r.String() != "1-3, 7, 9" {
		t.Errorf("ranges: %q", r.String())
	}
	if n := len(ParseRanges("1-99999999")); n != 0 {
		t.Errorf("a huge range was expanded to %d lines", n)
	}
	patch := "diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n@@ -1,3 +10,4 @@ func x\n ctx\n-old\n+new1\n+new2\n ctx2\n@@ -20 +30 @@\n+solo\n\\ No newline at end of file\n"
	if got := PatchLines(patch).String(); got != "11-12, 30" {
		t.Errorf("patch lines: %q", got)
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{{"internal/x/f.go", "internal/x/f.go", true}, {"github.com/o/p/internal/x/f.go", "internal/x/f.go", true},
		{"x/f.go", "internal/x/f.go", true}, {"other/f.go", "internal/x/f.go", false}, {"f.go", "of.go", false}, {"", "f.go", false}} {
		if PathMatches(c.a, c.b) != c.want {
			t.Errorf("PathMatches(%q,%q)", c.a, c.b)
		}
	}
}
