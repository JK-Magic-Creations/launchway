package executor

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// startMeasuring begins reading what the step uses in its container (see
// usage.go) and returns what ends it: finish(took) after the step ended
// normally fills the Spec's Usage; finish(-1), or a second call, only stops
// the reading. A step is never failed or delayed for long by measuring: if a
// reading fails, the step is "not measured" and says why.
func (k *Kubernetes) startMeasuring(ctx context.Context, pod, container string, s Spec) (finish func(took time.Duration)) {
	if s.Usage == nil {
		return func(time.Duration) {}
	}
	u := s.Usage
	*u = Usage{}
	u.ReservedMemory, u.ReservedCPUMilli = k.reserved(s)
	run := func(ctx context.Context, script string, args ...string) (string, error) {
		var out bytes.Buffer
		cmd := append([]string{"sh", "-c", script, "sh"}, args...)
		err := k.exec(ctx, pod, container, cmd, nil, &out, io.Discard)
		return out.String(), err
	}
	m := &meter{}
	base, ok := oneSample(ctx, run)
	if !ok {
		u.notMeasured("the container has no cgroup that can be read, or reading it failed, so what the step used is not known")
		return func(time.Duration) {}
	}
	m.setBase(base)
	finalCtx := context.WithoutCancel(ctx) // the step ending must not stop the last reading
	loopCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	pr, pw := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		readSamples(pr, m)
	}()
	go func() {
		defer wg.Done()
		// The loop writes its process ID where finish can find it, so that it can be ended from the inside and its
		// stream closed by the container, not torn down by the client (which the client logs as an error).
		_ = k.exec(loopCtx, pod, container, []string{"sh", "-c", "echo $$ > " + samplerPID + "\n" + sampleScript, "sh", "loop"}, nil, pw, io.Discard)
		pw.Close()
	}()
	var once sync.Once
	return func(took time.Duration) {
		once.Do(func() {
			if took < 0 {
				stop() // the step did not end normally: the container may be gone
				wg.Wait()
				return
			}
			k.endSampler(finalCtx, pod, container, &wg, stop)
			final, ok := oneSample(finalCtx, run)
			if !ok {
				u.notMeasured("the container's use could not be read at the end of the step")
				return
			}
			r := m.result(final, took)
			r.ReservedMemory, r.ReservedCPUMilli = u.ReservedMemory, u.ReservedCPUMilli
			*u = r
		})
	}
}

// reserved is what the step's container has reserved: its own request, else
// the runner's default.
func (k *Kubernetes) reserved(s Spec) (memory, cpuMilli int64) {
	sp, ok := s.stepContainer()
	if !ok {
		return 0, 0
	}
	a := sp.amount()
	memory, cpuMilli = a.Memory, a.CPU
	if memory == 0 && k.o.Memory != "" {
		if q, err := resource.ParseQuantity(k.o.Memory); err == nil {
			memory = q.Value()
		}
	}
	if cpuMilli == 0 && k.o.CPU != "" {
		if q, err := resource.ParseQuantity(k.o.CPU); err == nil {
			cpuMilli = q.MilliValue()
		}
	}
	return memory, cpuMilli
}

// samplerPID is where the sampling loop of a step's container writes its process ID (the container's /launchway
// volume is in memory and is the step's own).
const samplerPID = "/launchway/sampler.pid"

// endSampler ends the sampling loop from the inside and waits for its stream to close. Cancelling the stream
// instead makes the client log "Copying stdout failed ... use of closed network connection" for every step. If the
// container cannot be reached the stream is cancelled after a few seconds.
func (k *Kubernetes) endSampler(ctx context.Context, pod, container string, wg *sync.WaitGroup, cancel context.CancelFunc) {
	kctx, stopK := context.WithTimeout(ctx, 5*time.Second)
	defer stopK()
	_ = k.exec(kctx, pod, container, []string{"sh", "-c", `kill "$(cat ` + samplerPID + `)" 2>/dev/null; true`}, nil, io.Discard, io.Discard)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
	}
}
