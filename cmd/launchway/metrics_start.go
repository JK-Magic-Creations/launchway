package main

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/metrics"
	"github.com/jk-magic-creations/launchway/internal/trace"
)

// startMetrics serves the metrics endpoint on its own listener, unless
// observability.metrics.enabled is false. A listener that cannot start is a
// warning, never a reason for the product not to start: monitoring must not
// be able to take the product down.
func startMetrics(ctx context.Context, cfg *config.Config, eng *engine.Engine, version string, log *slog.Logger) {
	m := cfg.Observability.Metrics
	metrics.Init(version)
	metrics.SetNameLabels(m.Names())
	if !m.On() {
		return
	}
	if eng != nil {
		eng.RegisterMetrics(metrics.Default)
	}
	metrics.WatchDisk(cfg.Data)
	addr, err := metrics.Serve(ctx, metrics.Default, m.Address(), m.Token())
	if err != nil {
		log.Warn("the metrics endpoint could not start; the product runs without it", "address", m.Address(), "err", err)
		return
	}
	log.Info("metrics are served on their own address, not the one the API uses", "address", addr, "token", m.Token() != "")
}

// startRunnerMetrics serves a Station's (or the deploy runner's) metrics on
// --metrics-listen. The bearer token, if any, comes from LAUNCHWAY_METRICS_TOKEN.
func startRunnerMetrics(ctx context.Context, o *runnerOptions) {
	startRunnerTracing()
	if o.MetricsListen == "" {
		return
	}
	metrics.Init(version)
	token := os.Getenv("LAUNCHWAY_METRICS_TOKEN")
	addr, err := metrics.Serve(ctx, metrics.Default, o.MetricsListen, token)
	if err != nil {
		slog.Warn("the metrics endpoint could not start; the Station runs without it", "address", o.MetricsListen, "err", err)
		return
	}
	slog.Info("metrics are served on their own address", "address", addr, "token", token != "")
}

// startTracing turns trace export on when observability.tracing.enabled is set. The collector is reached
// through the same egress guard as every other outbound request of the server, so egress.allow decides
// whether an address inside the network may be used.
func startTracing(cfg *config.Config, log *slog.Logger) {
	t := cfg.Observability.Tracing
	if !t.Enabled {
		return
	}
	trace.Configure(&trace.Config{Endpoint: t.Endpoint, Headers: t.Headers(), Ratio: t.Ratio(), Client: egress.Default.HTTPClient(10 * time.Second),
		Service: "launchway-server", Version: version})
	api.SetTraceViewer(t.ViewerURLTemplate)
	log.Info("traces are sent to the collector", "endpoint", redactAddress(t.Endpoint), "sample_ratio", t.Ratio())
}

// startRunnerTracing turns trace export on for a Station from its environment: LAUNCHWAY_TRACING_ENDPOINT,
// LAUNCHWAY_TRACING_HEADERS (Name=value,...) and LAUNCHWAY_TRACING_SAMPLE_RATIO. A Station is not behind the
// server's egress guard: the collector is whatever its network allows.
func startRunnerTracing() {
	ep := os.Getenv("LAUNCHWAY_TRACING_ENDPOINT")
	if ep == "" {
		return
	}
	t := config.TracingConfig{Enabled: true, Endpoint: ep, HeadersEnv: "LAUNCHWAY_TRACING_HEADERS"}
	if r, err := strconv.ParseFloat(os.Getenv("LAUNCHWAY_TRACING_SAMPLE_RATIO"), 64); err == nil && r >= 0 && r <= 1 {
		t.SampleRatio = &r
	}
	trace.Configure(&trace.Config{Endpoint: t.Endpoint, Headers: t.Headers(), Ratio: t.Ratio(), Service: "launchway-station", Version: version})
}

// redactAddress is an address without its user, password and query, for the log.
func redactAddress(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "(unreadable)"
	}
	u.User, u.RawQuery = nil, ""
	return u.String()
}
