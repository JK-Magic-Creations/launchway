package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// VariableService is what the interfaces (HTTP API, MCP, CLI, the pages)
// call: it keeps variables in the store, applies the rule, and refuses what
// a lock forbids. It does not decide who may call it; the interfaces do, with
// the level's permission (see VariableLevelAdmin).
type VariableService struct {
	Store  *VariableStore
	layout func() (*Layout, error)
}

// NewVariableService returns the service. layout says what the levels look
// like now (which units and projects there are, and what the levels are
// called); Service.Levels is the usual one.
func NewVariableService(store *VariableStore, layout func() (*Layout, error)) *VariableService {
	return &VariableService{Store: store, layout: layout}
}

func (s *VariableService) lay() (*Layout, error) {
	if s == nil || s.layout == nil {
		return &Layout{Levels: DefaultLevels, ProjectUnit: map[string]string{}}, nil
	}
	l, err := s.layout()
	if err != nil {
		return nil, err
	}
	if l.Levels.Installation.Singular == "" {
		l.Levels = DefaultLevels
	}
	return l, nil
}

// checkScope checks that a level's scope exists, and returns the unit a
// project is in.
func checkScope(lay *Layout, k VarKey) (unit string, err error) {
	switch k.Level {
	case LevelUnit:
		for _, u := range lay.Units {
			if u.Name == k.Scope {
				return u.Name, nil
			}
		}
		return "", fmt.Errorf("%w: %s %s", engine.ErrNotFound, strings.ToLower(lay.Levels.Unit.Singular), k.Scope)
	case LevelProject:
		u, ok := lay.ProjectUnit[k.Scope]
		if !ok {
			return "", fmt.Errorf("%w: project %s", engine.ErrNotFound, k.Scope)
		}
		return u, nil
	}
	return "", nil
}

// VarEntry is a variable in effect at a level or for a project, with what a
// page needs to offer: whether it is set at this very level and whether
// this level may override it.
type VarEntry struct {
	Resolved
	// SetHere: the variable has a value set at this level and scope.
	SetHere bool `json:"set_here"`
	// CanOverride: this level may set (or change) it; Blocked says why not,
	// in a sentence, when a lock above forbids it.
	CanOverride bool   `json:"can_override"`
	Blocked     string `json:"blocked,omitempty"`
}

// VariableView is the variables in effect at a level, or for a project,
// with the whole chain of each: the hierarchy, as a page shows it.
type VariableView struct {
	Rule        string     `json:"rule"`
	Level       Level      `json:"level"`
	Scope       string     `json:"scope,omitempty"`
	Project     string     `json:"project,omitempty"`
	Unit        string     `json:"unit,omitempty"`
	Environment string     `json:"environment,omitempty"`
	Words       Levels     `json:"words"`
	Variables   []VarEntry `json:"variables"`
}

func (s *VariableService) inputFor(lay *Layout, all []Variable, k VarKey, env string) (ResolveInput, error) {
	unit, err := checkScope(lay, k)
	if err != nil {
		return ResolveInput{}, err
	}
	in := ResolveInput{Variables: all, Environment: env, Words: lay.Levels, Within: k.Level}
	switch k.Level {
	case LevelProject:
		in.Project, in.Unit = k.Scope, unit
	case LevelUnit:
		in.Unit = unit
	}
	return in, nil
}

// View resolves the variables as seen from a level and scope: what is set
// there and what it inherits from above. At LevelProject it is the
// project's effective view, for an environment if one is given.
func (s *VariableService) View(ctx context.Context, level Level, scope, env string, names []string) (*VariableView, error) {
	if _, err := ScopeFor(level, scope); err != nil {
		return nil, err
	}
	if level == LevelOrganization {
		scope = DefaultOrg
	}
	if env != "" && !variableEnvRe.MatchString(env) {
		return nil, variableProblem("%q is not a valid environment name", clip(env, 60))
	}
	lay, err := s.lay()
	if err != nil {
		return nil, err
	}
	all, err := s.Store.All(ctx)
	if err != nil {
		return nil, err
	}
	k := VarKey{Level: level, Scope: scope}
	in, err := s.inputFor(lay, all, k, env)
	if err != nil {
		return nil, err
	}
	in.Names = names
	// A level's own page lists what is set at that level for any environment
	// too, but resolves for the one asked.
	res := ResolveVariables(in)
	v := &VariableView{Rule: VariableRule, Level: level, Scope: scope, Project: in.Project, Unit: in.Unit, Environment: env, Words: lay.Levels, Variables: []VarEntry{}}
	here := map[string]bool{}
	for _, x := range all {
		if x.Level == level && x.Scope == scope {
			here[x.Name] = true
		}
	}
	seen := map[string]bool{}
	for _, r := range res.Variables {
		seen[r.Name] = true
		v.Variables = append(v.Variables, s.entry(in, k, r, here[r.Name]))
	}
	// A variable set here only for another environment still belongs on the
	// level's page, so it can be found and changed.
	if len(names) == 0 {
		var extra []string
		for n := range here {
			if !seen[n] {
				extra = append(extra, n)
			}
		}
		sort.Strings(extra)
		for _, n := range extra {
			in2 := in
			in2.Environment = envOf(all, k, n)
			in2.Names = []string{n}
			for _, r := range ResolveVariables(in2).Variables {
				v.Variables = append(v.Variables, s.entry(in2, k, r, true))
			}
		}
		sort.Slice(v.Variables, func(i, j int) bool { return v.Variables[i].Name < v.Variables[j].Name })
	}
	return v, nil
}

// envOf is an environment a name is set for at the level, for showing it.
func envOf(all []Variable, k VarKey, name string) string {
	for _, x := range all {
		if x.Level == k.Level && x.Scope == k.Scope && x.Name == name && x.Environment != "" {
			return x.Environment
		}
	}
	return ""
}

func (s *VariableService) entry(in ResolveInput, k VarKey, r Resolved, setHere bool) VarEntry {
	e := VarEntry{Resolved: r, SetHere: setHere, CanOverride: true}
	if r.Locked != nil && r.Locked.Level.rank() < k.Level.rank() {
		e.CanOverride = false
		e.Blocked = lockedSentence(in, r)
	}
	return e
}

// lockedSentence says what a lock forbids and who can lift it.
func lockedSentence(in ResolveInput, r Resolved) string {
	by := in.levelPhrase(r.Locked.Level, r.Locked.Scope)
	s := fmt.Sprintf("%s is locked by %s", r.Name, by)
	if r.Locked.Reason != "" {
		s += " (" + r.Locked.Reason + ")"
	}
	return s + ". Only " + r.Locked.Who + " can change it."
}

// lockAbove returns why k may not be set because a lock above it forbids it,
// or "".
func (s *VariableService) lockAbove(lay *Layout, all []Variable, k VarKey) (string, error) {
	if k.Level == LevelInstallation {
		return "", nil
	}
	in, err := s.inputFor(lay, all, k, k.Environment)
	if err != nil {
		return "", err
	}
	in.Within = variableLevels[k.Level.rank()-1]
	in.Names = []string{k.Name}
	for _, r := range ResolveVariables(in).Variables {
		if r.Locked != nil && (r.Locked.Environment == "" || r.Locked.Environment == k.Environment) {
			return lockedSentence(in, r), nil
		}
	}
	return "", nil
}

// SetResult is what a Set did.
type SetResult struct {
	Variable *Variable `json:"variable"`
	Changed  bool      `json:"changed"`
}

// Set sets a variable at a level. It refuses when a lock above forbids it.
func (s *VariableService) Set(ctx context.Context, actor string, in SetInput) (*SetResult, error) {
	if err := CheckKey(in.VarKey); err != nil {
		return nil, err
	}
	if in.Level == LevelOrganization {
		in.Scope = DefaultOrg
	}
	lay, err := s.lay()
	if err != nil {
		return nil, err
	}
	if _, err := checkScope(lay, in.VarKey); err != nil {
		return nil, err
	}
	all, err := s.Store.All(ctx)
	if err != nil {
		return nil, err
	}
	if why, err := s.lockAbove(lay, all, in.VarKey); err != nil {
		return nil, err
	} else if why != "" {
		return nil, fmt.Errorf("%w: %s", engine.ErrConflict, why)
	}
	v, changed, err := s.Store.Set(ctx, actor, in)
	if err != nil {
		return nil, err
	}
	return &SetResult{Variable: v, Changed: changed}, nil
}

// Delete removes a variable set at a level. A lock above does not stop it:
// what is removed is this level's own, ignored value.
func (s *VariableService) Delete(ctx context.Context, actor string, k VarKey) error {
	if k.Level == LevelOrganization {
		k.Scope = DefaultOrg
	}
	if err := CheckKey(k); err != nil {
		return err
	}
	return s.Store.Delete(ctx, actor, k)
}

// LockResult is what locking did, and what it now ignores.
type LockResult struct {
	Variable *Variable `json:"variable"`
	// Overrides are the variables set nearer for the same name that the lock
	// now makes count for nothing; they are kept, and count again when the
	// lock is removed.
	Overrides []VarKey `json:"overrides"`
}

// Lock locks a variable set at a level. It needs a reason.
func (s *VariableService) Lock(ctx context.Context, actor string, k VarKey, reason string) (*LockResult, error) {
	if k.Level == LevelOrganization {
		k.Scope = DefaultOrg
	}
	if err := CheckKey(k); err != nil {
		return nil, err
	}
	lay, err := s.lay()
	if err != nil {
		return nil, err
	}
	all, err := s.Store.All(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.SetLock(ctx, actor, k, reason)
	if err != nil {
		return nil, err
	}
	return &LockResult{Variable: v, Overrides: nearerThan(lay, all, k)}, nil
}

// Unlock removes a lock.
func (s *VariableService) Unlock(ctx context.Context, actor string, k VarKey) (*Variable, error) {
	if k.Level == LevelOrganization {
		k.Scope = DefaultOrg
	}
	if err := CheckKey(k); err != nil {
		return nil, err
	}
	return s.Store.ClearLock(ctx, actor, k)
}

// nearerThan lists the variables of the same name that a lock at k sits
// above: nearer levels, in the unit or project it covers.
func nearerThan(lay *Layout, all []Variable, k VarKey) []VarKey {
	covered := func(v Variable) bool {
		if v.Level.rank() <= k.Level.rank() || v.Name != k.Name {
			return false
		}
		if k.Environment != "" && v.Environment != k.Environment {
			return false
		}
		switch k.Level {
		case LevelUnit:
			if v.Level == LevelProject {
				return lay.ProjectUnit[v.Scope] == k.Scope
			}
		case LevelProject:
			return false
		}
		return true
	}
	out := []VarKey{}
	for _, v := range all {
		if covered(v) {
			out = append(out, v.Key())
		}
	}
	return out
}

// Changes returns the audit of variables.
func (s *VariableService) Changes(ctx context.Context, name string, limit int) ([]VariableChange, error) {
	return s.Store.Changes(ctx, name, limit)
}

// Bind returns the variables in effect for a project (for an environment, if
// one is known), as a pipeline takes them.
func (s *VariableService) Bind(ctx context.Context, project, env string) (pipeline.Variables, error) {
	v, err := s.View(ctx, LevelProject, project, env, nil)
	if err != nil {
		return nil, err
	}
	in := ResolveInput{Words: v.Words}
	out := make(pipeline.Variables, len(v.Variables))
	for _, e := range v.Variables {
		b := pipeline.BoundVariable{Name: e.Name, Type: string(e.Type), Text: Text(e.Type, e.Value), Display: e.Display, Source: in.sourcePhrase(e.From)}
		if e.Type == VarSecret {
			b.Text = ""
		}
		if e.Locked != nil {
			b.Locked, b.Who = true, e.Locked.Who
			b.LockedBy = in.levelPhrase(e.Locked.Level, e.Locked.Scope)
			if e.Locked.Reason != "" {
				b.LockedBy += " (" + e.Locked.Reason + ")"
			}
		}
		b.Detail, _ = json.Marshal(e.Resolved)
		out[e.Name] = b
	}
	return out, nil
}

func (in ResolveInput) sourcePhrase(s Source) string {
	if s.Kind != SourceLevel {
		return s.Kind
	}
	p := in.levelPhrase(s.Level, s.Scope)
	if s.Environment != "" {
		p += " for " + s.Environment
	}
	return p
}
