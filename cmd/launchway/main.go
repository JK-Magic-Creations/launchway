// Command launchway is the whole product in one binary: the server, the
// runner, and a local runner for trying pipelines on your own machine.
package main

import (
	"bufio"
	"bytes"
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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/source"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
	"github.com/jk-magic-creations/launchway/web"
)

var version = "dev"

const usage = `Launchway: build and deliver software.

Usage:
  launchway server   [--config launchway.yaml]   run the server, its web UI and a built-in runner
  launchway runner   --server URL --join J      enrol a runner with a join token, then take work
                     [--config runner.yaml] [--health-listen ADDR] [--labels a,b]
  launchway runner   --server URL --token T     run a runner with the shared token (no identity)
  launchway run      [--file F] [--executor E]  run this directory's pipeline on this machine
                     [-i name=value ...]
  launchway validate [file] [--project P]       check a pipeline file (with --project, the server checks it for that project too)
  launchway graph    [file]                     print a pipeline's graph as JSON
  launchway login    --server URL               store an API token for a server
  launchway runs     start|list|show|logs|cancel|lanes|resume|rerun  start, follow and re-run runs
                                                (launchway runs, for more)
  launchway approvals list|approve|reject       approvals waiting for a decision, and deciding them
  launchway templates list [library]            the shared libraries' templates and their parameters
  launchway policy   list|run                   the policies in force; a run's decisions
  launchway waivers  list|grant|revoke          waive a policy's rule for a while, with a reason
  launchway blobs    status|copy                see and move where artifacts are kept
                                                (launchway blobs, for more)
  launchway mcp      [--server URL]             the server's MCP tools for agents, on stdin and stdout
  launchway verifications list|show|mark        verifications on a server
                                                (launchway verifications, for more)
  launchway health-sources | models             what verify and ai steps may name
  launchway runners  list|join-token|revoke|rotate  runners, and join tokens to enrol them
  launchway step-resources get|set-limits|set-default  the default CPU and memory of steps, and their limits
  launchway config   list|get|set|create|delete|restore|history|export|import|apply ...
                                                configuration kept in the database (launchway config, for more)
  launchway projects|libraries|environments|policies|runner-pools|role-bindings|policy-sources|units|secret-stores ...
                                                the same commands for one kind of object
  launchway run-limits get|set                         the longest run timeout a pipeline may ask for
  launchway caches   list|delete|policy         build caches: what is kept, deleting, how much is kept
  launchway variables list|effective|set|delete|lock|unlock  variables set at a level; the nearest setting wins
  launchway doctor   [database]                 would this installation survive losing its database?
  launchway repos    list|get|create|update|delete   hosted Git repositories (installation setting forge.hosting)
  launchway version

The pipeline file defaults to .launchway/pipeline.yaml. runner and run take
--secrets-config FILE, the secret stores they may read.
`

func main() {
	gitexec.Reexec()
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
	// -h or --help on any command prints its help and exits 0, signed in or not: help must not need a server.
	helping := helpRequested(os.Args[1], args)
	if helping {
		if text, ok := helpTexts[os.Args[1]]; ok {
			fmt.Print(text)
			os.Exit(0)
		}
		allowHelpWithoutCredentials()
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
	case "graph":
		err = cmdGraph(args)
	case "login":
		err = cmdLogin(args)
	case "blobs":
		err = cmdBlobs(args)
	case "runs":
		err = cmdRuns(args)
	case "approvals":
		err = cmdApprovals(args)
	case "templates":
		err = cmdTemplates(args)
	case "policy":
		err = cmdPolicy(args)
	case "waivers":
		err = cmdWaivers(args)
	case "mcp":
		err = cmdMCP(args)
	case "verifications":
		err = cmdVerifications(args)
	case "health-sources":
		err = cmdHealthSources(args)
	case "models":
		err = cmdModels(args)
	case "runners":
		err = cmdRunners(args)
	case "step-resources":
		err = cmdStepResources(args)
	case "config":
		err = cmdConfig(args)
	case "projects", "libraries", "environments", "policies", "runner-pools", "role-bindings", "policy-sources", "units", "secret-stores":
		err = cmdKindAlias(os.Args[1])(args)
	case "run-limits":
		err = cmdRunLimits(args)
	case "caches":
		err = cmdCaches(args)
	case "variables":
		err = cmdVariables(args)
	case "doctor":
		err = cmdDoctor(args)
	case "repos":
		err = cmdRepos(args)
	case "version", "--version", "-v":
		fmt.Println("launchway", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		os.Exit(unknownCommand(os.Args[1]))
	}
	if err != nil {
		var ex exitErr
		if helping && errors.As(err, &ex) && int(ex) == exitUsage {
			os.Exit(0) // the flag set printed its help and asked for exit 2, which is for a command written wrongly
		}
		exitOn(err)
	}
}

// helpTexts are the commands whose arguments are not parsed as flags only: asking for help prints this, and runs nothing.
var helpTexts = map[string]string{
	"validate": `Usage: launchway validate [file] [--project NAME]

Checks a pipeline file (default .launchway/pipeline.yaml) and prints each problem with its file, line and
path, one per line. Exits 0 if the file is valid, 1 if it has problems. With --project NAME the server you
are signed in to checks it for that project as well: its libraries, the limits the installation sets, and
what the project must have for the pipeline to work.
`,
	"graph": `Usage: launchway graph [file]

Prints a pipeline's graph (steps and what each needs) as JSON. The file defaults to .launchway/pipeline.yaml.
Exits 0, or 1 if the file has problems.
`,
}

// helpRequested says whether the arguments ask for help: -h, -help or --help before any "--". The commands that
// start a server or a runner, and those that already answer help themselves, are left alone.
func helpRequested(command string, args []string) bool {
	switch command {
	case "server", "runner", "run", "help", "--help", "-h", "version", "--version", "-v":
		return false
	}
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "-help" || a == "--help" {
			return true
		}
	}
	return false
}

// allowHelpWithoutCredentials lets a client command get as far as printing its flags: the stand-ins are never
// used, because help makes no request.
func allowHelpWithoutCredentials() {
	if os.Getenv("LAUNCHWAY_SERVER") == "" {
		_ = os.Setenv("LAUNCHWAY_SERVER", "http://127.0.0.1:1")
	}
	if os.Getenv("LAUNCHWAY_TOKEN") == "" {
		_ = os.Setenv("LAUNCHWAY_TOKEN", "help")
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
	cfg, cfgPath, log, live, authOpts := su.cfg, su.cfgPath, su.log, su.live, su.authOpts
	eng, err := su.openEngine()
	if err != nil {
		return err
	}
	defer eng.Close()
	logStore(eng, log)

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
	token, err := config.RunnerToken(cfg.Data, eng)
	if err != nil {
		return err
	}
	srv := &api.Server{Engine: eng, Projects: mgr, RunnerToken: token, Actor: cfg.User, Auth: authOpts,
		TrustProxy: cfg.Server.TrustProxy, UI: web.FS(), Version: version, Log: log, ReportsOrigin: su.reportsOrigin}
	if store.Available() {
		srv.Settings = store
	}
	// Variables set at levels (schema 29): the manager binds pipelines to
	// them, the server serves and changes them.
	vars := store.Variables()
	mgr.SetVariables(vars)
	srv.Variables = vars
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
	if err := startHosting(ctx, eff, eng, log, srv); err != nil {
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
	signIn := "sign-in with " + cfg.Auth.OIDC.Issuer
	if authOpts == nil {
		signIn = "no sign-in: anyone who can reach it has full access"
	}
	fmt.Fprintf(os.Stderr, "\n  Launchway %s is listening on %s (%s)\n  %d project(s), data in %s, %s\n  Runner token for other machines: %s\n\n",
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

// shutdownTimeout bounds how long a stopping server waits for the requests
// in flight to finish.
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
	if cfg.Auth.Mode != config.AuthOIDC {
		return nil, nil
	}
	secret, err := cfg.Auth.OIDC.ClientSecret()
	if err != nil {
		return nil, err
	}
	policy, err := cfg.Auth.Policy()
	if err != nil {
		return nil, err
	}
	idle, lifetime, err := cfg.Auth.Sessions()
	if err != nil {
		return nil, err
	}
	o := cfg.Auth.OIDC
	return &api.Auth{
		OIDC: auth.NewOIDC(auth.OIDCOptions{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: secret,
			RedirectURL: o.RedirectURL, Scopes: o.Scopes, UsernameClaim: o.UsernameClaim, GroupsClaim: o.GroupsClaim}),
		Policy: policy, IdleTimeout: idle, Lifetime: lifetime,
		SecureCookies: strings.HasPrefix(o.RedirectURL, "https://"),
	}, nil
}

// runnerOptions is what `launchway runner` runs with.
type runnerOptions struct {
	config.RunnerFile
	Token        string
	Join         string // a join token, to enrol with
	HealthListen string
}

// runnerArgs reads the runner's settings: a flag given on the command line
// wins, then the environment (LAUNCHWAY_SERVER, LAUNCHWAY_RUNNER_TOKEN), then
// the --config file, then the defaults.
func runnerArgs(args []string) (*runnerOptions, error) {
	fs := flag.NewFlagSet("runner", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "YAML file of runner settings, the same as runner: in the server's configuration plus server, name and data")
	server := fs.String("server", "", "server URL (or LAUNCHWAY_SERVER)")
	token := fs.String("token", "", "the shared runner token or a deploy pool's (or LAUNCHWAY_RUNNER_TOKEN)")
	join := fs.String("join", "", "a join token, to enrol this runner once and get an identity of its own (or LAUNCHWAY_JOIN)")
	labels := fs.String("labels", "", "labels this runner has, separated by commas, such as arm64,gpu")
	name := fs.String("name", "", "a name for this runner (default: the host name)")
	data := fs.String("data", "", "directory for workspaces (default launchway-runner)")
	conc := fs.Int("concurrency", 0, "steps to run at once (default 2)")
	exe := fs.String("executor", "", "auto (the default), container, podman, docker, host or kubernetes")
	k8s := k8sFlags(fs)
	secretsConfig := secretsFlag(fs)
	health := fs.String("health-listen", "", "address to serve /healthz and /readyz on, such as :8081 (default: none)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, exitErr(0)
		}
		return nil, exitErr(2) // the flag package has explained
	}
	o := &runnerOptions{HealthListen: *health}
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
		return fmt.Errorf("runner settings: %w", err)
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
	ex, err := executor.ByName(o.Executor, executor.Options{Kubernetes: o.Kubernetes, RunnerID: id})
	if err != nil {
		return err
	}
	if _, host := ex.(*executor.Host); host && ident != nil && o.Executor != "host" {
		// An enrolled runner runs other people's code: never on the host
		// without being told to.
		return errors.New("no Podman or Docker was found, and an enrolled runner does not fall back to running steps directly on this host; install one, or choose executor: host explicitly")
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
	imagesConfig := fs.String("images-config", "", "a file with the runner's images: section (builders and registries), for image steps")
	var given multi
	fs.Var(&given, "i", "an input value, name=value; once for each input (the others take their defaults)")
	fs.Parse(args)
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
	if images == nil {
		for _, s := range p.Steps {
			if s.IsImage() {
				return fmt.Errorf("step %s builds an image: give the builders and registries to use with --images-config FILE "+
					"(an images: section, as in a runner's configuration)", s.ID)
			}
		}
	}
	ex, err := executor.ByName(*exe, executor.Options{Kubernetes: *k8s, RunnerID: "local-" + currentUser()})
	if err != nil {
		return err
	}
	if err := checkExecutor(context.Background(), ex); err != nil {
		return err
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

	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
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
	run, err := eng.CreateRun(engine.NewRun{Project: "local", Pipeline: p, Trigger: trig, Source: engine.Source{Kind: "dir", Path: dir}})
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

// asker decides approval steps in a local run: it asks on the terminal, one
// at a time, or rejects them if there is no terminal to ask on.
type asker struct {
	eng      *engine.Engine
	runID    string
	actor    string
	pr       *printer
	lines    chan string // what is typed on stdin; nil if stdin is not a terminal
	asked    map[string]bool
	question string // the step whose answer is awaited, or ""
}

func newAsker(eng *engine.Engine, runID, actor string, pr *printer) *asker {
	a := &asker{eng: eng, runID: runID, actor: actor, pr: pr, asked: map[string]bool{}}
	if isTerminal(os.Stdin) {
		a.lines = make(chan string)
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				a.lines <- sc.Text()
			}
			close(a.lines)
		}()
	}
	return a
}

// update asks about the next approval that is waiting, if nothing is being
// asked already, and notices when the one being asked about stops waiting.
func (a *asker) update(d *engine.RunDetail) {
	for i := range d.StepList {
		s := &d.StepList[i]
		if s.ID == a.question && s.Status != engine.StepWaitingS {
			fmt.Println() // it was decided another way (canceled, or it expired)
			a.question = ""
		}
	}
	for i := range d.StepList {
		s := &d.StepList[i]
		key := fmt.Sprintf("%s#%d", s.ID, s.Attempt)
		if a.question != "" || s.Status != engine.StepWaitingS || a.asked[key] {
			continue
		}
		a.asked[key] = true
		if a.lines == nil {
			a.decide(s.ID, false, byCommand, "approvals need a terminal to answer in, or a Launchway server")
			continue
		}
		a.question = s.ID
		fmt.Print(a.pr.prefix(s.ID) + "Approve? [y/N] ")
	}
}

// answers is where typed answers arrive while a question is open.
func (a *asker) answers() <-chan string {
	if a.question == "" {
		return nil
	}
	return a.lines
}

func (a *asker) answer(line string, ok bool) {
	id := a.question
	a.question = ""
	if !ok {
		fmt.Println()
		a.lines = nil // stdin is closed: nobody can answer any more
		a.decide(id, false, byCommand, "stdin was closed before anyone answered")
		return
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		a.decide(id, true, a.actor, "")
	default:
		a.decide(id, false, a.actor, "")
	}
}

// byCommand is the actor recorded when nobody could be asked.
const byCommand = "launchway run"

func (a *asker) decide(stepID string, approve bool, actor, comment string) {
	if err := a.eng.Decide(a.runID, stepID, approve, actor, comment); err != nil {
		fmt.Fprintln(os.Stderr, a.pr.prefix(stepID)+err.Error())
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

// runnerClient makes the client a runner talks to its server with: through
// the proxy and with the certificates its settings name, and, for an
// enrolled runner, with access tokens from its identity. With a join token
// and no identity yet, it enrols first; an identity kept in the data
// directory is used on every start after.
func runnerClient(ctx context.Context, o *runnerOptions) (*runner.Client, *runner.Identity, error) {
	hc, err := runner.NewHTTPClient(runner.HTTPOptions{Proxy: o.Proxy, NoProxy: o.NoProxy, CAFile: o.CAFile})
	if err != nil {
		return nil, nil, err
	}
	c := runner.NewClient(o.Server, o.Token)
	c.HTTP = hc
	if o.Token != "" {
		return c, nil, nil
	}
	id, err := runner.LoadIdentity(o.Data)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the runner's identity: %w", err)
	}
	server := strings.TrimRight(o.Server, "/")
	switch {
	case id != nil && id.Server != server:
		return nil, nil, fmt.Errorf("this runner is enrolled with %s, not %s; use another data directory (--data) for another server", id.Server, server)
	case id == nil:
		host, _ := os.Hostname()
		executorName := o.Executor
		if o.Deploy != nil {
			executorName = "deploy"
		}
		jctx, cancel := context.WithTimeout(ctx, time.Minute)
		id, err = runner.Join(jctx, hc, server, o.Join, o.Data, host, runner.JoinFacts{Name: o.Name, OS: runtime.GOOS, Arch: runtime.GOARCH,
			Version: version, Executor: executorName, Labels: o.Labels})
		cancel()
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(os.Stderr, "Enrolled as runner %s in pool %s; its identity is in %s\n", id.RunnerID, id.Pool, filepath.Join(o.Data, "identity"))
	case o.Join != "":
		fmt.Fprintf(os.Stderr, "Already enrolled as runner %s; the join token is not used\n", id.RunnerID)
	}
	c.Auth = &runner.IdentityAuth{ID: id, HTTP: hc}
	c.Auth.RecoverRotation(ctx)
	return c, id, nil
}

// runDeployRunner runs `launchway runner` with a deploy section: a deploy
// runner, which does the work of deploy and rollback steps only.
func runDeployRunner(ctx context.Context, o *runnerOptions, client *runner.Client, id string, secretStores *secrets.Resolver) error {
	targets := make([]string, 0, len(o.Deploy.Targets))
	for name := range o.Deploy.Targets {
		targets = append(targets, name)
	}
	sort.Strings(targets)
	d := &runner.DeployRunner{
		Client:  client,
		Agent:   &argocd.Agent{Config: *o.Deploy, DataDir: o.Data, Secrets: secretStores},
		Info:    engine.RunnerInfo{ID: id, Name: o.Name, Executor: "deploy", Version: version, Targets: targets, OS: runtime.GOOS, Arch: runtime.GOARCH},
		Accept:  o.Accept,
		Secrets: secretStores,
	}
	if len(o.HealthSources) > 0 {
		lookup := func(ctx context.Context, project string, ref pipeline.SecretRef) (string, error) {
			return secretStores.Lookup(ctx, project, ref)
		}
		srcs, err := hsource.Open(o.HealthSources, lookup)
		if err != nil {
			return fmt.Errorf("health_sources: %w", err)
		}
		d.Sources = srcs
	}
	if o.AIEnabled() {
		d.AI = &ai.Client{}
	}
	if o.HealthListen != "" {
		ln, err := net.Listen("tcp", o.HealthListen)
		if err != nil {
			return fmt.Errorf("--health-listen: %w", err)
		}
		hs := &http.Server{Handler: d.Health(), ReadHeaderTimeout: 10 * time.Second}
		go hs.Serve(ln)
		defer hs.Close()
	}
	return d.Run(ctx)
}

// loadImagesConfig reads an images: section from a file, for launchway run.
func loadImagesConfig(path string) (*imagebuild.Config, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Images *imagebuild.Config `yaml:"images"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Images == nil {
		return nil, fmt.Errorf("%s: no images: section", path)
	}
	f.Images.RelativeTo(filepath.Dir(path))
	if err := f.Images.Check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f.Images, nil
}
