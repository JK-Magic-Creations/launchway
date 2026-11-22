// Package metrics is the product's own small metrics registry and its text
// exposition writer (the format Prometheus-compatible scrapers read). It has no
// dependency outside the standard library.
//
// Rules the registry keeps for every metric:
//
//   - Labels are fixed when the metric is declared, and a metric's label values
//     come from small closed sets (a route template, a status class, an outcome).
//     A repository, a branch, a person or a run id is never a label.
//   - Every metric has a hard cap on distinct series. A series beyond the cap
//     is dropped and counted in launchway_metrics_series_dropped_total, so a
//     mistake in a caller cannot grow memory or a scraper's storage without end.
//   - The registry holds no secret: it sees only the label values callers give it.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Kind is a metric's type in the exposition format.
type Kind string

// The metric types.
const (
	Counter   Kind = "counter"
	Gauge     Kind = "gauge"
	Histogram Kind = "histogram"
)

// DefaultSeriesCap is the most distinct label sets one metric may hold.
const DefaultSeriesCap = 500

// Desc describes a metric: what the reference documents.
type Desc struct {
	Name   string
	Kind   Kind
	Help   string
	Labels []string
	// Buckets are a histogram's upper bounds in ascending order.
	Buckets []float64
	// Cap is the most distinct series; 0 means DefaultSeriesCap.
	Cap int
}

// Vec is one metric with its series.
type Vec struct {
	desc Desc
	reg  *Registry
	mu   sync.Mutex
	rows map[string]*series
}

type series struct {
	values  []string
	v       float64
	buckets []uint64
	sum     float64
	count   uint64
}

// Registry holds metrics, in the order they were declared.
type Registry struct {
	mu      sync.Mutex
	vecs    []*Vec
	byName  map[string]*Vec
	dropped *Vec
	hooks   []func()
	hooksMu sync.Mutex
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	r := &Registry{byName: map[string]*Vec{}}
	r.dropped = r.New(Desc{Name: "launchway_metrics_series_dropped_total", Kind: Counter, Labels: []string{"metric"}, Cap: 100,
		Help: "Label sets refused because their metric already held its cap of distinct series."})
	return r
}

// New declares a metric. It panics on a name used twice or a bad name, which
// is a programming error found by any test.
func (r *Registry) New(d Desc) *Vec {
	if !validName(d.Name) {
		panic("metrics: bad name " + d.Name)
	}
	if d.Kind == Histogram && len(d.Buckets) == 0 {
		panic("metrics: histogram without buckets: " + d.Name)
	}
	if d.Cap <= 0 {
		d.Cap = DefaultSeriesCap
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byName[d.Name]; dup {
		panic("metrics: declared twice: " + d.Name)
	}
	v := &Vec{desc: d, reg: r, rows: map[string]*series{}}
	r.vecs = append(r.vecs, v)
	r.byName[d.Name] = v
	return v
}

// Descs returns every declared metric, sorted by name.
func (r *Registry) Descs() []Desc {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Desc, 0, len(r.vecs))
	for _, v := range r.vecs {
		out = append(out, v.desc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// OnScrape registers a function run before every exposition; it refreshes
// gauges that are read from elsewhere (the database). It must be quick or
// keep its own cache.
func (r *Registry) OnScrape(fn func()) {
	r.hooksMu.Lock()
	r.hooks = append(r.hooks, fn)
	r.hooksMu.Unlock()
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		ok := c == '_' || c == ':' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// find returns the series for label values, creating it if the cap allows.
func (v *Vec) find(values []string) *series {
	if len(values) != len(v.desc.Labels) {
		panic(fmt.Sprintf("metrics: %s takes %d label values, got %d", v.desc.Name, len(v.desc.Labels), len(values)))
	}
	key := strings.Join(values, "\x00")
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.rows[key]; ok {
		return s
	}
	if len(v.rows) >= v.desc.Cap {
		if v.reg != nil && v.reg.dropped != nil && v != v.reg.dropped {
			v.reg.dropped.Inc(v.desc.Name)
		}
		return nil
	}
	s := &series{values: append([]string(nil), values...)}
	if v.desc.Kind == Histogram {
		s.buckets = make([]uint64, len(v.desc.Buckets))
	}
	v.rows[key] = s
	return s
}

// Inc adds one to a counter or gauge.
func (v *Vec) Inc(values ...string) { v.Add(1, values...) }

// Add adds n to a counter or gauge. A counter never goes down: a negative n is ignored.
func (v *Vec) Add(n float64, values ...string) {
	if v.desc.Kind == Counter && n < 0 {
		return
	}
	s := v.find(values)
	if s == nil {
		return
	}
	v.mu.Lock()
	s.v += n
	v.mu.Unlock()
}

// Set sets a gauge.
func (v *Vec) Set(n float64, values ...string) {
	s := v.find(values)
	if s == nil {
		return
	}
	v.mu.Lock()
	s.v = n
	v.mu.Unlock()
}

// Reset removes every series; a scrape hook that rebuilds a gauge from a
// query calls it first, so a pool that went away stops being reported.
func (v *Vec) Reset() {
	v.mu.Lock()
	v.rows = map[string]*series{}
	v.mu.Unlock()
}

// Observe records a value in a histogram.
func (v *Vec) Observe(n float64, values ...string) {
	s := v.find(values)
	if s == nil {
		return
	}
	v.mu.Lock()
	for i, ub := range v.desc.Buckets {
		if n <= ub {
			s.buckets[i]++
		}
	}
	s.sum += n
	s.count++
	v.mu.Unlock()
}

// Value returns a counter's or gauge's value for the labels (0 if none), for tests.
func (v *Vec) Value(values ...string) float64 {
	key := strings.Join(values, "\x00")
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.rows[key]; ok {
		return s.v
	}
	return 0
}

// Count returns how many observations a histogram holds for the labels.
func (v *Vec) Count(values ...string) uint64 {
	key := strings.Join(values, "\x00")
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.rows[key]; ok {
		return s.count
	}
	return 0
}

// SeriesCount returns how many distinct series the metric holds.
func (v *Vec) SeriesCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.rows)
}

// Write writes every metric in the text exposition format, after the scrape hooks ran.
func (r *Registry) Write(w io.Writer) error {
	r.hooksMu.Lock()
	hooks := append([]func(){}, r.hooks...)
	r.hooksMu.Unlock()
	for _, h := range hooks {
		h()
	}
	r.mu.Lock()
	vecs := append([]*Vec(nil), r.vecs...)
	r.mu.Unlock()
	sort.Slice(vecs, func(i, j int) bool { return vecs[i].desc.Name < vecs[j].desc.Name })
	var b strings.Builder
	for _, v := range vecs {
		v.write(&b)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (v *Vec) write(b *strings.Builder) {
	v.mu.Lock()
	rows := make([]series, 0, len(v.rows))
	for _, s := range v.rows {
		c := *s
		c.buckets = append([]uint64(nil), s.buckets...)
		rows = append(rows, c)
	}
	v.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		return strings.Join(rows[i].values, "\x00") < strings.Join(rows[j].values, "\x00")
	})
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", v.desc.Name, escapeHelp(v.desc.Help), v.desc.Name, v.desc.Kind)
	for _, s := range rows {
		switch v.desc.Kind {
		case Histogram:
			for i, ub := range v.desc.Buckets {
				fmt.Fprintf(b, "%s_bucket%s %d\n", v.desc.Name, labelText(v.desc.Labels, s.values, "le", formatFloat(ub)), s.buckets[i])
			}
			fmt.Fprintf(b, "%s_bucket%s %d\n", v.desc.Name, labelText(v.desc.Labels, s.values, "le", "+Inf"), s.count)
			fmt.Fprintf(b, "%s_sum%s %s\n", v.desc.Name, labelText(v.desc.Labels, s.values, "", ""), formatFloat(s.sum))
			fmt.Fprintf(b, "%s_count%s %d\n", v.desc.Name, labelText(v.desc.Labels, s.values, "", ""), s.count)
		default:
			fmt.Fprintf(b, "%s%s %s\n", v.desc.Name, labelText(v.desc.Labels, s.values, "", ""), formatFloat(s.v))
		}
	}
}

func labelText(names, values []string, extraName, extraValue string) string {
	if len(names) == 0 && extraName == "" {
		return ""
	}
	parts := make([]string, 0, len(names)+1)
	for i, n := range names {
		parts = append(parts, n+`="`+escapeLabel(values[i])+`"`)
	}
	if extraName != "" {
		parts = append(parts, extraName+`="`+extraValue+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

func formatFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	case math.IsNaN(f):
		return "NaN"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
