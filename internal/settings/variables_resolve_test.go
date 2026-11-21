package settings

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
)

func vv(level Level, scope, env string, t VarType, val string, lock string) Variable {
	v := Variable{Level: level, Scope: scope, Name: "replicas", Environment: env, Type: t, Value: json.RawMessage(val)}
	if lock != "" {
		v.Lock = &Lock{Reason: lock, By: "ada"}
	}
	return v
}

func resolveOne(t *testing.T, in ResolveInput) Resolved {
	t.Helper()
	in.Project = "cart"
	in.Names = []string{"replicas"}
	r := ResolveVariables(in)
	if len(r.Variables) != 1 {
		t.Fatalf("got %d variables: %+v", len(r.Variables), r)
	}
	return r.Variables[0]
}

func TestResolveDecidedEdgeCases(t *testing.T) {
	num := VarNumber
	cases := []struct {
		name     string
		in       ResolveInput
		want     string // display of the winner
		from     Level
		locked   bool
		contains string // in Why
	}{
		{"nearest level wins", ResolveInput{Unit: "pay", Variables: []Variable{
			vv(LevelInstallation, "", "", num, "1", ""), vv(LevelOrganization, DefaultOrg, "", num, "2", ""), vv(LevelUnit, "pay", "", num, "3", ""), vv(LevelProject, "cart", "", num, "4", "")}},
			"4", LevelProject, false, "nearest setting wins"},
		{"a project's own setting beats the organization's for the environment (ruling 3)", ResolveInput{Environment: "production", Variables: []Variable{
			vv(LevelOrganization, DefaultOrg, "production", num, "3", ""), vv(LevelProject, "cart", "", num, "2", "")}},
			"2", LevelProject, false, ""},
		{"at the same level the environment's variable beats the general one", ResolveInput{Environment: "staging", Variables: []Variable{
			vv(LevelProject, "cart", "", num, "2", ""), vv(LevelProject, "cart", "staging", num, "1", "")}},
			"1", LevelProject, false, "beats the general one"},
		{"a step with no environment sees only the general variable", ResolveInput{Variables: []Variable{
			vv(LevelProject, "cart", "", num, "2", ""), vv(LevelProject, "cart", "staging", num, "1", "")}},
			"2", LevelProject, false, ""},
		{"another environment's variable is not seen", ResolveInput{Environment: "production", Variables: []Variable{
			vv(LevelProject, "cart", "staging", num, "1", "")}}, "", "", false, "NONE"},
		{"another unit's variable does not reach this project", ResolveInput{Unit: "pay", Variables: []Variable{
			vv(LevelUnit, "other", "", num, "9", ""), vv(LevelUnit, "pay", "", num, "3", "")}},
			"3", LevelUnit, false, ""},
		{"another project's variable does not reach this one", ResolveInput{Variables: []Variable{
			vv(LevelProject, "web", "", num, "9", "")}}, "", "", false, "NONE"},
		{"a lock above beats a nearer project variable, which is kept but ignored", ResolveInput{Variables: []Variable{
			vv(LevelOrganization, DefaultOrg, "", num, "3", "audited"), vv(LevelProject, "cart", "", num, "2", "")}},
			"3", LevelOrganization, true, "Locked by the wing: audited"},
		{"a lock beats the file and the run", ResolveInput{Run: Given{"replicas": json.RawMessage("7")}, File: Given{"replicas": json.RawMessage("5")}, Variables: []Variable{
			vv(LevelInstallation, "", "", num, "3", "fleet rule")}},
			"3", LevelInstallation, true, ""},
		{"two locks: the topmost wins", ResolveInput{Unit: "pay", Variables: []Variable{
			vv(LevelInstallation, "", "", num, "1", "top"), vv(LevelUnit, "pay", "", num, "3", "lower")}},
			"1", LevelInstallation, true, "base"},
		{"an environment lock beats a general lock at the same level", ResolveInput{Environment: "production", Variables: []Variable{
			vv(LevelOrganization, DefaultOrg, "", num, "3", "general"), vv(LevelOrganization, DefaultOrg, "production", num, "4", "prod")}},
			"4", LevelOrganization, true, "prod"},
		{"the run beats the file, the file beats the levels", ResolveInput{Run: Given{"replicas": json.RawMessage("7")}, File: Given{"replicas": json.RawMessage("5")}, Variables: []Variable{
			vv(LevelProject, "cart", "", num, "2", "")}}, "7", "", false, "Given when the run started"},
		{"the file beats the project", ResolveInput{File: Given{"replicas": json.RawMessage("5")}, Variables: []Variable{
			vv(LevelProject, "cart", "", num, "2", "")}}, "5", "", false, "with:"},
		{"the template's default is the last resort", ResolveInput{Defaults: map[string]Default{"replicas": {num, json.RawMessage("1")}}}, "1", "", false, "template's own default"},
		{"a variable beats the template's default", ResolveInput{Defaults: map[string]Default{"replicas": {num, json.RawMessage("1")}}, Variables: []Variable{
			vv(LevelInstallation, "", "", num, "6", "")}}, "6", LevelInstallation, false, ""},
		{"lists replace, never merge", ResolveInput{Variables: []Variable{
			vv(LevelOrganization, DefaultOrg, "", VarList, `["a","b"]`, ""), vv(LevelProject, "cart", "", VarList, `["c"]`, "")}}, "c", LevelProject, false, ""},
		{"a locked secret reference is allowed and shown as the reference", ResolveInput{Variables: []Variable{
			vv(LevelOrganization, DefaultOrg, "", VarSecret, `{"store":"vault","key":"apps/x","field":"token"}`, "one vault")}},
			"vault:apps/x#token", LevelOrganization, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.Project, in.Names = "cart", []string{"replicas"}
			res := ResolveVariables(in)
			if tc.contains == "NONE" {
				if len(res.Variables) != 0 {
					t.Fatalf("expected nothing, got %+v", res.Variables)
				}
				return
			}
			r := resolveOne(t, tc.in)
			if r.Display != tc.want || r.From.Level != tc.from || (r.Locked != nil) != tc.locked {
				t.Fatalf("got %q from %q locked %v (%s), want %q from %q locked %v", r.Display, r.From.Level, r.Locked != nil, r.Why, tc.want, tc.from, tc.locked)
			}
			if !strings.Contains(r.Why, tc.contains) {
				t.Errorf("why %q lacks %q", r.Why, tc.contains)
			}
		})
	}
}

func TestLockedConflictsNameTheLevelAndWhoCanChangeIt(t *testing.T) {
	r := resolveOne(t, ResolveInput{Unit: "pay", File: Given{"replicas": json.RawMessage("5")}, Variables: []Variable{
		vv(LevelOrganization, DefaultOrg, "", VarNumber, "3", "audited")}})
	if len(r.Conflicts) != 1 {
		t.Fatalf("conflicts: %+v", r.Conflicts)
	}
	m := r.Conflicts[0].Message
	for _, want := range []string{"replicas is locked to 3 by the wing (audited)", "the pipeline file sets 5", "an administrator of every project"} {
		if !strings.Contains(m, want) {
			t.Errorf("message %q lacks %q", m, want)
		}
	}
	// The same value as the locked one is accepted, not a conflict.
	r = resolveOne(t, ResolveInput{File: Given{"replicas": json.RawMessage("3")}, Variables: []Variable{
		vv(LevelOrganization, DefaultOrg, "", VarNumber, "3", "audited")}})
	if len(r.Conflicts) != 0 {
		t.Errorf("an equal value is accepted: %+v", r.Conflicts)
	}
	// A unit lock names the unit.
	r = resolveOne(t, ResolveInput{Unit: "pay", Run: Given{"replicas": json.RawMessage("9")}, Variables: []Variable{
		vv(LevelUnit, "pay", "", VarNumber, "3", "pay rule")}})
	if m := r.Conflicts[0].Message; !strings.Contains(m, "squadron pay") || !strings.Contains(m, "an administrator of squadron pay") || !strings.Contains(m, "this run asked for 9") {
		t.Errorf("message %q", m)
	}
}

func TestIgnoredNearerSettingsAreKeptInTheChain(t *testing.T) {
	r := resolveOne(t, ResolveInput{Unit: "pay", Variables: []Variable{
		vv(LevelOrganization, DefaultOrg, "", VarNumber, "3", "audited"), vv(LevelUnit, "pay", "", VarNumber, "4", ""), vv(LevelProject, "cart", "", VarNumber, "2", "")}})
	if len(r.Chain) != 3 || r.Chain[0].Level != LevelOrganization || r.Chain[0].Overridden {
		t.Fatalf("chain: %+v", r.Chain)
	}
	for _, l := range r.Chain[1:] {
		if !l.Overridden || !strings.Contains(l.Ignored, "locked by the wing") {
			t.Errorf("link %+v should be ignored by the wing's lock", l)
		}
	}
}

// An independent, deliberately plain model of the rule, to hold the real
// function to: walk from the widest level to the nearest.
func oracle(all []Variable, env string, run, file *string, def *string) (value string, ok bool) {
	var vars []Variable
	for _, v := range all {
		if v.Scope != "other" && v.Scope != "web" { // not this unit's or project's
			vars = append(vars, v)
		}
	}
	at := func(l Level, wantLock bool) *Variable {
		var gen, e *Variable
		for i := range vars {
			v := &vars[i]
			if v.Level != l || (wantLock && v.Lock == nil) {
				continue
			}
			if v.Environment == "" {
				gen = v
			} else if env != "" && v.Environment == env {
				e = v
			}
		}
		if e != nil {
			return e
		}
		return gen
	}
	// The value a level gives is its environment variable, else its general one.
	value1 := func(l Level) *Variable { return at(l, false) }
	for _, l := range variableLevels { // widest first: the topmost lock wins
		if at(l, true) != nil {
			return string(value1(l).Value), true
		}
	}
	if run != nil {
		return *run, true
	}
	if file != nil {
		return *file, true
	}
	for i := len(variableLevels) - 1; i >= 0; i-- {
		if p := value1(variableLevels[i]); p != nil {
			return string(p.Value), true
		}
	}
	if def != nil {
		return *def, true
	}
	return "", false
}

func TestResolveMatchesTheRuleOnRandomSetups(t *testing.T) {
	rng := rand.New(rand.NewSource(44))
	scopes := map[Level]string{LevelInstallation: "", LevelOrganization: DefaultOrg, LevelUnit: "pay", LevelProject: "cart"}
	for i := 0; i < 3000; i++ {
		var vars []Variable
		n := 0
		for _, l := range variableLevels {
			for _, env := range []string{"", "production", "staging"} {
				if rng.Intn(3) != 0 {
					continue
				}
				lock := ""
				if rng.Intn(4) == 0 {
					lock = "r"
				}
				n++
				vars = append(vars, vv(l, scopes[l], env, VarNumber, fmt.Sprint(n), lock))
			}
		}
		// Another unit's and another project's variables must never count.
		vars = append(vars, vv(LevelUnit, "other", "", VarNumber, "900", "x"), vv(LevelProject, "web", "", VarNumber, "901", "x"))
		env := []string{"", "production", "staging"}[rng.Intn(3)]
		in := ResolveInput{Variables: vars, Project: "cart", Unit: "pay", Environment: env, Names: []string{"replicas"}}
		var run, file, def *string
		if rng.Intn(3) == 0 {
			s := "100"
			run = &s
			in.Run = Given{"replicas": json.RawMessage(s)}
		}
		if rng.Intn(3) == 0 {
			s := "200"
			file = &s
			in.File = Given{"replicas": json.RawMessage(s)}
		}
		if rng.Intn(3) == 0 {
			s := "300"
			def = &s
			in.Defaults = map[string]Default{"replicas": {VarNumber, json.RawMessage(s)}}
		}
		want, ok := oracle(vars, env, run, file, def)
		res := ResolveVariables(in)
		if !ok {
			if len(res.Variables) != 0 {
				t.Fatalf("setup %d: expected nothing, got %+v", i, res.Variables)
			}
			continue
		}
		if len(res.Variables) != 1 {
			t.Fatalf("setup %d: want a value %s, got %+v", i, want, res.Variables)
		}
		r := res.Variables[0]
		if string(r.Value) != want {
			t.Fatalf("setup %d (env %q): got %s from %+v, want %s\nvars: %+v", i, env, r.Value, r.From, want, vars)
		}
		// Invariants: one winner first, never from another unit or project,
		// no value of another environment, every other link overridden.
		if r.Chain[0].Overridden || string(r.Chain[0].Value) != want {
			t.Fatalf("setup %d: chain starts with %+v", i, r.Chain[0])
		}
		for _, l := range r.Chain[1:] {
			if !l.Overridden {
				t.Fatalf("setup %d: link %+v not overridden", i, l)
			}
		}
		for _, l := range r.Chain {
			s := string(l.Value)
			if s == "900" || s == "901" {
				t.Fatalf("setup %d: another unit's or project's variable reached the chain", i)
			}
			if l.Environment != "" && l.Environment != env {
				t.Fatalf("setup %d: a value for %s in the chain of %q", i, l.Environment, env)
			}
		}
		// A lock is never beaten by what is nearer: a conflict is exactly a
		// run or file value that differs from the winner.
		if r.Locked != nil {
			for _, c := range r.Conflicts {
				if string(c.Value) == want {
					t.Fatalf("setup %d: equal value reported as conflict", i)
				}
			}
		} else if len(r.Conflicts) != 0 {
			t.Fatalf("setup %d: conflicts without a lock", i)
		}
	}
}

func TestAllNamesAreResolvedWhenNoneIsAsked(t *testing.T) {
	vars := []Variable{vv(LevelProject, "cart", "", VarNumber, "2", ""), {Level: LevelUnit, Scope: "pay", Name: "registry", Type: VarConnection, Of: "registry", Value: json.RawMessage(`"harbor"`)}}
	res := ResolveVariables(ResolveInput{Variables: vars, Project: "cart", Unit: "pay"})
	if len(res.Variables) != 2 || res.Variables[0].Name != "registry" || res.Rule != VariableRule {
		t.Fatalf("%+v", res)
	}
}

// The rule is one sentence and must be the same everywhere it is written:
// the guide, the concept page, the references and the pipeline guide.
func TestTheRuleIsTheSameInEveryDocument(t *testing.T) {
	core := strings.TrimSuffix(VariableRule, " Lists replace, never merge. Variables and locks never cross Wings.")
	for _, f := range []string{"docs/guide/variables.md", "docs/learn/concepts/variables.md", "docs/reference/http-api.md", "docs/reference/cli.md"} {
		b, err := os.ReadFile("../../" + f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), core) {
			t.Errorf("%s does not hold the rule, word for word:\n%s", f, core)
		}
	}
	b, _ := os.ReadFile("../pipeline/guide/reference.md")
	for _, part := range []string{"the nearest setting wins", "a lock set at a level beats everything below it"} {
		if !strings.Contains(strings.ToLower(string(b)), part) {
			t.Errorf("the pipeline guide lacks %q", part)
		}
	}
}
