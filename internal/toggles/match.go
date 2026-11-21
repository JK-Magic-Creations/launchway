package toggles

import (
	"strconv"
	"strings"
)

// matchClause reports whether a clause matches a context. segments is the
// set of segments in effect, for in_segment.
func matchClause(c Clause, ctx Context, segments map[string]Segment, depth int) bool {
	ok := clauseTrue(c, ctx, segments, depth)
	return ok != c.Negate
}

func clauseTrue(c Clause, ctx Context, segments map[string]Segment, depth int) bool {
	if c.Op == OpInSegment {
		for _, v := range c.Values {
			name, _ := v.(string)
			if seg, ok := segments[name]; ok && inSegment(seg, ctx, segments, depth) {
				return true
			}
		}
		return false
	}
	have, present := ctx[c.Attribute]
	if !present || have == nil {
		return false
	}
	if c.Op == OpExists {
		return true
	}
	// A list attribute matches when any element does.
	if list, ok := have.([]any); ok {
		for _, el := range list {
			if matchOne(c.Op, Normalize(el), c.Values) {
				return true
			}
		}
		return false
	}
	if list, ok := have.([]string); ok {
		for _, el := range list {
			if matchOne(c.Op, el, c.Values) {
				return true
			}
		}
		return false
	}
	return matchOne(c.Op, Normalize(have), c.Values)
}

func matchOne(op string, have any, values []any) bool {
	switch op {
	case OpEquals:
		return len(values) > 0 && same(have, values[0])
	case OpIn:
		for _, v := range values {
			if same(have, v) {
				return true
			}
		}
		return false
	case OpPrefix, OpSuffix, OpContains:
		return matchText(op, have, values)
	case OpNumberEq, OpNumberLt, OpNumberLte, OpNumberGt, OpNumberGte:
		return matchNumber(op, have, values)
	case OpSemverEq, OpSemverLt, OpSemverLte, OpSemverGt, OpSemverGte:
		return matchSemver(op, have, values)
	}
	return false
}

func matchText(op string, have any, values []any) bool {
	s, ok := have.(string)
	if !ok {
		return false
	}
	for _, v := range values {
		t, ok := v.(string)
		if !ok {
			continue
		}
		switch {
		case op == OpPrefix && strings.HasPrefix(s, t), op == OpSuffix && strings.HasSuffix(s, t), op == OpContains && strings.Contains(s, t):
			return true
		}
	}
	return false
}

func matchNumber(op string, have any, values []any) bool {
	a, ok := have.(float64)
	if !ok || len(values) == 0 {
		return false
	}
	b, ok := Normalize(values[0]).(float64)
	if !ok {
		return false
	}
	switch op {
	case OpNumberEq:
		return a == b
	case OpNumberLt:
		return a < b
	case OpNumberLte:
		return a <= b
	case OpNumberGt:
		return a > b
	}
	return a >= b
}

func matchSemver(op string, have any, values []any) bool {
	a, ok := have.(string)
	if !ok || len(values) == 0 {
		return false
	}
	b, ok := values[0].(string)
	if !ok {
		return false
	}
	cmp, ok := CompareSemver(a, b)
	if !ok {
		return false
	}
	switch op {
	case OpSemverEq:
		return cmp == 0
	case OpSemverLt:
		return cmp < 0
	case OpSemverLte:
		return cmp <= 0
	case OpSemverGt:
		return cmp > 0
	}
	return cmp >= 0
}

// same compares two values of the same type; a string never equals a number.
func same(a, b any) bool {
	a, b = Normalize(a), Normalize(b)
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}

func inSegment(seg Segment, ctx Context, segments map[string]Segment, depth int) bool {
	if depth > 5 { // a segment that names another, no deeper than this
		return false
	}
	key, _ := bucketText(ctx["key"])
	for _, k := range seg.Excluded {
		if k == key && key != "" {
			return false
		}
	}
	for _, k := range seg.Included {
		if k == key && key != "" {
			return true
		}
	}
	for _, r := range seg.Rules {
		if allMatch(r.Clauses, ctx, segments, depth+1) {
			return true
		}
	}
	return false
}

func allMatch(cs []Clause, ctx Context, segments map[string]Segment, depth int) bool {
	for _, c := range cs {
		if !matchClause(c, ctx, segments, depth) {
			return false
		}
	}
	return true
}

// CompareSemver orders two semantic versions (semver.org precedence): the
// numbers first, then a version with a pre-release before the same without,
// and pre-release parts one by one (numbers before words, numbers by value,
// a shorter list before a longer one that starts the same). A leading "v" is
// allowed and build metadata (after +) is ignored. ok is false when either is
// not a version: major.minor.patch, each a whole number.
func CompareSemver(a, b string) (cmp int, ok bool) {
	x, ok1 := parseSemver(a)
	y, ok2 := parseSemver(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if x.n[i] != y.n[i] {
			if x.n[i] < y.n[i] {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case len(x.pre) == 0 && len(y.pre) == 0:
		return 0, true
	case len(x.pre) == 0:
		return 1, true
	case len(y.pre) == 0:
		return -1, true
	}
	for i := 0; i < len(x.pre) && i < len(y.pre); i++ {
		p, q := x.pre[i], y.pre[i]
		pn, perr := strconv.ParseUint(p, 10, 64)
		qn, qerr := strconv.ParseUint(q, 10, 64)
		switch {
		case perr == nil && qerr == nil:
			if pn != qn {
				if pn < qn {
					return -1, true
				}
				return 1, true
			}
		case perr == nil:
			return -1, true
		case qerr == nil:
			return 1, true
		case p != q:
			if p < q {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case len(x.pre) < len(y.pre):
		return -1, true
	case len(x.pre) > len(y.pre):
		return 1, true
	}
	return 0, true
}

type semver struct {
	n   [3]uint64
	pre []string
}

func parseSemver(s string) (semver, bool) {
	var v semver
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = strings.Split(s[i+1:], ".")
		s = s[:i]
		for _, p := range v.pre {
			if p == "" {
				return v, false
			}
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil || p == "" || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		v.n[i] = n
	}
	return v, true
}
