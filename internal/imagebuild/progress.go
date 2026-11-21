package imagebuild

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moby/buildkit/client"
)

// progressPrinter turns the builder's progress events into log lines the
// moment they arrive. It replaces the builder library's plain display, which
// prints one vertex at a time and holds the output of the others back until
// the one it follows has finished: during a long build that showed nothing
// for minutes.
//
//	#7 [build 3/9] RUN go build ./...      a step starts
//	#7 1.204 some output                   its output, seconds since it started
//	#7 DONE 41.2s                          it ended (or CACHED, or ERROR: ...)
//	#9 sha256:ab12… 12.3MB / 45.0MB        a layer moving, at most once a second
//
// Partial lines (output without a newline yet) are held for a short while
// (tick) so a progress bar that never ends a line still shows.
type progressPrinter struct {
	log func(string)
	now func() time.Time

	mu      sync.Mutex
	next    int
	vtx     map[string]*pvertex
	status  map[string]time.Time // last progress line per layer
	partial map[string]*ppartial
}

type pvertex struct {
	n       int
	name    string
	started time.Time
	shown   bool
	done    bool
}

type ppartial struct {
	vertex string
	text   string
	at     time.Time // the builder's timestamp, for the elapsed seconds
	seen   time.Time // when it reached this runner
}

// statusEvery is the least time between two progress lines for one layer.
const statusEvery = time.Second

// partialAfter is how long output without a newline is held back.
const partialAfter = 400 * time.Millisecond

func newProgressPrinter(log func(string)) *progressPrinter {
	return &progressPrinter{log: log, now: time.Now, vtx: map[string]*pvertex{}, status: map[string]time.Time{}, partial: map[string]*ppartial{}}
}

func (p *progressPrinter) vertex(d string) *pvertex {
	v := p.vtx[d]
	if v == nil {
		p.next++
		v = &pvertex{n: p.next}
		p.vtx[d] = v
	}
	return v
}

func (p *progressPrinter) announce(v *pvertex) {
	if v.shown {
		return
	}
	v.shown = true
	name := v.name
	if name == "" {
		name = "(unnamed step)"
	}
	p.log(fmt.Sprintf("#%d %s", v.n, name))
}

// Update prints what one progress event says.
func (p *progressPrinter) Update(s *client.SolveStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, bv := range s.Vertexes {
		v := p.vertex(bv.Digest.String())
		if bv.Name != "" {
			v.name = bv.Name
		}
		if bv.Started != nil && v.started.IsZero() {
			v.started = *bv.Started
		}
		if (bv.Started != nil || bv.Completed != nil) && !v.shown {
			p.announce(v)
		}
		if bv.Completed != nil && !v.done {
			v.done = true
			p.flushVertex(bv.Digest.String(), v)
			switch {
			case bv.Error != "":
				p.log(fmt.Sprintf("#%d ERROR: %s", v.n, bv.Error))
			case bv.Cached:
				p.log(fmt.Sprintf("#%d CACHED", v.n))
			default:
				took := time.Duration(0)
				if !v.started.IsZero() {
					took = bv.Completed.Sub(v.started)
				}
				p.log(fmt.Sprintf("#%d DONE %.1fs", v.n, took.Seconds()))
			}
		}
	}
	for _, st := range s.Statuses {
		v := p.vertex(st.Vertex.String())
		p.announce(v)
		key := st.Vertex.String() + "/" + st.ID
		name := st.ID
		if st.Name != "" {
			name = st.Name
		}
		switch {
		case st.Completed != nil:
			delete(p.status, key)
			p.log(fmt.Sprintf("#%d %s done%s", v.n, name, sizeOf(st.Total, st.Current)))
		case st.Total > 0 || st.Current > 0:
			last, seen := p.status[key]
			if seen && st.Timestamp.Sub(last) < statusEvery {
				continue
			}
			p.status[key] = st.Timestamp
			if st.Total > 0 {
				p.log(fmt.Sprintf("#%d %s %s / %s", v.n, name, bytesText(st.Current), bytesText(st.Total)))
			} else {
				p.log(fmt.Sprintf("#%d %s %s", v.n, name, bytesText(st.Current)))
			}
		}
	}
	for _, l := range s.Logs {
		d := l.Vertex.String()
		v := p.vertex(d)
		p.announce(v)
		key := fmt.Sprintf("%s/%d", d, l.Stream)
		text := string(l.Data)
		if pp := p.partial[key]; pp != nil {
			text = pp.text + text
			delete(p.partial, key)
		}
		for {
			i := strings.IndexByte(text, '\n')
			if i < 0 {
				break
			}
			p.logLine(v, l.Timestamp, strings.TrimRight(text[:i], "\r"))
			text = text[i+1:]
		}
		if text != "" {
			p.partial[key] = &ppartial{vertex: d, text: text, at: l.Timestamp, seen: p.now()}
		}
	}
	for _, w := range s.Warnings {
		v := p.vertex(w.Vertex.String())
		p.announce(v)
		p.log(fmt.Sprintf("#%d WARN: %s", v.n, strings.TrimSpace(string(w.Short))))
	}
}

func (p *progressPrinter) logLine(v *pvertex, at time.Time, text string) {
	el := 0.0
	if !v.started.IsZero() && at.After(v.started) {
		el = at.Sub(v.started).Seconds()
	}
	p.log(fmt.Sprintf("#%d %.3f %s", v.n, el, text))
}

// flushVertex prints the unfinished output lines of a vertex that ended.
func (p *progressPrinter) flushVertex(d string, v *pvertex) {
	for key, pp := range p.partial {
		if pp.vertex == d {
			p.logLine(v, pp.at, pp.text)
			delete(p.partial, key)
		}
	}
}

// Tick prints output that has waited for its newline long enough.
func (p *progressPrinter) Tick(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, pp := range p.partial {
		if now.Sub(pp.seen) >= partialAfter {
			p.logLine(p.vertex(pp.vertex), pp.at, pp.text)
			delete(p.partial, key)
		}
	}
}

// Close prints whatever is still held.
func (p *progressPrinter) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, pp := range p.partial {
		p.logLine(p.vertex(pp.vertex), pp.at, pp.text)
		delete(p.partial, key)
	}
}

func sizeOf(total, cur int64) string {
	n := total
	if n == 0 {
		n = cur
	}
	if n == 0 {
		return ""
	}
	return " (" + bytesText(n) + ")"
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fkB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}
