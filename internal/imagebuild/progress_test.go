package imagebuild

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

type lineLog struct {
	mu    sync.Mutex
	lines []string
	at    []time.Time
}

func (l *lineLog) add(s string) {
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.at = append(l.at, time.Now())
	l.mu.Unlock()
}

func (l *lineLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func dg(s string) digest.Digest { return digest.FromString(s) }

func TestProgressPrinterLinesAsTheyHappen(t *testing.T) {
	var out lineLog
	pp := newProgressPrinter(out.add)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, b := dg("a"), dg("b")
	t1 := t0.Add(time.Second)
	// Two steps run at once; the second's output must not wait for the first.
	pp.Update(&client.SolveStatus{Vertexes: []*client.Vertex{
		{Digest: a, Name: "[build 3/9] RUN go build ./...", Started: &t0},
		{Digest: b, Name: "[build 4/9] RUN go vet ./...", Started: &t0},
	}})
	pp.Update(&client.SolveStatus{Logs: []*client.VertexLog{{Vertex: b, Stream: 1, Data: []byte("vetting\n"), Timestamp: t1}}})
	if got := out.text(); !strings.Contains(got, "#2 [build 4/9] RUN go vet") || !strings.Contains(got, "#2 1.000 vetting") {
		t.Fatalf("the second step's output was held back:\n%s", got)
	}
	done := t0.Add(41200 * time.Millisecond)
	pp.Update(&client.SolveStatus{Vertexes: []*client.Vertex{{Digest: a, Name: "[build 3/9] RUN go build ./...", Started: &t0, Completed: &done}}})
	if !strings.Contains(out.text(), "#1 DONE 41.2s") {
		t.Fatalf("no timing:\n%s", out.text())
	}
	c := dg("c")
	pp.Update(&client.SolveStatus{Vertexes: []*client.Vertex{{Digest: c, Name: "[build 5/9] COPY . .", Started: &done, Completed: &done, Cached: true}}})
	if !strings.Contains(out.text(), "#3 CACHED") {
		t.Fatalf("cache hit not marked:\n%s", out.text())
	}
	pp.Update(&client.SolveStatus{Vertexes: []*client.Vertex{{Digest: b, Started: &t0, Completed: &done, Error: "exit code 1"}}})
	if !strings.Contains(out.text(), "#2 ERROR: exit code 1") {
		t.Fatalf("error not shown:\n%s", out.text())
	}
}

func TestProgressPrinterThrottlesLayerProgress(t *testing.T) {
	var out lineLog
	pp := newProgressPrinter(out.add)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v := dg("push")
	for i := 0; i < 1000; i++ { // 100 s of updates, one every 100 ms
		ts := t0.Add(time.Duration(i) * 100 * time.Millisecond)
		pp.Update(&client.SolveStatus{Statuses: []*client.VertexStatus{{ID: "sha256:abc", Vertex: v, Current: int64(i) << 20, Total: 1000 << 20, Timestamp: ts}}})
	}
	n := 0
	for _, l := range out.lines {
		if strings.Contains(l, "sha256:abc") {
			n++
		}
	}
	if n < 95 || n > 105 { // about one a second
		t.Fatalf("%d progress lines for 100 s; want about 100", n)
	}
	end := t0.Add(101 * time.Second)
	pp.Update(&client.SolveStatus{Statuses: []*client.VertexStatus{{ID: "sha256:abc", Vertex: v, Current: 1000 << 20, Total: 1000 << 20, Timestamp: end, Completed: &end}}})
	if !strings.Contains(out.lines[len(out.lines)-1], "done (1000.0MB)") {
		t.Fatalf("last line: %s", out.lines[len(out.lines)-1])
	}
}

func TestProgressPrinterPartialLineAndLiveDrain(t *testing.T) {
	var out lineLog
	pp := newProgressPrinter(out.add)
	ch := make(chan *client.SolveStatus)
	stats := &solveStats{}
	fin := make(chan struct{})
	go func() { drainProgress(ch, pp, stats); close(fin) }()
	t0 := time.Now()
	v := dg("v")
	sent := time.Now()
	ch <- &client.SolveStatus{Vertexes: []*client.Vertex{{Digest: v, Name: "[1/2] RUN download", Started: &t0}}}
	ch <- &client.SolveStatus{Logs: []*client.VertexLog{{Vertex: v, Data: []byte("45%"), Timestamp: t0}}}
	// The text without a newline appears on its own, after the idle wait.
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.text(), "45%") {
		if time.Now().After(deadline) {
			t.Fatalf("a line without a newline never appeared:\n%s", out.text())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := time.Since(sent); d > testlib.Slow(2*time.Second) {
		t.Fatalf("the partial line took %s", d)
	}
	close(ch)
	<-fin
}
