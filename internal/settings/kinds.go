// Package settings keeps the configuration an administrator manages in the
// database (schema 23): projects, libraries, environments, runner pools,
// role bindings, policies, health sources, models, secret-store
// definitions, units and the installation's settings. Each is an object
// with a version, a history and an audit trail, changed through one API
// that the UI, the CLI and the MCP server all use, and checked by the same
// code that checks the configuration file (config.Config.Validate), so the
// two can never disagree about what is valid.
//
// The file the server starts with keeps only what is needed before the
// database can be read (see Bootstrap). On the first start of a server of
// this schema, the file's other settings are imported once; after that they
// are ignored, with a warning naming each one, and the database is the only
// source of truth.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Kind describes one kind of configuration object.
type Kind struct {
	// Name is the kind as stored and in the object's kind field: project.
	Name string `json:"name"`
	// Path names it in URLs (/api/v1/config/projects) and on the command
	// line (launchway config list projects): projects.
	Path string `json:"path"`
	// Title and Plural are the names people read.
	Title  string `json:"title"`
	Plural string `json:"plural"`
	// Singleton is the name of the only object of the kind, for the
	// installation's settings and the organisation; "" for the others.
	Singleton string `json:"singleton,omitempty"`
	// Levels: the object may be defined at a unit, and then applies only
	// to the projects of that unit. For projects, the unit is the unit the
	// project is in.
	Levels bool `json:"levels"`
	// Doc says in a sentence what it is.
	Doc string `json:"doc"`
	// FileKey is where the configuration file kept it, for messages and
	// for the export ("projects", "policy.sources", ...).
	FileKey string `json:"file_key"`

	// spec returns a new value to read a spec into.
	spec func() any
	// named: the spec type has a name field, which is the object's name.
	named bool
}

// The kinds, in the order they are applied and exported: what an object
// names comes before it.
var kinds = []*Kind{
	{Name: "installation", Path: "installation", Title: "Installation settings", Plural: "Installation settings", Singleton: "installation",
		FileKey: "(approvals, drafts, pull_requests, schedules, loops, tests, findings, reports, artifacts limits, runner_admin, policy.waivers, auth.tokens)",
		Doc:     "Settings of the whole installation: what the levels are called, approvals, drafts, pull requests, schedules, limits and retention.",
		spec:    func() any { return &Installation{} }},
	{Name: "organisation", Path: "organisation", Title: "Organisation", Plural: "Organisations", Singleton: "default",
		Doc:  "The organisation that owns the projects. There is one today; its display name is shown where it helps.",
		spec: func() any { return &Organisation{} }},
	{Name: "unit", Path: "units", Title: "Unit", Plural: "Units", FileKey: "",
		Doc:  "A group of projects with shared settings: roles, libraries, policies, environments, runner pools and secret stores defined at a unit apply to its projects.",
		spec: func() any { return &Unit{} }},
	{Name: "library", Path: "libraries", Title: "Library", Plural: "Libraries", Levels: true, FileKey: "libraries", named: true,
		Doc:  "A Git repository of templates and scripts that pipelines may use by name.",
		spec: func() any { return &config.LibraryConfig{} }},
	{Name: "project", Path: "projects", Title: "Project", Plural: "Projects", Levels: true, FileKey: "projects", named: true,
		Doc:  "A repository Launchway watches: its branch, how runs start, its pipeline (a file, or a library template) and what its runs may do.",
		spec: func() any { return &config.ProjectConfig{} }},
	{Name: "environment", Path: "environments", Title: "Environment", Plural: "Environments", Levels: true, FileKey: "environments", named: true,
		Doc:  "Where deploy steps may deploy: its target, which projects may deploy there, its queue and whether it is protected.",
		spec: func() any { return &config.EnvironmentConfig{} }},
	{Name: "runner-pool", Path: "runner-pools", Title: "Runner pool", Plural: "Runner pools", Levels: true, FileKey: "runner_pools", named: true,
		Doc:  "A group of runners: a build pool serves projects, a deploy pool serves targets.",
		spec: func() any { return &config.RunnerPoolConfig{} }},
	{Name: "role-binding", Path: "role-bindings", Title: "Role binding", Plural: "Role bindings", Levels: true, FileKey: "auth.access",
		Doc:  "Gives a user or a group a role (viewer, operator or admin) on projects, optionally only for some environments.",
		spec: func() any { return &config.AccessBinding{} }},
	{Name: "policy", Path: "policies", Title: "Policy", Plural: "Policies", Levels: true, FileKey: "policy.policies",
		Doc:  "A delivery policy: rules runs must meet before they may deploy or finish, enforced or only reported.",
		spec: func() any { return &yaml.Node{} }},
	{Name: "policy-source", Path: "policy-sources", Title: "Policy source", Plural: "Policy sources", Levels: true, FileKey: "policy.sources", named: true,
		Doc:  "A library whose files hold policies, at a pinned version.",
		spec: func() any { return &config.PolicySourceConfig{} }},
	{Name: "health-source", Path: "health-sources", Title: "Health source", Plural: "Health sources", Levels: true, FileKey: "verification.sources", named: true,
		Doc:  "A metrics or logs system verify steps may read, by name; where it is and its credentials stay with the deploy runners.",
		spec: func() any { return &config.HealthSourceConfig{} }},
	{Name: "model", Path: "models", Title: "AI model", Plural: "AI models", Levels: true, FileKey: "ai.models", named: true,
		Doc:  "A model steps may ask, and which projects may use it; its key is a reference to a secret store, never a value.",
		spec: func() any { return &ai.Model{} }},
	{Name: "secret-store", Path: "secret-stores", Title: "Secret store", Plural: "Secret stores", Levels: true,
		Doc:  "The definition of a secret store pipelines may name: its type and which projects may use it. Its address and credentials stay with the runners.",
		spec: func() any { return &SecretStore{} }},
}

// Kinds returns the kinds, in order.
func Kinds() []*Kind { return slices.Clone(kinds) }

// KindByPath finds a kind by its path or its name.
func KindByPath(s string) *Kind {
	for _, k := range kinds {
		if k.Path == s || k.Name == s {
			return k
		}
	}
	return nil
}

func kindByName(s string) *Kind {
	for _, k := range kinds {
		if k.Name == s {
			return k
		}
	}
	return nil
}

func kindOrder(name string) int {
	for i, k := range kinds {
		if k.Name == name {
			return i
		}
	}
	return len(kinds)
}

// Installation is the installation's settings (kind installation).
type Installation struct {
	// Levels names the levels; empty means the built-in names.
	Levels       Levels                    `yaml:"levels"`
	Approvals    config.ApprovalsConfig    `yaml:"approvals"`
	Drafts       config.DraftsConfig       `yaml:"drafts"`
	PullRequests config.PullRequestsConfig `yaml:"pull_requests"`
	Schedules    config.SchedulesConfig    `yaml:"schedules"`
	Loops        config.LoopsConfig        `yaml:"loops"`
	Tests        config.TestsConfig        `yaml:"tests"`
	Findings     config.FindingsConfig     `yaml:"findings"`
	// Reports are the limits of reports.* (reports.origin stays in the
	// file: it is part of how the server is reached).
	Reports config.ServerReportsConfig `yaml:"reports"`
	// Artifacts are the limits and retention of artifacts.* (where they
	// are kept stays in the file).
	Artifacts     ArtifactLimits             `yaml:"artifacts"`
	RunnerAdmin   string                     `yaml:"runner_admin"`
	PolicyWaivers config.PolicyWaiversConfig `yaml:"policy_waivers"`
	Tokens        config.TokensConfig        `yaml:"tokens"`
}

// ArtifactLimits are the artifacts settings that moved to the database.
type ArtifactLimits struct {
	MaxFileMiB    int    `yaml:"max_file_mib"`
	MaxRunMiB     int    `yaml:"max_run_mib"`
	MaxProjectGiB int    `yaml:"max_project_gib"`
	Retain        string `yaml:"retain"`
	MaxRetain     string `yaml:"max_retain"`
}

// Levels are the display names of the levels, singular and plural.
type Levels struct {
	Installation LevelName `yaml:"installation"`
	Organisation LevelName `yaml:"organisation"`
	Unit         LevelName `yaml:"unit"`
	Project      LevelName `yaml:"project"`
}

// LevelName is how a level is called.
type LevelName struct {
	Singular string `yaml:"singular"`
	Plural   string `yaml:"plural"`
}

// DefaultLevels are the built-in names.
var DefaultLevels = Levels{
	Installation: LevelName{"Installation", "Installations"},
	Organisation: LevelName{"Organisation", "Organisations"},
	Unit:         LevelName{"Unit", "Units"},
	Project:      LevelName{"Project", "Projects"},
}

// withDefaults fills in names left empty.
func (l Levels) withDefaults() Levels {
	fill := func(n *LevelName, d LevelName) {
		if n.Singular == "" {
			n.Singular = d.Singular
		}
		if n.Plural == "" {
			n.Plural = d.Plural
		}
	}
	fill(&l.Installation, DefaultLevels.Installation)
	fill(&l.Organisation, DefaultLevels.Organisation)
	fill(&l.Unit, DefaultLevels.Unit)
	fill(&l.Project, DefaultLevels.Project)
	return l
}

func (l Levels) check() error {
	for _, n := range []struct {
		key string
		v   LevelName
	}{{"installation", l.Installation}, {"organisation", l.Organisation}, {"unit", l.Unit}, {"project", l.Project}} {
		for _, s := range []string{n.v.Singular, n.v.Plural} {
			if len(s) > 40 || strings.ContainsAny(s, "<>\n\r\t") {
				return fmt.Errorf("levels.%s: %q: at most 40 characters, on one line, without < or >", n.key, s)
			}
		}
	}
	return nil
}

// Organisation is the organisation's settings (kind organisation).
type Organisation struct {
	DisplayName string `yaml:"display_name"`
	Description string `yaml:"description"`
}

// Unit is a unit (kind unit).
type Unit struct {
	DisplayName string `yaml:"display_name"`
	Description string `yaml:"description"`
}

// SecretStore is a secret-store definition (kind secret-store): what
// pipelines may name, never where it is or how to read it.
type SecretStore struct {
	// Type is vault, aws-secrets-manager, gcp-secret-manager,
	// azure-key-vault, kubernetes, env or file.
	Type        string   `yaml:"type"`
	Description string   `yaml:"description"`
	Projects    []string `yaml:"projects"`
}

// SecretStoreTypes are the types a definition may have.
var SecretStoreTypes = []string{"vault", "aws-secrets-manager", "gcp-secret-manager", "azure-key-vault", "kubernetes", "env", "file"}

var objectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidName reports whether s can name an object.
func ValidName(s string) bool { return objectName.MatchString(s) }

// decodeSpec reads a spec, given as JSON with the keys of the configuration
// file, into the kind's type, refusing keys the type does not have exactly
// as the file does. It returns the typed value and the spec as stored: the
// typed value written back, without its name and without settings left at
// their zero value.
func decodeSpec(k *Kind, name string, raw json.RawMessage) (any, json.RawMessage, error) {
	var g any
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&g); err != nil {
		return nil, nil, fmt.Errorf("the spec is not valid JSON: %w", err)
	}
	m, ok := g.(map[string]any)
	if !ok {
		return nil, nil, errors.New("the spec must be an object")
	}
	if n, ok := m["name"]; ok && hasName(k) {
		if s, _ := n.(string); s != name {
			return nil, nil, fmt.Errorf("the spec's name %q is not the object's name %q", n, name)
		}
	}
	if hasName(k) {
		m["name"] = name
	}
	y, err := yaml.Marshal(numbersForYAML(m))
	if err != nil {
		return nil, nil, err
	}
	v := k.spec()
	if node, ok := v.(*yaml.Node); ok {
		if err := yaml.Unmarshal(y, node); err != nil {
			return nil, nil, err
		}
		if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
			*node = *node.Content[0]
		}
	} else {
		yd := yaml.NewDecoder(bytes.NewReader(y))
		yd.KnownFields(true)
		if err := yd.Decode(v); err != nil {
			return nil, nil, fmt.Errorf("%s: %s", k.Title, strings.TrimPrefix(err.Error(), "yaml: "))
		}
	}
	stored, err := encodeSpec(k, v)
	if err != nil {
		return nil, nil, err
	}
	return v, stored, nil
}

func hasName(k *Kind) bool { return k.named || k.Name == "policy" }

// encodeSpec writes a typed spec as stored: the file's keys, without the
// name and without settings at their zero value (an unset pointer, an empty
// string, list or map, a zero number or false). A pointer that is set is
// written even when what it points to is empty, as push: {} must be.
func encodeSpec(k *Kind, v any) (json.RawMessage, error) {
	g, _, err := toGeneric(reflect.ValueOf(v))
	if err != nil {
		return nil, err
	}
	m, _ := g.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	if hasName(k) {
		delete(m, "name")
	}
	return json.Marshal(m)
}

var nodeType = reflect.TypeOf(yaml.Node{})

// toGeneric turns a value into maps, lists and scalars by its yaml tags. omit
// reports a value to leave out.
func toGeneric(v reflect.Value) (out any, omit bool, err error) {
	switch v.Kind() {
	case reflect.Invalid:
		return nil, true, nil
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, true, nil
		}
		g, _, err := toGeneric(v.Elem())
		if g == nil && v.Kind() == reflect.Pointer && v.Elem().Kind() == reflect.Struct {
			g = map[string]any{}
		}
		return g, false, err
	case reflect.Struct:
		if v.Type() == nodeType {
			n := v.Interface().(yaml.Node)
			if n.Kind == 0 {
				return nil, true, nil
			}
			y, err := yaml.Marshal(&n)
			if err != nil {
				return nil, false, err
			}
			var g any
			if err := yaml.Unmarshal(y, &g); err != nil {
				return nil, false, err
			}
			if g == nil && n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
				// pull_request: (no value) means the same as {}.
				return map[string]any{}, false, nil
			}
			return g, false, nil
		}
		m := map[string]any{}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "-" {
				continue
			}
			fv := v.Field(i)
			if strings.Contains(opts, "inline") {
				g, _, err := toGeneric(fv)
				if err != nil {
					return nil, false, err
				}
				if gm, ok := g.(map[string]any); ok {
					for k, x := range gm {
						m[k] = x
					}
				}
				continue
			}
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			if fv.Kind() != reflect.Pointer && fv.Kind() != reflect.Interface && fv.Type() != nodeType && fv.IsZero() {
				continue
			}
			g, omit, err := toGeneric(fv)
			if err != nil {
				return nil, false, err
			}
			if !omit {
				m[name] = g
			}
		}
		return m, false, nil
	case reflect.Map:
		if v.Len() == 0 {
			return nil, true, nil
		}
		m := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			g, _, err := toGeneric(iter.Value())
			if err != nil {
				return nil, false, err
			}
			if g == nil {
				g = map[string]any{}
			}
			m[fmt.Sprint(iter.Key().Interface())] = g
		}
		return m, false, nil
	case reflect.Slice, reflect.Array:
		if v.Len() == 0 {
			return nil, true, nil
		}
		out := make([]any, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			g, _, err := toGeneric(v.Index(i))
			if err != nil {
				return nil, false, err
			}
			out = append(out, g)
		}
		return out, false, nil
	}
	return v.Interface(), v.IsZero(), nil
}

// numbersForYAML turns json.Numbers into ints or floats, which YAML writes
// as plain numbers.
func numbersForYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = numbersForYAML(x)
		}
	case []any:
		for i, x := range t {
			t[i] = numbersForYAML(x)
		}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	}
	return v
}

// sortObjects orders objects by kind, then name.
func sortObjects(objs []engine.ConfigObject) {
	sort.SliceStable(objs, func(i, j int) bool {
		a, b := kindOrder(objs[i].Kind), kindOrder(objs[j].Kind)
		if a != b {
			return a < b
		}
		return objs[i].Name < objs[j].Name
	})
}
