// Command launchway is the whole product in one binary: the server, the
// runner, and a local runner for trying pipelines on your own machine.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/logsafe"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/source"
	standardsservice "github.com/jk-magic-creations/launchway/internal/standards/service"
	"github.com/jk-magic-creations/launchway/web"
)

var version = "dev"

var usage = themed(`Launchway: build and deliver software.

Usage:
  launchway server   [--config launchway.yaml]   run the server, its web UI and a built-in {runner}
  launchway runner   --server URL --join J      enrol a {runner} with a join token, then take work
                     [--config runner.yaml] [--health-listen ADDR] [--labels a,b]
  launchway runner   --server URL --token T     run a {runner} with the shared token (no identity)
  launchway run      [--file F] [--executor E]  run this directory's pipeline on this machine
                     [-i name=value ...]
  launchway validate [file] [--project P]       check a pipeline file (with --project, the server checks it for that Flight too)
  launchway init     [--write] [--force]         propose the pipeline file for this repository from what it holds
  launchway import   pipeline [--from FORMAT|auto] [--dir .] [--write] [--report FILE]  turn the pipeline files of another system into a Launchway pipeline, with a report of what needs a person
  launchway graph    [file]                     print a pipeline's graph as JSON; or ask the software graph (launchway graph impact|order|where|needs|spec ...)
  launchway login    --server URL               store an API token for a server
  launchway runs     start|list|show|logs|cancel|lanes|resume|rerun  start, follow and re-run {runs}
                                                (launchway runs, for more)
  launchway service-accounts list|create|update|delete|token  non-person accounts that own tokens for automation
  launchway account show|sessions|revoke|profile             your account in one answer, your signed-in browsers, a person's public profile
  launchway approvals list|approve|reject       approvals waiting for a decision, and deciding them
  launchway notifications [--limit N]           the log of notifications sent, retried or failed
  launchway deployments options|rollback|review  send a service back after a deployment succeeded ({rollback}); review {emergency_rollbacks}
  launchway release list|show|next|create|publish|promote|...  releases: version, notes, assets, promotion across Airfields, freezes, the report
  launchway rollout  status|tree|manifest|logs|pause|resume|restart|scale|promote|abort ...  look into a delivery and steer it
  launchway templates list [library]            the shared libraries' templates and their parameters
  launchway policy   list|run                   the policies in force; a run's decisions
  launchway waivers  list|grant|revoke          waive a policy's rule for a while, with a reason
  launchway blobs    status|copy                see and move where artifacts are kept
                                                (launchway blobs, for more)
  launchway mcp      [--server URL]             the server's MCP tools for agents, on stdin and stdout
  launchway verifications list|show|mark        verifications on a server
                                                (launchway verifications, for more)
  launchway health-sources | models             what verify and ai steps may name
  launchway runners  list|join-token|revoke|rotate  {runners}, and join tokens to enrol them
  launchway step-resources get|set-limits|set-default  the default CPU and memory of steps, and their limits
  launchway encryption init|status|rotate|rewrap     the root key and encryption at rest (launchway encryption, for more)
  launchway config   list|get|set|create|delete|restore|history|export|import|apply ...
                                                configuration kept in the database (launchway config, for more)
  launchway projects|libraries|environments|policies|runner-pools|role-bindings|policy-sources|units|secret-stores ...
                                                the same commands for one kind of object
  launchway run-limits get|set                         the longest run timeout a pipeline may ask for
  launchway debrief  show|summary|export <run-id>   the report of a run's tests: read it, or export it as html, pdf, json or junit
  launchway insights [--flight F|--squadron S]    {run} success, duration, holding pattern time, slowest and flakiest steps, tests, delivery measures
  launchway tests    flaky|timings|split <Flight>   flaky tests, typical durations, and a balanced split of tests for N shards
  launchway caches   list|delete|policy         build caches: what is kept, deleting, how much is kept
  launchway registry status|list|tags|inspect|delete|retention|policy|gc   the built-in image and chart registry
  launchway toggles list|get|create|set|rules|history|revert|keys|safeguards|plan|apply  feature toggles: roll a feature out to a share of users without a deployment
  launchway events   list|show|replay            what reached the server and what was decided about it; start one again
  launchway event-sources list|create|change|delete|test   incoming webhook sources: a signed address that starts a run
  launchway squadrons list|create|show|candidates|add-flight|move|organize   Squadrons (folders of Flights): make, fill, move, organize
  launchway run-triggers list|create|delete|allow|allowed|revoke   start a run when another Flight's run or launch ends
  launchway standards show|set|explain|coverage|profile  the Standing orders every repository has: checks, gates, settings by level
  launchway findings list|show|accept|reopen|export|inspect  what the checks found: the backlog, decisions, a run's verdict
  launchway state    list|show|versions|lock|unlock|pull|push|rollback   built-in Terraform/OpenTofu state
  launchway configuration overview|report|rerun-failed   Ansible and plan reports, drift, hosts to run again
  launchway variables list|effective|set|delete|lock|unlock  variables set at a level; the nearest setting wins
  launchway users    list|setup-code|reset NAME|unlock NAME   built-in accounts, from the server's console
  launchway install  [--answers FILE] [--dry-run] [--uninstall]   the guided installer: this machine or a Kubernetes cluster
  launchway doctor   [database|ssh|security]             would this installation survive losing its database? is Git over SSH reachable?
  launchway repos    list|get|create|update|delete|deploy-keys|hooks|protected-branches|protected-tags|download|settings   hosted Git repositories (installation setting forge.hosting)
  launchway repos    connection|import|import-all|verify|mirror  bring repositories in from other servers, keep them in step
  launchway pr       list|view|create|checkout|diff|comment|review|merge|close|reopen|edit|revert|apply|settings|rules   pull requests and code review on hosted repositories
  launchway fleet    probe|run                  deliver a deploy step's fleet: block to hosts over SSH, from a Station
  launchway deps     list|check|update|ignore   what a repository depends on, what is newer or vulnerable
  launchway updates  preview|run|settings   Launchway's own dependency updates, proposed as pull requests
  launchway work     items|board|sprints|milestones|agents ...   work tracking: items, boards, sprints, roadmap, agents
                                                (launchway work, for more)
  launchway keys     list|add|delete|host             your SSH keys for Git over SSH, and the server's host key fingerprints
  launchway signing  keys|verify|installation-key     your signing keys, and whether commits and tags are verified
  launchway inbox    list|count|read|done             your one inbox: notices, work entries, pull requests waiting on you
  launchway notices  prefs|set|watch|quiet|digest|mail how and when you are told, by e-mail or in the inbox
  launchway attention [--json]                          what needs you: the Tower's rule, for a script or an agent
  launchway organize list|folder|move|label|export|import     folders, labels and layouts shared by level
  launchway recent [--limit N] [--clear|--pause|--resume]   what you opened and did lately (yours alone)
  launchway steps    list|get                   the custom steps of the server's libraries: what each takes, gives and may do, and who uses it
  launchway step     new|test|validate          custom steps: write one in Go, Python, TypeScript or shell, run it as a Station would, check it
  launchway version

The pipeline file defaults to .launchway/pipeline.yaml. runner and run take
--secrets-config FILE, the secret stores they may read.
`)

// commands take their arguments and nothing else, by name (registry.go adds registry).
var commands = map[string]func([]string) error{
	"runs":             cmdRuns,
	"debrief":          cmdDebrief,
	"tests":            cmdTests,
	"attention":        cmdAttention,
	"recent":           cmdRecent,
	"organize":         cmdOrganize,
	"approvals":        cmdApprovals,
	"notifications":    cmdNotifications,
	"deployments":      cmdDeployments,
	"release":          cmdRelease,
	"rollout":          cmdRollout,
	"templates":        cmdTemplates,
	"import":           cmdImport,
	"policy":           cmdPolicy,
	"waivers":          cmdWaivers,
	"mcp":              cmdMCP,
	"verifications":    cmdVerifications,
	"health-sources":   cmdHealthSources,
	"models":           cmdModels,
	"runners":          cmdRunners,
	"step-resources":   cmdStepResources,
	"config":           cmdConfig,
	"run-limits":       cmdRunLimits,
	"caches":           cmdCaches,
	"events":           cmdEvents,
	"event-sources":    cmdEventSources,
	"run-triggers":     cmdRunTriggers,
	"squadrons":        cmdSquadrons,
	"units":            cmdSquadrons,
	"standards":        cmdStandards,
	"findings":         cmdFindings,
	"state":            cmdState,
	"configuration":    cmdConfiguration,
	"variables":        cmdVariables,
	"toggles":          cmdToggles,
	"doctor":           cmdDoctor,
	"users":            cmdUsers,
	"service-accounts": cmdServiceAccounts,
	"bases":            cmdBases,
	"install":          cmdInstall,
	"work":             cmdWork,
	"step":             cmdStep,
	"steps":            cmdSteps,
	"account":          cmdAccount,
}

func main() {
	gitexec.Reexec()
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
	// -h or --help on any command prints its help and exits 0, signed in or not: help must not need a server.
	helping := prepareHelp(os.Args[1], args)
	if run, ok := commands[os.Args[1]]; ok {
		if err = run(args); err != nil {
			exitAfter(err, helping)
		}
		return
	}
	switch os.Args[1] {
	case "server":
		err = cmdServer(args)
	case "runner":
		err = cmdRunner(args)
	case "run":
		err = cmdRun(args)
	case "validate":
		err = cmdValidate(args)
	case "init":
		err = cmdInit(args)
	case "graph":
		if len(args) > 0 && graphSubcommands[args[0]] {
			err = cmdSoftwareGraph(args)
		} else {
			err = cmdGraph(args)
		}
	case "login":
		err = cmdLogin(args)
	case "blobs":
		err = cmdBlobs(args)
	case "projects", "libraries", "environments", "policies", "runner-pools", "role-bindings", "policy-sources", "units", "secret-stores":
		err = cmdKindAlias(os.Args[1])(args)
	case "repos", "keys", "pr", "encryption":
		err = cmdHosting(os.Args[1], args)
	case "signing", "inbox", "notices":
		err = cmdPeople(os.Args[1], args)
	case "fleet":
		err = cmdFleet(args)
	case "deps":
		err = cmdDeps(args)
	case "updates":
		err = cmdUpdates(args)
	case "self-check": // not listed in the help: the image check runs it
		err = cmdSelfCheck(args)
	case "version", "--version", "-v":
		fmt.Println("launchway", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		os.Exit(unknownCommand(os.Args[1]))
	}
	if err != nil {
		exitAfter(err, helping)
	}
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// ---- server ----

func cmdServer(args []string) error {
	su, err := readServerSetup(args)
	if err != nil {
		return err
	}
	cfg, cfgPath, log, live := su.cfg, su.cfgPath, su.log, su.live
	eng, err := su.openEngine()
	if err != nil {
		return err
	}
	defer eng.Close()
	logStore(eng, log)
	authOpts, openInstall, err := su.settleSignIn(eng)
	if err != nil {
		return err
	}

	// Everything but the bootstrap settings is kept in the database
	// (schema 23): the file's copies are imported once, then ignored.
	store := settings.New(eng, cfg, log)
	eff, store, err := chooseConfig(store, cfg, cfgPath, su.ignoreDB, log)
	if err != nil {
		return err
	}
	if err := egress.Default.Set(settings.EgressPolicy(eff)); err != nil {
		return fmt.Errorf("%s: egress: %w", cfgPath, err)
	}
	if eff != cfg {
		l, err := eff.EngineLive()
		if err != nil {
			return err
		}
		if err := eng.Reconfigure(l); err != nil {
			return err
		}
	}
	projects, err := eff.SourceProjects()
	if err != nil {
		return err
	}
	mgr, err := source.NewManagerWithLibraries(eng, cfg.Data, projects, eff.SourceLibraries(), log)
	if err == nil {
		loopLimit, _ := eff.LoopLimit() // checked when the configuration was read
		mgr.SetLoopLimit(loopLimit)
	}
	if err != nil {
		return err
	}
	logChartNotice(log)
	secretsSvc, token, err := su.secretsAndToken(eng)
	if err != nil {
		return err
	}
	api.SetRootKeySource(guideRootKey{su.rootHolder}) // the setup guide's guarded recovery copy reads the unlocked holder
	forge.SetSealedReader(func(name string) (string, error) { return secretsSvc.ConnectionToken(context.Background(), name) })
	eng.SetTOTPBox(secretsSvc.TOTP()) // authenticator secrets of built-in accounts are stored sealed
	srv := &api.Server{Engine: eng, Projects: mgr, RunnerToken: token, Secrets: secretsSvc, Actor: cfg.User, Auth: authOpts,
		TrustProxy: cfg.Server.TrustProxy, UI: web.FS(), Version: version, Log: log, ReportsOrigin: su.reportsOrigin, OpenInstall: openInstall}
	setDebrief(srv, cfg, log)
	if store.Available() {
		srv.Settings = store
	}
	// Variables set at levels (schema 29): the manager binds pipelines to them, the server serves and changes them.
	srv.Variables = store.Variables()
	mgr.SetVariables(srv.Variables)
	// Standing orders (schema 45): the manager adds the Inspection to every
	// run it makes, the server serves the settings and the findings.
	stds := &standardsservice.Service{Eng: eng, Layout: store.LayoutOrFile}
	mgr.SetInspector(stds)
	srv.Standards = stds
	wireStarterRegistry(mgr, srv, eff)
	applyLive := func(c *config.Config) error {
		l, err := serverLive(c, token, authOpts)
		if err != nil {
			return err
		}
		srv.Reconfigure(l)
		return nil
	}
	if err := applyLive(eff); err != nil {
		return fmt.Errorf("%s: %w", cfgPath, err)
	}
	models := live.Models
	if eff != cfg {
		models, _ = eff.Models()
	}
	if models != nil || store.Available() {
		if err := setupAI(srv, cfg, cfgPath); err != nil {
			return err
		}
	}
	warmUpSignIn(authOpts, log)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := startServices(ctx, eff, cfg, eng, log, srv); err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()
	mgr.Start(ctx)
	mgr.WatchPolicySources(ctx)
	startNotifications(ctx, eng, srv, eff, log)
	if store.Available() {
		// Changes made through any server take effect here within seconds.
		// Applying is safe to repeat: everything that can fail is worked out
		// first, then each part is replaced as a whole, so a try that stops
		// half way is finished by the next one (the service tries again at
		// every poll until it succeeds).
		if err := store.Load(storeApplier(eng, mgr, srv, token, authOpts)); err != nil {
			return err
		}
		go store.Watch(ctx)
	}

	url := "http://" + loopback(ln.Addr().String())
	runnerCtx, stopRunner := context.WithCancel(context.Background())
	defer stopRunner()
	runnerDone, err := startBuiltinRunner(runnerCtx, ctx, cfg, ln, url, token, log, su.secretStores, su.reportLimits)
	if err != nil {
		return err
	}

	tokenAt := config.TokenFile(cfg.Data)
	if os.Getenv("LAUNCHWAY_RUNNER_TOKEN") != "" {
		tokenAt = "LAUNCHWAY_RUNNER_TOKEN"
	}
	signIn := signInBanner(cfg, authOpts != nil)
	fmt.Fprintf(os.Stderr, "\n  Launchway %s is listening on %s (%s)\n  %d Flight(s), data in %s, %s\n  Station token for other machines: %s\n\n",
		version, listening(ln.Addr()), signIn, len(projects), cfg.Data, describeDatabase(cfg.DatabaseAddr()), tokenAt)

	<-ctx.Done()
	fmt.Fprintln(os.Stderr, "\nStopping. Steps in progress will run again when Launchway starts.")
	return shutdown(httpSrv, srv, su.drain, func() {
		stopRunner()
		select {
		case <-runnerDone:
		case <-time.After(30 * time.Second):
		}
	})
}

// shutdownTimeout bounds how long a stopping server waits for requests in flight.
const shutdownTimeout = 15 * time.Second

// shutdown stops serving gracefully. At once, /readyz reports that the
// server is not ready, and each connection closes after its current request
// so that clients reconnect, to another server if there is one. The server
// keeps serving for drain, so that load balancers checking /readyz stop
// sending it requests first. Then the built-in runner is stopped, requests
// that wait for a long time (runners waiting for work, live streams) are
// ended, and the other requests in flight get up to shutdownTimeout.
func shutdown(hs *http.Server, srv *api.Server, drain time.Duration, stopRunner func()) error {
	srv.Drain()
	hs.SetKeepAlivesEnabled(false)
	time.Sleep(drain)
	stopRunner()
	srv.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := hs.Shutdown(ctx); err != nil {
		return fmt.Errorf("requests still in flight after %s were cut off: %w", shutdownTimeout, err)
	}
	return nil
}

// serverLive makes the API server's live settings from a configuration.
func serverLive(c *config.Config, runnerToken string, auth *api.Auth) (api.Live, error) {
	var l api.Live
	var err error
	signIn := auth != nil
	if m := c.MCP; m.On() {
		l.MCP = &api.MCPOptions{AllowedOrigins: m.AllowedOrigins, RequestsPerMinute: m.RequestsPerMinute, Concurrent: m.Concurrent, Total: m.Total}
		if o := m.OAuth; o != nil {
			l.MCP.OAuth = &api.MCPOAuth{Resource: o.Resource, Scopes: o.Scopes}
			if err := api.CheckMCPOAuth(l.MCP.OAuth, auth); err != nil {
				return l, err
			}
		}
	}
	if signIn {
		if l.Policy, err = c.Auth.Policy(); err != nil {
			return l, err
		}
	}
	if l.Drafts, err = c.DraftRules(); err != nil {
		return l, err
	}
	waive, err := c.WaiverRole()
	if err != nil {
		return l, err
	}
	runners, err := c.RunnerAdminRole()
	if err != nil {
		return l, err
	}
	l.WaiveRole, l.RunnersRole = &waive, &runners
	if l.TokenMaxDays, err = c.TokenMaxDays(); err != nil {
		return l, err
	}
	pools, err := c.Pools(runnerToken)
	if err != nil {
		return l, err
	}
	for _, p := range pools {
		if p.Kind == engine.PoolDeploy {
			l.RunnerPools = append(l.RunnerPools, api.RunnerPool{Name: p.Name, Token: p.Token, Targets: p.Targets, RequireIdentity: p.RequireIdentity})
		}
	}
	return l, nil
}

// serverAuth builds the sign-in settings, or returns nil if sign-in is off.
func serverAuth(cfg *config.Config) (*api.Auth, error) {
	if cfg.Auth.Mode != config.AuthOIDC && cfg.Auth.Mode != config.AuthLocal {
		return nil, nil
	}
	policy, err := cfg.Auth.Policy()
	if err != nil {
		return nil, err
	}
	idle, lifetime, err := cfg.Auth.Sessions()
	if err != nil {
		return nil, err
	}
	out := &api.Auth{Policy: policy, IdleTimeout: idle, Lifetime: lifetime}
	if cfg.Auth.LocalAccounts() {
		ttl := time.Duration(0)
		if cfg.Auth.Local.SetupCodeTTL != "" {
			if ttl, err = time.ParseDuration(cfg.Auth.Local.SetupCodeTTL); err != nil || ttl < time.Minute {
				return nil, fmt.Errorf("auth.local.setup_code_ttl %q must be a duration of at least 1m, such as 24h", cfg.Auth.Local.SetupCodeTTL)
			}
		}
		out.Local = &api.LocalAuth{Required: cfg.Auth.Mode == config.AuthLocal, SecondFactor: cfg.Auth.Local.SecondFactorRule(),
			SetupCodeFile: nonEmptyStr(os.Getenv("LAUNCHWAY_SETUP_CODE_FILE"), cfg.Auth.Local.SetupCodeFile), SetupTTL: ttl}
	}
	if cfg.Auth.Mode == config.AuthLocal {
		return out, nil
	}
	secret, err := cfg.Auth.OIDC.ClientSecret()
	if err != nil {
		return nil, err
	}
	o := cfg.Auth.OIDC
	out.OIDC = auth.NewOIDC(auth.OIDCOptions{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: secret,
		RedirectURL: o.RedirectURL, Scopes: o.Scopes, UsernameClaim: o.UsernameClaim, GroupsClaim: o.GroupsClaim})
	out.SecureCookies = strings.HasPrefix(o.RedirectURL, "https://")
	return out, nil
}

// runnerOptions is what `launchway runner` runs with.
type runnerOptions struct {
	config.RunnerFile
	Token        string
	Join         string // a join token, to enrol with
	HealthListen string
	// MetricsListen is where the Station serves metrics (--metrics-listen); empty: nowhere.
	MetricsListen string
}

// runnerArgs reads the runner's settings: a flag given on the command line
// wins, then the environment (LAUNCHWAY_SERVER, LAUNCHWAY_RUNNER_TOKEN), then
// the --config file, then the defaults.
func runnerArgs(args []string) (*runnerOptions, error) {
	fs := flag.NewFlagSet("runner", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "YAML file of Station settings, the same as runner: in the server's configuration plus server, name and data")
	server := fs.String("server", "", "server URL (or LAUNCHWAY_SERVER)")
	token := fs.String("token", "", "the shared Station token or a deploy pool's (or LAUNCHWAY_RUNNER_TOKEN)")
	join := fs.String("join", "", "a join token, to enrol this Station once and get an identity of its own (or LAUNCHWAY_JOIN)")
	labels := fs.String("labels", "", "labels this Station has, separated by commas, such as arm64,gpu")
	name := fs.String("name", "", "a name for this Station (default: the host name)")
	data := fs.String("data", "", "directory for workspaces (default launchway-runner)")
	conc := fs.Int("concurrency", 0, "steps to run at once (default 2)")
	exe := fs.String("executor", "", "auto (the default), container, podman, docker, host or kubernetes")
	k8s := k8sFlags(fs)
	secretsConfig := secretsFlag(fs)
	health := fs.String("health-listen", "", "address to serve /healthz and /readyz on, such as :8081 (default: none)")
	metricsListen := fs.String("metrics-listen", "", "address to serve metrics on, such as :9102 (default: none); a bearer token is read from LAUNCHWAY_METRICS_TOKEN if set")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, exitErr(0)
		}
		return nil, exitErr(2) // the flag package has explained
	}
	o := &runnerOptions{HealthListen: *health, MetricsListen: *metricsListen}
	if *cfgPath != "" {
		f, err := config.LoadRunner(*cfgPath)
		if err != nil {
			return nil, err
		}
		o.RunnerFile = *f
	}
	host, _ := os.Hostname()
	o.Server = cmp.Or(*server, os.Getenv("LAUNCHWAY_SERVER"), o.Server)
	o.Token = cmp.Or(*token, os.Getenv("LAUNCHWAY_RUNNER_TOKEN"))
	o.Join = cmp.Or(*join, os.Getenv("LAUNCHWAY_JOIN"))
	if *labels != "" {
		o.Labels = strings.Split(*labels, ",")
	}
	o.Name = cmp.Or(*name, o.Name, host)
	o.Data = cmp.Or(*data, o.Data, "launchway-runner")
	o.Concurrency = cmp.Or(*conc, o.Concurrency, 2)
	o.Executor = cmp.Or(*exe, o.Executor, "auto")
	o.Kubernetes.Context = cmp.Or(k8s.Context, o.Kubernetes.Context)
	o.Kubernetes.Namespace = cmp.Or(k8s.Namespace, o.Kubernetes.Namespace)
	if k8s.Restricted != nil {
		o.Kubernetes.Restricted, o.Kubernetes.RunAsUser, o.Kubernetes.RunAsGroup = k8s.Restricted, nil, nil
	}
	if *secretsConfig != "" {
		c, err := secrets.LoadConfig(*secretsConfig)
		if err != nil {
			return nil, err
		}
		o.Secrets = c
	}
	if o.Server == "" {
		return nil, errors.New("a server URL (--server, LAUNCHWAY_SERVER or server: in --config) is required")
	}
	if o.Token != "" && o.Join != "" {
		return nil, errors.New("give either a join token (--join) or a shared token (--token), not both")
	}
	if o.Token == "" && o.Join == "" {
		if id, err := runner.LoadIdentity(o.Data); err != nil || id == nil {
			return nil, errors.New("a join token (--join or LAUNCHWAY_JOIN) to enrol this runner, or a shared token (--token or LAUNCHWAY_RUNNER_TOKEN), is required")
		}
	}
	return o, nil
}

func cmdRunner(args []string) error {
	o, err := runnerArgs(args)
	if err != nil {
		return err
	}
	secretStores, err := secrets.Open(o.Secrets)
	if err != nil {
		return fmt.Errorf("secret stores: %w", err)
	}
	reportLimits, err := o.Reports.Limits()
	if err != nil {
		return fmt.Errorf("settings of the Station: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, ident, err := runnerClient(ctx, o)
	if err != nil {
		return err
	}
	id := "runner-" + o.Name
	if ident != nil {
		id = ident.RunnerID
	}
	if o.Deploy != nil {
		return runDeployRunner(ctx, o, client, id, secretStores)
	}
	ex, err := executor.ByName(o.Executor, executor.Options{Kubernetes: o.Kubernetes, RunnerID: id, DataDir: o.Data, AllowUnsandboxed: o.AllowUnsandboxedSteps})
	if err != nil {
		return err
	}
	if _, host := ex.(*executor.Host); host && ident != nil && o.Executor != "host" {
		// An enrolled runner runs other people's code: never on the host
		// without being told to.
		return errors.New("no Podman or Docker was found, and an enrolled Station does not fall back to running steps directly on this host; install one, or choose executor: host explicitly")
	}
	if err := checkExecutor(ctx, ex); err != nil {
		return err
	}
	r := &runner.Runner{Client: client, Exec: ex,
		Info: engine.RunnerInfo{ID: id, Name: o.Name, Executor: ex.Name(), Version: version, Labels: o.Labels,
			OS: runtime.GOOS, Arch: runtime.GOARCH},
		DataDir: o.Data, Concurrency: o.Concurrency, Secrets: secretStores, Reports: reportLimits, Images: o.Images,
		Accept: o.Accept, Remote: true, SpoolMaxBytes: o.Spool.Bytes(), SpoolMaxAge: o.Spool.Age()}
	if o.HealthListen != "" {
		ln, err := net.Listen("tcp", o.HealthListen)
		if err != nil {
			return fmt.Errorf("--health-listen: %w", err)
		}
		hs := &http.Server{Handler: r.Health(), ReadHeaderTimeout: 10 * time.Second}
		go hs.Serve(ln)
		defer hs.Close()
	}
	startRunnerMetrics(ctx, o)
	err = r.Run(ctx)
	if ident != nil && ident.Ephemeral {
		// The server revoked it as it left; the next start enrols anew.
		_ = os.RemoveAll(filepath.Join(o.Data, "identity"))
	}
	return err
}

// ---- validate and graph ----

func pipelineFile(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	for _, p := range source.PipelinePaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return source.PipelinePaths[0]
}

func load(path string) (*pipeline.Pipeline, error) {
	// Read as a file of its directory: the path is the user's own, from the command line.
	dir, base := filepath.Split(filepath.Clean(path))
	if dir == "" {
		dir = "."
	}
	data, err := fs.ReadFile(os.DirFS(dir), base)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			pe.Path = path // name the file as the user wrote it
		}
		return nil, err
	}
	p, err := pipeline.Parse(data)
	if err != nil {
		var ps pipeline.Problems
		if errors.As(err, &ps) {
			for _, pr := range ps {
				loc := path
				if pr.Line > 0 {
					loc = fmt.Sprintf("%s:%d", path, pr.Line)
				}
				where := ""
				if pr.Path != "" {
					where = pr.Path + ": "
				}
				fmt.Fprintf(os.Stderr, "%s: %s%s\n", loc, where, pr.Message)
			}
			return nil, exitErr(1)
		}
		return nil, err
	}
	return p, nil
}

func cmdGraph(args []string) error {
	p, err := load(pipelineFile(args))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

// ---- run locally ----

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	file := fs.String("file", "", "pipeline file (default .launchway/pipeline.yaml)")
	exe := fs.String("executor", "auto", "auto, container, podman, docker, host or kubernetes")
	k8s := k8sFlags(fs)
	secretsConfig := secretsFlag(fs)
	imagesConfig := fs.String("images-config", "", "a file with the Station's images: section (builders and registries), for image steps")
	unsandboxed := fs.Bool("allow-unsandboxed-steps", false, "let the host executor run custom steps although it cannot enforce their sandbox (no network, a read-only workspace)")
	var given multi
	fs.Var(&given, "i", "an input value, name=value; once for each input (the others take their defaults)")
	var libs libraryFlag
	fs.Var(&libs, "library", "run templates from a library directory: NAME=DIRECTORY, where NAME is what the pipeline's libraries section calls it (repeat for each library); the starters that ship with Launchway need none")
	fs.Parse(args)
	libDirs, err := pipeline.ParseLibraryFlags(libs)
	if err != nil {
		return err
	}
	secretStores, err := localSecrets(*secretsConfig)
	if err != nil {
		return err
	}
	images, err := loadImagesConfig(*imagesConfig)
	if err != nil {
		return err
	}

	path := *file
	if path == "" {
		path = pipelineFile(nil)
	}
	p, err := load(path)
	if err != nil {
		return err
	}
	p, inputs, err := bindLocal(p, given)
	if err != nil {
		return err
	}
	p, runLibs, cleanup, err := expandRunLibraries(p, path, libDirs)
	if err != nil {
		return err
	}
	defer cleanup()
	if images == nil {
		for _, s := range p.Steps {
			if s.IsImage() {
				return fmt.Errorf("step %s builds an image: give the builders and registries to use with --images-config FILE "+
					"(an images: section, as in a Station's configuration)", s.ID)
			}
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "launchway-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	ex, err := executor.ByName(*exe, executor.Options{Kubernetes: *k8s, RunnerID: "local-" + currentUser(), DataDir: filepath.Join(tmp, "runner"), AllowUnsandboxed: *unsandboxed})
	if err != nil {
		return err
	}
	if err := checkExecutor(context.Background(), ex); err != nil {
		return err
	}

	quiet := slog.New(logsafe.Handler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	localStore, err := blobstore.NewFilesystem(filepath.Join(tmp, "artifacts"))
	if err != nil {
		return err
	}
	eng, err := engine.Open(engine.Options{Database: filepath.Join(tmp, "run.db"), Logger: quiet,
		Artifacts: engine.ArtifactOptions{Store: localStore}})
	if err != nil {
		return err
	}
	defer eng.Close()
	token := "local"
	srv := &api.Server{Engine: eng, RunnerToken: token, Log: quiet}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler()}
	go httpSrv.Serve(ln)
	defer httpSrv.Close()

	trig := engine.Trigger{Event: "manual", Actor: currentUser(), Inputs: inputs}
	trig.Branch, trig.Ref, trig.SHA = currentGitHead()
	run, err := eng.CreateRun(engine.NewRun{Project: "local", Pipeline: p, Trigger: trig, Source: engine.Source{Kind: "dir", Path: dir, Libraries: runLibs}})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runnerCtx, stopRunner := context.WithCancel(context.Background())
	defer stopRunner()
	r := &runner.Runner{Client: runner.NewClient("http://"+ln.Addr().String(), token), Exec: ex,
		Info:    engine.RunnerInfo{ID: "local", Name: "this machine", Executor: ex.Name(), Version: version},
		DataDir: filepath.Join(tmp, "runner"), Concurrency: 4, Log: quiet, Secrets: secretStores, Images: images}
	go r.Run(runnerCtx)

	pr := newPrinter(p)
	pr.header(p, ex.Name(), dir, trig)
	canceled := false
	ask := newAsker(eng, run.ID, trig.Actor, pr)
	for {
		d, err := eng.GetRun(run.ID)
		if err != nil {
			return err
		}
		pr.update(eng, d)
		ask.update(d)
		if d.Status.Terminal() {
			// A local run ends with this process, so release what it held.
			if cl, ok := ex.(executor.Cleaner); ok {
				cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_ = cl.Cleanup(cctx, func(context.Context, []string) (map[string]executor.RunInfo, error) { return nil, nil })
				cancel()
			}
			pr.summary(d)
			if d.Status != engine.RunSucceeded {
				return exitErr(1)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			if !canceled {
				canceled = true
				fmt.Fprintln(os.Stderr, "\nCanceling… (press Ctrl-C again to quit at once)")
				_ = eng.Cancel(run.ID, trig.Actor)
				stop()
				ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			} else {
				return exitErr(130)
			}
		case line, ok := <-ask.answers():
			ask.answer(line, ok)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// isTerminal reports whether f is an interactive terminal (not merely a
// character device such as /dev/null).
func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// k8sFlags adds the Kubernetes executor's command-line settings.
func k8sFlags(fs *flag.FlagSet) *executor.KubernetesOptions {
	o := &executor.KubernetesOptions{}
	fs.StringVar(&o.Context, "k8s-context", "", "kubeconfig context for the kubernetes executor (default: current)")
	fs.StringVar(&o.Namespace, "k8s-namespace", "", "namespace for step pods (default: the context's)")
	fs.StringVar(&o.Pod, "k8s-pod", "", "how much of a run one pod holds in the kubernetes executor: per-step (default) or per-run (kubernetes.pod)")
	fs.BoolFunc("k8s-unrestricted", "run step pods' containers as their images' own users (often root), "+
		"without the restricted security context (kubernetes.restricted: false)", func(v string) error {
		b, err := strconv.ParseBool(v)
		if err == nil && b {
			o.Restricted = new(bool)
		}
		return err
	})
	return o
}

// secretsFlag adds --secrets-config, a YAML file mapping store names to
// settings, the same as runner.secrets in the server's configuration.
func secretsFlag(fs *flag.FlagSet) *string {
	return fs.String("secrets-config", "", "YAML file of secret stores")
}

// checkExecutor lets executors that can verify their setup do so at start-up.
func checkExecutor(ctx context.Context, ex executor.Executor) error {
	if c, ok := ex.(interface{ Check(context.Context) error }); ok {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return c.Check(cctx)
	}
	return nil
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "local"
}

// logChartNotice says which chart revision rendered this server, against the one the binary expects.
func logChartNotice(log *slog.Logger) {
	msg, warn := config.ChartNotice(os.Getenv, os.Getenv("KUBERNETES_SERVICE_HOST") != "")
	switch {
	case msg == "":
	case warn:
		log.Warn(msg, "binary_version", version)
	default:
		log.Info(msg, "binary_version", version)
	}
	if m := config.IgnoredValuesNotice(os.Getenv); m != "" {
		log.Warn(m)
	}
	if m := config.NetworkPolicyNotice(os.Getenv); m != "" {
		log.Info(m)
	}
}

// setProxy gives the server what the configuration says about proxies in front of it and about who may make
// service accounts' tokens, and warns when the older trust_proxy is on without server.trusted_proxies.
func setProxy(srv *api.Server, cfg *config.Config, log *slog.Logger) error {
	ranges, err := cfg.Server.TrustedProxyPrefixes()
	if err != nil {
		return err
	}
	if cfg.Server.TrustProxy && len(ranges) == 0 {
		log.Warn("server.trust_proxy is on without server.trusted_proxies, so the forwarding headers (and with them the client address that token source-address limits and the Logbook use) are believed from every address that reaches this server. Set server.trusted_proxies to the address ranges of your proxies, for example `server: {trusted_proxies: [10.0.0.0/8]}` (Helm: server.trustedProxies); the older trust_proxy then no longer widens it")
	}
	srv.TrustedProxies, srv.ClientAddrHeader = ranges, cfg.Server.ClientAddressHeader
	srv.AdminTokenMakesServiceTokens = cfg.Auth.Tokens.ServiceAccountsFromTokens
	return nil
}
