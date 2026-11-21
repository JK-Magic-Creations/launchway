package work

import (
	"context"
	"encoding/json"
	"strings"
)

// Milestone is a goal with a date that items belong to.
type Milestone struct {
	ID          string  `json:"id"`
	Scope       Scope   `json:"scope"`
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	DueAt       string  `json:"due_at,omitempty"`
	State       string  `json:"state"` // open or closed
	Total       int     `json:"total"`
	Done        int     `json:"done"`
	Points      float64 `json:"points"`
	PointsDone  float64 `json:"points_done"`
	Percent     int     `json:"percent"`
}

// MilestoneIn creates or changes a milestone.
type MilestoneIn struct {
	Title       string  `json:"title"`
	Description *string `json:"description"`
	DueAt       *string `json:"due_at"`
	State       *string `json:"state"`
}

func (s *Store) milestoneID(ctx context.Context, tx DB, sc Scope, title string, _ bool) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM work_milestones WHERE scope_kind = ? AND scope_id = ? AND title = ?`, sc.Kind, sc.ID, title).Scan(&id)
	if isNoRows(err) {
		return "", notFound("no milestone " + title + " in " + sc.String())
	}
	return id, err
}

// CreateMilestone adds a milestone.
func (s *Store) CreateMilestone(ctx context.Context, c Caller, sc Scope, in MilestoneIn) (*Milestone, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	if !c.canWrite(sc) {
		return nil, forbidden("you may not plan work in %s", sc)
	}
	title := cleanTitle(in.Title)
	if title == "" || len(title) > 100 {
		return nil, invalid("a milestone has a title of 1 to 100 characters")
	}
	var due int64
	if in.DueAt != nil {
		var err error
		if due, err = ParseTime(*in.DueAt); err != nil {
			return nil, err
		}
	}
	desc := ""
	if in.Description != nil {
		desc = cleanBody(*in.Description)
	}
	id := newID("wm")
	err := s.tx(ctx, func(tx DB) error {
		if err := s.ensureScope(ctx, tx, sc); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_milestones WHERE scope_kind = ? AND scope_id = ? AND title = ?`, sc.Kind, sc.ID, title).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return conflict("there is already a milestone called %s", title)
		}
		_, err := tx.Exec(ctx, `INSERT INTO work_milestones(id, scope_kind, scope_id, title, description, due_at, state, created_at) VALUES(?,?,?,?,?,?,?,?)`,
			id, sc.Kind, sc.ID, title, desc, due, "open", ms(s.now()))
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetMilestone(ctx, c, id)
}

// UpdateMilestone changes one.
func (s *Store) UpdateMilestone(ctx context.Context, c Caller, id string, in MilestoneIn) (*Milestone, error) {
	m, err := s.GetMilestone(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(m.Scope) {
		return nil, forbidden("you may not plan work in %s", m.Scope)
	}
	title, desc, due, state := m.Title, m.Description, m.DueAt, m.State
	if in.Title != "" {
		title = cleanTitle(in.Title)
	}
	if in.Description != nil {
		desc = cleanBody(*in.Description)
	}
	if in.DueAt != nil {
		due = *in.DueAt
	}
	if in.State != nil {
		if *in.State != "open" && *in.State != "closed" {
			return nil, invalid("a milestone is open or closed")
		}
		state = *in.State
	}
	d, err := ParseTime(due)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE work_milestones SET title = ?, description = ?, due_at = ?, state = ? WHERE id = ?`, title, desc, d, state, id); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil, conflict("there is already a milestone called %s", title)
		}
		return nil, err
	}
	return s.GetMilestone(ctx, c, id)
}

// GetMilestone reads one with its progress.
func (s *Store) GetMilestone(ctx context.Context, c Caller, id string) (*Milestone, error) {
	m := &Milestone{}
	var due int64
	err := s.db.QueryRow(ctx, `SELECT id, scope_kind, scope_id, title, description, due_at, state FROM work_milestones WHERE id = ?`, id).
		Scan(&m.ID, &m.Scope.Kind, &m.Scope.ID, &m.Title, &m.Description, &due, &m.State)
	if isNoRows(err) {
		return nil, notFound("no milestone " + id)
	}
	if err != nil {
		return nil, err
	}
	if !c.canRead(m.Scope) {
		return nil, notFound("no milestone " + id)
	}
	m.DueAt = iso(due)
	return m, s.milestoneProgress(ctx, c, m)
}

func (s *Store) milestoneProgress(ctx context.Context, c Caller, m *Milestone) error {
	eff, err := s.settingsCached(ctx, m.Scope)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT state, estimate_milli, confidential, reporter, id FROM work_items WHERE milestone_id = ?`, m.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state, rep, id string
		var est int64
		var conf int
		if err := rows.Scan(&state, &est, &conf, &rep, &id); err != nil {
			return err
		}
		cat := eff.Category(state)
		if cat == CatCancelled {
			continue
		}
		m.Total++
		m.Points += fromMilli(est)
		if cat == CatDone {
			m.Done++
			m.PointsDone += fromMilli(est)
		}
	}
	if m.Total > 0 {
		m.Percent = m.Done * 100 / m.Total
	}
	return rows.Err()
}

// Milestones lists a scope's milestones, soonest due first.
func (s *Store) Milestones(ctx context.Context, c Caller, sc Scope) ([]*Milestone, error) {
	if !c.canRead(sc) {
		return nil, notFound("no such project")
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM work_milestones WHERE scope_kind = ? AND scope_id = ? ORDER BY CASE WHEN due_at = 0 THEN 1 ELSE 0 END, due_at, title`, sc.Kind, sc.ID)
	if err != nil {
		return nil, err
	}
	var idList []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		idList = append(idList, id)
	}
	rows.Close()
	out := []*Milestone{}
	for _, id := range idList {
		m, err := s.GetMilestone(ctx, c, id)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Sprint is an iteration with dates and a goal.
type Sprint struct {
	ID          string             `json:"id"`
	Scope       Scope              `json:"scope"`
	Name        string             `json:"name"`
	Goal        string             `json:"goal,omitempty"`
	StartAt     string             `json:"start_at"`
	EndAt       string             `json:"end_at"`
	State       string             `json:"state"` // planned, active or completed
	Capacity    map[string]float64 `json:"capacity,omitempty"`
	Unit        string             `json:"unit"`
	Committed   float64            `json:"committed"`
	Completed   float64            `json:"completed"`
	Added       float64            `json:"added"` // taken on after the sprint started
	CarriedOver int                `json:"carried_over"`
	Items       int                `json:"items"`
	ItemsDone   int                `json:"items_done"`
	TotalCap    float64            `json:"total_capacity"`
	Warning     string             `json:"warning,omitempty"` // advice: committed beyond capacity
}

// SprintIn creates or changes a sprint.
type SprintIn struct {
	Name     string             `json:"name"`
	Goal     *string            `json:"goal"`
	StartAt  *string            `json:"start_at"`
	EndAt    *string            `json:"end_at"`
	Capacity map[string]float64 `json:"capacity"`
}

func (s *Store) sprintID(ctx context.Context, tx DB, sc Scope, name string) (string, error) {
	var id, state string
	err := tx.QueryRow(ctx, `SELECT id, state FROM work_sprints WHERE scope_kind = ? AND scope_id = ? AND name = ?`, sc.Kind, sc.ID, name).Scan(&id, &state)
	if isNoRows(err) {
		return "", notFound("no sprint " + name + " in " + sc.String())
	}
	if err != nil {
		return "", err
	}
	if state == "completed" {
		return "", invalid("sprint %s is completed", name)
	}
	return id, nil
}

// sprintAdd puts an item in a sprint; after the sprint started it is scope
// taken on, which the report shows.
func (s *Store) sprintAdd(ctx context.Context, tx DB, sprintID, itemID string, estMilli int64) error {
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM work_sprints WHERE id = ?`, sprintID).Scan(&state); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_sprint_items(sprint_id, item_id, estimate_milli, added_at, committed, completed) VALUES(?,?,?,?,?,0)`,
		sprintID, itemID, estMilli, ms(s.now()), 0)
	return err
}

func (s *Store) sprintRemove(ctx context.Context, tx DB, sprintID, itemID string) error {
	_, err := tx.Exec(ctx, `DELETE FROM work_sprint_items WHERE sprint_id = ? AND item_id = ?`, sprintID, itemID)
	return err
}

// CreateSprint plans a sprint.
func (s *Store) CreateSprint(ctx context.Context, c Caller, sc Scope, in SprintIn) (*Sprint, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	if !c.canWrite(sc) {
		return nil, forbidden("you may not plan work in %s", sc)
	}
	name := cleanTitle(in.Name)
	if name == "" || len(name) > 100 {
		return nil, invalid("a sprint has a name of 1 to 100 characters")
	}
	start, end := int64(0), int64(0)
	var err error
	if in.StartAt != nil {
		if start, err = ParseTime(*in.StartAt); err != nil {
			return nil, err
		}
	}
	if in.EndAt != nil {
		if end, err = ParseTime(*in.EndAt); err != nil {
			return nil, err
		}
	}
	if start == 0 || end == 0 || end <= start {
		return nil, invalid("a sprint has a start and an end, and the end is after the start")
	}
	goal := ""
	if in.Goal != nil {
		goal = cleanBody(*in.Goal)
	}
	capJSON, err := checkCapacity(in.Capacity)
	if err != nil {
		return nil, err
	}
	id := newID("wsp")
	err = s.tx(ctx, func(tx DB) error {
		if err := s.ensureScope(ctx, tx, sc); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_sprints WHERE scope_kind = ? AND scope_id = ? AND name = ?`, sc.Kind, sc.ID, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return conflict("there is already a sprint called %s", name)
		}
		_, err := tx.Exec(ctx, `INSERT INTO work_sprints(id, scope_kind, scope_id, name, goal, start_at, end_at, state, capacity, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			id, sc.Kind, sc.ID, name, goal, start, end, "planned", capJSON, ms(s.now()))
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetSprint(ctx, c, id)
}

func checkCapacity(m map[string]float64) (string, error) {
	if len(m) > 200 {
		return "", invalid("capacity is for at most 200 people")
	}
	for k, v := range m {
		if k == "" || v < 0 || v > 100000 {
			return "", invalid("capacity is a person and a number of at least 0")
		}
	}
	b, _ := json.Marshal(m)
	if len(m) == 0 {
		return "{}", nil
	}
	return string(b), nil
}

// UpdateSprint changes a planned or active sprint's name, goal, dates and capacity.
func (s *Store) UpdateSprint(ctx context.Context, c Caller, id string, in SprintIn) (*Sprint, error) {
	sp, err := s.GetSprint(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(sp.Scope) {
		return nil, forbidden("you may not plan work in %s", sp.Scope)
	}
	if sp.State == "completed" {
		return nil, conflict("a completed sprint is a record and is not changed")
	}
	name, goal, startS, endS := sp.Name, sp.Goal, sp.StartAt, sp.EndAt
	if in.Name != "" {
		name = cleanTitle(in.Name)
	}
	if in.Goal != nil {
		goal = cleanBody(*in.Goal)
	}
	if in.StartAt != nil {
		startS = *in.StartAt
	}
	if in.EndAt != nil {
		endS = *in.EndAt
	}
	start, err := ParseTime(startS)
	if err != nil {
		return nil, err
	}
	end, err := ParseTime(endS)
	if err != nil {
		return nil, err
	}
	if end <= start {
		return nil, invalid("the end is after the start")
	}
	capMap := sp.Capacity
	if in.Capacity != nil {
		capMap = in.Capacity
	}
	capJSON, err := checkCapacity(capMap)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE work_sprints SET name = ?, goal = ?, start_at = ?, end_at = ?, capacity = ? WHERE id = ?`, name, goal, start, end, capJSON, id); err != nil {
		return nil, conflict("there is already a sprint called %s", name)
	}
	return s.GetSprint(ctx, c, id)
}

// StartSprint makes a planned sprint active and records what it committed to.
// Only one sprint of a scope is active.
func (s *Store) StartSprint(ctx context.Context, c Caller, id string) (*Sprint, error) {
	sp, err := s.GetSprint(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(sp.Scope) || c.Agent != nil {
		return nil, forbidden("you may not start a sprint in %s", sp.Scope)
	}
	if sp.State != "planned" {
		return nil, conflict("sprint %s is %s", sp.Name, sp.State)
	}
	err = s.tx(ctx, func(tx DB) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_sprints WHERE scope_kind = ? AND scope_id = ? AND state = 'active'`, sp.Scope.Kind, sp.Scope.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return conflict("another sprint is active; complete it first")
		}
		now := ms(s.now())
		if _, err := tx.Exec(ctx, `UPDATE work_sprints SET state = 'active', started_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		// What each item is estimated at, as of now, is what was committed to.
		if _, err := tx.Exec(ctx, `UPDATE work_sprint_items SET committed = 1, estimate_milli = (SELECT estimate_milli FROM work_items i WHERE i.id = work_sprint_items.item_id) WHERE sprint_id = ?`, id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetSprint(ctx, c, id)
}

// CompleteSprint ends an active sprint. Items not done move to carryTo (a
// sprint's name; "" returns them to the backlog), which is how work carries over.
func (s *Store) CompleteSprint(ctx context.Context, c Caller, id, carryTo string) (*Sprint, error) {
	sp, err := s.GetSprint(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(sp.Scope) || c.Agent != nil {
		return nil, forbidden("you may not complete a sprint in %s", sp.Scope)
	}
	if sp.State != "active" {
		return nil, conflict("sprint %s is %s, not active", sp.Name, sp.State)
	}
	eff, err := s.Settings(ctx, sp.Scope)
	if err != nil {
		return nil, err
	}
	err = s.tx(ctx, func(tx DB) error {
		next := ""
		if carryTo != "" {
			if err := tx.QueryRow(ctx, `SELECT id FROM work_sprints WHERE scope_kind = ? AND scope_id = ? AND name = ? AND state <> 'completed' AND id <> ?`, sp.Scope.Kind, sp.Scope.ID, carryTo, id).Scan(&next); err != nil {
				return invalid("sprint %s is not a planned or active sprint of this project", carryTo)
			}
		}
		rows, err := tx.Query(ctx, `SELECT si.item_id, i.state, i.estimate_milli FROM work_sprint_items si JOIN work_items i ON i.id = si.item_id WHERE si.sprint_id = ?`, id)
		if err != nil {
			return err
		}
		type row struct {
			id, state string
			est       int64
		}
		var all []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.state, &r.est); err != nil {
				rows.Close()
				return err
			}
			all = append(all, r)
		}
		rows.Close()
		now := ms(s.now())
		for _, r := range all {
			done := 0
			if eff.Closed(r.state) {
				if eff.Category(r.state) == CatDone {
					done = 1
				}
			} else {
				// Not finished: it carries over, and the sprint keeps the record.
				if _, err := tx.Exec(ctx, `UPDATE work_items SET sprint_id = ?, version = version + 1, updated_at = ? WHERE id = ?`, next, now, r.id); err != nil {
					return err
				}
				if next != "" {
					if _, err := tx.Exec(ctx, `INSERT INTO work_sprint_items(sprint_id, item_id, estimate_milli, added_at, committed, completed) VALUES(?,?,?,?,0,0)`, next, r.id, r.est, now); err != nil {
						return err
					}
				}
				if err := s.event(ctx, tx, r.id, c.Actor, "carried_over", 0, map[string]any{"from": sp.Name, "to": carryTo}); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE work_sprint_items SET completed = ? WHERE sprint_id = ? AND item_id = ?`, done, id, r.id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE work_sprints SET state = 'completed', completed_at = ? WHERE id = ?`, now, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetSprint(ctx, c, id)
}

// GetSprint reads one with its numbers.
func (s *Store) GetSprint(ctx context.Context, c Caller, id string) (*Sprint, error) {
	sp := &Sprint{}
	var start, end int64
	var capJSON string
	err := s.db.QueryRow(ctx, `SELECT id, scope_kind, scope_id, name, goal, start_at, end_at, state, capacity FROM work_sprints WHERE id = ?`, id).
		Scan(&sp.ID, &sp.Scope.Kind, &sp.Scope.ID, &sp.Name, &sp.Goal, &start, &end, &sp.State, &capJSON)
	if isNoRows(err) {
		return nil, notFound("no sprint " + id)
	}
	if err != nil {
		return nil, err
	}
	if !c.canRead(sp.Scope) {
		return nil, notFound("no sprint " + id)
	}
	sp.StartAt, sp.EndAt = iso(start), iso(end)
	if capJSON != "" {
		_ = json.Unmarshal([]byte(capJSON), &sp.Capacity)
	}
	eff, err := s.settingsCached(ctx, sp.Scope)
	if err != nil {
		return nil, err
	}
	sp.Unit = eff.Unit
	rows, err := s.db.Query(ctx, `SELECT si.estimate_milli, si.committed, si.completed, i.state, i.estimate_milli FROM work_sprint_items si JOIN work_items i ON i.id = si.item_id WHERE si.sprint_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var snap, cur int64
		var committed, completed int
		var state string
		if err := rows.Scan(&snap, &committed, &completed, &state, &cur); err != nil {
			return nil, err
		}
		sp.Items++
		est := cur
		if sp.State == "completed" || committed == 1 {
			est = snap
		}
		isDone := completed == 1 || (sp.State != "completed" && eff.Category(state) == CatDone)
		if committed == 1 {
			sp.Committed += fromMilli(snap)
		} else if sp.State != "planned" {
			sp.Added += fromMilli(est)
		}
		if sp.State == "planned" {
			sp.Committed += fromMilli(est)
		}
		if isDone {
			sp.ItemsDone++
			sp.Completed += fromMilli(est)
		}
	}
	for _, v := range sp.Capacity {
		sp.TotalCap += v
	}
	if sp.TotalCap > 0 && sp.Committed+sp.Added > sp.TotalCap {
		sp.Warning = "The sprint holds more work than the team's capacity. Taking something out, or raising a capacity, are both fine; nothing is changed for you."
	}
	if sp.State == "completed" {
		_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_events e WHERE e.kind = 'carried_over' AND e.data LIKE ?`, `%"from":"`+sp.Name+`"%`).Scan(&sp.CarriedOver)
	}
	return sp, rows.Err()
}

// Sprints lists a scope's sprints, newest first.
func (s *Store) Sprints(ctx context.Context, c Caller, sc Scope) ([]*Sprint, error) {
	if !c.canRead(sc) {
		return nil, notFound("no such project")
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM work_sprints WHERE scope_kind = ? AND scope_id = ? ORDER BY start_at DESC`, sc.Kind, sc.ID)
	if err != nil {
		return nil, err
	}
	var idList []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		idList = append(idList, id)
	}
	rows.Close()
	out := []*Sprint{}
	for _, id := range idList {
		sp, err := s.GetSprint(ctx, c, id)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, nil
}
