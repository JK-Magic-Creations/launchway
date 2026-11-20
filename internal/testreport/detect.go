package testreport

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The format is always told from the content, never from the file name:
//
//   - XML by its root element: <testsuites> or <testsuite> (JUnit),
//     <testng-results> (TestNG), <test-run> (NUnit 3), <assemblies> or
//     <assembly> (xUnit.net v2), <TestRun> (TRX), <robot> (Robot Framework)
//   - JSON by its shape: an array of features with elements (Cucumber),
//     an object with config and suites (Playwright), with results.tool and
//     results.tests (CTRF), with stats and results (mochawesome), with
//     featureName or scenarioResults (Karate), and lines of {"Action": ...}
//     (go test -json)
//   - text whose first line is "TAP version", a plan (1..N) or a test line
//     (ok / not ok) is TAP

const knownRoots = "<testsuites>, <testsuite>, <testng-results>, <test-run>, <assemblies>, <TestRun>, <robot>"

const knownJSON = "Cucumber (an array of features), Karate (scenarioResults), Playwright (config and suites), CTRF (results.tool and results.tests), mochawesome (stats and results), go test -json"

// Parse detects the format from the content (never the file name) and
// parses it.
func Parse(ctx context.Context, data []byte, lim Limits) (*Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lim = lim.withDefaults()
	dl := newDeadline(ctx, lim.Timeout)
	if dl.expired() {
		return nil, dl.stopError()
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("%w: the file is empty", ErrNotReport)
	}
	body := bytes.TrimPrefix(data, utf8BOM)
	i := 0
	for i < len(body) && isSpace(body[i]) {
		i++
	}
	if i == len(body) {
		return nil, fmt.Errorf("%w: the file holds only white space", ErrNotReport)
	}
	body = body[i:]
	switch body[0] {
	case '<':
		return parseXML(dl, body, lim)
	case '{', '[':
		return parseJSON(dl, body, lim)
	}
	if looksLikeTAP(body) {
		return parseTAP(dl, body, lim)
	}
	// go test -json output often starts with a few lines from standard
	// error ("go: downloading ..."), so look for an event line near the
	// start before giving up.
	if jsonLineNearStart(body) {
		r, err := parseGoTest(dl, body, lim)
		if !errors.Is(err, ErrNotReport) {
			return r, err
		}
	}
	return nil, fmt.Errorf("%w: the content is not XML, JSON or TAP (it starts with %s)", ErrNotReport, snippet(body))
}

// jsonLineNearStart says whether a line in the first 64 KiB starts with
// {" the way every go test -json event does.
func jsonLineNearStart(b []byte) bool {
	b = b[:min(len(b), 64<<10)]
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return false
		}
		b = b[i+1:]
		if bytes.HasPrefix(bytes.TrimLeft(b, " \t\r"), []byte(`{"`)) {
			return true
		}
	}
}

// parseXML reads up to the root element and hands the rest to the reader
// for that root.
func parseXML(dl *deadline, data []byte, lim Limits) (*Report, error) {
	kind, err := declaredEncoding(data)
	if err != nil {
		return nil, err
	}
	clean := prepareXML(dl, data, kind == encLatin1)
	if clean == nil {
		return nil, dl.stopError()
	}
	d := xml.NewDecoder(bytes.NewReader(clean))
	d.Strict = true
	d.CharsetReader = charsetReader
	var root xml.StartElement
	for {
		if dl.tick() {
			return nil, dl.stopError()
		}
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("%w: the XML has no root element", ErrNotReport)
			}
			line, msg := xmlErrorText(d, err)
			return nil, fmt.Errorf("%w: the XML could not be read (line %d: %s)", ErrNotReport, line, msg)
		}
		if t, ok := tok.(xml.StartElement); ok {
			root = t.Copy()
			break
		}
	}
	switch name := root.Name.Local; name {
	case "testsuites", "testsuite":
		return parseJUnit(dl, d, root, lim)
	case "testng-results":
		return parseTestNG(dl, d, root, lim)
	case "test-run":
		return parseNUnit(dl, d, root, lim)
	case "assemblies", "assembly":
		return parseXUnit(dl, d, root, lim)
	case "TestRun":
		if ns := root.Name.Space; ns == "" || strings.Contains(ns, "TeamTest") {
			return parseTRX(dl, d, root, lim)
		}
	case "robot":
		return parseRobot(dl, d, root, lim)
	}
	return nil, fmt.Errorf("%w: the XML's root element is <%s>; known roots are %s", ErrNotReport, cleanName(root.Name.Local, 60), knownRoots)
}

// parseJSON tells the JSON formats apart by their shape.
func parseJSON(dl *deadline, data []byte, lim Limits) (*Report, error) {
	end, how := skipValue(data, 0, lim.MaxDepth)
	rest := bytes.TrimSpace(data[min(end, len(data)):])
	if data[0] == '{' && how == jsonOK && len(rest) > 0 {
		// More than one value: lines of JSON, as go test -json and Karate
		// 2 (karate-events.jsonl) write.
		if isKarateEvents(data[:end], lim.MaxDepth) {
			return parseKarateEvents(dl, data, lim)
		}
		return parseGoTest(dl, data, lim)
	}
	if how == jsonBad {
		return nil, fmt.Errorf("%w: the JSON could not be read (it starts with %s)", ErrNotReport, snippet(data))
	}
	if how == jsonOK && len(rest) > 0 {
		return nil, fmt.Errorf("%w: the file holds more than one JSON value", ErrNotReport)
	}
	if data[0] == '[' {
		return parseJSONArray(dl, data, lim, how)
	}
	keys := map[string][]byte{}
	var order []string
	topHow, _ := jsonFields(data, lim.MaxDepth, func(k string, v []byte, end jsonEnd) bool {
		if _, ok := keys[k]; !ok {
			keys[k] = v
			order = append(order, k)
		}
		return len(order) < 1000
	})
	has := func(k string) bool { _, ok := keys[k]; return ok }
	switch {
	case has("Action"):
		return parseGoTest(dl, data, lim)
	case has("config") && has("suites"):
		return parsePlaywright(dl, data, lim)
	case has("scenarioResults") || (has("featureName") && has("scenarioResults")):
		return parseKarate(dl, data, lim)
	case has("results"):
		res := keys["results"]
		switch jsonKind(res) {
		case '{':
			inner := jsonObject(res, lim.MaxDepth-1, true)
			if _, ok := inner["tool"]; ok {
				if _, ok := inner["tests"]; ok {
					return parseCTRF(dl, data, lim)
				}
			}
		case '[':
			if has("stats") {
				return parseMochawesome(dl, data, lim)
			}
		}
	}
	if topHow == jsonBad {
		return nil, fmt.Errorf("%w: the JSON could not be read (it starts with %s)", ErrNotReport, snippet(data))
	}
	if topHow == jsonDeep {
		return nil, jsonEndError(jsonDeep, lim.MaxDepth)
	}
	return nil, fmt.Errorf("%w: the JSON object has %s; known JSON reports are %s", ErrNotReport, describeKeys(order), knownJSON)
}

// describeKeys names the first few keys of an object for an error.
func describeKeys(keys []string) string {
	if len(keys) == 0 {
		return "no keys"
	}
	var sb strings.Builder
	sb.WriteString("keys ")
	for i, k := range keys {
		if i == 3 {
			sb.WriteString(", …")
			break
		}
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(strconv.Quote(cleanName(k, 24)))
	}
	return sb.String()
}

// parseJSONArray handles a JSON document that is an array: Cucumber.
func parseJSONArray(dl *deadline, data []byte, lim Limits, how jsonEnd) (*Report, error) {
	var first []byte
	firstHow, _ := jsonElemsPartial(data, lim.MaxDepth, func(e []byte) bool {
		first = e
		return false
	})
	if first == nil {
		switch {
		case firstHow == jsonOK:
			// [] is what Cucumber writes when there were no features.
			return parseCucumber(dl, data, lim)
		case firstHow == jsonDeep:
			return nil, jsonEndError(jsonDeep, lim.MaxDepth)
		}
		return nil, fmt.Errorf("%w: the JSON array ends before its first element", ErrNotReport)
	}
	if jsonKind(first) == '{' {
		m := jsonObject(first, lim.MaxDepth-1, true)
		_, hasElements := m["elements"]
		_, hasURI := m["uri"]
		_, hasKeyword := m["keyword"]
		if hasElements || (hasURI && hasKeyword) {
			return parseCucumber(dl, data, lim)
		}
		var keys []string
		jsonFields(first, lim.MaxDepth-1, func(k string, _ []byte, _ jsonEnd) bool {
			keys = append(keys, k)
			return len(keys) < 4
		})
		return nil, fmt.Errorf("%w: the JSON is an array of objects with %s, not Cucumber features; known JSON reports are %s", ErrNotReport, describeKeys(keys), knownJSON)
	}
	return nil, fmt.Errorf("%w: the JSON is an array, but not of Cucumber features; known JSON reports are %s", ErrNotReport, knownJSON)
}
