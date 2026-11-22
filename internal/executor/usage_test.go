package executor

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseSample(t *testing.T) {
	s, ok := parseSample("S cgroup2 1048576 2097152 5000000 1000.50", time.Now())
	if !ok || s.mem != 1048576 || s.peak != 2097152 || s.cpuMicro != 5000000 || s.method != "cgroup2" {
		t.Fatalf("%+v %v", s, ok)
	}
	for _, bad := range []string{"N", "", "S cgroup2 1 2", "S cgroup2 x 2 3 4", "hello world"} {
		if _, ok := parseSample(bad, time.Now()); ok {
			t.Errorf("%q read as a sample", bad)
		}
	}
}

func at(sec float64) time.Time { return time.Unix(0, 0).Add(time.Duration(sec * float64(time.Second))) }

// The peak is the container's own figure when the step set a new high; the
// highest reading otherwise; CPU is worked out from the CPU time between
// readings. A step that was not measured has no numbers.
func TestMeterPeaks(t *testing.T) {
	m := &meter{}
	m.setBase(sample{at: at(0), mem: 100, peak: 500, cpuMicro: 0, method: "cgroup2"})
	m.add(sample{at: at(1), mem: 900, peak: 900, cpuMicro: 1_000_000})  // 1 CPU
	m.add(sample{at: at(2), mem: 700, peak: 1500, cpuMicro: 3_000_000}) // 2 CPU
	u := m.result(sample{at: at(3), mem: 200, peak: 1500, cpuMicro: 3_500_000}, 3*time.Second)
	if !u.Measured || u.MemoryPeak != 1500 || u.Method != "cgroup2" || u.CPUMillis != 3500 || u.CPUPeakMilli != 2000 {
		t.Fatalf("%+v", u)
	}

	// An earlier step set a higher peak than this one reached: the readings decide.
	m = &meter{}
	m.setBase(sample{at: at(0), mem: 100, peak: 5000, cpuMicro: 0, method: "cgroup2"})
	m.add(sample{at: at(1), mem: 900, peak: 5000, cpuMicro: 500_000})
	u = m.result(sample{at: at(2), mem: 300, peak: 5000, cpuMicro: 600_000}, 2*time.Second)
	if u.MemoryPeak != 900 || u.Method != "sampled" || u.Note == "" {
		t.Fatalf("%+v", u)
	}

	// A step shorter than a reading still gets an average CPU.
	m = &meter{}
	m.setBase(sample{at: at(0), mem: 1, peak: 1, cpuMicro: 0, method: "cgroup2"})
	u = m.result(sample{at: at(0.4), mem: 50, peak: 50, cpuMicro: 400_000}, 400*time.Millisecond)
	if u.MemoryPeak != 50 || u.CPUPeakMilli != 1000 {
		t.Fatalf("%+v", u)
	}

	var n Usage
	n.notMeasured("no cgroup")
	if n.Measured || n.MemoryPeak != 0 || n.Note != "no cgroup" {
		t.Fatalf("%+v", n)
	}
}

// The host executor says it did not measure, with the reason.
func TestHostDoesNotMeasure(t *testing.T) {
	var u Usage
	s := spec(t, "true")
	s.Usage = &u
	if code, err := (&Host{}).Run(context.Background(), s, func(string, string) {}); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	if u.Measured || !strings.Contains(u.Note, "no container") {
		t.Fatalf("%+v", u)
	}
}

// With a container runtime, a step's use is read from its container: a step
// that holds some memory and burns some CPU shows both, and its exit code
// and output are unchanged.
func TestContainerMeasuresAStep(t *testing.T) {
	if testing.Short() || os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
	c, err := DetectContainer()
	if err != nil {
		t.Skip(err)
	}
	img := os.Getenv("LAUNCHWAY_TEST_IMAGE")
	if img == "" {
		img = "docker.io/library/alpine:3.22"
		if err := exec.Command(c.Bin, "pull", "-q", img).Run(); err != nil {
			t.Skipf("cannot pull alpine: %v", err)
		}
	}
	var u Usage
	var cap capture
	s := spec(t, "x=$(head -c 30000000 /dev/zero | tr '\\0' 'a'); i=0; while [ $i -lt 300000 ]; do i=$((i+1)); done; echo ${#x}; exit 3")
	s.Image = img
	s.Usage = &u
	code, err := c.Run(context.Background(), s, cap.sink)
	if err != nil || code != 3 {
		t.Fatalf("exit %d, %v: the wrapper must pass the step's exit code on", code, err)
	}
	if !u.Measured || u.MemoryPeak < 20<<20 || u.CPUMillis <= 0 {
		t.Fatalf("%+v", u)
	}
	t.Logf("%+v", u)
}
