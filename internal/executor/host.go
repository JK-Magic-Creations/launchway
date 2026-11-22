package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Host runs the script directly on this machine, ignoring the image. It is
// for machines without a container runtime, and for tests.
type Host struct {
	// AllowUnsandboxed lets the host run custom steps although it enforces none of what their
	// manifests ask for (no network, a read-only workspace). Off by default.
	AllowUnsandboxed bool
}

func (*Host) Name() string { return "host" }

// WorkspacePath is hostDir: the host executor runs steps in it directly.
func (*Host) WorkspacePath(hostDir string) string { return hostDir }

func (h *Host) Run(ctx context.Context, s Spec, out Sink) (int, error) {
	if s.Sandbox != nil {
		if !h.AllowUnsandboxed {
			return -1, cannotStart("%v", errHostCannotSandbox(s.Sandbox.Name))
		}
		out("system", "The host executor does not enforce the step's sandbox ("+s.Sandbox.Describe()+"); the runner allows steps that are not sandboxed")
	}
	if len(s.services()) > 0 {
		return -1, errors.New("services need the container executor with Podman, or the kubernetes executor: this runner uses the host executor")
	}
	if c, ok := s.stepContainer(); ok {
		// The host can limit nothing. A step that asked for resources is
		// refused; resources it only has by default (the installation's, or
		// its container's own cpu and memory) are not applied, and the log
		// says so once, so that nobody believes the step is limited.
		if s.AsksResources {
			return -1, errHostCannotLimit(c)
		}
		if a := c.amount(); !a.IsZero() && !c.Service {
			out("system", "resources not enforced on the host executor: "+a.Describe()+
				" (the installation's default or the container's own) is not applied; this step runs without limits. "+
				"The podman, docker and kubernetes executors enforce it")
		}
	}
	if s.Usage != nil {
		*s.Usage = Usage{}
		s.Usage.notMeasured("the host executor runs the step as a process of the Station, with no container or cgroup of its own, so what it used is not measured")
	}
	script, err := writeScript(s)
	if err != nil {
		return -1, err
	}
	cmd := exec.Command("/bin/sh", "-e", script)
	cmd.Dir = s.Workspace
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=C.UTF-8",
		"LAUNCHWAY_OUTPUTS=" + filepath.Join(s.MetaDir, OutputsFile),
	}
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Its own process group, so stopping it stops everything it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	go pipeLines(stdout, "stdout", s.Mask, s.Hold, out, &wg)
	go pipeLines(stderr, "stderr", s.Mask, s.Hold, out, &wg)
	done := make(chan error, 1)
	go func() {
		wg.Wait()
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return exitCode(err)
	case <-ctx.Done():
		pgid := -cmd.Process.Pid
		_ = syscall.Kill(pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(stopGrace):
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			<-done
		}
		return -1, ctx.Err()
	}
}
