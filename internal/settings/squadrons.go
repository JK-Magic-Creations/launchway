package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Squadrons and Flights: the folder (stored as a unit) and the repository's pipelines (stored as a project).
// Nothing here is stored beyond the objects that already exist: a move changes the `unit` of a project, and a
// preview only reads. See docs/guide/squadrons-and-flights.md.

// UnitNameProblem says in a sentence what is wrong with a name given to a new Squadron, or "" when it is fine.
func UnitNameProblem(name string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "Give the Squadron a name."
	case name != strings.TrimSpace(name):
		return "A name does not start or end with a space."
	case len(name) > 100:
		return "A name is at most 100 characters."
	case !ValidName(name):
		return "A name uses letters, digits, '.', '_' and '-', and starts with a letter or a digit. Write the longer name you want people to read as the display name."
	}
	return ""
}

// MoveEffect is one shared setting that stops or starts applying to a Flight because it changed home.
type MoveEffect struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// From says where it is defined: "organization" or "unit".
	From string `json:"from"`
	// Note says in a sentence what changes for the Flight.
	Note string `json:"note,omitempty"`
}

// MoveVariable is a variable whose value for the Flight differs, appears or goes away.
type MoveVariable struct {
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// MoveSecret is one secret store a reference of the Flight names, and how the name resolves in each home.
type MoveSecret struct {
	Store string `json:"store"`
	// Before and After say where the name resolves: "the Squadron NAME", "the Wing" or "nowhere".
	Before string `json:"before"`
	After  string `json:"after"`
	// Result is "same" (the same store), "different" (another store answers to the name) or "none".
	Result string `json:"result"`
}

// MoveAccess is a person or group whose ability to start runs of the Flight changes.
type MoveAccess struct {
	Who    string `json:"who"`
	Before string `json:"before"`
	After  string `json:"after"`
	// Effect is "gains" or "loses" the ability to start runs.
	Effect string `json:"effect"`
}

// MovePlan is what a move of one Flight would change, worked out without changing anything.
type MovePlan struct {
	Flight    string `json:"flight"`
	From      string `json:"from"`
	To        string `json:"to"`
	Unchanged bool   `json:"unchanged,omitempty"`
	// Stops and Starts are the shared settings that no longer apply and that begin to apply.
	Stops     []MoveEffect   `json:"stops"`
	Starts    []MoveEffect   `json:"starts"`
	Variables []MoveVariable `json:"variables"`
	Secrets   []MoveSecret   `json:"secrets"`
	Access    []MoveAccess   `json:"access"`
	// ReferencesFrom says how the secret references were found: "the pipeline file" or "assumed" (the file could not be read, so every store either home offers counts).
	ReferencesFrom string `json:"references_from"`
	Blocked        bool   `json:"blocked"`
	// Reasons say, in sentences, why the move is blocked.
	Reasons []string `json:"reasons"`
}

var storeRef = regexp.MustCompile(`(?m)\bstore:\s*["']?([A-Za-z0-9][A-Za-z0-9._-]*)`)

// SecretStoresNamed lists the secret store names a pipeline file's references mention.
func SecretStoresNamed(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range storeRef.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// PlanMoves works out, for each Flight, what moving it to the Squadron `to` ("" is Unfiled) would change.
// pipelineText returns the Flight's pipeline file, or false when it cannot be read.
func (s *Service) PlanMoves(ctx context.Context, flights []string, to string, pipelineText func(string) (string, bool)) ([]MovePlan, error) {
	return s.planMoves(ctx, flights, to, nil, pipelineText)
}

// planMoves is PlanMoves where `coming` names Squadrons that an organize will create in the same change.
func (s *Service) planMoves(ctx context.Context, flights []string, to string, coming map[string]bool, pipelineText func(string) (string, bool)) ([]MovePlan, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if len(flights) == 0 {
		return nil, invalid(fmt.Errorf("choose at least one Flight to move"))
	}
	if len(flights) > 200 {
		return nil, invalid(fmt.Errorf("move at most 200 Flights at once"))
	}
	_, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return nil, err
	}
	_, lay, err := Compose(s.boot, objs)
	if err != nil {
		return nil, err
	}
	if to != "" && !unitExists(lay, to) && !coming[to] {
		return nil, fmt.Errorf("%w: Squadron %s", engine.ErrNotFound, to)
	}
	live := liveObjects(objs)
	vars, err := s.Variables().Store.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []MovePlan
	seen := map[string]bool{}
	for _, f := range flights {
		if seen[f] {
			continue
		}
		seen[f] = true
		from, ok := lay.ProjectUnit[f]
		if !ok {
			return nil, fmt.Errorf("%w: Flight %s", engine.ErrNotFound, f)
		}
		p := MovePlan{Flight: f, From: from, To: to, Stops: []MoveEffect{}, Starts: []MoveEffect{}, Variables: []MoveVariable{}, Secrets: []MoveSecret{}, Access: []MoveAccess{}, Reasons: []string{}}
		if from == to {
			p.Unchanged = true
			out = append(out, p)
			continue
		}
		before, after := objectsFor(live, f, from), objectsFor(live, f, to)
		p.Stops, p.Starts = diffEntries(before, after)
		p.Variables = moveVariables(vars, f, from, to, lay)
		p.Access = moveAccess(live, before, after)
		var text string
		var have bool
		if pipelineText != nil {
			text, have = pipelineText(f)
		}
		p.ReferencesFrom = "the pipeline file"
		var names []string
		if have {
			names = SecretStoresNamed(text)
		} else {
			p.ReferencesFrom = "assumed"
			names = storeNames(before, after)
		}
		p.Secrets = moveSecrets(live, names, f, from, to)
		for _, sc := range p.Secrets {
			switch sc.Result {
			case "different":
				p.Reasons = append(p.Reasons, fmt.Sprintf("The secret store %q would answer from a different place: %s now, %s after the move. A reference to it would read another store's secrets. Give the new Squadron a store with the same definition, or change the reference, then move.", sc.Store, sc.Before, sc.After))
			case "none":
				p.Reasons = append(p.Reasons, fmt.Sprintf("The secret store %q is defined for the Flight now (%s) and would be defined nowhere after the move, so a reference to it would stop resolving.", sc.Store, sc.Before))
			}
		}
		p.Blocked = len(p.Reasons) > 0
		out = append(out, p)
	}
	return out, nil
}

func unitExists(lay *Layout, name string) bool {
	for _, u := range lay.Units {
		if u.Name == name {
			return true
		}
	}
	return false
}

func liveObjects(objs []engine.ConfigObject) []engine.ConfigObject {
	live := make([]engine.ConfigObject, 0, len(objs))
	for _, o := range objs {
		if !o.Deleted() {
			live = append(live, o)
		}
	}
	sortObjects(live)
	return live
}

// objectsFor lists the shared objects (not the Flight itself) that apply to a Flight if it is in `unit`.
func objectsFor(live []engine.ConfigObject, project, unit string) []EffectiveEntry {
	var out []EffectiveEntry
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
		out = append(out, EffectiveEntry{Kind: o.Kind, Name: o.Name, From: from, Unit: o.Unit})
	}
	return out
}

func entryKey(e EffectiveEntry) string { return e.Kind + "/" + e.Name + "@" + e.Unit }

func diffEntries(before, after []EffectiveEntry) (stops, starts []MoveEffect) {
	stops, starts = []MoveEffect{}, []MoveEffect{}
	has := func(l []EffectiveEntry, e EffectiveEntry) bool {
		for _, x := range l {
			if entryKey(x) == entryKey(e) {
				return true
			}
		}
		return false
	}
	words := func(e EffectiveEntry) string {
		if e.Unit != "" {
			return "defined for the Squadron " + e.Unit
		}
		return "defined for the Wing"
	}
	for _, e := range before {
		if !has(after, e) {
			stops = append(stops, MoveEffect{Kind: e.Kind, Name: e.Name, From: levelOf(e), Note: "stops applying: " + words(e)})
		}
	}
	for _, e := range after {
		if !has(before, e) {
			starts = append(starts, MoveEffect{Kind: e.Kind, Name: e.Name, From: levelOf(e), Note: "starts applying: " + words(e)})
		}
	}
	return stops, starts
}

func levelOf(e EffectiveEntry) string {
	if e.Unit != "" {
		return "unit"
	}
	return "organization"
}

func moveVariables(all []Variable, project, from, to string, lay *Layout) []MoveVariable {
	resolve := func(unit string) map[string]string {
		res := ResolveVariables(ResolveInput{Variables: all, Project: project, Unit: unit, Words: lay.Levels})
		m := map[string]string{}
		for _, r := range res.Variables {
			m[r.Name] = r.Display
		}
		return m
	}
	b, a := resolve(from), resolve(to)
	names := map[string]bool{}
	for n := range b {
		names[n] = true
	}
	for n := range a {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	out := []MoveVariable{}
	for _, n := range sorted {
		bv, bok := b[n]
		av, aok := a[n]
		if bok && aok && bv == av {
			continue
		}
		if !bok {
			bv = "not set"
		}
		if !aok {
			av = "not set"
		}
		out = append(out, MoveVariable{Name: n, Before: bv, After: av})
	}
	return out
}

func storeNames(lists ...[]EffectiveEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, e := range l {
			if e.Kind == "secret-store" && !seen[e.Name] {
				seen[e.Name] = true
				out = append(out, e.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// resolveStore says where a store name answers for a Flight in `unit`: the Squadron's own definition wins over the Wing's.
func resolveStore(live []engine.ConfigObject, name, project, unit string) (where string, spec []byte) {
	var wing *engine.ConfigObject
	for i := range live {
		o := &live[i]
		if o.Kind != "secret-store" || o.Name != name {
			continue
		}
		if o.Unit != "" && o.Unit != unit {
			continue
		}
		v, _, err := decodeSpec(kindByName(o.Kind), o.Name, o.Spec)
		if err != nil {
			continue
		}
		if pats, all := projectsOf(v); !all && !matchAny(pats, project) {
			continue
		}
		if o.Unit != "" {
			return "the Squadron " + o.Unit, o.Spec
		}
		wing = o
	}
	if wing != nil {
		return "the Wing", wing.Spec
	}
	return "nowhere", nil
}

func moveSecrets(live []engine.ConfigObject, names []string, project, from, to string) []MoveSecret {
	out := []MoveSecret{}
	for _, n := range names {
		bw, bs := resolveStore(live, n, project, from)
		aw, as := resolveStore(live, n, project, to)
		if bw == "nowhere" && aw == "nowhere" {
			continue // not a store this installation defines (a Station's own store, say): a move changes nothing for it
		}
		res := "same"
		switch {
		case aw == "nowhere":
			res = "none"
		case bw == "nowhere":
			res = "different"
		case bw != aw || !bytes.Equal(canonical(bs), canonical(as)):
			res = "different"
		}
		out = append(out, MoveSecret{Store: n, Before: bw, After: aw, Result: res})
	}
	return out
}

func canonical(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	c, _ := json.Marshal(v)
	return c
}

// canRun reports whether a role may start runs of a Flight (the operator and admin roles).
func canRun(role string) bool { return role == "operator" || role == "admin" }

func moveAccess(live []engine.ConfigObject, before, after []EffectiveEntry) []MoveAccess {
	roles := func(entries []EffectiveEntry) map[string]string {
		m := map[string]string{}
		for _, e := range entries {
			if e.Kind != "role-binding" {
				continue
			}
			for _, o := range live {
				if o.Kind != e.Kind || o.Name != e.Name || o.Unit != e.Unit {
					continue
				}
				var b config.AccessBinding
				if v, _, err := decodeSpec(kindByName(o.Kind), o.Name, o.Spec); err == nil {
					if x, ok := v.(*config.AccessBinding); ok {
						b = *x
					}
				}
				who := ""
				switch {
				case b.User != "":
					who = "user " + b.User
				case b.Group != "":
					who = "group " + b.Group
				}
				if who != "" && (m[who] == "" || (canRun(b.Role) && !canRun(m[who]))) {
					m[who] = b.Role
				}
			}
		}
		return m
	}
	b, a := roles(before), roles(after)
	whos := map[string]bool{}
	for w := range b {
		whos[w] = true
	}
	for w := range a {
		whos[w] = true
	}
	sorted := make([]string, 0, len(whos))
	for w := range whos {
		sorted = append(sorted, w)
	}
	sort.Strings(sorted)
	out := []MoveAccess{}
	for _, w := range sorted {
		br, ar := b[w], a[w]
		if canRun(br) == canRun(ar) {
			continue
		}
		show := func(r string) string {
			if r == "" {
				return "no role here"
			}
			return r
		}
		eff := "gains"
		if canRun(br) {
			eff = "loses"
		}
		out = append(out, MoveAccess{Who: w, Before: show(br), After: show(ar), Effect: eff})
	}
	return out
}

// MoveResult is what a move did.
type MoveResult struct {
	Moved   []string   `json:"moved"`
	Blocked []MovePlan `json:"blocked"`
	// Unchanged are Flights already in the Squadron.
	Unchanged []string `json:"unchanged"`
	// Undo maps each moved Flight to the Squadron it came from ("" is Unfiled): send it to the move route as `homes` to put each back.
	Undo map[string]string `json:"undo"`
	To   string            `json:"to"`
}

// MoveFlights moves the Flights that the plans allow, in one audited change that can be undone.
// homes, when given, names a Squadron per Flight (the undo and the organize wizard); otherwise every Flight goes to `to`.
func (s *Service) MoveFlights(ctx context.Context, actor string, flights []string, to string, homes map[string]string, comment string, pipelineText func(string) (string, bool)) (*MoveResult, error) {
	return s.moveFlights(ctx, actor, flights, to, homes, comment, nil, nil, pipelineText)
}

// moveFlights does the move; `creates` are Squadrons to make in the same change, `removes` Squadrons to delete in it.
func (s *Service) moveFlights(ctx context.Context, actor string, flights []string, to string, homes map[string]string, comment string, creates []Change, removes []Change, pipelineText func(string) (string, bool)) (*MoveResult, error) {
	coming := map[string]bool{}
	for _, c := range creates {
		coming[c.Name] = true
	}
	res := &MoveResult{Moved: []string{}, Blocked: []MovePlan{}, Unchanged: []string{}, Undo: map[string]string{}, To: to}
	groups := map[string][]string{}
	order := []string{}
	for _, f := range flights {
		home := to
		if h, ok := homes[f]; ok {
			home = h
		}
		if _, ok := groups[home]; !ok {
			order = append(order, home)
		}
		groups[home] = append(groups[home], f)
	}
	_, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return nil, err
	}
	changes := append([]Change{}, creates...)
	k := kindByName("project")
	for _, home := range order {
		plans, err := s.planMoves(ctx, groups[home], home, coming, pipelineText)
		if err != nil {
			return nil, err
		}
		for _, p := range plans {
			switch {
			case p.Unchanged:
				res.Unchanged = append(res.Unchanged, p.Flight)
			case p.Blocked:
				res.Blocked = append(res.Blocked, p)
			default:
				var cur *engine.ConfigObject
				for i := range objs {
					if objs[i].Kind == "project" && objs[i].Name == p.Flight && !objs[i].Deleted() {
						cur = &objs[i]
					}
				}
				if cur == nil {
					return nil, fmt.Errorf("%w: Flight %s", engine.ErrNotFound, p.Flight)
				}
				changes = append(changes, Change{Kind: k, Name: p.Flight, Action: engine.ConfigUpdate, Unit: home, Spec: cur.Spec, IfVersion: cur.Version,
					ManagedBy: engine.ManagedByAPI, Comment: clipComment(comment, p.From, home)})
				res.Moved = append(res.Moved, p.Flight)
				res.Undo[p.Flight] = p.From
			}
		}
	}
	if len(res.Moved) == 0 {
		return res, nil
	}
	changes = append(changes, removes...)
	if _, err := s.Apply(actor, changes); err != nil {
		return nil, err
	}
	return res, nil
}

func clipComment(c, from, to string) string {
	name := func(u string) string {
		if u == "" {
			return "Unfiled"
		}
		return u
	}
	s := fmt.Sprintf("moved from %s to %s", name(from), name(to))
	if c != "" {
		s += ": " + c
	}
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

// OrganizeGroup is a Squadron to make (or use) and the Flights to put in it.
type OrganizeGroup struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Description string   `json:"description,omitempty"`
	Flights     []string `json:"flights"`
}

// OrganizeResult is what an organize did, with what is needed to undo it.
type OrganizeResult struct {
	Created []string `json:"created"`
	*MoveResult
	// UndoDelete are the Squadrons the organize made: the undo removes them if they are empty again.
	UndoDelete []string `json:"undo_delete"`
}

// OrganizeApply makes the Squadrons that do not exist and files each group's Flights in them, as one change.
// A Flight whose move is blocked (design: a secret reference that would resolve differently) stays where it was.
func (s *Service) OrganizeApply(ctx context.Context, actor string, groups []OrganizeGroup, pipelineText func(string) (string, bool)) (*OrganizeResult, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	lay, err := s.Levels()
	if err != nil {
		return nil, err
	}
	uk := kindByName("unit")
	var creates []Change
	homes := map[string]string{}
	var flights []string
	made := map[string]bool{}
	for _, g := range groups {
		if p := UnitNameProblem(g.Name); p != "" {
			return nil, invalid(fmt.Errorf("%q: %s", g.Name, p))
		}
		if !unitExists(lay, g.Name) && !made[g.Name] {
			spec, _ := json.Marshal(Unit{DisplayName: g.DisplayName, Description: g.Description})
			creates = append(creates, Change{Kind: uk, Name: g.Name, Action: engine.ConfigUpdate, Spec: spec, IfVersion: 0, ManagedBy: engine.ManagedByAPI, Comment: "made by Organize into Squadrons"})
			made[g.Name] = true
		}
		for _, f := range g.Flights {
			if _, dup := homes[f]; dup {
				return nil, invalid(fmt.Errorf("the Flight %s is in two groups", f))
			}
			homes[f] = g.Name
			flights = append(flights, f)
		}
	}
	if len(flights) == 0 {
		return nil, invalid(fmt.Errorf("no Flight was chosen"))
	}
	res, err := s.moveFlights(ctx, actor, flights, "", homes, "Organize into Squadrons", creates, nil, pipelineText)
	if err != nil {
		return nil, err
	}
	out := &OrganizeResult{Created: []string{}, MoveResult: res, UndoDelete: []string{}}
	if len(res.Moved) > 0 {
		for _, c := range creates {
			out.Created = append(out.Created, c.Name)
		}
		out.UndoDelete = append(out.UndoDelete, out.Created...)
	}
	return out, nil
}

// OrganizeUndo puts each Flight back in the Squadron it came from, and removes the Squadrons the organize made if they are empty.
func (s *Service) OrganizeUndo(ctx context.Context, actor string, homes map[string]string, deleteUnits []string) (*MoveResult, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	flights := make([]string, 0, len(homes))
	for f := range homes {
		flights = append(flights, f)
	}
	sort.Strings(flights)
	_, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return nil, err
	}
	var removes []Change
	for _, u := range deleteUnits {
		for _, o := range objs {
			if o.Kind != "unit" || o.Name != u || o.Deleted() {
				continue
			}
			left := false
			for _, p := range objs {
				if p.Kind == "project" && !p.Deleted() && p.Unit == u {
					if h, ok := homes[p.Name]; !ok || h == u {
						left = true
					}
				}
			}
			if !left {
				removes = append(removes, Change{Kind: kindByName("unit"), Name: u, Action: engine.ConfigDelete, IfVersion: o.Version, ManagedBy: engine.ManagedByAPI, Comment: "undo of Organize into Squadrons"})
			}
		}
	}
	return s.moveFlights(ctx, actor, flights, "", homes, "undo", nil, removes, nil)
}
