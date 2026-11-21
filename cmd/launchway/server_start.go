package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// serverSetup is what a starting server reads before it opens the database:
// the bootstrap file and what is derived from it.
type serverSetup struct {
	cfg           *config.Config
	cfgPath       string
	ignoreDB      bool
	secretStores  *secrets.Resolver
	drain         time.Duration
	dbWait        time.Duration
	stall         time.Duration
	authOpts      *api.Auth
	log           *slog.Logger
	reportLimits  executor.CollectLimits
	reportsOrigin string
	live          engine.LiveOptions
	fromDatabase  func([]byte) (engine.ArtifactOptions, error)
}

// readServerSetup reads the flags and the bootstrap file and checks them.
func readServerSetup(args []string) (*serverSetup, error) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	listen := fs.String("listen", "", "address to listen on (overrides the config)")
	data := fs.String("data", "", "data directory (overrides the config)")
	ignoreDB := fs.Bool("ignore-database-config", os.Getenv("LAUNCHWAY_IGNORE_DATABASE_CONFIG") != "",
		"start with the bootstrap file only and ignore the configuration stored in the database (recovery: see the runbook)")
	_ = fs.Parse(args) // ExitOnError: it exits itself

	su := &serverSetup{cfgPath: *cfgPath, ignoreDB: *ignoreDB}
	var err error
	if su.cfg, err = config.LoadBootstrap(*cfgPath); err != nil {
		return nil, err
	}
	cfg := su.cfg
	if su.secretStores, err = secrets.Open(cfg.Runner.Secrets); err != nil {
		return nil, fmt.Errorf("%s: runner.secrets: %w", *cfgPath, err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *data != "" {
		cfg.Data = *data
	}
	if err := cfg.CheckExposure(cfg.Listen); err != nil {
		return nil, err
	}
	if su.dbWait, su.drain, err = cfg.Timings(); err != nil {
		return nil, fmt.Errorf("%s: %w", *cfgPath, err)
	}
	if su.stall, err = cfg.StallBudget(); err != nil {
		return nil, fmt.Errorf("%s: %w", *cfgPath, err)
	}
	if su.authOpts, err = serverAuth(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", *cfgPath, err)
	}
	if err := os.MkdirAll(cfg.Data, 0o750); err != nil {
		return nil, err
	}
	su.log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(su.log)
	if su.reportLimits, err = cfg.Runner.Reports.Limits(); err != nil {
		return nil, fmt.Errorf("%s: runner.%w", *cfgPath, err)
	}
	artifactOpts, findingLimits, err := artifactSettings(cfg, su.log)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", *cfgPath, err)
	}
	if su.reportsOrigin, err = cfg.ReportsOrigin(); err != nil {
		return nil, fmt.Errorf("%s: %w", *cfgPath, err)
	}
	// The engine opens with the file's settings; those kept in the
	// database replace them later, before anything is served.
	if su.live, err = cfg.EngineLive(); err != nil {
		return nil, fmt.Errorf("%s: %w", *cfgPath, err)
	}
	su.live.Artifacts = artifactOpts // the limits, and where artifacts are kept
	su.live.Findings = findingLimits
	if !su.ignoreDB {
		// Once the database holds the installation's settings, it, not the
		// file, says where artifacts are kept.
		su.fromDatabase = func(spec []byte) (engine.ArtifactOptions, error) {
			c, err := settings.ArtifactsFromInstallation(cfg, spec)
			if err != nil {
				return engine.ArtifactOptions{}, err
			}
			o, _, err := artifactSettings(c, su.log)
			return o, err
		}
	}
	return su, nil
}

// openEngine opens the database with the file's settings.
func (su *serverSetup) openEngine() (*engine.Engine, error) {
	e, err := su.openEngineRaw()
	if err == nil {
		e.SetStallBudget(su.stall)
	}
	return e, err
}

func (su *serverSetup) openEngineRaw() (*engine.Engine, error) {
	live := su.live
	return engine.Open(engine.Options{Database: su.cfg.DatabaseAddr(), Logger: su.log, AllowSelfApproval: live.AllowSelfApproval, ConnectWait: su.dbWait,
		ArtifactsFromDatabase: su.fromDatabase, Tests: live.Tests, Environments: live.Environments, Artifacts: live.Artifacts, Findings: live.Findings,
		Policy: live.Policy, Reports: live.Reports, HealthSources: live.HealthSources, Models: live.Models, RunnerPools: live.RunnerPools, Queues: live.Queues})
}

// startSettings imports the file's settings into the database the first time
// a server of schema 23 starts, says which of the file's settings are
// ignored since, and returns the configuration in force: the file's
// bootstrap settings and the database's.
func startSettings(store *settings.Service, log *slog.Logger) (*config.Config, error) {
	res, err := store.ImportBootstrap()
	if err != nil {
		return nil, err
	}
	when := time.UnixMilli(res.Import.At).UTC().Format(time.RFC3339)
	switch {
	case res.Done && len(res.Ignored) > 0:
		log.Info("imported the configuration file's settings into the database; from now on they are changed in the web UI, the API, the MCP server or with launchway config, and the file's copies are ignored",
			"import", res.Import.ID, "keys", strings.Join(res.Ignored, ", "))
	case res.Done:
		log.Info("the configuration is kept in the database; the file holds only how to start", "import", res.Import.ID)
	case len(res.Ignored) > 0:
		for _, k := range res.Ignored {
			log.Warn("the configuration file sets a key that is kept in the database since it was imported; the file's value is ignored. Change it in the web UI, the API or with launchway config, or import the file again with launchway config import",
				"key", k, "import", res.Import.ID, "imported_at", when)
		}
	}
	// A stored configuration that cannot be used starts safe mode (the last
	// one that worked, or the file alone), not an exit with no way back.
	return store.StartConfig()
}

// storeApplier puts a stored configuration into effect on this server.
// Applying is safe to repeat: everything that can fail is worked out first,
// then each part is replaced as a whole, so a try that stops half way is
// finished by the next one (the service tries again at every poll).
func storeApplier(eng *engine.Engine, mgr *source.Manager, srv *api.Server, token string, authOpts *api.Auth) settings.Applier {
	return func(c *config.Config, _ *settings.Layout) error {
		l, err := c.EngineLive()
		if err != nil {
			return err
		}
		sl, err := serverLive(c, token, authOpts)
		if err != nil {
			return err
		}
		ps, err := c.SourceProjects()
		if err != nil {
			return err
		}
		libs := c.SourceLibraries()
		n, _ := c.LoopLimit()
		if err := eng.Reconfigure(l); err != nil {
			return err
		}
		if err := mgr.Reconfigure(ps, libs); err != nil {
			return err
		}
		mgr.SetLoopLimit(n)
		srv.Reconfigure(sl)
		return nil
	}
}

// chooseConfig decides what configuration the server starts with: the
// stored one (or safe mode's), the file alone when asked to ignore the
// database, or the file alone on a build limited to an older schema. It
// returns the store to use (nil when ignored).
func chooseConfig(store *settings.Service, cfg *config.Config, cfgPath string, ignoreDB bool, log *slog.Logger) (*config.Config, *settings.Service, error) {
	switch {
	case ignoreDB:
		log.Warn("RECOVERY: --ignore-database-config: the configuration stored in the database is ignored and not changed; this server runs with the bootstrap file only, and the configuration API is off. Restart without the flag when the configuration is repaired")
		cfg.Auth.SetAccessInDatabase(false)
		return cfg, nil, cfg.Validate(cfgPath)
	case store.Available():
		eff, err := startSettings(store, log)
		return eff, store, err
	}
	// A build limited to an older schema: the file is the whole
	// configuration, checked in full.
	cfg.Auth.SetAccessInDatabase(false)
	return cfg, store, cfg.Validate(cfgPath)
}

// setupAI gives the server a model client: it calls models only to explain a
// failed step, with keys from its own stores (ai.secrets), if it has any.
// With models in the database, any may be added while the server runs. The
// address is an administrator's, but still never an internal one unless the
// allowed hosts say so (internal/egress).
func setupAI(srv *api.Server, cfg *config.Config, cfgPath string) error {
	srv.AI = &ai.Client{HTTP: egress.Default.HTTPClient(16 * time.Minute)}
	if len(cfg.AI.Secrets) > 0 {
		var err error
		if srv.AISecrets, err = secrets.Open(cfg.AI.Secrets); err != nil {
			return fmt.Errorf("%s: ai.secrets: %w", cfgPath, err)
		}
	}
	return nil
}

// warmUpSignIn finds the identity provider now, so a mistake shows at once;
// sign-in tries again later if it is unreachable.
func warmUpSignIn(authOpts *api.Auth, log *slog.Logger) {
	if authOpts == nil {
		return
	}
	dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := authOpts.OIDC.Discover(dctx); err != nil {
		log.Warn("the sign-in provider cannot be reached yet; sign-in will keep trying", "err", err)
	}
}

// startBuiltinRunner starts the runner built into the server, if the
// configuration has one. The returned channel closes when it has stopped.
func startBuiltinRunner(runnerCtx, ctx context.Context, cfg *config.Config, ln net.Listener, url, token string, log *slog.Logger,
	secretStores *secrets.Resolver, reportLimits executor.CollectLimits) (chan struct{}, error) {
	done := make(chan struct{})
	if !cfg.RunnerEnabled() {
		close(done)
		return done, nil
	}
	host, _ := os.Hostname()
	// Unique per server: two servers on one machine must not share an identity.
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	runnerID := "builtin-" + host + "-" + port
	ex, err := executor.ByName(cfg.Runner.Executor, executor.Options{Kubernetes: cfg.Runner.Kubernetes, RunnerID: runnerID})
	if err != nil {
		return nil, err
	}
	if err := checkExecutor(ctx, ex); err != nil {
		return nil, err
	}
	r := &runner.Runner{
		Client: runner.NewClient(url, token), Exec: ex,
		Info:    engine.RunnerInfo{ID: runnerID, Name: host + " (built in)", Executor: ex.Name(), Version: version},
		DataDir: filepath.Join(cfg.Data, "runner"), Concurrency: cfg.Runner.Concurrency, Log: log,
		Secrets: secretStores, Reports: reportLimits, Images: cfg.Runner.Images,
		SpoolMaxBytes: cfg.Runner.Spool.Bytes(), SpoolMaxAge: cfg.Runner.Spool.Age(),
	}
	if cfg.Runner.AIEnabled() {
		r.AI = &ai.Client{}
	}
	go func() {
		defer close(done)
		if err := r.Run(runnerCtx); err != nil && runnerCtx.Err() == nil {
			log.Error("the built-in runner stopped", "err", err)
		}
	}()
	return done, nil
}
