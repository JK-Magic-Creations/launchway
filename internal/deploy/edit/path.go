package edit

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxPathLen bounds a path, so a mistake cannot become a huge error message.
const MaxPathLen = 512

// maxIndex bounds a sequence index. No real file has a list this long.
const maxIndex = 1_000_000

var keyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// step is one part of a path: a key in a mapping, or an index in a sequence.
type step struct {
	key     string
	index   int
	isIndex bool
	quoted  bool // the key was written as ["key"]
}

// path is a parsed path, such as spec.sources[0].targetRevision.
type path []step

// CheckPath reports whether a path is written correctly (used when a
// pipeline is parsed, before any file is read). nil if it is.
//
// A path is keys separated by dots, each optionally followed by one or more
// [N] sequence indexes: image.tag, spec.sources[1].targetRevision,
// images[0]. Keys use letters, digits, _ and -. A key with other characters,
// such as a dot, is written in brackets and double quotes:
// metadata.annotations["example.com/revision"].
func CheckPath(p string) error {
	_, err := parsePath(p)
	return err
}

func parsePath(s string) (path, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("path %q: %s", s, fmt.Sprintf(format, args...))
	}
	if s == "" {
		return nil, errors.New("the path is empty; give one such as image.tag")
	}
	if len(s) > MaxPathLen {
		return nil, fmt.Errorf("the path is longer than %d characters", MaxPathLen)
	}
	var p path
	i := 0
	// readKey reads a plain key starting at i.
	readKey := func() error {
		j := i
		for j < len(s) && s[j] != '.' && s[j] != '[' {
			j++
		}
		key := s[i:j]
		if key == "" {
			if j == len(s) {
				return bad("it ends with a dot; remove it or add the key that follows")
			}
			return bad("there is an empty key at character %d", i+1)
		}
		if !keyRe.MatchString(key) {
			return bad("key %q may use only letters, digits, _ and -; write other keys as [\"%s\"]", key, key)
		}
		p = append(p, step{key: key})
		i = j
		return nil
	}
	// readBracket reads [N] or ["key"] starting at i, which is '['.
	readBracket := func() error {
		end := strings.IndexByte(s[i:], ']')
		if end < 0 {
			return bad("a [ at character %d is not closed with ]", i+1)
		}
		inner := s[i+1 : i+end]
		switch {
		case len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"':
			key := inner[1 : len(inner)-1]
			if key == "" {
				return bad("there is an empty key at character %d", i+1)
			}
			if strings.ContainsAny(key, "\"\\") {
				return bad("a key in brackets may not contain \" or \\")
			}
			p = append(p, step{key: key, quoted: true})
		case inner == "":
			return bad("[] needs an index, such as [0]")
		default:
			for _, c := range inner {
				if c < '0' || c > '9' {
					return bad("[%s] is not an index; use a whole number 0 or more, such as [0], or a quoted key such as [\"name\"]", inner)
				}
			}
			n, err := strconv.Atoi(inner)
			if err != nil || n > maxIndex {
				return bad("index [%s] is too large", inner)
			}
			p = append(p, step{index: n, isIndex: true})
		}
		i += end + 1
		return nil
	}

	if s[0] == '[' {
		if err := readBracket(); err != nil {
			return nil, err
		}
		if p[0].isIndex {
			return nil, bad("it must start with a key, such as images[0], not with an index")
		}
	} else if err := readKey(); err != nil {
		return nil, err
	}
	for i < len(s) {
		switch s[i] {
		case '.':
			i++
			if err := readKey(); err != nil {
				return nil, err
			}
		case '[':
			if err := readBracket(); err != nil {
				return nil, err
			}
		default:
			return nil, bad("expected . or [ at character %d", i+1)
		}
	}
	return p, nil
}

// String writes the path back in its usual form.
func (p path) String() string {
	var b strings.Builder
	for i, st := range p {
		switch {
		case st.isIndex:
			fmt.Fprintf(&b, "[%d]", st.index)
		case st.quoted:
			fmt.Fprintf(&b, "[%q]", st.key)
		default:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(st.key)
		}
	}
	return b.String()
}
