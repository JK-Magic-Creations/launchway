package testreport

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// Cucumber JSON, as cucumber-js, Cucumber-JVM and Karate write it: an array
// of features, each with elements (scenarios and backgrounds), each with
// steps. A case is one scenario: Suite is the feature's name, File its uri,
// Name the scenario's name. Background steps are put in front of the
// scenario's own (Cucumber-JVM and Karate write the background as an
// element of its own before each scenario). Hooks become steps only when
// they failed or carry attachments. Durations are nanoseconds.
//
// Scenario Outline examples often share a name; to keep their histories
// apart, a scenario whose keyword is an Outline (or Template), or whose
// name was already used in the feature, gets " (line N)" added, N being
// the example row's line.
//
// A scenario's status is the worst of its steps': failed, then undefined
// or ambiguous (Errored, "step is undefined: ..."), then pending (Skipped),
// then skipped (Skipped); with every step passed, or no steps, it passed.

type cukeResult struct {
	Status       jsonText `json:"status"`
	Duration     jsonNum  `json:"duration"`
	ErrorMessage jsonText `json:"error_message"`
}

type cukeEmbedding struct {
	Data     jsonText `json:"data"`
	MimeType jsonText `json:"mime_type"`
	Media    struct {
		Type jsonText `json:"type"`
	} `json:"media"`
	Name jsonText `json:"name"`
}

type cukeStep struct {
	Keyword   jsonText `json:"keyword"`
	Name      jsonText `json:"name"`
	Line      jsonNum  `json:"line"`
	Result    cukeResult
	DocString *struct {
		Value jsonText `json:"value"`
	} `json:"doc_string"`
	Rows []struct {
		Cells []jsonText `json:"cells"`
	} `json:"rows"`
	Embeddings []cukeEmbedding `json:"embeddings"`
	Output     []jsonText      `json:"output"`
	Match      struct {
		Location jsonText `json:"location"`
	} `json:"match"`
	Before []cukeStep `json:"before"`
	After  []cukeStep `json:"after"`
	hook   string
}

type cukeTag struct {
	Name jsonText `json:"name"`
}

type cukeElement struct {
	Keyword jsonText   `json:"keyword"`
	Type    jsonText   `json:"type"`
	Name    jsonText   `json:"name"`
	Line    jsonNum    `json:"line"`
	Tags    []cukeTag  `json:"tags"`
	Before  []cukeStep `json:"before"`
	Steps   []cukeStep `json:"steps"`
	After   []cukeStep `json:"after"`
}

// cukeSlim is what is read of a scenario past MaxCases: its statuses.
type cukeSlim struct {
	Type   jsonText `json:"type"`
	Before []struct{ Result cukeResult }
	Steps  []struct{ Result cukeResult }
	After  []struct{ Result cukeResult }
}

type cukeFeature struct {
	URI         jsonText  `json:"uri"`
	Name        jsonText  `json:"name"`
	Description jsonText  `json:"description"`
	Tags        []cukeTag `json:"tags"`
}

// cukeRank orders step statuses from best to worst.
func cukeRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "passed":
		return 0
	case "skipped", "unused", "":
		return 1
	case "pending":
		return 2
	case "undefined", "ambiguous":
		return 3
	case "failed":
		return 4
	}
	return 1
}

func cukeStatus(rank int) Status {
	switch rank {
	case 0:
		return Passed
	case 4:
		return Failed
	case 3:
		return Errored
	}
	return Skipped
}

type cucumberParser struct {
	dl  *deadline
	lim Limits
	col *collector
	ms  int64
}

func parseCucumber(dl *deadline, data []byte, lim Limits) (*Report, error) {
	p := &cucumberParser{dl: dl, lim: lim, col: newCollector(FormatCucumber, lim)}
	stopped := false
	how, _ := jsonElemsPartial(data, lim.MaxDepth, func(f []byte) bool {
		if dl.expired() {
			stopped = true
			return false
		}
		return p.feature(f)
	})
	if stopped || p.dl.expired() {
		if p.col.rep.Counts.Total == 0 {
			return nil, dl.stopError()
		}
		p.col.warn("parsing %s; results after that are missing", dl.what())
	} else if how != jsonOK {
		if p.col.rep.Counts.Total == 0 && how == jsonDeep {
			return nil, jsonEndError(how, lim.MaxDepth)
		}
		p.col.jsonEndWarning(how, lim.MaxDepth)
	}
	r := p.col.finish()
	r.DurationMs = p.ms
	return r, nil
}

// feature reads one feature; it returns false to stop.
func (p *cucumberParser) feature(raw []byte) bool {
	var f cukeFeature
	var elements []byte
	if jsonKind(raw) != '{' {
		p.col.warnOnce("feature", "some features could not be read and were left out")
		return true
	}
	jsonFields(raw, p.lim.MaxDepth-1, func(k string, v []byte, end jsonEnd) bool {
		switch k {
		case "uri":
			decodeInto(v, &f.URI)
		case "name":
			decodeInto(v, &f.Name)
		case "description":
			decodeInto(v, &f.Description)
		case "tags":
			decodeInto(v, &f.Tags)
		case "elements":
			elements = v
		}
		return true
	})
	// Karate 1.x writes the feature's path as its name and its name as
	// the description.
	if f.Name == f.URI && f.URI != "" {
		if d := strings.TrimSpace(firstLine(string(f.Description))); d != "" {
			f.Name = jsonText(d)
		}
	}
	if elements == nil {
		return true
	}
	seen := map[uint64]bool{}
	var background []cukeStep
	stop := false
	how, _ := jsonElems(elements, p.lim.MaxDepth-2, func(e []byte) bool {
		if p.dl.tick() {
			stop = true
			return false
		}
		if p.col.full() {
			var s cukeSlim
			if !decodeInto(e, &s) {
				return true
			}
			if strings.EqualFold(string(s.Type), "background") {
				return true
			}
			rank := 0
			for _, l := range [][]struct{ Result cukeResult }{s.Before, s.Steps, s.After} {
				for _, st := range l {
					rank = max(rank, cukeRank(string(st.Result.Status)))
				}
			}
			p.col.add(cukeStatus(rank), false, nil)
			return true
		}
		var el cukeElement
		if !decodeInto(e, &el) {
			p.col.warnOnce("scenario", "some scenarios could not be read and were left out")
			return true
		}
		if strings.EqualFold(string(el.Type), "background") || strings.EqualFold(strings.TrimSpace(string(el.Keyword)), "background") {
			background = el.Steps
			return true
		}
		p.scenario(&f, &el, background, seen)
		background = nil
		return true
	})
	if stop {
		return false
	}
	if how != jsonOK {
		p.col.jsonEndWarning(how, p.lim.MaxDepth)
		return false
	}
	return true
}

func (p *cucumberParser) scenario(f *cukeFeature, el *cukeElement, background []cukeStep, seen map[uint64]bool) {
	// Every step in order: before hooks, background, steps, after hooks.
	var all []cukeStep
	for _, h := range el.Before {
		h.hook = "Before"
		all = append(all, h)
	}
	all = append(all, background...)
	all = append(all, el.Steps...)
	for _, h := range el.After {
		h.hook = "After"
		all = append(all, h)
	}
	rank := 0
	var ms int64
	var worst *cukeStep
	for i := range all {
		s := &all[i]
		r := cukeRank(string(s.Result.Status))
		for _, h := range append(s.Before, s.After...) {
			r = max(r, cukeRank(string(h.Result.Status)))
		}
		if r > rank {
			rank, worst = r, s
		}
		ms = addMs(ms, nsToMs(float64(s.Result.Duration)))
	}
	p.ms = addMs(p.ms, ms)
	st := cukeStatus(rank)

	name := cleanName(string(el.Name), p.lim.MaxNameBytes)
	h := fnv.New64a()
	h.Write([]byte(name))
	key := h.Sum64()
	kw := strings.ToLower(string(el.Keyword))
	if strings.Contains(kw, "outline") || strings.Contains(kw, "template") || seen[key] {
		if line := int64(el.Line); line > 0 {
			name = fmt.Sprintf("%s (line %d)", name, line)
		}
	}
	if len(seen) < 100000 {
		seen[key] = true
	}

	p.col.add(st, false, func() Case {
		cs := Case{
			Suite:      cleanName(string(f.Name), p.lim.MaxNameBytes),
			Name:       cutHead(name, p.lim.MaxNameBytes, false),
			File:       cleanName(string(f.URI), maxFileBytes),
			DurationMs: ms,
		}
		if cs.Suite == "" {
			cs.Suite = cleanName(string(f.URI), p.lim.MaxNameBytes)
		}
		for _, t := range f.Tags {
			p.col.addTags(&cs, string(t.Name))
		}
		for _, t := range el.Tags {
			p.col.addTags(&cs, string(t.Name))
		}
		if worst != nil {
			label := strings.TrimSpace(string(worst.Keyword)) + " " + strings.TrimSpace(string(worst.Name))
			if worst.hook != "" {
				label = worst.hook + " hook " + string(worst.Match.Location)
			}
			msg := string(worst.Result.ErrorMessage)
			switch rank {
			case 3:
				msg = "step is " + strings.ToLower(strings.TrimSpace(string(worst.Result.Status))) + ": " + strings.TrimSpace(label)
			case 2:
				msg = "step is pending: " + strings.TrimSpace(label)
			}
			if msg != "" {
				cs.Message = cleanMessage(firstLine(cleanText(msg)), p.lim.MaxMessageBytes, false)
				if d := string(worst.Result.ErrorMessage); strings.Contains(strings.TrimSpace(d), "\n") {
					cs.Details = cleanBlock(d, p.lim.MaxDetailsBytes, false)
				}
			}
		}
		var out strings.Builder
		for i := range all {
			p.step(&cs, &all[i], 0, &out)
		}
		if st == Failed || st == Errored {
			cs.Output = cleanTail(out.String(), p.lim.MaxOutputBytes, false)
		}
		return cs
	})
}

// step adds one step (or hook) and its attachments to the case.
func (p *cucumberParser) step(cs *Case, s *cukeStep, depth int, out *strings.Builder) {
	for i := range s.Before {
		h := s.Before[i]
		h.hook = "BeforeStep"
		p.step(cs, &h, depth, out)
	}
	defer func() {
		for i := range s.After {
			h := s.After[i]
			h.hook = "AfterStep"
			p.step(cs, &h, depth, out)
		}
	}()
	rank := cukeRank(string(s.Result.Status))
	if s.hook != "" && rank < 3 && len(s.Embeddings) == 0 {
		return // a hook that did not fail and attached nothing
	}
	var attach []int
	var httpLog string
	for _, e := range s.Embeddings {
		ct := string(e.MimeType)
		if ct == "" {
			ct = string(e.Media.Type)
		}
		kind := inlineBase64
		if strings.HasPrefix(strings.ToLower(ct), "text/") {
			kind = inlineMaybe
		}
		i := p.col.addAttachmentData(cs, string(e.Name), ct, string(e.Data), kind)
		if i >= 0 {
			attach = append(attach, i)
			a := cs.Attachments[i]
			if httpLog == "" && a.Data != nil && strings.HasPrefix(strings.ToLower(ct), "text/") && looksLikeHTTPLog(string(a.Data)) {
				httpLog = string(a.Data)
				// The request log can carry credentials: keep it only
				// with them hidden.
				// "[hidden]" can be longer than what it hides: never let
				// the log grow past the size already counted.
				a.Data = []byte(cutHead(hideHTTPLogCredentials(httpLog), len(a.Data), false))
				p.col.inlineUsed -= a.Size - int64(len(a.Data))
				a.Size = int64(len(a.Data))
				cs.Attachments[i] = a
			}
		}
	}
	var text strings.Builder
	if s.DocString != nil {
		text.WriteString(string(s.DocString.Value))
	}
	for i, r := range s.Rows {
		if i >= 1000 || text.Len() > 4*p.lim.MaxStepTextBytes {
			break
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString("|")
		for _, c := range r.Cells {
			text.WriteString(" " + string(c) + " |")
		}
	}
	for _, o := range s.Output {
		if out.Len() < 4*p.lim.MaxOutputBytes {
			out.WriteString(string(o))
			out.WriteByte('\n')
		}
	}
	t := text.String()
	if httpLog == "" && looksLikeHTTPLog(t) {
		httpLog = t
	}
	var ex *HTTPExchange
	if httpLog != "" {
		ex = parseHTTPLog(httpLog, p.lim)
		t = hideHTTPLogCredentials(t)
	}
	kw := strings.TrimSpace(string(s.Keyword))
	name := string(s.Name)
	if s.hook != "" {
		kw, name = s.hook, string(s.Match.Location)
	}
	errText := string(s.Result.ErrorMessage)
	if rank == 3 && errText == "" {
		errText = "step is " + strings.ToLower(strings.TrimSpace(string(s.Result.Status)))
	}
	p.col.addStep(cs, stepIn{
		depth:   depth,
		keyword: kw,
		name:    name,
		status:  cukeStatus(rank),
		ms:      nsToMs(float64(s.Result.Duration)),
		err:     errText,
		text:    t,
		http:    ex,
		attach:  attach,
	})
}
