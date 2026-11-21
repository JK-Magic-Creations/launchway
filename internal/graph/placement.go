package graph

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// HostFacts is what a machine or a platform offers, in the neutral terms
// requirements are written in. It is read from the attributes of machine and
// platform nodes, whichever source (a discovery scan, configuration facts, a
// hypervisor or cluster inventory) put them there.
type HostFacts struct {
	Arch      []string          `json:"arch,omitempty"`
	CPU       int64             `json:"cpu_millis,omitempty"`
	Memory    int64             `json:"memory_bytes,omitempty"`
	Storage   int64             `json:"storage_bytes,omitempty"`
	Devices   map[string]int    `json:"devices,omitempty"`
	OS        string            `json:"os,omitempty"`
	Kernel    string            `json:"kernel,omitempty"`
	Features  []string          `json:"features,omitempty"`
	Platforms []string          `json:"platforms,omitempty"` // container, vm, serverless, bootc
	Data      map[string]string `json:"data,omitempty"`      // data services offered: kind -> version
	PortsUsed []int             `json:"ports_used,omitempty"`
}

// HostFactsOf reads the attributes of a machine or platform node.
func HostFactsOf(attrs map[string]any) HostFacts {
	var h HostFacts
	h.Arch = strs(attrs["arch"])
	h.CPU = num(attrs["cpu_millis"])
	if c := num(attrs["cpu_cores"]); c > 0 && h.CPU == 0 {
		h.CPU = c * 1000
	}
	h.Memory = num(attrs["memory_bytes"])
	h.Storage = num(attrs["storage_bytes"])
	h.Devices = map[string]int{}
	for _, d := range strs(attrs["devices"]) {
		k, n, _ := strings.Cut(d, ":")
		c := 1
		if n != "" {
			c, _ = strconv.Atoi(strings.SplitN(n, "@", 2)[0])
		}
		h.Devices[k] += c
	}
	h.OS, _ = attrs["os"].(string)
	h.Kernel, _ = attrs["kernel"].(string)
	h.Features = strs(attrs["features"])
	h.Platforms = strs(attrs["platforms"])
	h.Data = map[string]string{}
	if m, ok := attrs["data"].(map[string]any); ok {
		for k, v := range m {
			h.Data[k], _ = v.(string)
		}
	}
	for _, p := range strs(attrs["ports_used"]) {
		if n, err := strconv.Atoi(p); err == nil {
			h.PortsUsed = append(h.PortsUsed, n)
		}
	}
	return h
}

func strs(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else if f, ok := e.(float64); ok {
				out = append(out, strconv.Itoa(int(f)))
			}
		}
		return out
	}
	return nil
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	}
	return 0
}

var dataFamilies = map[string][]string{
	"database":       {"postgres", "mysql", "mongodb", "sqlite"},
	"queue":          {"kafka", "rabbitmq", "nats"},
	"cache":          {"redis", "memcached"},
	"object-storage": {"s3", "object-storage"},
}

// Satisfies says whether the host offers what a fact needs, and if not, why.
func (h HostFacts) Satisfies(f Fact) (bool, string) {
	switch f.Facet {
	case FacetPlatform:
		if len(h.Platforms) == 0 || in(h.Platforms, f.Value) {
			return true, ""
		}
		return false, "does not run " + f.Value + " workloads"
	case FacetArch:
		if len(h.Arch) == 0 || in(h.Arch, f.Value) {
			return true, ""
		}
		return false, "is " + strings.Join(h.Arch, "/") + ", not " + f.Value
	case FacetCPU:
		n, _ := strconv.ParseInt(f.Value, 10, 64)
		if h.CPU == 0 || h.CPU >= n {
			return true, ""
		}
		return false, fmt.Sprintf("has %s cores, needs %s", FormatCPU(h.CPU), FormatCPU(n))
	case FacetMemory:
		n, _ := strconv.ParseInt(f.Value, 10, 64)
		if h.Memory == 0 || h.Memory >= n {
			return true, ""
		}
		return false, fmt.Sprintf("has %s memory, needs %s", FormatBytes(h.Memory), FormatBytes(n))
	case FacetStorage:
		n, _ := strconv.ParseInt(f.Value, 10, 64)
		if h.Storage == 0 || h.Storage >= n {
			return true, ""
		}
		return false, fmt.Sprintf("has %s storage, needs %s", FormatBytes(h.Storage), FormatBytes(n))
	case FacetDevice:
		kind, rest, _ := strings.Cut(f.Value, ":")
		n, _ := strconv.Atoi(strings.SplitN(rest, "@", 2)[0])
		if n == 0 {
			n = 1
		}
		if h.Devices[kind] >= n {
			return true, ""
		}
		return false, fmt.Sprintf("has %d %s, needs %d", h.Devices[kind], kind, n)
	case FacetOS:
		if h.OS == "" || h.OS == f.Value {
			return true, ""
		}
		return false, "runs " + h.OS + ", not " + f.Value
	case FacetFeature:
		if in(h.Features, f.Value) {
			return true, ""
		}
		return false, "lacks " + f.Value
	case FacetKernel:
		if h.Kernel == "" || Meets(h.Kernel, f.Value) {
			return true, ""
		}
		return false, "kernel " + h.Kernel + " does not meet " + f.Value
	case FacetPort:
		p, _, _ := strings.Cut(f.Value, "/")
		n, _ := strconv.Atoi(p)
		for _, u := range h.PortsUsed {
			if u == n {
				return false, "port " + p + " is already in use"
			}
		}
		return true, ""
	case FacetData:
		kind, cons := splitData(f.Value)
		for _, k := range append([]string{kind}, dataFamilies[kind]...) {
			if v, ok := h.Data[k]; ok {
				if cons == "" || v == "" || Meets(v, cons) {
					return true, ""
				}
				return false, fmt.Sprintf("offers %s %s, needs %s", k, v, cons)
			}
		}
		return false, "offers no " + kind
	case FacetRuntime:
		return true, "" // a runtime travels inside the built thing
	}
	return true, ""
}

func splitData(v string) (kind, constraint string) {
	if i := strings.IndexAny(v, "<>="); i > 0 {
		return v[:i], v[i:]
	}
	return v, ""
}

// hostFacets are the facets a single machine is judged on; data services
// are judged against any platform in the graph.
func isHostFacet(facet string) bool { return facet != FacetData && facet != FacetRuntime }

// Candidate is a machine or platform judged against a component's needs.
type Candidate struct {
	Host      Node          `json:"host"`
	Fits      bool          `json:"fits"`
	Satisfied []Fact        `json:"satisfied"`
	Missing   []MissingFact `json:"missing,omitempty"`
}

// MissingFact is a need a host does not meet, and why.
type MissingFact struct {
	Fact   Fact   `json:"fact"`
	Reason string `json:"reason"`
}

// Gap is a combination of needs nothing in the graph satisfies together.
type Gap struct {
	Facts   []Fact `json:"facts"`
	Message string `json:"message"`
}

// Judge judges hosts against facts. Hosts are machines and platforms; data
// facts are met by any host (a platform) that offers the service.
func Judge(facts []Fact, hosts, providers []Node) (cands []Candidate, gaps []Gap) {
	var host, data []Fact
	for _, f := range facts {
		switch {
		case f.Facet == FacetRuntime:
		case isHostFacet(f.Facet):
			host = append(host, f)
		default:
			data = append(data, f)
		}
	}
	for _, h := range hosts {
		hf := HostFactsOf(h.Attrs)
		c := Candidate{Host: h, Fits: true}
		for _, f := range host {
			if ok, why := hf.Satisfies(f); ok {
				c.Satisfied = append(c.Satisfied, f)
			} else {
				c.Missing = append(c.Missing, MissingFact{f, why})
				c.Fits = false
			}
		}
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Fits != cands[j].Fits {
			return cands[i].Fits
		}
		if len(cands[i].Missing) != len(cands[j].Missing) {
			return len(cands[i].Missing) < len(cands[j].Missing)
		}
		return cands[i].Host.Key < cands[j].Host.Key
	})
	anyFit := false
	for _, c := range cands {
		anyFit = anyFit || c.Fits
	}
	// A need nothing satisfies on its own is a gap by itself.
	var solo []Fact
	for _, f := range host {
		met := false
		for _, h := range hosts {
			if ok, _ := HostFactsOf(h.Attrs).Satisfies(f); ok {
				met = true
				break
			}
		}
		if !met {
			solo = append(solo, f)
			gaps = append(gaps, Gap{Facts: []Fact{f}, Message: fmt.Sprintf("needs %s %s: nothing satisfies it", f.Facet, f.Display())})
		}
	}
	if !anyFit && len(solo) == 0 && len(host) > 1 {
		gaps = append(gaps, Gap{Facts: host, Message: "needs " + factsText(host) + ": each is available somewhere, but nothing satisfies them together"})
	}
	for _, f := range data {
		met := false
		for _, h := range providers {
			if ok, _ := HostFactsOf(h.Attrs).Satisfies(f); ok {
				met = true
				break
			}
		}
		if !met {
			gaps = append(gaps, Gap{Facts: []Fact{f}, Message: fmt.Sprintf("needs %s %s: no platform in the graph provides it", f.Facet, f.Display())})
		}
	}
	return cands, gaps
}

func factsText(fs []Fact) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.Facet+" "+f.Display())
	}
	return strings.Join(s, " + ")
}
