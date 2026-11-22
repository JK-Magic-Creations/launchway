package refrules

import (
	"strings"
	"testing"
)

func TestTagRulesDefaultsAndValidation(t *testing.T) {
	rs := []TagRule{{Pattern: "v*"}}
	if err := ValidateTagRules(rs); err != nil || rs[0].CreateRole != RoleAdmin || rs[0].ChangeRole != RoleNobody || rs[0].Pipelines == nil {
		t.Fatalf("%+v %v", rs, err)
	}
	for _, bad := range []TagRule{{Pattern: ""}, {Pattern: "refs/tags/v*"}, {Pattern: "v[1"}, {Pattern: "v*", CreateRole: "viewer"}, {Pattern: "v*", ChangeRole: "operator"}, {Pattern: "v*", Pipelines: []string{"*"}}} {
		if err := ValidateTagRules([]TagRule{bad}); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if err := ValidateTagRules([]TagRule{{Pattern: "v*"}, {Pattern: "v*"}}); err == nil {
		t.Error("a pattern twice was accepted")
	}
}

func TestCheckTags(t *testing.T) {
	rules := []TagRule{{Pattern: "v*", CreateRole: RoleAdmin, Pipelines: []string{"ship-*"}}, {Pattern: "rc-*", CreateRole: RoleOperator, ChangeRole: RoleAdmin}}
	mk := func(name, old, new string) []Update { return []Update{{Name: "refs/tags/" + name, Old: old, New: new}} }
	tests := []struct {
		name string
		up   []Update
		who  TagActor
		want string // a word of the refusal, empty for allowed
	}{
		{"unprotected tag, anyone", mk("nightly", zero, "a"), TagActor{}, ""},
		{"a branch is not looked at", []Update{{Name: "refs/heads/v1", Old: zero, New: "a"}}, TagActor{}, ""},
		{"operator may not create v*", mk("v1.0", zero, "a"), TagActor{}, "only an administrator"},
		{"admin may create v*", mk("v1.0", zero, "a"), TagActor{Admin: true}, ""},
		{"empty old id is a creation", mk("v1.0", "", "a"), TagActor{}, "only an administrator"},
		{"named pipeline may create", mk("v1.0", zero, "a"), TagActor{Pipeline: "ship-web"}, ""},
		{"other pipeline may not", mk("v1.0", zero, "a"), TagActor{Pipeline: "web"}, "not named"},
		{"nobody deletes v*, not even admin", mk("v1.0", "a", zero), TagActor{Admin: true}, "cannot be deleted"},
		{"nobody moves v*", mk("v1.0", "a", "b"), TagActor{Admin: true}, "cannot be moved"},
		{"a named pipeline never moves", mk("v1.0", "a", "b"), TagActor{Pipeline: "ship-web"}, "cannot be moved"},
		{"operator creates rc-*", mk("rc-1", zero, "a"), TagActor{}, ""},
		{"operator may not move rc-*", mk("rc-1", "a", "b"), TagActor{}, "only an administrator"},
		{"admin moves rc-*", mk("rc-1", "a", "b"), TagActor{Admin: true}, ""},
		{"admin deletes rc-*", mk("rc-1", "a", zero), TagActor{Admin: true}, ""},
	}
	for _, tc := range tests {
		err := CheckTags(rules, tc.up, tc.who)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want a refusal with %q", tc.name, err, tc.want)
		}
	}
}
