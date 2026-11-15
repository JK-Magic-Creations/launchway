// Package executor runs one step's script somewhere: in a container, or
// directly on the host.
package executor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Spec is one step attempt to execute.
type Spec struct {
	RunID      string            // the run it belongs to
	Containers []ContainerSpec   // every container the run needs, services included
	Container  string            // which of Containers this step runs in
	Name       string            // unique, safe for a container name
	Image      string            // the image of Container; ignored by the host executor
	Script     string            // run with sh -e
	Env        map[string]string // added to the step's environment
	Workspace  string            // host directory the step works in
	// WorkspaceID names the run's workspace: the same for the steps that
	// share it, different when the run starts again in a fresh one.
	WorkspaceID string
	MetaDir     string // host directory for the script and outputs file
	// Mask, if set, hides secret values in stdout and stderr. It is applied
	// to whole lines, before long lines are split.
	Mask func(string) string
}

// Sink receives output lines as they are produced.
type Sink func(stream, text string)

// Executor runs a step. It returns the exit code. A non-nil error means the
// step could not be run at all, or ctx ended before it finished.
type Executor interface {
	Name() string
	Run(ctx context.Context, s Spec, out Sink) (exitCode int, err error)
	// WorkspacePath is where a step sees the workspace that is hostDir on
	// the runner: /workspace in a container, hostDir itself on the host.
	WorkspacePath(hostDir string) string
}

// ContainerWorkspace is where the workspace is in a step's container.
const ContainerWorkspace = "/workspace"

// ContainerSpec is one of a run's containers.
type ContainerSpec struct {
	Name    string
	Image   string
	Env     map[string]string // services only; a step's environment already includes its container's
	CPU     string
	Memory  string
	Service bool
	Command []string
}

// services returns the service containers.
func (s Spec) services() []ContainerSpec {
	var out []ContainerSpec
	for _, c := range s.Containers {
		if c.Service {
			out = append(out, c)
		}
	}
	return out
}

// RunInfo is what the server says about a run, for cleaning up after it.
type RunInfo struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	FinishedAt int64  `json:"finished_at"`
}

// RunLookup asks the server about runs. Runs it does not know are omitted.
type RunLookup func(ctx context.Context, ids []string) (map[string]RunInfo, error)

// Cleaner is implemented by executors that hold resources per run.
type Cleaner interface {
	Cleanup(ctx context.Context, lookup RunLookup) error
}

// removable decides whether to remove what a run's steps held (a pod), given
// what the server says about the run. A run that is still active keeps it.
// A run that succeeded does not need it; a failed or canceled run keeps it
// for keep after it finished, so that a resume can use it. Any runner may
// remove what belongs to such runs. A run the server does not know may be
// another installation's, or a local `launchway run` sharing the namespace,
// so only the runner that made it (mine) removes it.
func removable(info RunInfo, known, mine bool, keep time.Duration) bool {
	if !known {
		return mine
	}
	switch info.Status {
	case "succeeded":
		return true
	case "failed", "canceled":
		return time.Since(time.UnixMilli(info.FinishedAt)) > keep
	}
	return false
}

// OutputsFile is where a step writes key=value outputs, relative to MetaDir.
const OutputsFile = "outputs"

// ReadOutputs parses the outputs file a step wrote, if any.
func ReadOutputs(metaDir string) map[string]string {
	b, err := os.ReadFile(filepath.Join(metaDir, OutputsFile))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || len(out) >= 100 {
			continue
		}
		if len(v) > 4096 {
			v = v[:4096]
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Auto picks the container executor if Podman or Docker is installed, and
// the host executor otherwise.
func Auto() Executor {
	if c, err := DetectContainer(); err == nil {
		return c
	}
	return &Host{}
}

// Options carry settings that only some executors need.
type Options struct {
	Kubernetes KubernetesOptions
	RunnerID   string
}

// ByName returns the executor with the given name: auto, container, podman,
// docker, host or kubernetes.
func ByName(name string, o Options) (Executor, error) {
	switch name {
	case "kubernetes", "k8s":
		return NewKubernetes(o.Kubernetes, o.RunnerID)
	}
	ex, err := byNameLocal(name)
	if c, ok := ex.(*Container); ok {
		c.Owner = ownerID(o.RunnerID)
	}
	return ex, err
}

func ownerID(runnerID string) string {
	sum := sha256.Sum256([]byte(runnerID))
	return hex.EncodeToString(sum[:])[:10]
}

func byNameLocal(name string) (Executor, error) {
	switch name {
	case "", "auto":
		return Auto(), nil
	case "container", "podman", "docker":
		if name == "podman" || name == "docker" {
			p, err := exec.LookPath(name)
			if err != nil {
				return nil, err
			}
			return &Container{Bin: p}, nil
		}
		return DetectContainer()
	case "host":
		return &Host{}, nil
	}
	return nil, errors.New("unknown executor " + name + "; use auto, container, podman, docker, host or kubernetes")
}

const (
	// maxLine is the longest line sent on; longer lines are split.
	maxLine = 64 * 1024
	// maxMaskedLine is how much of a line is gathered before masking.
	maxMaskedLine = 1024 * 1024
)

// pipeLines copies r to out line by line, masking each line and splitting
// lines longer than maxLine. A line is masked whole before it is split, so a
// split never cuts a secret in two. A line longer than maxMaskedLine is
// masked in pieces of that size, so a secret that straddles a piece boundary
// in such a line is not masked; ordinary output never has lines that long.
func pipeLines(r io.Reader, stream string, mask func(string) string, out Sink, wg *sync.WaitGroup) {
	defer wg.Done()
	br := bufio.NewReaderSize(r, maxLine)
	var buf []byte
	emit := func(s string) {
		if mask != nil {
			s = mask(s)
		}
		for len(s) > maxLine {
			out(stream, s[:maxLine])
			s = s[maxLine:]
		}
		out(stream, s)
	}
	for {
		chunk, isPrefix, err := br.ReadLine()
		buf = append(buf, chunk...)
		if err == nil && (!isPrefix || len(buf) >= maxMaskedLine) {
			emit(strings.TrimRight(string(buf), "\r"))
			buf = buf[:0]
		}
		if err != nil {
			if len(buf) > 0 {
				emit(string(buf))
			}
			return
		}
	}
}

// stopGrace is how long a step gets to stop after being asked.
const stopGrace = 10 * time.Second

func writeScript(s Spec) (string, error) {
	if err := os.MkdirAll(s.MetaDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(s.MetaDir, "script.sh")
	if err := os.WriteFile(path, []byte(s.Script+"\n"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(s.MetaDir, OutputsFile), nil, 0o666); err != nil {
		return "", err
	}
	// The container may run as a different user; let it write outputs.
	_ = os.Chmod(filepath.Join(s.MetaDir, OutputsFile), 0o666)
	_ = os.Chmod(s.MetaDir, 0o777)
	return path, nil
}

func exitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}
