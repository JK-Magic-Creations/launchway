package main

import (
	"flag"
	"os"

	"context"
	"errors"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/config"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/installer"
	"github.com/jk-magic-creations/launchway/internal/securitycheck"
)

var errBundleComing = errors.New("the offline bundle is not built yet (the design is in the install guide, section Offline installs). Until then, on a network without internet, mirror the image and the chart into your own registry and use `launchway install --answers` with kubernetes.image set to your mirror")

// installDoctor prints the plain security checklist at the end of an install.
func installDoctor(ctx context.Context, w io.Writer, in installer.DoctorInput) {
	printChecklist(w, securitycheck.Check(factsFromInstall(ctx, in)))
}

func factsFromInstall(ctx context.Context, in installer.DoctorInput) securitycheck.Facts {
	f := securitycheck.Facts{Kubernetes: in.Kubernetes}
	switch in.Facts["sign_in"] {
	case "accounts":
		f.SignIn, f.SignInKind = securitycheck.Yes, "built-in accounts"
	case "none":
		f.SignIn, f.SignInKind = securitycheck.No, "none"
	}
	switch in.Facts["tls"] {
	case "yes":
		f.TLS = securitycheck.Yes
	case "loopback":
		f.TLS = securitycheck.Yes
	case "no":
		f.TLS = securitycheck.No
	}
	switch in.Facts["proxy"] {
	case "named":
		f.ProxyNamed = securitycheck.Yes
	case "any-peer":
		f.ProxyNamed = securitycheck.No
	}
	if in.Facts["loopback"] == "yes" || in.Facts["tls"] == "loopback" {
		f.Loopback = securitycheck.Yes
		f.PublicExposure = securitycheck.Yes
	}
	switch in.Facts["network_policies"] {
	case "enforced":
		f.NetworkPolicies = securitycheck.Yes
	case "not-enforced":
		f.NetworkPolicies = securitycheck.No
	}
	// The sign-in claim is checked against the server when it answers: an unauthenticated read must be refused.
	if in.URL != "" {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(in.URL, "/")+"/api/v1/projects", nil)
		if r, err := (&http.Client{Timeout: 5 * time.Second}).Do(req); err == nil {
			r.Body.Close()
			switch r.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				f.SignIn = securitycheck.Yes
			case http.StatusOK:
				f.SignIn = securitycheck.No
			}
		}
	}
	return f
}

func printChecklist(w io.Writer, items []securitycheck.Item) {
	mark := map[string]string{"ok": "[ok]       ", "attention": "[ATTENTION]", "unknown": "[?]        "}
	for _, it := range items {
		fmt.Fprintf(w, "  %s %s\n               %s\n", mark[it.State], it.Title, it.Detail)
	}
	if n := securitycheck.Attention(items); n > 0 {
		fmt.Fprintf(w, "  %d thing(s) need attention. Running `launchway doctor security` shows this list again.\n", n)
	}
}

// doctorSecurity is `launchway doctor security`: the checklist, from the configuration and, when an
// address is given, from asking the server what it refuses to strangers.
func doctorSecurity(args []string) error {
	fs := flag.NewFlagSet("doctor security", flag.ContinueOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	url := fs.String("url", "", "the address people use (default: from listen)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // the flag set has printed its help
		}
		return usageErr("%v", err)
	}
	in := installer.DoctorInput{URL: *url, Facts: map[string]string{}}
	if cfg, err := config.LoadBootstrap(*cfgPath); err == nil {
		cfg.ApplyAuthDefault(cfg.Listen)
		if config.IsLoopback(cfg.Listen) {
			in.Facts["loopback"] = "yes"
		}
		if r, _ := cfg.Server.TrustedProxyPrefixes(); len(r) > 0 {
			in.Facts["proxy"] = "named"
		} else if cfg.Server.TrustProxy {
			in.Facts["proxy"] = "any-peer"
		}
		switch cfg.Auth.Mode {
		case config.AuthNone:
			in.Facts["sign_in"] = "none"
		default:
			in.Facts["sign_in"] = "accounts"
		}
		if in.URL == "" {
			in.URL = "http://" + strings.Replace(cfg.Listen, "0.0.0.0", "127.0.0.1", 1)
		}
	} else if *url == "" {
		return fmt.Errorf("give --url ADDRESS, or a configuration file with --config: %w", err)
	}
	if strings.HasPrefix(in.URL, "https://") {
		in.Facts["tls"] = "yes"
	} else if in.Facts["loopback"] != "yes" {
		in.Facts["tls"] = "no"
	}
	printChecklist(os.Stdout, securitycheck.Check(factsFromInstall(context.Background(), in)))
	return nil
}
