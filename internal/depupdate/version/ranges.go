package version

import "strings"

// A comparator is one ">=1.2.3" style bound.
type comparator struct {
	op string
	v  Semver
}

// Range is an npm style version range ("^1.2.3", "~1.2", ">=1 <2",
// "1.x || 2.x", "1.2.3 - 2.0.0", "*", "latest").
type Range struct {
	sets [][]comparator // OR of ANDs
	any  bool
	raw  string
}

// ParseRange reads an npm range. ok is false for what is not a range this
// package understands (a URL, a git reference, a dist-tag): such a
// dependency is not updated automatically.
func ParseRange(s string) (Range, bool) {
	r := Range{raw: s}
	s = strings.TrimSpace(s)
	if s == "" || s == "*" || s == "x" || s == "latest" {
		r.any = true
		return r, true
	}
	for _, alt := range strings.Split(s, "||") {
		set, ok := parseSet(strings.TrimSpace(alt))
		if !ok {
			return r, false
		}
		r.sets = append(r.sets, set)
	}
	return r, true
}

func parseSet(s string) ([]comparator, bool) {
	if s == "" || s == "*" {
		return nil, true
	}
	if i := strings.Index(s, " - "); i > 0 {
		lo, ok1 := ParseSemver(strings.TrimSpace(s[:i]))
		hi, ok2 := ParseSemver(strings.TrimSpace(s[i+3:]))
		if !ok1 || !ok2 {
			return nil, false
		}
		return []comparator{{">=", lo}, {"<=", hi}}, true
	}
	// Join "^ 1.2" and ">= 1" written with a space.
	f := strings.Fields(s)
	var toks []string
	for i := 0; i < len(f); i++ {
		if isOp(f[i]) && i+1 < len(f) {
			toks = append(toks, f[i]+f[i+1])
			i++
			continue
		}
		toks = append(toks, f[i])
	}
	var out []comparator
	for _, t := range toks {
		cs, ok := parseToken(t)
		if !ok {
			return nil, false
		}
		out = append(out, cs...)
	}
	return out, true
}

func isOp(s string) bool {
	switch s {
	case "^", "~", ">", ">=", "<", "<=", "=", "~>":
		return true
	}
	return false
}

func parseToken(t string) ([]comparator, bool) {
	op := ""
	for _, o := range []string{">=", "<=", "~>", "^", "~", ">", "<", "="} {
		if strings.HasPrefix(t, o) {
			op, t = o, t[len(o):]
			break
		}
	}
	t = strings.TrimPrefix(t, "v")
	parts := strings.SplitN(strings.SplitN(t, "+", 2)[0], ".", 3)
	wild := 0 // index of the first wildcard part, or 3
	wild = 3
	for i, p := range parts {
		if p == "x" || p == "X" || p == "*" {
			wild = i
			break
		}
	}
	for len(parts) < 3 {
		if wild == 3 {
			wild = len(parts)
		}
		parts = append(parts, "0")
	}
	for i := wild; i < 3; i++ {
		parts[i] = "0"
	}
	v, ok := ParseSemver(strings.Join(parts, "."))
	if !ok {
		return nil, false
	}
	switch op {
	case "", "=":
		if wild == 3 {
			return []comparator{{"=", v}}, true
		}
		if wild == 0 {
			return nil, true
		}
		return []comparator{{">=", v}, {"<", bump(v, wild-1)}}, true
	case "^":
		switch {
		case wild == 0:
			return nil, true
		case v.Major > 0 || wild <= 1:
			return []comparator{{">=", v}, {"<", bump(v, 0)}}, true
		case v.Minor > 0 || wild <= 2:
			return []comparator{{">=", v}, {"<", bump(v, 1)}}, true
		}
		return []comparator{{">=", v}, {"<", bump(v, 2)}}, true
	case "~", "~>":
		if wild <= 1 {
			return []comparator{{">=", v}, {"<", bump(v, 0)}}, true
		}
		return []comparator{{">=", v}, {"<", bump(v, 1)}}, true
	case ">":
		if wild < 3 {
			return []comparator{{">=", bump(v, wild-1)}}, true
		}
	case "<=":
		if wild < 3 {
			return []comparator{{"<", bump(v, wild-1)}}, true
		}
	}
	return []comparator{{op, v}}, true
}

// bump raises part i (0 major, 1 minor, 2 patch) by one and zeroes the rest.
func bump(v Semver, i int) Semver {
	if i < 0 {
		i = 0
	}
	switch i {
	case 0:
		return Semver{Major: v.Major + 1}
	case 1:
		return Semver{Major: v.Major, Minor: v.Minor + 1}
	}
	return Semver{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
}

// Contains reports whether v is in the range. A pre-release is only inside a
// range that itself names a pre-release of the same major.minor.patch.
func (r Range) Contains(v Semver) bool {
	if r.any {
		return !v.IsPre()
	}
	for _, set := range r.sets {
		if setContains(set, v) {
			return true
		}
	}
	return false
}

func setContains(set []comparator, v Semver) bool {
	pre := false
	for _, c := range set {
		if c.v.IsPre() && c.v.Major == v.Major && c.v.Minor == v.Minor && c.v.Patch == v.Patch {
			pre = true
		}
		switch c.op {
		case "=", "":
			if v.Compare(c.v) != 0 {
				return false
			}
		case ">":
			if v.Compare(c.v) <= 0 {
				return false
			}
		case ">=":
			if v.Compare(c.v) < 0 {
				return false
			}
		case "<":
			if v.Compare(c.v) >= 0 {
				return false
			}
		case "<=":
			if v.Compare(c.v) > 0 {
				return false
			}
		}
	}
	return !v.IsPre() || pre
}

// Rewrite returns the range with its version replaced by to, keeping the
// operator the way it was written: "^1.2.3" and 1.5.0 give "^1.5.0"; an exact
// "1.2.3" gives "1.5.0"; ">=1 <2" and a version outside it cannot be
// rewritten and gives ok false (the pull request then says a person must
// choose the new range).
func Rewrite(constraint, to string) (string, bool) {
	c := strings.TrimSpace(constraint)
	to = strings.TrimPrefix(to, "v")
	for _, op := range []string{"^", "~>", "~", ">=", "="} {
		if strings.HasPrefix(c, op) {
			rest := strings.TrimSpace(c[len(op):])
			if strings.ContainsAny(rest, " |") {
				return "", false
			}
			if _, ok := ParseSemver(strings.TrimPrefix(rest, "v")); !ok {
				return "", false
			}
			return op + to, true
		}
	}
	if _, ok := ParseSemver(c); ok && !strings.ContainsAny(c, " |") {
		return to, true
	}
	return "", false
}
