package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComponentNeeds is what one component needs: the facts that count, each
// marked declared or inferred, and advice that was not applied.
type ComponentNeeds struct {
	Component  Node     `json:"component"`
	Repository string   `json:"repository,omitempty"`
	Facts      []Fact   `json:"facts"`
	Advice     []Advice `json:"advice,omitempty"`
	// File is where a person says it: the requirements file of the repository.
	File string `json:"file"`
}

// componentsOf expands a reference to the components it means: a component
// is itself, a repository is the components it contains.
func (s *Store) componentsOf(ctx context.Context, v Viewer, ref NodeRef, when When) ([]Node, error) {
	n, err := s.find(ctx, v, ref, when)
	if err != nil {
		return nil, err
	}
	switch n.Kind {
	case KindComponent:
		return []Node{n}, nil
	case KindRepository:
		w, err := s.walk(ctx, v, []int64{n.ID}, Downstream, []any{string(EdgeContains)}, Limits{Depth: 1, Nodes: 500}, when)
		if err != nil {
			return nil, err
		}
		hits, _, err := s.hits(ctx, w, []int64{n.ID})
		if err != nil {
			return nil, err
		}
		var out []Node
		for _, h := range hits {
			if h.Node.Kind == KindComponent {
				out = append(out, h.Node)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: requirements belong to a component or a repository, not a %s", ErrInvalid, n.Kind)
}

func repoOfComponent(n Node) string {
	if p, ok := strings.CutPrefix(n.Subject, subRepo); ok {
		return p
	}
	return ""
}

// Needs returns the effective requirements of a component, or of every
// component of a repository.
func (s *Store) Needs(ctx context.Context, v Viewer, ref NodeRef, when When) ([]ComponentNeeds, error) {
	comps, err := s.componentsOf(ctx, v, ref, when)
	if err != nil {
		return nil, err
	}
	var out []ComponentNeeds
	for _, c := range comps {
		w, err := s.walk(ctx, v, []int64{c.ID}, Upstream, []any{string(EdgeNeeds)}, Limits{Depth: 1, Nodes: 1000}, when)
		if err != nil {
			return nil, err
		}
		hits, _, err := s.hits(ctx, w, []int64{c.ID})
		if err != nil {
			return nil, err
		}
		cn := ComponentNeeds{Component: c, Repository: repoOfComponent(c), File: RequirementsPath}
		for _, h := range hits {
			if h.Node.Kind != KindRequirement {
				continue
			}
			f := Fact{Facet: strOf(h.Node.Attrs["facet"]), Value: strOf(h.Node.Attrs["value"]), Source: Confidence(strOf(h.Node.Attrs["source"])), Evidence: strOf(h.Node.Attrs["evidence"])}
			if adv, _ := h.Node.Attrs["advice"].(bool); adv {
				cn.Advice = append(cn.Advice, Advice{Facet: f.Facet, Inferred: f.Display(), Declared: strOf(h.Node.Attrs["declared"]), Evidence: f.Evidence, Message: strOf(h.Node.Attrs["message"])})
				continue
			}
			cn.Facts = append(cn.Facts, f)
		}
		sort.SliceStable(cn.Facts, func(i, j int) bool {
			if cn.Facts[i].Facet != cn.Facts[j].Facet {
				return cn.Facts[i].Facet < cn.Facts[j].Facet
			}
			return cn.Facts[i].Value < cn.Facts[j].Value
		})
		sort.Slice(cn.Advice, func(i, j int) bool {
			return cn.Advice[i].Facet+cn.Advice[i].Inferred < cn.Advice[j].Facet+cn.Advice[j].Inferred
		})
		if ref.Kind == KindRepository && len(cn.Facts) == 0 && len(cn.Advice) == 0 {
			continue // a repository's answer lists the components that need something
		}
		out = append(out, cn)
	}
	return out, nil
}

// PlacementResult says what can run a component and what is missing.
type PlacementResult struct {
	Component  Node        `json:"component"`
	Facts      []Fact      `json:"facts"`
	Fits       []Candidate `json:"fits"`
	NearMisses []Candidate `json:"near_misses,omitempty"` // fail on at most two needs
	Gaps       []Gap       `json:"gaps"`
	Hosts      int         `json:"hosts_considered"`
}

// Placement judges every machine and platform the viewer can see against a
// component's needs: what satisfies it, what nearly does, and the gaps.
func (s *Store) Placement(ctx context.Context, v Viewer, ref NodeRef, when When) ([]PlacementResult, error) {
	needs, err := s.Needs(ctx, v, ref, when)
	if err != nil {
		return nil, err
	}
	hosts, err := s.visibleNodesOfKinds(ctx, v, when, KindMachine, KindPlatform)
	if err != nil {
		return nil, err
	}
	// Data platforms (databases, queues) provide services but do not run
	// workloads, so they are not candidates for where a component runs.
	var runners []Node
	for _, h := range hosts {
		if k := strOf(h.Attrs["platform_kind"]); h.Kind == KindMachine || k == "cluster" || k == "serverless" || k == "" {
			runners = append(runners, h)
		}
	}
	var out []PlacementResult
	for _, n := range needs {
		cands, gaps := Judge(n.Facts, runners, hosts)
		r := PlacementResult{Component: n.Component, Facts: n.Facts, Gaps: gaps, Hosts: len(runners)}
		for _, c := range cands {
			switch {
			case c.Fits:
				r.Fits = append(r.Fits, c)
			case len(c.Missing) <= 2 && len(r.NearMisses) < 10:
				r.NearMisses = append(r.NearMisses, c)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// ---- the machine specification ------------------------------------------

// SpecOptions tune the specification.
type SpecOptions struct {
	// HeadroomPercent is added to CPU, memory and storage (default 25).
	HeadroomPercent int
}

// MachineSpec is a neutral, structured description of a machine that can
// run a set of components. It is an export: the VM, image and
// infrastructure work turn it into a VM definition, an image build or
// infrastructure code. Nothing is provisioned from it here.
type MachineSpec struct {
	Version    int              `json:"version" yaml:"version"`
	Kind       string           `json:"kind" yaml:"kind"`
	Components []SpecComponent  `json:"components" yaml:"components"`
	Platform   string           `json:"platform,omitempty" yaml:"platform,omitempty"`
	Arch       string           `json:"architecture,omitempty" yaml:"architecture,omitempty"`
	ArchOpts   []string         `json:"architecture_options,omitempty" yaml:"architecture_options,omitempty"`
	CPU        SpecAmount       `json:"cpu" yaml:"cpu"`
	Memory     SpecAmount       `json:"memory" yaml:"memory"`
	Storage    SpecAmount       `json:"storage" yaml:"storage"`
	Headroom   int              `json:"headroom_percent" yaml:"headroom_percent"`
	Devices    []SpecDevice     `json:"devices,omitempty" yaml:"devices,omitempty"`
	OS         SpecOS           `json:"os" yaml:"os"`
	Ports      []SpecPort       `json:"ports,omitempty" yaml:"ports,omitempty"`
	Data       []SpecData       `json:"data_services,omitempty" yaml:"data_services,omitempty"`
	Provenance []SpecProvenance `json:"provenance" yaml:"provenance"`
	Conflicts  []string         `json:"conflicts,omitempty" yaml:"conflicts,omitempty"`
	Warnings   []string         `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// SpecComponent names a component the specification covers.
type SpecComponent struct {
	Component  string `json:"component" yaml:"component"`
	Repository string `json:"repository,omitempty" yaml:"repository,omitempty"`
}

// SpecAmount is a required amount and the amount with headroom. Cores are
// in thousandths (millicores), memory and storage in bytes; Unit says which.
type SpecAmount struct {
	Required     int64  `json:"required" yaml:"required"`
	WithHeadroom int64  `json:"with_headroom" yaml:"with_headroom"`
	Unit         string `json:"unit" yaml:"unit"`
	Display      string `json:"display" yaml:"display"`
}

// SpecDevice is a device the machine must have.
type SpecDevice struct {
	Kind   string `json:"kind" yaml:"kind"`
	Count  int    `json:"count" yaml:"count"`
	Vendor string `json:"vendor,omitempty" yaml:"vendor,omitempty"`
}

// SpecOS is what the operating system must offer.
type SpecOS struct {
	Family    string   `json:"family,omitempty" yaml:"family,omitempty"`
	MinKernel string   `json:"min_kernel,omitempty" yaml:"min_kernel,omitempty"`
	Features  []string `json:"features,omitempty" yaml:"features,omitempty"`
}

// SpecPort is a port the machine must be able to listen on.
type SpecPort struct {
	Port      int      `json:"port" yaml:"port"`
	Protocol  string   `json:"protocol" yaml:"protocol"`
	Component []string `json:"components" yaml:"components"`
}

// SpecData is a data service the components need, which the machine, or a
// platform elsewhere, must provide.
type SpecData struct {
	Kind       string   `json:"kind" yaml:"kind"`
	Constraint string   `json:"version,omitempty" yaml:"version,omitempty"`
	Components []string `json:"components" yaml:"components"`
}

// SpecProvenance says where each requirement came from.
type SpecProvenance struct {
	Component string     `json:"component" yaml:"component"`
	Facet     string     `json:"facet" yaml:"facet"`
	Value     string     `json:"value" yaml:"value"`
	Source    Confidence `json:"source" yaml:"source"`
	Evidence  string     `json:"evidence,omitempty" yaml:"evidence,omitempty"`
}

// Specification builds the machine specification for components (or the
// components of repositories) that run together on one machine. It
// adds up CPU, memory and storage and adds headroom, takes the architectures
// all can run on, unions devices, features and ports, takes the highest
// kernel minimum, and reports what cannot be reconciled as conflicts rather
// than choosing silently.
func (s *Store) Specification(ctx context.Context, v Viewer, refs []NodeRef, opt SpecOptions, when When) (MachineSpec, error) {
	if opt.HeadroomPercent <= 0 {
		opt.HeadroomPercent = 25
	}
	if opt.HeadroomPercent > 300 {
		return MachineSpec{}, fmt.Errorf("%w: headroom is a percentage up to 300", ErrInvalid)
	}
	var all []ComponentNeeds
	seen := map[string]bool{}
	for _, r := range refs {
		ns, err := s.Needs(ctx, v, r, when)
		if err != nil {
			return MachineSpec{}, err
		}
		for _, n := range ns {
			if !seen[n.Component.Key] {
				seen[n.Component.Key] = true
				all = append(all, n)
			}
		}
	}
	if len(all) == 0 {
		return MachineSpec{}, fmt.Errorf("%w: no components to specify", ErrNotFound)
	}
	return buildSpec(all, opt.HeadroomPercent), nil
}

// specBuilder gathers what the components of one specification need.
type specBuilder struct {
	sp               MachineSpec
	cpu, mem, sto    int64
	archSets         [][]string
	platforms, osFam map[string]bool
	features         map[string]bool
	kernel           string
	devices          map[string]*SpecDevice
	ports            map[string]*SpecPort
	data             map[string]*SpecData
}

func buildSpec(all []ComponentNeeds, headroom int) MachineSpec {
	b := &specBuilder{sp: MachineSpec{Version: 1, Kind: "launchway.machine-specification", Headroom: headroom},
		platforms: map[string]bool{}, osFam: map[string]bool{}, features: map[string]bool{},
		devices: map[string]*SpecDevice{}, ports: map[string]*SpecPort{}, data: map[string]*SpecData{}}
	for _, c := range all {
		b.add(c)
	}
	return b.finish(headroom)
}

// add takes in one component's facts.
func (b *specBuilder) add(c ComponentNeeds) {
	name := c.Component.Name
	b.sp.Components = append(b.sp.Components, SpecComponent{c.Component.Key, c.Repository})
	var archs []string
	haveCPU, haveMem := false, false
	for _, f := range c.Facts {
		b.sp.Provenance = append(b.sp.Provenance, SpecProvenance{c.Component.Key, f.Facet, f.Display(), f.Source, f.Evidence})
		switch f.Facet {
		case FacetPlatform:
			b.platforms[f.Value] = true
		case FacetArch:
			archs = append(archs, f.Value)
		case FacetCPU:
			n, _ := strconv.ParseInt(f.Value, 10, 64)
			b.cpu, haveCPU = b.cpu+n, true
		case FacetMemory:
			n, _ := strconv.ParseInt(f.Value, 10, 64)
			b.mem, haveMem = b.mem+n, true
		case FacetStorage:
			n, _ := strconv.ParseInt(f.Value, 10, 64)
			b.sto += n
		case FacetDevice:
			b.addDevice(f.Value)
		case FacetOS:
			b.osFam[f.Value] = true
		case FacetKernel:
			if _, ver, _ := parseConstraint(f.Value); b.kernel == "" || CompareVersions(ver, b.kernel) > 0 {
				b.kernel = ver
			}
		case FacetFeature:
			b.features[f.Value] = true
		case FacetPort:
			b.addPort(f.Value, name)
		case FacetData:
			b.addData(f.Value, name)
		}
	}
	if len(archs) > 0 {
		b.archSets = append(b.archSets, archs)
	}
	if !haveCPU {
		b.sp.Warnings = append(b.sp.Warnings, fmt.Sprintf("%s has no CPU requirement, declared or inferred: it adds nothing to the total. Declare cpu in %s.", name, RequirementsPath))
	}
	if !haveMem {
		b.sp.Warnings = append(b.sp.Warnings, fmt.Sprintf("%s has no memory requirement, declared or inferred: it adds nothing to the total. Declare memory in %s.", name, RequirementsPath))
	}
}

func (b *specBuilder) addDevice(value string) {
	kind, rest, _ := strings.Cut(value, ":")
	cnt, vendor := rest, ""
	if x, y, ok := strings.Cut(rest, "@"); ok {
		cnt, vendor = x, y
	}
	n, _ := strconv.Atoi(cnt)
	d := b.devices[kind+"@"+vendor]
	if d == nil {
		d = &SpecDevice{Kind: kind, Vendor: vendor}
		b.devices[kind+"@"+vendor] = d
	}
	d.Count += max(n, 1)
}

func (b *specBuilder) addPort(value, component string) {
	p, proto, _ := strings.Cut(value, "/")
	n, _ := strconv.Atoi(p)
	pt := b.ports[value]
	if pt == nil {
		pt = &SpecPort{Port: n, Protocol: proto}
		b.ports[value] = pt
	}
	pt.Component = append(pt.Component, component)
}

func (b *specBuilder) addData(value, component string) {
	kind, con := splitData(value)
	d := b.data[kind+con]
	if d == nil {
		d = &SpecData{Kind: kind, Constraint: con}
		if strings.HasPrefix(con, ">=") {
			d.Constraint = strings.TrimPrefix(con, ">=")
		}
		b.data[kind+con] = d
	}
	d.Components = append(d.Components, component)
}

// finish reconciles what was gathered: the one platform, operating system and
// architecture all share, or a conflict saying why there is none.
func (b *specBuilder) finish(headroom int) MachineSpec {
	sp := b.sp
	sp.Platform = single(b.platforms, "platform", &sp.Conflicts)
	sp.OS.Family = single(b.osFam, "operating system", &sp.Conflicts)
	b.chooseArch(&sp)
	withHead := func(n int64) int64 { return int64(math.Ceil(float64(n) * float64(100+headroom) / 100)) }
	sp.CPU = SpecAmount{b.cpu, withHead(b.cpu), "millicores", FormatCPU(b.cpu) + " cores, " + FormatCPU(withHead(b.cpu)) + " with headroom"}
	sp.Memory = SpecAmount{b.mem, withHead(b.mem), "bytes", FormatBytes(b.mem) + ", " + FormatBytes(withHead(b.mem)) + " with headroom"}
	sp.Storage = SpecAmount{b.sto, withHead(b.sto), "bytes", FormatBytes(b.sto) + ", " + FormatBytes(withHead(b.sto)) + " with headroom"}
	sp.OS.MinKernel = b.kernel
	sp.OS.Features = sortedKeys(b.features)
	for _, k := range sortedMapKeys(b.devices) {
		sp.Devices = append(sp.Devices, *b.devices[k])
	}
	for _, k := range sortedMapKeys(b.ports) {
		p := b.ports[k]
		sort.Strings(p.Component)
		if len(p.Component) > 1 {
			sp.Conflicts = append(sp.Conflicts, fmt.Sprintf("port %d/%s is wanted by %s: they cannot share one machine's address without a proxy or another port", p.Port, p.Protocol, strings.Join(p.Component, " and ")))
		}
		sp.Ports = append(sp.Ports, *p)
	}
	sort.Slice(sp.Ports, func(i, j int) bool { return sp.Ports[i].Port < sp.Ports[j].Port })
	for _, k := range sortedMapKeys(b.data) {
		d := b.data[k]
		sort.Strings(d.Components)
		sp.Data = append(sp.Data, *d)
	}
	sort.Slice(sp.Components, func(i, j int) bool { return sp.Components[i].Component < sp.Components[j].Component })
	sort.SliceStable(sp.Provenance, func(i, j int) bool {
		if sp.Provenance[i].Component != sp.Provenance[j].Component {
			return sp.Provenance[i].Component < sp.Provenance[j].Component
		}
		return sp.Provenance[i].Facet < sp.Provenance[j].Facet
	})
	return sp
}

// chooseArch takes an architecture every component can run on.
func (b *specBuilder) chooseArch(sp *MachineSpec) {
	if len(b.archSets) == 0 {
		sp.Warnings = append(sp.Warnings, "no architecture is declared or inferred for these components")
		return
	}
	common := map[string]int{}
	for _, set := range b.archSets {
		seen := map[string]bool{}
		for _, a := range set {
			if !seen[a] {
				seen[a] = true
				common[a]++
			}
		}
	}
	for a, n := range common {
		if n == len(b.archSets) {
			sp.ArchOpts = append(sp.ArchOpts, a)
		}
	}
	sort.Strings(sp.ArchOpts)
	if len(sp.ArchOpts) == 0 {
		sp.Conflicts = append(sp.Conflicts, "the components share no architecture: "+describeArchSets(b.archSets))
		return
	}
	sp.Arch = preferredArch(sp.ArchOpts)
}

func single(set map[string]bool, what string, conflicts *[]string) string {
	switch len(set) {
	case 0:
		return ""
	case 1:
		for k := range set {
			return k
		}
	}
	ks := sortedKeys(set)
	*conflicts = append(*conflicts, "the components need different "+what+"s: "+strings.Join(ks, ", "))
	return ""
}

func preferredArch(opts []string) string {
	for _, p := range []string{"amd64", "arm64"} {
		if in(opts, p) {
			return p
		}
	}
	return opts[0]
}

func describeArchSets(sets [][]string) string {
	var parts []string
	for _, s := range sets {
		parts = append(parts, strings.Join(s, "/"))
	}
	return strings.Join(parts, " versus ")
}

func sortedMapKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// YAML writes the specification as YAML, the same structure.
func (m MachineSpec) YAML() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return "", err
	}
	out, err := yaml.Marshal(orderedFromJSON(generic))
	return string(out), err
}

// orderedFromJSON keeps keys sorted by yaml's own map handling; this exists
// so the encoder sees plain maps and lists.
func orderedFromJSON(v any) any { return v }
