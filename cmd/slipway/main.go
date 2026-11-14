// Command slipway is the whole product in one binary: the server, the
// runner, and a local runner for trying pipelines on your own machine.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"slipway/internal/api"
	"slipway/internal/config"
	"slipway/internal/engine"
	"slipway/internal/executor"
	"slipway/internal/pipeline"
	"slipway/internal/runner"
	"slipway/internal/source"
	"slipway/web"
)

var version = "dev"

const usage = `Slipway: build and deliver software.

Usage:
  slipway server   [--config slipway.yaml]   run the server, its web UI and a built-in runner
  slipway runner   --server URL --token T     run a runner that takes work from a server
  slipway run      [--file F] [--executor E]  run this directory's pipeline on this machine
  slipway validate [file]                     check a pipeline file
  slipway graph    [file]                     print a pipeline's graph as JSON
  slipway version

The pipeline file defaults to .slipway/pipeline.yaml.
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
		fmt.Println("slipway", version)
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
		fmt.Fprintln(os.Stderr, "slipway:", err)
		os.Exit(1)
	}
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// ---- server ----

func cmdServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	cfgPath := fs.String("config", "slipway.yaml", "configuration file")
	listen := fs.String("listen", "", "address to listen on (overrides the config)")
	data := fs.String("data", "", "data directory (overrides the config)")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *data != "" {
		cfg.Data = *data
	}
	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	eng, err := engine.Open(engine.Options{Path: filepath.Join(cfg.Data, "slipway.db"), Logger: log})
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
	token, err := config.RunnerToken(cfg.Data)
	if err != nil {
		return err
	}
	srv := &api.Server{Engine: eng, Projects: mgr, RunnerToken: token, Actor: cfg.User, UI: web.FS(), Version: version, Log: log}

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
		ex, err := executor.ByName(cfg.Runner.Executor)
		if err != nil {
			return err
		}
		host, _ := os.Hostname()
		r := &runner.Runner{
			Client: runner.NewClient(url, token), Exec: ex,
			Info:    engine.RunnerInfo{ID: "builtin-" + host, Name: host + " (built in)", Executor: ex.Name(), Version: version},
			DataDir: filepath.Join(cfg.Data, "runner"), Concurrency: cfg.Runner.Concurrency, Log: log,
		}
		go func() { defer close(runnerDone); r.Run(runnerCtx) }()
	} else {
		close(runnerDone)
	}

	fmt.Fprintf(os.Stderr, "\n  Slipway %s is running at %s\n  %d project(s), data in %s\n  Runner token for other machines: %s\n\n",
		version, url, len(projects), cfg.Data, filepath.Join(cfg.Data, "runner-token"))

	<-ctx.Done()
	fmt.Fprintln(os.Stderr, "\nStopping. Steps in progress will run again when Slipway starts.")
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
	server := fs.String("server", os.Getenv("SLIPWAY_SERVER"), "server URL")
	token := fs.String("token", os.Getenv("SLIPWAY_RUNNER_TOKEN"), "runner token (or SLIPWAY_RUNNER_TOKEN)")
	name := fs.String("name", "", "a name for this runner (default: the host name)")
	data := fs.String("data", "slipway-runner", "directory for workspaces")
	conc := fs.Int("concurrency", 2, "steps to run at once")
	exe := fs.String("executor", "auto", "auto, container, podman, docker or host")
	fs.Parse(args)
	if *server == "" || *token == "" {
		return errors.New("--server and --token are required")
	}
	ex, err := executor.ByName(*exe)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if *name == "" {
		*name = host
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := &runner.Runner{Client: runner.NewClient(*server, *token), Exec: ex,
		Info:    engine.RunnerInfo{ID: "runner-" + *name, Name: *name, Executor: ex.Name(), Version: version},
		DataDir: *data, Concurrency: *conc}
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
	file := fs.String("file", "", "pipeline file (default .slipway/pipeline.yaml)")
	exe := fs.String("executor", "auto", "auto, container, podman, docker or host")
	fs.Parse(args)
	path := *file
	if path == "" {
		path = pipelineFile(nil)
	}
	p, err := load(path)
	if err != nil {
		return err
	}
	ex, err := executor.ByName(*exe)
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "slipway-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	eng, err := engine.Open(engine.Options{Path: filepath.Join(tmp, "run.db"), Logger: quiet})
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
		DataDir: filepath.Join(tmp, "runner"), Concurrency: 4, Log: quiet}
	go r.Run(runnerCtx)

	pr := newPrinter(p)
	pr.header(p, ex.Name(), dir, trig)
	canceled := false
	for {
		d, err := eng.GetRun(run.ID)
		if err != nil {
			return err
		}
		pr.update(eng, d)
		if d.Status.Terminal() {
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
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "local"
}
