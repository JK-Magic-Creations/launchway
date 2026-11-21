package main

// Linking the storage package is what makes the binary answer git's hooks of
// hosted repositories: they are symlinks to it, and the package registers the
// entry that handles them (see gitexec.RegisterEntry, which main runs first).
import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/launchway"
	_ "github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// currentGitHead is the branch, its ref and the commit of the working
// directory's checkout; each is empty when there is none (or HEAD is detached).
func currentGitHead() (branch, ref, sha string) {
	if out, err := gitexec.Output(context.Background(), gitexec.Spec{Args: []string{"rev-parse", "--abbrev-ref", "HEAD"}}); err == nil {
		if b := strings.TrimSpace(out); b != "HEAD" {
			branch, ref = b, "refs/heads/"+b
		}
	}
	if out, err := gitexec.Output(context.Background(), gitexec.Spec{Args: []string{"rev-parse", "HEAD"}}); err == nil {
		sha = strings.TrimSpace(out)
	}
	return branch, ref, sha
}

// startHosting sets Git hosting up and starts its scheduled maintenance, or
// does nothing when forge.hosting is off.
func startHosting(ctx context.Context, cfg *config.Config, eng *engine.Engine, log *slog.Logger, srv *api.Server) error {
	f := cfg.Forge
	if !f.Hosting {
		return nil
	}
	if err := f.Check(); err != nil {
		return err
	}
	host, _ := os.Hostname()
	if f.MaxPushMiB == 0 {
		f.MaxPushMiB = 2048 // the database blob store's largest object
	}
	sshPrefix := ""
	if f.SSH.On() {
		host := f.SSH.PublicHost
		if host == "" {
			if u, err := url.Parse(f.PublicURL); err == nil {
				host = u.Hostname()
			}
		}
		if host == "" {
			host, _ = os.Hostname()
		}
		if port := f.SSH.Port(); port == 22 {
			sshPrefix = "git@" + host + ":"
		} else {
			sshPrefix = fmt.Sprintf("ssh://git@%s:%d/", host, port)
		}
	}
	h, err := hosting.New(ctx, hosting.Options{
		Engine: eng, Data: cfg.Data, Server: host + fmt.Sprintf("-%d", os.Getpid()), BaseURL: f.PublicURL, Log: log,
		MaxRepoBytes: int64(f.MaxRepoMiB) << 20, MaxPackBytes: int64(f.MaxPushMiB) << 20,
		Provider:    launchway.Options{DefaultHome: f.DefaultOwner, MaxPerHome: f.MaxPerOwner, CreatesPerMinute: f.CreatesPerMinute, SSHPrefix: sshPrefix},
		MirrorAllow: f.MirrorAllow, Every: f.MaintainInterval(), CacheBytes: int64(f.CacheGiB) << 30,
		Hooks: webhooks.Options{OnDisabled: srv.WebhookDisabled}, Secrets: srv.Secrets,
	})
	if err != nil {
		return fmt.Errorf("forge.hosting: %w", err)
	}
	srv.Forge = &api.ForgeHosting{Provider: h.Provider, Mirrors: h.Mirrors, RunTokens: h.RunTokens, AllowInsecureHTTP: f.AllowInsecureHTTP,
		Git:  githttp.New(githttp.Options{Backend: h.Store, MaxPushBytes: int64(f.MaxPushMiB) << 20, MaxConcurrent: f.MaxConcurrent, Log: log}),
		Keys: h.Keys, Rules: h.Rules, Hooks: h.Hooks, Pulls: h.Pulls, RepoByID: h.Forge.Repository}
	srv.WirePulls()
	if f.SSH.On() {
		if err := startSSH(ctx, f, srv, log); err != nil {
			return err
		}
	}
	if warns, err := h.Check(ctx); err == nil {
		for _, w := range warns {
			log.Warn("Git hosting: " + w)
		}
	}
	log.Info("Git hosting is on", "connection", h.Connection.Name)
	h.Start(ctx)
	return nil
}

// startSSH starts the SSH server for Git (forge.ssh.listen) and records where
// it is for the API.
func startSSH(ctx context.Context, f config.ForgeConfig, srv *api.Server, log *slog.Logger) error {
	cas, err := f.SSH.CAs()
	if err != nil {
		return err
	}
	idle, _ := time.ParseDuration(f.SSH.IdleTimeout)
	life, _ := time.ParseDuration(f.SSH.MaxDuration)
	host := f.SSH.PublicHost
	if host == "" {
		if u, err := url.Parse(f.PublicURL); err == nil {
			host = u.Hostname()
		}
	}
	srv.Forge.SSH = api.SSHInfo{Enabled: true, Host: host, Port: f.SSH.Port(), Certificates: len(cas) > 0}
	return srv.StartSSH(ctx, api.SSHOptions{Listen: f.SSH.Listen, UserCAs: cas, MaxConns: f.SSH.MaxConnections, MaxPerAddr: f.SSH.MaxPerAddress,
		IdleTimeout: idle, MaxDuration: life, Ready: func(a net.Addr) { log.Info("Git over SSH is on", "listen", a.String()) }})
}
