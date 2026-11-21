package settings

// Variables at levels (doc: docs/guide/variables.md). A variable is a name
// with a type and a value, set at a level and optionally for one
// environment; the nearest level wins, and a lock set at a level binds
// everything below it. This file is the model and its checks; the rest is
// variables_store.go (keeping and auditing), variables_resolve.go (the
// rule) and variables_service.go (what the interfaces call).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Level is where a variable is set. Its words in the installation's chosen
// terms are for display only; these are the stable names.
type Level string

const (
	// LevelInstallation is the whole installation (the base).
	LevelInstallation Level = "installation"
	// LevelOrganization is the organization (the wing).
	LevelOrganization Level = "organization"
	// LevelUnit is one unit (a squadron).
	LevelUnit Level = "unit"
	// LevelProject is one project.
	LevelProject Level = "project"
)

// Levels, from the widest to the nearest.
var variableLevels = []Level{LevelInstallation, LevelOrganization, LevelUnit, LevelProject}

// VariableLevels returns the levels, from the widest to the nearest.
func VariableLevels() []Level { return append([]Level(nil), variableLevels...) }

// rank is how near a level is to a run: the larger, the nearer.
func (l Level) rank() int {
	for i, x := range variableLevels {
		if x == l {
			return i
		}
	}
	return -1
}

// Valid reports whether l is one of the levels.
func (l Level) Valid() bool { return l.rank() >= 0 }

// DefaultOrg is the one organization there is today. Variables carry the
// organization they belong to, so that none can cross to another.
const DefaultOrg = "default"

// VarType is the type of a variable. It is fixed by the first level that
// sets the name.
type VarType string

// The types, in the words files use.
const (
	VarString     VarType = "string"
	VarNumber     VarType = "number"
	VarBoolean    VarType = "boolean"
	VarList       VarType = "list"
	VarMap        VarType = "map"
	VarSecret     VarType = "secret"
	VarConnection VarType = "connection"
)

// VarTypes lists the types, in the order they are documented.
var VarTypes = []VarType{VarString, VarNumber, VarBoolean, VarList, VarMap, VarSecret, VarConnection}

// Limits.
const (
	MaxVariableValueBytes = 4096
	MaxVariableItems      = 100
	MaxVariableDescLen    = 200
	MaxLockReasonLen      = 300
)

var (
	variableNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	variableEnvRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	variableOfRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	variableKeyRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,62}$`)
	variableScopeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// variableProblem is a problem with a variable a caller can fix (an invalid
// request to the API).
func variableProblem(format string, a ...any) error {
	return fmt.Errorf("%w: %s", engine.ErrInvalid, fmt.Sprintf(format, a...))
}

// Lock says that nobody below the level may change the variable.
type Lock struct {
	Reason string `json:"reason"`
	By     string `json:"by"`
	At     int64  `json:"at"`
}

// Variable is one variable set at one level, for everything or for one
// environment.
type Variable struct {
	Level Level `json:"level"`
	// Scope is the unit's or the project's name; empty at the installation,
	// and the organization's name at the organization.
	Scope string `json:"scope"`
	Name  string `json:"name"`
	// Environment is "" for the general variable.
	Environment string  `json:"environment,omitempty"`
	Type        VarType `json:"type"`
	// Of is the kind of outside system a connection names (registry,
	// cluster, ...); empty for other types.
	Of          string          `json:"of,omitempty"`
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description,omitempty"`
	Lock        *Lock           `json:"lock,omitempty"`
	Version     int             `json:"version"`
	CreatedAt   int64           `json:"created_at"`
	CreatedBy   string          `json:"created_by,omitempty"`
	UpdatedAt   int64           `json:"updated_at"`
	UpdatedBy   string          `json:"updated_by,omitempty"`
}

// VarKey identifies a variable at a level.
type VarKey struct {
	Level       Level  `json:"level"`
	Scope       string `json:"scope"`
	Name        string `json:"name"`
	Environment string `json:"environment,omitempty"`
}

// Key returns the variable's key.
func (v Variable) Key() VarKey {
	return VarKey{Level: v.Level, Scope: v.Scope, Name: v.Name, Environment: v.Environment}
}

func (k VarKey) String() string {
	s := string(k.Level)
	if k.Scope != "" {
		s += " " + k.Scope
	}
	s += ": " + k.Name
	if k.Environment != "" {
		s += " (for " + k.Environment + ")"
	}
	return s
}

// ScopeFor returns the scope a level takes: none for the installation, the
// organization's name for the organization, and the name given for the others.
func ScopeFor(l Level, name string) (string, error) {
	switch l {
	case LevelInstallation:
		if name != "" {
			return "", variableProblem("the installation has no name to give")
		}
		return "", nil
	case LevelOrganization:
		if name == "" || name == DefaultOrg {
			return DefaultOrg, nil
		}
		return "", variableProblem("there is one organization, %s", DefaultOrg)
	case LevelUnit, LevelProject:
		if !variableScopeRe.MatchString(name) {
			return "", variableProblem("say which %s: its name is lower-case letters, digits and -", l)
		}
		return name, nil
	}
	return "", variableProblem("%q is not a level; the levels are installation, organization, unit and project", string(l))
}

// CheckKey checks the names of a key.
func CheckKey(k VarKey) error {
	if _, err := ScopeFor(k.Level, k.Scope); err != nil {
		return err
	}
	if !variableNameRe.MatchString(k.Name) {
		return variableProblem("%q is not a valid variable name: lower-case letters, digits and _, starting with a letter (at most 40)", clip(k.Name, 60))
	}
	if k.Environment != "" && !variableEnvRe.MatchString(k.Environment) {
		return variableProblem("%q is not a valid environment name", clip(k.Environment, 60))
	}
	return nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}

// ValidType reports whether t is a type of variable.
func ValidType(t VarType) bool {
	for _, x := range VarTypes {
		if x == t {
			return true
		}
	}
	return false
}

// SecretRef is the value of a secret variable: where the runner finds the
// secret, never the secret.
type SecretRef struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	Field string `json:"field,omitempty"`
}

// String is the reference in the words people read.
func (r SecretRef) String() string {
	s := r.Store + ":" + r.Key
	if r.Field != "" {
		s += "#" + r.Field
	}
	return s
}

// CheckValue checks a value against a type and returns it in canonical
// form (compact JSON, map keys sorted).
func CheckValue(t VarType, of string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > MaxVariableValueBytes*4 {
		return nil, variableProblem("the value is too long")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, variableProblem("the value is not valid JSON: %v", err)
	}
	if dec.More() {
		return nil, variableProblem("the value is more than one JSON value")
	}
	if of != "" && t != VarConnection {
		return nil, variableProblem("of is for connections; this is a %s", t)
	}
	return checkTyped(t, of, v)
}

// checkTyped checks a decoded value against its type.
func checkTyped(t VarType, of string, v any) (json.RawMessage, error) {
	switch t {
	case VarString:
		s, ok := v.(string)
		if !ok {
			return nil, variableProblem("a string variable takes text; got %s", kindName(v))
		}
		if len(s) > MaxVariableValueBytes || !utf8.ValidString(s) {
			return nil, variableProblem("text is at most %d bytes", MaxVariableValueBytes)
		}
	case VarNumber:
		n, ok := v.(json.Number)
		if !ok {
			return nil, variableProblem("a number variable takes a number; got %s", kindName(v))
		}
		if _, err := n.Float64(); err != nil {
			return nil, variableProblem("%s is not a number Launchway can hold", n)
		}
	case VarBoolean:
		if _, ok := v.(bool); !ok {
			return nil, variableProblem("a boolean variable takes true or false; got %s", kindName(v))
		}
	case VarList:
		l, ok := v.([]any)
		if !ok {
			return nil, variableProblem("a list variable takes a list; got %s", kindName(v))
		}
		if len(l) > MaxVariableItems {
			return nil, variableProblem("a list holds at most %d items", MaxVariableItems)
		}
		for _, x := range l {
			if err := scalarOK(x); err != nil {
				return nil, err
			}
		}
	case VarMap:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, variableProblem("a map variable takes a map of names to values; got %s", kindName(v))
		}
		if len(m) > MaxVariableItems {
			return nil, variableProblem("a map holds at most %d entries", MaxVariableItems)
		}
		for k, x := range m {
			if !variableKeyRe.MatchString(k) {
				return nil, variableProblem("%q is not a valid key of a map", clip(k, 60))
			}
			if err := scalarOK(x); err != nil {
				return nil, err
			}
		}
	case VarSecret:
		return canonSecret(v)
	case VarConnection:
		if !variableOfRe.MatchString(of) {
			return nil, variableProblem("a connection says what it is a connection to (of: registry, cluster, ...)")
		}
		s, ok := v.(string)
		if !ok || !variableScopeRe.MatchString(s) {
			return nil, variableProblem("a connection variable names a connection: lower-case letters, digits and -")
		}
	default:
		return nil, variableProblem("%q is not a type; the types are %s", string(t), typeNames())
	}
	return canonJSON(v)
}

func typeNames() string {
	var s []string
	for _, t := range VarTypes {
		s = append(s, string(t))
	}
	return strings.Join(s, ", ")
}

func kindName(v any) string {
	switch v.(type) {
	case string:
		return "text"
	case json.Number:
		return "a number"
	case bool:
		return "true or false"
	case []any:
		return "a list"
	case map[string]any:
		return "a map"
	case nil:
		return "nothing"
	}
	return "something else"
}

func scalarOK(x any) error {
	switch t := x.(type) {
	case string:
		if len(t) > MaxVariableValueBytes {
			return variableProblem("an item is at most %d bytes", MaxVariableValueBytes)
		}
		return nil
	case json.Number, bool:
		return nil
	}
	return variableProblem("lists and maps hold text, numbers and true or false, not %s", kindName(x))
}

// canonSecret takes a reference as an object and refuses text: a secret is
// only ever a reference to where a runner finds it.
func canonSecret(v any) (json.RawMessage, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, variableProblem("a secret variable is a reference, not the secret: { store: vault, key: apps/x, field: token }")
	}
	var r SecretRef
	for k, x := range m {
		s, ok := x.(string)
		if !ok {
			return nil, variableProblem("secret.%s is text", k)
		}
		switch k {
		case "store":
			r.Store = s
		case "key":
			r.Key = s
		case "field":
			r.Field = s
		default:
			return nil, variableProblem("a secret reference has store, key and field; not %q", clip(k, 40))
		}
	}
	if !variableScopeRe.MatchString(r.Store) {
		return nil, variableProblem("secret.store names a secret store: lower-case letters, digits and -")
	}
	for name, s := range map[string]string{"key": r.Key, "field": r.Field} {
		if s == "" && name == "field" {
			continue
		}
		if s == "" || len(s) > 200 || strings.ContainsAny(s, "\r\n\t ") {
			return nil, variableProblem("secret.%s is one word of at most 200 characters", name)
		}
	}
	return canonJSON(r)
}

func canonJSON(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v) // map keys are sorted by encoding/json
	if err != nil {
		return nil, variableProblem("%v", err)
	}
	return b, nil
}

// DisplayValue is a value in the words people read: text as it is, a list
// as "a, b", a secret as the reference (never the secret, which Launchway
// does not hold).
func DisplayValue(t VarType, raw json.RawMessage) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return string(raw)
	}
	switch t {
	case VarString, VarConnection:
		if s, ok := v.(string); ok {
			return s
		}
	case VarSecret:
		var r SecretRef
		if json.Unmarshal(raw, &r) == nil {
			return r.String()
		}
	case VarList:
		if l, ok := v.([]any); ok {
			parts := make([]string, len(l))
			for i, x := range l {
				parts[i] = fmt.Sprint(x)
			}
			return strings.Join(parts, ", ")
		}
	case VarMap:
		if m, ok := v.(map[string]any); ok {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = fmt.Sprintf("%s=%v", k, m[k])
			}
			return strings.Join(parts, ", ")
		}
	}
	return fmt.Sprint(v)
}

// Text is the value as a pipeline input takes it: text as it is, a list as
// a JSON array, a boolean as true or false, a number in its shortest form.
// A secret has no text: it is a reference a runner follows.
func Text(t VarType, raw json.RawMessage) string {
	switch t {
	case VarList, VarMap, VarSecret:
		return string(raw)
	}
	return DisplayValue(t, raw)
}

// VariableChange is one entry of the audit of variables.
type VariableChange struct {
	ID     int64  `json:"id"`
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	VarKey        // level, scope, name, environment
	Action string `json:"action"` // set, delete, lock, unlock
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
	Detail string `json:"detail,omitempty"`
}
