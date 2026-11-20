package settings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// ParseFile reads configuration text: a configuration file (its bootstrap
// settings are ignored), or what Export wrote. It returns the objects it
// describes.
func ParseFile(data []byte) ([]engine.ConfigObject, error) {
	c, extra, err := config.ParseSections(data, "objects")
	if err != nil {
		return nil, err
	}
	objs, err := FromConfig(c)
	if err != nil {
		return nil, err
	}
	n := extra["objects"]
	if n == nil {
		return objs, nil
	}
	type entry struct {
		Kind string         `yaml:"kind"`
		Name string         `yaml:"name"`
		Unit string         `yaml:"unit"`
		Spec map[string]any `yaml:"spec"`
	}
	var entries []entry
	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode(n); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("objects: %w", err)
	}
	bindings := false
	for _, e := range entries {
		if e.Kind == "role-binding" {
			bindings = true
		}
	}
	if bindings {
		// The objects carry the bindings with their names and levels; the
		// auth.access beside them is for an older server.
		kept := objs[:0]
		for _, o := range objs {
			if o.Kind != "role-binding" {
				kept = append(kept, o)
			}
		}
		objs = kept
	}
	for i, e := range entries {
		k := kindByName(e.Kind)
		if k == nil {
			return nil, fmt.Errorf("objects[%d]: unknown kind %q", i, e.Kind)
		}
		spec, err := json.Marshal(jsonSafe(e.Spec))
		if err != nil {
			return nil, fmt.Errorf("objects[%d]: %w", i, err)
		}
		if _, stored, err := decodeSpec(k, e.Name, spec); err != nil {
			return nil, fmt.Errorf("objects[%d] (%s %s): %w", i, e.Kind, e.Name, err)
		} else {
			spec = stored
		}
		if e.Kind == "installation" {
			merged := false
			for j := range objs {
				if objs[j].Kind == "installation" {
					objs[j].Spec = mergeJSON(objs[j].Spec, spec)
					merged = true
				}
			}
			if merged {
				continue
			}
		}
		objs = append(objs, engine.ConfigObject{Kind: e.Kind, Name: e.Name, Unit: e.Unit, Spec: spec})
	}
	return objs, nil
}

// jsonSafe turns map[any]any (which YAML may give) into map[string]any.
func jsonSafe(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = jsonSafe(x)
		}
		return t
	case map[any]any:
		out := map[string]any{}
		for k, x := range t {
			out[fmt.Sprint(k)] = jsonSafe(x)
		}
		return out
	case []any:
		for i, x := range t {
			t[i] = jsonSafe(x)
		}
	}
	return v
}

func mergeJSON(a, b json.RawMessage) json.RawMessage {
	var ma, mb map[string]any
	_ = json.Unmarshal(a, &ma)
	_ = json.Unmarshal(b, &mb)
	if ma == nil {
		ma = map[string]any{}
	}
	for k, v := range mb {
		ma[k] = v
	}
	out, _ := json.Marshal(ma)
	return out
}

// PlanItem is one change an import would make.
type PlanItem struct {
	Action  string        `json:"action"` // create, update, delete or unchanged
	Kind    string        `json:"kind"`
	Name    string        `json:"name"`
	Unit    string        `json:"unit,omitempty"`
	Version int64         `json:"version,omitempty"` // the version it changes
	Diff    []FieldChange `json:"diff,omitempty"`
}

// Plan is what an import would do.
type Plan struct {
	Items  []PlanItem `json:"items"`
	Digest string     `json:"digest"`
	// Changes counts the items that change something.
	Changes int `json:"changes"`
	// Problems, if any, are why the import would be refused.
	Problems []string `json:"problems,omitempty"`
}

// PlanImport works out what importing data would change. With prune, the
// objects data does not describe are deleted (except the installation's
// settings and the organisation).
func (s *Service) PlanImport(data []byte, prune bool) (*Plan, []Change, error) {
	if err := s.check(); err != nil {
		return nil, nil, err
	}
	in, err := ParseFile(data)
	if err != nil {
		return nil, nil, invalid(err)
	}
	_, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(data)
	plan := &Plan{Items: []PlanItem{}, Digest: hex.EncodeToString(sum[:])}
	var changes []Change
	have := map[string]engine.ConfigObject{}
	for _, o := range objs {
		have[o.Kind+"\x00"+o.Name] = o
	}
	want := map[string]bool{}
	for _, o := range in {
		key := o.Kind + "\x00" + o.Name
		want[key] = true
		k := kindByName(o.Kind)
		cur, ok := have[key]
		item := PlanItem{Kind: o.Kind, Name: o.Name, Unit: o.Unit}
		switch {
		case !ok || cur.Deleted():
			item.Action = "create"
			item.Diff = Diff(nil, o.Spec)
		case cur.Unit != o.Unit || string(compact(cur.Spec)) != string(compact(o.Spec)):
			item.Action, item.Version = "update", cur.Version
			item.Diff = Diff(cur.Spec, o.Spec)
			if cur.Unit != o.Unit {
				item.Diff = append([]FieldChange{{Path: "(unit)", Before: cur.Unit, After: o.Unit}}, item.Diff...)
			}
		default:
			item.Action, item.Version = "unchanged", cur.Version
		}
		plan.Items = append(plan.Items, item)
		if item.Action != "unchanged" {
			plan.Changes++
			changes = append(changes, Change{Kind: k, Name: o.Name, Action: engine.ConfigUpdate, Unit: o.Unit, Spec: o.Spec,
				IfVersion: -1, ManagedBy: engine.ManagedByImport})
		}
	}
	if prune {
		for _, o := range objs {
			k := kindByName(o.Kind)
			if o.Deleted() || want[o.Kind+"\x00"+o.Name] || k == nil || k.Singleton != "" {
				continue
			}
			plan.Items = append(plan.Items, PlanItem{Action: "delete", Kind: o.Kind, Name: o.Name, Unit: o.Unit, Version: o.Version,
				Diff: Diff(o.Spec, nil)})
			plan.Changes++
			changes = append(changes, Change{Kind: k, Name: o.Name, Action: engine.ConfigDelete, IfVersion: o.Version, ManagedBy: engine.ManagedByImport})
		}
	}
	sortPlan(plan.Items)
	if len(changes) > 0 {
		if err := s.DryRun(changes); err != nil {
			plan.Problems = problemsOf(err)
		}
	}
	return plan, changes, nil
}

func compact(b json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return b
	}
	return buf.Bytes()
}

func sortPlan(items []PlanItem) {
	order := func(a string) int {
		switch a {
		case "create":
			return 0
		case "update":
			return 1
		case "delete":
			return 2
		}
		return 3
	}
	// Stable: by action, then kind order, then name.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j-1], items[j]
			less := order(b.Action) < order(a.Action) ||
				(order(b.Action) == order(a.Action) && (kindOrder(b.Kind) < kindOrder(a.Kind) ||
					(kindOrder(b.Kind) == kindOrder(a.Kind) && b.Name < a.Name)))
			if !less {
				break
			}
			items[j-1], items[j] = b, a
		}
	}
}

func problemsOf(err error) []string {
	if ie, ok := err.(*InvalidError); ok {
		return ie.Problems
	}
	return []string{err.Error()}
}

// ImportResult2 is what an import did.
type ImportDone struct {
	Import  *engine.ConfigImport `json:"import"`
	Plan    *Plan                `json:"plan"`
	Results []Result             `json:"results"`
}

// Import applies data, as PlanImport described it, recorded as one import.
// expect, if not empty, is the digest of the plan that was reviewed: the
// import is refused if the text differs.
func (s *Service) Import(actor string, data []byte, prune bool, origin, expect string) (*ImportDone, error) {
	plan, changes, err := s.PlanImport(data, prune)
	if err != nil {
		return nil, err
	}
	if expect != "" && expect != plan.Digest {
		return nil, fmt.Errorf("%w: the file is not the one that was reviewed", engine.ErrConflict)
	}
	if len(plan.Problems) > 0 {
		return nil, &InvalidError{Problems: plan.Problems}
	}
	counts := map[string]int{}
	for _, it := range plan.Items {
		counts[it.Action]++
	}
	summary, _ := json.Marshal(map[string]any{"counts": counts, "prune": prune, "items": plan.Items})
	if origin == "" {
		origin = "api"
	}
	imp := &engine.ConfigImport{Origin: origin, Digest: plan.Digest, Summary: summary}
	res, err := s.apply1(actor, changes, imp, false)
	if err != nil {
		return nil, err
	}
	return &ImportDone{Import: imp, Plan: plan, Results: res}, nil
}
