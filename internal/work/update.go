package work

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// Patch changes an item. A nil field is left alone.
type Patch struct {
	Difficulty      *string `json:"difficulty"`
	ExpectedVersion int     `json:"expected_version"`

	Title        *string        `json:"title"`
	Body         *string        `json:"body"`
	Type         *string        `json:"type"`
	State        *string        `json:"state"`
	Priority     *string        `json:"priority"`
	Estimate     *float64       `json:"estimate"`
	Parent       *string        `json:"parent"` // a ref; "" clears
	Milestone    *string        `json:"milestone"`
	Sprint       *string        `json:"sprint"`
	StartAt      *string        `json:"start_at"`
	DueAt        *string        `json:"due_at"`
	Confidential *bool          `json:"confidential"`
	Labels       *[]string      `json:"labels"`
	AddLabels    []string       `json:"add_labels"`
	RemoveLabels []string       `json:"remove_labels"`
	Assignees    *[]string      `json:"assignees"`
	AddAssignees []string       `json:"add_assignees"`
	RemoveAssign []string       `json:"remove_assignees"`
	Custom       map[string]any `json:"custom"` // set keys; a null removes one
	Criteria     *[]CriterionIn `json:"criteria"`
	Watch        *bool          `json:"watch"` // the caller starts or stops watching
	Resolution   *string        `json:"resolution"`
}

// CriterionIn is a criterion in a patch: with an id it updates, without it adds.
type CriterionIn struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// updater carries one Update through its transaction.
type updater struct {
	s        *Store
	ctx      context.Context
	tx       DB
	c        Caller
	cur      *Item
	eff      *Effective
	p        Patch
	sets     []string
	args     []any
	changes  map[string]any
	stateEvt map[string]any
	typ      string
	estMilli int64
	assigned map[string]bool
	fired    []Event
}

func (u *updater) set(col string, val any, name string, from, to any) {
	u.sets = append(u.sets, col+" = ?")
	u.args = append(u.args, val)
	u.changes[name] = map[string]any{"from": from, "to": to}
}

// Update changes an item and records what changed.
func (s *Store) Update(ctx context.Context, c Caller, ref string, p Patch) (*Item, error) {
	cur, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if p.Watch != nil && onlyWatch(p) {
		return s.watch(ctx, c, cur, *p.Watch)
	}
	if !c.canWrite(cur.Scope) {
		return nil, forbidden("you may not change work in %s", cur.Scope)
	}
	if p.ExpectedVersion != 0 && p.ExpectedVersion != cur.Version {
		return nil, conflict("the item changed since you read it (version %d, yours %d)", cur.Version, p.ExpectedVersion)
	}
	eff, err := s.Settings(ctx, cur.Scope)
	if err != nil {
		return nil, err
	}
	if c.Agent != nil {
		if err := s.agentMayEdit(ctx, c, cur, p); err != nil {
			return nil, err
		}
	}
	var fired []Event
	err = s.tx(ctx, func(tx DB) error {
		u := &updater{s: s, ctx: ctx, tx: tx, c: c, cur: cur, eff: eff, p: p, changes: map[string]any{}, typ: cur.Type, estMilli: toMilli(cur.Estimate), assigned: map[string]bool{}}
		fired = nil
		if err := u.run(); err != nil {
			return err
		}
		fired = u.fired
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.invalidate()
	for _, e := range fired {
		s.fire(ctx, e)
	}
	out, err := s.Get(ctx, c, cur.ID)
	if err == nil && p.State != nil && *p.State != cur.State {
		s.fire(ctx, Event{ItemID: cur.ID, Kind: "state", Data: map[string]any{"ref": out.Ref, "from": cur.State, "to": out.State}})
	}
	return out, err
}

func (u *updater) run() error {
	for _, step := range []func() error{u.text, u.classification, u.parent, u.planning, u.custom, u.labels, u.assignees, u.state, u.criteria} {
		if err := step(); err != nil {
			return err
		}
	}
	return u.save()
}

func (u *updater) text() error {
	p, cur := u.p, u.cur
	if p.Title != nil {
		t := cleanTitle(*p.Title)
		if t == "" || len(t) > MaxTitle {
			return invalid("a title is 1 to %d characters", MaxTitle)
		}
		if t != cur.Title {
			u.set("title", t, "title", cur.Title, t)
		}
	}
	if p.Body != nil {
		if len(*p.Body) > MaxBody {
			return invalid("the body is longer than %d bytes", MaxBody)
		}
		if b := cleanBody(*p.Body); b != cur.Body {
			u.sets, u.args = append(u.sets, "body = ?"), append(u.args, b)
			u.changes["body"] = map[string]any{"edited": true, "chars": len(b)}
		}
	}
	return nil
}

func (u *updater) classification() error {
	p, cur := u.p, u.cur
	if p.Type != nil && *p.Type != cur.Type {
		if !u.eff.TypeAllowed(*p.Type) {
			return invalid("items of type %q are not used here", *p.Type)
		}
		if *p.Type == TypeEpic && cur.ParentID != "" {
			return invalid("clear the parent before making it an epic")
		}
		u.typ = *p.Type
		u.set("type", u.typ, "type", cur.Type, u.typ)
	}
	if p.Priority != nil && *p.Priority != cur.Priority {
		if !slices.Contains(Priorities, *p.Priority) {
			return invalid("priority is one of %s", strings.Join(Priorities, ", "))
		}
		u.set("priority", *p.Priority, "priority", cur.Priority, *p.Priority)
	}
	if p.Difficulty != nil && *p.Difficulty != cur.Difficulty {
		if !slices.Contains([]string{"", "small", "medium", "large"}, *p.Difficulty) {
			return invalid("difficulty is small, medium or large")
		}
		u.set("difficulty", *p.Difficulty, "difficulty", cur.Difficulty, *p.Difficulty)
	}
	if p.Estimate != nil && *p.Estimate != cur.Estimate {
		if *p.Estimate < 0 || *p.Estimate > 100000 {
			return invalid("an estimate is between 0 and 100000")
		}
		u.estMilli = toMilli(*p.Estimate)
		u.set("estimate_milli", u.estMilli, "estimate", cur.Estimate, *p.Estimate)
	}
	for _, f := range []struct {
		v        *string
		col, key string
		old      string
	}{{p.StartAt, "start_at", "start_at", cur.StartAt}, {p.DueAt, "due_at", "due_at", cur.DueAt}} {
		if f.v == nil {
			continue
		}
		t, err := ParseTime(*f.v)
		if err != nil {
			return err
		}
		if iso(t) != f.old {
			u.set(f.col, t, f.key, f.old, iso(t))
		}
	}
	if p.Confidential != nil && *p.Confidential != cur.Confidential {
		if !u.c.isMaintainer(cur.Scope) && u.c.Kind != KindSystem {
			return forbidden("only a maintainer changes whether work is confidential")
		}
		u.set("confidential", boolInt(*p.Confidential), "confidential", cur.Confidential, *p.Confidential)
	}
	return nil
}

func (u *updater) parent() error {
	p, cur := u.p, u.cur
	if p.Parent == nil {
		return nil
	}
	if *p.Parent == "" {
		if cur.ParentID != "" {
			u.set("parent_id", "", "parent", cur.ParentRef, "")
		}
		return nil
	}
	pid, err := u.s.checkParent(u.ctx, u.tx, u.c, cur.ID, u.typ, *p.Parent)
	if err != nil {
		return err
	}
	if pid != cur.ParentID {
		u.set("parent_id", pid, "parent", cur.ParentRef, *p.Parent)
	}
	return nil
}

func (u *updater) planning() error {
	p, cur := u.p, u.cur
	if p.Milestone != nil {
		mid := ""
		if *p.Milestone != "" {
			var err error
			if mid, err = u.s.milestoneID(u.ctx, u.tx, cur.Scope, *p.Milestone, false); err != nil {
				return err
			}
		}
		if mid != cur.MilestoneID {
			u.set("milestone_id", mid, "milestone", cur.Milestone, *p.Milestone)
		}
	}
	if p.Sprint != nil {
		sid := ""
		if *p.Sprint != "" {
			var err error
			if sid, err = u.s.sprintID(u.ctx, u.tx, cur.Scope, *p.Sprint); err != nil {
				return err
			}
		}
		if sid != cur.SprintID {
			if cur.SprintID != "" {
				if err := u.s.sprintRemove(u.ctx, u.tx, cur.SprintID, cur.ID); err != nil {
					return err
				}
			}
			if sid != "" {
				if err := u.s.sprintAdd(u.ctx, u.tx, sid, cur.ID, u.estMilli); err != nil {
					return err
				}
			}
			u.set("sprint_id", sid, "sprint", cur.Sprint, *p.Sprint)
		}
	} else if cur.SprintID != "" && p.Estimate != nil {
		if _, err := u.tx.Exec(u.ctx, `UPDATE work_sprint_items SET estimate_milli = ? WHERE sprint_id = ? AND item_id = ? AND committed = 0`, u.estMilli, cur.SprintID, cur.ID); err != nil {
			return err
		}
	}
	return nil
}

func (u *updater) custom() error {
	if u.p.Custom == nil {
		return nil
	}
	merged := map[string]any{}
	for k, v := range u.cur.Custom {
		merged[k] = v
	}
	for k, v := range u.p.Custom {
		if v == nil {
			delete(merged, k)
		} else {
			merged[k] = v
		}
	}
	if err := u.s.checkCustom(u.eff, u.typ, merged); err != nil {
		return err
	}
	raw, _ := json.Marshal(merged)
	u.set("custom", string(raw), "custom", nil, merged)
	return nil
}

func (u *updater) labels() error {
	p, cur := u.p, u.cur
	if p.Labels == nil && len(p.AddLabels) == 0 && len(p.RemoveLabels) == 0 {
		return nil
	}
	want := slices.Clone(cur.Labels)
	if p.Labels != nil {
		want = nil
		for _, l := range *p.Labels {
			cl, err := cleanLabel(l)
			if err != nil {
				return err
			}
			want = append(want, cl)
		}
	}
	for _, l := range p.AddLabels {
		cl, err := cleanLabel(l)
		if err != nil {
			return err
		}
		want = append(want, cl)
	}
	for _, l := range p.RemoveLabels {
		cl, _ := cleanLabel(l)
		want = slices.DeleteFunc(want, func(x string) bool { return x == cl })
	}
	slices.Sort(want)
	want = slices.Compact(want)
	if len(want) > MaxLabels {
		return invalid("an item has at most %d labels", MaxLabels)
	}
	if slices.Equal(want, cur.Labels) {
		return nil
	}
	if _, err := u.tx.Exec(u.ctx, `DELETE FROM work_labels WHERE item_id = ?`, cur.ID); err != nil {
		return err
	}
	for _, l := range want {
		if _, err := u.tx.Exec(u.ctx, `INSERT INTO work_labels(item_id, label) VALUES(?,?)`, cur.ID, l); err != nil {
			return err
		}
	}
	u.changes["labels"] = map[string]any{"from": cur.Labels, "to": want}
	return nil
}

func (u *updater) assignees() error {
	p, cur := u.p, u.cur
	if p.Assignees == nil && len(p.AddAssignees) == 0 && len(p.RemoveAssign) == 0 {
		return nil
	}
	want := map[string]Assignee{}
	add := func(n string) error {
		a, err := u.s.assigneeKind(u.ctx, u.tx, n)
		if err != nil {
			return err
		}
		want[a.Name] = a
		return nil
	}
	if p.Assignees != nil {
		for _, n := range *p.Assignees {
			if err := add(n); err != nil {
				return err
			}
		}
	} else {
		for _, a := range cur.Assignees {
			want[a.Name] = a
		}
	}
	for _, n := range p.AddAssignees {
		if err := add(n); err != nil {
			return err
		}
	}
	for _, n := range p.RemoveAssign {
		a, _ := u.s.assigneeKind(u.ctx, u.tx, strings.TrimPrefix(n, "agent:"))
		delete(want, a.Name)
	}
	if len(want) > MaxAssignees {
		return invalid("an item has at most %d assignees", MaxAssignees)
	}
	before := map[string]bool{}
	for _, a := range cur.Assignees {
		before[a.Name] = true
	}
	changed := len(before) != len(want)
	for n, a := range want {
		if before[n] {
			continue
		}
		changed = true
		u.assigned[n] = true
		if a.Kind == KindAgent {
			if err := u.s.checkAgentMayTake(u.ctx, u.tx, a.Name, cur, u.c); err != nil {
				return err
			}
		}
	}
	if !changed {
		return nil
	}
	if _, err := u.tx.Exec(u.ctx, `DELETE FROM work_assignees WHERE item_id = ?`, cur.ID); err != nil {
		return err
	}
	names := []string{}
	for n, a := range want {
		if _, err := u.tx.Exec(u.ctx, `INSERT INTO work_assignees(item_id, who, kind) VALUES(?,?,?)`, cur.ID, n, a.Kind); err != nil {
			return err
		}
		names = append(names, n)
		if u.assigned[n] && a.Kind == KindAgent {
			u.fired = append(u.fired, Event{ItemID: cur.ID, Kind: "agent.assigned", Data: map[string]any{"agent": n, "ref": cur.Ref}})
		}
	}
	slices.Sort(names)
	u.changes["assignees"] = map[string]any{"to": names}
	return nil
}

func (u *updater) state() error {
	p, cur := u.p, u.cur
	if p.State == nil || *p.State == cur.State {
		return nil
	}
	if err := u.eff.CheckMove(cur.State, *p.State, u.c.Actor, u.c.isMaintainer(cur.Scope)); err != nil {
		return err
	}
	closing := u.eff.Closed(*p.State)
	if closing && u.c.Agent != nil {
		return forbidden("agents hand work over for review; a person marks it done")
	}
	closed, res := int64(0), ""
	if closing {
		closed, res = ms(u.s.now()), resolutionFor(u.eff, *p.State)
		if p.Resolution != nil && *p.Resolution != "" {
			res = *p.Resolution
		}
	}
	u.sets, u.args = append(u.sets, "state = ?", "closed_at = ?", "resolution = ?"), append(u.args, *p.State, closed, res)
	u.stateEvt = map[string]any{"from": cur.State, "to": *p.State}
	if closing {
		if _, err := u.tx.Exec(u.ctx, `DELETE FROM work_claims WHERE item_id = ?`, cur.ID); err != nil {
			return err
		}
	}
	if cur.SprintID != "" {
		if _, err := u.tx.Exec(u.ctx, `UPDATE work_sprint_items SET completed = ? WHERE sprint_id = ? AND item_id = ?`, boolInt(u.eff.Category(*p.State) == CatDone), cur.SprintID, cur.ID); err != nil {
			return err
		}
	}
	return nil
}

func (u *updater) criteria() error {
	if u.p.Criteria == nil {
		return nil
	}
	return u.s.setCriteria(u.ctx, u.tx, u.cur.ID, *u.p.Criteria, u.changes)
}

// save writes the changes, the history and the notifications.
func (u *updater) save() error {
	cur, p := u.cur, u.p
	if len(u.sets) == 0 && len(u.changes) == 0 && u.stateEvt == nil && p.Watch == nil {
		return nil
	}
	u.sets, u.args = append(u.sets, "version = version + 1", "updated_at = ?"), append(u.args, ms(u.s.now()))
	u.args = append(u.args, cur.ID, cur.Version)
	res, err := u.tx.Exec(u.ctx, `UPDATE work_items SET `+strings.Join(u.sets, ", ")+` WHERE id = ? AND version = ?`, u.args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return conflict("the item changed while you were saving; read it again")
	}
	if len(u.changes) > 0 {
		if err := u.s.event(u.ctx, u.tx, cur.ID, u.c.Actor, "updated", 0, u.changes); err != nil {
			return err
		}
	}
	if u.stateEvt != nil {
		if err := u.s.event(u.ctx, u.tx, cur.ID, u.c.Actor, "state", 0, u.stateEvt); err != nil {
			return err
		}
	}
	if p.Watch != nil {
		if err := u.s.setWatch(u.ctx, u.tx, cur.ID, u.c.Name, *p.Watch); err != nil {
			return err
		}
	}
	if len(u.assigned) > 0 {
		if err := u.s.notifyAssigned(u.ctx, u.tx, u.c, cur.ID, cur.Title, u.assigned); err != nil {
			return err
		}
	}
	if u.stateEvt != nil {
		return u.s.notifyState(u.ctx, u.tx, u.c, cur, *p.State, u.eff)
	}
	return nil
}

func onlyWatch(p Patch) bool {
	q := Patch{Watch: p.Watch, ExpectedVersion: p.ExpectedVersion}
	b1, _ := json.Marshal(p)
	b2, _ := json.Marshal(q)
	return string(b1) == string(b2)
}

func (s *Store) watch(ctx context.Context, c Caller, it *Item, on bool) (*Item, error) {
	if err := s.tx(ctx, func(tx DB) error { return s.setWatch(ctx, tx, it.ID, c.Name, on) }); err != nil {
		return nil, err
	}
	return s.Get(ctx, c, it.ID)
}

func (s *Store) setWatch(ctx context.Context, tx DB, id, who string, on bool) error {
	if _, err := tx.Exec(ctx, `DELETE FROM work_watchers WHERE item_id = ? AND who = ?`, id, who); err != nil {
		return err
	}
	if on {
		_, err := tx.Exec(ctx, `INSERT INTO work_watchers(item_id, who) VALUES(?,?)`, id, who)
		return err
	}
	return nil
}

func (s *Store) setCriteria(ctx context.Context, tx DB, id string, in []CriterionIn, changes map[string]any) error {
	if len(in) > MaxCriteria {
		return invalid("an item has at most %d criteria", MaxCriteria)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM work_criteria WHERE item_id = ?`, id); err != nil {
		return err
	}
	done := 0
	for i, c := range in {
		text := cleanTitle(c.Text)
		if text == "" {
			return invalid("a criterion needs text")
		}
		cid := c.ID
		if cid == "" {
			cid = newID("wc")
		}
		d := 0
		if c.Done {
			d, done = 1, done+1
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_criteria(id, item_id, pos, text, done) VALUES(?,?,?,?,?)`, cid, id, i, text, d); err != nil {
			return err
		}
	}
	changes["criteria"] = map[string]any{"total": len(in), "done": done}
	return nil
}
