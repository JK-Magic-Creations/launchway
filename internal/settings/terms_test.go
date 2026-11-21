package settings

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

func TestTermsCheck(t *testing.T) {
	ok := []Terms{{}, {Set: "aviation"}, {Set: "business", Overrides: map[string]string{"guide": "Coach", "unit_plural": "Crews"}}}
	for _, v := range ok {
		if err := v.check(); err != nil {
			t.Errorf("%+v: %v", v, err)
		}
	}
	bad := []Terms{
		{Set: "pirate"},
		{Overrides: map[string]string{"nonsense": "x"}},
		{Overrides: map[string]string{"guide": ""}},
		{Overrides: map[string]string{"guide": "<b>x</b>"}},
		{Overrides: map[string]string{"guide": strings.Repeat("x", 41)}},
		{Overrides: map[string]string{"guide": "two\nlines"}},
	}
	for _, v := range bad {
		if err := v.check(); err == nil {
			t.Errorf("%+v was accepted", v)
		}
	}
}

func TestLevelsFollowTheSetAndOverrides(t *testing.T) {
	if got := (Terms{}).levelsFor(Levels{}); got != DefaultLevels || got.Unit.Singular != "Squadron" {
		t.Fatalf("default: %+v", got)
	}
	b := Terms{Set: "business"}.levelsFor(Levels{})
	if b.Organisation.Singular != "Organization" || b.Unit.Plural != "Teams" {
		t.Fatalf("business: %+v", b)
	}
	o := Terms{Set: "aviation", Overrides: map[string]string{"unit": "Flight", "organization": "Company", "installation_plural": "Sites"}}.levelsFor(Levels{})
	if o.Unit.Plural != "Flights" || o.Organisation.Plural != "Companies" || o.Installation.Singular != "Base" || o.Installation.Plural != "Sites" {
		t.Fatalf("overrides: %+v", o)
	}
	// An older explicit levels entry still applies under the set; an override beats it.
	l := Terms{}.levelsFor(Levels{Unit: LevelName{Singular: "Group"}})
	if l.Unit.Singular != "Group" || l.Unit.Plural != "Groups" {
		t.Fatalf("legacy: %+v", l.Unit)
	}
}

func TestKindByPathAcceptsTheAmericanSpelling(t *testing.T) {
	if k := KindByPath("organization"); k == nil || k.Name != "organisation" {
		t.Fatalf("got %+v", k)
	}
}

// The web app holds the labels; every term the server accepts must be there.
func TestEveryTermKeyHasALabelInTheWebApp(t *testing.T) {
	b, err := os.ReadFile("../../web/src/terms.ts")
	if err != nil {
		t.Skip("web sources not present")
	}
	for _, k := range TermKeys {
		if !strings.Contains(string(b), "  "+k+":") {
			t.Errorf("web/src/terms.ts has no label for term %q", k)
		}
	}
}

func TestComposeReportsTheTermsAndTheLevelsTheyGive(t *testing.T) {
	boot := &config.Config{}
	mk := func(spec string) []engine.ConfigObject {
		return []engine.ConfigObject{{Kind: "installation", Name: "installation", Spec: json.RawMessage(spec)}}
	}
	_, lay, err := Compose(boot, mk(`{"terms":{"set":"business","overrides":{"unit":"Crew"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if lay.Terms.Set != "business" || lay.Levels.Unit.Singular != "Crew" || lay.Levels.Organisation.Singular != "Organization" {
		t.Fatalf("got %+v %+v", lay.Terms, lay.Levels)
	}
	_, lay, err = Compose(boot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lay.Terms.Set != "aviation" || lay.Levels.Installation.Singular != "Base" || lay.Terms.Overrides == nil {
		t.Fatalf("defaults: %+v %+v", lay.Terms, lay.Levels)
	}
}
