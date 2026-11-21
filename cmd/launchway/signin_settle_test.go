package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

func settleSetup(t *testing.T, mode, listen string) (*serverSetup, *engine.Engine) {
	t.Helper()
	cfg := &config.Config{Listen: listen}
	cfg.Auth.Mode = mode
	cfg.Auth.AllowAnonymous = true
	eng, err := engine.Open(engine.Options{Database: filepath.Join(t.TempDir(), "x.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return &serverSetup{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, eng
}

// An explicit "no sign-in" on a reachable address survives an upgrade: it
// starts open, flagged, until the page's request is recorded; only then does
// the next start use built-in accounts. A loopback address is not flagged.
func TestSettleSignInKeepsAnExplicitNoneUntilAsked(t *testing.T) {
	su, eng := settleSetup(t, config.AuthNone, "0.0.0.0:8080")
	a, open, err := su.settleSignIn(eng)
	if err != nil || a != nil || !open || su.cfg.Auth.Mode != config.AuthNone {
		t.Fatalf("an explicit none: %v %v %v mode %s", a, open, err, su.cfg.Auth.Mode)
	}
	if err := eng.RequestLocalSignIn(1); err != nil {
		t.Fatal(err)
	}
	a, open, err = su.settleSignIn(eng)
	if err != nil || a == nil || a.Local == nil || open || su.cfg.Auth.Mode != config.AuthLocal {
		t.Fatalf("after the request: %v %v %v mode %s", a, open, err, su.cfg.Auth.Mode)
	}
	su2, eng2 := settleSetup(t, config.AuthNone, "127.0.0.1:8080")
	if a, open, err := su2.settleSignIn(eng2); err != nil || a != nil || open {
		t.Fatalf("loopback: %v %v %v", a, open, err)
	}
	su3, eng3 := settleSetup(t, config.AuthOIDC, "0.0.0.0:8080")
	su3.authOpts = nil
	if a, open, err := su3.settleSignIn(eng3); err != nil || open || a != nil {
		t.Fatalf("oidc is not touched: %v %v %v", a, open, err)
	}
}

func TestDoctorFlagsNoSignIn(t *testing.T) {
	cfg := &config.Config{Listen: "0.0.0.0:8080"}
	cfg.Auth.Mode = config.AuthNone
	f := signInFinding(cfg)
	if len(f) != 2 || !strings.Contains(f[0], "WARNING no sign-in") || !strings.Contains(f[1], "Set up sign-in now") {
		t.Fatalf("%v", f)
	}
	cfg.Listen = "127.0.0.1:8080"
	if len(signInFinding(cfg)) != 0 {
		t.Fatal("loopback flagged")
	}
	cfg.Listen, cfg.Auth.Mode = "0.0.0.0:8080", config.AuthOIDC
	if len(signInFinding(cfg)) != 0 {
		t.Fatal("oidc flagged")
	}
}
