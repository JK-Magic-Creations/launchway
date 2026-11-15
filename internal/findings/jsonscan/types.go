package jsonscan

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"strconv"
	"strings"
)

// Decode decodes a value already read by Value into v, which is usually a
// small struct. It uses encoding/json/v2 because the older Unmarshal
// matches field names regardless of case (and folds a few letters such as
// "ſ" to "s"), so an element could carry "ruleId" and "RULEID" and get
// whichever came last; v2 matches names exactly and refuses duplicate
// names. Invalid UTF-8 in strings is replaced rather than refused.
func Decode(data []byte, v any) error {
	return jsonv2.Unmarshal(data, v, jsontext.AllowInvalidUTF8(true))
}

// The types below are for the small structs elements are decoded into.
// Tools disagree on whether a line number is 12 or "12", so scalars accept
// both. A value of an entirely different kind (an object where a string
// belongs) is an error, which makes the parser skip that element.

var errKind = errors.New("a value of the wrong kind")

// Str is a string that may also be written as a number. null leaves it
// empty.
type Str string

func (s *Str) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	v, ok := String(b)
	if !ok {
		return errKind
	}
	*s = Str(v)
	return nil
}

// LooseStr is like Str, but a value of any other kind is ignored rather
// than an error. It is for free-form property bags.
type LooseStr string

func (s *LooseStr) UnmarshalJSON(b []byte) error {
	if v, ok := String(b); ok {
		*s = LooseStr(v)
	}
	return nil
}

// Int is a whole number that may also be written as a string. Set says the
// value was given at all; OK says it was a whole number of sensible size.
type Int struct {
	V       int64
	Set, OK bool
}

// maxSafeInt is the largest whole number a float64 holds exactly.
const maxSafeInt = 1 << 53

func (n *Int) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	v, ok := String(b)
	if !ok {
		return errKind
	}
	n.Set = true
	v = strings.TrimSpace(v)
	if i, err := strconv.ParseInt(v, 10, 64); err == nil {
		n.V, n.OK = i, i >= -maxSafeInt && i <= maxSafeInt
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err == nil && f == math.Trunc(f) && math.Abs(f) <= maxSafeInt {
		n.V, n.OK = int64(f), true
	}
	return nil
}

// Num is a number that may also be written as a string ("9.8"). Anything
// unreadable leaves it unset, without an error.
type Num struct {
	V  float64
	OK bool
}

func (n *Num) UnmarshalJSON(b []byte) error {
	v, ok := String(b)
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	n.V, n.OK = f, true
	return nil
}

// Strings keeps up to Max strings of an array, ignoring elements that are
// not strings or numbers, and anything that is not an array. It is for tag
// lists in property bags.
type Strings struct {
	List []string
	Max  int // set before decoding; 0 means 20
	Each int // bytes kept of each string before cleaning; 0 means 400
}

func (t *Strings) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || b[0] != '[' {
		return nil
	}
	limit, each := t.Max, t.Each
	if limit <= 0 {
		limit = 20
	}
	if each <= 0 {
		each = 400
	}
	s := New(b, nil)
	stop := errors.New("full")
	err := s.Array(func() error {
		v, _, err := s.Value()
		if err != nil {
			return err
		}
		if str, ok := String(v); ok {
			if len(str) > each {
				str = str[:RuneStartAtOrBefore(str, each)]
			}
			t.List = append(t.List, str)
			if len(t.List) >= limit {
				return stop
			}
		}
		return nil
	})
	_ = err // the bytes were checked when the element was read
	return nil
}

// Each calls fn for each element of the JSON array b (already read by
// Value) until fn returns false. Anything that is not an array calls fn
// for nothing.
func Each(b []byte, fn func(elem []byte) bool) {
	if len(b) == 0 || b[0] != '[' {
		return
	}
	s := New(b, nil)
	stop := errors.New("stop")
	_ = s.Array(func() error {
		v, _, err := s.Value()
		if err != nil {
			return err
		}
		if !fn(v) {
			return stop
		}
		return nil
	})
}

// Members calls fn for each member of the JSON object b (already read by
// Value) until fn returns false.
func Members(b []byte, fn func(key string, value []byte) bool) {
	if len(b) == 0 || b[0] != '{' {
		return
	}
	s := New(b, nil)
	stop := errors.New("stop")
	_ = s.Object(func(key string) error {
		v, _, err := s.Value()
		if err != nil {
			return err
		}
		if !fn(key, v) {
			return stop
		}
		return nil
	})
}

// RawFirst keeps a copy of the first element of an array, undecoded, so a
// list of a million locations costs no more than a list of one. Anything
// that is not an array leaves it empty.
type RawFirst struct {
	V []byte
}

func (r *RawFirst) UnmarshalJSON(b []byte) error {
	Each(b, func(e []byte) bool {
		r.V = bytes.Clone(e)
		return false
	})
	return nil
}
