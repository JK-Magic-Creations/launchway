package testreport

import (
	"encoding/json"
	"fmt"
)

// The JSON reports are read piece by piece: a small scanner finds the
// bounds of each value without decoding it (and refuses nesting deeper
// than Limits.MaxDepth before anything is decoded), and only the pieces a
// parser needs are handed to encoding/json, one case at a time. Nothing
// is copied up front, cases past MaxCases are decoded into a struct that
// holds only their status, and a file cut short still yields the cases
// before the cut.

// jsonEnd says how scanning a value ended.
type jsonEnd int

const (
	jsonOK   jsonEnd = iota
	jsonCut          // the data ends inside the value
	jsonDeep         // the value nests deeper than allowed
	jsonBad          // the data is not JSON here
)

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) && isJSONSpace(b[i]) {
		i++
	}
	return i
}

// skipString returns the index just past the string that starts at b[i]
// (a quote), or -1 if the data ends first.
func skipString(b []byte, i int) int {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return -1
}

// skipValue returns the index just past the JSON value that starts at or
// after b[i], allowing at most depth levels of nesting (an object or array
// is one level).
func skipValue(b []byte, i, depth int) (int, jsonEnd) {
	i = skipJSONSpace(b, i)
	if i >= len(b) {
		return i, jsonCut
	}
	level := 0
	for i < len(b) {
		switch c := b[i]; c {
		case '"':
			j := skipString(b, i)
			if j < 0 {
				return len(b), jsonCut
			}
			i = j
			if level == 0 {
				return i, jsonOK
			}
			continue
		case '{', '[':
			level++
			if level > depth {
				return i, jsonDeep
			}
		case '}', ']':
			level--
			if level < 0 {
				return i, jsonBad
			}
			if level == 0 {
				return i + 1, jsonOK
			}
		default:
			if level == 0 {
				j := i
				for j < len(b) && !isJSONSpace(b[j]) && b[j] != ',' && b[j] != '}' && b[j] != ']' && b[j] != ':' {
					j++
				}
				if j == i {
					return i, jsonBad
				}
				return j, jsonOK
			}
		}
		i++
	}
	return len(b), jsonCut
}

// jsonFields calls fn with each member of the object at the start of b.
// When a member's value is cut short or nests too deep, fn gets the rest
// of the data from where the value starts, with the reason, and the walk
// stops there. depth is the nesting allowed for b itself. It returns how
// the object ended and where.
func jsonFields(b []byte, depth int, fn func(key string, val []byte, end jsonEnd) bool) (jsonEnd, int) {
	i := skipJSONSpace(b, 0)
	if i >= len(b) {
		return jsonCut, i
	}
	if b[i] != '{' {
		return jsonBad, i
	}
	if depth < 1 {
		return jsonDeep, i
	}
	i++
	for {
		i = skipJSONSpace(b, i)
		if i >= len(b) {
			return jsonCut, i
		}
		switch b[i] {
		case '}':
			return jsonOK, i + 1
		case ',':
			i++
			continue
		case '"':
		default:
			return jsonBad, i
		}
		j := skipString(b, i)
		if j < 0 {
			return jsonCut, len(b)
		}
		key := jsonKey(b[i:j])
		i = skipJSONSpace(b, j)
		if i >= len(b) {
			return jsonCut, i
		}
		if b[i] != ':' {
			return jsonBad, i
		}
		start := skipJSONSpace(b, i+1)
		end, how := skipValue(b, start, depth-1)
		if how != jsonOK {
			if how != jsonBad {
				fn(key, b[start:], how)
			}
			return how, end
		}
		if !fn(key, b[start:end], jsonOK) {
			return jsonOK, end
		}
		i = end
	}
}

// jsonElems calls fn with each element of the array at the start of b,
// stopping at an element that is cut short or nests too deep. It returns
// how the array ended and where.
func jsonElems(b []byte, depth int, fn func(elem []byte) bool) (jsonEnd, int) {
	i := skipJSONSpace(b, 0)
	if i >= len(b) {
		return jsonCut, i
	}
	if b[i] != '[' {
		return jsonBad, i
	}
	if depth < 1 {
		return jsonDeep, i
	}
	i++
	for {
		i = skipJSONSpace(b, i)
		if i >= len(b) {
			return jsonCut, i
		}
		switch b[i] {
		case ']':
			return jsonOK, i + 1
		case ',':
			i++
			continue
		}
		end, how := skipValue(b, i, depth-1)
		if how != jsonOK {
			return how, i // where the element starts
		}
		if !fn(b[i:end]) {
			return jsonOK, end
		}
		i = end
	}
}

// jsonElemsPartial is jsonElems, except that an element cut short or
// nested too deep is still handed to fn (as the rest of the data from where
// it starts), so that a parser reading it member by member can keep what
// comes before the problem.
func jsonElemsPartial(b []byte, depth int, fn func(elem []byte) bool) (jsonEnd, int) {
	var tail []byte
	how, at := jsonElems(b, depth, fn)
	if how == jsonCut || how == jsonDeep {
		i := skipJSONSpace(b, at)
		if i < len(b) && b[i] != ']' {
			tail = b[i:]
		}
	}
	if tail != nil && jsonKind(tail) == '{' {
		fn(tail)
	}
	return how, at
}

// jsonKey decodes an object key; very long keys are not decoded.
func jsonKey(raw []byte) string {
	if len(raw) > 256 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// jsonKind says what kind of value starts b: '{', '[', '"', or 0.
func jsonKind(b []byte) byte {
	i := skipJSONSpace(b, 0)
	if i < len(b) {
		switch b[i] {
		case '{', '[', '"':
			return b[i]
		}
	}
	return 0
}

// jsonObject returns the members of the object b keyed by name, the first
// of each name winning: complete values only, or with partial also a value
// cut short or nested too deep (as the rest of the data).
func jsonObject(b []byte, depth int, partial bool) map[string][]byte {
	m := map[string][]byte{}
	jsonFields(b, depth, func(k string, v []byte, end jsonEnd) bool {
		if _, ok := m[k]; !ok && (end == jsonOK || partial) {
			m[k] = v
		}
		return len(m) < 1000
	})
	return m
}

// endProblem turns how a list ended into a warning, or "" if it ended
// well.
func (c *collector) jsonEndWarning(how jsonEnd, depth int) {
	switch how {
	case jsonCut:
		c.warn("the file ends part-way through; results after that are missing")
	case jsonDeep:
		c.warn("the JSON nests more than %d deep; results after that are missing", depth)
	case jsonBad:
		c.warn("the JSON is broken; results after that are missing")
	}
}

// jsonError is the error for a JSON file that gave no result at all
// because of how it ended.
func jsonEndError(how jsonEnd, depth int) error {
	switch how {
	case jsonDeep:
		return fmt.Errorf("%w: the JSON nests more than %d deep", ErrNotReport, depth)
	case jsonCut:
		return fmt.Errorf("%w: the JSON ends before any test result", ErrNotReport)
	}
	return fmt.Errorf("%w: the JSON could not be read", ErrNotReport)
}

// decodeInto unmarshals a value the scanner already bounded.
func decodeInto(b []byte, v any) bool {
	return json.Unmarshal(b, v) == nil
}

// jsonText is a JSON value read leniently as text: a string as itself, a
// number or boolean as written, anything else (null, objects, arrays) as
// empty.
type jsonText string

func (t *jsonText) UnmarshalJSON(b []byte) error {
	switch jsonKind(b) {
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = jsonText(s)
	case '{', '[':
		*t = ""
	default:
		s := string(b)
		if s == "null" {
			s = ""
		}
		*t = jsonText(s)
	}
	return nil
}

// jsonNum is a number that may be written as a string, or be missing,
// null or garbage (then 0).
type jsonNum float64

func (n *jsonNum) UnmarshalJSON(b []byte) error {
	*n = jsonNum(jsonNumber(string(b)))
	return nil
}

// jsonStrings reads a string or a list of strings (other elements are
// skipped).
type jsonStrings []string

func (s *jsonStrings) UnmarshalJSON(b []byte) error {
	switch jsonKind(b) {
	case '"':
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = []string{one}
	case '[':
		var many []jsonText
		if err := json.Unmarshal(b, &many); err != nil {
			return err
		}
		out := make([]string, 0, len(many))
		for _, m := range many {
			if m != "" {
				out = append(out, string(m))
			}
		}
		*s = out
	default:
		*s = nil
	}
	return nil
}
