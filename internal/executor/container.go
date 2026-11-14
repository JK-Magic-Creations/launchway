package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Container runs each step in a fresh container with Podman or Docker.
// The workspace is mounted at /workspace and the step's own files at /slipway.
type Container struct {
	Bin string // path to podman or docker
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

func (c *Container) Run(ctx context.Context, s Spec, out Sink) (int, error) {
	// A relative path would be taken as the name of a volume.
	if !filepath.IsAbs(s.Workspace) || !filepath.IsAbs(s.MetaDir) {
		return -1, errors.New("the workspace and step directories must be absolute paths")
	}
	if _, err := writeScript(s); err != nil {
		return -1, err
	}
	s.Name = containerName(s.Name)
	args := []string{"run", "--rm", "--init",
		"--name", s.Name,
		"--label", "slipway=true",
		"--pull=missing",
		// Do not relabel the workspace for SELinux; it may be a directory
		// the user owns, such as their own checkout with `slipway run`.
		"--security-opt", "label=disable",
		"-v", s.Workspace + ":/workspace",
		"-v", s.MetaDir + ":/slipway",
		"-w", "/workspace",
		"--entrypoint", "/bin/sh",
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
	args = append(args, "-e", "SLIPWAY_OUTPUTS")
	env = append(env, "SLIPWAY_OUTPUTS=/slipway/"+OutputsFile)
	args = append(args, s.Image, "-e", "/slipway/script.sh")

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
		return "slipway-step"
	}
	return string(b)
}
