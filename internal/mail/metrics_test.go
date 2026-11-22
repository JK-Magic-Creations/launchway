package mail

import (
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/metrics"
)

func TestQueueCountsWhatItDoesAndNothingElse(t *testing.T) {
	q, srv, clk, _ := newQueue(t)
	q.MaxAttempts = 2
	sent, retried, failed := metrics.MailMessages.Value("sent"), metrics.MailMessages.Value("retried"), metrics.MailMessages.Value("failed")
	srv.FailNext(2)
	if _, err := q.Enqueue(t.Context(), item("dana", "first")); err != nil {
		t.Fatal(err)
	}
	_, _ = q.RunOnce(t.Context()) // try 1 fails: retried
	clk.Advance(time.Minute)
	_, _ = q.RunOnce(t.Context()) // try 2 fails: given up
	if _, err := q.Enqueue(t.Context(), item("eve", "second")); err != nil {
		t.Fatal(err)
	}
	if n, _ := q.RunOnce(t.Context()); n != 1 {
		t.Fatalf("sent %d", n)
	}
	if d := metrics.MailMessages.Value("retried") - retried; d != 1 {
		t.Fatalf("retried %v", d)
	}
	if d := metrics.MailMessages.Value("failed") - failed; d != 1 {
		t.Fatalf("failed %v", d)
	}
	if d := metrics.MailMessages.Value("sent") - sent; d != 1 {
		t.Fatalf("sent %v", d)
	}
	reg := metrics.NewRegistry()
	q.RegisterMetrics(reg)
	var b stringsBuilder
	if err := reg.Write(&b); err != nil {
		t.Fatal(err)
	}
	if got := metrics.MailQueued.Value("failed"); got != 1 {
		t.Fatalf("failed in queue %v", got)
	}
}

type stringsBuilder struct{ n int }

func (b *stringsBuilder) Write(p []byte) (int, error) { b.n += len(p); return len(p), nil }
