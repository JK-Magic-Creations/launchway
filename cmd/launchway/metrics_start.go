package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/metrics"
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
