package settings

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// List returns the objects of a kind (every kind if k is nil), deleted
// ones only if asked.
func (s *Service) List(k *Kind, deleted bool) ([]engine.ConfigObject, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	_, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return nil, err
	}
	out := []engine.ConfigObject{}
	have := false
	for _, o := range objs {
		if (k == nil || o.Kind == k.Name) && (deleted || !o.Deleted()) {
			out = append(out, o)
		}
		have = have || o.Kind == "step-resources"
	}
	if !have && (k == nil || k.Name == "step-resources") && s.eng.StepResourcesKnown() {
		// Never changed through the object: what the tables hold, so an
		// export has it too.
		if o, err := s.stepResourcesObject(); err == nil {
			out = append(out, *o)
		}
	}
	sortObjects(out)
	return out, nil
}

// Get returns one object, deleted or not.
func (s *Service) Get(k *Kind, name string) (*engine.ConfigObject, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	_, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		if o.Kind == k.Name && o.Name == name {
			return &o, nil
		}
	}
	if k.Name == "step-resources" && name == k.Singleton {
		// Never changed through the object: what the tables hold.
		return s.stepResourcesObject()
	}
	if k.Singleton != "" && name == k.Singleton {
		// Never written: its defaults.
		return &engine.ConfigObject{Kind: k.Name, Name: name, Spec: json.RawMessage("{}")}, nil
	}
	return nil, fmt.Errorf("%w: %s %s", engine.ErrNotFound, strings.ToLower(k.Title), name)
}

// HistoryEntry is a change with what it changed.
type HistoryEntry struct {
	engine.ConfigChange
	Diff []FieldChange `json:"diff"`
}

// History returns changes, newest first, with their differences.
func (s *Service) History(f engine.ConfigHistoryFilter) ([]HistoryEntry, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	cs, err := s.eng.ConfigHistory(f)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(cs))
	for _, c := range cs {
		d := Diff(c.Before, c.After)
		if c.UnitBefore != c.UnitAfter {
			d = append([]FieldChange{{Path: "(unit)", Before: c.UnitBefore, After: c.UnitAfter}}, d...)
		}
		out = append(out, HistoryEntry{ConfigChange: c, Diff: d})
	}
	return out, nil
}

// Imports lists the imports.
func (s *Service) Imports() ([]engine.ConfigImport, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return s.eng.ConfigImports()
}

// Levels returns the layout in effect.
func (s *Service) Levels() (*Layout, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	_, lay, _ := s.Current()
	if lay == nil {
		_, objs, err := s.eng.ConfigSnapshot()
		if err != nil {
			return nil, err
		}
		_, lay, err = Compose(s.boot, objs)
		if err != nil {
			return nil, err
		}
	}
	return lay, nil
}

// Effective is what applies to one project, and where each part comes from.
type Effective struct {
	Project string           `json:"project"`
	Unit    string           `json:"unit,omitempty"`
	Objects []EffectiveEntry `json:"objects"`
	// Settings are the project's settings that an installation default can
	// give, with where the value comes from.
	Settings []EffectiveSetting `json:"settings"`
}

// EffectiveEntry is an object that applies to the project.
type EffectiveEntry struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// From is the level it is defined at: organisation, unit or project.
	From string `json:"from"`
	Unit string `json:"unit,omitempty"`
}

// EffectiveSetting is one setting and its source.
type EffectiveSetting struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	From  string `json:"from"` // project, installation or default
}

// EffectiveFor works out what applies to a project.
func (s *Service) EffectiveFor(project string) (*Effective, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	_, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return nil, err
	}
	c, lay, err := Compose(s.boot, objs)
	if err != nil {
		return nil, err
	}
	unit, ok := lay.ProjectUnit[project]
	if !ok {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, project)
	}
	ef := &Effective{Project: project, Unit: unit, Objects: []EffectiveEntry{}, Settings: []EffectiveSetting{}}
	var pc config.ProjectConfig
	for _, p := range c.Projects {
		if p.Name == project {
			pc = p
		}
	}
	live := []engine.ConfigObject{}
	for _, o := range objs {
		if !o.Deleted() {
			live = append(live, o)
		}
	}
	sortObjects(live)
	for _, o := range live {
		k := kindByName(o.Kind)
		if k == nil || !k.Levels || o.Kind == "project" {
			continue
		}
		if o.Unit != "" && o.Unit != unit {
			continue
		}
		v, _, err := decodeSpec(k, o.Name, o.Spec)
		if err != nil {
			continue
		}
		pats, all := projectsOf(v)
		if !all && !matchAny(pats, project) {
			continue
		}
		from := "organisation"
		if o.Unit != "" {
			from = "unit"
		}
		if len(pats) == 1 && pats[0] == project {
			from = "project"
		}
		ef.Objects = append(ef.Objects, EffectiveEntry{Kind: o.Kind, Name: o.Name, From: from, Unit: o.Unit})
	}
	setting := func(key string, projectVal, instVal any, projectSet, instSet bool, def any) {
		e := EffectiveSetting{Key: key, Value: def, From: "default"}
		switch {
		case projectSet:
			e.Value, e.From = projectVal, "project"
		case instSet:
			e.Value, e.From = instVal, "installation"
		}
		ef.Settings = append(ef.Settings, e)
	}
	b := func(p *bool) any {
		if p == nil {
			return nil
		}
		return *p
	}
	setting("pull_requests.secrets", b(prSecrets(pc)), b(c.PullRequests.Secrets), prSecrets(pc) != nil, c.PullRequests.Secrets != nil, false)
	var pDraftAllow, pDraftSecrets any
	var pAllowSet, pSecretsSet bool
	if pc.Drafts != nil {
		pDraftAllow, pAllowSet = pc.Drafts.Allow, pc.Drafts.Allow != ""
		pDraftSecrets, pSecretsSet = b(pc.Drafts.Secrets), pc.Drafts.Secrets != nil
	}
	setting("drafts.allow", pDraftAllow, c.Drafts.Allow, pAllowSet, c.Drafts.Allow != "", "operator")
	setting("drafts.secrets", pDraftSecrets, b(c.Drafts.Secrets), pSecretsSet, c.Drafts.Secrets != nil, false)
	var pSched any
	pSchedSet := pc.Schedules != nil && pc.Schedules.Enabled != nil
	if pSchedSet {
		pSched = *pc.Schedules.Enabled
	}
	setting("schedules.enabled", pSched, b(c.Schedules.Enabled), pSchedSet, c.Schedules.Enabled != nil, true)
	setting("poll", pc.Poll, nil, pc.Poll != "", false, "30s")
	setting("branch", pc.Branch, nil, pc.Branch != "", false, "main")
	sort.SliceStable(ef.Objects, func(i, j int) bool { return kindOrder(ef.Objects[i].Kind) < kindOrder(ef.Objects[j].Kind) })
	return ef, nil
}

func prSecrets(p config.ProjectConfig) *bool {
	if p.PullRequests == nil {
		return nil
	}
	return p.PullRequests.Secrets
}

// projectsOf returns the project patterns an object applies to, and whether
// it applies to every project (no patterns).
func projectsOf(v any) ([]string, bool) {
	switch t := v.(type) {
	case *config.LibraryConfig:
		return nil, true
	case *config.EnvironmentConfig:
		return t.Projects, false
	case *config.RunnerPoolConfig:
		if t.Kind == engine.PoolBuild {
			return t.Projects, len(t.Projects) == 0
		}
		return nil, false
	case *config.AccessBinding:
		return t.Projects, len(t.Projects) == 0
	case *config.PolicySourceConfig:
		return t.Projects, len(t.Projects) == 0
	case *config.HealthSourceConfig:
		return t.Projects, false
	case *SecretStore:
		return t.Projects, len(t.Projects) == 0
	}
	if m, ok := v.(interface{ ProjectPatterns() []string }); ok {
		return m.ProjectPatterns(), false
	}
	return policyProjects(v)
}
