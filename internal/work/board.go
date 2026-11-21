package work

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Column is one column of a board: a state of the workflow.
type Column struct {
	State    string  `json:"state"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	WIP      int     `json:"wip,omitempty"`
	Count    int     `json:"count"`
	OverWIP  bool    `json:"over_wip,omitempty"` // a warning, never a refusal
	Points   float64 `json:"points"`
	Items    []*Item `json:"items"`
	Shown    int     `json:"shown"`
}

// Lane is a swimlane of a board.
type Lane struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Board is the data of a board.
type Board struct {
	Scope   Scope             `json:"scope"`
	Columns []Column          `json:"columns"`
	Lanes   []Lane            `json:"lanes,omitempty"`
	LaneBy  string            `json:"lane_by,omitempty"`
	Cards   map[string]string `json:"lane_of,omitempty"` // item id -> lane key
	Query   string            `json:"query,omitempty"`
}

// BoardOptions narrows a board.
type BoardOptions struct {
	Scope     Scope
	Query     string
	LaneBy    string // assignee, epic, priority or ""
	Sprint    string // a sprint's name, or "active"
	PerColumn int
}

// Board builds a board: a column for each state of the workflow, the items in
// backlog order, WIP warnings, and optionally swimlanes.
func (s *Store) Board(ctx context.Context, c Caller, o BoardOptions) (*Board, error) {
	if err := o.Scope.Validate(); err != nil {
		return nil, err
	}
	if !c.canRead(o.Scope) {
		return nil, notFound("no such project")
	}
	if o.LaneBy != "" && !slices.Contains([]string{"assignee", "epic", "priority"}, o.LaneBy) {
		return nil, invalid("lanes are by assignee, epic or priority")
	}
	eff, err := s.Settings(ctx, o.Scope)
	if err != nil {
		return nil, err
	}
	per := o.PerColumn
	if per <= 0 || per > 200 {
		per = 100
	}
	query := o.Query
	if o.Sprint == "active" {
		var name string
		if err := s.db.QueryRow(ctx, `SELECT name FROM work_sprints WHERE scope_kind = ? AND scope_id = ? AND state = 'active'`, o.Scope.Kind, o.Scope.ID).Scan(&name); err == nil {
			query += ` sprint:"` + name + `"`
		} else {
			query += ` sprint:"` + "\x00none" + `"`
		}
	} else if o.Sprint != "" {
		query += ` sprint:"` + o.Sprint + `"`
	}
	b := &Board{Scope: o.Scope, LaneBy: o.LaneBy, Query: o.Query, Cards: map[string]string{}}
	laneNames := map[string]string{}
	for _, st := range eff.States {
		pg, err := s.List(ctx, c, ListOptions{Scope: &o.Scope, Query: strings.TrimSpace(query + " state:" + st.Key), Limit: per})
		if err != nil {
			return nil, err
		}
		col := Column{State: st.Key, Name: st.Name, Category: st.Category, WIP: st.WIP, Count: pg.Total, Items: pg.Items, Shown: len(pg.Items)}
		col.OverWIP = st.WIP > 0 && pg.Total > st.WIP
		for _, it := range pg.Items {
			col.Points += it.Estimate
			if o.LaneBy != "" {
				k, n := laneOf(it, o.LaneBy)
				b.Cards[it.ID] = k
				laneNames[k] = n
			}
		}
		b.Columns = append(b.Columns, col)
	}
	if o.LaneBy != "" {
		keys := make([]string, 0, len(laneNames))
		for k := range laneNames {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if o.LaneBy == "priority" {
				return PriorityWeight(keys[i]) < PriorityWeight(keys[j])
			}
			if keys[i] == "" || keys[j] == "" {
				return keys[j] == ""
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			b.Lanes = append(b.Lanes, Lane{Key: k, Name: laneNames[k]})
		}
	}
	return b, nil
}

func laneOf(it *Item, by string) (key, name string) {
	switch by {
	case "assignee":
		if len(it.Assignees) == 0 {
			return "", "Unassigned"
		}
		return it.Assignees[0].Name, it.Assignees[0].Name
	case "epic":
		if it.Type == TypeEpic {
			return it.Ref, it.Ref + " " + it.Title
		}
		if it.ParentRef == "" {
			return "", "No epic"
		}
		return it.ParentRef, it.ParentRef
	default:
		return it.Priority, strings.ToUpper(it.Priority[:1]) + it.Priority[1:]
	}
}

// Move moves a card on a board: to a column (a state), optionally to a place
// within it (before another item). It is the one operation that the drag, the
// keyboard and the menu all make.
func (s *Store) Move(ctx context.Context, c Caller, ref, state string, before string) (*Item, error) {
	it, err := s.Update(ctx, c, ref, Patch{State: &state})
	if err != nil {
		return nil, err
	}
	if before != "" {
		return s.Rank(ctx, c, ref, Position{Where: "before", Other: before})
	}
	return it, nil
}

// ---- charts ----

// Point of a chart's series.
type Point struct {
	Date      string  `json:"date"`
	Remaining float64 `json:"remaining"`
	Ideal     float64 `json:"ideal"`
	Done      float64 `json:"done"`
	Added     float64 `json:"added,omitempty"`
}

// Burndown is what remains of a sprint's work, day by day, against a straight
// line from the commitment to zero.
type Burndown struct {
	Sprint    string  `json:"sprint"`
	Unit      string  `json:"unit"`
	Committed float64 `json:"committed"`
	Points    []Point `json:"points"`
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Burndown computes the chart of a sprint from the work's own record: when
// each item joined the sprint and when it was finished.
func (s *Store) Burndown(ctx context.Context, c Caller, sprintID string) (*Burndown, error) {
	sp, err := s.GetSprint(ctx, c, sprintID)
	if err != nil {
		return nil, err
	}
	startAt, _ := ParseTime(sp.StartAt)
	endAt, _ := ParseTime(sp.EndAt)
	eff, err := s.Settings(ctx, sp.Scope)
	if err != nil {
		return nil, err
	}
	type it struct {
		est, added, closed int64
		committed          bool
		done               bool
	}
	items, err := queryAll(ctx, s.db, func(r scanner) (it, error) {
		var x it
		var com int
		var state string
		if err := r.Scan(&x.est, &x.added, &com, &state, &x.closed); err != nil {
			return x, err
		}
		x.committed, x.done = com == 1, eff.Category(state) == CatDone
		return x, nil
	}, `SELECT si.estimate_milli, si.added_at, si.committed, i.state, i.closed_at FROM work_sprint_items si JOIN work_items i ON i.id = si.item_id WHERE si.sprint_id = ?`, sprintID)
	if err != nil {
		return nil, err
	}
	var committed float64
	for _, x := range items {
		if x.committed || sp.State == "planned" {
			committed += fromMilli(x.est)
		}
	}
	bd := &Burndown{Sprint: sp.Name, Unit: sp.Unit, Committed: committed}
	first, last := dayStart(time.UnixMilli(startAt)), dayStart(time.UnixMilli(endAt))
	days := int(last.Sub(first).Hours()/24) + 1
	if days < 1 || days > 400 {
		return nil, invalid("a sprint is up to 400 days long")
	}
	now := s.now()
	for d := 0; d < days; d++ {
		day := first.AddDate(0, 0, d)
		endOfDay := day.Add(24*time.Hour).UnixMilli() - 1
		if day.After(now) && sp.State != "completed" {
			break
		}
		var remaining, done, added float64
		for _, x := range items {
			if !x.committed && x.added > endOfDay {
				continue // not in the sprint yet
			}
			if !x.committed {
				added += fromMilli(x.est)
			}
			if x.done && x.closed > 0 && x.closed <= endOfDay {
				done += fromMilli(x.est)
				continue
			}
			remaining += fromMilli(x.est)
		}
		ideal := 0.0
		if days > 1 {
			ideal = committed * float64(days-1-d) / float64(days-1)
		}
		bd.Points = append(bd.Points, Point{Date: day.Format("2006-01-02"), Remaining: round2(remaining), Ideal: round2(ideal), Done: round2(done), Added: round2(added)})
	}
	return bd, nil
}

// VelocityEntry is one finished sprint.
type VelocityEntry struct {
	Sprint    string  `json:"sprint"`
	Committed float64 `json:"committed"`
	Completed float64 `json:"completed"`
	Unit      string  `json:"unit"`
}

// Velocity lists the last finished sprints, oldest first, with the average.
type Velocity struct {
	Sprints []VelocityEntry `json:"sprints"`
	Average float64         `json:"average"`
	Unit    string          `json:"unit"`
}

// Velocity computes the velocity of a scope's last n completed sprints.
func (s *Store) Velocity(ctx context.Context, c Caller, sc Scope, n int) (*Velocity, error) {
	if n <= 0 || n > 50 {
		n = 8
	}
	all, err := s.Sprints(ctx, c, sc)
	if err != nil {
		return nil, err
	}
	v := &Velocity{Sprints: []VelocityEntry{}}
	for _, sp := range all {
		if sp.State != "completed" {
			continue
		}
		v.Unit = sp.Unit
		v.Sprints = append([]VelocityEntry{{Sprint: sp.Name, Committed: sp.Committed, Completed: sp.Completed, Unit: sp.Unit}}, v.Sprints...)
		if len(v.Sprints) >= n {
			break
		}
	}
	if len(v.Sprints) > 0 {
		var sum float64
		for _, e := range v.Sprints {
			sum += e.Completed
		}
		v.Average = sum / float64(len(v.Sprints))
	}
	return v, nil
}

// CFDDay is one day of a cumulative flow chart: how many items were in each
// state category at the end of the day.
type CFDDay struct {
	Date   string         `json:"date"`
	Counts map[string]int `json:"counts"` // by state key
}

// CFD is a cumulative flow diagram.
type CFD struct {
	States []State  `json:"states"`
	Days   []CFDDay `json:"days"`
}

type transition struct {
	at    int64
	state string
}

// stateTimelines reconstructs when each item of a scope entered each state,
// from the history alone.
func (s *Store) stateTimelines(ctx context.Context, sc Scope) (map[string][]transition, error) {
	rows, err := s.db.Query(ctx, `SELECT e.item_id, e.at, e.kind, e.data FROM work_events e JOIN work_items i ON i.id = e.item_id
		WHERE i.scope_kind = ? AND i.scope_id = ? AND e.kind IN ('created','state','claimed','review_requested','agent_released','agent_handed_back','lease_expired') ORDER BY e.item_id, e.seq`, sc.Kind, sc.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]transition{}
	for rows.Next() {
		var id, kind, data string
		var at int64
		if err := rows.Scan(&id, &at, &kind, &data); err != nil {
			return nil, err
		}
		var m struct {
			State string `json:"state"`
			To    string `json:"to"`
		}
		_ = jsonUnmarshal(data, &m)
		to := ""
		switch kind {
		case "created":
			to = m.State
		case "state", "claimed", "review_requested":
			to = m.To
		}
		if to != "" {
			out[id] = append(out[id], transition{at, to})
		}
	}
	return out, rows.Err()
}

// CFD computes the cumulative flow of a scope over the last days.
func (s *Store) CFD(ctx context.Context, c Caller, sc Scope, days int) (*CFD, error) {
	if !c.canRead(sc) {
		return nil, notFound("no such project")
	}
	if days <= 0 || days > 365 {
		days = 30
	}
	eff, err := s.Settings(ctx, sc)
	if err != nil {
		return nil, err
	}
	tl, err := s.stateTimelines(ctx, sc)
	if err != nil {
		return nil, err
	}
	// Confidential items count in the total only for those who may see them;
	// a chart of counts must not give them away, so they are left out.
	conf := map[string]bool{}
	confIDs, err := queryAll(ctx, s.db, scanString, `SELECT id FROM work_items WHERE scope_kind = ? AND scope_id = ? AND confidential = 1`, sc.Kind, sc.ID)
	if err != nil {
		return nil, err
	}
	for _, id := range confIDs {
		if !c.isMaintainer(sc) {
			conf[id] = true
		}
	}
	out := &CFD{States: eff.States}
	today := dayStart(s.now())
	for d := days - 1; d >= 0; d-- {
		day := today.AddDate(0, 0, -d)
		end := day.Add(24*time.Hour).UnixMilli() - 1
		counts := map[string]int{}
		for id, ts := range tl {
			if conf[id] {
				continue
			}
			cur := ""
			for _, t := range ts {
				if t.at <= end {
					cur = t.state
				}
			}
			if cur != "" {
				counts[cur]++
			}
		}
		out.Days = append(out.Days, CFDDay{Date: day.Format("2006-01-02"), Counts: counts})
	}
	return out, nil
}

// TimeStats are the lead and cycle times of the work finished in a period.
type TimeStats struct {
	Count      int        `json:"count"`
	LeadMedian float64    `json:"lead_median_days"`
	Lead85     float64    `json:"lead_p85_days"`
	LeadMean   float64    `json:"lead_mean_days"`
	CycleMed   float64    `json:"cycle_median_days"`
	Cycle85    float64    `json:"cycle_p85_days"`
	CycleMean  float64    `json:"cycle_mean_days"`
	Items      []ItemTime `json:"items"`
	Histogram  []Bucket   `json:"histogram"`
}

// ItemTime is the times of one item.
type ItemTime struct {
	Ref   string  `json:"ref"`
	Title string  `json:"title"`
	Lead  float64 `json:"lead_days"`
	Cycle float64 `json:"cycle_days"`
}

// Bucket of a histogram of cycle times.
type Bucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// Times computes lead time (created to done) and cycle time (first started to
// done) of items finished in the last days, from the state history.
func (s *Store) Times(ctx context.Context, c Caller, sc Scope, days int) (*TimeStats, error) {
	if !c.canRead(sc) {
		return nil, notFound("no such project")
	}
	if days <= 0 || days > 730 {
		days = 90
	}
	eff, err := s.Settings(ctx, sc)
	if err != nil {
		return nil, err
	}
	since := s.now().AddDate(0, 0, -days).UnixMilli()
	pg, err := s.List(ctx, c, ListOptions{Scope: &sc, Query: fmt.Sprintf("is:closed closed:>%dd", days), Limit: 500, Sort: "updated", Desc: true})
	if err != nil {
		return nil, err
	}
	tl, err := s.stateTimelines(ctx, sc)
	if err != nil {
		return nil, err
	}
	st := &TimeStats{Items: []ItemTime{}}
	var leads, cycles []float64
	for _, it := range pg.Items {
		if eff.Category(it.State) != CatDone {
			continue
		}
		created, _ := ParseTime(it.CreatedAt)
		closed, _ := ParseTime(it.ClosedAt)
		if closed < since {
			continue
		}
		started := closed
		for _, t := range tl[it.ID] {
			if eff.Category(t.state) == CatDoing || eff.Category(t.state) == CatReview {
				started = t.at
				break
			}
		}
		lead := float64(closed-created) / 86400000
		cycle := float64(closed-started) / 86400000
		leads, cycles = append(leads, lead), append(cycles, cycle)
		st.Items = append(st.Items, ItemTime{Ref: it.Ref, Title: it.Title, Lead: round2(lead), Cycle: round2(cycle)})
	}
	st.Count = len(leads)
	sort.Float64s(leads)
	sort.Float64s(cycles)
	mean := func(v []float64) float64 {
		if len(v) == 0 {
			return 0
		}
		var s float64
		for _, x := range v {
			s += x
		}
		return round2(s / float64(len(v)))
	}
	st.LeadMedian, st.Lead85, st.LeadMean = round2(percentile(leads, .5)), round2(percentile(leads, .85)), mean(leads)
	st.CycleMed, st.Cycle85, st.CycleMean = round2(percentile(cycles, .5)), round2(percentile(cycles, .85)), mean(cycles)
	edges := []float64{1, 2, 4, 7, 14, 30}
	labels := []string{"under 1 day", "1 to 2 days", "2 to 4 days", "4 to 7 days", "1 to 2 weeks", "2 to 4 weeks", "over 4 weeks"}
	counts := make([]int, len(labels))
	for _, v := range cycles {
		i := 0
		for i < len(edges) && v >= edges[i] {
			i++
		}
		counts[i]++
	}
	for i, l := range labels {
		st.Histogram = append(st.Histogram, Bucket{Label: l, Count: counts[i]})
	}
	return st, nil
}

// RoadmapRow is a bar on a roadmap.
type RoadmapRow struct {
	Kind    string `json:"kind"` // epic, milestone or sprint
	Ref     string `json:"ref,omitempty"`
	Title   string `json:"title"`
	StartAt string `json:"start_at,omitempty"`
	DueAt   string `json:"due_at,omitempty"`
	Percent int    `json:"percent"`
	State   string `json:"state,omitempty"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
}

// Roadmap lists epics, milestones and sprints with their dates and progress.
// An epic without dates takes them from its children.
func (s *Store) Roadmap(ctx context.Context, c Caller, sc Scope) ([]RoadmapRow, error) {
	pg, err := s.List(ctx, c, ListOptions{Scope: &sc, Query: "type:epic", Limit: 200, Sort: "due"})
	if err != nil {
		return nil, err
	}
	out := []RoadmapRow{}
	for _, e := range pg.Items {
		row := RoadmapRow{Kind: "epic", Ref: e.Ref, Title: e.Title, StartAt: e.StartAt, DueAt: e.DueAt, State: e.State}
		kids, err := s.List(ctx, c, ListOptions{Scope: &sc, Parent: e.Ref, Limit: 500})
		if err != nil {
			return nil, err
		}
		eff, _ := s.settingsCached(ctx, sc)
		for _, k := range kids.Items {
			row.Total++
			if eff.Category(k.State) == CatDone {
				row.Done++
			}
			if row.StartAt == "" && k.StartAt != "" || (k.StartAt != "" && k.StartAt < row.StartAt) {
				row.StartAt = k.StartAt
			}
			if k.DueAt > row.DueAt {
				row.DueAt = k.DueAt
			}
		}
		if row.Total > 0 {
			row.Percent = row.Done * 100 / row.Total
		}
		out = append(out, row)
	}
	ms, err := s.Milestones(ctx, c, sc)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		out = append(out, RoadmapRow{Kind: "milestone", Title: m.Title, DueAt: m.DueAt, Percent: m.Percent, State: m.State, Total: m.Total, Done: m.Done})
	}
	sps, err := s.Sprints(ctx, c, sc)
	if err != nil {
		return nil, err
	}
	for _, sp := range sps {
		p := 0
		if sp.Items > 0 {
			p = sp.ItemsDone * 100 / sp.Items
		}
		out = append(out, RoadmapRow{Kind: "sprint", Title: sp.Name, StartAt: sp.StartAt, DueAt: sp.EndAt, Percent: p, State: sp.State, Total: sp.Items, Done: sp.ItemsDone})
	}
	return out, nil
}
