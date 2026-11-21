package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Container runs each step in a fresh container with Podman or Docker.
// The workspace is mounted at /workspace and the step's own files at /launchway.
type Container struct {
	Bin   string // path to podman or docker
	Owner string // identifies this runner's pods, for cleaning up

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// DetectContainer finds Podman, then Docker.
func DetectContainer() (*Container, error) {
	for _, b := range []string{"podman", "docker"} {
		if p, err := exec.LookPath(b); err == nil {
			return &Container{Bin: p}, nil
		}
	}
	return nil, errors.New("neither podman nor docker was found")
}

func (c *Container) Name() string { return "container (" + filepath.Base(c.Bin) + ")" }

// WorkspacePath is /workspace, where the workspace is mounted.
func (c *Container) WorkspacePath(string) string { return ContainerWorkspace }

func (c *Container) Run(ctx context.Context, s Spec, out Sink) (int, error) {
	// A relative path would be taken as the name of a volume.
	if !filepath.IsAbs(s.Workspace) || !filepath.IsAbs(s.MetaDir) {
		return -1, errors.New("the workspace and step directories must be absolute paths")
	}
	if _, err := writeScript(s); err != nil {
		return -1, err
	}
	s.Name = containerName(s.Name)
	s = s.withParallelism()
	var podArgs []string
	if len(s.services()) > 0 {
		pod, err := c.ensurePod(ctx, s, out)
		if err != nil {
			return -1, err
		}
		podArgs = []string{"--pod", pod}
	}
	args := []string{"run", "--rm", "--init",
		"--name", s.Name,
		"--label", "launchway=true",
		"--pull=missing",
		// Do not relabel the workspace for SELinux; it may be a directory
		// the user owns, such as their own checkout with `launchway run`.
		"--security-opt", "label=disable",
		"-v", s.Workspace + ":/workspace",
		"-v", s.MetaDir + ":/launchway",
		"-w", "/workspace",
		"--entrypoint", "/bin/sh",
	}
	args = append(args, podArgs...)
	if sc, ok := s.stepContainer(); ok && !sc.Service {
		if flags := containerFlags(sc); len(flags) > 0 {
			args = append(args, flags...)
			out("system", "Container resources: "+sc.describeResources())
		}
	}
	// Pass values through the environment of the podman process rather than
	// the command line, so they never appear in a process list.
	env := os.Environ()
	keys := make([]string, 0, len(s.Env)+1)
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k)
		env = append(env, k+"="+s.Env[k])
	}
	args = append(args, "-e", "LAUNCHWAY_OUTPUTS")
	env = append(env, "LAUNCHWAY_OUTPUTS=/launchway/"+OutputsFile)
	args = append(args, s.Image, "-e", "/launchway/script.sh")

	cmd := exec.Command(c.Bin, args...)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go pipeLines(stdout, "stdout", s.Mask, out, &wg)
	go pipeLines(stderr, "stderr", s.Mask, out, &wg)

	done := make(chan error, 1)
	go func() {
		wg.Wait()
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return exitCode(err)
	case <-ctx.Done():
		// Stop the container itself; killing the client alone can leave it running.
		stop := exec.Command(c.Bin, "stop", "--time", "10", s.Name)
		_ = stop.Run()
		select {
		case <-done:
		case <-time.After(stopGrace + 5*time.Second):
			_ = exec.Command(c.Bin, "rm", "-f", s.Name).Run()
			_ = cmd.Process.Kill()
			<-done
		}
		return -1, ctx.Err()
	}
}

func (c *Container) isPodman() bool { return strings.Contains(filepath.Base(c.Bin), "podman") }

func (c *Container) podName(runID string) string {
	return containerName("launchway-" + strings.ToLower(strings.ReplaceAll(runID, "_", "-")) + "-" + c.Owner)
}

// ensurePod starts a Podman pod for the run with its services, so steps can
// reach them at localhost, the same as on Kubernetes.
func (c *Container) ensurePod(ctx context.Context, s Spec, out Sink) (string, error) {
	if !c.isPodman() {
		return "", errors.New("services need Podman or Kubernetes: Docker is not supported for services, so run this on a runner that uses Podman (executor: podman) or Kubernetes")
	}
	c.mu.Lock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	lock := c.locks[s.RunID]
	if lock == nil {
		lock = &sync.Mutex{}
		c.locks[s.RunID] = lock
	}
	c.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	pod := c.podName(s.RunID)
	labels := []string{"--label", "launchway.dev/run=" + s.RunID, "--label", "launchway.dev/runner=" + c.Owner}
	if exec.CommandContext(ctx, c.Bin, "pod", "exists", pod).Run() != nil {
		args := append([]string{"pod", "create", "--name", pod}, labels...)
		if b, err := exec.CommandContext(ctx, c.Bin, args...).CombinedOutput(); err != nil {
			// Another process may have created it in the meantime.
			if exec.CommandContext(ctx, c.Bin, "pod", "exists", pod).Run() != nil {
				return "", fmt.Errorf("creating pod %s: %s", pod, strings.TrimSpace(string(b)))
			}
		}
	}
	for _, svc := range s.services() {
		name := containerName(pod + "-" + svc.Name)
		running, _ := exec.CommandContext(ctx, c.Bin, "inspect", "-f", "{{.State.Running}}", name).Output()
		if strings.TrimSpace(string(running)) == "true" {
			continue
		}
		_ = exec.CommandContext(ctx, c.Bin, "rm", "-f", name).Run()
		args := append([]string{"run", "-d", "--pod", pod, "--name", name, "--pull=missing",
			"--security-opt", "label=disable", "-v", s.Workspace + ":/workspace"}, labels...)
		env := os.Environ()
		keys := make([]string, 0, len(svc.Env))
		for k := range svc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "-e", k)
			env = append(env, k+"="+svc.Env[k])
		}
		args = append(args, podmanHealthArgs(svc.Ready)...)
		args = append(args, svc.Image)
		args = append(args, svc.Command...)
		cmd := exec.CommandContext(ctx, c.Bin, args...)
		cmd.Env = env
		if b, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("starting service %s: %s", svc.Name, lastLine(string(b)))
		}
		out("system", fmt.Sprintf("Started service %s (%s); reach it at localhost", svc.Name, svc.Image))
		if svc.Ready != nil {
			if err := c.waitHealthy(ctx, name, svc.Name, svc.Ready, out); err != nil {
				return "", err
			}
		}
	}
	return pod, nil
}

// Cleanup removes the Podman pods (which hold a run's services) of runs that
// have finished, whichever runner on this machine made them (see removable).
// The workspace is not in the pod, so nothing is kept for a resume.
func (c *Container) Cleanup(ctx context.Context, lookup RunLookup) error {
	if !c.isPodman() || c.Owner == "" {
		return nil
	}
	b, err := exec.CommandContext(ctx, c.Bin, "pod", "ps", "--filter", "label=launchway.dev/run", "--format", "json").Output()
	if err != nil {
		return err
	}
	var pods []struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(b, &pods); err != nil || len(pods) == 0 {
		return err
	}
	ids := make([]string, 0, len(pods))
	for _, p := range pods {
		ids = append(ids, p.Labels["launchway.dev/run"])
	}
	infos, err := lookup(ctx, ids)
	if err != nil {
		return err
	}
	for _, p := range pods {
		info, known := infos[p.Labels["launchway.dev/run"]]
		if removable(info, known, p.Labels["launchway.dev/runner"] == c.Owner, 0) {
			_ = exec.CommandContext(ctx, c.Bin, "pod", "rm", "-f", p.Name).Run()
		}
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// containerName makes name acceptable to Podman and Docker:
// [a-zA-Z0-9][a-zA-Z0-9_.-]*, at most 128 characters.
func containerName(name string) string {
	b := []byte(name)
	for i, c := range b {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || (i > 0 && (c == '_' || c == '.' || c == '-'))
		if !ok {
			b[i] = '-'
			if i == 0 {
				b[i] = 's'
			}
		}
	}
	if len(b) > 128 {
		b = b[:128]
	}
	if len(b) == 0 {
		return "launchway-step"
	}
	return string(b)
}
