package togglestore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// Toggles as a file in the repository. The file is the same model in YAML
// (toggles.File); Export writes the live state of a project as one, Plan says
// how a file and the live state differ, and Apply makes the live state match
// the file, toggle by toggle, through the same safeguards as any change (a
// reason, an approval where one is on). Nothing is applied by itself, and
// nothing is ever removed: a toggle or an environment that the file leaves out
// is listed as "only live" and left alone.

// Drift is one difference between a file and the live state.
type Drift struct {
	Kind        string `json:"kind"` // add, change, only_live
	What        string `json:"what"` // toggle, environment, segment
	Toggle      string `json:"toggle,omitempty"`
	Environment string `json:"environment,omitempty"`
	Detail      string `json:"detail"`
}

func fromMillis(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02")
}

func toMillis(date string) int64 {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// Export writes a project's own toggles (those defined at the project level)
// and segments as a file.
func (s *Service) Export(ctx context.Context, project string) (*toggles.File, error) {
	defs, err := listDefs(ctx, s.d.DB, "", project)
	if err != nil {
		return nil, err
	}
	f := &toggles.File{Toggles: map[string]toggles.FileToggle{}, Segments: map[string]toggles.Segment{}}
	for _, d := range defs {
		if d.Level != LevelProject {
			continue
		}
		ft := toggles.FileToggle{Type: d.Type, Kind: d.Kind, Description: d.Description, Owner: d.Owner, Expires: fromMillis(d.ExpiresAt), ClientVisible: d.ClientVisible,
			Tags: d.Tags, Default: d.Default, SafeValue: d.SafeValue, Environments: map[string]toggles.State{}}
		if len(ft.Tags) == 0 {
			ft.Tags = nil
		}
		envs, err := listEnvs(ctx, s.d.DB, d.Key)
		if err != nil {
			return nil, err
		}
		for _, e := range envs {
			ft.Environments[e.Environment] = e.State
		}
		f.Toggles[d.Key.Key] = ft
	}
	segs, err := listSegments(ctx, s.d.DB, "", project)
	if err != nil {
		return nil, err
	}
	for _, sg := range segs {
		if sg.Level == LevelProject {
			f.Segments[sg.Name] = sg.Segment
		}
	}
	return f, nil
}

// Plan says how a file differs from the live state of a project.
func (s *Service) Plan(ctx context.Context, project string, f *toggles.File) ([]Drift, error) {
	live, err := s.Export(ctx, project)
	if err != nil {
		return nil, err
	}
	var out []Drift
	for _, k := range f.Keys() {
		want := f.Toggles[k]
		have, ok := live.Toggles[k]
		if !ok {
			out = append(out, Drift{"add", "toggle", k, "", fmt.Sprintf("a new %s toggle, %d environment(s)", want.Type, len(want.Environments))})
			for env := range want.Environments {
				out = append(out, Drift{"add", "environment", k, env, "state set in " + env})
			}
			continue
		}
		if have.Type != want.Type {
			out = append(out, Drift{"change", "toggle", k, "", fmt.Sprintf("the type is %s live and %s in the file; a type cannot change, so apply will refuse it", have.Type, want.Type)})
			continue
		}
		if d := metaDiff(have, want); d != "" {
			out = append(out, Drift{"change", "toggle", k, "", d})
		}
		envs := make([]string, 0, len(want.Environments))
		for e := range want.Environments {
			envs = append(envs, e)
		}
		sort.Strings(envs)
		for _, env := range envs {
			cur, has := have.Environments[env]
			switch {
			case !has:
				out = append(out, Drift{"add", "environment", k, env, "state set in " + env + ", off live"})
			case toggles.Canonical(cur) != toggles.Canonical(want.Environments[env]):
				mode, serves := describeState(want.Environments[env], &Definition{})
				_, now := describeState(cur, &Definition{})
				out = append(out, Drift{"change", "environment", k, env, fmt.Sprintf("live: %s; the file: %s (%s)", now, serves, mode)})
			}
		}
		for env := range have.Environments {
			if _, in := want.Environments[env]; !in {
				out = append(out, Drift{"only_live", "environment", k, env, "set live, not in the file; apply leaves it alone"})
			}
		}
	}
	for k := range live.Toggles {
		if _, in := f.Toggles[k]; !in {
			out = append(out, Drift{"only_live", "toggle", k, "", "defined live, not in the file; apply leaves it alone"})
		}
	}
	for n, sg := range f.Segments {
		switch have, ok := live.Segments[n]; {
		case !ok:
			out = append(out, Drift{"add", "segment", "", "", "segment " + n})
		case toggles.Canonical(have) != toggles.Canonical(sg):
			out = append(out, Drift{"change", "segment", "", "", "segment " + n + " differs"})
		}
	}
	for n := range live.Segments {
		if _, in := f.Segments[n]; !in {
			out = append(out, Drift{"only_live", "segment", "", "", "segment " + n + " is live, not in the file; apply leaves it alone"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Toggle+out[i].Environment < out[j].Toggle+out[j].Environment })
	return out, nil
}

func metaDiff(have, want toggles.FileToggle) string {
	var d []string
	if have.Description != want.Description {
		d = append(d, "description")
	}
	if have.Owner != want.Owner {
		d = append(d, "owner")
	}
	if have.Expires != want.Expires {
		d = append(d, "expires")
	}
	if have.ClientVisible != want.ClientVisible {
		d = append(d, "client_visible")
	}
	if have.Kind != want.Kind && want.Kind != "" {
		d = append(d, "kind")
	}
	if toggles.Canonical(have.Default) != toggles.Canonical(want.Default) {
		d = append(d, "default")
	}
	if toggles.Canonical(have.SafeValue) != toggles.Canonical(want.SafeValue) && want.SafeValue != nil {
		d = append(d, "safe_value")
	}
	if strings.Join(have.Tags, ",") != strings.Join(want.Tags, ",") {
		d = append(d, "tags")
	}
	if len(d) == 0 {
		return ""
	}
	return "differs in " + strings.Join(d, ", ")
}

// ApplyResult says what Apply did.
type ApplyResult struct {
	Made    []string `json:"made"`
	Waiting []string `json:"waiting"` // changes that need approval first
	Failed  []string `json:"failed"`
}

// Apply makes a project's toggles match a file, in the file's order. Each
// change goes through ChangeState, so a safeguard that needs approval holds
// that change and the rest go on. The reason applies to every change.
func (s *Service) Apply(ctx context.Context, who Actor, project string, f *toggles.File, reason, origin string) (*ApplyResult, error) {
	if err := f.Check(); err != nil {
		return nil, invalid("%v", err)
	}
	res := &ApplyResult{Made: []string{}, Waiting: []string{}, Failed: []string{}}
	where := ProjectWhere(project)
	fail := func(what string, err error) { res.Failed = append(res.Failed, what+": "+err.Error()) }
	live, err := s.Export(ctx, project)
	if err != nil {
		return nil, err
	}
	for n, sg := range f.Segments {
		if toggles.Canonical(live.Segments[n]) == toggles.Canonical(sg) {
			continue
		}
		if err := s.SaveSegment(ctx, who, where, n, sg, reason); err != nil {
			fail("segment "+n, err)
		} else {
			res.Made = append(res.Made, "segment "+n)
		}
	}
	for _, k := range f.Keys() {
		want := f.Toggles[k]
		key := Key{Where: where, Key: k}
		have, exists := live.Toggles[k]
		switch {
		case !exists:
			_, err := s.Create(ctx, who, DefinitionInput{Key: key, Type: want.Type, Kind: want.Kind, Description: want.Description, Default: want.Default, SafeValue: want.SafeValue,
				ClientVisible: want.ClientVisible, Owner: want.Owner, Tags: want.Tags, ExpiresAt: toMillis(want.Expires), Origin: origin}, reason)
			if err != nil {
				fail("toggle "+k, err)
				continue
			}
			res.Made = append(res.Made, "toggle "+k)
		case have.Type != want.Type:
			fail("toggle "+k, invalid("the type cannot change (%s live, %s in the file)", have.Type, want.Type))
			continue
		case metaDiff(have, want) != "":
			exp := toMillis(want.Expires)
			kind, tags, def := want.Kind, want.Tags, want.Default
			if kind == "" {
				kind = have.Kind
			}
			p := Patch{Description: &want.Description, Owner: &want.Owner, ClientVisible: &want.ClientVisible, Kind: &kind, Tags: &tags, ExpiresAt: &exp, Default: &def}
			if want.SafeValue != nil {
				p.SafeValue = &want.SafeValue
			}
			if _, err := s.Update(ctx, who, key, p, reason); err != nil {
				fail("toggle "+k, err)
				continue
			}
			res.Made = append(res.Made, "toggle "+k)
		}
		envs := make([]string, 0, len(want.Environments))
		for e := range want.Environments {
			envs = append(envs, e)
		}
		sort.Strings(envs)
		for _, env := range envs {
			st := want.Environments[env]
			if cur, has := have.Environments[env]; has && toggles.Canonical(cur) == toggles.Canonical(st) {
				continue
			}
			out, err := s.ChangeState(ctx, who, StateChange{Project: project, Toggle: key, Environment: env, State: st, Reason: reason})
			switch {
			case err != nil:
				fail(k+" in "+env, err)
			case out.Waiting:
				res.Waiting = append(res.Waiting, fmt.Sprintf("%s in %s (change %d)", k, env, out.Change.ID))
			default:
				res.Made = append(res.Made, k+" in "+env)
			}
		}
	}
	return res, nil
}
