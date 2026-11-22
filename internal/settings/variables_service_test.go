package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func newVariableService(t *testing.T) *VariableService {
	t.Helper()
	eng := openEngine(t, testdb.New(t))
	lay := &Layout{Levels: DefaultLevels, Units: []UnitView{{Name: "pay", Projects: []string{"cart"}}, {Name: "ops"}},
		ProjectUnit: map[string]string{"cart": "pay", "web": "", "ledger": "pay"}}
	return NewVariableService(NewVariableStore(eng), func() (*Layout, error) { return lay, nil })
}

func setIn(level Level, scope, name, env string, typ VarType, val string) SetInput {
	return SetInput{VarKey: VarKey{Level: level, Scope: scope, Name: name, Environment: env}, Type: typ, Value: json.RawMessage(val)}
}

func mustSet(t *testing.T, s *VariableService, in SetInput) {
	t.Helper()
	if _, err := s.Set(context.Background(), "ada", in); err != nil {
		t.Fatalf("set %s: %v", in.VarKey, err)
	}
}

func entryNamed(t *testing.T, v *VariableView, name string) VarEntry {
	t.Helper()
	for _, e := range v.Variables {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no %s in %+v", name, v.Variables)
	return VarEntry{}
}

func TestSetAtTheBaseOverrideAtAProjectLockAndRefuse(t *testing.T) {
	ctx := context.Background()
	s := newVariableService(t)
	mustSet(t, s, setIn(LevelInstallation, "", "replicas", "", VarNumber, "1"))
	mustSet(t, s, setIn(LevelProject, "cart", "replicas", "", VarNumber, "2"))
	v, err := s.View(ctx, LevelProject, "cart", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	e := entryNamed(t, v, "replicas")
	if e.Display != "2" || e.From.Level != LevelProject || len(e.Chain) != 2 || !e.Chain[1].Overridden || !e.SetHere || !e.CanOverride {
		t.Fatalf("%+v", e)
	}
	// Lock it at the base: the project's value is kept but ignored, and listed.
	lr, err := s.Lock(ctx, "ada", VarKey{Level: LevelInstallation, Name: "replicas"}, "fleet rule")
	if err != nil || len(lr.Overrides) != 1 || lr.Overrides[0].Scope != "cart" {
		t.Fatalf("%+v %v", lr, err)
	}
	v, _ = s.View(ctx, LevelProject, "cart", "", nil)
	e = entryNamed(t, v, "replicas")
	if e.Display != "1" || e.Locked == nil || e.CanOverride || !strings.Contains(e.Blocked, "locked by the base (fleet rule). Only an administrator of every Flight can change it.") {
		t.Fatalf("%+v", e)
	}
	if e.Chain[1].Ignored == "" {
		t.Errorf("the project's value should show as ignored: %+v", e.Chain)
	}
	// A new override below the lock is refused with a message that names it.
	_, err = s.Set(ctx, "bo", setIn(LevelProject, "cart", "replicas", "production", VarNumber, "5"))
	if !errors.Is(err, engine.ErrConflict) || !strings.Contains(err.Error(), "locked by the base") {
		t.Fatalf("%v", err)
	}
	_, err = s.Set(ctx, "bo", setIn(LevelUnit, "pay", "replicas", "", VarNumber, "5"))
	if !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("%v", err)
	}
	// Unlocking brings the project's value back.
	if _, err := s.Unlock(ctx, "ada", VarKey{Level: LevelInstallation, Name: "replicas"}); err != nil {
		t.Fatal(err)
	}
	v, _ = s.View(ctx, LevelProject, "cart", "", nil)
	if e = entryNamed(t, v, "replicas"); e.Display != "2" || e.Locked != nil {
		t.Fatalf("%+v", e)
	}
}

func TestVariablesNeverCrossUnitsOrProjects(t *testing.T) {
	ctx := context.Background()
	s := newVariableService(t)
	mustSet(t, s, setIn(LevelUnit, "ops", "region", "", VarString, `"eu"`))
	mustSet(t, s, setIn(LevelProject, "web", "region", "", VarString, `"us"`))
	v, _ := s.View(ctx, LevelProject, "cart", "", nil)
	if len(v.Variables) != 0 {
		t.Fatalf("cart is in unit pay: %+v", v.Variables)
	}
	// A unit's own page shows its variables and what it inherits.
	mustSet(t, s, setIn(LevelInstallation, "", "region", "", VarString, `"base"`))
	uv, _ := s.View(ctx, LevelUnit, "ops", "", nil)
	if e := entryNamed(t, uv, "region"); e.Display != "eu" || !e.SetHere || len(e.Chain) != 2 {
		t.Fatalf("%+v", e)
	}
}

func TestBindGivesAPipelineWhatIsInEffectAndTheRunRecordsIt(t *testing.T) {
	ctx := context.Background()
	s := newVariableService(t)
	mustSet(t, s, setIn(LevelOrganization, "", "registry", "", VarString, `"harbor"`))
	if _, err := s.Lock(ctx, "ada", VarKey{Level: LevelOrganization, Name: "registry"}, "audited"); err != nil {
		t.Fatal(err)
	}
	mustSet(t, s, setIn(LevelProject, "cart", "replicas", "", VarNumber, "3"))
	mustSet(t, s, setIn(LevelProject, "cart", "replicas", "production", VarNumber, "9"))
	vars, err := s.Bind(ctx, "cart", "production")
	if err != nil {
		t.Fatal(err)
	}
	r, n := vars["registry"], vars["replicas"]
	if r.Text != "harbor" || !r.Locked || r.LockedBy != "the wing (audited)" || r.Source != "the wing" || n.Text != "9" || n.Source != "flight cart for production" {
		t.Fatalf("%+v %+v", r, n)
	}
	// Recording a run keeps what it used, and a re-run reads the original's.
	if err := s.RecordRun(ctx, "run_1", "production", []pipeline.BoundVariable{r, n}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunVariables(ctx, "run_1", "")
	if err != nil || len(got) != 2 || got[0].Name != "registry" || got[1].Display != "9" || got[1].Chain[1].Display != "3" {
		t.Fatalf("%+v %v", got, err)
	}
	// Later changes do not change what the run recorded.
	mustSet(t, s, setIn(LevelProject, "cart", "replicas", "production", VarNumber, "1"))
	got, _ = s.RunVariables(ctx, "run_2", "run_1")
	if len(got) != 2 || got[1].Display != "9" || got[1].RunID != "run_1" {
		t.Fatalf("%+v", got)
	}
}

func TestServiceChecksThatTheScopeExists(t *testing.T) {
	s := newVariableService(t)
	for _, in := range []SetInput{setIn(LevelProject, "nope", "a", "", VarString, `"x"`), setIn(LevelUnit, "nope", "a", "", VarString, `"x"`)} {
		if _, err := s.Set(context.Background(), "ada", in); !errors.Is(err, engine.ErrNotFound) || !strings.Contains(err.Error(), "nope") {
			t.Errorf("%v", err)
		}
	}
	if _, err := s.View(context.Background(), LevelProject, "nope", "", nil); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("%v", err)
	}
}
