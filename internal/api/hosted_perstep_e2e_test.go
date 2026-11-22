package api

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// copyingHost stands in for an executor that makes one pod for each step: the Station keeps its own copy of the
// run's workspace, made from the checkout the first time, and every step works in that copy.
type copyingHost struct {
	*executor.Host
	mu   sync.Mutex
	dir  string
	seen []string // what the checkout held when the copy was made
}

func (c *copyingHost) PerStep() bool { return true }

func (c *copyingHost) WorkspaceDir(executor.Spec) string { return c.dir }

func (c *copyingHost) Run(ctx context.Context, s executor.Spec, out executor.Sink) (int, error) {
	c.mu.Lock()
	if c.dir == "" {
		c.dir = filepath.Join(filepath.Dir(s.Workspace), "copy-of-"+filepath.Base(s.Workspace))
		if err := os.CopyFS(c.dir, os.DirFS(s.Workspace)); err != nil {
			c.mu.Unlock()
			return 1, err
		}
		entries, _ := os.ReadDir(c.dir)
		for _, en := range entries {
			c.seen = append(c.seen, en.Name())
		}
	}
	c.mu.Unlock()
	s.Workspace = c.dir
	return c.Host.Run(ctx, s, out)
}

const twoStepPipeline = `
pipeline: two
steps:
  first: {image: none, run: "echo from the first step > made-by-first"}
  second: {image: none, needs: [first], run: "test -f made-by-first && test -f .launchway/pipeline.yaml && echo the second step sees both"}
`

// With a pod for each step, a private repository hosted by this installation is still checked out by the Station
// with the credential made for the run (nothing configured, sign-in on), the Station's workspace copy is made from
// that checkout, and the later step sees what the earlier one wrote.
func TestPerStepExecutorOnAPrivateHostedRepositoryNeedsNoCredentials(t *testing.T) {
	os.Unsetenv("GIT_CONFIG_COUNT")
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	src := t.TempDir()
	gitRepo(t, src, twoStepPipeline)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/shop", e.tokenFor(t, "ada", "pusher")), "HEAD:refs/heads/main")
	if err := e.api.Projects.Reconfigure([]source.Project{{Name: "shop", URL: e.srv.URL + "/git/acme/shop.git", Poll: time.Hour}}, nil); err != nil {
		t.Fatal(err)
	}
	e.api.Projects.SetHostedLocal(e.api)

	ex := &copyingHost{Host: &executor.Host{}}
	r := &runner.Runner{Client: runner.NewClient(e.srv.URL, "runner-secret"), Exec: ex,
		Info: engine.RunnerInfo{ID: "station-ps", Name: "station-ps"}, DataDir: filepath.Join(e.dir, "station-ps"), Concurrency: 1, Log: quiet(), Secrets: secrets.NewResolver()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	run, err := e.api.Projects.StartRun(context.Background(), "shop", "main", "someone-else", nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := waitRun(t, e.eng, run.ID); d.Status != engine.RunSucceeded {
		t.Fatalf("the run: %s %+v", d.Status, d.StepList)
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	found := false
	for _, n := range ex.seen {
		found = found || n == ".launchway"
	}
	if !found {
		t.Errorf("the Station's copy was not made from the checkout of the hosted repository: %v", ex.seen)
	}
}
