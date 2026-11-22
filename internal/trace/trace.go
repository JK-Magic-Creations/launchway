// Package trace sends traces over OTLP/HTTP using its JSON encoding, which the
// OpenTelemetry specification defines and keeps stable; writing it by hand keeps
// the product free of the protobuf and gRPC libraries a generated client needs.
// It is off unless an administrator sets an endpoint.
//
// What a span may carry is an allow-list (Allowed): a name from the product's
// own vocabulary, an outcome, a pool, a route template, a status. A command
// line, an environment value, a token, a secret's name or value, a repository
// path or file content has no key to travel in, so it cannot be sent by a
// mistake in a caller.
//
// A run's trace id is worked out from the run's id (RunTraceID), so the server,
// every Station and the run page agree on it with nothing stored.
package trace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Allowed lists the attribute keys a span may carry.
var Allowed = map[string]bool{
	"launchway.step":            true, // a step's id, as written in the pipeline
	"launchway.attempt":         true,
	"launchway.outcome":         true, // succeeded, failed, canceled, skipped, deployed, ...
	"launchway.pool":            true,
	"launchway.phase":           true, // checkout, pod start, commands, reports
	"launchway.group":           true, // the index of a command group, never its text
	"launchway.kind":            true, // run, step, queue, deploy
	"http.route":                true, // a route template, never a path
	"http.request.method":       true,
	"http.response.status_code": true,
}

// Span is a finished span.
type Span struct {
	TraceID, SpanID, ParentID string
	Name                      string
	Server                    bool // a server span (a request handled); otherwise internal
	Start, End                time.Time
	Failed                    bool
	attrs                     [][2]string
}

// Set adds an attribute if its key is allowed; any other key is dropped. Values are cut to 128 bytes.
func (s *Span) Set(key, value string) {
	if !Allowed[key] {
		return
	}
	if len(value) > 128 {
		value = value[:128]
	}
	s.attrs = append(s.attrs, [2]string{key, value})
}

// Attrs returns the attributes, for tests.
func (s *Span) Attrs() [][2]string { return s.attrs }

// RunTraceID is the trace id of a run (and of a deploy, by its id).
func RunTraceID(id string) string {
	h := sha256.Sum256([]byte("launchway-trace:" + id))
	return hex.EncodeToString(h[:16])
}

// SpanIDOf is a span id worked out from the names that locate it, the same on every server.
func SpanIDOf(parts ...string) string {
	h := sha256.Sum256([]byte("launchway-span:" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// NewSpanID is a random span id.
func NewSpanID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewTraceID is a random trace id, for a request that carried none.
func NewTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Sampled says whether a trace is kept at the ratio: decided by the trace id alone, so every
// component that sees the same id decides the same way.
func Sampled(traceID string, ratio float64) bool {
	if ratio >= 1 {
		return true
	}
	if ratio <= 0 {
		return false
	}
	b, err := hex.DecodeString(traceID)
	if err != nil || len(b) < 8 {
		return false
	}
	return float64(binary.BigEndian.Uint64(b[8:16])>>11)/float64(1<<53) < ratio
}

// ---- W3C trace context ----

// Header is the W3C trace-context header.
const Header = "traceparent"

// Parent is what a traceparent header says.
type Parent struct {
	TraceID, SpanID string
	Sampled         bool
}

// Parse reads a traceparent value ("00-<32 hex>-<16 hex>-<2 hex>"); ok is false for anything else.
func Parse(v string) (Parent, bool) {
	p := strings.Split(strings.TrimSpace(v), "-")
	if len(p) != 4 || p[0] != "00" || len(p[1]) != 32 || len(p[2]) != 16 || len(p[3]) != 2 {
		return Parent{}, false
	}
	for _, s := range p[1:] {
		if _, err := hex.DecodeString(s); err != nil {
			return Parent{}, false
		}
	}
	if p[1] == strings.Repeat("0", 32) || p[2] == strings.Repeat("0", 16) {
		return Parent{}, false
	}
	return Parent{TraceID: p[1], SpanID: p[2], Sampled: p[3] == "01"}, true
}

// Format writes a traceparent value.
func Format(traceID, spanID string, sampled bool) string {
	f := "00"
	if sampled {
		f = "01"
	}
	return "00-" + traceID + "-" + spanID + "-" + f
}

// FromRequest reads the trace context of a request.
func FromRequest(h http.Header) (Parent, bool) { return Parse(h.Get(Header)) }

// ---- the exporter ----

// Config says where and how traces are sent.
type Config struct {
	Endpoint string
	Headers  map[string]string
	Ratio    float64
	Client   *http.Client
	Service  string
	Version  string
}

var (
	mu      sync.RWMutex
	current *exporter
	ratio   atomic.Value // float64
)

// Configure turns export on (a non-empty endpoint) or off (nil or an empty endpoint). It stops the previous exporter after sending what it holds.
func Configure(ctx context.Context, c *Config) {
	mu.Lock()
	old := current
	current = nil
	if c != nil && c.Endpoint != "" {
		current = newExporter(context.WithoutCancel(ctx), *c)
		ratio.Store(c.Ratio)
	}
	mu.Unlock()
	if old != nil {
		old.stop()
	}
}

// Enabled says whether spans are being exported.
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return current != nil
}

// Ratio is the sampling ratio in force (1 when none was set).
func Ratio() float64 {
	if r, ok := ratio.Load().(float64); ok {
		return r
	}
	return 1
}

// Record hands a finished span to the exporter if tracing is on and the trace is sampled.
// It never blocks: a full queue drops the span and counts it.
func Record(s Span) {
	mu.RLock()
	e := current
	mu.RUnlock()
	if e == nil || s.TraceID == "" || !Sampled(s.TraceID, Ratio()) {
		return
	}
	e.add(s)
}

// Flush sends what is queued now, for tests and for shutdown.
func Flush() {
	mu.RLock()
	e := current
	mu.RUnlock()
	if e != nil {
		e.flush()
	}
}

type ctxKey struct{}

// WithParent returns a context that carries a trace context, which the Station's client sends on every
// request it makes for that step.
func WithParent(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, traceparent)
}

// ParentFrom returns the traceparent a context carries.
func ParentFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}
