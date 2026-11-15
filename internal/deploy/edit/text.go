package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

var bom = []byte("\xEF\xBB\xBF")

// readDocs parses every document in a file.
func readDocs(data []byte) (docs []*yaml.Node, err error) {
	if bytes.HasPrefix(data, []byte{0xFE, 0xFF}) || bytes.HasPrefix(data, []byte{0xFF, 0xFE}) {
		return nil, errors.New("the file is UTF-16 text; Launchway edits UTF-8 files only")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("the file is not UTF-8 text; Launchway edits UTF-8 files only")
	}
	defer func() {
		if r := recover(); r != nil {
			docs, err = nil, fmt.Errorf("the file could not be read as YAML: %v", r)
		}
	}()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("the file is not valid YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
		}
		docs = append(docs, &n)
	}
}

// text is a file's bytes with the offset where each line starts, counted
// the way the YAML parser counts them.
type text struct {
	data   []byte
	starts []int // starts[i] is the byte offset of line i+1
}

func newText(data []byte) *text {
	t := &text{data: data}
	i := 0
	if bytes.HasPrefix(data, bom) {
		i = len(bom) // the parser skips a byte order mark without counting it
	}
	t.starts = append(t.starts, i)
	for i < len(data) {
		if w := breakAt(data, i); w > 0 {
			i += w
			t.starts = append(t.starts, i)
			continue
		}
		i++
	}
	return t
}

// breakAt returns the length of the line break at data[i], or 0 if there
// is none. YAML counts \r\n, \r, \n, and the Unicode NEL, LS and PS.
func breakAt(data []byte, i int) int {
	switch {
	case data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n':
		return 2
	case data[i] == '\r' || data[i] == '\n':
		return 1
	case data[i] == 0xC2 && i+1 < len(data) && data[i+1] == 0x85:
		return 2
	case data[i] == 0xE2 && i+2 < len(data) && data[i+1] == 0x80 && (data[i+2] == 0xA8 || data[i+2] == 0xA9):
		return 3
	}
	return 0
}

// offset turns a node's line and column (1-based; the column counts
// characters, not bytes) into a byte offset.
func (t *text) offset(line, col int) (int, bool) {
	if line < 1 || line > len(t.starts) || col < 1 {
		return 0, false
	}
	off := t.starts[line-1]
	for c := 1; c < col; c++ {
		if off >= len(t.data) || breakAt(t.data, off) > 0 {
			return 0, false
		}
		_, w := utf8.DecodeRune(t.data[off:])
		off += w
	}
	return off, true
}

// Reasons a scalar's text cannot be found or replaced safely.
var (
	errMultiLine = errors.New("is written over several lines; Launchway edits single-line values only")
	errNotFound  = errors.New("could not be found in the file text; the file was not changed")
)

// refusal says why a node cannot be edited in place, or "" if it can.
func refusal(n *yaml.Node) string {
	switch {
	case n.Kind == yaml.AliasNode:
		return fmt.Sprintf("is an alias (*%s); Launchway does not edit through aliases", n.Value)
	case n.Kind == yaml.MappingNode:
		return "is a map, not a single value; give the path of a value inside it"
	case n.Kind == yaml.SequenceNode:
		return "is a list, not a single value; give the path of an item, such as [0]"
	case n.Kind != yaml.ScalarNode:
		return "is not a single value"
	case n.Anchor != "":
		return fmt.Sprintf("has an anchor (&%s), so changing it would also change every *%s; Launchway does not edit anchored values", n.Anchor, n.Anchor)
	case n.Style&yaml.TaggedStyle != 0:
		return fmt.Sprintf("has an explicit tag (%s); Launchway does not edit tagged values", n.Tag)
	case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return "is a block of text (| or >); Launchway edits single-line values only"
	}
	return ""
}

// span finds the bytes of a scalar's text, quotes included. The scalar
// must have passed refusal and must not be empty plain text.
func (t *text) span(n *yaml.Node) (start, end int, err error) {
	start, ok := t.offset(n.Line, n.Column)
	if !ok || start >= len(t.data) {
		return 0, 0, errNotFound
	}
	d := t.data
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		if d[start] != '"' {
			return 0, 0, errNotFound
		}
		for i := start + 1; i < len(d); i++ {
			if breakAt(d, i) > 0 {
				return 0, 0, errMultiLine
			}
			switch d[i] {
			case '\\':
				i++
				if i < len(d) && breakAt(d, i) > 0 {
					return 0, 0, errMultiLine
				}
			case '"':
				return start, i + 1, nil
			}
		}
		return 0, 0, errNotFound
	case n.Style&yaml.SingleQuotedStyle != 0:
		if d[start] != '\'' {
			return 0, 0, errNotFound
		}
		for i := start + 1; i < len(d); i++ {
			if breakAt(d, i) > 0 {
				return 0, 0, errMultiLine
			}
			if d[i] == '\'' {
				if i+1 < len(d) && d[i+1] == '\'' {
					i++
					continue
				}
				return start, i + 1, nil
			}
		}
		return 0, 0, errNotFound
	default:
		// Plain text on one line is exactly its value. If the value is
		// not there as written, it was folded from several lines.
		if n.Value == "" || !bytes.HasPrefix(d[start:], []byte(n.Value)) {
			return 0, 0, errMultiLine
		}
		return start, start + len(n.Value), nil
	}
}

// emptyValueAt finds where to write a value for a key that has none
// (key: with nothing after it): just after the colon.
func (t *text) emptyValueAt(key *yaml.Node) (int, bool) {
	if key == nil || key.Kind != yaml.ScalarNode || refusal(key) != "" || key.Value == "" && key.Style == 0 {
		return 0, false
	}
	_, end, err := t.span(key)
	if err != nil {
		return 0, false
	}
	for end < len(t.data) && (t.data[end] == ' ' || t.data[end] == '\t') {
		end++
	}
	if end >= len(t.data) || t.data[end] != ':' {
		return 0, false
	}
	return end + 1, true
}

// render writes a value as scalar text, keeping the style it had: double
// quotes, single quotes or plain. Plain text that would read back as
// something other than the same string is double-quoted instead.
func render(style yaml.Style, v string) string {
	switch {
	case style&yaml.DoubleQuotedStyle != 0:
		return doubleQuote(v)
	case style&yaml.SingleQuotedStyle != 0:
		for _, r := range v {
			if !printable(r) {
				return doubleQuote(v)
			}
		}
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	case plainOK(v):
		return v
	default:
		return doubleQuote(v)
	}
}

// printable reports whether r may appear as itself in quoted YAML text.
func printable(r rune) bool {
	return r == ' ' || unicode.IsPrint(r) && r != 0xFEFF
}

func doubleQuote(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\t':
			b.WriteString(`\t`)
		case printable(r):
			b.WriteRune(r)
		case r <= 0xFF:
			fmt.Fprintf(&b, `\x%02X`, r)
		case r <= 0xFFFF:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			fmt.Fprintf(&b, `\U%08X`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// plainOK reports whether v can be written as plain text, in a block or in
// a flow collection, and read back as the same string.
func plainOK(v string) bool {
	if v == "" || v[0] == ' ' || v[len(v)-1] == ' ' {
		return false
	}
	for _, r := range v {
		if !printable(r) || r > 0x7F && unicode.IsSpace(r) {
			return false
		}
	}
	if strings.ContainsRune("?:,[]{}#&*!|>'\"%@`", rune(v[0])) ||
		v[0] == '-' && (len(v) == 1 || v[1] == ' ') ||
		strings.HasPrefix(v, "---") || strings.HasPrefix(v, "...") ||
		strings.ContainsAny(v, ",[]{}") ||
		strings.Contains(v, ": ") || strings.Contains(v, " #") || strings.HasSuffix(v, ":") {
		return false
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(v), &n); err != nil || len(n.Content) != 1 {
		return false
	}
	s := n.Content[0]
	return s.Kind == yaml.ScalarNode && s.Style == 0 && s.ShortTag() == "!!str" && s.Value == v
}

// change is one replacement of the bytes [start, end).
type change struct {
	start, end int
	text       string
}

// apply makes the changes, which must not overlap.
func apply(data []byte, changes []change) []byte {
	sort.Slice(changes, func(i, j int) bool { return changes[i].start < changes[j].start })
	out := make([]byte, 0, len(data)+64*len(changes))
	at := 0
	for _, c := range changes {
		out = append(out, data[at:c.start]...)
		out = append(out, c.text...)
		at = c.end
	}
	return append(out, data[at:]...)
}

// verify parses the edited file and checks that it is the same as the
// original except that each node in want now reads back as its string.
func verify(docs []*yaml.Node, out []byte, want map[*yaml.Node]string) error {
	got, err := readDocs(out)
	if err != nil {
		return fmt.Errorf("the edited file did not parse, so it was not changed (%v); please report this as a Launchway bug", err)
	}
	if len(got) != len(docs) {
		return errors.New("the edited file has a different number of documents, so it was not changed; please report this as a Launchway bug")
	}
	for i := range docs {
		if bad := sameTree(docs[i], got[i], want); bad != nil {
			return fmt.Errorf("line %d: the edited file did not read back as expected, so it was not changed; please report this as a Launchway bug", bad.Line)
		}
	}
	return nil
}

// sameTree compares two parsed documents and returns the first node of a
// that differs, or nil.
func sameTree(a, b *yaml.Node, want map[*yaml.Node]string) *yaml.Node {
	if w, ok := want[a]; ok {
		if b.Kind != yaml.ScalarNode || b.Value != w || b.ShortTag() != "!!str" {
			return a
		}
		return nil
	}
	if a.Kind != b.Kind || a.ShortTag() != b.ShortTag() || a.Value != b.Value ||
		a.Anchor != b.Anchor || a.Style != b.Style || len(a.Content) != len(b.Content) {
		return a
	}
	for i := range a.Content {
		if bad := sameTree(a.Content[i], b.Content[i], want); bad != nil {
			return bad
		}
	}
	return nil
}
