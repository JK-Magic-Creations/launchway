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
type Host struct{}

func (*Host) Name() string { return "host" }

func (*Host) Run(ctx context.Context, s Spec, out Sink) (int, error) {
	if len(s.services()) > 0 {
		return -1, errors.New("services need the container or kubernetes executor")
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
	go pipeLines(stdout, "stdout", out, &wg)
	go pipeLines(stderr, "stderr", out, &wg)
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
