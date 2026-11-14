// Command launchway is the whole product in one binary: the server, the
// runner, and a local runner for trying pipelines on your own machine.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/web"
)

var version = "dev"

const usage = `Launchway: build and deliver software.

Usage:
  launchway server   [--config launchway.yaml]   run the server, its web UI and a built-in runner
  launchway runner   --server URL --token T     run a runner that takes work from a server
  launchway run      [--file F] [--executor E]  run this directory's pipeline on this machine
  launchway validate [file]                     check a pipeline file
  launchway graph    [file]                     print a pipeline's graph as JSON
  launchway version

The pipeline file defaults to .launchway/pipeline.yaml. runner and run take
--secrets-config FILE, the secret stores they may read.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
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
	case "version", "--version", "-v":
		fmt.Println("launchway", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		var ex exitErr
		if errors.As(err, &ex) {
			os.Exit(int(ex))
		}
		fmt.Fprintln(os.Stderr, "launchway:", err)
		os.Exit(1)
	}
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// ---- server ----

func cmdServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	listen := fs.String("listen", "", "address to listen on (overrides the config)")
	data := fs.String("data", "", "data directory (overrides the config)")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	secretStores, err := secrets.Open(cfg.Runner.Secrets)
	if err != nil {
		return fmt.Errorf("%s: runner.secrets: %w", *cfgPath, err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *data != "" {
		cfg.Data = *data
	}
	if err := cfg.CheckExposure(cfg.Listen); err != nil {
		return err
	}
	authOpts, err := serverAuth(cfg)
	if err != nil {
		return fmt.Errorf("%s: %w", *cfgPath, err)
	}
	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	dbAddr := cfg.DatabaseAddr()
	allowSelf := cfg.AllowSelfApproval()
	eng, err := engine.Open(engine.Options{Database: dbAddr, Logger: log, AllowSelfApproval: &allowSelf})
	if err != nil {
		return err
	}
	defer eng.Close()
	projects, err := cfg.SourceProjects()
	if err != nil {
		return err
	}
	mgr, err := source.NewManager(eng, cfg.Data, projects, log)
	if err != nil {
		return err
	}
	token, err := config.RunnerToken(cfg.Data, eng)
	if err != nil {
		return err
	}
	srv := &api.Server{Engine: eng, Projects: mgr, RunnerToken: token, Actor: cfg.User, Auth: authOpts,
		TrustProxy: cfg.Server.TrustProxy, UI: web.FS(), Version: version, Log: log}
	if authOpts != nil {
		// Find the provider now, so a mistake shows at once; sign-in tries
		// again later if it is unreachable.
		dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, err := authOpts.OIDC.Discover(dctx); err != nil {
			log.Warn("the sign-in provider cannot be reached yet; sign-in will keep trying", "err", err)
		}
		cancel()
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return baseCtx }}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()
	mgr.Start(ctx)

	url := "http://" + loopback(ln.Addr().String())
	runnerCtx, stopRunner := context.WithCancel(context.Background())
	defer stopRunner()
	runnerDone := make(chan struct{})
	if cfg.RunnerEnabled() {
		host, _ := os.Hostname()
		// Unique per server: two servers on one machine must not share an identity.
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		runnerID := "builtin-" + host + "-" + port
		ex, err := executor.ByName(cfg.Runner.Executor, executor.Options{Kubernetes: cfg.Runner.Kubernetes, RunnerID: runnerID})
		if err != nil {
			return err
		}
		if err := checkExecutor(ctx, ex); err != nil {
			return err
		}
		r := &runner.Runner{
			Client: runner.NewClient(url, token), Exec: ex,
			Info:    engine.RunnerInfo{ID: runnerID, Name: host + " (built in)", Executor: ex.Name(), Version: version},
			DataDir: filepath.Join(cfg.Data, "runner"), Concurrency: cfg.Runner.Concurrency, Log: log,
			Secrets: secretStores,
		}
		go func() { defer close(runnerDone); r.Run(runnerCtx) }()
	} else {
		close(runnerDone)
	}

	tokenAt := config.TokenFile(cfg.Data)
	if os.Getenv("LAUNCHWAY_RUNNER_TOKEN") != "" {
		tokenAt = "LAUNCHWAY_RUNNER_TOKEN"
	}
	signIn := "sign-in with " + cfg.Auth.OIDC.Issuer
	if authOpts == nil {
		signIn = "no sign-in: anyone who can reach it has full access"
	}
	fmt.Fprintf(os.Stderr, "\n  Launchway %s is running at %s (%s)\n  %d project(s), data in %s, %s\n  Runner token for other machines: %s\n\n",
		version, url, signIn, len(projects), cfg.Data, describeDatabase(dbAddr), tokenAt)

	<-ctx.Done()
	fmt.Fprintln(os.Stderr, "\nStopping. Steps in progress will run again when Launchway starts.")
	stopRunner()
	select {
	case <-runnerDone:
	case <-time.After(30 * time.Second):
	}
	cancelBase()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shut)
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

// describeDatabase names the database without revealing a password.
func describeDatabase(addr string) string {
	u, err := neturl.Parse(addr)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "SQLite database " + addr
	}
	return "PostgreSQL database " + u.Redacted()
}

// loopback turns 0.0.0.0:8080 or [::]:8080 into 127.0.0.1:8080.
func loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// ---- runner ----

func cmdRunner(args []string) error {
	fs := flag.NewFlagSet("runner", flag.ExitOnError)
	server := fs.String("server", os.Getenv("LAUNCHWAY_SERVER"), "server URL")
	token := fs.String("token", os.Getenv("LAUNCHWAY_RUNNER_TOKEN"), "runner token (or LAUNCHWAY_RUNNER_TOKEN)")
	name := fs.String("name", "", "a name for this runner (default: the host name)")
	data := fs.String("data", "launchway-runner", "directory for workspaces")
	conc := fs.Int("concurrency", 2, "steps to run at once")
	exe := fs.String("executor", "auto", "auto, container, podman, docker, host or kubernetes")
	k8s := k8sFlags(fs)
	secretsConfig := secretsFlag(fs)
	fs.Parse(args)
	if *server == "" || *token == "" {
		return errors.New("--server and --token are required")
	}
	secretStores, err := openSecrets(*secretsConfig)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if *name == "" {
		*name = host
	}
	ex, err := executor.ByName(*exe, executor.Options{Kubernetes: *k8s, RunnerID: "runner-" + *name})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := checkExecutor(ctx, ex); err != nil {
		return err
	}
	r := &runner.Runner{Client: runner.NewClient(*server, *token), Exec: ex,
		Info:    engine.RunnerInfo{ID: "runner-" + *name, Name: *name, Executor: ex.Name(), Version: version},
		DataDir: *data, Concurrency: *conc, Secrets: secretStores}
	return r.Run(ctx)
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
	data, err := os.ReadFile(path)
	if err != nil {
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

func cmdValidate(args []string) error {
	path := pipelineFile(args)
	p, err := load(path)
	if err != nil {
		return err
	}
	fmt.Printf("%s: pipeline %q is valid, %d step(s) in %d level(s)\n", path, p.Name, len(p.Steps), len(p.Levels()))
	return nil
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
	fs.Parse(args)
	secretStores, err := localSecrets(*secretsConfig)
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
	eng, err := engine.Open(engine.Options{Database: filepath.Join(tmp, "run.db"), Logger: quiet})
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

	trig := engine.Trigger{Event: "manual", Actor: currentUser()}
	if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		if b := strings.TrimSpace(string(out)); b != "HEAD" {
			trig.Branch, trig.Ref = b, "refs/heads/"+b
		}
	}
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		trig.SHA = strings.TrimSpace(string(out))
	}
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
		DataDir: filepath.Join(tmp, "runner"), Concurrency: 4, Log: quiet, Secrets: secretStores}
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
	return o
}

// secretsFlag adds --secrets-config, a YAML file mapping store names to
// settings, the same as runner.secrets in the server's configuration.
func secretsFlag(fs *flag.FlagSet) *string {
	return fs.String("secrets-config", "", "YAML file of secret stores")
}

// localSecrets is openSecrets for 'launchway run' on your own machine, where
// your environment is yours to use: it adds the env store. A server or
// shared runner must never do this, or any pipeline could read the runner's
// environment.
func localSecrets(file string) (*secrets.Resolver, error) {
	r, err := openSecrets(file)
	if err != nil {
		return nil, err
	}
	return r.AddLocalEnv(), nil
}

// openSecrets reads and checks the secret stores in file, if one is given.
func openSecrets(file string) (*secrets.Resolver, error) {
	if file == "" {
		return secrets.NewResolver(), nil
	}
	c, err := secrets.LoadConfig(file)
	if err != nil {
		return nil, err
	}
	r, err := secrets.Open(c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return r, nil
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
