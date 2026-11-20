package coverage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// parseIstanbul reads Istanbul's json-summary (coverage-summary.json, as
// Jest, Vitest and nyc write it). The format has only counters: each file
// entry's lines, branches and functions {total, covered} are taken as
// they are (clamped), "pct" is ignored, and the "total" entry is ignored
// too — Totals are added up from the files — unless the file lists no
// files at all.
func parseIstanbul(dl *deadline, data []byte, lim Limits) (*Report, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	j := &jsonWalker{d: d, dl: dl, max: lim.MaxDepth}
	c := newCollector(FormatIstanbul, lim)

	if tok, err := j.token(); err != nil {
		return nil, j.notReport(err)
	} else if tok != json.Delim('{') {
		return nil, fmt.Errorf("%w: the file is not a JSON object; %s", ErrNotReport, knownFormats)
	}
	var seen struct{ summary, final, meta, files, totals bool }
	for {
		tok, err := j.token()
		if err != nil {
			if seen.summary {
				j.partial(c, err)
				break
			}
			return nil, j.notReport(err)
		}
		if tok == json.Delim('}') {
			break
		}
		key, _ := tok.(string)
		switch key {
		case "meta":
			seen.meta = true
		case "files":
			seen.files = true
		case "totals":
			seen.totals = true
		}
		tok, err = j.token()
		if err != nil {
			if seen.summary {
				j.partial(c, err)
				break
			}
			return nil, j.notReport(err)
		}
		if tok != json.Delim('{') {
			if err := j.skip(tok); err != nil {
				if seen.summary {
					j.partial(c, err)
					break
				}
				return nil, j.notReport(err)
			}
			continue
		}
		e, kind, err := j.entry()
		if err != nil {
			if seen.summary {
				j.partial(c, err)
				break
			}
			return nil, j.notReport(err)
		}
		switch kind {
		case entrySummary:
			seen.summary = true
			if key == "total" {
				t := e
				c.fallback = &t
			} else {
				f := c.file(key, "")
				f.fb = f.fb.add(e)
			}
		case entryFinal:
			seen.final = true
		}
	}
	if !seen.summary {
		switch {
		case seen.final:
			return nil, fmt.Errorf("%w: the file is Istanbul's detailed coverage-final.json; write the json-summary report instead (coverage-summary.json)", ErrNotReport)
		case seen.meta && seen.files && seen.totals:
			return nil, fmt.Errorf("%w: coverage.py's JSON report is not supported; write its XML report instead (coverage xml, or pytest --cov-report=xml)", ErrNotReport)
		}
		return nil, fmt.Errorf("%w: the file is JSON, but not an Istanbul json-summary (no entry holds lines, statements, functions or branches counts); %s", ErrNotReport, knownFormats)
	}
	return c.finish(), nil
}

type entryKind int

const (
	entryOther entryKind = iota
	entrySummary
	entryFinal
)

var errTooDeep = errors.New("too deep")

// jsonWalker reads JSON tokens with a bound on nesting and the deadline.
type jsonWalker struct {
	d       *json.Decoder
	dl      *deadline
	max     int
	depth   int
	stopped bool
}

func (j *jsonWalker) token() (json.Token, error) {
	if j.dl.tick() {
		j.stopped = true
		return nil, errStopped
	}
	tok, err := j.d.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'), json.Delim('['):
		j.depth++
		if j.depth > j.max {
			return nil, errTooDeep
		}
	case json.Delim('}'), json.Delim(']'):
		j.depth--
	}
	return tok, nil
}

var errStopped = errors.New("stopped")

// skip skips the rest of a value whose first token is tok.
func (j *jsonWalker) skip(tok json.Token) error {
	if tok != json.Delim('{') && tok != json.Delim('[') {
		return nil
	}
	target := j.depth - 1
	for j.depth > target {
		if _, err := j.token(); err != nil {
			return err
		}
	}
	return nil
}

// entry reads one file entry, after its {.
func (j *jsonWalker) entry() (Totals, entryKind, error) {
	var t Totals
	kind := entryOther
	for {
		tok, err := j.token()
		if err != nil {
			return t, kind, err
		}
		if tok == json.Delim('}') {
			return t, kind, nil
		}
		key, _ := tok.(string)
		tok, err = j.token()
		if err != nil {
			return t, kind, err
		}
		var dst *Counter
		switch key {
		case "lines":
			dst = &t.Lines
		case "functions":
			dst = &t.Functions
		case "branches":
			dst = &t.Branches
		case "statements":
			// Counted for recognising the entry, not reported.
		case "statementMap", "fnMap", "branchMap":
			if kind == entryOther {
				kind = entryFinal
			}
		}
		if (dst != nil || key == "statements") && tok == json.Delim('{') {
			ctr, err := j.counter()
			if err != nil {
				return t, kind, err
			}
			kind = entrySummary
			if dst != nil {
				*dst = ctr
			}
			continue
		}
		if err := j.skip(tok); err != nil {
			return t, kind, err
		}
	}
}

// counter reads {"total":..,"covered":..,..}, after its {.
func (j *jsonWalker) counter() (Counter, error) {
	var c Counter
	for {
		tok, err := j.token()
		if err != nil {
			return c, err
		}
		if tok == json.Delim('}') {
			return c.norm(), nil
		}
		key, _ := tok.(string)
		tok, err = j.token()
		if err != nil {
			return c, err
		}
		var v int64
		switch x := tok.(type) {
		case json.Number:
			v = parseCount(string(x))
		case string:
			v = parseCount(x)
		default:
			if err := j.skip(tok); err != nil {
				return c, err
			}
			continue
		}
		switch key {
		case "total":
			c.Total = v
		case "covered":
			c.Covered = v
		}
	}
}

func (j *jsonWalker) what(err error) string {
	switch {
	case errors.Is(err, errTooDeep):
		return fmt.Sprintf("the JSON nests more than %d deep", j.max)
	case err == io.EOF, errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Sprintf("the file ends part-way through (at byte %d)", j.d.InputOffset())
	}
	return fmt.Sprintf("the JSON is broken at byte %d (%s)", j.d.InputOffset(), cutHead(cleanName(err.Error(), 200), 200, false))
}

func (j *jsonWalker) notReport(err error) error {
	if j.stopped {
		return j.dl.stopError()
	}
	if errors.Is(err, errTooDeep) {
		return fmt.Errorf("%w: %s", ErrNotReport, j.what(err))
	}
	return fmt.Errorf("%w: the JSON could not be read (%s)", ErrNotReport, j.what(err))
}

func (j *jsonWalker) partial(c *collector, err error) {
	if j.stopped {
		c.warn("parsing %s; coverage after byte %d is missing", j.dl.what(), j.d.InputOffset())
		return
	}
	c.warn("%s; coverage after that is missing", j.what(err))
}
