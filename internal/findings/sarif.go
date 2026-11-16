package findings

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/findings/jsonscan"
)

// The structs elements are decoded into. Only the fields used are listed;
// encoding/json skips everything else (snippets included) without keeping
// it. Scalars accept a number or a string (see jsonscan); a value of an
// entirely wrong kind makes the whole element unreadable, and it is
// skipped.

// text is a SARIF message or multiformatMessageString: only "text" is used,
// never "markdown". A plain string is accepted too.
type text struct{ Text string }

func (t *text) UnmarshalJSON(b []byte) error {
	switch {
	case len(b) > 0 && b[0] == '"':
		return jsonscan.Decode(b, &t.Text)
	case string(b) == "null":
		return nil
	}
	var v struct {
		Text jsonscan.Str `json:"text"`
	}
	if err := jsonscan.Decode(b, &v); err != nil {
		return err
	}
	t.Text = string(v.Text)
	return nil
}

// propBag holds what is used from a property bag. Property bags are
// free-form, so nothing in one makes an element unreadable.
type propBag struct {
	score    jsonscan.Num // "security-severity"
	issueSev string       // "issue_severity"
	severity string       // "severity"
	tags     []string
}

func (p *propBag) UnmarshalJSON(b []byte) error {
	jsonscan.Members(b, func(key string, v []byte) bool {
		switch key {
		case "security-severity":
			_ = jsonscan.Decode(v, &p.score)
		case "issue_severity":
			p.issueSev, _ = jsonscan.String(v)
		case "severity":
			p.severity, _ = jsonscan.String(v)
		case "tags":
			t := jsonscan.Strings{Max: maxTags, Each: 4 * maxTagBytes}
			_ = jsonscan.Decode(v, &t)
			p.tags = t.List
		}
		return true
	})
	return nil
}

type sarifRule struct {
	ID                   jsonscan.Str `json:"id"`
	Name                 jsonscan.Str `json:"name"`
	ShortDescription     text         `json:"shortDescription"`
	FullDescription      text         `json:"fullDescription"`
	Help                 text         `json:"help"`
	HelpURI              jsonscan.Str `json:"helpUri"`
	DefaultConfiguration *struct {
		Level jsonscan.Str `json:"level"`
	} `json:"defaultConfiguration"`
	Properties propBag `json:"properties"`
}

type ruleRef struct {
	ID            jsonscan.Str `json:"id"`
	Index         jsonscan.Int `json:"index"`
	ToolComponent *struct {
		Name  jsonscan.Str `json:"name"`
		Index jsonscan.Int `json:"index"`
	} `json:"toolComponent"`
}

// suppressed is true when any suppression is in effect: its status is
// "accepted" or not given. "underReview" and "rejected" leave the result a
// finding.
type suppressed bool

func (s *suppressed) UnmarshalJSON(b []byte) error {
	jsonscan.Each(b, func(e []byte) bool {
		if len(e) == 0 || e[0] != '{' {
			return true
		}
		status := ""
		jsonscan.Members(e, func(key string, v []byte) bool {
			if key == "status" {
				status, _ = jsonscan.String(v)
				return false
			}
			return true
		})
		if st := toLowerTrim(status); st == "" || st == "accepted" {
			*s = true
			return false
		}
		return true
	})
	return nil
}

// partialFPs keeps up to maxPartialFPs non-empty string entries of a
// partialFingerprints or fingerprints object. The values only ever go into
// a hash.
type partialFPs []fpPair

type fpPair struct{ k, v string }

func (p *partialFPs) UnmarshalJSON(b []byte) error {
	jsonscan.Members(b, func(key string, v []byte) bool {
		val, ok := jsonscan.String(v)
		if !ok || strings.TrimSpace(val) == "" || key == "" {
			return true
		}
		*p = append(*p, fpPair{cutBytes(key, 100), cutBytes(val, 200)})
		return len(*p) < maxPartialFPs
	})
	return nil
}

type sarifResult struct {
	RuleID              jsonscan.Str      `json:"ruleId"`
	RuleIndex           jsonscan.Int      `json:"ruleIndex"`
	Rule                *ruleRef          `json:"rule"`
	Kind                jsonscan.Str      `json:"kind"`
	Level               jsonscan.Str      `json:"level"`
	Message             text              `json:"message"`
	Locations           jsonscan.RawFirst `json:"locations"`
	BaselineState       jsonscan.Str      `json:"baselineState"`
	Suppressions        suppressed        `json:"suppressions"`
	PartialFingerprints partialFPs        `json:"partialFingerprints"`
	Fingerprints        partialFPs        `json:"fingerprints"`
	Properties          propBag           `json:"properties"`
}

type sarifLocation struct {
	PhysicalLocation *struct {
		ArtifactLocation *struct {
			URI       jsonscan.Str `json:"uri"`
			URIBaseID jsonscan.Str `json:"uriBaseId"`
		} `json:"artifactLocation"`
		Region *struct {
			StartLine   jsonscan.Int `json:"startLine"`
			EndLine     jsonscan.Int `json:"endLine"`
			StartColumn jsonscan.Int `json:"startColumn"`
		} `json:"region"`
	} `json:"physicalLocation"`
	LogicalLocations jsonscan.RawFirst `json:"logicalLocations"`
}

type logicalLocation struct {
	FullyQualifiedName jsonscan.Str `json:"fullyQualifiedName"`
	Name               jsonscan.Str `json:"name"`
}

// The nesting depth, counting from 1 for the top-level object, at which
// each kind of element sits.
const (
	depthResult  = 4 // { runs [ { results [ here
	depthRule    = 6 // { runs [ { tool { driver { rules [ here
	depthExtRule = 7 // { runs [ { tool { extensions [ { rules [ here
)

// ruleEntry is one rule of a run, decoded only when a result uses it.
type ruleEntry struct {
	id      string
	raw     []byte // the element, nil once decoded or when unreadable
	info    *ruleInfo
	decoded bool
}

// ruleInfo is what is known about a rule once decoded.
type ruleInfo struct {
	out     Rule
	level   string // defaultConfiguration.level, if valid
	score   jsonscan.Num
	tagSev  Severity // from a tag that is exactly a severity name
	tagHit  bool
	secret  bool // the rule talks about secrets
	written bool // out has gone into the report's rules
}

type component struct {
	name  string
	rules []*ruleEntry
}

// runCtx is what one run's results need: its tool and rules.
type runCtx struct {
	tool    string
	secret  bool // the tool is a secret scanner
	driver  *component
	exts    []*component
	byID    map[string]*ruleEntry
	stored  int
	dropped bool // more rules than MaxRules
	bases   map[string]string
}

// kept is what the fingerprint needs beyond the finding itself.
type kept struct {
	partial partialFPs
}

// fpStat watches one partialFingerprints key for a value shared by every
// result, which marks it as a placeholder.
type fpStat struct {
	value  string
	count  int
	differ bool
}

type parser struct {
	lim      Limits
	dl       *jsonscan.Deadline
	s        *jsonscan.Scanner
	shrinkAt int // strings longer than this are cut before decoding

	rep      Report
	kept     []kept
	rules    map[string]*Rule // tool NUL id → rule, for kept findings
	tools    map[string]bool
	fpStats  map[string]*fpStat
	at       int // offset of the last element read
	elements int // results read, readable or not

	badResults, deepResults, badRules, deepRules, badRuns int
	dupRuns, dupResults, rulesDropped                     bool
	warnings                                              []string
}

func newParser(dl *jsonscan.Deadline, lim Limits) *parser {
	biggest := max(lim.MaxMessageBytes, lim.MaxHelpBytes, lim.MaxPathBytes, lim.MaxNameBytes, maxURIBytes)
	return &parser{
		lim:      lim,
		dl:       dl,
		shrinkAt: max(64<<10, 4*biggest),
		rep:      Report{Format: FormatSARIF, Tools: []string{}, Findings: []Finding{}, Rules: []Rule{}},
		rules:    map[string]*Rule{},
		tools:    map[string]bool{},
		fpStats:  map[string]*fpStat{},
	}
}

// errFatal stops the parse with p.fatal as the error.
type fatalError struct{ err error }

func (f *fatalError) Error() string { return f.err.Error() }

func (p *parser) parse(body []byte) (*Report, error) {
	s := jsonscan.New(body, p.dl)
	p.s = s
	runsSeen := false
	err := s.Object(func(key string) error {
		switch key {
		case "version":
			v, info, err := s.Value()
			if err != nil {
				return err
			}
			ver, ok := jsonscan.String(jsonscan.Shrink(v, info, 100))
			if !ok || v[0] != '"' {
				return &fatalError{notSARIF("its \"version\" is not a string")}
			}
			if ver != "2.1.0" {
				return &fatalError{notSARIF("version %s is not supported; write SARIF 2.1.0", strconv.Quote(jsonscan.Line(ver, 40)))}
			}
			return nil
		case "runs":
			if runsSeen {
				p.dupRuns = true
				return s.Skip()
			}
			runsSeen = true
			switch s.Peek() {
			case '[':
			case 0:
				return s.Skip() // reports the end of the data
			default:
				return &fatalError{notSARIF("its \"runs\" is not a list")}
			}
			return s.Array(p.run)
		}
		return s.Skip()
	})
	var fe *fatalError
	var se *jsonscan.SyntaxError
	switch {
	case errors.As(err, &fe):
		return nil, fe.err
	case errors.Is(err, jsonscan.ErrStopped):
		if p.elements == 0 && p.rep.Counts.Total == 0 {
			return nil, p.dl.StopError("results")
		}
		p.warnFirst(fmt.Sprintf("parsing %s; results after line %d are missing", p.dl.What(), s.Line(p.at)))
	case errors.As(err, &se):
		line := s.Line(se.Offset)
		if !runsSeen {
			return nil, notSARIF("the JSON could not be read (line %d: %s)", line, se.Msg)
		}
		if se.EOF {
			p.warnFirst("the file ends part-way through; results after that are missing")
		} else {
			p.warnFirst(fmt.Sprintf("the JSON is broken at line %d (%s); results after that are missing", line, se.Msg))
		}
	case err != nil:
		return nil, err
	case !runsSeen:
		return nil, notSARIF("the JSON object has no \"runs\" list")
	case s.Rest():
		p.warn("there is more data after the end of the report (line %d); it was ignored", s.Line(s.Pos()))
	}
	return p.finish(), nil
}

func (p *parser) warn(format string, args ...any) {
	if len(p.warnings) < 8 {
		p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
	}
}

func (p *parser) warnFirst(w string) {
	p.warnings = append([]string{w}, p.warnings...)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// run reads one element of "runs". Results may come before the tool that
// describes their rules, so the run is read in two steps: first everything
// but the results (which are only checked and skipped), then the results.
func (p *parser) run() error {
	s := p.s
	if s.Peek() != '{' {
		p.badRuns++
		return s.Skip()
	}
	rc := &runCtx{byID: map[string]*ruleEntry{}}
	resultsAt := -1
	err := s.Object(func(key string) error {
		switch key {
		case "tool":
			if rc.driver != nil || rc.exts != nil {
				return s.Skip()
			}
			return p.tool(rc)
		case "originalUriBaseIds":
			v, info, err := s.Value()
			if err != nil {
				return err
			}
			if 4+info.Depth <= p.lim.MaxDepth && jsonscan.Valid(v) {
				rc.bases = readBases(jsonscan.Shrink(v, info, p.shrinkAt))
			}
			return nil
		case "results":
			if resultsAt >= 0 {
				p.dupResults = true
				return s.Skip()
			}
			s.Peek()
			resultsAt = s.Pos()
			return s.Skip()
		}
		return s.Skip()
	})
	if rc.driver != nil {
		rc.tool = rc.driver.name
	}
	if rc.tool == "" {
		rc.tool = "(unknown tool)"
	}
	rc.secret = isSecretTool(rc.tool)
	if !p.tools[rc.tool] {
		p.tools[rc.tool] = true
		p.rep.Tools = append(p.rep.Tools, rc.tool)
	}
	if rc.dropped {
		p.rulesDropped = true
	}
	if errors.Is(err, jsonscan.ErrStopped) {
		return err
	}
	if resultsAt >= 0 {
		if rerr := p.results(rc, s.At(resultsAt)); rerr != nil {
			return rerr
		}
	}
	return err
}

func readBases(v []byte) map[string]string {
	bases := map[string]string{}
	jsonscan.Members(v, func(key string, b []byte) bool {
		var a struct {
			URI jsonscan.Str `json:"uri"`
		}
		if jsonscan.Decode(b, &a) == nil && a.URI != "" {
			bases[key] = string(a.URI)
		}
		return len(bases) < maxBases
	})
	return bases
}

func (p *parser) tool(rc *runCtx) error {
	s := p.s
	if s.Peek() != '{' {
		return s.Skip()
	}
	return s.Object(func(key string) error {
		switch key {
		case "driver":
			if rc.driver != nil {
				return s.Skip()
			}
			rc.driver = &component{}
			return p.component(rc, rc.driver, depthRule)
		case "extensions":
			if rc.exts != nil || s.Peek() != '[' {
				return s.Skip()
			}
			rc.exts = []*component{}
			return s.Array(func() error {
				if len(rc.exts) >= maxExtensions {
					return s.Skip()
				}
				c := &component{}
				rc.exts = append(rc.exts, c)
				return p.component(rc, c, depthExtRule)
			})
		}
		return s.Skip()
	})
}

// component reads a tool component: the driver or an extension.
func (p *parser) component(rc *runCtx, c *component, depth int) error {
	s := p.s
	if s.Peek() != '{' {
		return s.Skip()
	}
	seenRules := false
	return s.Object(func(key string) error {
		switch key {
		case "name":
			v, info, err := s.Value()
			if err != nil {
				return err
			}
			if name, ok := jsonscan.String(jsonscan.Shrink(v, info, 4*p.lim.MaxNameBytes)); ok {
				c.name = jsonscan.Line(name, p.lim.MaxNameBytes)
			}
			return nil
		case "rules":
			if seenRules || s.Peek() != '[' {
				return s.Skip()
			}
			seenRules = true
			return s.Array(func() error { return p.rule(rc, c, depth) })
		}
		return s.Skip()
	})
}

// rule reads one rule element. Only its id is decoded now; the rest waits
// until a result uses the rule, since tools such as semgrep describe a
// thousand rules to report eight findings.
func (p *parser) rule(rc *runCtx, c *component, depth int) error {
	v, info, err := p.s.Value()
	if err != nil {
		return err
	}
	if rc.stored >= p.lim.MaxRules {
		rc.dropped = true
		return nil
	}
	e := &ruleEntry{}
	c.rules = append(c.rules, e)
	rc.stored++
	if depth+info.Depth > p.lim.MaxDepth {
		p.deepRules++
		return nil
	}
	if !jsonscan.Valid(v) {
		p.badRules++
		return nil
	}
	v = jsonscan.Shrink(v, info, p.shrinkAt)
	var head struct {
		ID jsonscan.Str `json:"id"`
	}
	if jsonscan.Decode(v, &head) != nil {
		p.badRules++
		return nil
	}
	e.id = jsonscan.Line(string(head.ID), p.lim.MaxNameBytes)
	e.raw = v
	if _, dup := rc.byID[e.id]; e.id != "" && !dup {
		rc.byID[e.id] = e
	}
	return nil
}

// info decodes a rule the first time a result uses it.
func (p *parser) info(rc *runCtx, e *ruleEntry) *ruleInfo {
	if e == nil || e.decoded {
		if e == nil {
			return nil
		}
		return e.info
	}
	e.decoded = true
	raw := e.raw
	e.raw = nil
	if raw == nil || e.id == "" {
		return nil // unreadable, or without the id a rule must have
	}
	var r sarifRule
	if jsonscan.Decode(raw, &r) != nil {
		p.badRules++
		return nil
	}
	in := &ruleInfo{
		out: Rule{
			Tool:    rc.tool,
			ID:      e.id,
			Name:    jsonscan.Line(string(r.Name), p.lim.MaxNameBytes),
			Short:   jsonscan.Block(r.ShortDescription.Text, min(maxDescBytes, p.lim.MaxHelpBytes)),
			Full:    jsonscan.Block(r.FullDescription.Text, min(maxDescBytes, p.lim.MaxHelpBytes)),
			Help:    jsonscan.Block(r.Help.Text, p.lim.MaxHelpBytes),
			HelpURI: webURL(string(r.HelpURI)),
		},
		score: r.Properties.score,
	}
	for _, t := range r.Properties.tags {
		if t = jsonscan.Line(t, maxTagBytes); t != "" && len(in.out.Tags) < maxTags {
			in.out.Tags = append(in.out.Tags, t)
		}
	}
	for _, t := range in.out.Tags {
		if sev, ok := tagSeverity(t); ok {
			in.tagSev, in.tagHit = sev, true
			break
		}
	}
	if r.DefaultConfiguration != nil {
		in.level = sarifLevel(string(r.DefaultConfiguration.Level))
	}
	texts := append([]string{in.out.ID, in.out.Name, in.out.Short}, in.out.Tags...)
	in.secret = mentionsSecret(texts...)
	e.info = in
	return in
}

// resolve finds the rule a result refers to, and the rule id to show. An
// index is used only when it is in range and, if the result also gives an
// id, names a rule with that id; otherwise the id is looked up.
func (p *parser) resolve(rc *runCtx, r *sarifResult) (*ruleEntry, string) {
	id := string(r.RuleID)
	idx := r.RuleIndex
	comp := rc.driver
	if ref := r.Rule; ref != nil {
		if id == "" {
			id = string(ref.ID)
		}
		if ref.Index.Set {
			idx = ref.Index
		}
		if tc := ref.ToolComponent; tc != nil {
			switch {
			case tc.Index.Set:
				comp = nil
				if tc.Index.OK && tc.Index.V >= 0 && tc.Index.V < int64(len(rc.exts)) {
					comp = rc.exts[tc.Index.V]
				}
			case tc.Name != "":
				comp = nil
				name := jsonscan.Line(string(tc.Name), p.lim.MaxNameBytes)
				if rc.driver != nil && rc.driver.name == name {
					comp = rc.driver
				}
				for _, x := range rc.exts {
					if comp == nil && x.name == name {
						comp = x
					}
				}
			}
		}
	}
	id = jsonscan.Line(id, p.lim.MaxNameBytes)
	var e *ruleEntry
	if idx.Set && idx.OK && comp != nil && idx.V >= 0 && idx.V < int64(len(comp.rules)) {
		e = comp.rules[idx.V]
		if e != nil && id != "" && e.id != id && !strings.HasPrefix(id, e.id+"/") {
			e = nil // the index and the id disagree: trust the id
		}
	}
	if e == nil && id != "" {
		e = rc.byID[id]
		if e == nil {
			// A hierarchical id such as "CA2000/1" names a sub-rule of CA2000.
			if i := strings.IndexByte(id, '/'); i > 0 {
				if base := rc.byID[id[:i]]; base != nil && strings.Count(id, "/") == 1 && !strings.Contains(base.id, "/") {
					e = base
				}
			}
		}
	}
	if id == "" && e != nil {
		id = e.id
	}
	if id == "" {
		id = "(no rule)"
	}
	return e, id
}

// results reads a run's results array.
func (p *parser) results(rc *runCtx, s *jsonscan.Scanner) error {
	if s.Peek() != '[' {
		return nil // not a list: nothing to read
	}
	return s.Array(func() error {
		v, info, err := s.Value()
		if err != nil {
			return err
		}
		p.at = s.Pos()
		p.elements++
		if depthResult+info.Depth > p.lim.MaxDepth {
			p.deepResults++
			return nil
		}
		if !jsonscan.Valid(v) {
			p.badResults++
			return nil
		}
		p.result(rc, jsonscan.Shrink(v, info, p.shrinkAt))
		return nil
	})
}

func (p *parser) result(rc *runCtx, v []byte) {
	var r sarifResult
	if len(v) == 0 || v[0] != '{' || jsonscan.Decode(v, &r) != nil {
		p.badResults++
		return
	}
	var loc sarifLocation
	if r.Locations.V != nil && (r.Locations.V[0] != '{' || jsonscan.Decode(r.Locations.V, &loc) != nil) {
		p.badResults++
		return
	}
	switch toLowerTrim(string(r.Kind)) {
	case "pass", "notapplicable", "informational":
		return
	}
	if toLowerTrim(string(r.BaselineState)) == "absent" {
		return // reported as gone since the baseline: not a finding now
	}
	if r.Suppressions {
		p.rep.Counts.Suppressed++
		return
	}
	e, ruleID := p.resolve(rc, &r)
	ri := p.info(rc, e)
	secret := rc.secret || (ri != nil && ri.secret) || (ri == nil && mentionsSecret(ruleID))
	sev, level := severityOf(&r, ri, secret)

	n := &p.rep.Counts
	n.Total++
	switch sev {
	case Critical:
		n.Critical++
	case High:
		n.High++
	case Medium:
		n.Medium++
	case Low:
		n.Low++
	default:
		n.Unknown++
	}
	ids := r.PartialFingerprints
	for _, kv := range r.Fingerprints {
		ids = append(ids, fpPair{"fingerprints/" + kv.k, kv.v})
	}
	p.watchPartial(ids)
	if len(p.rep.Findings) >= p.lim.MaxFindings {
		p.rep.Truncated = true
		return
	}

	f := Finding{Tool: rc.tool, RuleID: ruleID, Severity: sev, Level: level, Secret: secret}
	f.Message = p.message(&r, ri, ruleID, secret)
	p.location(rc, &loc, &f)
	p.rep.Findings = append(p.rep.Findings, f)
	p.kept = append(p.kept, kept{partial: ids})
	if ri != nil && !ri.written {
		ri.written = true
		key := ri.out.Tool + "\x00" + ri.out.ID
		if _, ok := p.rules[key]; !ok {
			out := ri.out
			p.rules[key] = &out
		}
	}
}

// severityOf applies the mapping described in the package comment.
func severityOf(r *sarifResult, ri *ruleInfo, secret bool) (Severity, string) {
	level := sarifLevel(string(r.Level))
	if level == "" && ri != nil {
		level = ri.level
	}
	if level == "" {
		level = "warning"
	}
	sev, found := Unknown, false
	if r.Properties.score.OK {
		sev = SeverityFromScore(r.Properties.score.V)
		found = sev != Unknown
	}
	if !found && ri != nil && ri.score.OK {
		sev = SeverityFromScore(ri.score.V)
		found = sev != Unknown
	}
	if !found {
		sev, found = severityWord(r.Properties.issueSev)
	}
	if !found {
		sev, found = severityWord(r.Properties.severity)
	}
	if !found && ri != nil && ri.tagHit {
		sev, found = ri.tagSev, true
	}
	if !found {
		sev = levelSeverity(level)
	}
	if secret && sev.rank() < High.rank() {
		sev = High
	}
	return sev, level
}

// message builds a finding's message. A secret finding's message is built
// only from the rule's own description, never from the result.
func (p *parser) message(r *sarifResult, ri *ruleInfo, ruleID string, secret bool) string {
	limit := p.lim.MaxMessageBytes
	if secret {
		label := ruleID
		if ri != nil && ri.out.Short != "" {
			label = ri.out.Short
		} else if ri != nil && ri.out.Name != "" {
			label = ri.out.Name
		}
		label = strings.TrimRight(Mask(jsonscan.Line(label, 4*limit)), ".:; ")
		return jsonscan.Cut(label+secretText, limit)
	}
	raw := r.Message.Text
	if strings.TrimSpace(raw) == "" && ri != nil {
		raw = ri.out.Short
	}
	if strings.TrimSpace(raw) == "" {
		raw = ruleID
	}
	// Clean first (so an escape sequence cannot split a key in two), then
	// mask, then cut (so a cut cannot leave half a key that Mask misses).
	return jsonscan.Cut(Mask(jsonscan.Block(boundRaw(raw, 4*limit), noCut)), limit)
}

// boundRaw keeps at most n bytes of s before it is cleaned and masked. When
// it cuts, it also drops a word cut in half, so that the start of a long
// key never survives a cut that left too little of it for Mask to see.
func boundRaw(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := jsonscan.RuneStartAtOrBefore(s, n)
	for i > 0 && isTokenChar(s[i-1]) {
		i--
	}
	return s[:i] + jsonscan.CutMark
}

func (p *parser) location(rc *runCtx, loc *sarifLocation, f *Finding) {
	if pl := loc.PhysicalLocation; pl != nil {
		if al := pl.ArtifactLocation; al != nil {
			f.File = filePath(string(al.URI), string(al.URIBaseID), rc.bases, p.lim.MaxPathBytes)
		}
		if rg := pl.Region; rg != nil {
			f.StartLine = lineNumber(rg.StartLine)
			f.EndLine = lineNumber(rg.EndLine)
			f.StartColumn = lineNumber(rg.StartColumn)
		}
	}
	if f.File == "" && loc.LogicalLocations.V != nil {
		var ll logicalLocation
		if jsonscan.Decode(loc.LogicalLocations.V, &ll) == nil {
			name := string(ll.FullyQualifiedName)
			if name == "" {
				name = string(ll.Name)
			}
			f.File = jsonscan.Line(name, p.lim.MaxPathBytes)
		}
	}
}

// lineNumber keeps positive numbers below 10,000,000; anything else is 0.
func lineNumber(n jsonscan.Int) int {
	if !n.OK || n.V <= 0 || n.V >= 10_000_000 {
		return 0
	}
	return int(n.V)
}

func (p *parser) watchPartial(pf partialFPs) {
	seen := map[string]bool{}
	for _, kv := range pf {
		if seen[kv.k] {
			continue
		}
		seen[kv.k] = true
		st := p.fpStats[kv.k]
		if st == nil {
			if len(p.fpStats) >= maxFPStatsKeys {
				continue
			}
			st = &fpStat{value: kv.v}
			p.fpStats[kv.k] = st
		}
		st.count++
		if kv.v != st.value {
			st.differ = true
		}
	}
}

func (p *parser) finish() *Report {
	p.fingerprints()
	r := p.rep
	keys := make([]string, 0, len(p.rules))
	for k := range p.rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r.Rules = append(r.Rules, *p.rules[k])
	}
	if p.rulesDropped {
		r.Truncated = true
		p.warn("a run describes more than %d rules; findings of the others are counted but not described", p.lim.MaxRules)
	}
	if p.badResults > 0 {
		p.warn("%s and skipped", plural(p.badResults, "result could not be read", "results could not be read"))
	}
	if p.deepResults > 0 {
		p.warn("%s more than %d deep and skipped", plural(p.deepResults, "result was nested", "results were nested"), p.lim.MaxDepth)
	}
	if n := p.badRules + p.deepRules; n > 0 {
		p.warn("%s; findings of those rules are not described", plural(n, "rule could not be read", "rules could not be read"))
	}
	if p.badRuns > 0 {
		p.warn("%s not an object and skipped", plural(p.badRuns, "run was", "runs were"))
	}
	if p.dupRuns {
		p.warn("the report has a second \"runs\" list, which was ignored")
	}
	if p.dupResults {
		p.warn("a run has a second \"results\" list, which was ignored")
	}
	if len(p.warnings) > 0 {
		r.Warning = jsonscan.Line(strings.Join(p.warnings, "; "), maxWarnBytes)
	}
	return &r
}

// noCut is a limit no text reaches.
const noCut = math.MaxInt32

func toLowerTrim(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// cutBytes keeps at most n bytes of s, without splitting a character.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:jsonscan.RuneStartAtOrBefore(s, n)]
}
