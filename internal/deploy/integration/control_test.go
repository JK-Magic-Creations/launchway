package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// The control scenarios (phase 2) run against Application ctl-canary in
// namespace lw-ctl, from repository control.git (deploy/test/argo/control):
// names of their own, so they share the kind cluster with the phase-1
// scenarios and with other test runs without touching their objects.

// setupControl is setup for the control scenarios.
func setupControl(t *testing.T) *harness {
	kctx := os.Getenv("LAUNCHWAY_TEST_ARGO_CONTEXT")
	if kctx == "" {
		t.Skip("set LAUNCHWAY_TEST_ARGO_CONTEXT to a kind context with deploy/test/argo installed (make test-argo)")
	}
	if !strings.HasPrefix(kctx, "kind-") {
		t.Fatalf("refusing context %q: these tests only run against kind clusters", kctx)
	}
	h := &harness{t: t, kctx: kctx, dir: t.TempDir(),
		gitUser: env("LAUNCHWAY_TEST_ARGO_GIT_USER", "lwtest"), gitPass: env("LAUNCHWAY_TEST_ARGO_GIT_PASSWORD", "lwtest-not-a-secret"),
		log: quietLog()}
	h.db = filepath.Join(h.dir, "launchway.db")
	port := portForward(t, kctx, env("LAUNCHWAY_TEST_ARGO_GIT_NAMESPACE", "lw-git"), env("LAUNCHWAY_TEST_ARGO_GIT_SERVICE", "git"), 8080)
	h.gitURL = "http://127.0.0.1:" + port + "/" + h.gitUser + "/control.git"
	up := exec.Command("../../../deploy/test/argo/control/up.sh", "--context", kctx, "http://127.0.0.1:"+port)
	up.Stdout, up.Stderr = os.Stderr, os.Stderr
	if err := up.Run(); err != nil {
		t.Fatalf("setting up the control tests' repository: %v", err)
	}
	bare := filepath.Join(h.dir, "shop.git")
	gitRun(t, h.dir, "init", "--bare", "-b", "main", bare)
	work := filepath.Join(h.dir, "shop")
	gitRun(t, h.dir, "clone", bare, work)
	os.WriteFile(filepath.Join(work, "README"), []byte("shop\n"), 0o644)
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "shop")
	gitRun(t, work, "push", "origin", "HEAD:main")
	h.project = bare

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kctx}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	h.proxy = newProxy(t, strings.TrimPrefix(cfg.Host, "https://"))
	os.Setenv("LW_TEST_GIT_PASSWORD", h.gitPass)
	resolver := secrets.NewResolver()
	resolver.Add("env", secrets.Env{}, nil, nil)
	h.agent = &argocd.Agent{
		Config: argocd.Config{
			Targets: map[string]argocd.Target{"kind": {Kubernetes: argocd.Kubernetes{Context: kctx}, ArgoCDNamespace: "argocd",
				Applications:      []argocd.AppRule{{Projects: []string{"shop"}, Names: []string{"ctl-canary"}}},
				RolloutNamespaces: []string{"lw-ctl"}}},
			Repos: map[string]argocd.Repo{"control": {URL: h.gitURL,
				ArgoCDURL:   "http://" + env("LAUNCHWAY_TEST_ARGO_GIT_SERVICE", "git") + "." + env("LAUNCHWAY_TEST_ARGO_GIT_NAMESPACE", "lw-git") + ".svc:8080/" + h.gitUser + "/control.git",
				Credentials: &pipeline.SecretRef{Store: "env", Key: "LW_TEST_GIT_PASSWORD"}, Username: h.gitUser,
				Paths: []argocd.PathRule{{Projects: []string{"shop"}, Files: []string{"apps/ctl/**"}}}}},
		},
		DataDir: filepath.Join(h.dir, "deployer"), Secrets: resolver,
		Connect: func(string, argocd.Target) (*argocd.Cluster, error) { return h.deployerCluster(cfg.Host) },
	}
	if err := h.agent.Config.Check(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	h.startServer(ln)
	t.Cleanup(h.stopServer)
	h.startBuildRunner()
	return h
}

// deployerCluster is a client acting as the deploy runner's service
// account, through the switchable proxy.
func (h *harness) deployerCluster(string) (*argocd.Cluster, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: h.kctx}).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.Host = "https://" + h.proxy.ln.Addr().String()
	cfg.TLSClientConfig.ServerName = "localhost"
	cfg.Impersonate.UserName = deployerUser
	cfg.Timeout = 10 * time.Second
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return argocd.NewCluster(dyn, "argocd"), nil
}

// ctlPipeline is a pipeline deploying the ctl canary at tag; deploy is
// extra keys of the deploy step's rollout and the deploy step, after is
// the steps that follow it.
func ctlPipeline(tag, rollout, deployExtra, after string) string {
	return fmt.Sprintf(`pipeline: shop
steps:
  build:
    image: none
    run: echo tag=%s >> "$LAUNCHWAY_OUTPUTS"
  deploy:
    kind: deploy
    needs: build
    environment: kind
    service: ctl
    version: { output: build.tag }
    timeout: 5m
    argocd:
      application: ctl-canary
      write_back:
        repo: control
        branch: main
        changes:
          - { file: apps/ctl/rollout.yaml, image: docker.io/library/busybox, tag: { output: build.tag } }
    rollout: { name: ctl, namespace: lw-ctl%s }
%s%s`, tag, rollout, deployExtra, after)
}

func (h *harness) startSrc(src string) string {
	h.t.Helper()
	p, err := pipeline.Parse([]byte(src))
	if err != nil {
		h.t.Fatalf("%v\n%s", err, src)
	}
	sha := gitRun(h.t, h.dir, "--git-dir", h.project, "rev-parse", "main")
	run, err := h.engine().CreateRun(engine.NewRun{Project: "shop", Pipeline: p, Source: engine.Source{Kind: "git", URL: h.project},
		Trigger: engine.Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: sha, Actor: "tester"}})
	if err != nil {
		h.t.Fatal(err)
	}
	return run.ID
}

// ctl reads the rollout as the kind admin: phase, step, abort, current
// and stable pod template, image.
type ctlState struct{ phase, step, abort, current, stable, image string }

func (h *harness) ctl() ctlState {
	out := h.kubectl("-n", "lw-ctl", "get", "rollout", "ctl", "-o",
		"jsonpath={.status.phase}|{.status.currentStepIndex}|{.status.abort}|{.status.currentPodHash}|{.status.stableRS}|{.spec.template.spec.containers[0].image}")
	f := strings.Split(out, "|")
	for len(f) < 6 {
		f = append(f, "")
	}
	return ctlState{f[0], f[1], f[2], f[3], f[4], f[5]}
}

func (h *harness) ctlUntil(what string, cond func(ctlState) bool) ctlState {
	h.t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for {
		s := h.ctl()
		if cond(s) {
			return s
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("rollout ctl never %s: %+v", what, s)
		}
		time.Sleep(time.Second)
	}
}

// ctlReset brings the canary back to a healthy known version by a commit
// of the fixture files with that tag (a person's fix in Git), promoting
// or retrying the rollout as the kind admin until it is Healthy.
func (h *harness) ctlReset(tag string) {
	h.t.Helper()
	clone := filepath.Join(h.dir, "reset")
	os.RemoveAll(clone)
	gitRun(h.t, h.dir, append(h.authArgs(), "clone", "-q", "--branch", "main", h.gitURL, clone)...)
	src, _ := os.ReadFile("../../../deploy/test/argo/control/fixtures/apps/ctl/rollout.yaml")
	body := strings.Replace(string(src), "image: docker.io/library/busybox:1.36.1", "image: docker.io/library/busybox:"+tag, 1)
	os.WriteFile(filepath.Join(clone, "apps/ctl/rollout.yaml"), []byte(body), 0o644)
	gitRun(h.t, clone, "add", "-A")
	if out, _ := exec.Command("git", "-C", clone, "status", "--porcelain").Output(); len(out) > 0 {
		gitRun(h.t, clone, "commit", "-q", "-m", "reset the control fixture to "+tag)
		gitRun(h.t, clone, append(h.authArgs(), "push", "-q", "origin", "HEAD:main")...)
	}
	h.kubectl("-n", "argocd", "annotate", "application", "ctl-canary", "argocd.argoproj.io/refresh=normal", "--overwrite")
	deadline := time.Now().Add(4 * time.Minute)
	for {
		s := h.ctl()
		switch {
		case s.phase == "Healthy" && s.image == "docker.io/library/busybox:"+tag && s.current == s.stable:
			return
		case s.phase == "Paused":
			h.kubectl("-n", "lw-ctl", "patch", "rollout", "ctl", "--subresource=status", "--type=merge", "-p", `{"status":{"promoteFull":true}}`)
		case s.abort == "true":
			h.kubectl("-n", "lw-ctl", "patch", "rollout", "ctl", "--subresource=status", "--type=merge", "-p", `{"status":{"abort":false}}`)
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("the control fixture did not settle on %s: %+v", tag, s)
		}
		time.Sleep(2 * time.Second)
	}
}

// pushCtlRev commits a new pod template (env REV) outside Launchway.
func (h *harness) pushCtlRev(rev string) {
	h.t.Helper()
	clone := filepath.Join(h.dir, "rev")
	os.RemoveAll(clone)
	gitRun(h.t, h.dir, append(h.authArgs(), "clone", "-q", "--branch", "main", h.gitURL, clone)...)
	p := filepath.Join(clone, "apps/ctl/rollout.yaml")
	b, _ := os.ReadFile(p)
	i := strings.Index(string(b), "- name: REV")
	j := strings.Index(string(b)[i:], "value: ")
	k := strings.Index(string(b)[i+j:], "\n")
	body := string(b)[:i+j] + `value: "` + rev + `"` + string(b)[i+j+k:]
	os.WriteFile(p, []byte(body), 0o644)
	gitRun(h.t, clone, "commit", "-qam", "a change outside Launchway")
	gitRun(h.t, clone, append(h.authArgs(), "push", "-q", "origin", "HEAD:main")...)
}

func commandsOf(r *engine.RunDetail) []engine.CommandView {
	if len(r.Deployments) == 0 {
		return nil
	}
	return r.Deployments[0].Commands
}

func describeCommands(cs []engine.CommandView) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "[%s %s by %s: %s %s %s] ", c.Action, c.Origin, c.Actor, c.State, c.Result, c.Detail)
	}
	return b.String()
}

const (
	gatedSteps = `  smoke:
    needs: deploy
    image: none
    run: "true"
  approve:
    kind: approval
    needs: smoke
    deployment: deploy
    message: Promote the canary?
  promote:
    kind: rollout
    needs: approve
    deployment: deploy
    action: %s
    timeout: 5m
  rollback:
    kind: rollback
    deployment: deploy
    needs: [promote]
    abort_first: true
    timeout: 5m
`
	rollbackStep = `  smoke:
    needs: deploy
    image: none
    run: "true"
  rollback:
    kind: rollback
    deployment: deploy
    needs: [smoke]
    timeout: 5m
`
)

// TestArgoControl runs the control scenarios in order, each from a healthy
// canary.
func TestArgoControl(t *testing.T) {
	h := setupControl(t)
	stop := h.startDeployRunner("ctl-deployer-1")

	t.Run("the deploy runner's status patch is held to promote, abort and retry", func(t *testing.T) {
		cl, err := h.deployerCluster("")
		if err != nil {
			t.Fatal(err)
		}
		obj, err := cl.Rollout(context.Background(), "lw-ctl", "ctl")
		if err != nil {
			t.Fatal(err)
		}
		rv := obj["metadata"].(map[string]any)["resourceVersion"].(string)
		err = cl.PatchRolloutStatus(context.Background(), "lw-ctl", "ctl", rv, map[string]any{"stableRS": "not-a-hash"})
		if !errors.Is(err, argocd.ErrNoStatusRight) || !strings.Contains(err.Error(), "status.stableRS") {
			t.Fatalf("changing stableRS: %v", err)
		}
	})

	t.Run("paused canary, smoke, approval, promote, healthy", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.37.0", ", until: paused", "", fmt.Sprintf(gatedSteps, "promote")))
		r := h.until(id, "waiting for approval", func(r *engine.RunDetail) bool { return step(r, "approve").Status == engine.StepWaitingS })
		if s := h.ctl(); s.phase != "Paused" || s.step != "1" || s.current == s.stable {
			t.Fatalf("the rollout is not at its pause: %+v (%s)", s, describe(r))
		}
		if d := r.Deployments[0]; d.PodHash == "" || d.PodHash != h.ctl().current || d.Verdict != "" {
			t.Fatalf("deployment at the pause: %+v", d)
		}
		// While the approval waits, the deployment is still observed.
		before := r.Deployments[0].ObservedAt
		h.until(id, "observed again", func(r *engine.RunDetail) bool { return r.Deployments[0].ObservedAt > before })
		if err := h.engine().Decide(id, "approve", true, "approver", "canary fine"); err != nil {
			t.Fatal(err)
		}
		r = h.wait(id)
		if r.Status != engine.RunSucceeded || r.Deployments[0].Verdict != engine.VerdictDeployed {
			t.Fatalf("%s %s", describe(r), describeCommands(commandsOf(r)))
		}
		cs := commandsOf(r)
		if len(cs) != 1 || cs[0].Origin != engine.OriginStep || cs[0].Result != deploy.Applied {
			t.Fatalf("commands: %s", describeCommands(cs))
		}
		if s := h.ctl(); s.phase != "Healthy" || s.image != "docker.io/library/busybox:1.37.0" || s.current != s.stable {
			t.Fatalf("rollout %+v", s)
		}
		// Only this deployment's analysis run is shown, though Argo Rollouts
		// keeps those of earlier deployments (the seed, earlier runs of this
		// test) of the same pod template.
		var snap deploy.Snapshot
		json.Unmarshal(r.Deployments[0].Snapshot, &snap)
		rev := h.kubectl("-n", "lw-ctl", "get", "rollout", "ctl", "-o", `jsonpath={.metadata.annotations.rollout\.argoproj\.io/revision}`)
		all := h.kubectl("-n", "lw-ctl", "get", "analysisruns", "-o", "name")
		if len(snap.Analysis) != 1 || snap.Analysis[0].Revision != rev || snap.Analysis[0].Phase != "Successful" {
			t.Fatalf("analysis shown: %+v (rollout at revision %s; in the cluster: %s)", snap.Analysis, rev, all)
		}
		t.Logf("analysis runs in the cluster: %d; shown for this deployment: %s (revision %s)", len(strings.Fields(all)), snap.Analysis[0].Name, rev)
	})

	t.Run("promote-full", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.36.1", ", until: paused", "", `  full:
    kind: rollout
    needs: deploy
    deployment: deploy
    action: promote-full
    timeout: 5m
`))
		r := h.wait(id)
		if r.Status != engine.RunSucceeded || r.Deployments[0].Verdict != engine.VerdictDeployed {
			t.Fatalf("%s %s", describe(r), describeCommands(commandsOf(r)))
		}
		if cs := commandsOf(r); len(cs) != 1 || cs[0].Action != "promote-full" || cs[0].Result != deploy.Applied {
			t.Fatalf("commands: %s", describeCommands(cs))
		}
		if s := h.ctl(); s.phase != "Healthy" || s.image != "docker.io/library/busybox:1.36.1" {
			t.Fatalf("rollout %+v", s)
		}
	})

	t.Run("abort, the approval is rejected, abort-first rollback to the previous version", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.37.0", ", until: paused", "", fmt.Sprintf(gatedSteps, "promote")))
		r := h.until(id, "waiting for approval", func(r *engine.RunDetail) bool { return step(r, "approve").Status == engine.StepWaitingS })
		if _, err := h.engine().RequestCommand(engine.CommandRequest{Run: id, Deployment: r.Deployments[0].ID, Action: "abort", Actor: "oncall", Comment: "error rate"}); err != nil {
			t.Fatal(err)
		}
		r = h.wait(id)
		if a := step(r, "approve"); a.Status != engine.StepFailed || !strings.Contains(a.Error, "rejected automatically") {
			t.Fatalf("approval: %s", describe(r))
		}
		if r.Deployments[0].Verdict != engine.VerdictRolledBack || step(r, "rollback").Status != engine.StepSucceeded {
			t.Fatalf("%s %s", describe(r), describeCommands(commandsOf(r)))
		}
		if s := h.ctl(); s.phase != "Healthy" || s.image != "docker.io/library/busybox:1.36.1" || s.current != s.stable || s.abort == "true" {
			t.Fatalf("after the rollback: %+v", s)
		}
		lines, _, _ := h.engine().Logs(id, "rollback", 0, 0, 0)
		var log strings.Builder
		for _, l := range lines {
			log.WriteString(l.Text + "\n")
		}
		if !strings.Contains(log.String(), "Aborting rollout lw-ctl/ctl first") || !strings.Contains(log.String(), "already aborted") {
			t.Fatalf("the rollback's log:\n%s", log.String())
		}
	})

	t.Run("abort step, then retry, then promote", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.37.0", ", until: paused", "", `  stop:
    kind: rollout
    needs: deploy
    deployment: deploy
    action: abort
    timeout: 5m
  again:
    kind: rollout
    needs: stop
    deployment: deploy
    action: retry
    until: paused
    timeout: 5m
  promote:
    kind: rollout
    needs: again
    deployment: deploy
    action: promote
    timeout: 5m
`))
		r := h.wait(id)
		if r.Status != engine.RunSucceeded || r.Deployments[0].Verdict != engine.VerdictDeployed {
			t.Fatalf("%s %s", describe(r), describeCommands(commandsOf(r)))
		}
		cs := commandsOf(r)
		if len(cs) != 3 || cs[0].Action != "abort" || cs[1].Action != "retry" || cs[2].Action != "promote" {
			t.Fatalf("commands: %s", describeCommands(cs))
		}
		for _, c := range cs {
			if c.Result != deploy.Applied {
				t.Fatalf("commands: %s", describeCommands(cs))
			}
		}
		if s := h.ctl(); s.phase != "Healthy" || s.image != "docker.io/library/busybox:1.37.0" {
			t.Fatalf("rollout %+v", s)
		}
	})

	t.Run("promote asked for while no deploy runner runs, applied when one comes back", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.36.1", ", pauses: wait", "", rollbackStep))
		r := h.until(id, "at the pause", func(r *engine.RunDetail) bool {
			return len(r.Deployments) > 0 && r.Deployments[0].Phase == deploy.PhasePaused && r.Deployments[0].PodHash != ""
		})
		stop()
		time.Sleep(2 * time.Second)
		c, err := h.engine().RequestCommand(engine.CommandRequest{Run: id, Deployment: r.Deployments[0].ID, Action: "promote", Actor: "alice"})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(8 * time.Second)
		r, _ = h.engine().GetRun(id)
		if cs := commandsOf(r); len(cs) != 1 || cs[0].State != engine.CommandPending {
			t.Fatalf("with no runner: %s", describeCommands(cs))
		}
		if s := h.ctl(); s.phase != "Paused" {
			t.Fatalf("the rollout moved with no runner: %+v", s)
		}
		stop = h.startDeployRunner("ctl-deployer-2")
		r = h.wait(id)
		if r.Status != engine.RunSucceeded || commandsOf(r)[0].ID != c.ID || commandsOf(r)[0].Result != deploy.Applied || commandsOf(r)[0].TakenBy != "ctl-deployer-2" {
			t.Fatalf("%s %s", describe(r), describeCommands(commandsOf(r)))
		}
		if s := h.ctl(); s.phase != "Healthy" || s.image != "docker.io/library/busybox:1.36.1" {
			t.Fatalf("rollout %+v", s)
		}
	})

	t.Run("a command for a pod template that was superseded is not applied", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.37.0", ", pauses: wait", "", rollbackStep))
		r := h.until(id, "at the pause", func(r *engine.RunDetail) bool {
			return len(r.Deployments) > 0 && r.Deployments[0].Phase == deploy.PhasePaused && r.Deployments[0].PodHash != ""
		})
		hash := r.Deployments[0].PodHash
		stop()
		time.Sleep(2 * time.Second)
		if _, err := h.engine().RequestCommand(engine.CommandRequest{Run: id, Deployment: r.Deployments[0].ID, Action: "promote", Actor: "alice"}); err != nil {
			t.Fatal(err)
		}
		h.pushCtlRev("superseded")
		h.kubectl("-n", "argocd", "annotate", "application", "ctl-canary", "argocd.argoproj.io/refresh=normal", "--overwrite")
		moved := h.ctlUntil("on another pod template", func(s ctlState) bool { return s.current != hash && s.current != "" })
		stop = h.startDeployRunner("ctl-deployer-3")
		r = h.until(id, "the command done", func(r *engine.RunDetail) bool {
			cs := commandsOf(r)
			return len(cs) == 1 && cs[0].State == engine.CommandDone
		})
		if c := commandsOf(r)[0]; c.Result != deploy.NotApplied || !strings.Contains(c.Detail, "moved on to another version") {
			t.Fatalf("command: %s", describeCommands(commandsOf(r)))
		}
		time.Sleep(3 * time.Second)
		if s := h.ctl(); s.current != moved.current || s.phase == "Healthy" && s.stable == moved.current {
			t.Fatalf("the rollout was promoted: %+v", s)
		}
		if err := h.engine().Cancel(id, "tester"); err != nil {
			t.Fatal(err)
		}
		h.wait(id)
		h.ctlReset("1.36.1")
	})

	t.Run("pauses: promote", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.37.0", ", pauses: promote", "", rollbackStep))
		r := h.wait(id)
		cs := commandsOf(r)
		if r.Status != engine.RunSucceeded || len(cs) != 1 || cs[0].Origin != engine.OriginPauses || cs[0].Result != deploy.Applied {
			t.Fatalf("%s %s", describe(r), describeCommands(cs))
		}
		if s := h.ctl(); s.phase != "Healthy" || s.image != "docker.io/library/busybox:1.37.0" {
			t.Fatalf("rollout %+v", s)
		}
	})

	t.Run("pauses: fail, then the revert", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.36.1", ", pauses: fail", "", rollbackStep))
		r := h.wait(id)
		if d := step(r, "deploy"); d.Status != engine.StepFailed || !strings.Contains(d.Error, "pauses: fail") {
			t.Fatalf("%s", describe(r))
		}
		if r.Deployments[0].Verdict != engine.VerdictRolledBack {
			t.Fatalf("%s", describe(r))
		}
		if s := h.ctl(); s.phase != "Healthy" || s.image != "docker.io/library/busybox:1.37.0" {
			t.Fatalf("rollout %+v", s)
		}
	})

	t.Run("on_cancel: abort", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.36.1", ", pauses: wait", "    on_cancel: abort\n", rollbackStep))
		h.until(id, "at the pause", func(r *engine.RunDetail) bool {
			return len(r.Deployments) > 0 && r.Deployments[0].Phase == deploy.PhasePaused && r.Deployments[0].PodHash != ""
		})
		if err := h.engine().Cancel(id, "tester"); err != nil {
			t.Fatal(err)
		}
		r := h.wait(id)
		if r.Status != engine.RunCanceled || r.Deployments[0].Verdict != engine.VerdictAbandoned {
			t.Fatalf("%s", describe(r))
		}
		h.ctlUntil("aborted", func(s ctlState) bool { return s.abort == "true" })
		r = h.until(id, "the abort recorded", func(r *engine.RunDetail) bool {
			cs := commandsOf(r)
			return len(cs) == 1 && cs[0].State == engine.CommandDone
		})
		if c := commandsOf(r)[0]; c.Origin != engine.OriginOnCancel || c.Result != deploy.Applied {
			t.Fatalf("command: %s", describeCommands(commandsOf(r)))
		}
		h.ctlReset("1.37.0")
	})
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
