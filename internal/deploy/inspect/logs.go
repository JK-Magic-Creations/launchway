package inspect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Limits on log reading. They bound what one request can cost the cluster,
// the runner and the server.
const (
	MaxTargets     = 20      // pods of one aggregated request
	MaxLineBytes   = 8 << 10 // a longer line is cut
	MaxStreamBytes = 8 << 20 // one request, in total
	MaxLinesPerSec = 2000    // per request; more are dropped and counted
	MaxTail        = 5000
	MaxFollow      = 15 * time.Minute
)

// LogOptions say which part of a container's log to read.
type LogOptions struct {
	Follow       bool
	Previous     bool
	TailLines    int64
	SinceSeconds int64
}

// LogSource opens a container's log, with timestamps on every line.
type LogSource interface {
	Open(ctx context.Context, ns, pod, container string, o LogOptions) (io.ReadCloser, error)
}

// ClientLogs reads logs through the Kubernetes API.
type ClientLogs struct{ cs kubernetes.Interface }

// NewClientLogs makes a LogSource from a cluster configuration.
func NewClientLogs(cfg *rest.Config) (*ClientLogs, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &ClientLogs{cs: cs}, nil
}

// Open implements LogSource.
func (c *ClientLogs) Open(ctx context.Context, ns, pod, container string, o LogOptions) (io.ReadCloser, error) {
	opt := &corev1.PodLogOptions{Container: container, Follow: o.Follow, Previous: o.Previous, Timestamps: true}
	if o.TailLines > 0 {
		t := min(o.TailLines, MaxTail)
		opt.TailLines = &t
	}
	if o.SinceSeconds > 0 {
		opt.SinceSeconds = &o.SinceSeconds
	}
	lim := int64(MaxStreamBytes)
	opt.LimitBytes = &lim
	return c.cs.CoreV1().Pods(ns).GetLogs(pod, opt).Stream(ctx)
}

// LogTarget is one container to read, tagged with where its lines came from.
type LogTarget struct {
	Namespace, Pod, Container, Source string
}

// LogLine is one line of output.
type LogLine struct {
	T    int64  `json:"t"` // Unix ms, from the container runtime
	Src  string `json:"src"`
	Line string `json:"line"`
}

// LogSpec says what to read.
type LogSpec struct {
	// Exactly one of: Pod (with Container), ReplicaSet, Role (stable,
	// canary, preview, active), Job.
	Pod, Container, ReplicaSet, Role, Job string
	LogOptions
}

// Targets resolves a spec to containers of pods that are in the tree. A
// pod that is not in the release's tree is never read.
func (t *Tree) Targets(s LogSpec) ([]LogTarget, []string, error) {
	var notes []string
	var pods []*Node
	byParent := func(parent string) {
		for i := range t.Nodes {
			if t.Nodes[i].Kind == "Pod" && t.Nodes[i].Parent == parent {
				pods = append(pods, &t.Nodes[i])
			}
		}
	}
	switch {
	case s.Pod != "":
		for i := range t.Nodes {
			if t.Nodes[i].Kind == "Pod" && (t.Nodes[i].ID == s.Pod || t.Nodes[i].Name == s.Pod) {
				pods = append(pods, &t.Nodes[i])
			}
		}
	case s.ReplicaSet != "":
		byParent(s.ReplicaSet)
		if len(pods) == 0 {
			byParent("ReplicaSet/" + firstNS(t) + "/" + s.ReplicaSet)
		}
	case s.Job != "":
		byParent(s.Job)
	case s.Role != "":
		for i := range t.Nodes {
			if t.Nodes[i].Kind == "ReplicaSet" && t.Nodes[i].Role == s.Role {
				byParent(t.Nodes[i].ID)
			}
		}
	default:
		return nil, nil, errors.New("say which pod, ReplicaSet, role or Job to read")
	}
	if len(pods) == 0 {
		return nil, nil, ErrNotInRelease
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	if len(pods) > MaxTargets {
		notes = append(notes, fmt.Sprintf("%d pods match; the first %d are read", len(pods), MaxTargets))
		pods = pods[:MaxTargets]
	}
	var out []LogTarget
	for _, p := range pods {
		src := p.Name
		if s.Role != "" {
			src = s.Role + "/" + p.Name
		}
		for _, c := range p.Containers {
			if c.Init || s.Container != "" && c.Name != s.Container {
				continue
			}
			tag := src
			if len(p.Containers) > 1 && s.Container == "" {
				tag += "/" + c.Name
			}
			out = append(out, LogTarget{Namespace: p.Namespace, Pod: p.Name, Container: c.Name, Source: tag})
		}
		if s.Container != "" && !slicesHas(p.Containers, s.Container) {
			return nil, nil, fmt.Errorf("pod %s has no container %s", p.Name, s.Container)
		}
	}
	return out, notes, nil
}

func firstNS(t *Tree) string {
	if len(t.Namespaces) > 0 {
		return t.Namespaces[0]
	}
	return ""
}

func slicesHas(cs []Container, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
}

// Logger reads and redacts logs.
type Logger struct {
	Src LogSource
	Red *Redactor
}

// splitStamp splits "2026-11-21T10:00:00.123456789Z text".
func splitStamp(s string) (int64, string) {
	i := strings.IndexByte(s, ' ')
	if i < 20 || i > 40 {
		return 0, s
	}
	t, err := time.Parse(time.RFC3339Nano, s[:i])
	if err != nil {
		return 0, s
	}
	return t.UnixMilli(), s[i+1:]
}

// Read reads the targets and calls emit with batches of lines, redacted.
// Without Follow it returns after the tail of each, merged by time; with
// it, it runs until ctx ends, the limits are hit or emit returns false.
// The returned notes say what was cut or dropped.
func (lg *Logger) Read(ctx context.Context, targets []LogTarget, o LogOptions, emit func([]LogLine) bool) (notes []string, err error) {
	if o.TailLines <= 0 {
		o.TailLines = 200
	}
	if !o.Follow {
		var all []LogLine
		var firstErr error
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, t := range targets {
			wg.Add(1)
			go func() {
				defer wg.Done()
				lines, e := lg.readOne(ctx, t, o, nil)
				mu.Lock()
				defer mu.Unlock()
				all = append(all, lines...)
				if e != nil && firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", t.Source, e)
				}
			}()
		}
		wg.Wait()
		sort.SliceStable(all, func(i, j int) bool { return all[i].T < all[j].T })
		if len(all) > int(o.TailLines) && len(targets) > 1 {
			all = all[len(all)-int(o.TailLines):]
		}
		if len(all) > 0 {
			emit(all)
		}
		if len(all) == 0 && firstErr != nil {
			return nil, firstErr
		}
		if firstErr != nil {
			notes = append(notes, firstErr.Error())
		}
		return notes, nil
	}
	ctx, cancel := context.WithTimeout(ctx, MaxFollow)
	defer cancel()
	out := make(chan LogLine, 1024)
	var wg sync.WaitGroup
	var dropped, total int64
	var cmu sync.Mutex
	for _, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = lg.readOne(ctx, t, o, func(l LogLine) {
				cmu.Lock()
				total += int64(len(l.Line))
				over := total > MaxStreamBytes
				cmu.Unlock()
				if over {
					cancel()
					return
				}
				select {
				case out <- l:
				default:
					cmu.Lock()
					dropped++
					cmu.Unlock()
				}
			})
		}()
	}
	go func() { wg.Wait(); close(out) }()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var batch []LogLine
	window, count := time.Now(), 0
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		sort.SliceStable(batch, func(i, j int) bool { return batch[i].T < batch[j].T })
		ok := emit(batch)
		batch = nil
		return ok
	}
	for {
		select {
		case l, ok := <-out:
			if !ok {
				flush()
				return limitNotes(dropped, total), nil
			}
			if time.Since(window) >= time.Second {
				window, count = time.Now(), 0
			}
			if count++; count > MaxLinesPerSec {
				cmu.Lock()
				dropped++
				cmu.Unlock()
				continue
			}
			batch = append(batch, l)
		case <-tick.C:
			if !flush() {
				cancel()
				return limitNotes(dropped, total), nil
			}
		case <-ctx.Done():
			flush()
			return limitNotes(dropped, total), nil
		}
	}
}

func limitNotes(dropped, total int64) []string {
	var n []string
	if dropped > 0 {
		n = append(n, fmt.Sprintf("%d lines were dropped: the output is faster than %d lines a second", dropped, MaxLinesPerSec))
	}
	if total > MaxStreamBytes {
		n = append(n, fmt.Sprintf("the stream stopped after %d MiB", MaxStreamBytes>>20))
	}
	return n
}

// readOne reads one container. With sink nil it returns the lines.
func (lg *Logger) readOne(ctx context.Context, t LogTarget, o LogOptions, sink func(LogLine)) ([]LogLine, error) {
	rc, err := lg.Src.Open(ctx, t.Namespace, t.Pod, t.Container, o)
	if err != nil {
		return nil, explainLogErr(err, t)
	}
	defer rc.Close()
	var lines []LogLine
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64<<10), MaxLineBytes*4)
	for sc.Scan() {
		at, text := splitStamp(sc.Text())
		if len(text) > MaxLineBytes {
			text = text[:MaxLineBytes] + "… (line cut)"
		}
		l := LogLine{T: at, Src: t.Source, Line: lg.Red.Text(text)}
		if sink != nil {
			sink(l)
		} else {
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return lines, err
	}
	return lines, nil
}

func explainLogErr(err error, t LogTarget) error {
	s := err.Error()
	switch {
	case strings.Contains(s, "forbidden") || strings.Contains(s, "Forbidden"):
		return fmt.Errorf("this deploy runner may not read the logs of pod %s/%s; an administrator can allow it with the chart value delivery.deployRunner.rbac.inspect.logs", t.Namespace, t.Pod)
	case strings.Contains(s, "previous terminated container"):
		return fmt.Errorf("container %s has no earlier instance to read", t.Container)
	case strings.Contains(s, "is waiting to start"), strings.Contains(s, "ContainerCreating"):
		return fmt.Errorf("container %s has not started, so it has no log yet", t.Container)
	}
	return err
}
