package importer

import (
	"fmt"
	"strings"
)

// Construct is one entry of a reader's classification table. The same table
// drives the findings a reader reports and the page "Move your pipelines over",
// so the page cannot drift from what the reader does.
type Construct struct {
	Code  string // unique within the table
	Name  string // the construct as the source writes it
	Class Class
	Note  string // plain words; %s stands for a detail filled in at the time
	Next  string // what to do now; "" when nothing is needed
}

// Table is a reader's constructs.
type Table []Construct

func (t Table) find(code string) *Construct {
	for i := range t {
		if t[i].Code == code {
			return &t[i]
		}
	}
	return nil
}

// reporter collects findings for one file against a table.
type reporter struct {
	table Table
	file  string
	out   *[]Finding
}

// note reports the construct with the given code at a line; args fill the
// %s places of the table's note.
func (r reporter) note(code string, line int, args ...any) *Finding {
	c := r.table.find(code)
	if c == nil {
		panic("importer: construct not in the table: " + code) // a programming error, caught by the tests
	}
	f := Finding{Code: code, Class: c.Class, Message: fmt.Sprintf(c.Note, args...), Next: c.Next, File: r.file, Line: line}
	*r.out = append(*r.out, f)
	return &(*r.out)[len(*r.out)-1]
}

// Plain is the note as the guide shows it: details in the place of %s are
// written as an ellipsis.
func (c Construct) Plain() string {
	return strings.ReplaceAll(c.Note, "%s", "…")
}

// CheckTable reports problems with a table: duplicate codes, empty words, a
// manual construct with no next step.
func CheckTable(t Table) []string {
	var bad []string
	seen := map[string]bool{}
	for _, c := range t {
		switch {
		case seen[c.Code]:
			bad = append(bad, "duplicate code "+c.Code)
		case c.Name == "" || c.Note == "":
			bad = append(bad, c.Code+": name and note are required")
		case c.Class == Manual && c.Next == "":
			bad = append(bad, c.Code+": a construct that is not converted must say what to do")
		}
		seen[c.Code] = true
	}
	return bad
}
