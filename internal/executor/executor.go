// Package executor runs one step's script somewhere: in a container, or
// directly on the host.
package executor

import (
	"bufio"
	"context"
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
	RunID     string            // the run it belongs to
	Images    []string          // every image the run uses (the Kubernetes executor starts them together)
	Name      string            // unique, safe for a container name
	Image     string            // ignored by the host executor
	Script    string            // run with sh -e
	Env       map[string]string // added to the step's environment
	Workspace string            // host directory the step works in
	MetaDir   string            // host directory for the script and outputs file
}

// Sink receives output lines as they are produced.
type Sink func(stream, text string)

// Executor runs a step. It returns the exit code. A non-nil error means the
// step could not be run at all, or ctx ended before it finished.
type Executor interface {
	Name() string
	Run(ctx context.Context, s Spec, out Sink) (exitCode int, err error)
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

// pipeLines copies r to out line by line, splitting very long lines.
func pipeLines(r io.Reader, stream string, out Sink, wg *sync.WaitGroup) {
	defer wg.Done()
	br := bufio.NewReaderSize(r, 64*1024)
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 || (err == nil && !isPrefix) {
			buf = append(buf, chunk...)
		}
		if err == nil && (!isPrefix || len(buf) >= 64*1024) {
			out(stream, strings.TrimRight(string(buf), "\r"))
			buf = buf[:0]
		}
		if err != nil {
			if len(buf) > 0 {
				out(stream, string(buf))
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
