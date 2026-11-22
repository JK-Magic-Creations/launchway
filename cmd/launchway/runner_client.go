package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
)

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
		return nil, nil, fmt.Errorf("reading the Station's identity: %w", err)
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
		fmt.Fprintf(os.Stderr, "Enrolled as Station %s in pool %s; its identity is in %s\n", id.RunnerID, id.Pool, filepath.Join(o.Data, "identity"))
	case o.Join != "":
		fmt.Fprintf(os.Stderr, "Already enrolled as Station %s; the join token is not used\n", id.RunnerID)
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
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.HealthListen)
		if err != nil {
			return fmt.Errorf("--health-listen: %w", err)
		}
		hs := &http.Server{Handler: d.Health(), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = hs.Serve(ln) }()
		defer hs.Close()
	}
	startRunnerMetrics(ctx, o)
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
