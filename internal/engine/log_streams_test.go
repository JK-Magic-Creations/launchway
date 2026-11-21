package engine

import "testing"

func TestServiceLogStream(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, linear, Trigger{Event: "manual"})
	j := take(t, e)
	if err := e.AppendLogs(j.LeaseID, []LogLine{
		{Stream: "stdout", Text: "step output"},
		{Stream: "service", Text: "postgres: database system is ready"},
		{Stream: "service", Text: "cache: ready to accept connections"},
		{Stream: "service", Text: "postgres_extra: not postgres"},
	}); err != nil {
		t.Fatal(err)
	}
	lines := func(q LogQuery) []string {
		got, _, err := e.QueryLogs(id, j.StepID, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range got {
			out = append(out, l.Text)
		}
		return out
	}
	if got := lines(LogQuery{}); len(got) != 1 || got[0] != "step output" {
		t.Fatalf("default leaves services out: %v", got)
	}
	if got := lines(LogQuery{Stream: "service"}); len(got) != 3 {
		t.Fatalf("service: %v", got)
	}
	if got := lines(LogQuery{Stream: "service", Service: "postgres"}); len(got) != 1 || got[0] != "postgres: database system is ready" {
		t.Fatalf("one service: %v", got)
	}
	for _, bad := range [][2]string{{"all", ""}, {"stdout", "postgres"}, {"service", "a b"}} {
		if CheckLogStream(bad[0], bad[1]) == "" {
			t.Errorf("%v should be refused", bad)
		}
	}
}
