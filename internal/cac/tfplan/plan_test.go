package tfplan

import (
	"os"
	"strings"
	"testing"
)

// The fixtures are real: `tofu show -json` of plans made with OpenTofu
// 1.10.10 and the random and null providers.

func load(t *testing.T, name string) *Plan {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreatePlan(t *testing.T) {
	p := load(t, "plan-create.json")
	if p.Counts.Add != 4 || p.Counts.Destroy != 0 || p.Risk.Level != "low" && p.Risk.Level != "none" {
		t.Fatalf("%+v %+v", p.Counts, p.Risk)
	}
	if p.Summary != "4 to add, 0 to change, 0 to destroy" {
		t.Fatal(p.Summary)
	}
}

func TestSensitiveValuesAreHidden(t *testing.T) {
	b, _ := os.ReadFile("testdata/plan-create.json")
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Resources {
		if r.Type != "random_password" {
			continue
		}
		for _, a := range r.Attrs {
			if a.Path == "result" || a.Path == "bcrypt_hash" {
				if !a.Sensitive || a.After != "" || a.Before != "" {
					t.Fatalf("%s shown: %+v", a.Path, a)
				}
			}
		}
	}
	out := p.Summary
	for _, r := range p.Resources {
		for _, a := range r.Attrs {
			out += a.Before + a.After
		}
	}
	if strings.Contains(out, "$2a$") {
		t.Fatal("a hash leaked")
	}
}

func TestReplacementsAndDestroysComeFirst(t *testing.T) {
	p := load(t, "plan-replace.json")
	if p.Risk.Level != "high" || len(p.Risk.Destroyed) != 1 || len(p.Risk.Replaced) != 2 {
		t.Fatalf("%+v", p.Risk)
	}
	if p.Resources[0].Action != Delete || p.Resources[1].Action != Replace {
		t.Fatalf("order %v %v", p.Resources[0].Action, p.Resources[1].Action)
	}
	if p.Counts.Replace != 2 || p.Counts.Destroy != 3 || p.Counts.Add != 2 {
		t.Fatalf("%+v", p.Counts)
	}
	if !strings.Contains(p.Summary, "2 of them replacements") {
		t.Fatal(p.Summary)
	}
	found := false
	for _, r := range p.Resources {
		for _, a := range r.Attrs {
			if a.ForcesReplacement {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no attribute is marked as forcing the replacement")
	}
}

func TestNotAPlan(t *testing.T) {
	if _, err := Parse([]byte(`{"a":1}`)); err == nil {
		t.Fatal("accepted")
	}
	if _, err := Parse([]byte(`x`)); err == nil {
		t.Fatal("accepted")
	}
}
