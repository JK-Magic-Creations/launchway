package debrief

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Case is one test result, as the merged JUnit export lists it.
type Case struct {
	Suite      string
	Name       string
	File       string
	Variant    string
	Status     string
	DurationMs int64
	Message    string
	Details    string
	Output     string
	Flaky      bool
	// Quarantined: the failure is excused by the quarantine file; the case
	// is written as skipped with that reason, since another tool should not
	// fail a run Launchway did not.
	Quarantined bool
}

// illegalXML removes what XML 1.0 cannot carry.
func illegalXML(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r', r >= 0x20 && r <= 0xD7FF, r >= 0xE000 && r <= 0xFFFD, r >= 0x10000 && r <= 0x10FFFF:
			return r
		}
		return -1
	}, s)
}

// JUnit writes the cases of a run as one JUnit XML document with a suite per
// suite name, which other tools that read JUnit can open. Flaky tests carry
// a flakyFailure element beside their pass, and quarantined failures are
// skipped with the reason.
func JUnit(w io.Writer, r *Report, cases []Case) error {
	sort.SliceStable(cases, func(i, j int) bool { return cases[i].Suite < cases[j].Suite })
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	s := r.Summary
	root := xml.StartElement{Name: xml.Name{Local: "testsuites"}, Attr: []xml.Attr{
		{Name: xml.Name{Local: "name"}, Value: fmt.Sprintf("%s run %d", r.Project, r.Run.Number)},
		{Name: xml.Name{Local: "tests"}, Value: fmt.Sprint(s.Total)},
		{Name: xml.Name{Local: "failures"}, Value: fmt.Sprint(s.Failed + s.Errored)},
		{Name: xml.Name{Local: "skipped"}, Value: fmt.Sprint(s.Skipped)},
		{Name: xml.Name{Local: "time"}, Value: fmt.Sprintf("%.3f", float64(s.DurationMs)/1000)},
	}}
	if err := enc.EncodeToken(root); err != nil {
		return err
	}
	for i := 0; i < len(cases); {
		j := i
		for j < len(cases) && cases[j].Suite == cases[i].Suite {
			j++
		}
		if err := junitSuite(enc, r, cases[i:j]); err != nil {
			return err
		}
		i = j
	}
	if err := enc.EncodeToken(root.End()); err != nil {
		return err
	}
	return enc.Flush()
}

func junitSuite(enc *xml.Encoder, r *Report, cs []Case) error {
	var fails, skips int
	var ms int64
	for _, c := range cs {
		ms += c.DurationMs
		switch {
		case c.Quarantined || c.Status == Skipped:
			skips++
		case c.Status == Failed || c.Status == Errored:
			fails++
		}
	}
	name := cs[0].Suite
	if name == "" {
		name = "(no suite)"
	}
	start := xml.StartElement{Name: xml.Name{Local: "testsuite"}, Attr: []xml.Attr{
		{Name: xml.Name{Local: "name"}, Value: illegalXML(name)},
		{Name: xml.Name{Local: "tests"}, Value: fmt.Sprint(len(cs))},
		{Name: xml.Name{Local: "failures"}, Value: fmt.Sprint(fails)},
		{Name: xml.Name{Local: "skipped"}, Value: fmt.Sprint(skips)},
		{Name: xml.Name{Local: "time"}, Value: fmt.Sprintf("%.3f", float64(ms)/1000)},
		{Name: xml.Name{Local: "hostname"}, Value: ""},
	}}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	for _, c := range cs {
		tc := xml.StartElement{Name: xml.Name{Local: "testcase"}, Attr: []xml.Attr{
			{Name: xml.Name{Local: "classname"}, Value: illegalXML(name)},
			{Name: xml.Name{Local: "name"}, Value: illegalXML(c.Name)},
			{Name: xml.Name{Local: "time"}, Value: fmt.Sprintf("%.3f", float64(c.DurationMs)/1000)},
		}}
		if c.File != "" {
			tc.Attr = append(tc.Attr, xml.Attr{Name: xml.Name{Local: "file"}, Value: illegalXML(c.File)})
		}
		if err := enc.EncodeToken(tc); err != nil {
			return err
		}
		child := func(el string, msg, body string) error {
			se := xml.StartElement{Name: xml.Name{Local: el}}
			if msg != "" {
				se.Attr = []xml.Attr{{Name: xml.Name{Local: "message"}, Value: illegalXML(firstLine(msg))}}
			}
			if err := enc.EncodeToken(se); err != nil {
				return err
			}
			if body != "" {
				if err := enc.EncodeToken(xml.CharData(illegalXML(body))); err != nil {
					return err
				}
			}
			return enc.EncodeToken(se.End())
		}
		var err error
		switch {
		case c.Quarantined:
			err = child("skipped", "quarantined: "+firstLine(c.Message), "")
		case c.Status == Failed:
			err = child("failure", c.Message, nonEmpty(c.Details, c.Message))
		case c.Status == Errored:
			err = child("error", c.Message, nonEmpty(c.Details, c.Message))
		case c.Status == Skipped:
			err = child("skipped", c.Message, "")
		case c.Flaky:
			err = child("flakyFailure", c.Message, c.Details)
		}
		if err != nil {
			return err
		}
		if c.Output != "" {
			if err := child("system-out", "", c.Output); err != nil {
				return err
			}
		}
		if err := enc.EncodeToken(tc.End()); err != nil {
			return err
		}
	}
	return enc.EncodeToken(start.End())
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
