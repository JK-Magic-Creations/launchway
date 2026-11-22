// Package executor runs one step's script somewhere: in a container, or
// directly on the host.
package executor

import (
	"bytes"
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
	"unicode/utf8"
)

// Spec is one step attempt to execute.
type Spec struct {
	RunID      string            // the run it belongs to
	StepID     string            // the step within the run (empty for a caller that has none)
	Attempt    int               // which attempt of the step this is
	LeaseID    string            // the lease this attempt runs under
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
	// Hold, if set, says how many bytes at the end of text without a
	// newline could be the start of a secret (see secrets.Masker.Hold).
	Hold func(string) int
	// AsksResources says that this step wrote resources of its own. An
	// executor that cannot limit anything refuses such a step rather than
	// ignore it; for a step that asks for nothing it only says that the
	// defaults are not enforced. It is about the step, not the container:
	// another step that runs in the same container may ask while this one
	// does not.
	AsksResources bool
	// Cache, if set, says which paths of the workspace the step's caches own (see CacheScope). Exclude lists
	// workspace paths (globs as for artifacts) that an executor which carries the workspace from step to
	// step through the Station leaves out: what the step makes there stays in the step.
	Cache   *CacheScope
	Exclude []string
	// Sandbox, if set, says that the step is a custom step and what it may
	// do (see sandbox.go).
	Sandbox *Sandbox
	// Usage, if set, is filled by an executor that can read what the step
	// used (see usage.go); the caller reads it after Run returns.
	Usage *Usage
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
	CPU     string            // promised, such as 500m or 2
	Memory  string            // promised, such as 2Gi
	Service bool
	Command []string
	// Ready, for a service, says when it is ready; steps start after (nil:
	// when it has started).
	Ready *Ready

	// What the server worked out from the steps' resources and the
	// installation's defaults (see pipeline.Container); zero for a service.
	CPULimit    string // the most CPU it may use; empty: no limit
	MemoryLimit string // the most memory it may use; empty: Memory
	// Threads is how many parallel jobs the tools in it should start so that
	// they fit its limits; 0 leaves the tools to decide.
	Threads int
	// ExplicitCPU and ExplicitMemory say that the container's CPU, or
	// memory, was asked for by a step or by the container itself, and is
	// not a default the server chose. An executor whose operator or
	// namespace allows less fails a step for what it asked for, and lowers
	// a default (see Kubernetes.limitDefaults).
	ExplicitCPU, ExplicitMemory bool
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

// Releaser is implemented by executors that hold resources per run and can
// give them back at once when the server takes a step away from the runner
// (its lease was lost) instead of waiting for the run to end. It is a no-op
// while another step of the run is executing in this process.
type Releaser interface {
	ReleaseRun(ctx context.Context, runID string) error
}

// removable decides whether to remove what a run's steps held (a pod), given
// what the server says about the run. A run that is still active keeps it.
// A run that succeeded does not need it; a failed or canceled run keeps it
// for keep after it finished (0: it goes at once). A resume does not need it
// either: the runner keeps the run's checkout and a new pod is filled from
// it. Any runner may
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
	// DataDir is the runner's data directory; the Kubernetes executor keeps its copies of workspaces in a
	// directory of it unless kubernetes.work_dir says otherwise.
	DataDir string
	// AllowUnsandboxed lets the host executor run custom steps (see Host).
	AllowUnsandboxed bool
}

// ByName returns the executor with the given name: auto, container, podman,
// docker, host or kubernetes.
func ByName(name string, o Options) (Executor, error) {
	switch name {
	case "kubernetes", "k8s":
		if o.Kubernetes.WorkDir == "" && o.DataDir != "" {
			o.Kubernetes.WorkDir = filepath.Join(o.DataDir, "kubernetes")
		}
		return NewKubernetes(o.Kubernetes, o.RunnerID)
	}
	ex, err := byNameLocal(name)
	if c, ok := ex.(*Container); ok {
		c.Owner = ownerID(o.RunnerID)
	}
	if h, ok := ex.(*Host); ok {
		h.AllowUnsandboxed = o.AllowUnsandboxed
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

// partialIdle is how long output without a newline waits before it is sent
// on as it is: a progress bar or a prompt that never ends its line still
// shows. The rest of such a line, when it comes, is sent as a line of its
// own.
var partialIdle = 250 * time.Millisecond

// pipeLines copies r to out line by line, masking each line and splitting
// lines longer than maxLine. A line is masked whole before it is split, so a
// split never cuts a secret in two. A line longer than maxMaskedLine is
// masked in pieces of that size, so a secret that straddles a piece boundary
// in such a line is not masked; ordinary output never has lines that long.
//
// Output is sent on as it is read, not when a buffer fills. Text without a
// newline that stays idle for partialIdle is sent too, except for its end
// when that could be the start of a secret (hold says how many bytes): those
// wait for more text, so a secret is never cut in two.
func pipeLines(r io.Reader, stream string, mask func(string) string, hold func(string) int, out Sink, wg *sync.WaitGroup) {
	defer wg.Done()
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
	chunks := make(chan []byte, 16)
	go func() {
		defer close(chunks)
		for {
			b := make([]byte, 32*1024)
			n, err := r.Read(b)
			if n > 0 {
				chunks <- b[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	var pending []byte
	// whole: the text of the line so far was sent as it was (idle), so a
	// newline that now ends it does not add an empty line.
	whole := false
	idle := time.NewTimer(time.Hour)
	idle.Stop()
	defer idle.Stop()
	for {
		select {
		case c, ok := <-chunks:
			if !ok {
				if len(pending) > 0 {
					emit(strings.TrimRight(string(pending), "\r"))
				}
				return
			}
			pending = append(pending, c...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					if len(pending) >= maxMaskedLine {
						emit(string(pending))
						pending = pending[:0]
					}
					break
				}
				if line := strings.TrimRight(string(pending[:i]), "\r"); line != "" || !whole {
					emit(line)
				}
				whole = false
				pending = pending[i+1:]
			}
			if len(pending) == 0 {
				pending = nil
				idle.Stop()
			} else {
				idle.Reset(partialIdle)
			}
		case <-idle.C:
			n := len(pending)
			if hold != nil {
				n -= hold(string(pending))
			}
			// Do not cut a multi-byte character.
			for n > 0 && n < len(pending) && !utf8.RuneStart(pending[n]) {
				n--
			}
			if n > 0 {
				emit(string(pending[:n]))
				whole = n == len(pending)
				pending = append([]byte(nil), pending[n:]...)
			}
			if len(pending) > 0 {
				idle.Reset(partialIdle)
			}
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

// CacheScope is the part of a step's workspace that its caches own. An executor that carries the workspace
// from one step to the next through the Station does not carry these paths with it: the cache is restored into
// the step's own pod and saved from there (Back), so a cache of gigabytes does not also travel with the
// workspace. The runner fills it in before the step runs.
type CacheScope struct {
	Paths []string // every cache path of the step, as declared (globs as for artifacts)
	Back  []string // the paths of the caches that are saved after the step: what the step left there is handed back
}

// under reports whether a workspace path is one of the globs or below one.
func under(globs []string, name string) bool {
	for _, g := range globs {
		if matchArtifact(g, name) {
			return true
		}
	}
	return false
}

// apart says that a workspace path does not travel with the workspace: it is a cache's, or the pipeline left it out.
func (s Spec) apart(name string) bool {
	return (s.Cache != nil && under(s.Cache.Paths, name)) || under(s.Exclude, name)
}
