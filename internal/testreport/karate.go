package testreport

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"strings"
)

// Karate's own JSON: Karate 1.x writes one *.karate-json.txt per feature
// (an object with scenarioResults); Karate 2.x writes karate-events.jsonl,
// a stream of events whose FEATURE_EXIT events each carry the same kind of
// feature result. Both are read the same way: a case per scenario (Suite =
// the feature's name, File = its relative path), its steps with their logs
// (HTTP calls become Step.HTTP), embeds as attachments, and features the
// scenario called as nested steps. Outline examples get " (line N)" added
// to their names, as in Cucumber JSON.

type karateStepResult struct {
	Step struct {
		Background bool     `json:"background"`
		Prefix     jsonText `json:"prefix"`
		Keyword    jsonText `json:"keyword"` // Karate 2 splits the keyword from the text
		Text       jsonText `json:"text"`
		DocString  jsonText `json:"docString"`
	} `json:"step"`
	Result struct {
		Status       jsonText `json:"status"`
		Millis       jsonNum  `json:"millis"`
		Nanos        jsonNum  `json:"nanos"`
		ErrorMessage jsonText `json:"errorMessage"`
	} `json:"result"`
	StepLog     jsonText        `json:"stepLog"`
	Embeds      []karateEmbed   `json:"embeds"`
	CallResults []karateFeature `json:"callResults"`
	Hidden      bool            `json:"hidden"`
}

type karateEmbed struct {
	File     jsonText `json:"file"`
	Data     jsonText `json:"data"`
	MimeType jsonText `json:"mime_type"`
	Type     jsonText `json:"resourceType"`
	Name     jsonText `json:"name"`
}

type karateScenario struct {
	Name           jsonText           `json:"name"`
	Line           jsonNum            `json:"line"`
	ExampleIndex   *jsonNum           `json:"exampleIndex"`
	Tags           jsonStrings        `json:"tags"`
	Failed         bool               `json:"failed"`
	Skipped        bool               `json:"skipped"`
	Error          jsonText           `json:"error"`
	DurationMillis jsonNum            `json:"durationMillis"`
	StepResults    []karateStepResult `json:"stepResults"`
}

type karateScenarioSlim struct {
	Failed      bool `json:"failed"`
	Skipped     bool `json:"skipped"`
	StepResults []struct {
		Result struct {
			Status jsonText `json:"status"`
		} `json:"result"`
	} `json:"stepResults"`
}

type karateFeature struct {
	Name            jsonText         `json:"name"`
	FeatureName     jsonText         `json:"featureName"`
	RelativePath    jsonText         `json:"relativePath"`
	ScenarioResults []karateScenario `json:"scenarioResults"`
}

type karateParser struct {
	dl  *deadline
	lim Limits
	col *collector
	ms  int64
}

func parseKarate(dl *deadline, data []byte, lim Limits) (*Report, error) {
	p := &karateParser{dl: dl, lim: lim, col: newCollector(FormatKarate, lim)}
	how := p.feature(data, lim.MaxDepth)
	return p.finish(how)
}

func (p *karateParser) finish(how jsonEnd) (*Report, error) {
	if p.dl.expired() {
		if p.col.rep.Counts.Total == 0 {
			return nil, p.dl.stopError()
		}
		p.col.warn("parsing %s; results after that are missing", p.dl.what())
	} else if how != jsonOK {
		if p.col.rep.Counts.Total == 0 && how == jsonDeep {
			return nil, jsonEndError(how, p.lim.MaxDepth)
		}
		p.col.jsonEndWarning(how, p.lim.MaxDepth)
	}
	r := p.col.finish()
	r.DurationMs = p.ms
	return r, nil
}

// isKarateEvents says whether a JSON line is a Karate 2 event.
func isKarateEvents(first []byte, depth int) bool {
	m := jsonObject(first, depth, false)
	t, ok := m["type"]
	if _, hasData := m["data"]; !ok || !hasData {
		return false
	}
	return bytes.HasPrefix(t, []byte(`"SUITE_`)) || bytes.HasPrefix(t, []byte(`"FEATURE_`)) || bytes.HasPrefix(t, []byte(`"SCENARIO_`))
}

// parseKarateEvents reads karate-events.jsonl: each FEATURE_EXIT event's
// data is a feature result.
func parseKarateEvents(dl *deadline, data []byte, lim Limits) (*Report, error) {
	p := &karateParser{dl: dl, lim: lim, col: newCollector(FormatKarate, lim)}
	how := jsonOK
	for pos := 0; pos < len(data) && !dl.expired(); {
		end, h := skipValue(data, pos, lim.MaxDepth)
		if h != jsonOK {
			if skipJSONSpace(data, pos) < len(data) {
				how = h
			}
			break
		}
		var exit []byte
		jsonFields(data[pos:end], lim.MaxDepth, func(k string, v []byte, e jsonEnd) bool {
			if k == "type" && string(v) != `"FEATURE_EXIT"` {
				return false
			}
			if k == "data" && e == jsonOK {
				exit = v
			}
			return true
		})
		if exit != nil {
			if h := p.feature(exit, lim.MaxDepth-1); h != jsonOK {
				how = h
				break
			}
		}
		pos = end
	}
	return p.finish(how)
}

// feature reads one feature result.
func (p *karateParser) feature(raw []byte, depth int) jsonEnd {
	var f karateFeature
	var scenarios []byte
	how, _ := jsonFields(raw, depth, func(k string, v []byte, end jsonEnd) bool {
		switch k {
		case "name":
			decodeInto(v, &f.Name)
		case "featureName":
			decodeInto(v, &f.FeatureName)
		case "relativePath":
			decodeInto(v, &f.RelativePath)
		case "scenarioResults":
			scenarios = v
		}
		return true
	})
	if scenarios == nil {
		return how
	}
	suite := string(f.Name)
	if suite == "" {
		suite = string(f.FeatureName)
	}
	if suite == "" {
		suite = string(f.RelativePath)
	}
	seen := map[uint64]bool{}
	h2, _ := jsonElems(scenarios, depth-1, func(e []byte) bool {
		if p.dl.tick() {
			return false
		}
		if p.col.full() {
			var s karateScenarioSlim
			if decodeInto(e, &s) {
				rank := 0
				for _, st := range s.StepResults {
					rank = max(rank, cukeRank(string(st.Result.Status)))
				}
				if s.Failed {
					rank = 4
				}
				p.col.add(cukeStatus(rank), false, nil)
			}
			return true
		}
		var s karateScenario
		if !decodeInto(e, &s) {
			p.col.warnOnce("scenario", "some scenarios could not be read and were left out")
			return true
		}
		p.scenario(suite, string(f.RelativePath), &s, seen)
		return true
	})
	if how == jsonOK {
		how = h2
	}
	return how
}

func (p *karateParser) scenario(suite, file string, s *karateScenario, seen map[uint64]bool) {
	rank := 0
	var worst *karateStepResult
	for i := range s.StepResults {
		r := cukeRank(string(s.StepResults[i].Result.Status))
		if r > rank {
			rank, worst = r, &s.StepResults[i]
		}
	}
	if s.Failed && rank < 4 {
		rank = 4
	}
	if s.Skipped && rank == 0 {
		rank = 1
	}
	st := cukeStatus(rank)
	ms := msToMs(float64(s.DurationMillis))
	p.ms = addMs(p.ms, ms)

	name := cleanName(string(s.Name), p.lim.MaxNameBytes)
	h := fnv.New64a()
	h.Write([]byte(name))
	key := h.Sum64()
	if (s.ExampleIndex != nil && *s.ExampleIndex >= 0) || seen[key] {
		if line := int64(s.Line); line > 0 {
			name = fmt.Sprintf("%s (line %d)", name, line)
		}
	}
	if len(seen) < 100000 {
		seen[key] = true
	}
	p.col.add(st, false, func() Case {
		cs := Case{
			Suite:      cleanName(suite, p.lim.MaxNameBytes),
			Name:       cutHead(name, p.lim.MaxNameBytes, false),
			File:       cleanName(file, maxFileBytes),
			DurationMs: ms,
		}
		p.col.addTags(&cs, s.Tags...)
		msg := string(s.Error)
		if msg == "" && worst != nil {
			msg = string(worst.Result.ErrorMessage)
			if rank == 3 || rank == 2 {
				msg = "step is " + strings.ToLower(string(worst.Result.Status)) + ": " + karateStepName(worst)
			}
		}
		if msg != "" {
			cs.Message = cleanMessage(firstLine(cleanText(msg)), p.lim.MaxMessageBytes, false)
			if strings.Contains(strings.TrimSpace(msg), "\n") {
				cs.Details = cleanBlock(msg, p.lim.MaxDetailsBytes, false)
			}
		}
		for i := range s.StepResults {
			p.step(&cs, &s.StepResults[i], 0)
		}
		return cs
	})
}

func karateStepName(r *karateStepResult) string {
	if r.Step.Keyword != "" {
		return string(r.Step.Keyword) + " " + string(r.Step.Text)
	}
	return string(r.Step.Text)
}

func (p *karateParser) step(cs *Case, r *karateStepResult, depth int) {
	if r.Hidden {
		return
	}
	var attach []int
	for _, e := range r.Embeds {
		ct := string(e.MimeType)
		var i int
		switch {
		case e.Data != "":
			kind := inlineBase64
			if strings.HasPrefix(strings.ToLower(ct), "text/") {
				kind = inlineMaybe
			}
			i = p.col.addAttachmentData(cs, string(e.Name), ct, string(e.Data), kind)
		case e.File != "":
			i = p.col.addAttachmentPath(cs, string(e.Name), ct, string(e.File))
		default:
			continue
		}
		if i >= 0 {
			attach = append(attach, i)
		}
	}
	text := string(r.StepLog)
	if text == "" {
		text = string(r.Step.DocString)
	}
	var ex *HTTPExchange
	if looksLikeHTTPLog(text) {
		ex = parseHTTPLog(text, p.lim)
		text = hideHTTPLogCredentials(text)
	}
	rank := cukeRank(string(r.Result.Status))
	errText := string(r.Result.ErrorMessage)
	if rank == 3 && errText == "" {
		errText = "step is " + strings.ToLower(string(r.Result.Status))
	}
	ms := nsToMs(float64(r.Result.Nanos))
	if ms == 0 {
		ms = msToMs(float64(r.Result.Millis))
	}
	if !p.col.addStep(cs, stepIn{
		depth:   depth,
		keyword: string(r.Step.Prefix),
		name:    karateStepName(r),
		status:  cukeStatus(rank),
		ms:      ms,
		err:     errText,
		text:    text,
		http:    ex,
		attach:  attach,
	}) {
		return
	}
	for _, f := range r.CallResults {
		for i := range f.ScenarioResults {
			for j := range f.ScenarioResults[i].StepResults {
				if len(cs.Steps) >= p.lim.MaxSteps {
					p.col.warnOnce("steps", "some cases have more than %d steps; the rest were left out", p.lim.MaxSteps)
					return
				}
				p.step(cs, &f.ScenarioResults[i].StepResults[j], depth+1)
			}
		}
	}
}
