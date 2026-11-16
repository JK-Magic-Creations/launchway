// Package jsonscan reads untrusted JSON one value at a time, and cleans the
// text taken from it. The findings (SARIF) and sbom (CycloneDX) parsers both
// use it.
//
// encoding/json's Decoder is not used for the structure because it cannot
// skip one bad element of an array and carry on: an element nested too
// deeply, or broken part-way, stops the whole decoder, and Decode buffers an
// element completely before anything can check its depth. The Scanner here
// walks the bytes itself, keeps only a bit per nesting level, checks the
// clock as it goes, and hands each element's bytes to the caller, who checks
// the depth it reports and then decodes the element with encoding/json into a
// small typed struct.
package jsonscan

import (
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// SyntaxError says where and why the JSON could not be read. EOF is true
// when the data ends part-way through a value, which usually means the file
// was cut short.
type SyntaxError struct {
	Offset int
	Msg    string
	EOF    bool
}

func (e *SyntaxError) Error() string { return e.Msg }

// ErrStopped is returned when the time limit passes or the context ends.
var ErrStopped = errors.New("parsing stopped")

// Info describes a value the Scanner skipped over.
type Info struct {
	Depth         int // nesting inside the value: 0 for a string or number, 1 for [] or {"a":1}, 2 for [[]]
	LongestString int // bytes in the longest string (key or value), escapes counted as written
}

// Scanner reads JSON from a byte slice. It never copies the data.
type Scanner struct {
	data  []byte
	pos   int
	dl    *Deadline
	stack []uint64 // one bit per open container: 1 for an object, 0 for an array
}

// New returns a Scanner at the start of data. dl may be nil.
func New(data []byte, dl *Deadline) *Scanner {
	return &Scanner{data: data, dl: dl}
}

// At returns a second Scanner over the same data, starting at pos.
func (s *Scanner) At(pos int) *Scanner {
	return &Scanner{data: s.data, pos: pos, dl: s.dl}
}

// Pos is the offset of the next byte to read.
func (s *Scanner) Pos() int { return s.pos }

// Line is the 1-based line number of a byte offset.
func (s *Scanner) Line(off int) int {
	off = min(max(off, 0), len(s.data))
	n := 1
	for _, c := range s.data[:off] {
		if c == '\n' {
			n++
		}
	}
	return n
}

// Peek skips white space and returns the next byte, or 0 at the end. A NUL
// byte in the data (never valid JSON outside a string) is returned as 0x01,
// which is not valid either, so that 0 always means the end and callers
// report the NUL as the syntax error it is; error messages read the data
// itself (see found), so they still name byte 0x00.
func (s *Scanner) Peek() byte {
	s.ws()
	if s.pos >= len(s.data) {
		return 0
	}
	if c := s.data[s.pos]; c != 0 {
		return c
	}
	return 0x01
}

// Rest says whether anything other than white space is left.
func (s *Scanner) Rest() bool {
	s.ws()
	return s.pos < len(s.data)
}

func (s *Scanner) ws() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\n', '\r':
			s.pos++
		default:
			return
		}
	}
}

func (s *Scanner) errAt(off int, format string, args ...any) error {
	return &SyntaxError{Offset: off, Msg: fmt.Sprintf(format, args...)}
}

func (s *Scanner) eof() error {
	return &SyntaxError{Offset: len(s.data), Msg: "the data ends part-way through a value", EOF: true}
}

// found describes the byte at off for an error message.
func (s *Scanner) found(off int) string {
	if off >= len(s.data) {
		return "the end of the data"
	}
	c := s.data[off]
	if c >= 0x21 && c < 0x7f {
		return strconv.QuoteRune(rune(c))
	}
	return fmt.Sprintf("byte 0x%02x", c)
}

func (s *Scanner) tick() error {
	if s.dl != nil && s.dl.Tick() {
		return ErrStopped
	}
	return nil
}

// Expired checks the clock and the context now.
func (s *Scanner) Expired() bool { return s.dl != nil && s.dl.Expired() }

// Value reads the value at the current position (after white space) and
// returns its bytes, which are part of the data and must not be changed.
// The whole value is checked to be well-formed JSON, apart from duplicate
// object keys and invalid UTF-8, which Valid looks at.
func (s *Scanner) Value() ([]byte, Info, error) {
	s.ws()
	start := s.pos
	var info Info
	// bits holds one bit per open container; n is how many are open.
	bits := s.stack[:0]
	n := 0
	setTop := func(object bool) {
		w, b := n/64, uint(n%64)
		for len(bits) <= w {
			bits = append(bits, 0)
		}
		if object {
			bits[w] |= 1 << b
		} else {
			bits[w] &^= 1 << b
		}
		n++
		info.Depth = max(info.Depth, n)
	}
	topIsObject := func() bool { return bits[(n-1)/64]&(1<<uint((n-1)%64)) != 0 }
	defer func() { s.stack = bits[:0] }()

	for {
		// A value starts here.
		if err := s.tick(); err != nil {
			return nil, info, err
		}
		s.ws()
		if s.pos >= len(s.data) {
			return nil, info, s.eof()
		}
		switch c := s.data[s.pos]; {
		case c == '{':
			s.pos++
			setTop(true)
			s.ws()
			if s.pos < len(s.data) && s.data[s.pos] == '}' {
				s.pos++
				n--
				break
			}
			if err := s.memberKey(&info); err != nil {
				return nil, info, err
			}
			continue
		case c == '[':
			s.pos++
			setTop(false)
			s.ws()
			if s.pos < len(s.data) && s.data[s.pos] == ']' {
				s.pos++
				n--
				break
			}
			continue
		case c == '"':
			l, err := s.skipString()
			if err != nil {
				return nil, info, err
			}
			info.LongestString = max(info.LongestString, l)
		case c == '-' || (c >= '0' && c <= '9'):
			if err := s.skipNumber(); err != nil {
				return nil, info, err
			}
		case c == 't':
			if err := s.literal("true"); err != nil {
				return nil, info, err
			}
		case c == 'f':
			if err := s.literal("false"); err != nil {
				return nil, info, err
			}
		case c == 'n':
			if err := s.literal("null"); err != nil {
				return nil, info, err
			}
		default:
			return nil, info, s.errAt(s.pos, "expected a JSON value, found %s", s.found(s.pos))
		}
		// A value has ended: close containers until another value is due.
		for {
			if n == 0 {
				return s.data[start:s.pos], info, nil
			}
			s.ws()
			if s.pos >= len(s.data) {
				return nil, info, s.eof()
			}
			c := s.data[s.pos]
			if topIsObject() {
				if c == '}' {
					s.pos++
					n--
					continue
				}
				if c != ',' {
					return nil, info, s.errAt(s.pos, "expected ',' or '}' after an object member, found %s", s.found(s.pos))
				}
				s.pos++
				if err := s.memberKey(&info); err != nil {
					return nil, info, err
				}
				break
			}
			if c == ']' {
				s.pos++
				n--
				continue
			}
			if c != ',' {
				return nil, info, s.errAt(s.pos, "expected ',' or ']' after an array element, found %s", s.found(s.pos))
			}
			s.pos++
			break
		}
	}
}

// memberKey reads `"key" :` inside an object.
func (s *Scanner) memberKey(info *Info) error {
	s.ws()
	if s.pos >= len(s.data) {
		return s.eof()
	}
	if s.data[s.pos] != '"' {
		return s.errAt(s.pos, "expected a quoted object key, found %s", s.found(s.pos))
	}
	l, err := s.skipString()
	if err != nil {
		return err
	}
	info.LongestString = max(info.LongestString, l)
	s.ws()
	if s.pos >= len(s.data) {
		return s.eof()
	}
	if s.data[s.pos] != ':' {
		return s.errAt(s.pos, "expected ':' after an object key, found %s", s.found(s.pos))
	}
	s.pos++
	return nil
}

// skipString moves past the string starting at pos and returns the length
// of its content in bytes.
func (s *Scanner) skipString() (int, error) {
	start := s.pos
	i := s.pos + 1
	d := s.data
	for i < len(d) {
		c := d[i]
		switch {
		case c == '"':
			s.pos = i + 1
			return i - start - 1, nil
		case c == '\\':
			if i+1 >= len(d) {
				return 0, s.eof()
			}
			switch d[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > len(d) {
					return 0, s.eof()
				}
				for _, h := range d[i+2 : i+6] {
					if !isHex(h) {
						return 0, s.errAt(i, "a \\u escape in a string is not followed by four hex digits")
					}
				}
				i += 6
			default:
				return 0, s.errAt(i, "a string holds an unknown escape \\%s", printable(d[i+1]))
			}
		case c < 0x20:
			return 0, s.errAt(i, "a string holds a raw control character (byte 0x%02x)", c)
		default:
			i++
		}
	}
	return 0, s.eof()
}

func printable(c byte) string {
	if c >= 0x21 && c < 0x7f {
		return string(rune(c))
	}
	return fmt.Sprintf("(byte 0x%02x)", c)
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// skipNumber checks -?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)? .
func (s *Scanner) skipNumber() error {
	d, i := s.data, s.pos
	start := i
	bad := func() error {
		if i >= len(d) {
			return s.eof()
		}
		return s.errAt(start, "a number is not written the way JSON allows")
	}
	if d[i] == '-' {
		i++
	}
	if i >= len(d) {
		return bad()
	}
	if d[i] == '0' {
		i++
	} else if d[i] >= '1' && d[i] <= '9' {
		for i < len(d) && isDigit(d[i]) {
			i++
		}
	} else {
		return bad()
	}
	if i < len(d) && d[i] == '.' {
		i++
		if i >= len(d) || !isDigit(d[i]) {
			return bad()
		}
		for i < len(d) && isDigit(d[i]) {
			i++
		}
	}
	if i < len(d) && (d[i] == 'e' || d[i] == 'E') {
		i++
		if i < len(d) && (d[i] == '+' || d[i] == '-') {
			i++
		}
		if i >= len(d) || !isDigit(d[i]) {
			return bad()
		}
		for i < len(d) && isDigit(d[i]) {
			i++
		}
	}
	s.pos = i
	return nil
}

func (s *Scanner) literal(word string) error {
	d := s.data[s.pos:]
	for i := 0; i < len(word); i++ {
		if i >= len(d) {
			return s.eof()
		}
		if d[i] != word[i] {
			return s.errAt(s.pos, "expected %s, found something else", word)
		}
	}
	s.pos += len(word)
	return nil
}

// Skip reads and ignores the value at the current position.
func (s *Scanner) Skip() error {
	_, _, err := s.Value()
	return err
}

// maxKeyBytes is the longest raw object key that Object decodes; longer
// keys are passed on as "" (no key the parsers look for is that long).
const maxKeyBytes = 256

// Object reads the object at the current position, calling fn once per
// member with its key. fn must read the member's value with Value, Skip,
// Object or Array. An error from fn stops the reading and is returned as is.
func (s *Scanner) Object(fn func(key string) error) error {
	if err := s.expect('{', "an object"); err != nil {
		return err
	}
	if s.Peek() == '}' {
		s.pos++
		return nil
	}
	for {
		if err := s.tick(); err != nil {
			return err
		}
		if s.Peek() != '"' {
			if s.pos >= len(s.data) {
				return s.eof()
			}
			return s.errAt(s.pos, "expected a quoted object key, found %s", s.found(s.pos))
		}
		kstart := s.pos
		if _, err := s.skipString(); err != nil {
			return err
		}
		key := decodeKey(s.data[kstart:s.pos])
		if err := s.expect(':', "':' after an object key"); err != nil {
			return err
		}
		before := s.pos
		if err := fn(key); err != nil {
			return err
		}
		if s.pos == before {
			// fn did not read the value; read it here so the structure
			// stays in step.
			if err := s.Skip(); err != nil {
				return err
			}
		}
		switch s.Peek() {
		case ',':
			s.pos++
		case '}':
			s.pos++
			return nil
		case 0:
			return s.eof()
		default:
			return s.errAt(s.pos, "expected ',' or '}' after an object member, found %s", s.found(s.pos))
		}
	}
}

// Array reads the array at the current position, calling fn once per
// element. fn must read the element as Object does.
func (s *Scanner) Array(fn func() error) error {
	if err := s.expect('[', "an array"); err != nil {
		return err
	}
	if s.Peek() == ']' {
		s.pos++
		return nil
	}
	for {
		if err := s.tick(); err != nil {
			return err
		}
		s.ws()
		before := s.pos
		if err := fn(); err != nil {
			return err
		}
		if s.pos == before {
			if err := s.Skip(); err != nil {
				return err
			}
		}
		switch s.Peek() {
		case ',':
			s.pos++
		case ']':
			s.pos++
			return nil
		case 0:
			return s.eof()
		default:
			return s.errAt(s.pos, "expected ',' or ']' after an array element, found %s", s.found(s.pos))
		}
	}
}

func (s *Scanner) expect(c byte, what string) error {
	switch s.Peek() {
	case c:
		s.pos++
		return nil
	case 0:
		return s.eof()
	}
	return s.errAt(s.pos, "expected %s, found %s", what, s.found(s.pos))
}

// decodeKey turns a raw quoted key into its text. Keys with escapes are
// decoded properly; very long keys become "".
func decodeKey(raw []byte) string {
	if len(raw) > maxKeyBytes+2 {
		return ""
	}
	inner := raw[1 : len(raw)-1]
	plain := true
	for _, c := range inner {
		if c == '\\' || c >= utf8.RuneSelf {
			plain = false
			break
		}
	}
	if plain {
		return string(inner)
	}
	var k string
	if json.Unmarshal(raw, &k) != nil {
		return ""
	}
	return k
}

// Valid reports whether a value already read by Value has no duplicate keys
// in any of its objects. Duplicate keys are a way to make two readers see
// different content, so elements holding them are not used. Invalid UTF-8
// is allowed here; the text cleaners replace it.
func Valid(v []byte) bool {
	return jsontext.Value(v).IsValid(jsontext.AllowInvalidUTF8(true))
}

// String decodes a JSON string value. Numbers are given as written; any
// other kind of value gives ok=false.
func String(v []byte) (string, bool) {
	if len(v) == 0 {
		return "", false
	}
	switch c := v[0]; {
	case c == '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return "", false
		}
		return s, true
	case c == '-' || isDigit(c):
		return string(v), true
	}
	return "", false
}
