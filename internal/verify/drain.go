package verify

import (
	"net/netip"
	"regexp"
	"strings"
)

// The clusterer's settings, from the Drain paper (He et al., "Drain: An
// Online Log Parsing Approach with Fixed Depth Tree", ICWS 2017).
const (
	// prefixLayers is how many leading tokens route a line through the
	// tree, below the layer that splits lines by token count: a tree of
	// depth 4 counting the root, the length layer and the leaves.
	prefixLayers = 2
	// simThreshold is how similar a line must be to a cluster's template
	// to join it: the share of positions with equal tokens.
	simThreshold = 0.5
	// maxChildren caps a tree node's children; further distinct tokens
	// route to the variable child.
	maxChildren = 100
	// maxLineBytes is how much of a line is kept.
	maxLineBytes = 2000
	// rawLineBytes is how much of a line is redacted before the cut to
	// maxLineBytes, so a very long line costs bounded work.
	rawLineBytes = 4 * maxLineBytes
)

// wildcard marks a variable token in a template.
const wildcard = "<*>"

// overflowTemplate is the template of the cluster that takes the lines
// that would need a new cluster once the clusterer is full.
const overflowTemplate = "<other>"

// Clusterer groups log lines into templates as they stream in, with the
// Drain algorithm: a line is split into tokens, variable-looking tokens
// (numbers, ids, addresses, times, durations) are replaced by <*>, and the
// line is routed through a fixed-depth tree by its token count and first
// tokens to a few candidate clusters; it joins the most similar one if at
// least half its tokens match, and the cluster's template turns the tokens
// that differ into <*>. Otherwise it starts a new cluster.
//
// Memory is bounded: at most MaxClusters clusters (the last one being the
// "<other>" overflow cluster once the rest are full), MaxSamples samples
// per cluster of at most MaxSampleBytes each, and lines cut to 2000 bytes.
// Every line passes through the redaction function before it is split or
// kept as a sample, so neither templates nor samples carry what it
// removes. A Clusterer is not safe for concurrent use.
type Clusterer struct {
	redact   func(string) string
	byLength map[int]*drainNode
	clusters []*drainCluster
	overflow drainCluster
	lines    int
}

type drainNode struct {
	children map[string]*drainNode
	clusters []*drainCluster // at the leaves, in creation order
}

type drainCluster struct {
	tokens  []string
	count   int
	samples []string
}

// NewClusterer returns an empty clusterer that passes every line through
// redact first (nil keeps lines as they are; the deploy runner passes
// Redact or MaskWith).
func NewClusterer(redact func(string) string) *Clusterer {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	return &Clusterer{redact: redact, byLength: map[int]*drainNode{}, overflow: drainCluster{tokens: []string{overflowTemplate}}}
}

// Lines is how many non-empty lines were added.
func (c *Clusterer) Lines() int { return c.lines }

// Add adds one log line.
func (c *Clusterer) Add(line string) {
	line = strings.TrimSpace(clip(line, rawLineBytes))
	if line == "" {
		return
	}
	line = strings.TrimSpace(clip(c.redact(line), maxLineBytes))
	if line == "" {
		return
	}
	c.lines++
	tokens := templateTokens(line)
	if cl := c.search(tokens); cl != nil {
		for i, t := range tokens {
			if cl.tokens[i] != t {
				cl.tokens[i] = wildcard
			}
		}
		cl.add(line)
		return
	}
	if len(c.clusters) >= MaxClusters-1 {
		c.overflow.add(line)
		return
	}
	cl := &drainCluster{tokens: tokens}
	cl.add(line)
	c.clusters = append(c.clusters, cl)
	c.insert(cl)
}

func (cl *drainCluster) add(line string) {
	cl.count++
	if len(cl.samples) >= MaxSamples {
		return
	}
	s := clip(line, MaxSampleBytes)
	if !contains(cl.samples, s) {
		cl.samples = append(cl.samples, s)
	}
}

// Clusters returns the clusters so far, most lines first, then by
// template. Clusters whose templates became equal are merged.
func (c *Clusterer) Clusters() []Cluster {
	var set clusterSet
	for _, cl := range c.clusters {
		set.add(Cluster{Template: strings.Join(cl.tokens, " "), Count: cl.count, Samples: cl.samples}, true)
	}
	if c.overflow.count > 0 {
		set.add(Cluster{Template: overflowTemplate, Count: c.overflow.count, Samples: c.overflow.samples}, true)
	}
	out := set.sorted()
	for i := range out {
		out[i].Samples = append([]string(nil), out[i].Samples...)
	}
	return out
}

// routeKey is the tree key for a token: tokens with digits route to the
// variable child, as in the paper.
func routeKey(t string) string {
	if t == wildcard || hasDigit(t) {
		return wildcard
	}
	return t
}

// search finds the cluster a line joins, or nil.
func (c *Clusterer) search(tokens []string) *drainCluster {
	n := c.byLength[len(tokens)]
	for i := 0; n != nil && i < prefixLayers && i < len(tokens); i++ {
		next := n.children[routeKey(tokens[i])]
		if next == nil {
			next = n.children[wildcard]
		}
		n = next
	}
	if n == nil {
		return nil
	}
	var best *drainCluster
	bestSim, bestParams := -1.0, -1
	for _, cl := range n.clusters {
		same, params := 0, 0
		for i, t := range cl.tokens {
			switch {
			case t == tokens[i]:
				same++
			case t == wildcard:
				params++
			}
		}
		sim := float64(same) / float64(len(tokens))
		if sim > bestSim || (sim == bestSim && params > bestParams) {
			best, bestSim, bestParams = cl, sim, params
		}
	}
	if bestSim >= simThreshold {
		return best
	}
	return nil
}

// insert puts a new cluster into the tree.
func (c *Clusterer) insert(cl *drainCluster) {
	n := c.byLength[len(cl.tokens)]
	if n == nil {
		n = &drainNode{children: map[string]*drainNode{}}
		c.byLength[len(cl.tokens)] = n
	}
	for i := 0; i < prefixLayers && i < len(cl.tokens); i++ {
		key := routeKey(cl.tokens[i])
		next := n.children[key]
		if next == nil && key != wildcard && len(n.children) >= maxChildren-1 {
			key = wildcard
			next = n.children[key]
		}
		if next == nil {
			next = &drainNode{children: map[string]*drainNode{}}
			n.children[key] = next
		}
		n = next
	}
	n.clusters = append(n.clusters, cl)
}

// templateTokens splits a line into tokens, masks the variable ones, and
// keeps as many as fit in MaxTemplateBytes.
func templateTokens(line string) []string {
	fields := strings.Fields(line)
	out := make([]string, 0, len(fields))
	size := 0
	for _, f := range fields {
		t := maskToken(f)
		if size > 0 {
			size++ // the space
		}
		if size+len(t) > MaxTemplateBytes {
			if len(out) == 0 {
				out = append(out, clip(t, MaxTemplateBytes))
			}
			break
		}
		size += len(t)
		out = append(out, t)
	}
	return out
}

// trimChars are stripped from both ends of a token before it is tested.
const trimChars = `,;:()[]{}"'<>`

// maskToken returns <*> for a token that looks variable, key=<*> for a
// key=value pair whose value looks variable, and the token otherwise.
func maskToken(t string) string {
	if t == wildcard {
		return t
	}
	if _, err := netip.ParseAddrPort(t); err == nil { // [::1]:443
		return wildcard
	}
	core := strings.Trim(t, trimChars)
	if core == "" {
		return t
	}
	if variable(core) {
		return wildcard
	}
	if i := strings.IndexByte(core, '='); i > 0 && i < len(core)-1 {
		if v := strings.Trim(core[i+1:], trimChars); v != "" && variable(v) {
			return core[:i+1] + wildcard
		}
	}
	if len(core) > 12 && hasDigit(core) && hasLetter(core) {
		return wildcard
	}
	return t
}

var (
	numberRe   = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?%?$`)
	versionRe  = regexp.MustCompile(`^[vV]?\d+(\.\d+)+([-+][0-9A-Za-z.-]+)?$`)
	durationRe = regexp.MustCompile(`^(\d+(\.\d+)?([a-zA-Z]{1,3}|µs))+$`)
	dateRe     = regexp.MustCompile(`^\d{4}[-/]\d{1,2}[-/]\d{1,2}([T_ ]?[0-9:.,]+)?(Z|[+-][0-9:]+)?$`)
	clockRe    = regexp.MustCompile(`^\d{1,2}:\d{2}(:\d{2})?([.,]\d+)?(Z|[+-]\d{2}:?\d{2})?$`)
	uuidRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// variable says whether a token is a value rather than a word: a number,
// version, duration or size, date or time, UUID, IP address (with or
// without a port), or a hex string of at least 8 characters.
func variable(s string) bool {
	if !hasDigit(s) {
		return isHex(s)
	}
	if numberRe.MatchString(s) || versionRe.MatchString(s) || durationRe.MatchString(s) ||
		dateRe.MatchString(s) || clockRe.MatchString(s) || uuidRe.MatchString(s) || isHex(s) {
		return true
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	if _, err := netip.ParseAddrPort(s); err == nil {
		return true
	}
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	return false
}

// isHex says whether s is at least 8 hex digits, with an optional 0x.
func isHex(s string) bool {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) < 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

func hasLetter(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i] | 0x20; c >= 'a' && c <= 'z' {
			return true
		}
	}
	return false
}
