package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func newStore(t *testing.T) *VariableStore {
	t.Helper()
	return NewVariableStore(openEngine(t, testdb.New(t)))
}

func input(level Level, scope, name, env string, typ VarType, val string) SetInput {
	return SetInput{VarKey: VarKey{Level: level, Scope: scope, Name: name, Environment: env}, Type: typ, Value: json.RawMessage(val)}
}

func TestStoreKeepsOneTypePerName(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if _, _, err := s.Set(ctx, "ada", input(LevelOrganization, DefaultOrg, "replicas", "", VarNumber, "3")); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Set(ctx, "ada", input(LevelProject, "cart", "replicas", "", VarString, `"three"`))
	if !errors.Is(err, engine.ErrInvalid) || !strings.Contains(err.Error(), "replicas is number (set at organization)") {
		t.Fatalf("%v", err)
	}
	// The same name for an environment must agree with the general one.
	if _, _, err := s.Set(ctx, "ada", input(LevelOrganization, DefaultOrg, "replicas", "production", VarString, `"x"`)); err == nil {
		t.Fatal("an environment variable of another type was accepted")
	}
}

func TestStoreChecksNamesTypesAndValues(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	for _, tc := range []struct {
		in   SetInput
		want string
	}{
		{input(LevelProject, "cart", "Bad-Name", "", VarString, `"x"`), "not a valid variable name"},
		{input("galaxy", "", "a", "", VarString, `"x"`), "not a level"},
		{input(LevelProject, "cart", "a", "Prod!", VarString, `"x"`), "not a valid environment"},
		{input(LevelProject, "cart", "a", "", VarNumber, `"x"`), "takes a number"},
		{input(LevelProject, "cart", "a", "", VarBoolean, `"true"`), "true or false"},
		{input(LevelProject, "cart", "a", "", VarList, `[["a"]]`), "text, numbers and true or false"},
		{input(LevelProject, "cart", "a", "", VarSecret, `"hunter2"`), "a reference, not the secret"},
		{input(LevelProject, "cart", "a", "", VarSecret, `{"store":"vault","key":"a b"}`), "one word"},
		{input(LevelProject, "cart", "a", "", VarSecret, `{"store":"vault","key":"k","password":"x"}`), "store, key and field"},
		{input(LevelProject, "cart", "a", "", VarConnection, `"harbor"`), "of: registry"},
		{input(LevelProject, "cart", "a", "", "wat", `"x"`), "is not a type"},
		{input(LevelInstallation, "cart", "a", "", VarString, `"x"`), "no name to give"},
		{input(LevelProject, "", "a", "", VarString, `"x"`), "say which project"},
	} {
		if _, _, err := s.Set(ctx, "ada", tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.in.VarKey, err, tc.want)
		}
	}
	// What is valid is accepted in every type, and kept canonical.
	conn := input(LevelProject, "cart", "reg", "", VarConnection, `"harbor"`)
	conn.Of = "registry"
	for _, in := range []SetInput{
		input(LevelProject, "cart", "s", "", VarString, `"hello"`),
		input(LevelProject, "cart", "n", "", VarNumber, `2.50`),
		input(LevelProject, "cart", "b", "", VarBoolean, `true`),
		input(LevelProject, "cart", "l", "", VarList, `["a", 2, true]`),
		input(LevelProject, "cart", "m", "", VarMap, `{"b": 1, "a": "x"}`),
		input(LevelProject, "cart", "sec", "", VarSecret, `{"field":"token","key":"apps/x","store":"vault"}`),
		conn,
	} {
		if _, _, err := s.Set(ctx, "ada", in); err != nil {
			t.Fatalf("%s: %v", in.VarKey, err)
		}
	}
	v, _ := s.Get(ctx, VarKey{Level: LevelProject, Scope: "cart", Name: "m"})
	if string(v.Value) != `{"a":"x","b":1}` {
		t.Errorf("a map is kept with sorted keys: %s", v.Value)
	}
	v, _ = s.Get(ctx, VarKey{Level: LevelProject, Scope: "cart", Name: "sec"})
	if DisplayValue(v.Type, v.Value) != "vault:apps/x#token" {
		t.Errorf("a secret shows its reference: %s", DisplayValue(v.Type, v.Value))
	}
	if all, _ := s.All(ctx); len(all) != 7 {
		t.Errorf("%d variables kept, want 7", len(all))
	}
}

func TestStoreAuditsEveryChangeAndASecretShowsOnlyItsReference(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	key := VarKey{Level: LevelOrganization, Scope: DefaultOrg, Name: "token"}
	set := func(val string) {
		if _, _, err := s.Set(ctx, "ada", input(LevelOrganization, DefaultOrg, "token", "", VarSecret, val)); err != nil {
			t.Fatal(err)
		}
	}
	set(`{"store":"vault","key":"a/old"}`)
	set(`{"store":"vault","key":"a/new","field":"t"}`)
	set(`{"store":"vault","key":"a/new","field":"t"}`) // no change, no entry
	if _, err := s.SetLock(ctx, "boss", key, "one vault"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearLock(ctx, "boss", key); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "ada", key); err != nil {
		t.Fatal(err)
	}
	cs, err := s.Changes(ctx, "token", 0)
	if err != nil || len(cs) != 5 {
		t.Fatalf("%d changes: %+v %v", len(cs), cs, err)
	}
	// Newest first: delete, unlock, lock, set (old to new), set (first).
	if cs[0].Action != "delete" || cs[0].Old != "vault:a/new#t" || cs[1].Action != "unlock" || cs[1].Actor != "boss" ||
		cs[2].Action != "lock" || cs[2].Detail != "one vault" || cs[3].Old != "vault:a/old" || cs[3].New != "vault:a/new#t" || cs[4].Old != "" {
		t.Fatalf("%+v", cs)
	}
	if cs[4].Actor != "ada" || cs[4].At == 0 || cs[4].Level != LevelOrganization {
		t.Fatalf("%+v", cs[4])
	}
}

func TestStoreLockNeedsAReasonAndAVariable(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	key := VarKey{Level: LevelOrganization, Scope: DefaultOrg, Name: "a"}
	if _, err := s.SetLock(ctx, "ada", key, "why"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, _, err := s.Set(ctx, "ada", input(LevelOrganization, DefaultOrg, "a", "", VarString, `"x"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLock(ctx, "ada", key, ""); err == nil || !strings.Contains(err.Error(), "needs a reason") {
		t.Fatalf("%v", err)
	}
	// A lock survives a change of the value, and the lock's author is kept.
	v, err := s.SetLock(ctx, "ada", key, "because")
	if err != nil || v.Lock == nil || v.Lock.By != "ada" || v.Lock.At == 0 {
		t.Fatalf("%+v %v", v, err)
	}
	if v, _, _ = s.Set(ctx, "bo", input(LevelOrganization, DefaultOrg, "a", "", VarString, `"y"`)); v.Lock == nil || v.Version != 3 {
		t.Fatalf("%+v", v)
	}
}

// Nothing crosses organizations: every row belongs to one, and a read asks for it.
func TestStoreReadsOnlyItsOrganization(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if _, _, err := s.Set(ctx, "ada", input(LevelOrganization, DefaultOrg, "a", "", VarString, `"x"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE variables SET org = 'other'`); err != nil {
		t.Fatal(err)
	}
	if all, err := s.All(ctx); err != nil || len(all) != 0 {
		t.Fatalf("another organization's variable was read: %+v %v", all, err)
	}
}
