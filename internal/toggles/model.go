// Package toggles is the model and the evaluator of feature toggles: what a
// toggle is, the rules that decide what value an application gets for a
// context, and the check that a definition is well formed. It has no
// database and no HTTP; internal/togglestore keeps toggles and
// internal/api serves them. The same rules are written again, with no
// dependency on this package, in sdk/go/toggles and sdk/js, and all three
// run the cases in sdk/vectors/evaluation.json, so that they agree.
//
// The evaluation protocol in one paragraph. A toggle has a type (boolean,
// string, number or json) and, in each environment, a state: on or off, the
// value served while off, prerequisites, rules in order and the value served
// when no rule matches (the fallthrough). Evaluating it for a context (a key
// that names who or what is asking, and attributes) goes: not found, then
// off, then prerequisites, then the first rule whose clauses all match, then
// the fallthrough. A rule or the fallthrough serves one value, or a
// percentage rollout: weights, in 1/100 of a percent, over values, applied to
// a bucket (0 to 9999) that is a hash of the toggle's salt and the context's
// bucketing attribute (see Bucket). The same person lands in the same bucket
// every time, so raising a rollout from 5% to 25% keeps the first 5%.
package toggles

import "encoding/json"

// ValueType is the type of the values a toggle serves.
type ValueType string

const (
	Boolean ValueType = "boolean"
	String  ValueType = "string"
	Number  ValueType = "number"
	JSON    ValueType = "json"
)

// Valid reports whether t is a type of toggle.
func (t ValueType) Valid() bool { return t == Boolean || t == String || t == Number || t == JSON }

// Kind says what a toggle is for. A kill switch is a toggle that turns a
// capability off in an emergency: it has a safe value, and turning it to that
// value is never held for an approval.
type Kind string

const (
	KindRelease    Kind = "release"
	KindKill       Kind = "kill"
	KindExperiment Kind = "experiment"
	KindOperations Kind = "operational"
	KindPermission Kind = "permission"
)

// Valid reports whether k is a kind.
func (k Kind) Valid() bool {
	switch k {
	case KindRelease, KindKill, KindExperiment, KindOperations, KindPermission:
		return true
	}
	return false
}

// Context is what an application says about who or what is asking: "key" is
// the identifier that percentage rollouts bucket by (a user, an account, a
// device; anything stable), and the other attributes are what rules look at.
// Values are strings, numbers, booleans or lists of those.
type Context map[string]any

// Clause is one condition of a rule: the attribute's value tested with an
// operator against Values. A clause on an attribute the context does not
// have is false, whatever the operator, and Negate turns it into true.
type Clause struct {
	Attribute string `json:"attribute,omitempty"`
	Op        string `json:"op"`
	Values    []any  `json:"values,omitempty"`
	Negate    bool   `json:"negate,omitempty"`
}

// The operators of a clause.
const (
	OpEquals    = "equals"     // the attribute equals the first value
	OpIn        = "in"         // the attribute equals any value
	OpPrefix    = "prefix"     // a string attribute starts with any value
	OpSuffix    = "suffix"     // ends with any value
	OpContains  = "contains"   // contains any value
	OpExists    = "exists"     // the context has the attribute
	OpInSegment = "in_segment" // the context is in any of the named segments (Attribute is not used)
	OpSemverEq  = "semver_eq"  // a version attribute equals the first value, by semantic versioning
	OpSemverLt  = "semver_lt"  // less than
	OpSemverLte = "semver_lte" // less than or equal
	OpSemverGt  = "semver_gt"  // greater than
	OpSemverGte = "semver_gte" // greater than or equal
	OpNumberEq  = "number_eq"  // a numeric attribute equals the first value
	OpNumberLt  = "number_lt"  // less than
	OpNumberLte = "number_lte" // less than or equal
	OpNumberGt  = "number_gt"  // greater than
	OpNumberGte = "number_gte" // greater than or equal
)

// Operators lists every operator.
var Operators = []string{OpEquals, OpIn, OpPrefix, OpSuffix, OpContains, OpExists, OpInSegment,
	OpSemverEq, OpSemverLt, OpSemverLte, OpSemverGt, OpSemverGte, OpNumberEq, OpNumberLt, OpNumberLte, OpNumberGt, OpNumberGte}

// Share is one share of a rollout: a value and its weight, in hundredths of
// a percent (2500 is 25%). The weights of a rollout add up to 10000.
type Share struct {
	Value  any `json:"value"`
	Weight int `json:"weight"`
}

// Rollout serves values by percentage. By names the context attribute the
// percentage is taken over ("key" when not written).
type Rollout struct {
	By      string  `json:"by,omitempty"`
	Buckets []Share `json:"buckets"`
}

// Serve is what a rule or the fallthrough hands out: one value, or a rollout.
type Serve struct {
	Value   any      `json:"value,omitempty"`
	Rollout *Rollout `json:"rollout,omitempty"`
}

// Rule serves when every clause matches.
type Rule struct {
	ID          string   `json:"id,omitempty"`
	Description string   `json:"description,omitempty"`
	Clauses     []Clause `json:"clauses,omitempty"`
	Serve       Serve    `json:"serve"`
}

// Prerequisite makes a toggle depend on another: unless Toggle serves Value
// in the same environment and context, the toggle serves its off value.
type Prerequisite struct {
	Toggle string `json:"toggle"`
	Value  any    `json:"value"`
}

// State is a toggle's behaviour in one environment.
type State struct {
	On            bool           `json:"on"`
	OffValue      any            `json:"off_value"`
	Prerequisites []Prerequisite `json:"prerequisites,omitempty"`
	Rules         []Rule         `json:"rules,omitempty"`
	Fallthrough   Serve          `json:"fallthrough"`
}

// Flag is a toggle as an application sees it in one environment: the
// definition's type and salt, with the environment's state. It is the unit of
// a Ruleset.
type Flag struct {
	Key           string    `json:"key"`
	Type          ValueType `json:"type"`
	Kind          Kind      `json:"kind,omitempty"`
	ClientVisible bool      `json:"client_visible,omitempty"`
	Salt          string    `json:"salt"`
	State
}

// SegmentRule is a group of clauses that all must match.
type SegmentRule struct {
	Clauses []Clause `json:"clauses"`
}

// Segment is a named, reusable set of rules. A context is in it when its key
// is not excluded and is included, or when any rule matches. An included or
// excluded key is a person's identifier and can be personal data: prefer
// rules, and see docs/guide/feature-toggles.md.
type Segment struct {
	Description string        `json:"description,omitempty"`
	Included    []string      `json:"included,omitempty"`
	Excluded    []string      `json:"excluded,omitempty"`
	Rules       []SegmentRule `json:"rules,omitempty"`
}

// SchemaVersion is the version of the ruleset format.
const SchemaVersion = 1

// Ruleset is everything an application needs to evaluate the toggles of one
// environment on its own: this is what a server SDK downloads.
type Ruleset struct {
	Schema      int                `json:"schema"`
	Environment string             `json:"environment"`
	Toggles     map[string]Flag    `json:"toggles"`
	Segments    map[string]Segment `json:"segments,omitempty"`
}

// Result is the outcome of an evaluation.
type Result struct {
	Value  any    `json:"value"`
	Reason string `json:"reason"`
	RuleID string `json:"rule_id,omitempty"`
}

// The reasons of a Result.
const (
	ReasonNotFound     = "not_found"           // no such toggle: the caller's default applies
	ReasonOff          = "off"                 // the toggle is off in this environment
	ReasonPrerequisite = "prerequisite_failed" // a prerequisite did not serve its value
	ReasonRule         = "rule_match"
	ReasonFallthrough  = "fallthrough"
	ReasonNoBucketKey  = "missing_bucket_key" // a rollout could not bucket: the context lacks the attribute
	ReasonError        = "error"              // the ruleset is malformed (a loop of prerequisites, an unknown segment)
)

// Normalize makes a decoded value comparable: numbers of every Go type become
// float64, so that a context built in Go and one decoded from JSON agree.
func Normalize(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int8:
		return float64(n)
	case int16:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint8:
		return float64(n)
	case uint16:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	case float32:
		return float64(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return n.String()
		}
		return f
	}
	return v
}
