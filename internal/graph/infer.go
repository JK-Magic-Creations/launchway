package graph

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Fact is one thing a component needs, with where the claim came from.
// A declared fact is what a person wrote in the requirements file; an
// inferred one is what the product worked out and says so, with the evidence.
type Fact struct {
	Facet    string     `json:"facet"`
	Value    string     `json:"value"`              // the canonical value: "arm64", "500" (millicores), "536870912" (bytes), "gpu:1", "8080/tcp", "postgres>=15"
	Source   Confidence `json:"source"`             // declared or inferred
	Evidence string     `json:"evidence,omitempty"` // for an inferred fact, what it was read from
}

// Display writes the value for people.
func (f Fact) Display() string {
	switch f.Facet {
	case FacetCPU:
		n, _ := strconv.ParseInt(f.Value, 10, 64)
		return FormatCPU(n) + " cores"
	case FacetMemory, FacetStorage:
		n, _ := strconv.ParseInt(f.Value, 10, 64)
		return FormatBytes(n)
	}
	return f.Value
}

// Advice is something the product inferred that differs from what is
// declared. It is shown and never applied.
type Advice struct {
	Facet    string `json:"facet"`
	Inferred string `json:"inferred"`
	Declared string `json:"declared"`
	Evidence string `json:"evidence,omitempty"`
	Message  string `json:"message"`
}

// Unit is something that gets requirements: a component of a repository.
type Unit struct {
	Key           string // the component node's key
	Dir           string
	Containerfile string
	Streams       []string // image names built from its Containerfile
	Platforms     []string
	Facts         []Fact // inferred
}

// Units finds the components of a repository that get requirements and what
// can safely be inferred for each from the files of the repository.
func Units(tree fs.FS, repo string, cfg Config) []Unit {
	cfs := ScanContainerfiles(tree)
	pf, _ := ScanPipeline(tree)
	var units []Unit
	hasCF := map[string]bool{}
	for _, cf := range cfs {
		hasCF[path.Dir(cf.Path)] = true
		u := Unit{Key: componentKey(repo, cf.Path), Dir: path.Dir(cf.Path), Containerfile: cf.Path}
		for _, is := range pf.Images {
			if is.File == cf.Path {
				for _, t := range is.Targets {
					reg, rp, _ := strings.Cut(t, "/")
					u.Streams = append(u.Streams, cfg.StreamKey(reg, rp))
				}
				u.Platforms = append(u.Platforms, is.Platforms...)
			}
		}
		for _, p := range cf.Ports {
			u.Facts = append(u.Facts, Fact{FacetPort, fmt.Sprintf("%d/tcp", p), Inferred, "EXPOSE " + strconv.Itoa(p) + " in " + cf.Path})
		}
		u.Facts = append(u.Facts, Fact{FacetPlatform, "container", Inferred, cf.Path + " is a Containerfile"})
		archs := map[string]bool{}
		for _, pl := range u.Platforms {
			archs[Arch(pl)] = true
		}
		for _, a := range sortedKeys(archs) {
			u.Facts = append(u.Facts, Fact{FacetArch, a, Inferred, "the pipeline builds " + u.Containerfile + " for " + strings.Join(u.Platforms, ", ")})
		}
		units = append(units, u)
	}
	// Manifest directories with no Containerfile are components too (a
	// library, a service run as a VM or a function).
	manifests := map[string]string{}
	_ = fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if ecoOfFile(p) != "" && !strings.HasPrefix(path.Base(p), "requirements") {
			manifests[dirOf(p)] = p
		}
		return nil
	})
	for _, dir := range sortedKeys2(manifests) {
		if hasCF[dir] {
			continue
		}
		units = append(units, Unit{Key: componentKey(repo, dir), Dir: dir})
	}
	if len(units) == 0 {
		units = append(units, Unit{Key: componentKey(repo, "."), Dir: "."})
	}
	// Runtime versions from manifests, data services from dependencies.
	for i := range units {
		u := &units[i]
		for _, dir := range []string{u.Dir, "."} {
			if m := manifests[dir]; m != "" {
				data, err := fs.ReadFile(tree, m)
				if err == nil {
					u.Facts = append(u.Facts, runtimeFacts(m, data)...)
					u.Facts = append(u.Facts, dataHints(m, data)...)
				}
				break
			}
		}
	}
	applyKubernetes(tree, units)
	for i := range units {
		units[i].Facts = dedupeFacts(units[i].Facts)
	}
	return units
}

var (
	goVerRe      = regexp.MustCompile(`(?m)^go\s+(\d+(?:\.\d+)*)`)
	pyRequiresRe = regexp.MustCompile(`(?m)^requires-python\s*=\s*["']([^"']+)["']`)
)

func runtimeFacts(file string, data []byte) []Fact {
	switch path.Base(file) {
	case "go.mod":
		if m := goVerRe.FindSubmatch(data); m != nil {
			return []Fact{{FacetRuntime, "go>=" + string(m[1]), Inferred, "go " + string(m[1]) + " in " + file}}
		}
	case "package.json":
		var pj struct {
			Engines map[string]string `json:"engines"`
		}
		if yaml.Unmarshal(data, &pj) == nil {
			if n := pj.Engines["node"]; n != "" {
				if _, v, err := parseConstraint(strings.Fields(strings.ReplaceAll(n, "^", ">="))[0]); err == nil {
					return []Fact{{FacetRuntime, "node>=" + v, Inferred, "engines.node " + n + " in " + file}}
				}
			}
		}
	case "pyproject.toml":
		if m := pyRequiresRe.FindSubmatch(data); m != nil {
			if _, v, err := parseConstraint(strings.Split(string(m[1]), ",")[0]); err == nil {
				return []Fact{{FacetRuntime, "python>=" + v, Inferred, "requires-python " + string(m[1]) + " in " + file}}
			}
		}
	}
	return nil
}

// dataHints reads well-known database and cache clients out of a manifest.
// They are hints and marked as inferred: a client library does not prove the
// service is needed, which is why the declared file wins.
var dataLibs = map[string]string{
	"github.com/lib/pq": "postgres", "github.com/jackc/pgx": "postgres", "psycopg2": "postgres", "psycopg": "postgres", "asyncpg": "postgres", `"pg"`: "postgres",
	"github.com/go-sql-driver/mysql": "mysql", "mysqlclient": "mysql", "pymysql": "mysql", `"mysql2"`: "mysql",
	"github.com/redis/go-redis": "redis", "github.com/go-redis/redis": "redis", "redis": "redis", `"ioredis"`: "redis",
	"go.mongodb.org/mongo-driver": "mongodb", "pymongo": "mongodb", `"mongodb"`: "mongodb",
	"github.com/segmentio/kafka-go": "kafka", "github.com/rabbitmq/amqp091-go": "rabbitmq", "pika": "rabbitmq", "github.com/nats-io/nats.go": "nats",
}

func dataHints(file string, data []byte) []Fact {
	txt := string(data)
	seen := map[string]string{}
	for lib, kind := range dataLibs {
		if strings.Contains(txt, lib) {
			if _, ok := seen[kind]; !ok || lib < seen[kind] {
				seen[kind] = lib
			}
		}
	}
	var out []Fact
	for _, k := range sortedKeys(seenSet(seen)) {
		out = append(out, Fact{FacetData, k, Inferred, "depends on " + strings.Trim(seen[k], `"`) + " in " + file})
	}
	return out
}

func seenSet(m map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// kubePod is what the resource requests of one workload say.
type kubePod struct {
	file, kind, name string
	cpu, mem, eph    int64
	gpu              int
	ports            []int
	images           []string
}

// readKubePod reads the containers of a workload: images, requests, GPU
// limits and ports. It returns false for a document that is not a workload.
func readKubePod(doc map[string]any, file string) (kubePod, bool) {
	kind, _ := doc["kind"].(string)
	tmpl := podSpec(doc, kind)
	if tmpl == nil {
		return kubePod{}, false
	}
	md, _ := doc["metadata"].(map[string]any)
	nm, _ := md["name"].(string)
	pd := kubePod{file: file, kind: kind, name: nm}
	cs, _ := tmpl["containers"].([]any)
	for _, c := range cs {
		cm, _ := c.(map[string]any)
		if img, _ := cm["image"].(string); img != "" {
			pd.images = append(pd.images, ImageName(img))
		}
		res, _ := cm["resources"].(map[string]any)
		req, _ := res["requests"].(map[string]any)
		lim, _ := res["limits"].(map[string]any)
		pd.cpu += qtyCPU(req["cpu"], lim["cpu"])
		pd.mem += qtyBytes(req["memory"], lim["memory"])
		pd.eph += qtyBytes(req["ephemeral-storage"], lim["ephemeral-storage"])
		for _, k := range []string{"nvidia.com/gpu", "amd.com/gpu", "gpu.intel.com/i915"} {
			pd.gpu += max(qtyInt(lim[k]), 0)
		}
		ps, _ := cm["ports"].([]any)
		for _, x := range ps {
			pm, _ := x.(map[string]any)
			if n := qtyInt(pm["containerPort"]); n > 0 {
				pd.ports = append(pd.ports, n)
			}
		}
	}
	return pd, true
}

// kubePods reads every workload of the Kubernetes manifests in the tree.
func kubePods(tree fs.FS) []kubePod {
	var pods []kubePod
	_ = fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (!strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml")) || strings.HasPrefix(p, ".launchway/") {
			return nil
		}
		data, err := fs.ReadFile(tree, p)
		if err != nil || !bytes.Contains(data, []byte("containers")) {
			return nil
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				return nil // the end of the file, or a document that is not YAML
			}
			if pd, ok := readKubePod(doc, p); ok {
				pods = append(pods, pd)
			}
		}
	})
	return pods
}

// podUnit finds the unit whose image a workload runs; with one unit only, that unit.
func podUnit(units []Unit, pd kubePod) *Unit {
	for i := range units {
		for _, s := range units[i].Streams {
			for _, img := range pd.images {
				if img == s || strings.HasSuffix(s, "/"+strings.TrimPrefix(img, "docker.io/")) || strings.HasSuffix(img, "/"+lastTwo(s)) {
					return &units[i]
				}
			}
		}
	}
	if len(units) == 1 {
		return &units[0]
	}
	return nil
}

// applyKubernetes adds the resource requests, devices and ports that
// Kubernetes manifests in the repository give to the unit whose image they
// run; with one unit only, to that unit.
func applyKubernetes(tree fs.FS, units []Unit) {
	for _, pd := range kubePods(tree) {
		target := podUnit(units, pd)
		if target == nil {
			continue
		}
		ev := pd.kind + "/" + pd.name + " in " + pd.file
		add := func(facet, value, what string) {
			target.Facts = append(target.Facts, Fact{facet, value, Inferred, what + ev})
		}
		if pd.cpu > 0 {
			add(FacetCPU, strconv.FormatInt(pd.cpu, 10), "resource requests of ")
		}
		if pd.mem > 0 {
			add(FacetMemory, strconv.FormatInt(pd.mem, 10), "resource requests of ")
		}
		if pd.eph > 0 {
			add(FacetStorage, strconv.FormatInt(pd.eph, 10), "ephemeral storage of ")
		}
		if pd.gpu > 0 {
			add(FacetDevice, "gpu:"+strconv.Itoa(pd.gpu), "GPU limit of ")
		}
		for _, p := range pd.ports {
			add(FacetPort, fmt.Sprintf("%d/tcp", p), "containerPort in ")
		}
	}
}

func lastTwo(s string) string {
	i := strings.LastIndex(s, "/")
	if i < 0 {
		return s
	}
	j := strings.LastIndex(s[:i], "/")
	return s[j+1:]
}

func podSpec(doc map[string]any, kind string) map[string]any {
	get := func(m map[string]any, keys ...string) map[string]any {
		cur := m
		for _, k := range keys {
			next, _ := cur[k].(map[string]any)
			if next == nil {
				return nil
			}
			cur = next
		}
		return cur
	}
	switch kind {
	case "Pod":
		return get(doc, "spec")
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		return get(doc, "spec", "template", "spec")
	case "CronJob":
		return get(doc, "spec", "jobTemplate", "spec", "template", "spec")
	}
	return nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func qtyCPU(vals ...any) int64 {
	for _, v := range vals {
		if s := scalar(v); s != "" {
			if n, err := ParseCPU(s); err == nil {
				return n
			}
		}
	}
	return 0
}

func qtyBytes(vals ...any) int64 {
	for _, v := range vals {
		if s := scalar(v); s != "" {
			if n, err := ParseBytes(s); err == nil {
				return n
			}
		}
	}
	return 0
}

func qtyInt(v any) int {
	n, _ := strconv.Atoi(scalar(v))
	return n
}

func dedupeFacts(fs []Fact) []Fact {
	seen := map[string]bool{}
	var out []Fact
	for _, f := range fs {
		k := f.Facet + "=" + f.Value
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Facet != out[j].Facet {
			return out[i].Facet < out[j].Facet
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys2(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// declaredFacts turns the declared spec for a unit into facts: the
// repository's defaults, then the component's own, field by field.
func declaredFacts(r *Requirements, u Unit, name string) []Fact {
	if r == nil {
		return nil
	}
	spec := r.Spec
	for n, c := range r.Components {
		if matchesUnit(n, c, u, name) {
			spec = overlay(spec, c.Spec)
			break
		}
	}
	return specFacts(spec)
}

func matchesUnit(name string, c ComponentSpec, u Unit, unitName string) bool {
	if c.Path != "" {
		p := path.Clean(c.Path)
		return p == u.Dir || p == u.Containerfile
	}
	return name == path.Base(u.Dir) || name == unitName || name == u.Key
}

func overlay(base, over Spec) Spec {
	out := base
	if over.Platform != "" {
		out.Platform = over.Platform
	}
	if len(over.Architectures) > 0 {
		out.Architectures = over.Architectures
	}
	if over.CPU != "" {
		out.CPU = over.CPU
	}
	if over.Memory != "" {
		out.Memory = over.Memory
	}
	if over.Storage != "" {
		out.Storage = over.Storage
	}
	if len(over.Devices) > 0 {
		out.Devices = over.Devices
	}
	if over.OS != nil {
		out.OS = over.OS
	}
	if len(over.Ports) > 0 {
		out.Ports = over.Ports
	}
	if len(over.Data) > 0 {
		out.Data = over.Data
	}
	return out
}

func specFacts(s Spec) []Fact {
	var out []Fact
	add := func(facet, value string) {
		out = append(out, Fact{Facet: facet, Value: value, Source: Declared, Evidence: RequirementsPath})
	}
	if s.Platform != "" {
		add(FacetPlatform, s.Platform)
	}
	for _, a := range s.Architectures {
		add(FacetArch, a)
	}
	if s.CPU != "" {
		n, _ := ParseCPU(s.CPU)
		add(FacetCPU, strconv.FormatInt(n, 10))
	}
	if s.Memory != "" {
		n, _ := ParseBytes(s.Memory)
		add(FacetMemory, strconv.FormatInt(n, 10))
	}
	if s.Storage != "" {
		n, _ := ParseBytes(s.Storage)
		add(FacetStorage, strconv.FormatInt(n, 10))
	}
	for _, d := range s.Devices {
		c := d.Count
		if c == 0 {
			c = 1
		}
		v := d.Kind + ":" + strconv.Itoa(c)
		if d.Vendor != "" {
			v += "@" + d.Vendor
		}
		add(FacetDevice, v)
	}
	if s.OS != nil {
		if s.OS.Family != "" {
			add(FacetOS, s.OS.Family)
		}
		if s.OS.Kernel != "" {
			op, v, _ := parseConstraint(s.OS.Kernel)
			add(FacetKernel, op+v)
		}
		for _, f := range s.OS.Features {
			add(FacetFeature, f)
		}
	}
	for _, p := range s.Ports {
		m := portRe.FindStringSubmatch(p)
		proto := m[2]
		if proto == "" {
			proto = "tcp"
		}
		add(FacetPort, m[1]+"/"+proto)
	}
	for _, d := range s.Data {
		v := d.Kind
		if d.Version != "" {
			op, ver, _ := parseConstraint(d.Version)
			v += op + ver
		}
		add(FacetData, v)
	}
	return out
}

// Effective is a component's requirements: what counts, and advice.
type Effective struct {
	Component string   `json:"component"`
	Facts     []Fact   `json:"facts"`
	Advice    []Advice `json:"advice,omitempty"`
}

// MergeFacts combines declared and inferred facts. A facet the file
// declares is exactly what the file says: inferred values for it are never
// applied, and where they differ they are returned as advice. A facet the
// file does not mention takes the inferred values, marked as inferred.
func MergeFacts(declared, inferred []Fact) ([]Fact, []Advice) {
	byFacet := map[string][]Fact{}
	for _, f := range declared {
		byFacet[f.Facet] = append(byFacet[f.Facet], f)
	}
	var out []Fact
	out = append(out, declared...)
	var advice []Advice
	for _, f := range inferred {
		d, has := byFacet[f.Facet]
		if !has {
			out = append(out, f)
			continue
		}
		if factPresent(d, f) {
			continue
		}
		advice = append(advice, Advice{Facet: f.Facet, Inferred: f.Display(), Declared: joinDisplay(d), Evidence: f.Evidence,
			Message: fmt.Sprintf("%s: the file declares %s; Launchway read %s (%s). The declaration stands.", f.Facet, joinDisplay(d), f.Display(), f.Evidence)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Facet != out[j].Facet {
			return out[i].Facet < out[j].Facet
		}
		return out[i].Value < out[j].Value
	})
	return out, advice
}

func factPresent(l []Fact, f Fact) bool {
	for _, x := range l {
		if x.Value == f.Value {
			return true
		}
		// A declared device or data service of the same kind covers an inferred one of that kind.
		if (f.Facet == FacetDevice || f.Facet == FacetData || f.Facet == FacetRuntime) && kindOf(x.Value) == kindOf(f.Value) && f.Facet != FacetDevice {
			return true
		}
		if f.Facet == FacetDevice && kindOf(x.Value) == kindOf(f.Value) {
			return true
		}
	}
	return false
}

func kindOf(v string) string {
	if i := strings.IndexAny(v, ":><=@"); i > 0 {
		return v[:i]
	}
	return v
}

func joinDisplay(fs []Fact) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.Display())
	}
	return strings.Join(s, ", ")
}
