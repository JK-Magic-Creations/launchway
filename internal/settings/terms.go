package settings

import (
	"fmt"
	"slices"
	"strings"
)

// Terms are the words Launchway shows for its own concepts: a chosen set and
// the single words an installation renamed. They are display labels only. API
// paths, JSON fields, pipeline keys, CLI commands, configuration kind names,
// audit actions and database columns keep their plain forms, so nothing a
// script depends on is ever a label.
//
// The two built-in sets (aviation, the default, and business) and every word
// live in ONE table, vocabulary.go; the web app receives it in the overview and
// the command line and the sentences the server builds read it through Words.
type Terms struct {
	// Set is "aviation" (the default, also when empty) or "business".
	Set string `yaml:"set" json:"set"`
	// Overrides rename single terms: the key is a term (see TermKeys), the
	// value its new singular label. A key followed by _plural renames the
	// plural, for the words that have one.
	Overrides map[string]string `yaml:"overrides" json:"overrides"`
}

// TermSets are the built-in sets, the first being the default.
var TermSets = []string{"aviation", "business"}

// termLevels are the four level words of each set, singular and plural, read from the vocabulary table.
func termLevels(set string) Levels {
	lv := func(key string) LevelName {
		w, _ := VocabularyWord(key)
		f := w.forms(set)
		return LevelName{f.One, f.plural()}
	}
	return Levels{Installation: lv("installation"), Organisation: lv("organization"), Unit: lv("unit"), Project: lv("project")}
}

// maxTermLen is the longest label.
const maxTermLen = 40

func (t Terms) check() error {
	if t.Set != "" && !slices.Contains(TermSets, t.Set) {
		return fmt.Errorf("terms.set %q: one of %s", t.Set, strings.Join(TermSets, ", "))
	}
	for k, v := range t.Overrides {
		base := strings.TrimSuffix(k, "_plural")
		if !slices.Contains(TermKeys, base) {
			return fmt.Errorf("terms.overrides: %q is not a term that can be renamed (%s; add _plural for a plural)", k, strings.Join(TermKeys, ", "))
		}
		if v == "" || len(v) > maxTermLen || strings.ContainsAny(v, "<>&\n\r\t") {
			return fmt.Errorf("terms.overrides.%s: %q: 1 to %d characters, on one line, without < > or &", k, v, maxTermLen)
		}
	}
	return nil
}

// levelsFor resolves the level names: a legacy levels entry the installation
// set, else the override, else the chosen set's word.
func (t Terms) levelsFor(explicit Levels) Levels {
	set := t.Set
	if set == "" {
		set = TermSets[0]
	}
	d := termLevels(set)
	pick := func(key string, ex, def LevelName) LevelName {
		out := def
		if ex.Singular != "" {
			out.Singular = ex.Singular
			out.Plural = pluralOf(ex.Singular)
		}
		if ex.Plural != "" {
			out.Plural = ex.Plural
		}
		if v := t.Overrides[key]; v != "" {
			out.Singular = v
			out.Plural = pluralOf(v)
		}
		if v := t.Overrides[key+"_plural"]; v != "" {
			out.Plural = v
		}
		return out
	}
	return Levels{
		Installation: pick("installation", explicit.Installation, d.Installation),
		Organisation: pick("organization", explicit.Organisation, d.Organisation),
		Unit:         pick("unit", explicit.Unit, d.Unit),
		Project:      pick("project", explicit.Project, d.Project),
	}
}

func pluralOf(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.HasSuffix(l, "y") && len(l) > 1 && !strings.ContainsRune("aeiou", rune(l[len(l)-2])):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(l, "s"), strings.HasSuffix(l, "x"), strings.HasSuffix(l, "ch"), strings.HasSuffix(l, "sh"):
		return s + "es"
	}
	return s + "s"
}

// normal fills the set and an empty map, so the overview always says which set is in effect.
func (t Terms) normal() Terms {
	if t.Set == "" {
		t.Set = TermSets[0]
	}
	if t.Overrides == nil {
		t.Overrides = map[string]string{}
	}
	return t
}
