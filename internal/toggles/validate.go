package toggles

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Limits on what a definition may hold, so that a ruleset stays small enough
// to evaluate in a hurry and to download.
const (
	MaxRules       = 100
	MaxClauses     = 20
	MaxValues      = 200
	MaxBuckets     = 20
	MaxStringBytes = 4096
	MaxJSONBytes   = 16 << 10
	MaxPrereqs     = 10
	MaxSegmentKeys = 5000
)

var (
	keyPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*([-_.][a-z0-9]+)*$`)
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
)

// CheckKey checks a toggle's key: lower-case letters, digits and - _ .,
// starting with a letter, at most 64 characters.
func CheckKey(k string) error {
	if len(k) > 64 || !keyPattern.MatchString(k) {
		return fmt.Errorf("a toggle's key is lower-case letters and digits, with - _ or . between them, starting with a letter, at most 64 characters (%q is not)", k)
	}
	return nil
}

// CheckSegmentName checks a segment's name: lower-case letters, digits and -.
func CheckSegmentName(n string) error {
	if len(n) > 64 || !namePattern.MatchString(n) {
		return fmt.Errorf("a segment's name is lower-case letters and digits with - between them, starting with a letter, at most 64 characters (%q is not)", n)
	}
	return nil
}

// CheckValue checks that v is a value of type t, in its decoded JSON form.
func CheckValue(t ValueType, v any) error {
	switch t {
	case Boolean:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("a boolean toggle serves true or false, not %s", describe(v))
		}
	case String:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("a string toggle serves text, not %s", describe(v))
		}
		if len(s) > MaxStringBytes {
			return fmt.Errorf("a string value is at most %d bytes", MaxStringBytes)
		}
	case Number:
		if _, ok := Normalize(v).(float64); !ok {
			return fmt.Errorf("a number toggle serves a number, not %s", describe(v))
		}
	case JSON:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("a json toggle serves JSON: %v", err)
		}
		if len(b) > MaxJSONBytes {
			return fmt.Errorf("a json value is at most %d bytes", MaxJSONBytes)
		}
	default:
		return fmt.Errorf("type is boolean, string, number or json, not %q", t)
	}
	return nil
}

func describe(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case string:
		return "text"
	case bool:
		return "a boolean"
	case float64, int, int64, json.Number:
		return "a number"
	}
	return "something else"
}

// CheckState checks a toggle's state in an environment against its type and
// assigns an id to every rule that has none (r1, r2, ... by position; an id
// that is written stays, so that a rule keeps its identity when others move).
func CheckState(t ValueType, s *State) error {
	if err := CheckValue(t, s.OffValue); err != nil {
		return fmt.Errorf("off_value: %w", err)
	}
	if err := checkServe(t, s.Fallthrough); err != nil {
		return fmt.Errorf("fallthrough: %w", err)
	}
	if len(s.Rules) > MaxRules {
		return fmt.Errorf("at most %d rules", MaxRules)
	}
	if len(s.Prerequisites) > MaxPrereqs {
		return fmt.Errorf("at most %d prerequisites", MaxPrereqs)
	}
	for i, p := range s.Prerequisites {
		if err := CheckKey(p.Toggle); err != nil {
			return fmt.Errorf("prerequisite %d: %w", i+1, err)
		}
		if p.Value == nil {
			return fmt.Errorf("prerequisite %d (%s) needs the value it must serve", i+1, p.Toggle)
		}
	}
	seen := map[string]bool{}
	for _, r := range s.Rules {
		if r.ID != "" {
			if seen[r.ID] {
				return fmt.Errorf("two rules have the id %q", r.ID)
			}
			seen[r.ID] = true
		}
	}
	n := 0
	for i := range s.Rules {
		r := &s.Rules[i]
		if r.ID == "" {
			for {
				n++
				if id := fmt.Sprintf("r%d", n); !seen[id] {
					r.ID, seen[id] = id, true
					break
				}
			}
		}
		if len(r.ID) > 40 || strings.ContainsAny(r.ID, " \t\n") {
			return fmt.Errorf("rule %d: an id is at most 40 characters with no spaces", i+1)
		}
		if err := checkClauses(r.Clauses); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
		if err := checkServe(t, r.Serve); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
	}
	return nil
}

func checkServe(t ValueType, s Serve) error {
	if s.Rollout == nil {
		return CheckValue(t, s.Value)
	}
	if s.Value != nil {
		return fmt.Errorf("serve a value or a rollout, not both")
	}
	r := s.Rollout
	if len(r.Buckets) == 0 || len(r.Buckets) > MaxBuckets {
		return fmt.Errorf("a rollout has 1 to %d shares", MaxBuckets)
	}
	total := 0
	for _, b := range r.Buckets {
		if b.Weight < 0 || b.Weight > BucketCount {
			return fmt.Errorf("a weight is 0 to %d (hundredths of a percent)", BucketCount)
		}
		total += b.Weight
		if err := CheckValue(t, b.Value); err != nil {
			return err
		}
	}
	if total != BucketCount {
		return fmt.Errorf("the weights of a rollout add up to %d (100%%), not %d", BucketCount, total)
	}
	if r.By != "" && !validAttr(r.By) {
		return fmt.Errorf("by is a context attribute name, not %q", r.By)
	}
	return nil
}

func validAttr(a string) bool { return a != "" && len(a) <= 64 && !strings.ContainsAny(a, " \t\n") }

func checkClauses(cs []Clause) error {
	if len(cs) > MaxClauses {
		return fmt.Errorf("at most %d clauses", MaxClauses)
	}
	for i, c := range cs {
		if !slices.Contains(Operators, c.Op) {
			return fmt.Errorf("clause %d: op is one of %s, not %q", i+1, strings.Join(Operators, ", "), c.Op)
		}
		if c.Op != OpInSegment && !validAttr(c.Attribute) {
			return fmt.Errorf("clause %d: say which attribute of the context it looks at", i+1)
		}
		if len(c.Values) > MaxValues {
			return fmt.Errorf("clause %d: at most %d values", i+1, MaxValues)
		}
		if c.Op != OpExists && len(c.Values) == 0 {
			return fmt.Errorf("clause %d: %s needs at least one value", i+1, c.Op)
		}
		for _, v := range c.Values {
			switch x := Normalize(v).(type) {
			case string:
				if len(x) > MaxStringBytes {
					return fmt.Errorf("clause %d: a value is at most %d bytes", i+1, MaxStringBytes)
				}
				if strings.HasPrefix(c.Op, "semver_") {
					if _, ok := parseSemver(x); !ok {
						return fmt.Errorf("clause %d: %q is not a version (major.minor.patch)", i+1, x)
					}
				}
				if strings.HasPrefix(c.Op, "number_") {
					return fmt.Errorf("clause %d: %s compares numbers, and %q is text", i+1, c.Op, x)
				}
			case float64, bool:
				if strings.HasPrefix(c.Op, "semver_") || c.Op == OpPrefix || c.Op == OpSuffix || c.Op == OpContains {
					return fmt.Errorf("clause %d: %s takes text", i+1, c.Op)
				}
				if _, isBool := x.(bool); isBool && strings.HasPrefix(c.Op, "number_") {
					return fmt.Errorf("clause %d: %s compares numbers", i+1, c.Op)
				}
			default:
				return fmt.Errorf("clause %d: a value is text, a number or a boolean", i+1)
			}
		}
	}
	return nil
}

// CheckSegment checks a segment.
func CheckSegment(name string, s Segment) error {
	if err := CheckSegmentName(name); err != nil {
		return err
	}
	if len(s.Included)+len(s.Excluded) > MaxSegmentKeys {
		return fmt.Errorf("a segment lists at most %d keys; use a rule on an attribute instead", MaxSegmentKeys)
	}
	if len(s.Rules) > MaxRules {
		return fmt.Errorf("at most %d rules", MaxRules)
	}
	for i, r := range s.Rules {
		if len(r.Clauses) == 0 {
			return fmt.Errorf("rule %d of the segment has no clauses: it would match everyone", i+1)
		}
		for _, c := range r.Clauses {
			if c.Op == OpInSegment {
				return fmt.Errorf("rule %d: a segment does not name another segment", i+1)
			}
		}
		if err := checkClauses(r.Clauses); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

// PrerequisiteLoop returns a loop of prerequisites among the toggles, as the
// keys in order, or nil.
func PrerequisiteLoop(flags map[string]Flag) []string {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var path []string
	var walk func(k string) []string
	walk = func(k string) []string {
		switch state[k] {
		case visiting:
			i := slices.Index(path, k)
			return append(slices.Clone(path[i:]), k)
		case done:
			return nil
		}
		state[k] = visiting
		path = append(path, k)
		f := flags[k]
		for _, p := range f.Prerequisites {
			if loop := walk(p.Toggle); loop != nil {
				return loop
			}
		}
		path = path[:len(path)-1]
		state[k] = done
		return nil
	}
	keys := make([]string, 0, len(flags))
	for k := range flags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if loop := walk(k); loop != nil {
			return loop
		}
	}
	return nil
}

// Settled reports whether a state is on and hands the same value to everyone,
// and what that value is, as JSON text: no rules, no rollout, no
// prerequisites. A toggle that stays settled for long is stale.
func Settled(s State) (string, bool) {
	if !s.On {
		return "", false
	}
	if len(s.Rules) > 0 || s.Fallthrough.Rollout != nil || len(s.Prerequisites) > 0 {
		return "", false
	}
	b, _ := json.Marshal(s.Fallthrough.Value)
	return string(b), true
}
