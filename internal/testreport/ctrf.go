package testreport

import (
	"encoding/json"
	"sort"
	"strings"
)

// CTRF (Common Test Report Format): results.tests is a flat list of tests.
// Name, suite (a string, or a list joined with " › "), filePath, duration
// (ms), message and trace, retries and retryAttempts, flaky, tags, steps,
// attachments (paths), screenshot (inline base64), stdout and stderr map
// directly; browser becomes the Variant (its first word, so a browser
// upgrade does not start a new history) and a property with the full
// value, and parameters and extra become properties. Status "pending" is
// Skipped; "other" (todo and the like) is Errored when it carries a
// message, else Skipped. The report-level environment is not kept (the
// model has no report properties).

type ctrfAttempt struct {
	Status   jsonText `json:"status"`
	Duration jsonNum  `json:"duration"`
	Message  jsonText `json:"message"`
}

type ctrfTest struct {
	Name          jsonText        `json:"name"`
	Status        jsonText        `json:"status"`
	Duration      jsonNum         `json:"duration"`
	Suite         jsonStrings     `json:"suite"`
	Message       jsonText        `json:"message"`
	Trace         jsonText        `json:"trace"`
	FilePath      jsonText        `json:"filePath"`
	Retries       jsonNum         `json:"retries"`
	RetryAttempts []ctrfAttempt   `json:"retryAttempts"`
	Flaky         bool            `json:"flaky"`
	Tags          jsonStrings     `json:"tags"`
	Browser       jsonText        `json:"browser"`
	Device        jsonText        `json:"device"`
	Screenshot    jsonText        `json:"screenshot"`
	Stdout        jsonStrings     `json:"stdout"`
	Stderr        jsonStrings     `json:"stderr"`
	Parameters    json.RawMessage `json:"parameters"`
	Extra         json.RawMessage `json:"extra"`
	Attachments   []struct {
		Name        jsonText `json:"name"`
		ContentType jsonText `json:"contentType"`
		Path        jsonText `json:"path"`
	} `json:"attachments"`
	Steps []struct {
		Name   jsonText `json:"name"`
		Status jsonText `json:"status"`
	} `json:"steps"`
}

func ctrfStatus(status string, hasMsg bool) Status {
	switch strings.ToLower(status) {
	case "passed":
		return Passed
	case "failed":
		return Failed
	case "skipped", "pending":
		return Skipped
	}
	if hasMsg {
		return Errored
	}
	return Skipped
}

func parseCTRF(dl *deadline, data []byte, lim Limits) (*Report, error) {
	col := newCollector(FormatCTRF, lim)
	var ms int64
	how := jsonOK
	stop := false
	jsonFields(data, lim.MaxDepth, func(k string, v []byte, end jsonEnd) bool {
		if k != "results" {
			return true
		}
		jsonFields(v, lim.MaxDepth-1, func(k string, v []byte, end jsonEnd) bool {
			if k != "tests" {
				return true
			}
			how, _ = jsonElems(v, lim.MaxDepth-2, func(e []byte) bool {
				if dl.tick() {
					stop = true
					return false
				}
				var t ctrfTest
				if col.full() {
					var slim struct {
						Status  jsonText `json:"status"`
						Message jsonText `json:"message"`
						Flaky   bool     `json:"flaky"`
					}
					if decodeInto(e, &slim) {
						st := ctrfStatus(string(slim.Status), slim.Message != "")
						col.add(st, slim.Flaky && st == Passed, nil)
					}
					return true
				}
				if !decodeInto(e, &t) {
					col.warnOnce("test", "some tests could not be read and were left out")
					return true
				}
				ms = addMs(ms, ctrfCase(col, &t))
				return true
			})
			return false
		})
		return false
	})
	if stop {
		if col.rep.Counts.Total == 0 {
			return nil, dl.stopError()
		}
		col.warn("parsing %s; results after that are missing", dl.what())
	} else if how != jsonOK {
		if col.rep.Counts.Total == 0 && how == jsonDeep {
			return nil, jsonEndError(how, lim.MaxDepth)
		}
		col.jsonEndWarning(how, lim.MaxDepth)
	}
	r := col.finish()
	r.DurationMs = ms
	return r, nil
}

func ctrfCase(col *collector, t *ctrfTest) int64 {
	lim := col.lim
	st := ctrfStatus(string(t.Status), t.Message != "" || t.Trace != "")
	flaky := t.Flaky && st == Passed
	ms := msToMs(float64(t.Duration))
	col.add(st, flaky, func() Case {
		cs := Case{
			Suite:      cleanName(strings.Join(t.Suite, " › "), lim.MaxNameBytes),
			Name:       cleanName(string(t.Name), lim.MaxNameBytes),
			File:       cleanName(string(t.FilePath), maxFileBytes),
			DurationMs: ms,
		}
		if st != Passed {
			cs.Message = cleanMessage(firstLine(cleanText(string(t.Message))), lim.MaxMessageBytes, false)
			d := string(t.Message)
			if t.Trace != "" {
				d = joinOutput(d, string(t.Trace))
			}
			if strings.TrimSpace(cleanText(d)) != cs.Message {
				cs.Details = cleanBlock(d, lim.MaxDetailsBytes, false)
			}
		}
		if st == Failed || st == Errored {
			out := strings.Join(t.Stdout, "")
			cs.Output = cleanTail(joinOutput(cutTail(out, 4*lim.MaxOutputBytes, false), cutTail(strings.Join(t.Stderr, ""), 4*lim.MaxOutputBytes, false)), lim.MaxOutputBytes, false)
		}
		col.addTags(&cs, t.Tags...)
		if b := strings.TrimSpace(string(t.Browser)); b != "" {
			setVariant(&cs, strings.Fields(b)[0])
			col.addProperty(&cs, "browser", b)
		}
		if t.Device != "" {
			col.addProperty(&cs, "device", string(t.Device))
		}
		retries := int(t.Retries)
		if n := len(t.RetryAttempts); n > retries {
			retries = n
		}
		setRetries(&cs, retries)
		for _, a := range t.RetryAttempts {
			col.addAttempt(&cs, ctrfStatus(string(a.Status), a.Message != ""), msToMs(float64(a.Duration)), string(a.Message))
		}
		for _, s := range t.Steps {
			col.addStep(&cs, stepIn{name: string(s.Name), status: ctrfStatus(string(s.Status), false)})
		}
		for _, a := range t.Attachments {
			col.addAttachmentPath(&cs, string(a.Name), string(a.ContentType), string(a.Path))
		}
		if t.Screenshot != "" {
			col.addAttachmentData(&cs, "screenshot", "image/png", string(t.Screenshot), inlineBase64)
		}
		addJSONProperties(col, &cs, t.Parameters, "")
		addJSONProperties(col, &cs, t.Extra, "")
		return cs
	})
	return ms
}

// addJSONProperties adds the members of a JSON object as properties, in
// name order, values that are not strings written as JSON.
func addJSONProperties(col *collector, cs *Case, raw json.RawMessage, prefix string) {
	if jsonKind(raw) != '{' {
		return
	}
	m := jsonObject(raw, 4, false)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		var s string
		if jsonKind(v) != '"' || !decodeInto(v, &s) {
			s = string(v[:min(len(v), 4*MaxPropertyText)])
		}
		col.addProperty(cs, prefix+k, s)
	}
}
