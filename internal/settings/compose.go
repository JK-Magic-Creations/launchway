package settings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Layout is what the levels look like: the units, which project is in
// which, the names of the levels, and where each object is defined.
type Layout struct {
	Levels       Levels            `json:"levels"`
	Organisation Organisation      `json:"organisation"`
	Units        []UnitView        `json:"units"`
	ProjectUnit  map[string]string `json:"project_unit"`
	// UnitsInUse: some unit exists, so the UI shows the level.
	UnitsInUse bool `json:"units_in_use"`
}

// UnitView is a unit and what is in it.
type UnitView struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description,omitempty"`
	Projects    []string `json:"projects"`
}

// noProject is a project pattern that matches no project (names cannot
// start with a hyphen): what an object defined at a unit with no projects
// is narrowed to.
const noProject = "-"

// Compose makes the configuration the server runs with: the bootstrap
// settings of the file it started with, and everything else from the
// objects (deleted ones are skipped). Objects defined at a unit are
// narrowed to the projects of that unit. It does not check the result;
// Check does.
func Compose(boot *config.Config, objs []engine.ConfigObject) (*config.Config, *Layout, error) {
	c := Bootstrap(boot)
	lay := &Layout{Levels: DefaultLevels, ProjectUnit: map[string]string{}, Units: []UnitView{}}
	live := make([]engine.ConfigObject, 0, len(objs))
	for _, o := range objs {
		// Objects of a kind this version does not know were written by a
		// newer version; they are ignored (the service warns).
		if !o.Deleted() && kindByName(o.Kind) != nil {
			live = append(live, o)
		}
	}
	sortObjects(live)
	typed, units, err := decodeAll(live, lay)
	if err != nil {
		return nil, nil, err
	}
	for name, unit := range lay.ProjectUnit {
		if u := units[unit]; unit != "" && u != nil {
			u.Projects = append(u.Projects, name)
		}
	}
	for _, u := range units {
		sort.Strings(u.Projects) // objects narrowed to a unit read them in this order
	}
	var policies yaml.Node
	policies.Kind = yaml.SequenceNode
	var errs []error
	for i, o := range live {
		if err := composeObject(c, lay, &policies, units, o, typed[i]); err != nil {
			errs = append(errs, err)
		}
	}
	if len(policies.Content) > 0 {
		ensurePolicy(c).Policies = policies
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(units))
	for n := range units {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		u := units[n]
		sort.Strings(u.Projects)
		lay.Units = append(lay.Units, *u)
	}
	lay.UnitsInUse = len(lay.Units) > 0
	return c, lay, nil
}

// decodeAll reads every object's spec, and notes the units and which unit
// each project is in.
func decodeAll(live []engine.ConfigObject, lay *Layout) ([]any, map[string]*UnitView, error) {
	units := map[string]*UnitView{}
	var errs []error
	typed := make([]any, len(live))
	for i, o := range live {
		k := kindByName(o.Kind)
		v, _, err := decodeSpec(k, o.Name, o.Spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", k.Name, o.Name, err))
			continue
		}
		typed[i] = v
		switch o.Kind {
		case "unit":
			u := v.(*Unit)
			units[o.Name] = &UnitView{Name: o.Name, DisplayName: cmpOr(u.DisplayName, o.Name), Description: u.Description, Projects: []string{}}
		case "project":
			lay.ProjectUnit[o.Name] = o.Unit
		}
	}
	return typed, units, errors.Join(errs...)
}

// composeObject puts one object into the configuration, narrowed to its
// unit's projects if it is defined at a unit.
func composeObject(c *config.Config, lay *Layout, policies *yaml.Node, units map[string]*UnitView, o engine.ConfigObject, v any) error {
	k := kindByName(o.Kind)
	if o.Unit != "" {
		if !k.Levels {
			return fmt.Errorf("%s %s: a %s cannot be defined at a unit", k.Name, o.Name, strings.ToLower(k.Title))
		}
		if units[o.Unit] == nil {
			return fmt.Errorf("%s %s: unit %q does not exist", k.Name, o.Name, o.Unit)
		}
	}
	scoped := o.Unit != "" && o.Kind != "project"
	var unitProjects []string
	if scoped {
		unitProjects = units[o.Unit].Projects
	}
	narrow := func(ps []string) []string { return narrowTo(ps, unitProjects, scoped) }
	switch t := v.(type) {
	case *Installation:
		applyInstallation(c, t)
		lay.Levels = t.Levels.withDefaults()
	case *Organisation:
		lay.Organisation = *t
	case *config.LibraryConfig:
		c.Libraries = append(c.Libraries, *t)
	case *config.ProjectConfig:
		c.Projects = append(c.Projects, *t)
	case *config.EnvironmentConfig:
		t.Projects = narrow(t.Projects)
		c.Environments = append(c.Environments, *t)
	case *config.RunnerPoolConfig:
		if t.Kind == engine.PoolBuild {
			t.Projects = narrow(t.Projects)
		}
		c.RunnerPools = append(c.RunnerPools, *t)
	case *config.AccessBinding:
		t.Projects = narrow(t.Projects)
		c.Auth.Access = append(c.Auth.Access, *t)
	case *yaml.Node:
		if o.Unit != "" {
			narrowPolicy(t, unitProjects)
		}
		policies.Content = append(policies.Content, t)
	case *config.PolicySourceConfig:
		t.Projects = narrow(t.Projects)
		ensurePolicy(c).Sources = append(ensurePolicy(c).Sources, *t)
	case *config.HealthSourceConfig:
		t.Projects = narrow(t.Projects)
		c.Verification.Sources = append(c.Verification.Sources, *t)
	case *ai.Model:
		t.Projects = narrow(t.Projects)
		c.AI.Models = append(c.AI.Models, *t)
	}
	// Units, secret-store definitions and the sign-in settings are saved and
	// shown; nothing in the running server reads them (see SignIn).
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func ensurePolicy(c *config.Config) *config.PolicyConfig {
	if c.Policy == nil {
		c.Policy = &config.PolicyConfig{}
	}
	return c.Policy
}

// narrowTo limits project patterns to the projects of a unit: the unit's
// projects that match one of them (or all of the unit's, if there are no
// patterns). A unit with none gets a pattern that matches nothing.
func narrowTo(patterns, unitProjects []string, scoped bool) []string {
	if !scoped {
		return patterns
	}
	var out []string
	for _, p := range unitProjects {
		if len(patterns) == 0 || matchAny(patterns, p) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{noProject}
	}
	return out
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// narrowPolicy limits a policy written at a unit to the unit's projects, in
// its scope.projects.
func narrowPolicy(n *yaml.Node, unitProjects []string) {
	if n.Kind != yaml.MappingNode {
		return
	}
	var scope *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "scope" {
			scope = n.Content[i+1]
		}
	}
	if scope == nil {
		scope = &yaml.Node{Kind: yaml.MappingNode}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "scope"}, scope)
	}
	if scope.Kind != yaml.MappingNode {
		return // the policy parser reports it
	}
	var given []string
	idx := -1
	for i := 0; i+1 < len(scope.Content); i += 2 {
		if scope.Content[i].Value == "projects" {
			idx = i + 1
			for _, x := range scope.Content[i+1].Content {
				given = append(given, x.Value)
			}
		}
	}
	list := &yaml.Node{Kind: yaml.SequenceNode}
	for _, p := range narrowTo(given, unitProjects, true) {
		list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: p, Style: yaml.DoubleQuotedStyle})
	}
	if idx >= 0 {
		scope.Content[idx] = list
	} else {
		scope.Content = append(scope.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "projects"}, list)
	}
}

func applyInstallation(c *config.Config, s *Installation) {
	c.Approvals = s.Approvals
	c.Drafts = s.Drafts
	c.PullRequests = s.PullRequests
	c.Schedules = s.Schedules
	c.Loops = s.Loops
	c.Tests = s.Tests
	c.Findings = s.Findings
	origin := c.Reports.Origin
	c.Reports = s.Reports
	if s.Reports.Origin == "" {
		c.Reports.Origin = origin
	}
	c.Artifacts.MaxFileMiB = s.Artifacts.MaxFileMiB
	c.Artifacts.MaxRunMiB = s.Artifacts.MaxRunMiB
	c.Artifacts.MaxProjectGiB = s.Artifacts.MaxProjectGiB
	c.Artifacts.Retain = s.Artifacts.Retain
	c.Artifacts.MaxRetain = s.Artifacts.MaxRetain
	c.Artifacts.Store, c.Artifacts.Database, c.Artifacts.Dir, c.Artifacts.S3 = s.Artifacts.Store, s.Artifacts.Database, s.Artifacts.Dir, s.Artifacts.S3
	c.MCP = s.MCP
	c.Egress.Allow = s.Egress.Allow
	c.History = s.History
	c.RunnerAdmin = s.RunnerAdmin
	if s.PolicyWaivers != (config.PolicyWaiversConfig{}) {
		ensurePolicy(c).Waivers = s.PolicyWaivers
	}
	c.Auth.Tokens = s.Tokens
	c.Forge = s.Forge
}

// Bootstrap returns a copy of a configuration with only what the file keeps
// since schema 23: everything that moved to the database is cleared.
func Bootstrap(boot *config.Config) *config.Config {
	c := *boot
	c.Projects = nil
	c.Libraries = nil
	c.Approvals = config.ApprovalsConfig{}
	c.Drafts = config.DraftsConfig{}
	c.PullRequests = config.PullRequestsConfig{}
	c.Tests = config.TestsConfig{}
	c.Findings = config.FindingsConfig{}
	c.Reports = config.ServerReportsConfig{Origin: boot.Reports.Origin}
	c.Artifacts.MaxFileMiB, c.Artifacts.MaxRunMiB, c.Artifacts.MaxProjectGiB = 0, 0, 0
	c.Artifacts.Retain, c.Artifacts.MaxRetain = "", ""
	c.Artifacts.Store, c.Artifacts.Database, c.Artifacts.Dir, c.Artifacts.S3 = "", nil, "", nil
	c.MCP = config.MCPConfig{}
	c.Egress.Allow = nil
	c.History = config.HistoryConfig{}
	c.Loops = config.LoopsConfig{}
	c.Schedules = config.SchedulesConfig{}
	c.Environments = nil
	c.RunnerPools = nil
	c.RunnerAdmin = ""
	c.Policy = nil
	c.Verification = config.VerificationConfig{}
	c.AI.Models = nil
	c.Auth.Access = nil
	c.Auth.Tokens = config.TokensConfig{}
	c.Forge = config.ForgeConfig{}
	c.Auth.SetAccessInDatabase(false)
	return &c
}

// FromConfig turns the settings of a configuration file that moved to the
// database into objects: what the one-time import and launchway config
// import store. Role bindings, which have no names in the file, are named
// after who they are for and their role.
func FromConfig(c *config.Config) ([]engine.ConfigObject, error) {
	var out []engine.ConfigObject
	add := func(kind, name string, v any) error {
		k := kindByName(kind)
		spec, err := encodeSpec(k, v)
		if err != nil {
			return fmt.Errorf("%s %s: %w", kind, name, err)
		}
		out = append(out, engine.ConfigObject{Kind: kind, Name: name, Spec: spec})
		return nil
	}
	inst := installationOf(c)
	if err := add("installation", "installation", &inst); err != nil {
		return nil, err
	}
	if string(out[0].Spec) == "{}" {
		// The file sets none of them: nothing to import (an import must
		// not reset settings it does not mention).
		out = out[:0]
	}
	for _, l := range c.Libraries {
		if err := add("library", l.Name, &l); err != nil {
			return nil, err
		}
	}
	for _, p := range c.Projects {
		if err := add("project", p.Name, &p); err != nil {
			return nil, err
		}
	}
	for _, e := range c.Environments {
		if err := add("environment", e.Name, &e); err != nil {
			return nil, err
		}
	}
	for _, p := range c.RunnerPools {
		if err := add("runner-pool", p.Name, &p); err != nil {
			return nil, err
		}
	}
	seen := map[string]int{}
	for _, b := range c.Auth.Access {
		name := bindingName(b)
		seen[name]++
		if seen[name] > 1 {
			name = fmt.Sprintf("%s-%d", name, seen[name])
		}
		if err := add("role-binding", name, &b); err != nil {
			return nil, err
		}
	}
	if c.Policy != nil {
		if n := c.Policy.Policies; n.Kind == yaml.SequenceNode {
			for i, pn := range n.Content {
				name := ""
				for j := 0; j+1 < len(pn.Content); j += 2 {
					if pn.Content[j].Value == "name" {
						name = pn.Content[j+1].Value
					}
				}
				if name == "" {
					return nil, fmt.Errorf("policy.policies[%d] has no name", i)
				}
				if err := add("policy", name, pn); err != nil {
					return nil, err
				}
			}
		}
		for _, s := range c.Policy.Sources {
			if err := add("policy-source", s.Name, &s); err != nil {
				return nil, err
			}
		}
	}
	for _, h := range c.Verification.Sources {
		if err := add("health-source", h.Name, &h); err != nil {
			return nil, err
		}
	}
	for _, m := range c.AI.Models {
		if err := add("model", m.Name, &m); err != nil {
			return nil, err
		}
	}
	for _, o := range out {
		if !ValidName(o.Name) {
			return nil, fmt.Errorf("%s %q: names are letters, digits, '.', '_' and '-', starting with a letter or digit, at most 100", o.Kind, o.Name)
		}
	}
	return out, nil
}

func bindingName(b config.AccessBinding) string {
	who := "user-" + b.User
	if b.Group != "" {
		who = "group-" + b.Group
	}
	var sb strings.Builder
	for _, r := range strings.ToLower(who + "-" + b.Role) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	s := strings.Trim(sb.String(), "-")
	if len(s) > 90 {
		s = s[:90]
	}
	if len(b.Projects) > 0 || len(b.Environments) > 0 {
		// Several bindings for the same person and role differ in what
		// they cover; keep the name short and stable.
		s += "-" + shortHash(strings.Join(b.Projects, ",")+"|"+strings.Join(b.Environments, ","))
	}
	return s
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func installationOf(c *config.Config) Installation {
	s := Installation{Approvals: c.Approvals, Drafts: c.Drafts, PullRequests: c.PullRequests, Schedules: c.Schedules, Loops: c.Loops,
		Tests: c.Tests, Findings: c.Findings, Reports: c.Reports, RunnerAdmin: c.RunnerAdmin, Tokens: c.Auth.Tokens,
		Artifacts: ArtifactLimits{MaxFileMiB: c.Artifacts.MaxFileMiB, MaxRunMiB: c.Artifacts.MaxRunMiB, MaxProjectGiB: c.Artifacts.MaxProjectGiB,
			Retain: c.Artifacts.Retain, MaxRetain: c.Artifacts.MaxRetain,
			Store: c.Artifacts.Store, Database: c.Artifacts.Database, Dir: c.Artifacts.Dir, S3: c.Artifacts.S3},
		MCP: c.MCP, Egress: EgressAllow{Allow: c.Egress.Allow}, History: c.History, Forge: c.Forge}
	s.Reports.Origin = ""
	if c.Policy != nil {
		s.PolicyWaivers = c.Policy.Waivers
	}
	return s
}

// movedKeys lists the keys of a configuration file that moved to the
// database and that the file sets: what is ignored after the import.
func movedKeys(c *config.Config) []string {
	var keys []string
	set := func(cond bool, key string) {
		if cond {
			keys = append(keys, key)
		}
	}
	set(len(c.Projects) > 0, "projects")
	set(len(c.Libraries) > 0, "libraries")
	set(c.Approvals != (config.ApprovalsConfig{}), "approvals")
	set(c.Drafts != (config.DraftsConfig{}), "drafts")
	set(c.PullRequests != (config.PullRequestsConfig{}), "pull_requests")
	set(c.Tests != (config.TestsConfig{}), "tests")
	set(c.Findings != (config.FindingsConfig{}), "findings")
	r := c.Reports
	r.Origin = ""
	set(r != (config.ServerReportsConfig{}), "reports (except origin)")
	a := c.Artifacts
	set(a.MaxFileMiB != 0 || a.MaxRunMiB != 0 || a.MaxProjectGiB != 0 || a.Retain != "" || a.MaxRetain != "",
		"artifacts limits (max_file_mib, max_run_mib, max_project_gib, retain, max_retain)")
	set(a.Store != "" || a.Database != nil || a.Dir != "" || a.S3 != nil, "artifacts store (store, database, dir, s3)")
	set(!reflect.DeepEqual(c.MCP, config.MCPConfig{}), "mcp")
	set(len(c.Egress.Allow) > 0, "egress.allow")
	set(c.History != (config.HistoryConfig{}), "history")
	set(c.Loops != (config.LoopsConfig{}), "loops")
	set(c.Schedules != (config.SchedulesConfig{}), "schedules")
	set(len(c.Environments) > 0, "environments")
	set(len(c.RunnerPools) > 0, "runner_pools")
	set(c.RunnerAdmin != "", "runner_admin")
	set(c.Policy != nil, "policy")
	set(len(c.Verification.Sources) > 0, "verification")
	set(len(c.AI.Models) > 0, "ai.models")
	set(len(c.Auth.Access) > 0, "auth.access")
	set(c.Auth.Tokens != (config.TokensConfig{}), "auth.tokens")
	set(c.Forge != (config.ForgeConfig{}), "forge")
	return keys
}

// Export writes the objects as a configuration file's sections: what
// launchway config export prints, which launchway config import reads
// back, and which, merged into a configuration file, an older server can
// run with. Objects defined at a unit, and units, have no place in the
// file; they are written under the key units, which only import reads.
func Export(objs []engine.ConfigObject) ([]byte, error) {
	live := make([]engine.ConfigObject, 0, len(objs))
	for _, o := range objs {
		if !o.Deleted() {
			live = append(live, o)
		}
	}
	sortObjects(live)
	doc := &yaml.Node{Kind: yaml.MappingNode}
	put := func(key string, v *yaml.Node) {
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
	}
	seqs := map[string]*yaml.Node{}
	seq := func(key string) *yaml.Node {
		if seqs[key] == nil {
			seqs[key] = &yaml.Node{Kind: yaml.SequenceNode}
		}
		return seqs[key]
	}
	var levels []*yaml.Node
	for _, o := range live {
		k := kindByName(o.Kind)
		if k == nil {
			return nil, fmt.Errorf("%s %s: unknown kind", o.Kind, o.Name)
		}
		var m map[string]any
		if err := json.Unmarshal(o.Spec, &m); err != nil {
			return nil, err
		}
		item := map[string]any{}
		if hasName(k) {
			item["name"] = o.Name
		}
		for key, v := range m {
			item[key] = v
		}
		n, err := toNode(item)
		if err != nil {
			return nil, err
		}
		if keptUnderObjects(k, o) {
			// Kept with its name and level, under objects.
			entry := map[string]any{"kind": o.Kind, "name": o.Name}
			if o.Unit != "" {
				entry["unit"] = o.Unit
			}
			entry["spec"] = m
			en, err := toNode(entry)
			if err != nil {
				return nil, err
			}
			levels = append(levels, en)
			if o.Kind == "role-binding" && o.Unit == "" {
				seq("auth.access").Content = append(seq("auth.access").Content, n)
			}
			continue
		}
		if o.Kind == "installation" {
			var inst Installation
			if v, _, err := decodeSpec(k, o.Name, o.Spec); err == nil {
				inst = *v.(*Installation)
			}
			if err := exportInstallation(put, &inst); err != nil {
				return nil, err
			}
		} else if key := exportSection[o.Kind]; key != "" {
			seq(key).Content = append(seq(key).Content, n)
		}
	}
	for _, key := range []string{"libraries", "projects", "environments", "runner_pools"} {
		if s := seqs[key]; s != nil {
			put(key, s)
		}
	}
	nested := func(top string, keys ...string) {
		var m *yaml.Node
		for _, k := range keys {
			if s := seqs[top+"."+k]; s != nil {
				if m == nil {
					m = &yaml.Node{Kind: yaml.MappingNode}
				}
				m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, s)
			}
		}
		if m != nil {
			mergeInto(doc, top, m)
		}
	}
	nested("policy", "sources", "policies")
	nested("verification", "sources")
	nested("ai", "models")
	nested("auth", "access")
	if len(levels) > 0 {
		put("objects", &yaml.Node{Kind: yaml.SequenceNode, Content: levels})
	}
	var b strings.Builder
	b.WriteString("# Launchway configuration, exported from the database.\n")
	b.WriteString("# launchway config import reads it back. Merged into a configuration file\n")
	b.WriteString("# (without objects:, which only import reads), an older server runs with it.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// exportSection is where each kind of object is written in the file's
// sections (a list under that key).
var exportSection = map[string]string{
	"library": "libraries", "project": "projects", "environment": "environments", "runner-pool": "runner_pools",
	"policy": "policy.policies", "policy-source": "policy.sources", "health-source": "verification.sources", "model": "ai.models",
}

// keptUnderObjects: the file has no place for the object, or not for its
// name or level, so the export keeps it under objects.
func keptUnderObjects(k *Kind, o engine.ConfigObject) bool {
	switch o.Kind {
	case "role-binding", "unit", "organisation", "secret-store", "step-resources", "sign-in":
		return true
	}
	return k.Levels && o.Unit != ""
}

// mergeInto puts m under key in doc, merging with a mapping already there.
func mergeInto(doc *yaml.Node, key string, m *yaml.Node) {
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == key && doc.Content[i+1].Kind == yaml.MappingNode {
			doc.Content[i+1].Content = append(doc.Content[i+1].Content, m.Content...)
			return
		}
	}
	doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, m)
}

func exportInstallation(put func(string, *yaml.Node), s *Installation) error {
	g, _, err := toGeneric(reflect.ValueOf(s))
	if err != nil {
		return err
	}
	m, _ := g.(map[string]any)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		switch k {
		case "levels":
			continue // only in objects: the file has no levels
		case "policy_waivers":
			n, err := toNode(map[string]any{"waivers": v})
			if err != nil {
				return err
			}
			put("policy", n)
			continue
		case "tokens":
			n, err := toNode(map[string]any{"tokens": v})
			if err != nil {
				return err
			}
			put("auth", n)
			continue
		}
		n, err := toNode(v)
		if err != nil {
			return err
		}
		put(k, n)
	}
	return nil
}

// toNode turns a generic value into a YAML node, keys sorted with name
// first.
func toNode(v any) (*yaml.Node, error) {
	switch t := v.(type) {
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if i := slices.Index(keys, "name"); i > 0 {
			keys = append([]string{"name"}, slices.Delete(keys, i, i+1)...)
		}
		for _, k := range keys {
			c, err := toNode(t[k])
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, c)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		for _, x := range t {
			c, err := toNode(x)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, c)
		}
		return n, nil
	}
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return &n, nil
}
