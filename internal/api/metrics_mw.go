package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/metrics"
	"github.com/jk-magic-creations/launchway/internal/trace"
)

// requestMetrics counts and times every request by route template, method and
// status class. The route is the pattern the mux matched (never the path, so
// a run id or a repository name cannot become a label); requests no pattern
// matched are "unmatched".
func (s *Server) requestMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hold := &routeHolder{}
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, hold))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		metrics.HTTPInFlight.Add(1)
		defer func() {
			metrics.HTTPInFlight.Add(-1)
			route := hold.route()
			metrics.HTTPRequests.Inc(route, r.Method, metrics.ClassOf(sw.status))
			metrics.HTTPDuration.Observe(time.Since(start).Seconds(), route, r.Method)
			requestSpan(r, route, sw.status, start)
		}()
		next.ServeHTTP(sw, r)
	})
}

type routeKey struct{}

type routeHolder struct{ pattern string }

func (h *routeHolder) route() string {
	if h.pattern == "" {
		return "unmatched"
	}
	return h.pattern
}

// noteRoute remembers which route template served a request, for its metrics.
func noteRoute(r *http.Request, pattern string) {
	if h, ok := r.Context().Value(routeKey{}).(*routeHolder); ok {
		// "GET /api/v1/runs/{id}" is labelled by its path template and method separately.
		if i := strings.IndexByte(pattern, ' '); i >= 0 {
			pattern = pattern[i+1:]
		}
		h.pattern = pattern
	}
}

// routed serves a request with the mux and notes the pattern it matched.
func routed(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		noteRoute(r, r.Pattern)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection (hijack, deadlines).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// tokenKind names how a request was authenticated, for the token metric.
func tokenKind(via string) string {
	switch via {
	case viaSession, viaToken, viaNone:
		return via
	}
	return "other"
}

// countAudit counts sign-in attempts and incoming webhook calls from the audit
// entries that record them.
func countAudit(e *engine.AuditEntry) {
	var method string
	switch e.Action {
	case "auth.local.sign_in":
		method = "local"
	case "auth.sign_in":
		method = "oidc"
	case "webhook.git", "webhook.run":
		switch e.Outcome {
		case "ok":
			metrics.WebhookDeliveries.Inc("in", "accepted")
		case "failed":
			metrics.WebhookDeliveries.Inc("in", "failed")
		default:
			metrics.WebhookDeliveries.Inc("in", "rejected")
		}
		return
	default:
		return
	}
	outcome := "denied"
	if e.Outcome == "ok" || e.Outcome == "" {
		outcome = "ok"
	}
	metrics.SignIns.Inc(method, outcome)
}

// requestSpan records a span for a request when trace export is on, continuing the trace a caller sent in
// a traceparent header (a Station does, for its step) or starting one. The Stations' own polling is not traced
// unless it comes with a trace context.
func requestSpan(r *http.Request, route string, status int, start time.Time) {
	if !trace.Enabled() {
		return
	}
	parent, ok := trace.FromRequest(r.Header)
	if !ok && strings.HasPrefix(route, "/api/runner/") {
		return
	}
	sp := trace.Span{Name: "HTTP " + r.Method + " " + route, Server: true, Start: start, End: time.Now(), Failed: status >= 500, SpanID: trace.NewSpanID()}
	if ok {
		sp.TraceID, sp.ParentID = parent.TraceID, parent.SpanID
	} else {
		sp.TraceID = trace.NewTraceID()
	}
	sp.Set("http.route", route)
	sp.Set("http.request.method", r.Method)
	sp.Set("http.response.status_code", strconv.Itoa(status))
	trace.Record(sp)
}

// addTrace gives a run's detail its trace id, and a link to the trace viewer when the installation has one.
func addTrace(d *engine.RunDetail) {
	if !trace.Enabled() {
		return
	}
	d.TraceID = trace.RunTraceID(d.ID)
	if tpl := traceViewer.Load(); tpl != nil && *tpl != "" {
		d.TraceURL = strings.ReplaceAll(*tpl, "{trace_id}", d.TraceID)
	}
}

var traceViewer atomic.Pointer[string]

// SetTraceViewer sets the trace viewer address template, with {trace_id} in it (observability.tracing.viewer_url_template).
func SetTraceViewer(tpl string) { traceViewer.Store(&tpl) }
