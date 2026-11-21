package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// startNotifications runs the notification service: it tells people when an
// approval waits or something went wrong, by the rules of the notifications
// section. The addresses of the channels are references to the server's own
// secret stores (ai.secrets), and the calls go through the egress guard.
func startNotifications(ctx context.Context, eng *engine.Engine, srv *api.Server, cfg *config.Config, log *slog.Logger) {
	n := engine.Notifier{BaseURL: cfg.Reports.Origin, Client: egress.Default.HTTPClient(20 * time.Second), Log: log}
	if srv.AISecrets != nil {
		n.Secrets = srv.AISecrets
	}
	go eng.RunNotifications(ctx, n)
	go srv.RunReleaseWork(ctx)
}
