package graph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// NodeKind says what a node is.
type NodeKind string

// The node kinds.
const (
	KindRepository  NodeKind = "repository"  // a hosted repository
	KindPackage     NodeKind = "package"     // a library or module: go:, npm:, pypi:
	KindComponent   NodeKind = "component"   // a buildable unit inside a repository
	KindRequirement NodeKind = "requirement" // what a component needs
	KindImage       NodeKind = "image"       // a container image by name (a stream of builds), base images too
	KindBuilt       NodeKind = "built"       // a concrete built thing: image digest per platform, OS image, machine image, package
	KindPipeline    NodeKind = "pipeline"
	KindAirfield    NodeKind = "airfield" // an environment
	KindDeployment  NodeKind = "deployment"
	KindMachine     NodeKind = "machine"
	KindPlatform    NodeKind = "platform" // cluster, serverless runtime, database, queue
	KindOwner       NodeKind = "owner"    // a person or Squadron
	KindCommit      NodeKind = "commit"
	KindAdvisory    NodeKind = "advisory"
)

// NodeKinds lists every kind, in the order shown to people.
var NodeKinds = []NodeKind{KindRepository, KindPackage, KindComponent, KindRequirement, KindImage, KindBuilt, KindPipeline,
	KindAirfield, KindDeployment, KindMachine, KindPlatform, KindOwner, KindCommit, KindAdvisory}

// Valid reports whether k is a known kind.
func (k NodeKind) Valid() bool {
	for _, v := range NodeKinds {
		if v == k {
			return true
		}
	}
	return false
}

// EdgeKind says how two nodes relate.
type EdgeKind string

// The edge kinds. The comment on each gives the natural reading (from reads
// as the verb to to) and how a change travels.
const (
	EdgeDependsOn    EdgeKind = "depends-on"    // from depends on to; a change in to affects from
	EdgeContains     EdgeKind = "contains"      // from holds to (a repository holds its packages and components); a change in from affects to
	EdgeDefinedIn    EdgeKind = "defined-in"    // pipeline is defined in repository; a change in the repository affects the pipeline
	EdgeBuilds       EdgeKind = "builds"        // pipeline makes built thing; a change in the pipeline affects it
	EdgeBuiltFrom    EdgeKind = "built-from"    // image built from base image, component or commit; a change in to affects from
	EdgeInstanceOf   EdgeKind = "instance-of"   // a concrete built thing of an image name; a change in the image affects it
	EdgeNeeds        EdgeKind = "needs"         // component needs requirement; a change in the requirement affects the component
	EdgeSatisfies    EdgeKind = "satisfies"     // machine or platform satisfies requirement; a change in from affects the requirement
	EdgeRunsOn       EdgeKind = "runs-on"       // deployment runs on airfield, machine or platform; a change in to affects from
	EdgeHosts        EdgeKind = "hosts"         // platform hosts machine or deployment; a change in from affects to
	EdgeDeliveredTo  EdgeKind = "delivered-to"  // built thing was delivered to a deployment (digest and time); a change in from affects to
	EdgeConfiguredBy EdgeKind = "configured-by" // machine or platform configured by a playbook or definition; a change in to affects from
	EdgeOwnedBy      EdgeKind = "owned-by"      // from is owned by to; no change travels
	EdgeContainsPkg  EdgeKind = "includes"      // built thing includes package (from its component list); a change in to affects from
	EdgeOrderBefore  EdgeKind = "order-before"  // an override: from must be built before to
)

// edgeInfo is the rule of one kind.
type edgeInfo struct {
	// rev: a change travels from to to from (the stored direction is
	// reversed from the natural reading).
	rev bool
	// none: no change travels along it.
	none bool
}

var edgeKinds = map[EdgeKind]edgeInfo{
	EdgeDependsOn: {rev: true}, EdgeContains: {}, EdgeDefinedIn: {rev: true}, EdgeBuilds: {}, EdgeBuiltFrom: {rev: true},
	EdgeInstanceOf: {rev: true}, EdgeNeeds: {rev: true}, EdgeSatisfies: {}, EdgeRunsOn: {rev: true}, EdgeHosts: {},
	EdgeDeliveredTo: {}, EdgeConfiguredBy: {rev: true}, EdgeOwnedBy: {none: true}, EdgeContainsPkg: {rev: true}, EdgeOrderBefore: {},
}

// Valid reports whether k is a known kind.
func (k EdgeKind) Valid() bool { _, ok := edgeKinds[k]; return ok }

// EdgeKinds lists every kind, sorted.
func EdgeKinds() []EdgeKind {
	var out []EdgeKind
	for k := range edgeKinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Confidence says how a fact was found.
type Confidence string

// The confidences. Declared is what a person wrote down, extracted is read
// from a file or a result, inferred is a guess the product made and says so.
const (
	Declared  Confidence = "declared"
	Extracted Confidence = "extracted"
	Inferred  Confidence = "inferred"
)

// Node is one thing in the graph. Kind and Key identify it within an
// organisation; every source that mentions the same kind and key means the
// same node, which is how a dependency in one repository meets the package
// another repository publishes.
type Node struct {
	ID         int64          `json:"id,omitempty"`
	Kind       NodeKind       `json:"kind"`
	Key        string         `json:"key"`
	Name       string         `json:"name,omitempty"`
	Subject    string         `json:"-"` // what a reader must be allowed to read; "" for public identities
	Owner      string         `json:"owner,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
	Origin     string         `json:"origin,omitempty"`
	Scope      string         `json:"scope,omitempty"`
	Ref        string         `json:"ref,omitempty"`
	Confidence Confidence     `json:"confidence,omitempty"`
	FirstSeen  int64          `json:"first_seen,omitempty"`
	LastSeen   int64          `json:"last_seen,omitempty"`
	RemovedAt  int64          `json:"removed_at,omitempty"`
}

// Ref names a node by kind and key.
func (n Node) Label() string {
	if n.Name != "" {
		return n.Name
	}
	return n.Key
}

// NodeRef is how an edge names its ends before they have numbers.
type NodeRef struct {
	Kind NodeKind `json:"kind"`
	Key  string   `json:"key"`
}

func (r NodeRef) String() string { return string(r.Kind) + ":" + r.Key }

// ParseRef reads "kind:key".
func ParseRef(s string) (NodeRef, error) {
	k, key, ok := strings.Cut(s, ":")
	if !ok || key == "" || !NodeKind(k).Valid() {
		return NodeRef{}, fmt.Errorf("%q is not a node: write it as kind:key, for example repository:team/app or package:go:example.com/lib (kinds: %s)", s, kindsText())
	}
	return NodeRef{NodeKind(k), key}, nil
}

func kindsText() string {
	var s []string
	for _, k := range NodeKinds {
		s = append(s, string(k))
	}
	return strings.Join(s, ", ")
}

// Edge is one relation, as a source asserts it (natural direction).
type Edge struct {
	ID         int64          `json:"id,omitempty"`
	Kind       EdgeKind       `json:"kind"`
	From       NodeRef        `json:"from"`
	To         NodeRef        `json:"to"`
	Qualifier  string         `json:"qualifier,omitempty"` // tells apart several edges of one kind between two nodes (a manifest file, a platform, a service)
	Constraint string         `json:"constraint,omitempty"`
	Resolved   string         `json:"resolved,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
	Subject    string         `json:"-"`
	Origin     string         `json:"origin,omitempty"`
	Scope      string         `json:"scope,omitempty"`
	Ref        string         `json:"ref,omitempty"`
	Confidence Confidence     `json:"confidence,omitempty"`
	ValidFrom  int64          `json:"valid_from,omitempty"`
	ValidTo    int64          `json:"valid_to,omitempty"`
}

// Assertion is everything one source says about one scope.
type Assertion struct {
	Nodes []Node
	Edges []Edge
}

// Add appends a node.
func (a *Assertion) Add(n Node) { a.Nodes = append(a.Nodes, n) }

// Link appends an edge.
func (a *Assertion) Link(e Edge) { a.Edges = append(a.Edges, e) }

func encodeAttrs(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeAttrs(s string) map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// Subject helpers: what a node's or edge's reader must be allowed to read.
const (
	subRepo     = "repo:"
	subProject  = "project:"
	subAirfield = "airfield:"
	// SubjectInstall is the subject of facts about the installation's machines and platforms: anyone who can read any project.
	SubjectInstall = "installation"
)

// RepoSubject is the subject of facts read from a repository.
func RepoSubject(path string) string { return subRepo + path }

// ProjectSubject is the subject of facts about a project's runs and deployments.
func ProjectSubject(name string) string { return subProject + name }

// Limits bound a traversal. A traversal that hits one says so (Truncated).
type Limits struct {
	Depth int // at most this many hops; default 8, at most 64
	Nodes int // at most this many nodes returned; default 2000, at most 50000
	Time  int // milliseconds; default 5000, at most 60000
}

// Normalised fills defaults and applies the ceilings.
func (l Limits) Normalised() Limits {
	if l.Depth <= 0 {
		l.Depth = 8
	}
	if l.Depth > 64 {
		l.Depth = 64
	}
	if l.Nodes <= 0 {
		l.Nodes = 2000
	}
	if l.Nodes > 50000 {
		l.Nodes = 50000
	}
	if l.Time <= 0 {
		l.Time = 5000
	}
	if l.Time > 60000 {
		l.Time = 60000
	}
	return l
}

// Truncation says a result is not the whole answer, and why.
type Truncation struct {
	Truncated bool   `json:"truncated"`
	Reason    string `json:"truncated_reason,omitempty"` // depth, nodes or time
}
