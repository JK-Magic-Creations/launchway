package resources

import (
	"strings"
	"testing"
)

func TestParseQuantities(t *testing.T) {
	for in, want := range map[string]int64{"500m": 500, "2": 2000, "0.5": 500, "1.5": 1500, "16": 16000} {
		if got, err := ParseCPU(in); err != nil || got != want {
			t.Errorf("ParseCPU(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for in, want := range map[string]int64{"6Gi": 6 << 30, "512Mi": 512 << 20, "2G": 2_000_000_000, "1.5Gi": 3 << 29, "1024": 1024, "64k": 64000} {
		if got, err := ParseMemory(in); err != nil || got != want {
			t.Errorf("ParseMemory(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	// A CPU is not measured in bytes, nor memory in millicores.
	if _, err := ParseCPU("2Gi"); err == nil {
		t.Error("ParseCPU accepted 2Gi")
	}
	if _, err := ParseMemory("500m"); err == nil {
		t.Error("ParseMemory accepted 500m")
	}
	for _, bad := range []string{"", "x", "-1", "1e3", "0", "0m", "6 Gi", "6GB", "1Pi", "99999999999999Ti"} {
		if _, err := ParseCPU(bad); err == nil {
			t.Errorf("ParseCPU(%q) was accepted", bad)
		}
		if _, err := ParseMemory(bad); err == nil {
			t.Errorf("ParseMemory(%q) was accepted", bad)
		}
	}
	for b, want := range map[int64]string{6 << 30: "6Gi", 1536 << 20: "1536Mi", 512 << 20: "512Mi", 1000: "1000", 2_000_000_000: "1953125Ki"} {
		if got := FormatMemory(b); got != want {
			t.Errorf("FormatMemory(%d) = %s, want %s", b, got, want)
		}
	}
	if FormatCPU(2500) != "2500m" || FormatCPU(4000) != "4" {
		t.Errorf("FormatCPU: %s %s", FormatCPU(2500), FormatCPU(4000))
	}
}

func TestRequestParseRefusesALimitBelowThePromise(t *testing.T) {
	if _, err := (Request{Memory: "8Gi", MemoryLimit: "4Gi"}).Parse(); err == nil || !strings.Contains(err.Error(), "memory 8Gi is more than memory_limit 4Gi") {
		t.Fatalf("%v", err)
	}
	if _, err := (Request{CPU: "4", CPULimit: "2"}).Parse(); err == nil || !strings.Contains(err.Error(), "cpu 4 is more than cpu_limit 2") {
		t.Fatalf("%v", err)
	}
	if _, err := (Request{Memory: "lots"}).Parse(); err == nil || !strings.Contains(err.Error(), "memory:") {
		t.Fatalf("%v", err)
	}
	a, err := (Request{CPU: "1", CPULimit: "4", Memory: "2Gi", MemoryLimit: "6Gi"}).Parse()
	if err != nil || a != (Amount{1000, 4000, 2 << 30, 6 << 30}) {
		t.Fatalf("%+v %v", a, err)
	}
	if r := a.Request(); r != (Request{CPU: "1", CPULimit: "4", Memory: "2Gi", MemoryLimit: "6Gi"}) {
		t.Fatalf("%+v", r)
	}
}

func TestMerge(t *testing.T) {
	def := Amount{CPU: 1000, CPULimit: 4000, Memory: 2 << 30, MemoryLimit: 6 << 30}
	for _, c := range []struct {
		name     string
		declared Amount
		want     Amount
	}{
		{"nothing declared: the default", Amount{}, def},
		{"memory declared: guaranteed and capped at it; cpu from the default",
			Amount{Memory: 8 << 30}, Amount{1000, 4000, 8 << 30, 8 << 30}},
		{"cpu declared: promised and capped; memory from the default",
			Amount{CPU: 2000}, Amount{2000, 2000, 2 << 30, 6 << 30}},
		{"both ends declared", Amount{Memory: 1 << 30, MemoryLimit: 3 << 30}, Amount{1000, 4000, 1 << 30, 3 << 30}},
		{"only a limit: the default's promise, never above the limit",
			Amount{MemoryLimit: 1 << 30}, Amount{1000, 4000, 1 << 30, 1 << 30}},
		{"only a limit above the default's promise", Amount{MemoryLimit: 12 << 30}, Amount{1000, 4000, 2 << 30, 12 << 30}},
	} {
		if got := Merge(c.declared, def); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	// A default with no limit of its own promises and caps the same amount.
	if got := Merge(Amount{}, Amount{Memory: 1 << 30}); got != (Amount{Memory: 1 << 30, MemoryLimit: 1 << 30}) {
		t.Errorf("%+v", got)
	}
	if got := Merge(Amount{}, Amount{}); !got.IsZero() {
		t.Errorf("no default and nothing declared must set nothing: %+v", got)
	}
}

func TestLimitsNameWhatIsAbove(t *testing.T) {
	l := Limits{MaxMemory: 8 << 30, MaxCPU: 8000}
	if v := l.Check(Amount{CPU: 1000, CPULimit: 8000, Memory: 6 << 30, MemoryLimit: 8 << 30}); len(v) != 0 {
		t.Fatalf("at the limit is allowed: %v", v)
	}
	v := l.Check(Amount{CPU: 16000, CPULimit: 16000, Memory: 12 << 30, MemoryLimit: 12 << 30})
	if len(v) != 4 || v[2].Field != "memory" || v[2].Name != "max_memory" || v[2].Limit != "8Gi" || v[2].Asked != "12Gi" {
		t.Fatalf("%+v", v)
	}
	if len((Limits{}).Check(Amount{Memory: 1 << 40})) != 0 {
		t.Fatal("no limit set must allow anything")
	}
}

// The failure this package exists for: 2 GiB of memory and a host of 32
// CPUs. A tool that starts one job per CPU needs far more than the memory
// can hold; the threads we derive must fit it.
func TestThreadsMatchTheMemoryGranted(t *testing.T) {
	const gi = int64(1) << 30
	for _, c := range []struct {
		name         string
		cpu, mem, pt int64
		want         int
	}{
		{"2 GiB, no cpu limit, the host has 32 CPUs", 0, 2 * gi, gi, 2},
		{"6 GiB", 0, 6 * gi, gi, 6},
		{"cpu is the tighter limit", 4000, 6 * gi, gi, 4},
		{"memory is the tighter limit", 16000, 6 * gi, gi, 6},
		{"a fraction of a cpu is one thread", 500, 6 * gi, gi, 1},
		{"less than one thread's worth of memory still runs one", 4000, gi / 4, gi, 1},
		{"a heavier build needs more per thread", 0, 6 * gi, 3 * gi, 2},
		{"the default per thread", 0, 3 * gi, 0, 3},
		{"only a cpu limit", 3000, 0, gi, 3},
		{"nothing limited: no opinion", 0, 0, gi, 0},
	} {
		if got := Threads(c.cpu, c.mem, c.pt); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

func TestToolEnvDoesNotOverrideWhatTheStepSets(t *testing.T) {
	env := ToolEnv(4)
	for k, want := range map[string]string{"GOMAXPROCS": "4", "MAKEFLAGS": "-j4", "CARGO_BUILD_JOBS": "4",
		"CMAKE_BUILD_PARALLEL_LEVEL": "4", "UV_THREADPOOL_SIZE": "4", "LAUNCHWAY_PARALLELISM": "4"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if ToolEnv(0) != nil {
		t.Error("no opinion must set nothing")
	}
	got := WithDefaults(map[string]string{"GOMAXPROCS": "2", "X": "y"}, 4)
	if got["GOMAXPROCS"] != "2" || got["X"] != "y" || got["MAKEFLAGS"] != "-j4" {
		t.Errorf("%v", got)
	}
	in := map[string]string{"X": "y"}
	if out := WithDefaults(in, 0); len(out) != 1 {
		t.Errorf("%v", out)
	}
	if len(in) != 1 {
		t.Error("the argument was changed")
	}
}

func TestDescribe(t *testing.T) {
	if got := (Amount{1000, 4000, 2 << 30, 6 << 30}).Describe(); got != "cpu 1 (up to 4), memory 2Gi (up to 6Gi)" {
		t.Error(got)
	}
	if got := (Amount{Memory: 6 << 30, MemoryLimit: 6 << 30}).Describe(); got != "memory 6Gi" {
		t.Error(got)
	}
	if got := (Amount{}).Describe(); got != "none set" {
		t.Error(got)
	}
}
