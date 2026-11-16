// Package sbom reads CycloneDX JSON software bills of materials and turns
// them into a flat list of components with their licences.
//
// Only the top-level "components" list is read, with the components nested
// inside them (to MaxDepth); metadata.component, which describes the thing
// the SBOM is about rather than what it contains, is not counted, and
// neither are services, dependencies or vulnerabilities. Nothing is
// deduplicated: a component listed twice is counted twice.
//
// The files come from whoever can push code, so they are treated as hostile
// the same way the findings package treats SARIF: memory and nesting are
// bounded, nothing written in the file is trusted as a count, the whole
// parse has a time limit, and a file that is only partly readable gives the
// components read so far with a warning.
package sbom

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings/jsonscan"
)

// FormatCycloneDX names the one format Parse reads.
const FormatCycloneDX = "cyclonedx-json"

// ErrNotSBOM is wrapped by errors saying the content is not a CycloneDX SBOM.
var ErrNotSBOM = errors.New("not a CycloneDX SBOM")

// Component is one component of the SBOM.
type Component struct {
	Name     string   `json:"name"`              // "group/name" when the component has a group (an npm scope, a Maven group id)
	Version  string   `json:"version,omitempty"` // as written
	Type     string   `json:"type,omitempty"`    // library, application, framework, container, operating-system, ...
	PURL     string   `json:"purl,omitempty"`
	Licenses []string `json:"licenses,omitempty"` // for each licence entry: its SPDX id, else its name, else the expression
}

// Report is the result of parsing one file.
type Report struct {
	Format      string         // FormatCycloneDX
	SpecVersion string         // e.g. "1.5"
	Components  []Component    // at most Limits.MaxComponents, nested ones flattened, in file order (a component before those inside it)
	Total       int            // every component found, including any beyond MaxComponents
	Licenses    map[string]int // licence → number of components that have it, over all components; see NoLicence and OtherLicences
	Truncated   bool           // fewer Components than Total
	Warning     string         // a problem that did not stop parsing
}

// Special keys in Report.Licenses.
const (
	NoLicence     = "(none)"  // components that name no licence
	OtherLicences = "(other)" // components whose licence came after MaxLicenceKeys others
)

// Limits bounds the work and memory one parse may use.
type Limits struct {
	MaxComponents int           // default 20000
	MaxDepth      int           // default 64: JSON nesting accepted
	MaxNameBytes  int           // default 300, for Name
	Timeout       time.Duration // default 10s for the whole parse
}

// Fixed limits for the smaller fields.
const (
	MaxLicenceKeys  = 200 // distinct licences counted in Report.Licenses
	maxLicences     = 10  // Component.Licenses
	maxLicenceBytes = 200
	maxVersionBytes = 200
	maxTypeBytes    = 50
	maxPURLBytes    = 1000
	maxWarnBytes    = 2000
)

// DefaultLimits returns the default limits. Zero fields in a Limits passed
// to Parse are taken as the defaults.
func DefaultLimits() Limits {
	return Limits{MaxComponents: 20000, MaxDepth: 64, MaxNameBytes: 300, Timeout: 10 * time.Second}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxComponents <= 0 {
		l.MaxComponents = d.MaxComponents
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxNameBytes <= 0 {
		l.MaxNameBytes = d.MaxNameBytes
	}
	if l.Timeout <= 0 {
		l.Timeout = d.Timeout
	}
	return l
}

func notSBOM(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrNotSBOM}, args...)...)
}

// fatalError stops the parse with err.
type fatalError struct{ err error }

func (f *fatalError) Error() string { return f.err.Error() }

type parser struct {
	lim       Limits
	dl        *jsonscan.Deadline
	s         *jsonscan.Scanner
	shrinkAt  int
	rep       Report
	other     int // components counted under OtherLicences
	at        int // offset of the last component read
	elements  int
	bad, deep int
	warnings  []string
}

// Parse reads a CycloneDX JSON SBOM.
func Parse(ctx context.Context, data []byte, lim Limits) (*Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lim = lim.withDefaults()
	dl := jsonscan.NewDeadline(ctx, lim.Timeout)
	body, err := startOfJSON(data)
	if err != nil {
		return nil, err
	}
	if dl.Expired() {
		return nil, dl.StopError("components")
	}
	p := &parser{
		lim:      lim,
		dl:       dl,
		shrinkAt: max(4*lim.MaxNameBytes, 4*maxPURLBytes, 16<<10),
		rep:      Report{Format: FormatCycloneDX, Components: []Component{}, Licenses: map[string]int{}},
	}
	return p.parse(body)
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func startOfJSON(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, notSBOM("the file is empty")
	}
	body := data
	if len(body) >= 3 && body[0] == utf8BOM[0] && body[1] == utf8BOM[1] && body[2] == utf8BOM[2] {
		body = body[3:]
	}
	i := 0
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	if i == len(body) {
		return nil, notSBOM("the file holds only white space")
	}
	body = body[i:]
	switch body[0] {
	case '{':
		return body, nil
	case '<':
		return nil, notSBOM("it is XML or HTML (it starts with %s); only CycloneDX JSON is read", jsonscan.Snippet(body))
	case '[':
		return nil, notSBOM("the JSON is a list; a CycloneDX SBOM is an object")
	}
	return nil, notSBOM("it is not JSON (it starts with %s)", jsonscan.Snippet(body))
}

func (p *parser) parse(body []byte) (*Report, error) {
	s := jsonscan.New(body, p.dl)
	p.s = s
	format, formatSeen, compsSeen, specSeen := "", false, false, false
	err := s.Object(func(key string) error {
		switch key {
		case "spdxVersion":
			return &fatalError{notSBOM("this is an SPDX document; only CycloneDX JSON is read")}
		case "bomFormat":
			v, info, err := s.Value()
			if err != nil {
				return err
			}
			formatSeen = true
			format, _ = jsonscan.String(jsonscan.Shrink(v, info, 100))
			if !strings.EqualFold(strings.TrimSpace(format), "CycloneDX") {
				return &fatalError{notSBOM("its bomFormat is %s, not \"CycloneDX\"", strconv.Quote(jsonscan.Line(format, 40)))}
			}
			return nil
		case "specVersion":
			v, info, err := s.Value()
			if err != nil {
				return err
			}
			specSeen = true
			ver, _ := jsonscan.String(jsonscan.Shrink(v, info, 100))
			p.rep.SpecVersion = jsonscan.Line(ver, 20)
			return nil
		case "components":
			if compsSeen || s.Peek() != '[' {
				return s.Skip()
			}
			compsSeen = true
			return s.Array(p.element)
		}
		return s.Skip()
	})
	var fe *fatalError
	var se *jsonscan.SyntaxError
	switch {
	case errors.As(err, &fe):
		return nil, fe.err
	case errors.Is(err, jsonscan.ErrStopped):
		if p.elements == 0 {
			return nil, p.dl.StopError("components")
		}
		p.warnFirst(fmt.Sprintf("parsing %s; components after line %d are missing", p.dl.What(), s.Line(p.at)))
	case errors.As(err, &se):
		if !formatSeen {
			if se.EOF {
				return nil, notSBOM("the file ends before it says it is CycloneDX (it has no bomFormat)")
			}
			return nil, notSBOM("the JSON could not be read (line %d: %s)", s.Line(se.Offset), se.Msg)
		}
		if se.EOF {
			p.warnFirst("the file ends part-way through; components after that are missing")
		} else {
			p.warnFirst(fmt.Sprintf("the JSON is broken at line %d (%s); components after that are missing", s.Line(se.Offset), se.Msg))
		}
	case err != nil:
		return nil, err
	case !formatSeen:
		return nil, notSBOM("the JSON object has no bomFormat \"CycloneDX\"")
	case s.Rest():
		p.warn("there is more data after the end of the SBOM (line %d); it was ignored", s.Line(s.Pos()))
	}
	switch {
	case !specSeen && err == nil:
		// Only said of a whole file: a cut one may have had it further on.
		p.warn("the SBOM does not give its specVersion; it was read as CycloneDX 1.7")
	case specSeen && !knownSpec(p.rep.SpecVersion):
		p.warn("specVersion %s is not one this reader knows (1.2 to 1.7); it was read anyway", strconv.Quote(p.rep.SpecVersion))
	}
	return p.finish(), nil
}

func knownSpec(v string) bool {
	switch v {
	case "1.2", "1.3", "1.4", "1.5", "1.6", "1.7":
		return true
	}
	return false
}

func (p *parser) warn(format string, args ...any) {
	if len(p.warnings) < 8 {
		p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
	}
}

func (p *parser) warnFirst(w string) { p.warnings = append([]string{w}, p.warnings...) }

// The nesting depth of a top-level component: { components [ here.
const depthComponent = 2

// element reads one element of the top-level components list.
func (p *parser) element() error {
	s := p.s
	s.Peek()
	start := s.Pos()
	v, info, err := s.Value()
	if err != nil {
		return err
	}
	p.at = s.Pos()
	p.elements++
	if depthComponent+info.Depth > p.lim.MaxDepth {
		p.deep++
		return nil
	}
	if !jsonscan.Valid(v) {
		p.bad++
		return nil
	}
	// The element is well-formed and within the depth limit; walk it again
	// to pick out its fields and the components nested inside it.
	if err := p.component(s.At(start)); err != nil {
		if errors.Is(err, jsonscan.ErrStopped) {
			return err
		}
		p.bad++
	}
	return nil
}

// component reads a component object and, after it, the components nested
// in it. Nesting is bounded because the top-level element was checked
// against MaxDepth before this is called.
func (p *parser) component(s *jsonscan.Scanner) error {
	if s.Peek() != '{' {
		p.bad++
		return s.Skip()
	}
	var name, group, version, typ, purl string
	var licences []string
	nested := -1
	err := s.Object(func(key string) error {
		switch key {
		case "name", "group", "version", "type", "purl":
			v, info, err := s.Value()
			if err != nil {
				return err
			}
			str, _ := jsonscan.String(jsonscan.Shrink(v, info, p.shrinkAt))
			switch key {
			case "name":
				name = str
			case "group":
				group = str
			case "version":
				version = str
			case "type":
				typ = str
			default:
				purl = str
			}
			return nil
		case "licenses":
			v, info, err := s.Value()
			if err != nil {
				return err
			}
			licences = readLicences(jsonscan.Shrink(v, info, p.shrinkAt))
			return nil
		case "components":
			if nested >= 0 || s.Peek() != '[' {
				return s.Skip()
			}
			nested = s.Pos()
			return s.Skip()
		}
		return s.Skip()
	})
	if err != nil {
		return err
	}
	p.add(name, group, version, typ, purl, licences)
	if nested >= 0 {
		ns := s.At(nested)
		return ns.Array(func() error { return p.component(ns) })
	}
	return nil
}

// readLicences reads a CycloneDX licenses list: each entry gives a licence
// object with an SPDX id or a name, or an SPDX expression.
func readLicences(v []byte) []string {
	var out []string
	jsonscan.Each(v, func(e []byte) bool {
		var id, name, expr string
		jsonscan.Members(e, func(key string, val []byte) bool {
			switch key {
			case "license":
				jsonscan.Members(val, func(k string, x []byte) bool {
					switch k {
					case "id":
						id, _ = jsonscan.String(x)
					case "name":
						name, _ = jsonscan.String(x)
					}
					return true
				})
			case "expression":
				expr, _ = jsonscan.String(val)
			}
			return true
		})
		l := id
		if strings.TrimSpace(l) == "" {
			l = name
		}
		if strings.TrimSpace(l) == "" {
			l = expr
		}
		if l = jsonscan.Line(l, maxLicenceBytes); l != "" && !contains(out, l) {
			out = append(out, l)
		}
		return len(out) < maxLicences
	})
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// add counts a component and keeps it while there is room. Components
// without a name are not counted.
func (p *parser) add(name, group, version, typ, purl string, licences []string) {
	name = jsonscan.Line(name, 4*p.lim.MaxNameBytes)
	if name == "" {
		return
	}
	if g := jsonscan.Line(group, 4*p.lim.MaxNameBytes); g != "" {
		name = g + "/" + name
	}
	p.rep.Total++
	if len(licences) == 0 {
		p.rep.Licenses[NoLicence]++
	}
	for _, l := range licences {
		if _, ok := p.rep.Licenses[l]; ok || len(p.rep.Licenses)-p.specialKeys() < MaxLicenceKeys {
			p.rep.Licenses[l]++
		} else {
			p.other++
		}
	}
	if len(p.rep.Components) >= p.lim.MaxComponents {
		p.rep.Truncated = true
		return
	}
	p.rep.Components = append(p.rep.Components, Component{
		Name:     jsonscan.Cut(name, p.lim.MaxNameBytes),
		Version:  jsonscan.Line(version, maxVersionBytes),
		Type:     jsonscan.Line(typ, maxTypeBytes),
		PURL:     jsonscan.Line(purl, maxPURLBytes),
		Licenses: licences,
	})
}

// specialKeys counts the keys of Licenses that are not licences.
func (p *parser) specialKeys() int {
	n := 0
	for _, k := range []string{NoLicence, OtherLicences} {
		if _, ok := p.rep.Licenses[k]; ok {
			n++
		}
	}
	return n
}

func (p *parser) finish() *Report {
	if p.other > 0 {
		p.rep.Licenses[OtherLicences] += p.other
		p.warn("more than %d different licences; the rest are counted as %s", MaxLicenceKeys, OtherLicences)
	}
	if p.bad > 0 {
		p.warn("%d components could not be read and were skipped", p.bad)
	}
	if p.deep > 0 {
		p.warn("%d components were nested more than %d deep and skipped", p.deep, p.lim.MaxDepth)
	}
	if len(p.warnings) > 0 {
		p.rep.Warning = jsonscan.Line(strings.Join(p.warnings, "; "), maxWarnBytes)
	}
	r := p.rep
	return &r
}
