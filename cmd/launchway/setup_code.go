package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// announceSetup makes sure a brand-new installation has its one-time setup
// code and, when this start made the code, says where to use it. A code that
// came from a file (the chart's Secret) is never printed. The code is printed
// once, to the log, only when no other channel carries it.
func announceSetup(srv *api.Server, cfg *config.Config, log *slog.Logger) error {
	code, err := srv.EnsureSetupCode()
	if err != nil {
		return fmt.Errorf("preparing the first-run setup code: %w", err)
	}
	if code == "" {
		return nil
	}
	fmt.Fprintf(os.Stderr, "\nThis installation has no administrator yet.\n"+
		"  Open the address of this server in a browser and enter this one-time setup code (valid 24 hours, works once):\n\n"+
		"      %s\n\n"+
		"  Lost it or expired? Run `launchway users setup-code` on this server for a new one.\n\n", code)
	log.Info("waiting for first-run setup", "listen", cfg.Listen)
	return nil
}

// startServices starts what runs beside the API: Git hosting, and the first-run setup code.
func startServices(ctx context.Context, eff, cfg *config.Config, eng *engine.Engine, log *slog.Logger, srv *api.Server) error {
	if err := announceSetup(srv, cfg, log); err != nil {
		return err
	}
	return startHosting(ctx, eff, eng, log, srv)
}
