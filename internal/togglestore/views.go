package togglestore

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// StaleDays is how long a toggle may sit fully on, or unevaluated, before it is
// mentioned. The mention is a nudge and nothing more: no toggle is ever
// removed, turned off or changed because it looks stale.
const StaleDays = 30

// EnvView is a toggle in one environment at a glance.
type EnvView struct {
	Environment  string        `json:"environment"`
	Configured   bool          `json:"configured"` // false: never set there: off, serving the default
	State        toggles.State `json:"state"`
	Mode         string        `json:"mode"`   // off, on, targeted (rules), rollout
	Serves       string        `json:"serves"` // a sentence: "true for everyone", "25% true, 75% false", "2 rules, then false"
	Version      int           `json:"version"`
	UpdatedAt    int64         `json:"updated_at,omitempty"`
	UpdatedBy    string        `json:"updated_by,omitempty"`
	SettledSince int64         `json:"settled_since,omitempty"`
	Waiting      int           `json:"waiting"`               // changes waiting for approval
	WatchUntil   int64         `json:"watch_until,omitempty"` // the environment is marked as watched until
}

// StaleNote is a gentle mention that a toggle may be finished with.
type StaleNote struct {
	Reason string `json:"reason"` // settled, unused, expired
	Detail string `json:"detail"`
}

// Summary is a toggle as a project sees it.
type Summary struct {
	Definition
	Inherited     bool              `json:"inherited"` // defined at a level above the project
	Environments  []EnvView         `json:"environments"`
	Stale         []StaleNote       `json:"stale"`
	LastEvaluated map[string]string `json:"last_evaluated,omitempty"`
}

func describeState(st toggles.State, d *Definition) (mode, serves string) {
	val := func(v any) string { return variationText(v) }
	serve := func(sv toggles.Serve) string {
		if sv.Rollout == nil {
			return val(sv.Value) + " for everyone"
		}
		var parts []string
		for _, b := range sv.Rollout.Buckets {
			if b.Weight > 0 {
				parts = append(parts, fmt.Sprintf("%g%% %s", float64(b.Weight)/100, val(b.Value)))
			}
		}
		return strings.Join(parts, ", ")
	}
	switch {
	case !st.On:
		return "off", "off: " + val(st.OffValue) + " for everyone"
	case len(st.Rules) > 0:
		return "targeted", fmt.Sprintf("%d rule(s), then %s", len(st.Rules), serve(st.Fallthrough))
	case st.Fallthrough.Rollout != nil:
		return "rollout", serve(st.Fallthrough)
	}
	return "on", serve(st.Fallthrough)
}

// List returns the toggles a project sees, each with its state in every
// environment given (and any other that has one), stale mentions and when it
// was last evaluated.
func (s *Service) List(ctx context.Context, project string, envs []string) ([]Summary, error) {
	defs, err := listDefs(ctx, s.d.DB, s.d.UnitOf(project), project)
	if err != nil {
		return nil, err
	}
	chosen := map[string]Definition{}
	for _, d := range defs {
		cur, ok := chosen[d.Key.Key]
		if ok && cur.Lock != nil {
			continue
		}
		if !ok || levelRank(d.Level) > levelRank(cur.Level) {
			chosen[d.Key.Key] = d
		}
	}
	keys := make([]string, 0, len(chosen))
	for k := range chosen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	waiting, err := s.Pending(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(keys))
	for _, k := range keys {
		sm, err := s.summary(ctx, project, chosen[k], envs, waiting)
		if err != nil {
			return nil, err
		}
		out = append(out, *sm)
	}
	return out, nil
}

// Get returns one toggle as a project sees it.
func (s *Service) Get(ctx context.Context, project, key string, envs []string) (*Summary, error) {
	d, err := s.resolve(ctx, s.d.DB, project, key)
	if err != nil {
		return nil, err
	}
	waiting, err := s.Pending(ctx, project)
	if err != nil {
		return nil, err
	}
	return s.summary(ctx, project, *d, envs, waiting)
}

func (s *Service) summary(ctx context.Context, project string, d Definition, envs []string, waiting []Change) (*Summary, error) {
	rows, err := listEnvs(ctx, s.d.DB, d.Key)
	if err != nil {
		return nil, err
	}
	have := map[string]EnvState{}
	names := slices.Clone(envs)
	for _, e := range rows {
		have[e.Environment] = e
		if !slices.Contains(names, e.Environment) {
			names = append(names, e.Environment)
		}
	}
	sort.Strings(names)
	sm := &Summary{Definition: d, Inherited: d.Level != LevelProject, Environments: []EnvView{}, Stale: []StaleNote{}}
	now := s.d.Now()
	watch, _ := queryChanges(ctx, s.d.DB, `AND level = ? AND scope = ? AND key = ? AND action = 'state' AND status = 'applied' AND watch_until > ? ORDER BY id DESC`,
		string(d.Level), d.Scope, d.Key.Key, now.UnixMilli())
	for _, name := range names {
		v := EnvView{Environment: name, State: offState(&d)}
		if e, ok := have[name]; ok {
			v.Configured, v.State, v.Version, v.UpdatedAt, v.UpdatedBy, v.SettledSince = true, e.State, e.Version, e.UpdatedAt, e.UpdatedBy, e.SettledSince
		}
		v.Mode, v.Serves = describeState(v.State, &d)
		for _, w := range waiting {
			if w.Toggle == d.Key.Key && w.Environment == name && w.Level == d.Level && w.Scope == d.Scope {
				v.Waiting++
			}
		}
		for _, w := range watch {
			if w.Environment == name {
				v.WatchUntil = max(v.WatchUntil, w.WatchUntil)
			}
		}
		sm.Environments = append(sm.Environments, v)
	}
	usage, err := s.Usage(ctx, project, d.Key.Key, 90)
	if err != nil {
		return nil, err
	}
	sm.LastEvaluated = map[string]string{}
	var newest string
	for _, u := range usage {
		sm.LastEvaluated[u.Environment] = u.LastEvaluated
		if u.LastEvaluated > newest {
			newest = u.LastEvaluated
		}
	}
	sm.Stale = s.staleNotes(&d, sm.Environments, newest, now)
	return sm, nil
}

func (s *Service) staleNotes(d *Definition, envs []EnvView, lastEvaluated string, now time.Time) []StaleNote {
	var out []StaleNote
	day := func(ms int64) int { return int(now.Sub(time.UnixMilli(ms)).Hours() / 24) }
	if d.ExpiresAt > 0 && d.ExpiresAt < now.UnixMilli() {
		out = append(out, StaleNote{"expired", fmt.Sprintf("Its expected removal date, %s, has passed. If the code no longer reads it, remove the check and then delete the toggle; if it is still needed, move the date.", time.UnixMilli(d.ExpiresAt).UTC().Format("2006-01-02"))})
	}
	// Fully on, to everyone, in every environment, for a long time.
	if d.Kind != toggles.KindKill && d.Kind != toggles.KindPermission && d.Kind != toggles.KindOperations && len(envs) > 0 {
		var since int64
		same := ""
		all := true
		for _, e := range envs {
			v, ok := toggles.Settled(e.State)
			if !e.Configured || !ok || (same != "" && v != same) || e.SettledSince == 0 {
				all = false
				break
			}
			same = v
			since = max(since, e.SettledSince)
		}
		if all && day(since) >= StaleDays {
			out = append(out, StaleNote{"settled", fmt.Sprintf("It has served %s to everyone in every environment for %d days. If it has done its job, remove the check from the code and then delete the toggle.", same, day(since))})
		}
	}
	// Not evaluated for a long time (and old enough to have been).
	if day(d.CreatedAt) >= StaleDays {
		switch lastEvaluated {
		case "":
			out = append(out, StaleNote{"unused", fmt.Sprintf("No application has evaluated it in the last 90 days that Launchway counted (it is %d days old). It may be unused, or the applications may not report counts.", day(d.CreatedAt))})
		default:
			if t, err := time.Parse("2006-01-02", lastEvaluated); err == nil && int(now.Sub(t).Hours()/24) >= StaleDays {
				out = append(out, StaleNote{"unused", fmt.Sprintf("It was last evaluated on %s, %d days ago.", lastEvaluated, int(now.Sub(t).Hours()/24))})
			}
		}
	}
	return out
}

// Timeline returns what changed about toggles in an environment, newest first:
// the entries the environment page shows beside its deployments.
func (s *Service) Timeline(ctx context.Context, project, env string, since int64, limit int) ([]Change, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return queryChanges(ctx, s.d.DB, `AND environment = ? AND (project = ? OR project = '') AND action IN ('state','safeguards','key') AND status IN ('applied','pending') AND at >= ? ORDER BY id DESC LIMIT `+fmt.Sprint(limit),
		env, project, since)
}

// ChangesSince lists the toggle changes made in an environment since a time
// that are still in effect: not reverted, and not themselves reverts. They are
// what a rollback would leave behind.
func (s *Service) ChangesSince(ctx context.Context, project, env string, since int64) ([]Change, error) {
	return queryChanges(ctx, s.d.DB, `AND environment = ? AND (project = ? OR project = '') AND action = 'state' AND status = 'applied' AND at > ? AND reverted_by = 0 AND reverts = 0 ORDER BY id DESC`,
		env, project, since)
}

// RevertSince reverts the changes of ChangesSince, newest first, so that every
// toggle of the environment is as it was at the time. A change that cannot be
// reverted is reported and the rest go on.
func (s *Service) RevertSince(ctx context.Context, who Actor, project, env string, since int64, reason string) ([]RevertResult, error) {
	list, err := s.ChangesSince(ctx, project, env, since)
	if err != nil {
		return nil, err
	}
	out := []RevertResult{}
	for _, c := range list {
		r := RevertResult{Change: c.ID, Toggle: c.Toggle}
		if o, err := s.Revert(ctx, who, c.ID, reason); err != nil {
			r.Error = err.Error()
		} else {
			r.RevertedBy = o.Change.ID
		}
		out = append(out, r)
	}
	return out, nil
}

// RevertResult is what happened to one change in RevertSince.
type RevertResult struct {
	Change     int64  `json:"change"`
	Toggle     string `json:"toggle"`
	RevertedBy int64  `json:"reverted_by,omitempty"`
	Error      string `json:"error,omitempty"`
}
