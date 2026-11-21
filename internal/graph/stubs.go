package graph

import (
	"context"
	"sort"
)

// Typed adapters for data that other work brings. Each is a Source over a
// small interface; until the work that owns the data merges and implements
// the interface, nothing implements it on this branch and tests feed each one
// fixtures. When it merges, the owner implements the interface (or writes its
// own Source) and adds it to the Runner: nothing here changes.

// MachineInfo is a machine (a virtual machine or a physical server) with its
// discovered configuration.
type MachineInfo struct {
	Name     string
	Facts    map[string]any // the attributes of HostFacts: arch, cpu_cores, memory_bytes, storage_bytes, devices, os, kernel, features, platforms, ports_used
	Provider string         // where the facts came from: discovery, configuration facts, hypervisor
	Owner    string
	// ConfiguredBy names the playbook or infrastructure definition that
	// configures it, as a repository path and file ("team/infra:site.yml").
	ConfiguredBy string
}

// PlatformInfo is something that hosts workloads or data: a cluster, a
// serverless runtime, a database, a queue.
type PlatformInfo struct {
	Name     string
	Kind     string         // cluster, serverless, database, queue
	Facts    map[string]any // as MachineInfo.Facts; data: {"postgres": "16"} for a database
	Hosts    []string       // machine names it runs on
	Provider string
	Owner    string
}

// Inventory lists machines and platforms. The fleet inventory and host
// discovery of the VM delivery work, the facts of configuration as code, and
// hypervisor and cluster inventories are Inventory implementations.
type Inventory interface {
	Machines(ctx context.Context) ([]MachineInfo, error)
	Platforms(ctx context.Context) ([]PlatformInfo, error)
}

// InventorySource asserts machines, platforms, what hosts what, and what
// configures them.
type InventorySource struct{ Inv Inventory }

// Name is "inventory".
func (InventorySource) Name() string { return "inventory" }

// Scopes is the installation.
func (InventorySource) Scopes() []string { return []string{ScopeInstallation} }

// Assert reads the inventory.
func (s InventorySource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	if s.Inv == nil {
		return a, "no inventory", nil
	}
	ms, err := s.Inv.Machines(ctx)
	if err != nil {
		return a, "", err
	}
	ps, err := s.Inv.Platforms(ctx)
	if err != nil {
		return a, "", err
	}
	for _, m := range ms {
		attrs := map[string]any{"provider": m.Provider}
		for k, v := range m.Facts {
			attrs[k] = v
		}
		a.Add(Node{Kind: KindMachine, Key: m.Name, Name: m.Name, Subject: SubjectInstall, Owner: m.Owner, Attrs: attrs, Confidence: Extracted})
		if m.Owner != "" {
			a.Add(Node{Kind: KindOwner, Key: m.Owner, Name: m.Owner})
			a.Link(Edge{Kind: EdgeOwnedBy, From: NodeRef{KindMachine, m.Name}, To: NodeRef{KindOwner, m.Owner}, Subject: SubjectInstall})
		}
		if m.ConfiguredBy != "" {
			a.Add(Node{Kind: KindRepository, Key: repoOfRef(m.ConfiguredBy), Name: repoOfRef(m.ConfiguredBy), Subject: RepoSubject(repoOfRef(m.ConfiguredBy))})
			a.Link(Edge{Kind: EdgeConfiguredBy, From: NodeRef{KindMachine, m.Name}, To: NodeRef{KindRepository, repoOfRef(m.ConfiguredBy)}, Qualifier: m.ConfiguredBy, Subject: SubjectInstall})
		}
	}
	for _, p := range ps {
		attrs := map[string]any{"platform_kind": p.Kind, "provider": p.Provider}
		for k, v := range p.Facts {
			attrs[k] = v
		}
		a.Add(Node{Kind: KindPlatform, Key: p.Name, Name: p.Name, Subject: SubjectInstall, Owner: p.Owner, Attrs: attrs, Confidence: Extracted})
		hosts := append([]string{}, p.Hosts...)
		sort.Strings(hosts)
		for _, h := range hosts {
			a.Link(Edge{Kind: EdgeHosts, From: NodeRef{KindMachine, h}, To: NodeRef{KindPlatform, p.Name}, Subject: SubjectInstall})
		}
	}
	return a, "inventory", nil
}

func repoOfRef(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i]
		}
	}
	return s
}

// RegistryEntry is an image held in a registry (the built-in registry or an
// external one): its name, digest, platform and when it was pushed.
type RegistryEntry struct {
	Image    string // host/path, no tag
	Digest   string
	Platform string
	Base     string // the base image's name, if the registry knows it
	PushedAt int64
}

// RegistryContents lists what a registry holds. The built-in registry's
// content index implements it.
type RegistryContents interface {
	Entries(ctx context.Context) ([]RegistryEntry, error)
}

// RegistrySource asserts built things from registry contents, so images that
// were not built by a pipeline here are in the graph too.
type RegistrySource struct{ Reg RegistryContents }

// Name is "registry".
func (RegistrySource) Name() string { return "registry" }

// Scopes is the installation.
func (RegistrySource) Scopes() []string { return []string{ScopeInstallation} }

// Assert reads the registry.
func (s RegistrySource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	if s.Reg == nil {
		return a, "no registry", nil
	}
	es, err := s.Reg.Entries(ctx)
	if err != nil {
		return a, "", err
	}
	for _, e := range es {
		stream := ImageName(e.Image)
		a.Add(Node{Kind: KindImage, Key: stream, Name: stream, Attrs: map[string]any{"role": "stored"}})
		bk := BuiltKey(stream, e.Digest)
		attrs := map[string]any{"digest": e.Digest, "at": e.PushedAt, "in_registry": true}
		if e.Platform != "" {
			attrs["platform"], attrs["arch"] = e.Platform, Arch(e.Platform)
		}
		a.Add(Node{Kind: KindBuilt, Key: bk, Name: shortDigestName(stream, e.Digest), Subject: SubjectInstall, Attrs: attrs, Confidence: Extracted})
		a.Link(Edge{Kind: EdgeInstanceOf, From: NodeRef{KindBuilt, bk}, To: NodeRef{KindImage, stream}, Qualifier: e.Platform, Subject: SubjectInstall})
		if e.Base != "" {
			b := ImageName(e.Base)
			a.Add(Node{Kind: KindImage, Key: b, Name: b, Attrs: map[string]any{"role": "base"}})
			a.Link(Edge{Kind: EdgeBuiltFrom, From: NodeRef{KindBuilt, bk}, To: NodeRef{KindImage, b}, Qualifier: "registry", Subject: SubjectInstall})
		}
	}
	return a, "registry", nil
}

// InfraResource is something an infrastructure definition (OpenTofu state,
// an Ansible inventory) says exists and what defines it.
type InfraResource struct {
	Kind       string // machine or platform
	Name       string
	DefinedIn  string // repository path and file
	Attributes map[string]any
}

// InfraState lists the resources of infrastructure state. Configuration as
// code's state and discovery implement it.
type InfraState interface {
	Resources(ctx context.Context) ([]InfraResource, error)
}

// InfraSource asserts what infrastructure definitions configure.
type InfraSource struct{ State InfraState }

// Name is "infrastructure".
func (InfraSource) Name() string { return "infrastructure" }

// Scopes is the installation.
func (InfraSource) Scopes() []string { return []string{ScopeInstallation} }

// Assert reads the state.
func (s InfraSource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	if s.State == nil {
		return a, "no state", nil
	}
	rs, err := s.State.Resources(ctx)
	if err != nil {
		return a, "", err
	}
	for _, r := range rs {
		kind := KindMachine
		if r.Kind == "platform" {
			kind = KindPlatform
		}
		a.Add(Node{Kind: kind, Key: r.Name, Name: r.Name, Subject: SubjectInstall, Attrs: r.Attributes, Confidence: Declared})
		if r.DefinedIn != "" {
			repo := repoOfRef(r.DefinedIn)
			a.Add(Node{Kind: KindRepository, Key: repo, Name: repo, Subject: RepoSubject(repo)})
			a.Link(Edge{Kind: EdgeConfiguredBy, From: NodeRef{kind, r.Name}, To: NodeRef{KindRepository, repo}, Qualifier: r.DefinedIn, Subject: SubjectInstall, Confidence: Declared})
		}
	}
	return a, "state", nil
}

// ClusterWorkload is something a cluster runs: a workload and the image
// digest it runs.
type ClusterWorkload struct {
	Cluster   string
	Namespace string
	Name      string
	Image     string // host/path
	Digest    string
	Node      string // the machine it runs on, if known
}

// ClusterInventory lists workloads of clusters, as the Station or Detachment
// reports them.
type ClusterInventory interface {
	Workloads(ctx context.Context) ([]ClusterWorkload, error)
}

// ClusterSource asserts what clusters run and where.
type ClusterSource struct{ Inv ClusterInventory }

// Name is "clusters".
func (ClusterSource) Name() string { return "clusters" }

// Scopes is the installation.
func (ClusterSource) Scopes() []string { return []string{ScopeInstallation} }

// Assert reads the clusters.
func (s ClusterSource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	if s.Inv == nil {
		return a, "no clusters", nil
	}
	ws, err := s.Inv.Workloads(ctx)
	if err != nil {
		return a, "", err
	}
	for _, w := range ws {
		a.Add(Node{Kind: KindPlatform, Key: w.Cluster, Name: w.Cluster, Subject: SubjectInstall, Attrs: map[string]any{"platform_kind": "cluster", "platforms": []any{"container"}}})
		dk := "cluster:" + w.Cluster + "/" + w.Namespace + "/" + w.Name
		a.Add(Node{Kind: KindDeployment, Key: dk, Name: w.Namespace + "/" + w.Name, Subject: SubjectInstall, Attrs: map[string]any{"cluster": w.Cluster, "namespace": w.Namespace, "digest": w.Digest}})
		a.Link(Edge{Kind: EdgeRunsOn, From: NodeRef{KindDeployment, dk}, To: NodeRef{KindPlatform, w.Cluster}, Subject: SubjectInstall})
		if w.Node != "" {
			a.Link(Edge{Kind: EdgeRunsOn, From: NodeRef{KindDeployment, dk}, To: NodeRef{KindMachine, w.Node}, Qualifier: "node", Subject: SubjectInstall})
		}
		if w.Digest != "" {
			stream := ImageName(w.Image)
			bk := BuiltKey(stream, w.Digest)
			a.Link(Edge{Kind: EdgeDeliveredTo, From: NodeRef{KindBuilt, bk}, To: NodeRef{KindDeployment, dk}, Qualifier: w.Digest, Attrs: map[string]any{"digest": w.Digest, "cluster": w.Cluster}, Subject: SubjectInstall})
		}
	}
	return a, "workloads", nil
}

// PlacementSource derives the satisfies edges: for every machine and
// platform and every non-advice requirement in the graph, whether the host
// satisfies it. Derived facts are confidence inferred. It is read from the
// graph itself, so it runs after the others.
type PlacementSource struct{ Store *Store }

// Name is "placement".
func (PlacementSource) Name() string { return "placement" }

// Scopes is the installation.
func (PlacementSource) Scopes() []string { return []string{ScopeInstallation} }

// Assert derives satisfies edges.
func (s PlacementSource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	hosts, err := s.Store.nodesOfKinds(ctx, KindMachine, KindPlatform)
	if err != nil {
		return a, "", err
	}
	reqs, err := s.Store.requirementNodes(ctx)
	if err != nil {
		return a, "", err
	}
	for _, h := range hosts {
		hf := HostFactsOf(h.Attrs)
		for _, r := range reqs {
			f := factOf(r)
			if f.Facet == "" || !isHostFacet(f.Facet) && f.Facet != FacetData {
				continue
			}
			if ok, _ := hf.Satisfies(f); ok && explicit(hf, f) {
				a.Link(Edge{Kind: EdgeSatisfies, From: NodeRef{h.Kind, h.Key}, To: NodeRef{KindRequirement, r.Key}, Subject: SubjectInstall, Confidence: Inferred})
			}
		}
	}
	return a, "derived", nil
}

// explicit says the host states the thing the fact is about, so that a host
// that says nothing about it is not credited with satisfying it.
func explicit(h HostFacts, f Fact) bool {
	switch f.Facet {
	case FacetArch:
		return len(h.Arch) > 0
	case FacetCPU:
		return h.CPU > 0
	case FacetMemory:
		return h.Memory > 0
	case FacetStorage:
		return h.Storage > 0
	case FacetOS:
		return h.OS != ""
	case FacetKernel:
		return h.Kernel != ""
	case FacetPlatform:
		return len(h.Platforms) > 0
	}
	return true
}

func factOf(n Node) Fact {
	f := Fact{Source: Confidence(strOf(n.Attrs["source"]))}
	f.Facet, f.Value, f.Evidence = strOf(n.Attrs["facet"]), strOf(n.Attrs["value"]), strOf(n.Attrs["evidence"])
	if adv, _ := n.Attrs["advice"].(bool); adv {
		return Fact{}
	}
	return f
}

func strOf(v any) string { s, _ := v.(string); return s }
