package graph

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// RequirementsPath is where a repository declares what its software needs.
const RequirementsPath = ".launchway/requirements.yaml"

// The facets a requirement can have. Each is one thing a machine or a
// platform can or cannot offer.
const (
	FacetPlatform = "platform" // container, vm, serverless, bootc
	FacetArch     = "arch"     // amd64, arm64, ...
	FacetCPU      = "cpu"      // cores
	FacetMemory   = "memory"   // bytes
	FacetStorage  = "storage"  // bytes
	FacetDevice   = "device"   // gpu, ...
	FacetOS       = "os"       // linux, ...
	FacetFeature  = "feature"  // kernel and OS features: cgroups-v2, ...
	FacetKernel   = "kernel"   // a minimum kernel version
	FacetPort     = "port"     // a network port: 8080/tcp
	FacetData     = "data"     // a data service: postgres>=15
	FacetRuntime  = "runtime"  // a language runtime: go>=1.23
)

// Platforms a component can be delivered as.
var Platforms = []string{"container", "vm", "serverless", "bootc"}

// DataKinds are the data services a component can need.
var DataKinds = []string{"postgres", "mysql", "sqlite", "mongodb", "redis", "memcached", "kafka", "rabbitmq", "nats", "s3", "object-storage", "queue", "cache", "database"}

// Requirements is the file .launchway/requirements.yaml. The top-level
// fields apply to every component of the repository; a component named
// under components: overrides them field by field.
type Requirements struct {
	Version    int `yaml:"version" json:"version"`
	Spec       `yaml:",inline"`
	Components map[string]ComponentSpec `yaml:"components,omitempty" json:"components,omitempty"`
}

// Spec is what one component, or the repository's default, needs.
type Spec struct {
	Platform      string       `yaml:"platform,omitempty" json:"platform,omitempty"`
	Architectures []string     `yaml:"architectures,omitempty" json:"architectures,omitempty"`
	CPU           string       `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory        string       `yaml:"memory,omitempty" json:"memory,omitempty"`
	Storage       string       `yaml:"storage,omitempty" json:"storage,omitempty"`
	Devices       []DeviceSpec `yaml:"devices,omitempty" json:"devices,omitempty"`
	OS            *OSSpec      `yaml:"os,omitempty" json:"os,omitempty"`
	Ports         []string     `yaml:"ports,omitempty" json:"ports,omitempty"`
	Data          []DataSpec   `yaml:"data,omitempty" json:"data,omitempty"`
}

// ComponentSpec is a component's own requirements, found by its path.
type ComponentSpec struct {
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	Spec `yaml:",inline"`
}

// DeviceSpec is a device the component needs. A bare word ("gpu") is a
// device of that kind, one of them.
type DeviceSpec struct {
	Kind   string `yaml:"kind" json:"kind"`
	Count  int    `yaml:"count,omitempty" json:"count,omitempty"`
	Vendor string `yaml:"vendor,omitempty" json:"vendor,omitempty"`
}

// UnmarshalYAML reads "gpu" as a device of that kind.
func (d *DeviceSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		d.Kind, d.Count = n.Value, 1
		return nil
	}
	type plain DeviceSpec
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*d = DeviceSpec(p)
	return nil
}

// OSSpec is what the operating system must offer.
type OSSpec struct {
	Family   string   `yaml:"family,omitempty" json:"family,omitempty"`
	Kernel   string   `yaml:"kernel,omitempty" json:"kernel,omitempty"` // a minimum, ">=6.1" or "6.1"
	Features []string `yaml:"features,omitempty" json:"features,omitempty"`
}

// DataSpec is a data service the component needs: a kind, and a version
// constraint if it matters.
type DataSpec struct {
	Kind    string `yaml:"kind" json:"kind"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
}

// UnmarshalYAML reads "postgres" as a service of that kind.
func (d *DataSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		d.Kind = n.Value
		return nil
	}
	type plain DataSpec
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*d = DataSpec(p)
	return nil
}

// MaxRequirementsBytes bounds the file.
const MaxRequirementsBytes = 64 << 10

// ParseRequirements reads and checks the file. Every problem found is
// reported, each with where it is when the YAML says.
func ParseRequirements(data []byte) (*Requirements, error) {
	if len(data) > MaxRequirementsBytes {
		return nil, fmt.Errorf("%s is larger than %d KiB", RequirementsPath, MaxRequirementsBytes>>10)
	}
	var r Requirements
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s is empty", RequirementsPath)
		}
		return nil, fmt.Errorf("%s: %v", RequirementsPath, err)
	}
	if r.Version != 1 {
		return nil, fmt.Errorf("%s: version must be 1 (got %d)", RequirementsPath, r.Version)
	}
	var problems []string
	check := func(where string, s Spec) {
		problems = append(problems, s.validate(where)...)
	}
	check("(repository)", r.Spec)
	names := make([]string, 0, len(r.Components))
	for n := range r.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := r.Components[n]
		check("components."+n, c.Spec)
		if c.Path != "" && (strings.HasPrefix(c.Path, "/") || strings.Contains(c.Path, "..")) {
			problems = append(problems, fmt.Sprintf("components.%s.path: %q must be a path inside the repository", n, c.Path))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: %s", RequirementsPath, strings.Join(problems, "; "))
	}
	return &r, nil
}

var portRe = regexp.MustCompile(`^(\d{1,5})(?:/(tcp|udp))?$`)

func (s Spec) validate(where string) []string {
	var p []string
	bad := func(f, format string, a ...any) { p = append(p, where+"."+f+": "+fmt.Sprintf(format, a...)) }
	if s.Platform != "" && !in(Platforms, s.Platform) {
		bad("platform", "%q is not one of %s", s.Platform, strings.Join(Platforms, ", "))
	}
	for _, a := range s.Architectures {
		if !validArch(a) {
			bad("architectures", "%q is not an architecture (amd64, arm64, riscv64, ppc64le, s390x, arm, 386)", a)
		}
	}
	if s.CPU != "" {
		if _, err := ParseCPU(s.CPU); err != nil {
			bad("cpu", "%v", err)
		}
	}
	for f, v := range map[string]string{"memory": s.Memory, "storage": s.Storage} {
		if v != "" {
			if _, err := ParseBytes(v); err != nil {
				bad(f, "%v", err)
			}
		}
	}
	for _, d := range s.Devices {
		if d.Kind == "" || !devKindRe.MatchString(d.Kind) {
			bad("devices", "a device needs a kind such as gpu (got %q)", d.Kind)
		}
		if d.Count < 0 || d.Count > 64 {
			bad("devices", "count %d is out of range (0 to 64)", d.Count)
		}
	}
	for _, port := range s.Ports {
		m := portRe.FindStringSubmatch(port)
		if m == nil {
			bad("ports", "%q is not a port (write 8080 or 8080/tcp)", port)
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n < 1 || n > 65535 {
			bad("ports", "%q is out of range", port)
		}
	}
	for _, d := range s.Data {
		if !in(DataKinds, d.Kind) {
			bad("data", "%q is not a known data service (%s)", d.Kind, strings.Join(DataKinds, ", "))
		}
		if d.Version != "" {
			if _, _, err := parseConstraint(d.Version); err != nil {
				bad("data", "version %q: %v", d.Version, err)
			}
		}
	}
	if s.OS != nil && s.OS.Kernel != "" {
		if _, _, err := parseConstraint(s.OS.Kernel); err != nil {
			bad("os.kernel", "%v", err)
		}
	}
	return p
}

var devKindRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func validArch(a string) bool {
	switch a {
	case "amd64", "arm64", "riscv64", "ppc64le", "s390x", "arm", "386":
		return true
	}
	return false
}

func in(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// ParseCPU reads "2", "0.5" or "500m" as thousandths of a core.
func ParseCPU(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	if m, ok := strings.CutSuffix(s, "m"); ok {
		n, err := strconv.ParseInt(m, 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%q is not a CPU amount (write 2, 0.5 or 500m)", s)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) || f > 100000 {
		return 0, fmt.Errorf("%q is not a CPU amount (write 2, 0.5 or 500m)", s)
	}
	return int64(math.Round(f * 1000)), nil
}

var qtyRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(Ki|Mi|Gi|Ti|K|M|G|T|k|m|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$`)

// ParseBytes reads an amount such as 512Mi, 2Gi, 1G or 1048576.
func ParseBytes(s string) (int64, error) {
	m := qtyRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("%q is not an amount of bytes (write 512Mi, 2Gi or 1G)", s)
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"": 1, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
		"K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "k": 1e3, "m": 1e-3}[m[2]]
	v := f * mult
	if v > math.MaxInt64/2 {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return int64(math.Round(v)), nil
}

// FormatBytes writes a size the way the declared file would.
func FormatBytes(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%dGi", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dMi", n>>20)
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGi", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMi", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d", n)
}

// FormatCPU writes thousandths of a core as cores.
func FormatCPU(m int64) string {
	if m%1000 == 0 {
		return strconv.FormatInt(m/1000, 10)
	}
	return strconv.FormatFloat(float64(m)/1000, 'f', -1, 64)
}

// parseConstraint reads ">=15", "15", "<=3.2", "=1.2" into an operator and a
// version.
func parseConstraint(s string) (op, ver string, err error) {
	s = strings.TrimSpace(s)
	for _, o := range []string{">=", "<=", "==", ">", "<", "="} {
		if rest, ok := strings.CutPrefix(s, o); ok {
			op, s = o, strings.TrimSpace(rest)
			break
		}
	}
	if op == "" {
		op = ">="
	}
	if s == "" || !regexp.MustCompile(`^[0-9]+(\.[0-9]+)*([-.+a-zA-Z0-9]*)?$`).MatchString(s) {
		return "", "", fmt.Errorf("%q is not a version constraint (write >=15 or 6.1)", s)
	}
	return op, s, nil
}

// CompareVersions compares dotted numbers (1.10 is above 1.9); anything
// after the numbers is ignored.
func CompareVersions(a, b string) int {
	pa, pb := nums(a), nums(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func nums(v string) []int {
	var out []int
	for _, f := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
		n := 0
		got := false
		for _, c := range f {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
			got = true
		}
		if !got {
			break
		}
		out = append(out, n)
	}
	return out
}

// Meets reports whether have satisfies the constraint.
func Meets(have, constraint string) bool {
	op, ver, err := parseConstraint(constraint)
	if err != nil {
		return false
	}
	c := CompareVersions(have, ver)
	switch op {
	case ">=":
		return c >= 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case "<":
		return c < 0
	}
	return c == 0
}
