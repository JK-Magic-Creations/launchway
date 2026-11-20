package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// OriginBootstrap is the origin of the one-time import of the file a server
// starts with.
const OriginBootstrap = "bootstrap-file"

// ErrUnavailable: this server or its database does not keep configuration
// in the database (a build limited to an older schema).
var ErrUnavailable = errors.New("configuration in the database needs schema 23")

// Applier puts a checked configuration into effect on this server: the
// engine's, the projects' and the API's live settings.
type Applier func(c *config.Config, lay *Layout) error

// Service is the configuration in the database, as one server sees it.
type Service struct {
	eng  *engine.Engine
	boot *config.Config
	log  *slog.Logger
	// Poll is how often the revision is read, in case a notification was
	// missed; default 2s.
	Poll time.Duration

	mu    sync.Mutex // one reload at a time
	cur   *config.Config
	lay   *Layout
	rev   int64
	apply Applier
	// lastErr is why the newest configuration was not applied, if it was
	// not.
	lastErr string
	// writeMu keeps this server's writes in turn; other servers' are kept
	// apart by the revision.
	writeMu sync.Mutex
}

// New returns the service for a server that started with boot.
func New(eng *engine.Engine, boot *config.Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{eng: eng, boot: boot, log: log, Poll: 2 * time.Second}
}

// Available reports whether configuration is kept in the database.
func (s *Service) Available() bool { return s != nil && s.eng.ConfigKnown() }

// Boot is the file the server started with.
func (s *Service) Boot() *config.Config { return s.boot }

// Engine is the engine the service stores in.
func (s *Service) Engine() *engine.Engine { return s.eng }

func (s *Service) check() error {
	if !s.Available() {
		return fmt.Errorf("%w: %w", engine.ErrConflict, ErrUnavailable)
	}
	return nil
}

// ImportResult says what the start-up import did.
type ImportResult struct {
	// Import is the import of the file, by this server or an earlier one.
	Import *engine.ConfigImport
	// Done: this server made it now.
	Done bool
	// Ignored are the keys of the file that moved to the database and that
	// the file sets: from now on they are not read.
	Ignored []string
}

// ImportBootstrap imports, once per installation, the settings of the file
// the server started with that now live in the database. Every server of
// an installation may call it as it starts; exactly one import happens,
// recorded with what it brought. Afterwards the file's copies are ignored.
func (s *Service) ImportBootstrap() (*ImportResult, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	res := &ImportResult{Ignored: movedKeys(s.boot)}
	digest := ""
	if f := s.boot.File(); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			sum := sha256.Sum256(b)
			digest = hex.EncodeToString(sum[:])
		}
	}
	for attempt := 0; attempt < 10; attempt++ {
		prior, err := s.eng.ConfigImportByOrigin(OriginBootstrap)
		if err != nil {
			return nil, err
		}
		if prior != nil {
			res.Import = prior
			return res, nil
		}
		rev, objs, err := s.eng.ConfigSnapshot()
		if err != nil {
			return nil, err
		}
		file, err := FromConfig(s.boot)
		if err != nil {
			return nil, fmt.Errorf("importing the configuration file: %w", err)
		}
		for _, o := range file {
			k := kindByName(o.Kind)
			v, _, err := decodeSpec(k, o.Name, o.Spec)
			if err == nil {
				err = checkOwn(k, v)
			}
			if err != nil {
				return nil, fmt.Errorf("importing the configuration file: %s %s: %w", o.Kind, o.Name, err)
			}
		}
		writes := make([]engine.ConfigWrite, 0, len(file))
		after := mergeObjects(objs, file)
		if _, err := s.composeChecked(after); err != nil {
			return nil, fmt.Errorf("importing the configuration file: %w", err)
		}
		counts := map[string]int{}
		var names []string
		for _, o := range file {
			writes = append(writes, engine.ConfigWrite{Kind: o.Kind, Name: o.Name, Unit: o.Unit, Spec: o.Spec, Action: engine.ConfigUpdate,
				IfVersion: -1, ManagedBy: engine.ManagedByImport, Comment: "imported from the configuration file"})
			counts[o.Kind]++
			names = append(names, o.Kind+"/"+o.Name)
		}
		sum, _ := json.Marshal(map[string]any{"file": s.boot.File(), "keys": res.Ignored, "counts": counts, "objects": names})
		imp := &engine.ConfigImport{Origin: OriginBootstrap, Digest: digest, Summary: sum}
		_, _, err = s.eng.WriteConfig(rev, "launchway (import of "+displayFile(s.boot.File())+")", writes, imp)
		if errors.Is(err, engine.ErrConfigMoved) {
			continue
		}
		if err != nil {
			return nil, err
		}
		res.Import, res.Done = imp, true
		return res, nil
	}
	return nil, errors.New("importing the configuration file: other servers kept changing the configuration; start again")
}

func displayFile(f string) string {
	if f == "" {
		return "no file"
	}
	return f
}

// mergeObjects returns objs with puts applied (deleted ones made live).
func mergeObjects(objs []engine.ConfigObject, puts []engine.ConfigObject) []engine.ConfigObject {
	out := make([]engine.ConfigObject, 0, len(objs)+len(puts))
	idx := map[string]int{}
	for _, o := range objs {
		idx[o.Kind+"\x00"+o.Name] = len(out)
		out = append(out, o)
	}
	for _, p := range puts {
		if i, ok := idx[p.Kind+"\x00"+p.Name]; ok {
			o := out[i]
			o.Spec, o.Unit, o.DeletedAt = p.Spec, p.Unit, 0
			out[i] = o
			continue
		}
		idx[p.Kind+"\x00"+p.Name] = len(out)
		out = append(out, p)
	}
	return out
}

// composeChecked composes and checks.
func (s *Service) composeChecked(objs []engine.ConfigObject) (*composed, error) {
	c, lay, err := Compose(s.boot, objs)
	if err != nil {
		return nil, err
	}
	if err := Check(c, s.eng); err != nil {
		return nil, err
	}
	return &composed{cfg: c, lay: lay}, nil
}

type composed struct {
	cfg *config.Config
	lay *Layout
}

// Check holds a composed configuration to every rule a server applies
// before using one: the configuration file's (config.Config.Validate),
// the projects' and libraries' (source.CheckProjects), the runner pools'
// and the engine's.
func Check(c *config.Config, eng *engine.Engine) error {
	if err := c.Validate("configuration"); err != nil {
		return err
	}
	projects, err := c.SourceProjects()
	if err != nil {
		return err
	}
	if err := source.CheckProjects(projects, c.SourceLibraries()); err != nil {
		return err
	}
	live, err := c.EngineLive()
	if err != nil {
		return err
	}
	if eng != nil {
		return eng.CheckLive(live)
	}
	return nil
}

// Load reads the configuration, checks it and applies it with apply, which
// is then used for every later change. A configuration that does not pass
// stops the server, as a mistake in the file always has.
func (s *Service) Load(apply Applier) error {
	if err := s.check(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apply = apply
	return s.reloadLocked(true)
}

// Current returns the configuration in effect, its layout and revision.
func (s *Service) Current() (*config.Config, *Layout, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.lay, s.rev
}

// Status says what this server runs with.
type Status struct {
	Revision int64  `json:"revision"`
	Problem  string `json:"problem,omitempty"`
}

// Status returns the revision in effect and why a newer one is not, if not.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Revision: s.rev, Problem: s.lastErr}
}

// Refresh applies the stored configuration if it changed.
func (s *Service) Refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.apply == nil {
		return
	}
	if err := s.reloadLocked(false); err != nil {
		s.log.Error("the configuration in the database changed but cannot be used here; keeping the configuration in effect", "err", err)
	}
}

func (s *Service) reloadLocked(first bool) error {
	rev, err := s.eng.ConfigRevision()
	if err != nil {
		return err
	}
	if !first && rev == s.rev {
		return nil
	}
	rev, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return err
	}
	cp, err := s.composeChecked(objs)
	if err == nil {
		err = s.projectStepResources(objs)
	}
	if err == nil {
		err = s.apply(cp.cfg, cp.lay)
	}
	if err != nil {
		s.lastErr = fmt.Sprintf("revision %d: %v", rev, err)
		if !first {
			s.rev = rev // do not try again until it changes
		}
		return err
	}
	if !first && s.rev != rev {
		s.log.Info("applied a change to the configuration", "revision", rev)
	}
	s.cur, s.lay, s.rev, s.lastErr = cp.cfg, cp.lay, rev, ""
	return nil
}

// Watch applies changes made by any server until ctx ends: at once when
// this server or a notification says so, and otherwise within Poll.
func (s *Service) Watch(ctx context.Context) {
	kick := make(chan struct{}, 1)
	s.eng.OnConfigChange(func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	})
	t := time.NewTicker(s.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
		s.Refresh()
	}
}

// ---- reading ----

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

// ---- changing ----

// Change is one change asked for.
type Change struct {
	Kind *Kind
	Name string
	// Action is engine.ConfigUpdate (create or replace), ConfigDelete or
	// ConfigRestore.
	Action string
	Unit   string
	Spec   json.RawMessage
	// IfVersion: -1 any; 0 create only; otherwise the version expected.
	IfVersion int64
	ManagedBy string
	Comment   string
}

// Result is what a change did.
type Result struct {
	Object  *engine.ConfigObject `json:"object"`
	Change  *engine.ConfigChange `json:"change,omitempty"`
	Changed bool                 `json:"changed"`
}

// Problem is a reason a change is refused.
type Problem struct {
	Message string `json:"message"`
}

// InvalidError is a change the rules refuse; Problems says why.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string { return strings.Join(e.Problems, "; ") }

// Is makes an InvalidError an engine.ErrInvalid.
func (e *InvalidError) Is(target error) bool { return target == engine.ErrInvalid }

func invalid(err error) error {
	var ie *InvalidError
	if errors.As(err, &ie) {
		return err
	}
	var parts []string
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, e := range joined.Unwrap() {
			parts = append(parts, e.Error())
		}
	} else {
		parts = strings.Split(err.Error(), "\n")
	}
	return &InvalidError{Problems: parts}
}

// prepare checks a change on its own and makes the write.
func (s *Service) prepare(c Change) (engine.ConfigWrite, *engine.ConfigObject, error) {
	k := c.Kind
	w := engine.ConfigWrite{Kind: k.Name, Name: c.Name, Action: c.Action, Unit: c.Unit, IfVersion: c.IfVersion, ManagedBy: c.ManagedBy, Comment: c.Comment}
	switch {
	case k.Singleton != "" && c.Name != k.Singleton:
		return w, nil, invalid(fmt.Errorf("there is one %s, named %s", strings.ToLower(k.Title), k.Singleton))
	case !ValidName(c.Name):
		return w, nil, invalid(fmt.Errorf("%q: names are letters, digits, '.', '_' and '-', starting with a letter or digit, at most 100", c.Name))
	case c.Unit != "" && !k.Levels:
		return w, nil, invalid(fmt.Errorf("a %s belongs to the whole installation, not to a unit", strings.ToLower(k.Title)))
	case k.Singleton != "" && (c.Action == engine.ConfigDelete || c.Action == engine.ConfigRestore):
		return w, nil, invalid(fmt.Errorf("the %s cannot be deleted; change it instead", strings.ToLower(k.Title)))
	case len(c.Comment) > 500:
		return w, nil, invalid(errors.New("a comment is at most 500 characters"))
	}
	if c.ManagedBy != "" && c.ManagedBy != engine.ManagedByAPI && c.ManagedBy != engine.ManagedByTerraform && c.ManagedBy != engine.ManagedByImport {
		return w, nil, invalid(fmt.Errorf("managed by %q: api, terraform or import", c.ManagedBy))
	}
	if c.Action == engine.ConfigUpdate || c.Action == engine.ConfigCreate {
		v, stored, err := decodeSpec(k, c.Name, c.Spec)
		if err != nil {
			return w, nil, invalid(err)
		}
		if err := checkOwn(k, v); err != nil {
			return w, nil, invalid(err)
		}
		w.Spec = stored
		return w, &engine.ConfigObject{Kind: k.Name, Name: c.Name, Unit: c.Unit, Spec: stored}, nil
	}
	return w, nil, nil
}

// checkOwn checks what only this kind's objects say (the whole
// configuration is checked after).
func checkOwn(k *Kind, v any) error {
	switch t := v.(type) {
	case *Installation:
		if t.Reports.Origin != "" {
			return errors.New("reports.origin stays in the configuration file: it is part of how the server is reached")
		}
		return t.Levels.check()
	case *config.ProjectConfig:
		if err := checkAddress("repo", t.Repo); err != nil {
			return err
		}
		return t.Normalize()
	case *config.LibraryConfig:
		return checkAddress("repo", t.Repo)
	case *ai.Model:
		if err := checkAddress("endpoint", t.Endpoint); err != nil {
			return err
		}
		return checkHeaders("headers", t.Headers)
	case *StepResources:
		return checkStepResources(t)
	case *SecretStore:
		if !containsString(SecretStoreTypes, t.Type) {
			return fmt.Errorf("type %q: one of %s", t.Type, strings.Join(SecretStoreTypes, ", "))
		}
		for _, p := range t.Projects {
			if err := checkPattern(p); err != nil {
				return err
			}
		}
	case *Unit:
		if len(t.DisplayName) > 80 {
			return errors.New("display_name is at most 80 characters")
		}
	}
	return nil
}

func checkPattern(p string) error {
	if p == "" {
		return errors.New("an empty project pattern")
	}
	if _, err := pathMatch(p, ""); err != nil {
		return fmt.Errorf("%q is not a valid project pattern", p)
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Apply makes changes, all or none, after checking the whole configuration
// they would make, and puts it into effect on this server before returning
// (other servers follow within seconds). actor is who asked.
func (s *Service) Apply(actor string, changes []Change) ([]Result, error) {
	return s.apply1(actor, changes, nil, false)
}

// DryRun checks changes as Apply would, and stores nothing.
func (s *Service) DryRun(changes []Change) error {
	_, err := s.apply1("", changes, nil, true)
	return err
}

func (s *Service) apply1(actor string, changes []Change, imp *engine.ConfigImport, dry bool) ([]Result, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	writes := make([]engine.ConfigWrite, 0, len(changes))
	puts := []engine.ConfigObject{}
	for _, c := range changes {
		w, obj, err := s.prepare(c)
		if err != nil {
			return nil, err
		}
		writes = append(writes, w)
		if obj != nil {
			puts = append(puts, *obj)
		}
	}
	if !dry {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	for attempt := 0; attempt < 10; attempt++ {
		rev, objs, err := s.eng.ConfigSnapshot()
		if err != nil {
			return nil, err
		}
		after, err := applyLocally(objs, writes)
		if err != nil {
			return nil, err
		}
		if _, err := s.composeChecked(after); err != nil {
			return nil, invalid(err)
		}
		if dry {
			return nil, nil
		}
		_, recorded, err := s.eng.WriteConfig(rev, actor, writes, imp)
		if errors.Is(err, engine.ErrConfigMoved) {
			continue
		}
		if err != nil {
			return nil, err
		}
		s.Refresh()
		_, now, err := s.eng.ConfigSnapshot()
		if err != nil {
			return nil, err
		}
		// This server's tables follow at once, whether or not it applies
		// configuration to a running engine (the others follow within seconds).
		if err := s.projectStepResources(now); err != nil {
			return nil, err
		}
		results := make([]Result, 0, len(writes))
		for _, w := range writes {
			r := Result{}
			for i := range now {
				if now[i].Kind == w.Kind && now[i].Name == w.Name {
					o := now[i]
					r.Object = &o
				}
			}
			for i := range recorded {
				if recorded[i].Kind == w.Kind && recorded[i].Name == w.Name {
					c := recorded[i]
					r.Change, r.Changed = &c, true
				}
			}
			results = append(results, r)
		}
		return results, nil
	}
	return nil, fmt.Errorf("%w: other changes kept coming first; try again", engine.ErrConflict)
}

// applyLocally works out the objects after writes, checking versions as
// the store will (so a dry run says the same).
func applyLocally(objs []engine.ConfigObject, writes []engine.ConfigWrite) ([]engine.ConfigObject, error) {
	out := append([]engine.ConfigObject(nil), objs...)
	find := func(kind, name string) int {
		for i := range out {
			if out[i].Kind == kind && out[i].Name == name {
				return i
			}
		}
		return -1
	}
	for _, w := range writes {
		i := find(w.Kind, w.Name)
		what := w.Kind + " " + w.Name
		if w.IfVersion > 0 && (i < 0 || out[i].Version != w.IfVersion) {
			have := int64(0)
			if i >= 0 {
				have = out[i].Version
			}
			return nil, fmt.Errorf("%w: %s is at version %d, not %d: someone changed it meanwhile; read it again", engine.ErrConflict, what, have, w.IfVersion)
		}
		switch w.Action {
		case engine.ConfigUpdate, engine.ConfigCreate:
			if w.IfVersion == 0 && i >= 0 && !out[i].Deleted() {
				return nil, fmt.Errorf("%w: %s already exists", engine.ErrConflict, what)
			}
			if i < 0 {
				out = append(out, engine.ConfigObject{Kind: w.Kind, Name: w.Name, Unit: w.Unit, Spec: w.Spec})
			} else {
				out[i].Spec, out[i].Unit, out[i].DeletedAt = w.Spec, w.Unit, 0
			}
		case engine.ConfigDelete:
			if i < 0 || out[i].Deleted() {
				return nil, fmt.Errorf("%w: %s", engine.ErrNotFound, what)
			}
			out[i].DeletedAt = 1
		case engine.ConfigRestore:
			if i < 0 {
				return nil, fmt.Errorf("%w: %s", engine.ErrNotFound, what)
			}
			if !out[i].Deleted() {
				return nil, fmt.Errorf("%w: %s is not deleted", engine.ErrConflict, what)
			}
			out[i].DeletedAt = 0
		}
	}
	return out, nil
}

// ---- history ----

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

// ---- levels ----

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
