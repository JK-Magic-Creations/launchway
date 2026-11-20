package testreport

import (
	"encoding/json"
	"strings"
)

// mochawesome JSON (Mocha, Cypress, and cypress-mochawesome-reporter, which
// writes the same shape): results are files, each a root suite holding
// suites and tests. A case is one test: Suite is the suite titles joined
// with " › ", Name the test's title, File the root suite's file. Failed
// hooks are listed as tests too (isHook) and become Errored cases. A test's
// context (addContext) is a JSON string holding a value or a list of
// values: strings that name an image or video, and {title, value} objects
// whose value does, become attachments; other {title, value} objects become
// properties.

type mochaTest struct {
	Title    jsonText `json:"title"`
	State    jsonText `json:"state"`
	Duration jsonNum  `json:"duration"`
	Pass     bool     `json:"pass"`
	Fail     bool     `json:"fail"`
	Pending  bool     `json:"pending"`
	Skipped  bool     `json:"skipped"`
	IsHook   bool     `json:"isHook"`
	Context  jsonText `json:"context"`
	Err      struct {
		Message jsonText `json:"message"`
		EStack  jsonText `json:"estack"`
		Diff    jsonText `json:"diff"`
	} `json:"err"`
}

type mochaParser struct {
	dl   *deadline
	lim  Limits
	col  *collector
	ms   int64
	stop bool
}

func parseMochawesome(dl *deadline, data []byte, lim Limits) (*Report, error) {
	p := &mochaParser{dl: dl, lim: lim, col: newCollector(FormatMochawesome, lim)}
	how := jsonOK
	jsonFields(data, lim.MaxDepth, func(k string, v []byte, end jsonEnd) bool {
		if k != "results" {
			return true
		}
		h, _ := jsonElemsPartial(v, lim.MaxDepth-1, func(s []byte) bool {
			return p.suite(s, nil, "", lim.MaxDepth-2)
		})
		how = h
		if how == jsonOK {
			how = end
		}
		return false
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
	r.DurationMs = p.ms
	return r, nil
}

func (p *mochaParser) suite(raw []byte, titles []string, file string, depth int) bool {
	var hdr struct {
		Title    jsonText `json:"title"`
		File     jsonText `json:"file"`
		FullFile jsonText `json:"fullFile"`
	}
	var tests, suites []byte
	jsonFields(raw, depth, func(k string, v []byte, end jsonEnd) bool {
		switch k {
		case "title":
			decodeInto(v, &hdr.Title)
		case "file":
			decodeInto(v, &hdr.File)
		case "fullFile":
			decodeInto(v, &hdr.FullFile)
		case "tests":
			tests = v
		case "suites":
			suites = v
		}
		return true
	})
	if file == "" {
		file = string(hdr.FullFile)
		if file == "" {
			file = string(hdr.File)
		}
	}
	if t := strings.TrimSpace(string(hdr.Title)); t != "" && len(titles) < 64 {
		titles = append(titles[:len(titles):len(titles)], t)
	}
	for i, list := range [][]byte{tests, suites} {
		if list == nil {
			continue
		}
		isTests := i == 0
		elems := jsonElemsPartial
		if isTests {
			elems = jsonElems
		}
		h, _ := elems(list, depth-1, func(e []byte) bool {
			if p.dl.tick() {
				p.stop = true
				return false
			}
			if isTests {
				p.test(e, titles, file)
				return true
			}
			return p.suite(e, titles, file, depth-2)
		})
		if h != jsonOK {
			p.col.jsonEndWarning(h, p.lim.MaxDepth)
			return false
		}
		if p.stop {
			return false
		}
	}
	return true
}

func mochaStatus(t *mochaTest) Status {
	switch string(t.State) {
	case "passed":
		return Passed
	case "failed":
		if t.IsHook {
			return Errored
		}
		return Failed
	case "pending", "skipped":
		return Skipped
	}
	switch {
	case t.Fail:
		return Failed
	case t.Pass:
		return Passed
	}
	return Skipped
}

func (p *mochaParser) test(raw []byte, titles []string, file string) {
	var t mochaTest
	if p.col.full() {
		var slim struct {
			State  jsonText `json:"state"`
			Pass   bool     `json:"pass"`
			Fail   bool     `json:"fail"`
			IsHook bool     `json:"isHook"`
		}
		if decodeInto(raw, &slim) {
			t.State, t.Pass, t.Fail, t.IsHook = slim.State, slim.Pass, slim.Fail, slim.IsHook
			p.col.add(mochaStatus(&t), false, nil)
		}
		return
	}
	if !decodeInto(raw, &t) {
		p.col.warnOnce("test", "some tests could not be read and were left out")
		return
	}
	st := mochaStatus(&t)
	ms := msToMs(float64(t.Duration))
	p.ms = addMs(p.ms, ms)
	p.col.add(st, false, func() Case {
		cs := Case{
			Suite:      cleanName(strings.Join(titles, " › "), p.lim.MaxNameBytes),
			Name:       cleanName(string(t.Title), p.lim.MaxNameBytes),
			File:       cleanName(file, maxFileBytes),
			DurationMs: ms,
		}
		if st == Failed || st == Errored {
			msg := string(t.Err.Message)
			cs.Message = cleanMessage(firstLine(cleanText(msg)), p.lim.MaxMessageBytes, false)
			d := string(t.Err.EStack)
			if t.Err.Diff != "" {
				d = joinOutput(d, string(t.Err.Diff))
			}
			cs.Details = cleanBlock(d, p.lim.MaxDetailsBytes, false)
		}
		if t.Context != "" {
			p.context(&cs, string(t.Context), 0)
		}
		return cs
	})
}

// mediaExtensions are the file kinds a context string is taken to attach.
var mediaExtensions = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".mp4", ".webm", ".avi", ".mov", ".zip", ".html", ".txt", ".log"}

func looksLikeMediaPath(s string) bool {
	if len(s) > 4*MaxPathBytes || strings.ContainsAny(s, "\n") || strings.Contains(s, "://") {
		return false
	}
	l := strings.ToLower(strings.TrimSpace(s))
	for _, e := range mediaExtensions {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	return false
}

// context reads a mochawesome context value: a JSON string holding JSON.
func (p *mochaParser) context(cs *Case, s string, level int) {
	b := []byte(s)
	if _, how := skipValue(b, 0, min(8, p.lim.MaxDepth)); how != jsonOK || !json.Valid(b) {
		// Not JSON (or too deep): plain text.
		if looksLikeMediaPath(s) {
			p.col.addAttachmentPath(cs, "", "", s)
		} else {
			p.col.addProperty(cs, "context", s)
		}
		return
	}
	p.contextValue(cs, b, level)
}

func (p *mochaParser) contextValue(cs *Case, b []byte, level int) {
	switch jsonKind(b) {
	case '"':
		var s string
		if decodeInto(b, &s) {
			if looksLikeMediaPath(s) {
				p.col.addAttachmentPath(cs, "", "", s)
			} else {
				p.col.addProperty(cs, "context", s)
			}
		}
	case '[':
		if level > 0 {
			return
		}
		n := 0
		jsonElems(b, 8, func(e []byte) bool {
			n++
			p.contextValue(cs, e, level+1)
			return n < 100
		})
	case '{':
		var o struct {
			Title jsonText        `json:"title"`
			Value json.RawMessage `json:"value"`
		}
		if !decodeInto(b, &o) {
			return
		}
		var s string
		if jsonKind(o.Value) == '"' && decodeInto(o.Value, &s) && looksLikeMediaPath(s) {
			p.col.addAttachmentPath(cs, string(o.Title), "", s)
			return
		}
		v := string(o.Value)
		if jsonKind(o.Value) == '"' {
			v = s
		}
		name := string(o.Title)
		if name == "" {
			name = "context"
		}
		p.col.addProperty(cs, name, cutHead(v, 4*MaxPropertyText, false))
	}
}
