package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/metrics"
)

const (
	queueSize  = 2048
	batchSize  = 256
	flushEvery = 5 * time.Second
)

type exporter struct {
	cfg  Config
	ch   chan Span
	done chan struct{}
	wg   sync.WaitGroup
	kick chan chan struct{}
}

func newExporter(c Config) *exporter {
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if c.Service == "" {
		c.Service = "launchway"
	}
	e := &exporter{cfg: c, ch: make(chan Span, queueSize), done: make(chan struct{}), kick: make(chan chan struct{})}
	e.wg.Add(1)
	go e.loop()
	return e
}

func (e *exporter) add(s Span) {
	select {
	case e.ch <- s:
	default:
		metrics.Spans.Inc("dropped")
	}
}

func (e *exporter) loop() {
	defer e.wg.Done()
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	var batch []Span
	send := func() {
		if len(batch) > 0 {
			e.send(batch)
			batch = nil
		}
	}
	drain := func() {
		for {
			select {
			case s := <-e.ch:
				batch = append(batch, s)
				if len(batch) >= batchSize {
					send()
				}
			default:
				return
			}
		}
	}
	for {
		select {
		case s := <-e.ch:
			batch = append(batch, s)
			if len(batch) >= batchSize {
				send()
			}
		case <-t.C:
			send()
		case ack := <-e.kick:
			drain()
			send()
			close(ack)
		case <-e.done:
			drain()
			send()
			return
		}
	}
}

func (e *exporter) flush() {
	ack := make(chan struct{})
	select {
	case e.kick <- ack:
		<-ack
	case <-e.done:
	}
}

func (e *exporter) stop() {
	close(e.done)
	e.wg.Wait()
}

func (e *exporter) send(spans []Span) {
	body, err := Encode(spans, e.cfg.Service, e.cfg.Version)
	if err != nil {
		metrics.Spans.Add(float64(len(spans)), "dropped")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		metrics.Spans.Add(float64(len(spans)), "dropped")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := e.cfg.Client.Do(req) //nolint:gosec // the collector's address is the administrator's setting, and the client is the egress guard's
	if err != nil {
		metrics.Spans.Add(float64(len(spans)), "dropped")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		metrics.Spans.Add(float64(len(spans)), "dropped")
		return
	}
	metrics.Spans.Add(float64(len(spans)), "exported")
}

// ---- OTLP/HTTP JSON ----

type kv struct {
	Key   string `json:"key"`
	Value struct {
		S string `json:"stringValue"`
	} `json:"value"`
}

func attr(k, v string) kv {
	a := kv{Key: k}
	a.Value.S = v
	return a
}

type otlpSpan struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	Parent  string `json:"parentSpanId,omitempty"`
	Name    string `json:"name"`
	Kind    int    `json:"kind"`
	Start   string `json:"startTimeUnixNano"`
	End     string `json:"endTimeUnixNano"`
	Attrs   []kv   `json:"attributes,omitempty"`
	Status  struct {
		Code int `json:"code"`
	} `json:"status"`
}

// Encode renders spans as an OTLP ExportTraceServiceRequest in its JSON encoding.
func Encode(spans []Span, service, version string) ([]byte, error) {
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		o := otlpSpan{TraceID: s.TraceID, SpanID: s.SpanID, Parent: s.ParentID, Name: s.Name, Kind: 1,
			Start: strconv.FormatInt(s.Start.UnixNano(), 10), End: strconv.FormatInt(s.End.UnixNano(), 10)}
		if s.Server {
			o.Kind = 2
		}
		for _, a := range s.attrs {
			o.Attrs = append(o.Attrs, attr(a[0], a[1]))
		}
		o.Status.Code = 1
		if s.Failed {
			o.Status.Code = 2
		}
		out = append(out, o)
	}
	doc := map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []kv{attr("service.name", service), attr("service.version", version)}},
		"scopeSpans": []any{map[string]any{"scope": map[string]string{"name": "launchway"}, "spans": out}},
	}}}
	return json.Marshal(doc)
}
