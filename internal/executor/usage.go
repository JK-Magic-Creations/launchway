package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Measuring what a step uses. The executor reads the cgroup of the container
// the step runs in: it is the same for every language and tool, it needs no
// help from the step and it is what the limits apply to.
//
//   - cgroup v2: memory.current (now), memory.peak (the most since the
//     container began, kernel 5.19 and later) and cpu.stat usage_usec.
//   - cgroup v1: memory/memory.usage_in_bytes, memory.max_usage_in_bytes and
//     cpuacct/cpuacct.usage.
//
// Reading is done by a small shell loop that runs in the container (see
// sampleScript), so it needs nothing from the cluster beyond the exec the
// executor already uses: no metrics server and no extra permission. The
// cluster's own metrics API was not chosen because it needs a metrics server
// and a role for it, and reports a figure averaged over 15 to 60 seconds,
// which hides the peak that causes an out-of-memory kill.
//
// Limits, stated in the result's Note: a container is shared by the steps
// of a run that use the same image, so when two of them run at once the
// numbers are the container's, not each step's; memory.peak counts from the
// container's start, so a step that does not set a new high is given the
// highest sample it was seen at instead (method "sampled"), which can miss a
// spike shorter than a second.

// Usage is what one step attempt used. An executor fills the Spec's Usage
// when it can; otherwise Measured stays false and Note says why. The
// numbers of a step that was not measured are zero, never a guess.
type Usage struct {
	Measured bool
	Method   string // cgroup2, cgroup1 or sampled
	Note     string

	MemoryPeak   int64 // bytes
	CPUPeakMilli int64 // thousandths of a CPU over the busiest sampled interval
	CPUMillis    int64 // CPU time in all, in milliseconds
	// ReservedMemory and ReservedCPUMilli are what the container the step
	// ran in had reserved; 0 if nothing was.
	ReservedMemory   int64
	ReservedCPUMilli int64
}

// notMeasured says that the step's use was not read, and why.
func (u *Usage) notMeasured(format string, a ...any) {
	if u != nil {
		*u = Usage{Note: fmt.Sprintf(format, a...), ReservedMemory: u.ReservedMemory, ReservedCPUMilli: u.ReservedCPUMilli}
	}
}

// sample is one reading of the container's cgroup.
type sample struct {
	at       time.Time
	mem      int64 // in use now
	peak     int64 // the most since the container began; 0 if the kernel does not say
	cpuMicro int64 // CPU time used since the container began, in microseconds
	method   string
}

// sampleScript prints one line "S <method> <mem> <peak> <cpu_usec> <uptime>" and, with
// the argument "loop", another every second until it is stopped. Nothing but
// sh, cat and sleep is needed (the shell's own read does what sed would), which every image the executor already
// runs steps in has (it needs sh to run them). A container with no cgroup
// files to read prints "N" once.
const sampleScript = `f=/sys/fs/cgroup
if [ -r $f/memory.current ]; then v=2
elif [ -r $f/memory/memory.usage_in_bytes ]; then v=1
else echo N; exit 0; fi
while :; do
 if [ $v = 2 ]; then
  m=$(cat $f/memory.current); p=$(cat $f/memory.peak 2>/dev/null || echo 0)
  c=0; while read -r k n; do [ "$k" = usage_usec ] && c=$n; done 2>/dev/null < $f/cpu.stat
 else
  m=$(cat $f/memory/memory.usage_in_bytes); p=$(cat $f/memory/memory.max_usage_in_bytes 2>/dev/null || echo 0)
  c=$(( $(cat $f/cpuacct/cpuacct.usage 2>/dev/null || cat $f/cpu/cpuacct.usage 2>/dev/null || echo 0) / 1000 ))
 fi
 read up _ < /proc/uptime 2>/dev/null
 echo "S cgroup$v $m $p ${c:-0} ${up:-0}"
 [ "$1" = loop ] || exit 0
 sleep 1
done`

// parseSample reads a line of sampleScript's output. ok is false for a line
// that is not a reading (the container has no cgroup files: "N").
func parseSample(line string, at time.Time) (sample, bool) {
	f := strings.Fields(line)
	if len(f) != 6 || f[0] != "S" {
		return sample{}, false
	}
	s := sample{at: at, method: f[1]}
	// The machine's uptime is a steadier clock than the arrival of a line.
	if up, err := strconv.ParseFloat(f[5], 64); err == nil && up > 0 {
		s.at = time.Unix(0, 0).Add(time.Duration(up * float64(time.Second)))
	}
	var err [3]error
	s.mem, err[0] = strconv.ParseInt(f[2], 10, 64)
	s.peak, err[1] = strconv.ParseInt(f[3], 10, 64)
	s.cpuMicro, err[2] = strconv.ParseInt(f[4], 10, 64)
	if err[0] != nil || err[1] != nil || err[2] != nil {
		return sample{}, false
	}
	return s, true
}

// meter turns readings taken before, during and after a step into a Usage.
type meter struct {
	mu      sync.Mutex
	base    sample
	hasBase bool
	last    sample
	hasLast bool
	maxMem  int64
	maxCPU  int64 // milli
	method  string
}

func (m *meter) setBase(s sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.base, m.hasBase, m.last, m.hasLast, m.method = s, true, s, true, s.method
}

// add takes a reading made while the step ran.
func (m *meter) add(s sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxMem = max(m.maxMem, s.mem)
	if m.hasLast {
		m.maxCPU = max(m.maxCPU, cpuMilli(m.last, s))
	}
	m.last, m.hasLast = s, true
	if m.method == "" {
		m.method = s.method
	}
}

// cpuMilli is the CPU used between two readings, in thousandths of a CPU.
func cpuMilli(a, b sample) int64 {
	d := b.at.Sub(a.at)
	if d < 200*time.Millisecond || b.cpuMicro < a.cpuMicro {
		return 0
	}
	return (b.cpuMicro - a.cpuMicro) * 1000 / d.Microseconds()
}

// result is the Usage after the last reading, final, was taken at the end of
// the step, which ran for took.
func (m *meter) result(final sample, took time.Duration) Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasBase {
		return Usage{Note: "the container's memory use could not be read at the start of the step"}
	}
	m.add2(final)
	u := Usage{Measured: true, Method: "sampled", MemoryPeak: m.maxMem}
	switch {
	case final.peak > m.base.peak && final.peak >= m.maxMem:
		// The step set a new high for the container, so the container's own
		// figure is exact.
		u.MemoryPeak, u.Method = final.peak, m.method
	case final.peak > 0 && m.base.peak > 0:
		u.Note = "the container used more memory earlier in the run than this step did, so the step's own peak is the highest of readings taken every second"
	default:
		u.Note = "the kernel does not report a peak, so it is the highest of readings taken every second"
	}
	u.MemoryPeak = max(u.MemoryPeak, m.base.mem)
	if final.cpuMicro >= m.base.cpuMicro {
		u.CPUMillis = (final.cpuMicro - m.base.cpuMicro) / 1000
	}
	u.CPUPeakMilli = m.maxCPU
	if took > 0 { // a step shorter than the readings: its average is all that is known
		avg := u.CPUMillis * 1000 / max(took.Milliseconds(), 1)
		u.CPUPeakMilli = max(u.CPUPeakMilli, avg)
	}
	return u
}

// add2 is add without the lock (result holds it).
func (m *meter) add2(s sample) {
	m.maxMem = max(m.maxMem, s.mem)
	if m.hasLast {
		m.maxCPU = max(m.maxCPU, cpuMilli(m.last, s))
	}
	m.last, m.hasLast = s, true
}

// readSamples reads the loop's output until it ends.
func readSamples(r io.Reader, m *meter) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if s, ok := parseSample(sc.Text(), time.Now()); ok {
			m.add(s)
		}
	}
}

// oneSample runs the sampler once through run, a function that executes a
// shell command in the container and returns its output.
func oneSample(ctx context.Context, run func(ctx context.Context, script string, args ...string) (string, error)) (sample, bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := run(ctx, sampleScript)
	if err != nil {
		return sample{}, false
	}
	for _, l := range strings.Split(out, "\n") {
		if s, ok := parseSample(strings.TrimSpace(l), time.Now()); ok {
			return s, true
		}
	}
	return sample{}, false
}
