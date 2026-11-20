package testreport

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// JUnit XML has no single standard; this reader follows what pytest, Maven
// surefire, jest-junit, vitest, gotestsum and friends write.

type encodingKind int

const (
	encUTF8 encodingKind = iota
	encLatin1
)

// encodingOf names the kind of a declared encoding, or says it is not
// supported.
func encodingOf(label string) (encodingKind, bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		// ASCII is a subset of UTF-8, and files labelled ASCII often
		// hold UTF-8 anyway.
		return encUTF8, true
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "latin-1", "l1":
		return encLatin1, true
	}
	return encUTF8, false
}

// declaredEncoding reads the encoding from the XML declaration, if any.
func declaredEncoding(data []byte) (encodingKind, error) {
	if !bytes.HasPrefix(data, []byte("<?xml")) {
		return encUTF8, nil
	}
	head := data[:min(len(data), 4096)]
	end := bytes.Index(head, []byte("?>"))
	if end < 0 {
		return encUTF8, nil // the decoder will say what is wrong
	}
	label := declParam(string(head[5:end]), "encoding")
	kind, ok := encodingOf(label)
	if !ok {
		return 0, fmt.Errorf("%w: the XML is in encoding %s, which is not supported; write it as UTF-8", ErrNotReport, cleanName(label, 40))
	}
	return kind, nil
}

// declParam finds name="value" or name='value' in an XML declaration.
func declParam(decl, name string) string {
	i := strings.Index(decl, name)
	if i < 0 {
		return ""
	}
	s := strings.TrimLeft(decl[i+len(name):], " \t\r\n")
	if !strings.HasPrefix(s, "=") {
		return ""
	}
	s = strings.TrimLeft(s[1:], " \t\r\n")
	if s == "" || (s[0] != '"' && s[0] != '\'') {
		return ""
	}
	q := s[0]
	j := strings.IndexByte(s[1:], q)
	if j < 0 {
		return ""
	}
	return s[1 : 1+j]
}

// charsetReader lets the decoder read the few 8-bit encodings that test
// tools still write. Nothing outside the standard library is needed for
// ISO-8859-1: each byte is the code point of the same number.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	kind, ok := encodingOf(label)
	if !ok {
		return nil, fmt.Errorf("encoding %s is not supported", cleanName(label, 40))
	}
	if kind == encUTF8 {
		return input, nil
	}
	return &latin1Reader{r: input}, nil
}

type latin1Reader struct {
	r       io.Reader
	in, out []byte
	pending []byte
	err     error
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	for len(l.pending) == 0 {
		if l.err != nil {
			return 0, l.err
		}
		if l.in == nil {
			l.in = make([]byte, 4096)
		}
		n, err := l.r.Read(l.in)
		l.err = err
		out := l.out[:0]
		for _, b := range l.in[:n] {
			if b < utf8.RuneSelf {
				out = append(out, b)
			} else {
				out = utf8.AppendRune(out, rune(b))
			}
		}
		l.out = out
		l.pending = out
		if n == 0 && err == nil {
			return 0, nil
		}
	}
	n := copy(p, l.pending)
	l.pending = l.pending[n:]
	return n, nil
}

// prepareXML makes real-world report bytes acceptable to the strict decoder
// without changing what they mean. Test tools often write raw control
// characters, broken UTF-8 and references such as &#27; into captured
// output, and the decoder rejects the whole file for one of them.
//
//   - invalid UTF-8 and characters XML forbids become U+FFFD (UTF-8 input only)
//   - a raw control character c becomes &#xE0cc; (a private-use character
//     that cleanText removes again, after using an escape to strip colours)
//   - a reference to a forbidden character becomes the same, or &#xFFFD;
//   - an & that does not start one of the five predefined entities or a
//     character reference becomes &amp;, so &nbsp; or a DOCTYPE entity
//     shows up as plain text and is never expanded
//
// Text inside CDATA sections and comments is left alone apart from the
// character fixes. It returns nil if the time ran out.
func prepareXML(dl *deadline, src []byte, latin bool) []byte {
	out := make([]byte, 0, len(src)+len(src)/32)
	inCDATA, inComment := false, false
	next := 1 << 20
	for i := 0; i < len(src); {
		if i >= next {
			if dl.expired() {
				return nil
			}
			next = i + 1<<20
		}
		b := src[i]
		switch {
		case inCDATA:
			if b == ']' && bytes.HasPrefix(src[i:], []byte("]]>")) {
				out = append(out, "]]>"...)
				i += 3
				inCDATA = false
				continue
			}
		case inComment:
			if b == '-' && bytes.HasPrefix(src[i:], []byte("-->")) {
				out = append(out, "-->"...)
				i += 3
				inComment = false
				continue
			}
		case b == '<':
			if bytes.HasPrefix(src[i:], []byte("<![CDATA[")) {
				out = append(out, "<![CDATA["...)
				i += 9
				inCDATA = true
				continue
			}
			if bytes.HasPrefix(src[i:], []byte("<!--")) {
				out = append(out, "<!--"...)
				i += 4
				inComment = true
				continue
			}
		case b == '&':
			var n int
			out, n = appendRef(out, src[i:])
			i += n
			continue
		}
		switch {
		case b >= 0x20 && b < utf8.RuneSelf, b == '\t', b == '\n', b == '\r':
			out = append(out, b)
			i++
		case b < 0x20:
			if !inCDATA && !inComment {
				out = appendCtrlRef(out, int(b))
			} else if latin {
				out = append(out, '?')
			} else {
				out = utf8.AppendRune(out, ctrlBase+rune(b))
			}
			i++
		case latin:
			out = append(out, b)
			i++
		default:
			r, size := utf8.DecodeRune(src[i:])
			if (r == utf8.RuneError && size <= 1) || !isXMLChar(r) {
				out = utf8.AppendRune(out, utf8.RuneError)
			} else {
				out = append(out, src[i:i+size]...)
			}
			i += max(size, 1)
		}
	}
	return out
}

const hexDigits = "0123456789ABCDEF"

func appendCtrlRef(out []byte, c int) []byte {
	return append(out, '&', '#', 'x', 'E', '0', hexDigits[c>>4], hexDigits[c&15], ';')
}

func isXMLChar(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// appendRef handles an & at the start of s and says how many bytes of s it
// used.
func appendRef(out, s []byte) ([]byte, int) {
	if len(s) > 1 && s[1] == '#' {
		j, base := 2, 10
		if len(s) > 2 && (s[2] == 'x' || s[2] == 'X') {
			j, base = 3, 16
		}
		start := j
		v, big := 0, false
		for j < len(s) && j-start < 40 {
			d := digitValue(s[j], base)
			if d < 0 {
				break
			}
			if v > 0x10FFFF {
				big = true
			} else {
				v = v*base + d
			}
			j++
		}
		if j == start || j >= len(s) || s[j] != ';' {
			return append(out, "&amp;"...), 1
		}
		j++ // the ;
		switch {
		case !big && isXMLChar(rune(v)):
			return append(out, s[:j]...), j
		case !big && v < 0x20:
			return appendCtrlRef(out, v), j
		default:
			return append(out, "&#xFFFD;"...), j
		}
	}
	for _, name := range [...]string{"&amp;", "&lt;", "&gt;", "&quot;", "&apos;"} {
		if bytes.HasPrefix(s, []byte(name)) {
			return append(out, name...), len(name)
		}
	}
	return append(out, "&amp;"...), 1
}

func digitValue(c byte, base int) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case base == 16 && c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case base == 16 && c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
