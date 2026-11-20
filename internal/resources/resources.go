// Package resources is the arithmetic of what a step may use: CPU and
// memory quantities, how a step's own settings combine with a default, how
// they compare with a limit, and how many parallel jobs a tool should start
// for the memory it has been given.
//
// It knows nothing of pipelines, databases or executors, so that the parser,
// the engine and the executors all work out the same numbers.
package resources

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Request is what a step (or a default) says about its CPU and memory, as
// written: quantities such as 500m or 2 for CPU and 512Mi or 6Gi for memory.
// CPU and Memory are what the step is promised; CPULimit and MemoryLimit are
// the most it may use. A limit left out is the same as the amount promised.
// An empty field says nothing.
type Request struct {
	CPU         string `json:"cpu,omitempty"`
	CPULimit    string `json:"cpu_limit,omitempty"`
	Memory      string `json:"memory,omitempty"`
	MemoryLimit string `json:"memory_limit,omitempty"`
}

// IsZero reports whether nothing is set.
func (r Request) IsZero() bool { return r == Request{} }

// Amount is a Request in numbers: CPU in millicores, memory in bytes. Zero
// means not set.
type Amount struct {
	CPU         int64
	CPULimit    int64
	Memory      int64
	MemoryLimit int64
}

// IsZero reports whether nothing is set.
func (a Amount) IsZero() bool { return a == Amount{} }

var quantityRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(m|k|M|G|T|Ki|Mi|Gi|Ti)?$`)

// MaxQuantity bounds what is accepted: far more than any host has, so that
// arithmetic on it cannot overflow.
const maxQuantity = int64(1) << 50

// ParseCPU reads a CPU quantity into millicores: 500m, 2 or 0.5.
func ParseCPU(s string) (int64, error) {
	m := quantityRe.FindStringSubmatch(s)
	if m == nil || (m[2] != "" && m[2] != "m") {
		return 0, fmt.Errorf("%q is not a CPU amount; write for example 500m, 2 or 0.5", s)
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("%q is too big: more than can be asked for", s)
	}
	if err != nil {
		return 0, fmt.Errorf("%q is not a CPU amount", s)
	}
	if m[2] == "" {
		f *= 1000
	}
	if f > float64(maxQuantity) {
		return 0, fmt.Errorf("%q is too big: more than can be asked for", s)
	}
	n := int64(math.Round(f))
	if n < 1 {
		return 0, fmt.Errorf("%q is less than one millicore (1m)", s)
	}
	return n, nil
}

// ParseMemory reads a memory quantity into bytes: 512Mi, 6Gi, 2G or a number
// of bytes. The binary suffixes (Ki, Mi, Gi, Ti) are powers of 1024, the
// decimal ones (k, M, G, T) powers of 1000.
func ParseMemory(s string) (int64, error) {
	m := quantityRe.FindStringSubmatch(s)
	if m == nil || m[2] == "m" {
		return 0, fmt.Errorf("%q is not a memory amount; write for example 512Mi or 6Gi", s)
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("%q is too big: more than can be asked for", s)
	}
	if err != nil {
		return 0, fmt.Errorf("%q is not a memory amount", s)
	}
	mult := map[string]float64{"": 1, "k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12,
		"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}[m[2]]
	// Check before converting: a float past int64's range converts to an
	// unspecified value, which could pass for a small amount.
	if v := f * mult; v > float64(maxQuantity) {
		return 0, fmt.Errorf("%q is too big: more than can be asked for", s)
	}
	n := int64(math.Round(f * mult))
	if n < 1 {
		return 0, fmt.Errorf("%q is less than one byte", s)
	}
	return n, nil
}

// FormatCPU writes millicores as 2 or 500m.
func FormatCPU(milli int64) string {
	if milli%1000 == 0 {
		return strconv.FormatInt(milli/1000, 10)
	}
	return strconv.FormatInt(milli, 10) + "m"
}

// FormatMemory writes bytes with the largest binary suffix that divides them
// exactly (6Gi, 1536Mi), or as a number of bytes.
func FormatMemory(b int64) string {
	for _, u := range []struct {
		suffix string
		size   int64
	}{{"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}} {
		if b%u.size == 0 {
			return strconv.FormatInt(b/u.size, 10) + u.suffix
		}
	}
	return strconv.FormatInt(b, 10)
}

// Parse reads and checks a Request: every quantity must be one, and a limit
// may not be below what is promised.
func (r Request) Parse() (Amount, error) {
	var a Amount
	var err error
	for _, f := range []struct {
		name string
		text string
		to   *int64
		fn   func(string) (int64, error)
	}{
		{"cpu", r.CPU, &a.CPU, ParseCPU},
		{"cpu_limit", r.CPULimit, &a.CPULimit, ParseCPU},
		{"memory", r.Memory, &a.Memory, ParseMemory},
		{"memory_limit", r.MemoryLimit, &a.MemoryLimit, ParseMemory},
	} {
		if f.text == "" {
			continue
		}
		if *f.to, err = f.fn(f.text); err != nil {
			return Amount{}, fmt.Errorf("%s: %w", f.name, err)
		}
	}
	if a.CPU > 0 && a.CPULimit > 0 && a.CPU > a.CPULimit {
		return Amount{}, fmt.Errorf("cpu %s is more than cpu_limit %s: a step cannot be promised more than it may use", r.CPU, r.CPULimit)
	}
	if a.Memory > 0 && a.MemoryLimit > 0 && a.Memory > a.MemoryLimit {
		return Amount{}, fmt.Errorf("memory %s is more than memory_limit %s: a step cannot be promised more than it may use", r.Memory, r.MemoryLimit)
	}
	return a, nil
}

// Request writes the amounts back as quantities.
func (a Amount) Request() Request {
	var r Request
	if a.CPU > 0 {
		r.CPU = FormatCPU(a.CPU)
	}
	if a.CPULimit > 0 {
		r.CPULimit = FormatCPU(a.CPULimit)
	}
	if a.Memory > 0 {
		r.Memory = FormatMemory(a.Memory)
	}
	if a.MemoryLimit > 0 {
		r.MemoryLimit = FormatMemory(a.MemoryLimit)
	}
	return r
}

// Merge works out what a step gets, resource by resource: if the step says
// anything about CPU (cpu or cpu_limit), the default's CPU is ignored; the
// same for memory. Where the step says something, a limit it leaves out is
// what it asks to be promised, and an amount promised it leaves out is the
// default's, but never above its limit. Where it says nothing, the default
// stands, a limit it leaves out being the amount promised.
//
// Both arguments must already be valid (see Request.Parse).
func Merge(declared, def Amount) Amount {
	one := func(dReq, dLim, req, lim int64) (int64, int64) {
		if req == 0 && lim == 0 {
			req, lim = dReq, dLim
			if lim == 0 {
				lim = req
			}
			return req, lim
		}
		if lim == 0 {
			lim = req
		}
		if req == 0 {
			req = lim
			if dReq > 0 {
				req = min(dReq, lim)
			}
		}
		return req, lim
	}
	var out Amount
	out.CPU, out.CPULimit = one(def.CPU, def.CPULimit, declared.CPU, declared.CPULimit)
	out.Memory, out.MemoryLimit = one(def.Memory, def.MemoryLimit, declared.Memory, declared.MemoryLimit)
	return out
}

// Max returns the larger of each field: what a container must have to serve
// every step that runs in it.
func Max(a, b Amount) Amount {
	return Amount{CPU: max(a.CPU, b.CPU), CPULimit: max(a.CPULimit, b.CPULimit),
		Memory: max(a.Memory, b.Memory), MemoryLimit: max(a.MemoryLimit, b.MemoryLimit)}
}

// Limits are the most a step may ask for. Zero means no limit.
type Limits struct {
	MaxCPU    int64 // millicores
	MaxMemory int64 // bytes
	// MemoryPerThread is the memory each parallel job of a build is assumed
	// to need; see Threads.
	MemoryPerThread int64
}

// Violation is one value that is above a limit.
type Violation struct {
	Field string // cpu, cpu_limit, memory or memory_limit
	Asked string // as a quantity
	Limit string // the limit, as a quantity
	Name  string // max_cpu or max_memory
}

func (v Violation) String() string {
	return fmt.Sprintf("%s %s is above %s %s", v.Field, v.Asked, v.Name, v.Limit)
}

// Check lists what a is above.
func (l Limits) Check(a Amount) []Violation {
	var out []Violation
	for _, f := range []struct {
		field string
		v     int64
	}{{"cpu", a.CPU}, {"cpu_limit", a.CPULimit}} {
		if l.MaxCPU > 0 && f.v > l.MaxCPU {
			out = append(out, Violation{f.field, FormatCPU(f.v), FormatCPU(l.MaxCPU), "max_cpu"})
		}
	}
	for _, f := range []struct {
		field string
		v     int64
	}{{"memory", a.Memory}, {"memory_limit", a.MemoryLimit}} {
		if l.MaxMemory > 0 && f.v > l.MaxMemory {
			out = append(out, Violation{f.field, FormatMemory(f.v), FormatMemory(l.MaxMemory), "max_memory"})
		}
	}
	return out
}

// DefaultMemoryPerThread is how much memory a parallel job is assumed to
// need when nothing else is said: a Go or C++ compile peaks at several
// hundred MiB, and the link step of a large program needs more than a
// gigabyte, so one thread per GiB leaves headroom for both.
const DefaultMemoryPerThread = int64(1) << 30

// Threads is the number of parallel jobs a tool should start in a container
// that may use cpuLimit millicores and memLimit bytes: as many as there are
// CPUs to run them, but no more than the memory feeds at memPerThread each.
// It is at least 1. It returns 0, meaning "no opinion", when neither limit is
// set: the tool then does what it always did.
func Threads(cpuLimit, memLimit, memPerThread int64) int {
	if cpuLimit <= 0 && memLimit <= 0 {
		return 0
	}
	if memPerThread <= 0 {
		memPerThread = DefaultMemoryPerThread
	}
	n := int64(math.MaxInt32)
	if cpuLimit > 0 {
		n = min(n, (cpuLimit+999)/1000) // a fraction of a CPU still runs one thread
	}
	if memLimit > 0 {
		n = min(n, memLimit/memPerThread)
	}
	return int(max(n, 1))
}

// ToolEnv is the environment that makes common toolchains start no more than
// n parallel jobs. Tools that look at the machine instead of the container
// (Go before 1.25, make, Cargo, CMake, Node's thread pool) see every CPU of
// the host, however little memory the container has.
//
//   - GOMAXPROCS: Go programs, including go build's compiler and linker.
//     go build also starts as many packages in parallel as GOMAXPROCS.
//   - MAKEFLAGS=-jN: GNU make (and tools that run it).
//   - CARGO_BUILD_JOBS: Cargo.
//   - CMAKE_BUILD_PARALLEL_LEVEL: cmake --build, and ctest -j through it.
//   - UV_THREADPOOL_SIZE: Node's libuv thread pool (file and DNS work).
//   - LAUNCHWAY_PARALLELISM: for scripts: make -j"$LAUNCHWAY_PARALLELISM".
//
// A variable the step sets itself is never replaced (see WithDefaults).
func ToolEnv(n int) map[string]string {
	if n < 1 {
		return nil
	}
	s := strconv.Itoa(n)
	return map[string]string{
		"GOMAXPROCS":                 s,
		"MAKEFLAGS":                  "-j" + s,
		"CARGO_BUILD_JOBS":           s,
		"CMAKE_BUILD_PARALLEL_LEVEL": s,
		"UV_THREADPOOL_SIZE":         s,
		"LAUNCHWAY_PARALLELISM":      s,
	}
}

// WithDefaults returns env with ToolEnv(n) added for the variables it does
// not set. A nil result means env is unchanged.
func WithDefaults(env map[string]string, n int) map[string]string {
	add := ToolEnv(n)
	if add == nil {
		return env
	}
	out := make(map[string]string, len(env)+len(add))
	for k, v := range add {
		out[k] = v
	}
	for k, v := range env {
		out[k] = v
	}
	return out
}

// Describe writes an amount for a log line: "cpu 1 (up to 4), memory 2Gi (up
// to 6Gi)"; nothing set reads "none set".
func (a Amount) Describe() string {
	var parts []string
	one := func(name, req, lim string) {
		switch {
		case req == "" && lim == "":
		case lim == "" || lim == req:
			parts = append(parts, name+" "+req)
		default:
			parts = append(parts, fmt.Sprintf("%s %s (up to %s)", name, req, lim))
		}
	}
	r := a.Request()
	one("cpu", r.CPU, r.CPULimit)
	one("memory", r.Memory, r.MemoryLimit)
	if len(parts) == 0 {
		return "none set"
	}
	return strings.Join(parts, ", ")
}
