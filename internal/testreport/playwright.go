package testreport

import (
	"strconv"
	"strings"
)

// Playwright's JSON reporter: suites (a file, then describe blocks) hold
// specs, each spec one test per project. A case is one test in one project:
// Suite is the file and describe titles joined with " › ", Name the spec's
// title, File the spec's file, Variant the project's name. The status comes
// from the final result (passed, failed, timedOut, interrupted, skipped);
// a test whose status is "flaky" passed on a retry. Earlier results are
// Attempts, Retries is their number. Attachments are the final result's,
// or, when it has none, those of the earlier failed results (a trace is
// often kept for the failed try only); a path is kept as written (often
// absolute), a body is inline base64.

type pwError struct {
	Message jsonText `json:"message"`
	Stack   jsonText `json:"stack"`
	Value   jsonText `json:"value"`
}

type pwStep struct {
	Title    jsonText `json:"title"`
	Category jsonText `json:"category"`
	Duration jsonNum  `json:"duration"`
	Error    *pwError `json:"error"`
	Steps    []pwStep `json:"steps"`
}

type pwOutput struct {
	Text   jsonText `json:"text"`
	Buffer jsonText `json:"buffer"`
}

type pwAttachment struct {
	Name        jsonText `json:"name"`
	ContentType jsonText `json:"contentType"`
	Path        jsonText `json:"path"`
	Body        jsonText `json:"body"`
}

type pwResult struct {
	Status      jsonText       `json:"status"`
	Duration    jsonNum        `json:"duration"`
	Error       *pwError       `json:"error"`
	Errors      []pwError      `json:"errors"`
	Stdout      []pwOutput     `json:"stdout"`
	Stderr      []pwOutput     `json:"stderr"`
	Retry       jsonNum        `json:"retry"`
	WorkerIndex *jsonNum       `json:"workerIndex"`
	Shard       *jsonNum       `json:"shardIndex"`
	Attachments []pwAttachment `json:"attachments"`
	Steps       []pwStep       `json:"steps"`
}

type pwAnnotation struct {
	Type        jsonText `json:"type"`
	Description jsonText `json:"description"`
}

type pwTest struct {
	ProjectName jsonText       `json:"projectName"`
	ProjectID   jsonText       `json:"projectId"`
	Status      jsonText       `json:"status"`
	Annotations []pwAnnotation `json:"annotations"`
	Results     []pwResult     `json:"results"`
}

type pwTestSlim struct {
	Status  jsonText `json:"status"`
	Results []struct {
		Status jsonText `json:"status"`
	} `json:"results"`
}

type pwSpec struct {
	Title jsonText    `json:"title"`
	File  jsonText    `json:"file"`
	Tags  jsonStrings `json:"tags"`
}

type playwrightParser struct {
	dl   *deadline
	lim  Limits
	col  *collector
	ms   int64
	stop bool
}

func parsePlaywright(dl *deadline, data []byte, lim Limits) (*Report, error) {
	p := &playwrightParser{dl: dl, lim: lim, col: newCollector(FormatPlaywright, lim)}
	var total int64
	how := jsonOK
	jsonFields(data, lim.MaxDepth, func(k string, v []byte, end jsonEnd) bool {
		switch k {
		case "suites":
			h, _ := jsonElemsPartial(v, lim.MaxDepth-1, func(s []byte) bool {
				return p.suite(s, nil, "", lim.MaxDepth-2)
			})
			if h != jsonOK {
				how = h
			} else if end != jsonOK {
				how = end
			}
			return how == jsonOK && !p.stop
		case "stats":
			var st struct {
				Duration jsonNum `json:"duration"`
			}
			if end == jsonOK && decodeInto(v, &st) {
				total = msToMs(float64(st.Duration))
			}
		}
		return true
	})
	if p.stop {
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
	// stats.duration is the run's wall-clock time, which is what a reader
	// wants; the sum of the tests is used when it is missing.
	r.DurationMs = total
	if total == 0 {
		r.DurationMs = p.ms
	}
	return r, nil
}

// suite reads one suite; titles are the describe titles above it, file the
// file the top suite is. It returns false to stop.
func (p *playwrightParser) suite(raw []byte, titles []string, file string, depth int) bool {
	var hdr struct {
		Title jsonText `json:"title"`
		File  jsonText `json:"file"`
	}
	var specs, suites []byte
	jsonFields(raw, depth, func(k string, v []byte, end jsonEnd) bool {
		switch k {
		case "title":
			decodeInto(v, &hdr.Title)
		case "file":
			decodeInto(v, &hdr.File)
		case "specs":
			specs = v
		case "suites":
			suites = v
		}
		return true
	})
	if file == "" {
		file = string(hdr.File)
		if file == "" {
			file = string(hdr.Title)
		}
	} else if len(titles) < 64 {
		titles = append(titles[:len(titles):len(titles)], string(hdr.Title))
	}
	if specs != nil {
		h, _ := jsonElemsPartial(specs, depth-1, func(s []byte) bool { return p.spec(s, titles, file, depth-2) })
		if h != jsonOK {
			p.col.jsonEndWarning(h, p.lim.MaxDepth)
			return false
		}
	}
	if p.stop {
		return false
	}
	if suites != nil {
		h, _ := jsonElemsPartial(suites, depth-1, func(s []byte) bool { return p.suite(s, titles, file, depth-2) })
		if h != jsonOK {
			p.col.jsonEndWarning(h, p.lim.MaxDepth)
			return false
		}
	}
	return !p.stop
}

func (p *playwrightParser) spec(raw []byte, titles []string, file string, depth int) bool {
	var sp pwSpec
	var tests []byte
	jsonFields(raw, depth, func(k string, v []byte, end jsonEnd) bool {
		switch k {
		case "title":
			decodeInto(v, &sp.Title)
		case "file":
			decodeInto(v, &sp.File)
		case "tags":
			decodeInto(v, &sp.Tags)
		case "tests":
			tests = v
		}
		return true
	})
	if tests == nil {
		return true
	}
	h, _ := jsonElems(tests, depth-1, func(t []byte) bool {
		if p.dl.tick() {
			p.stop = true
			return false
		}
		p.test(t, &sp, titles, file)
		return true
	})
	if h != jsonOK {
		p.col.jsonEndWarning(h, p.lim.MaxDepth)
		return false
	}
	return !p.stop
}

// pwStatus maps a result's status.
func pwStatus(s string) Status {
	switch s {
	case "passed":
		return Passed
	case "failed":
		return Failed
	case "skipped":
		return Skipped
	}
	return Errored // timedOut, interrupted, anything else
}

func (p *playwrightParser) test(raw []byte, sp *pwSpec, titles []string, file string) {
	if p.col.full() {
		var t pwTestSlim
		if !decodeInto(raw, &t) {
			return
		}
		st := Skipped
		if n := len(t.Results); n > 0 {
			st = pwStatus(string(t.Results[n-1].Status))
		}
		flaky := string(t.Status) == "flaky"
		if flaky {
			st = Passed
		}
		p.col.add(st, flaky, nil)
		return
	}
	var t pwTest
	if !decodeInto(raw, &t) {
		p.col.warnOnce("test", "some tests could not be read and were left out")
		return
	}
	st := Skipped
	var final *pwResult
	if n := len(t.Results); n > 0 {
		final = &t.Results[n-1]
		st = pwStatus(string(final.Status))
	}
	flaky := string(t.Status) == "flaky"
	if flaky {
		st = Passed
	}
	ms := int64(0)
	for i := range t.Results {
		ms = addMs(ms, msToMs(float64(t.Results[i].Duration)))
	}
	p.ms = addMs(p.ms, ms)
	p.col.add(st, flaky, func() Case {
		f := string(sp.File)
		if f == "" {
			f = file
		}
		suite := file
		for _, t := range titles {
			if t != "" {
				suite += " › " + t
			}
		}
		cs := Case{
			Suite:      cleanName(suite, p.lim.MaxNameBytes),
			Name:       cleanName(string(sp.Title), p.lim.MaxNameBytes),
			File:       cleanName(f, maxFileBytes),
			DurationMs: ms,
		}
		if final != nil {
			cs.DurationMs = msToMs(float64(final.Duration))
		}
		variant := string(t.ProjectName)
		if variant == "" {
			variant = string(t.ProjectID)
		}
		setVariant(&cs, variant)
		p.col.addTags(&cs, sp.Tags...)
		for _, a := range t.Annotations {
			if string(a.Type) == "tag" {
				p.col.addTags(&cs, string(a.Description))
			}
		}
		if variant != "" {
			p.col.addProperty(&cs, "project", variant)
		}
		if n := len(t.Results); n > 1 {
			setRetries(&cs, n-1)
			for i := range t.Results[:n-1] {
				r := &t.Results[i]
				p.col.addAttempt(&cs, pwStatus(string(r.Status)), msToMs(float64(r.Duration)), pwErrorText(r).msg)
			}
		}
		if final == nil {
			return cs
		}
		if final.WorkerIndex != nil {
			p.col.addProperty(&cs, "worker", strconv.FormatInt(int64(*final.WorkerIndex), 10))
		}
		if final.Shard != nil {
			p.col.addProperty(&cs, "shard", strconv.FormatInt(int64(*final.Shard), 10))
		}
		if st != Passed {
			e := pwErrorText(final)
			cs.Message = cleanMessage(firstLine(cleanText(e.msg)), p.lim.MaxMessageBytes, false)
			if cs.Message == "" && st == Skipped {
				for _, a := range t.Annotations {
					if (a.Type == "skip" || a.Type == "fixme") && a.Description != "" {
						cs.Message = cleanMessage(string(a.Description), p.lim.MaxMessageBytes, false)
						break
					}
				}
			}
			d := e.stack
			if d == "" {
				d = e.msg
			}
			if strings.TrimSpace(cleanText(d)) != cs.Message {
				cs.Details = cleanBlock(d, p.lim.MaxDetailsBytes, false)
			}
		}
		if st == Failed || st == Errored {
			cs.Output = cleanTail(pwOutputText(final), p.lim.MaxOutputBytes, false)
		}
		for i := range final.Steps {
			p.step(&cs, &final.Steps[i], 0)
		}
		atts := final.Attachments
		if len(atts) == 0 {
			for i := range t.Results[:len(t.Results)-1] {
				if pwStatus(string(t.Results[i].Status)) != Passed {
					atts = append(atts, t.Results[i].Attachments...)
				}
			}
		}
		for _, a := range atts {
			switch {
			case a.Path != "":
				p.col.addAttachmentPath(&cs, string(a.Name), string(a.ContentType), string(a.Path))
			case a.Body != "":
				p.col.addAttachmentData(&cs, string(a.Name), string(a.ContentType), string(a.Body), inlineBase64)
			}
		}
		return cs
	})
}

type pwErr struct{ msg, stack string }

func pwErrorText(r *pwResult) pwErr {
	e := r.Error
	if e == nil && len(r.Errors) > 0 {
		e = &r.Errors[0]
	}
	if e == nil {
		return pwErr{}
	}
	msg := string(e.Message)
	if msg == "" {
		msg = string(e.Value)
	}
	return pwErr{msg: msg, stack: string(e.Stack)}
}

func pwOutputText(r *pwResult) string {
	var b strings.Builder
	for _, l := range [][]pwOutput{r.Stdout, r.Stderr} {
		for _, o := range l {
			if b.Len() > 1<<20 {
				break
			}
			if o.Text != "" {
				b.WriteString(string(o.Text))
			} else if o.Buffer != "" {
				if d, ok := decodeBase64(string(o.Buffer), 1<<20); ok {
					b.Write(d)
				}
			}
		}
	}
	return b.String()
}

// step adds a test step; hook and fixture steps only when they failed.
func (p *playwrightParser) step(cs *Case, s *pwStep, depth int) {
	cat := string(s.Category)
	title := string(s.Title)
	if (cat == "hook" || cat == "fixture" || title == "Before Hooks" || title == "After Hooks" || title == "Worker Cleanup") && s.Error == nil {
		return
	}
	st := Passed
	var errText string
	if s.Error != nil {
		st = Failed
		errText = string(s.Error.Message)
		if errText == "" {
			errText = string(s.Error.Value)
		}
	}
	if !p.col.addStep(cs, stepIn{depth: depth, name: title, status: st, ms: msToMs(float64(s.Duration)), err: errText}) {
		return
	}
	for i := range s.Steps {
		p.step(cs, &s.Steps[i], depth+1)
	}
}
