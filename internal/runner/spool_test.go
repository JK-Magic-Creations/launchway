package runner

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

func testSpool(t *testing.T, maxBytes int64, maxAge time.Duration, now func() time.Time) *stepSpool {
	return &stepSpool{dir: filepath.Join(t.TempDir(), "lease"), st: spoolState{Lease: "l1"}, maxBytes: maxBytes, maxAge: maxAge, now: now}
}

func lineN(i int, at time.Time) engine.LogLine {
	return engine.LogLine{At: at.UnixMilli(), Stream: "stdout", Text: fmt.Sprintf("line %04d", i)}
}

// When the size bound is hit the oldest lines go, one marker says how many,
// the newest lines stay, and the result is never touched.
func TestSpoolDropsOldestLinesWithAMarkerButKeepsTheResult(t *testing.T) {
	now := time.Now()
	s := testSpool(t, 4000, time.Hour, func() time.Time { return now })
	for i := 0; i < 200; i += 10 {
		var b []engine.LogLine
		for j := i; j < i+10; j++ {
			b = append(b, lineN(j, now))
		}
		if err := s.append(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveResult(engine.Result{Outcome: "succeeded", Outputs: map[string]string{"k": "v"}}); err != nil {
		t.Fatal(err)
	}
	lines, _, err := s.readAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 || len(lines) >= 200 {
		t.Fatalf("%d lines kept", len(lines))
	}
	if lines[0].Stream != "system" || s.st.Dropped == 0 {
		t.Fatalf("the first line should be the marker, dropped=%d: %+v", s.st.Dropped, lines[0])
	}
	if last := lines[len(lines)-1].Text; last != "line 0199" {
		t.Fatalf("the newest line must stay, last is %q", last)
	}
	res, err := s.loadResult()
	if err != nil || res == nil || res.Outcome != "succeeded" || res.Outputs["k"] != "v" {
		t.Fatalf("the result must survive the bound: %+v %v", res, err)
	}
}

// Lines older than the age bound are dropped as well.
func TestSpoolDropsLinesOlderThanTheAgeBound(t *testing.T) {
	now := time.Now()
	s := testSpool(t, 1<<20, 15*time.Minute, func() time.Time { return now })
	if err := s.append([]engine.LogLine{lineN(1, now.Add(-time.Hour)), lineN(2, now.Add(-time.Minute))}); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := s.readAll()
	if len(lines) != 2 || lines[0].Stream != "system" || lines[1].Text != "line 0002" {
		t.Fatalf("%+v", lines)
	}
}

// Nothing is written until something fails: a spool that is only drained
// leaves no directory behind.
func TestSpoolWritesNothingWhenTheServerIsFine(t *testing.T) {
	s := testSpool(t, 1<<20, time.Hour, time.Now)
	if err := s.Drain(context.Background()); err != nil || !s.Done() {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.dir); err == nil {
		t.Fatal("a directory was created without an outage")
	}
}

func TestIsOutage(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&httpError{code: 503}, true},
		{&httpError{code: 502}, true},
		{&httpError{code: 400}, false},
		{&httpError{code: 403}, false},
		{ErrLeaseLost, false},
		{ErrTooLarge, false},
		{fmt.Errorf("dial tcp: connection refused"), true},
		{nil, false},
	} {
		if got := isOutage(c.err); got != c.want {
			t.Errorf("%v: %v", c.err, got)
		}
	}
}

// A runner that stops while it is delivering a spool keeps the spool for the next start; a
// canceled call is the runner's own doing, not a reason to drop the kept lines.
func TestStoppingWhileDeliveringKeepsTheSpool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	r := &Runner{Client: NewClient(srv.URL, "x"), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DataDir: t.TempDir()}
	s := testSpool(t, 1<<20, time.Hour, time.Now)
	s.c = r.Client
	if err := s.append([]engine.LogLine{lineN(1, time.Now())}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r.deliverSpool(ctx, s) {
		t.Fatal("a spool that could not be delivered was reported as finished")
	}
	if _, err := os.Stat(s.path("logs.jsonl")); err != nil {
		t.Fatalf("the kept lines were dropped: %v", err)
	}
}
