package settings

// The one rule for variables, and the one function that applies it. Every
// view, check and run that wants to know "which value, from where, and why"
// calls ResolveVariables; nothing else orders levels.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// VariableRule is the precedence rule, in the one sentence every page,
// message and document uses. A test holds the documents to it.
const VariableRule = "The nearest setting wins: what is given when a run starts, then the pipeline file's own `with:`, " +
	"then the project, the Squadron, the Wing, the Base, then the template's own default; " +
	"at the same level a variable set for the environment being deployed to beats the general one; " +
	"and a lock set at a level beats everything below it. Lists replace, never merge. Variables and locks never cross Wings."

// Kinds of source of a value, from the nearest to a run to the farthest.
const (
	SourceRun      = "run"
	SourceFile     = "file"
	SourceLevel    = "level"
	SourceDefault  = "default"
	linkIgnoredFmt = "ignored: locked by %s"
)

// Source says where a value comes from.
type Source struct {
	// Kind is run, file, level or default.
	Kind        string `json:"kind"`
	Level       Level  `json:"level,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// Link is one value a name has somewhere, in the chain from the nearest
// to the farthest.
type Link struct {
	Source
	Value   json.RawMessage `json:"value"`
	Display string          `json:"display"`
	// Locked: this value carries a lock.
	Locked bool `json:"locked,omitempty"`
	// Overridden: a nearer value (or a lock above) wins over this one.
	Overridden bool `json:"overridden,omitempty"`
	// Ignored: a lock above makes this value not count; it is kept, and
	// counts again when the lock is removed.
	Ignored string `json:"ignored,omitempty"`
}

// Conflict is a value a run or a pipeline file gave that a lock refuses.
type Conflict struct {
	Source
	Value   json.RawMessage `json:"value"`
	Display string          `json:"display"`
	// Message is the plain sentence naming the level that locked it and who
	// can change it.
	Message string `json:"message"`
}

// LockInfo says where a variable is locked and why.
type LockInfo struct {
	Level       Level  `json:"level"`
	Scope       string `json:"scope,omitempty"`
	Environment string `json:"environment,omitempty"`
	Reason      string `json:"reason"`
	By          string `json:"by,omitempty"`
	// Who says who can change it: the administrator of that level.
	Who string `json:"who"`
}

// Resolved is a variable in effect: the winning value, where it comes from,
// what it overrides, and why.
type Resolved struct {
	Name        string          `json:"name"`
	Type        VarType         `json:"type"`
	Of          string          `json:"of,omitempty"`
	Description string          `json:"description,omitempty"`
	Value       json.RawMessage `json:"value"`
	// Display is the value in the words people read; a secret shows the
	// reference.
	Display string `json:"display"`
	From    Source `json:"from"`
	// Chain lists every value the name has, the winner first, then those it
	// overrides from the nearest to the farthest.
	Chain  []Link    `json:"chain"`
	Locked *LockInfo `json:"locked,omitempty"`
	// Why is the rule that applied, in a sentence.
	Why       string     `json:"why"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// Resolution is every variable in effect for a project.
type Resolution struct {
	Rule        string     `json:"rule"`
	Project     string     `json:"project"`
	Unit        string     `json:"unit,omitempty"`
	Environment string     `json:"environment,omitempty"`
	Variables   []Resolved `json:"variables"`
}

// Given is a value given for a name in a run or in a pipeline file.
type Given map[string]json.RawMessage

// Default is a template's own default for a name.
type Default struct {
	Type  VarType
	Value json.RawMessage
}

// ResolveInput is what to resolve.
type ResolveInput struct {
	// Variables are the variables of the organization.
	Variables []Variable
	Project   string
	// Unit is the unit the project is in, "" if none.
	Unit string
	// Environment is the environment being deployed to, "" for none: a step
	// with no environment sees only the general variables.
	Environment string
	// Names narrows the answer to these names (what a template takes);
	// empty means every variable in effect.
	Names []string
	// Run and File are values given when a run starts and in the pipeline
	// file's with:; Defaults are templates' own defaults.
	Run, File Given
	Defaults  map[string]Default
	// Within, if set, ignores the levels nearer than it: the view of what a
	// level inherits, and the check for a lock above a level.
	Within Level
	// Words are the names of the levels in messages; empty uses the default
	// set.
	Words Levels
}

// levelPhrase names a level in a sentence, with its scope.
func (in ResolveInput) levelPhrase(l Level, scope string) string {
	w := in.Words
	if w.Installation.Singular == "" {
		w = DefaultLevels
	}
	lower := func(n LevelName) string { return strings.ToLower(n.Singular) }
	switch l {
	case LevelInstallation:
		return "the " + lower(w.Installation)
	case LevelOrganization:
		return "the " + lower(w.Organisation)
	case LevelUnit:
		return lower(w.Unit) + " " + scope
	}
	return lower(w.Project) + " " + scope
}

// whoCan says who can change a lock at a level.
func (in ResolveInput) whoCan(l Level, scope string) string {
	switch l {
	case LevelInstallation, LevelOrganization:
		return "an administrator of every project"
	case LevelUnit:
		return "an administrator of " + in.levelPhrase(l, scope)
	}
	return "an administrator of " + scope
}

// applies reports whether a variable set at (level, scope) reaches the
// project.
func (in ResolveInput) applies(v Variable) bool {
	if in.Within != "" && v.Level.rank() > in.Within.rank() {
		return false
	}
	switch v.Level {
	case LevelInstallation, LevelOrganization:
		return true
	case LevelUnit:
		return in.Unit != "" && v.Scope == in.Unit
	case LevelProject:
		return v.Scope == in.Project
	}
	return false
}

type levelPick struct {
	level  Level
	scope  string
	chosen Variable   // what the level gives: the environment's variable, else the general one
	extra  []Variable // the general variable the environment's beats, at the same level
	lock   *Variable  // the variable whose lock binds below
}

// ResolveVariables applies the rule: for each name, the value in effect and
// the whole chain behind it. It is pure: it reads only its input.
func ResolveVariables(in ResolveInput) *Resolution {
	res := &Resolution{Rule: VariableRule, Project: in.Project, Unit: in.Unit, Environment: in.Environment, Variables: []Resolved{}}
	byName := map[string][]Variable{}
	for _, v := range in.Variables {
		if in.applies(v) && (v.Environment == "" || (in.Environment != "" && v.Environment == in.Environment)) {
			byName[v.Name] = append(byName[v.Name], v)
		}
	}
	names := map[string]bool{}
	if len(in.Names) > 0 {
		for _, n := range in.Names {
			names[n] = true
		}
	} else {
		for n := range byName {
			names[n] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		if r, ok := in.resolveOne(n, byName[n]); ok {
			res.Variables = append(res.Variables, r)
		}
	}
	return res
}

// picks groups the variables that apply by level, nearest first.
func picks(vs []Variable) []levelPick {
	var out []levelPick
	for i := len(variableLevels) - 1; i >= 0; i-- {
		l := variableLevels[i]
		var env, gen *Variable
		var scope string
		for j := range vs {
			if vs[j].Level != l {
				continue
			}
			scope = vs[j].Scope
			if vs[j].Environment == "" {
				gen = &vs[j]
			} else {
				env = &vs[j]
			}
		}
		if env == nil && gen == nil {
			continue
		}
		p := levelPick{level: l, scope: scope}
		switch {
		case env != nil:
			p.chosen = *env
			if gen != nil {
				p.extra = []Variable{*gen}
			}
		default:
			p.chosen = *gen
		}
		// The lock that binds below: the environment's beats the general
		// one at the same level.
		switch {
		case env != nil && env.Lock != nil:
			p.lock = env
		case gen != nil && gen.Lock != nil:
			p.lock = gen
		}
		out = append(out, p)
	}
	return out
}

func varLink(v Variable, overridden bool) Link {
	return Link{Source: Source{Kind: SourceLevel, Level: v.Level, Scope: v.Scope, Environment: v.Environment},
		Value: v.Value, Display: DisplayValue(v.Type, v.Value), Locked: v.Lock != nil, Overridden: overridden}
}

func (in ResolveInput) resolveOne(name string, vs []Variable) (Resolved, bool) {
	ps := picks(vs)
	r := Resolved{Name: name}
	var typ VarType
	for _, v := range vs {
		typ, r.Of, r.Description = v.Type, v.Of, firstNonEmpty(r.Description, v.Description)
	}
	def, hasDef := in.Defaults[name]
	if typ == "" {
		if !hasDef {
			if _, ok := in.Run[name]; !ok {
				if _, ok := in.File[name]; !ok {
					return r, false
				}
			}
		}
		typ = def.Type
	}
	r.Type = typ
	// The topmost lock beats everything below it.
	var locked *levelPick
	for i := len(ps) - 1; i >= 0; i-- {
		if ps[i].lock != nil {
			locked = &ps[i]
			break
		}
	}
	mk := func(kind string, raw json.RawMessage) Link {
		return Link{Source: Source{Kind: kind}, Value: raw, Display: DisplayValue(typ, raw)}
	}
	var links []Link
	if raw, ok := in.Run[name]; ok {
		links = append(links, mk(SourceRun, raw))
	}
	if raw, ok := in.File[name]; ok {
		links = append(links, mk(SourceFile, raw))
	}
	for _, p := range ps {
		links = append(links, varLink(p.chosen, false))
		for _, x := range p.extra {
			links = append(links, varLink(x, true))
		}
	}
	if hasDef {
		links = append(links, mk(SourceDefault, def.Value))
	}
	if len(links) == 0 {
		return r, false
	}
	winner := 0
	if locked != nil {
		lv := *locked.lock
		li := &LockInfo{Level: lv.Level, Scope: lv.Scope, Environment: lv.Environment, Reason: lv.Lock.Reason, By: lv.Lock.By, Who: in.whoCan(lv.Level, lv.Scope)}
		r.Locked = li
		// The locked level's own value wins; a nearer one is ignored.
		for i, l := range links {
			if l.Kind == SourceLevel && l.Level == locked.level && l.Environment == locked.chosen.Environment {
				winner = i
				break
			}
		}
		by := in.levelPhrase(lv.Level, lv.Scope)
		for i := range links {
			if i < winner {
				links[i].Overridden = true
				if links[i].Kind == SourceLevel {
					links[i].Ignored = fmt.Sprintf(linkIgnoredFmt, by)
				}
			}
		}
		var keep []Conflict
		for _, l := range links[:winner] {
			if (l.Kind == SourceRun || l.Kind == SourceFile) && !bytes.Equal(l.Value, links[winner].Value) {
				keep = append(keep, Conflict{Source: l.Source, Value: l.Value, Display: l.Display, Message: in.conflictMessage(name, l, links[winner], li)})
			}
		}
		r.Conflicts = keep
	}
	for i := range links {
		if i > winner {
			links[i].Overridden = true
		}
	}
	w := links[winner]
	r.Value, r.Display, r.From = w.Value, w.Display, w.Source
	r.Chain = append([]Link{w}, append(append([]Link{}, links[:winner]...), links[winner+1:]...)...)
	r.Chain[0].Overridden = false
	r.Why = in.why(r, links, winner)
	return r, true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (in ResolveInput) conflictMessage(name string, got Link, locked Link, li *LockInfo) string {
	what := pipeline.AskedByRun
	if got.Kind == SourceFile {
		what = pipeline.SetByFile
	}
	by := in.levelPhrase(li.Level, li.Scope)
	if li.Reason != "" {
		by += " (" + li.Reason + ")"
	}
	// One wording, shared with the pipeline's own check.
	return pipeline.LockedMessage(name, pipeline.BoundVariable{Display: locked.Display, LockedBy: by, Who: li.Who}, what, got.Display)
}

// why says in a sentence which part of the rule decided.
func (in ResolveInput) why(r Resolved, links []Link, winner int) string {
	w := links[winner]
	if r.Locked != nil {
		by := in.levelPhrase(r.Locked.Level, r.Locked.Scope)
		s := "Locked by " + by
		if r.Locked.Reason != "" {
			s += ": " + r.Locked.Reason
		}
		if winner > 0 {
			s += "; nearer settings are kept but ignored"
		}
		return s + "."
	}
	switch w.Kind {
	case SourceRun:
		return "Given when the run started: the nearest setting wins."
	case SourceFile:
		return "Set in the pipeline file's with:, which beats every level."
	case SourceDefault:
		return "Nothing sets it at any level, so the template's own default is used."
	}
	at := in.levelPhrase(w.Level, w.Scope)
	s := "Set by " + at
	if w.Environment != "" {
		s += " for " + w.Environment
	}
	s += ": the nearest setting wins"
	if len(links) > 1 {
		hasEnv := false
		for _, l := range links[winner+1:] {
			hasEnv = hasEnv || (l.Kind == SourceLevel && l.Level == w.Level && l.Environment == "" && w.Environment != "")
		}
		if hasEnv {
			s += "; at the same level the variable for " + w.Environment + " beats the general one"
		}
	}
	return s + "."
}
