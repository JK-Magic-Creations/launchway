package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

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
	startToggles(ctx, eng, srv)
	if err := startHosting(ctx, eff, eng, log, srv); err != nil {
		return err
	}
	return startRegistry(ctx, eff, eng, log, srv)
}

// signInBanner says in a few words how people sign in, for the start-up message.
func signInBanner(cfg *config.Config, signedIn bool) string {
	switch {
	case !signedIn:
		return "no sign-in: anyone who can reach it has full access"
	case cfg.Auth.Mode == config.AuthLocal:
		return "sign-in with built-in accounts"
	case cfg.Auth.LocalAccounts():
		return "sign-in with " + cfg.Auth.OIDC.Issuer + " and built-in accounts"
	}
	return "sign-in with " + cfg.Auth.OIDC.Issuer
}

// settleSignIn decides, once the database is open, how this server signs
// people in. What the file says stands: an installation that runs with no
// sign-in on an address others can reach (auth.mode none with
// allow_anonymous, an explicit opt-in the older releases already required)
// is not switched by an upgrade, and says so loudly. The one thing that
// changes it is a request made from its page ("set up sign-in now"), kept in
// the database: then this start runs with built-in accounts.
func (su *serverSetup) settleSignIn(eng *engine.Engine) (*api.Auth, bool, error) {
	cfg, log := su.cfg, su.log
	if cfg.Auth.Mode != config.AuthNone || config.IsLoopback(cfg.Listen) {
		return su.authOpts, false, nil
	}
	if at, asked, err := eng.LocalSignInRequested(); err != nil {
		return nil, false, err
	} else if asked {
		log.Warn("sign-in was asked for from the page: this server starts with built-in accounts (auth.mode local), and asks for the one-time setup code", "asked_at", time.UnixMilli(at).UTC().Format(time.RFC3339))
		cfg.Auth.Mode = config.AuthLocal
		a, err := serverAuth(cfg)
		return a, false, err
	}
	log.Warn("NO SIGN-IN: auth.mode is none on " + cfg.Listen + ", an address others can reach, so anyone who can reach it can start pipelines (which run code on the runners) and approve deployments. This is how it was configured, so nothing was changed. To turn sign-in on: open the page and use \"Set up sign-in now\", or set auth.mode to local or oidc. `launchway doctor` reports this too.")
	return su.authOpts, true, nil
}

// signInFinding is what `launchway doctor` says about sign-in: a warning,
// with the way out, for an installation with no sign-in on an address others
// can reach; empty otherwise.
func signInFinding(cfg *config.Config) []string {
	c := *cfg
	c.ApplyAuthDefault(c.Listen)
	if c.Auth.Mode != config.AuthNone || config.IsLoopback(c.Listen) {
		return nil
	}
	return []string{
		"Sign-in: WARNING no sign-in. auth.mode is none on " + c.Listen + ", an address others can reach: anyone who can reach it can start pipelines (which run code on the runners) and approve deployments.",
		"  Turn it on: open the page and choose \"Set up sign-in now\", or set auth.mode to local (built-in accounts, with a one-time setup code) or oidc. An upgrade never changes this by itself.",
	}
}
