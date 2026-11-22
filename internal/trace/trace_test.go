package trace

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTraceparent(t *testing.T) {
	id, sp := RunTraceID("run_1"), SpanIDOf("run_1")
	v := Format(id, sp, true)
	p, ok := Parse(v)
	if !ok || p.TraceID != id || p.SpanID != sp || !p.Sampled {
		t.Fatalf("round trip: %+v %v", p, ok)
	}
	for _, bad := range []string{"", "00-xyz", "01-" + id + "-" + sp + "-01", "00-" + strings.Repeat("0", 32) + "-" + sp + "-01", "00-" + id + "-" + strings.Repeat("0", 16) + "-01", "00-" + id + "-" + sp} {
		if _, ok := Parse(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
	if RunTraceID("run_1") != id || RunTraceID("run_2") == id {
		t.Error("a run's trace id must be stable and its own")
	}
}

func TestSampling(t *testing.T) {
	kept := 0
	for i := 0; i < 2000; i++ {
		if Sampled(NewTraceID(), 0.25) {
			kept++
		}
	}
	if kept < 350 || kept > 650 {
		t.Errorf("kept %d of 2000 at 0.25", kept)
	}
	id := RunTraceID("r")
	first, second := Sampled(id, 0.5), Sampled(id, 0.5)
	if first != second || !Sampled(id, 1) || Sampled(id, 0) {
		t.Error("sampling must be decided by the id alone")
	}
}

// Only keys on the allow-list travel: a command line, an environment value or a token has no way into a span.
func TestSpanAttributesAreAnAllowList(t *testing.T) {
	var s Span
	s.Set("launchway.step", "build")
	s.Set("command", "curl -H 'Authorization: Bearer hunter2' https://x")
	s.Set("env.TOKEN", "hunter2")
	s.Set("launchway.secret", "hunter2")
	s.Set("repository", "acme/shop")
	s.Set("launchway.outcome", strings.Repeat("x", 500))
	if len(s.Attrs()) != 2 {
		t.Fatalf("attrs: %v", s.Attrs())
	}
	if len(s.Attrs()[1][1]) != 128 {
		t.Error("a value is cut to 128 bytes")
	}
}

func TestExportOverOTLPJSON(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		auth = r.Header.Get("X-Api-Key")
		mu.Unlock()
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}
	}))
	defer srv.Close()
	Configure(context.Background(), &Config{Endpoint: srv.URL + "/v1/traces", Headers: map[string]string{"X-Api-Key": "k"}, Ratio: 1, Version: "t"})
	defer Configure(context.Background(), nil)
	s := Span{TraceID: RunTraceID("run_9"), SpanID: SpanIDOf("a"), ParentID: SpanIDOf("b"), Name: "step build", Start: time.Unix(10, 5), End: time.Unix(12, 0), Failed: true}
	s.Set("launchway.step", "build")
	Record(s)
	Flush()
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || auth != "k" {
		t.Fatalf("bodies %d, key %q", len(bodies), auth)
	}
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					TraceID, SpanID, ParentSpanID, Name, StartTimeUnixNano string
					Status                                                 struct{ Code int }
					Attributes                                             []struct{ Key string }
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(bodies[0]), &doc); err != nil {
		t.Fatal(err)
	}
	sp := doc.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if sp.TraceID != s.TraceID || sp.Name != "step build" || sp.StartTimeUnixNano != "10000000005" || sp.Status.Code != 2 || sp.ParentSpanID != s.ParentID || len(sp.Attributes) != 1 {
		t.Fatalf("span: %+v", sp)
	}
}

func TestOffByDefaultAndRatioZeroSendsNothing(t *testing.T) {
	if Enabled() {
		t.Fatal("tracing must be off until configured")
	}
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit <- struct{}{} }))
	defer srv.Close()
	Record(Span{TraceID: NewTraceID(), SpanID: NewSpanID(), Name: "x", Start: time.Now(), End: time.Now()}) // off: a no-op
	Configure(context.Background(), &Config{Endpoint: srv.URL, Ratio: 0})
	defer Configure(context.Background(), nil)
	Record(Span{TraceID: NewTraceID(), SpanID: NewSpanID(), Name: "x", Start: time.Now(), End: time.Now()})
	Flush()
	select {
	case <-hit:
		t.Fatal("a span was sent at ratio 0")
	default:
	}
}
