package work

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Chains turns a scope into the scopes whose settings apply to it, nearest
// first. The server sets it from the levels; the default is the scope and the
// installation.
type Chains func(Scope) []Scope

// SetChains sets how scopes inherit.
func (s *Store) SetChains(c Chains) { s.chains = c }

func (s *Store) chain(sc Scope) []Scope {
	if s.chains != nil {
		return s.chains(sc)
	}
	if sc.Kind == ScopeInstallation {
		return []Scope{sc}
	}
	return []Scope{sc, {Kind: ScopeInstallation}}
}

// Settings returns what is in force for a scope.
func (s *Store) Settings(ctx context.Context, sc Scope) (*Effective, error) {
	return s.settingsIn(ctx, s.db, sc)
}

func (s *Store) settingsIn(ctx context.Context, db DB, sc Scope) (*Effective, error) {
	return s.resolveIn(ctx, db, s.chain(sc))
}

// NewItem is what creating an item takes.
type NewItem struct {
	Scope        Scope          `json:"scope"`
	Type         string         `json:"type"`
	Title        string         `json:"title"`
	Body         string         `json:"body"`
	State        string         `json:"state"`
	Priority     string         `json:"priority"`
	Estimate     float64        `json:"estimate"`
	Parent       string         `json:"parent"` // a ref
	Milestone    string         `json:"milestone"`
	Sprint       string         `json:"sprint"`
	StartAt      string         `json:"start_at"`
	DueAt        string         `json:"due_at"`
	Labels       []string       `json:"labels"`
	Assignees    []string       `json:"assignees"`
	Criteria     []string       `json:"criteria"`
	Custom       map[string]any `json:"custom"`
	Confidential bool           `json:"confidential"`
	Difficulty   string         `json:"difficulty"`
	Reporter     string         `json:"reporter"` // only for imports: otherwise the caller
	SourceKey    string         `json:"source_key"`
	Number       int            `json:"number"` // only for imports that keep numbers
	CreatedAt    string         `json:"created_at"`
	ClosedAt     string         `json:"closed_at"`
	Resolution   string         `json:"resolution"`
	// Repository (OWNER/NAME) is the repository the item is for: it is the item's scope when none is given, and
	// otherwise the item is tied to the repository (the API does this; Create ignores it).
	Repository string `json:"repository"`
}

const itemCols = `id, scope_kind, scope_id, number, type, title, body, state, resolution, priority, estimate_milli, parent_id, milestone_id, sprint_id, rank,
 start_at, due_at, reporter, reporter_kind, confidential, difficulty, custom, source_key, version, created_at, updated_at, closed_at`

// itemColsI is itemCols with each column qualified by the alias i.
var itemColsI = func() string {
	var out []string
	for _, c := range strings.Split(itemCols, ",") {
		out = append(out, "i."+strings.TrimSpace(c))
	}
	return strings.Join(out, ", ")
}()

type scanner interface{ Scan(...any) error }

func scanItem(sc scanner) (*Item, error) {
	it := &Item{}
	var est, start, due, cr, up, cl int64
	var conf int
	var custom string
	if err := sc.Scan(&it.ID, &it.Scope.Kind, &it.Scope.ID, &it.Number, &it.Type, &it.Title, &it.Body, &it.State, &it.Resolution, &it.Priority, &est,
		&it.ParentID, &it.MilestoneID, &it.SprintID, &it.Rank, &start, &due, &it.Reporter, &it.ReporterKind, &conf, &it.Difficulty, &custom, &it.SourceKey, &it.Version, &cr, &up, &cl); err != nil {
		return nil, err
	}
	it.Estimate, it.Confidential = fromMilli(est), conf != 0
	it.StartAt, it.DueAt, it.CreatedAt, it.UpdatedAt, it.ClosedAt = iso(start), iso(due), iso(cr), iso(up), iso(cl)
	if custom != "" && custom != "{}" {
		_ = json.Unmarshal([]byte(custom), &it.Custom)
	}
	it.Labels, it.Assignees = []string{}, []Assignee{}
	return it, nil
}

// ParseRef splits a ref such as PAY-12 into its prefix and number.
func ParseRef(ref string) (prefix string, n int, ok bool) {
	i := strings.LastIndex(ref, "-")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(ref[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return strings.ToUpper(ref[:i]), n, true
}

// idOf finds an item's id from an id or a ref.
func (s *Store) idOf(ctx context.Context, db DB, ref string) (string, error) {
	if strings.HasPrefix(ref, "wi_") {
		return ref, nil
	}
	prefix, n, ok := ParseRef(ref)
	if !ok {
		return "", invalid("%q is not an item reference (such as PAY-12)", ref)
	}
	var id string
	err := db.QueryRow(ctx, `SELECT i.id FROM work_items i JOIN work_scopes sc ON sc.scope_kind = i.scope_kind AND sc.scope_id = i.scope_id
		WHERE sc.prefix = ? AND i.number = ?`, prefix, n).Scan(&id)
	if isNoRows(err) {
		return "", notFound("no item " + ref)
	}
	return id, err
}

// Get returns an item by id or ref, as the caller may see it. Something the
// caller may not see is reported as not found.
func (s *Store) Get(ctx context.Context, c Caller, ref string) (*Item, error) {
	id, err := s.idOf(ctx, s.db, ref)
	if err != nil {
		return nil, err
	}
	it, err := s.load(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if !c.canSee(it) {
		return nil, notFound("no item " + ref)
	}
	if err := s.fillItemReactions(ctx, c, []*Item{it}); err != nil {
		return nil, err
	}
	return it, nil
}

// load reads one item with everything attached.
func (s *Store) load(ctx context.Context, db DB, id string) (*Item, error) {
	it, err := scanItem(db.QueryRow(ctx, `SELECT `+itemCols+` FROM work_items WHERE id = ?`, id))
	if isNoRows(err) {
		return nil, notFound("no item " + id)
	}
	if err != nil {
		return nil, err
	}
	if err := s.hydrate(ctx, db, []*Item{it}, true); err != nil {
		return nil, err
	}
	return it, nil
}

// hydrate fills in what lives in other tables. full adds the body-sized things
// (watchers, criteria); lists leave those out.
func (s *Store) hydrate(ctx context.Context, db DB, items []*Item, full bool) error {
	if len(items) == 0 {
		return nil
	}
	byID := map[string]*Item{}
	idList := make([]string, 0, len(items))
	for _, it := range items {
		byID[it.ID] = it
		idList = append(idList, it.ID)
	}
	for _, chunk := range chunks(idList, 400) {
		if err := s.hydrateChunk(ctx, db, byID, chunk, full); err != nil {
			return err
		}
	}
	// Names that live elsewhere, and the ref.
	for _, it := range items {
		p, err := s.prefixIn(ctx, db, it.Scope)
		if err != nil {
			return err
		}
		it.Ref = fmt.Sprintf("%s-%d", p, it.Number)
		eff, err := s.settingsCachedIn(ctx, db, it.Scope)
		if err != nil {
			return err
		}
		if st, ok := eff.StateByKey(it.State); ok {
			it.StateName, it.Category = st.Name, st.Category
		} else {
			it.StateName = it.State
		}
		if it.MilestoneID != "" {
			_ = db.QueryRow(ctx, `SELECT title FROM work_milestones WHERE id = ?`, it.MilestoneID).Scan(&it.Milestone)
		}
		if it.SprintID != "" {
			_ = db.QueryRow(ctx, `SELECT name FROM work_sprints WHERE id = ?`, it.SprintID).Scan(&it.Sprint)
		}
		if it.ParentID != "" {
			var pn int
			var pk, pid string
			if db.QueryRow(ctx, `SELECT number, scope_kind, scope_id FROM work_items WHERE id = ?`, it.ParentID).Scan(&pn, &pk, &pid) == nil {
				pp, _ := s.prefixIn(ctx, db, Scope{Kind: pk, ID: pid})
				it.ParentRef = fmt.Sprintf("%s-%d", pp, pn)
			}
		}
		if full || it.Type == TypeEpic {
			if p, err := s.rollup(ctx, db, it.ID, eff); err == nil && p.Total > 0 {
				it.Children = &p
			}
		}
		_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM work_comments WHERE item_id = ?`, it.ID).Scan(&it.Comments)
	}
	return nil
}

// settingsCached resolves a scope's settings, remembering them for a moment
// (settings change rarely, and a list asks for the same scope many times).
func (s *Store) settingsCached(ctx context.Context, sc Scope) (*Effective, error) {
	return s.settingsCachedIn(ctx, s.db, sc)
}

// settingsCachedIn reads through db when it must, so that inside a transaction
// nothing waits on another connection.
func (s *Store) settingsCachedIn(ctx context.Context, db DB, sc Scope) (*Effective, error) {
	s.mu.Lock()
	if e, ok := s.cache[sc]; ok && s.now().Before(e.until) {
		s.mu.Unlock()
		return e.eff, nil
	}
	s.mu.Unlock()
	eff, err := s.settingsIn(ctx, db, sc)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[Scope]cached{}
	}
	s.cache[sc] = cached{eff: eff, until: s.now().Add(cacheFor)}
	s.mu.Unlock()
	return eff, nil
}

// rollup counts an item's descendants (children and their children).
func (s *Store) rollup(ctx context.Context, db DB, id string, eff *Effective) (Progress, error) {
	var p Progress
	rows, err := db.Query(ctx, `SELECT c.state, c.estimate_milli, c.id, c.type FROM work_items c WHERE c.parent_id = ?
		UNION ALL SELECT g.state, g.estimate_milli, g.id, g.type FROM work_items g JOIN work_items c ON g.parent_id = c.id WHERE c.parent_id = ?`, id, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	parents := map[string]bool{}
	type row struct {
		state string
		est   int64
		id    string
		typ   string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.state, &r.est, &r.id, &r.typ); err != nil {
			return p, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	_ = parents
	for _, r := range all {
		p.Total++
		p.Points += fromMilli(r.est)
		switch eff.Category(r.state) {
		case CatDone:
			p.Done++
			p.PointsDone += fromMilli(r.est)
		case CatDoing, CatReview:
			p.InProgress++
		}
	}
	return p, nil
}

func chunks(s []string, n int) [][]string {
	var out [][]string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

func inList(vals []string) (string, []any) {
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(vals)), ","), args
}

// assigneeKind says whether a name is a registered agent.
func (s *Store) assigneeKind(ctx context.Context, db DB, name string) (Assignee, error) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "@"))
	forced := strings.HasPrefix(name, "agent:")
	name = strings.TrimPrefix(name, "agent:")
	if name == "" || len(name) > 100 {
		return Assignee{}, invalid("an assignee is a name")
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM work_agents WHERE name = ?`, name).Scan(&n); err != nil {
		return Assignee{}, err
	}
	if forced && n == 0 {
		return Assignee{}, invalid("there is no agent called %s", name)
	}
	if n > 0 {
		return Assignee{Name: name, Kind: KindAgent}, nil
	}
	return Assignee{Name: name, Kind: KindPerson}, nil
}

func (s *Store) checkCustom(eff *Effective, typ string, custom map[string]any) error {
	if len(custom) > MaxCustom {
		return invalid("an item has at most %d custom fields", MaxCustom)
	}
	for k, v := range custom {
		var def *FieldDef
		for i := range eff.Fields {
			if eff.Fields[i].Key == k {
				def = &eff.Fields[i]
			}
		}
		if def == nil || (len(def.Types) > 0 && !slices.Contains(def.Types, typ)) {
			return invalid("there is no custom field %q for a %s", k, typ)
		}
		switch def.Type {
		case "text", "date":
			if _, ok := v.(string); !ok {
				return invalid("field %s is text", k)
			}
		case "number":
			if _, ok := v.(float64); !ok {
				return invalid("field %s is a number", k)
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return invalid("field %s is true or false", k)
			}
		case "select":
			sv, _ := v.(string)
			if !slices.Contains(def.Options, sv) {
				return invalid("field %s is one of %s", k, strings.Join(def.Options, ", "))
			}
		}
	}
	for _, d := range eff.Fields {
		if d.Required && (len(d.Types) == 0 || slices.Contains(d.Types, typ)) {
			if _, ok := custom[d.Key]; !ok {
				return invalid("field %s is required for a %s", d.Key, typ)
			}
		}
	}
	return nil
}

// Create makes an item. The caller needs to be able to write the scope.
func (s *Store) Create(ctx context.Context, c Caller, in NewItem) (*Item, error) {
	p, err := s.prepareNew(ctx, c, in)
	if err != nil {
		return nil, err
	}
	id, err := s.insertNew(ctx, c, p)
	if err != nil {
		return nil, err
	}
	return s.load(ctx, s.db, id)
}

// newPlan is a checked NewItem.
type newPlan struct {
	in                          NewItem
	eff                         *Effective
	start, due, created, closed int64
	reporter, rkind             string
	labels                      []string
}

// normalizeNew checks and fills in the fields of a new item.
func (s *Store) normalizeNew(c Caller, eff *Effective, in *NewItem) error {
	in.Title = cleanTitle(in.Title)
	if in.Title == "" || len(in.Title) > MaxTitle {
		return invalid("a title is 1 to %d characters", MaxTitle)
	}
	if len(in.Body) > MaxBody {
		return invalid("the body is longer than %d bytes", MaxBody)
	}
	if in.Body == "" {
		for _, d := range eff.Types {
			if d.Key == in.Type {
				in.Body = d.Template
			}
		}
	}
	in.Body = cleanBody(in.Body)
	if in.State == "" {
		in.State = eff.FirstOf(CatTodo)
	}
	if _, ok := eff.StateByKey(in.State); !ok {
		return invalid("%q is not a state of this workflow", in.State)
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if !slices.Contains(Priorities, in.Priority) {
		return invalid("priority is one of %s", strings.Join(Priorities, ", "))
	}
	if in.Estimate < 0 || in.Estimate > 100000 {
		return invalid("an estimate is between 0 and 100000")
	}
	if len(in.Labels) > MaxLabels || len(in.Assignees) > MaxAssignees || len(in.Criteria) > MaxCriteria {
		return invalid("too many labels, assignees or criteria")
	}
	if err := s.checkCustom(eff, in.Type, in.Custom); err != nil {
		return err
	}
	if in.Confidential && !c.canWrite(in.Scope) {
		return forbidden("you may not make confidential work here")
	}
	return nil
}

// parseNewTimes reads the optional times of a new item.
func parseNewTimes(in NewItem) (start, due, created, closed int64, err error) {
	if start, err = ParseTime(in.StartAt); err != nil {
		return
	}
	if due, err = ParseTime(in.DueAt); err != nil {
		return
	}
	if created, err = ParseTime(in.CreatedAt); err != nil {
		return
	}
	closed, err = ParseTime(in.ClosedAt)
	return
}

func (s *Store) prepareNew(ctx context.Context, c Caller, in NewItem) (*newPlan, error) {
	if err := in.Scope.Validate(); err != nil {
		return nil, err
	}
	if !c.canWrite(in.Scope) {
		return nil, forbidden("you may not add work to %s", in.Scope)
	}
	eff, err := s.settingsCached(ctx, in.Scope)
	if err != nil {
		return nil, err
	}
	if in.Type == "" {
		in.Type = TypeIssue
	}
	if !eff.TypeAllowed(in.Type) {
		return nil, invalid("items of type %q are not used here", in.Type)
	}
	if err := s.normalizeNew(c, eff, &in); err != nil {
		return nil, err
	}
	start, due, created, closed, err := parseNewTimes(in)
	if err != nil {
		return nil, err
	}
	if in.CreatedAt != "" && !c.isMaintainer(in.Scope) && c.Kind != KindSystem {
		return nil, forbidden("only an import may set the creation time")
	}
	reporter, rkind := c.Name, c.Kind
	if in.Reporter != "" {
		if c.Kind != KindSystem && !c.isMaintainer(in.Scope) {
			return nil, forbidden("only a maintainer may report on behalf of someone")
		}
		reporter, rkind = in.Reporter, KindPerson
	}
	labels := []string{}
	for _, l := range in.Labels {
		cl, err := cleanLabel(l)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(labels, cl) {
			labels = append(labels, cl)
		}
	}
	return &newPlan{in: in, eff: eff, start: start, due: due, created: created, closed: closed, reporter: reporter, rkind: rkind, labels: labels}, nil
}

// insertNewChildren writes the labels, assignees and criteria of a new item
// and returns the assignees it added.
func (s *Store) insertNewChildren(ctx context.Context, tx DB, id string, in NewItem, labels []string) (map[string]bool, error) {
	for _, l := range labels {
		if _, err := tx.Exec(ctx, `INSERT INTO work_labels(item_id, label) VALUES(?,?)`, id, l); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	for _, name := range in.Assignees {
		a, err := s.assigneeKind(ctx, tx, name)
		if err != nil {
			return nil, err
		}
		if seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		if _, err := tx.Exec(ctx, `INSERT INTO work_assignees(item_id, who, kind) VALUES(?,?,?)`, id, a.Name, a.Kind); err != nil {
			return nil, err
		}
	}
	for i, text := range in.Criteria {
		text = cleanTitle(text)
		if text == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_criteria(id, item_id, pos, text, done) VALUES(?,?,?,?,0)`, newID("wc"), id, i, text); err != nil {
			return nil, err
		}
	}
	return seen, nil
}

func (s *Store) insertNew(ctx context.Context, c Caller, p *newPlan) (string, error) {
	var id string
	err := s.tx(ctx, func(tx DB) error {
		var err error
		id, err = s.insertNewIn(ctx, tx, c, p)
		return err
	})
	return id, err
}

// insertNewIn is insertNew inside a transaction already open.
func (s *Store) insertNewIn(ctx context.Context, tx DB, c Caller, p *newPlan) (string, error) {
	in, eff, start, due, created, closed, reporter, rkind, labels := p.in, p.eff, p.start, p.due, p.created, p.closed, p.reporter, p.rkind, p.labels
	var id string
	var err error
	err = func() error {
		var n int
		if in.Number > 0 {
			if err := s.reserve(ctx, tx, in.Scope, in.Number); err != nil {
				return err
			}
			n = in.Number
		} else if n, err = s.allocate(ctx, tx, in.Scope); err != nil {
			return err
		}
		id = newID("wi")
		now := ms(s.now())
		if created == 0 {
			created = now
		}
		var parentID string
		if in.Parent != "" {
			if parentID, err = s.checkParent(ctx, tx, c, id, in.Type, in.Parent); err != nil {
				return err
			}
		}
		var mid, sid string
		if in.Milestone != "" {
			if mid, err = s.milestoneID(ctx, tx, in.Scope, in.Milestone, true); err != nil {
				return err
			}
		}
		if in.Sprint != "" {
			if sid, err = s.sprintID(ctx, tx, in.Scope, in.Sprint); err != nil {
				return err
			}
		}
		rank, err := s.nextRank(ctx, tx, in.Scope)
		if err != nil {
			return err
		}
		custom, _ := json.Marshal(in.Custom)
		if len(in.Custom) == 0 {
			custom = []byte("{}")
		}
		conf := 0
		if in.Confidential {
			conf = 1
		}
		difficulty := in.Difficulty
		if difficulty == "" {
			difficulty = eff.difficultyFor(in.Labels, in.Estimate)
		}
		if closed == 0 && eff.Closed(in.State) {
			closed = created
		}
		res := in.Resolution
		if res == "" && eff.Closed(in.State) {
			res = resolutionFor(eff, in.State)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO work_items(`+itemCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, in.Scope.Kind, in.Scope.ID, n, in.Type, in.Title, in.Body, in.State, res, in.Priority, toMilli(in.Estimate), parentID, mid, sid, rank,
			start, due, reporter, rkind, conf, difficulty, string(custom), in.SourceKey, 1, created, now, closed); err != nil {
			return err
		}
		seen, err := s.insertNewChildren(ctx, tx, id, in, labels)
		if err != nil {
			return err
		}
		if sid != "" {
			if err = s.sprintAdd(ctx, tx, sid, id, toMilli(in.Estimate)); err != nil {
				return err
			}
		}
		if err = s.event(ctx, tx, id, c.Actor, "created", created, map[string]any{"type": in.Type, "state": in.State, "title": in.Title}); err != nil {
			return err
		}
		if c.Kind == KindPerson || c.Kind == KindAgent {
			if _, err = tx.Exec(ctx, `INSERT INTO work_watchers(item_id, who) SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM work_watchers WHERE item_id = ? AND who = ?)`, id, c.Name, id, c.Name); err != nil {
				return err
			}
		}
		return s.notifyAssigned(ctx, tx, c, id, in.Title, seen)
	}()
	return id, err
}

func resolutionFor(eff *Effective, state string) string {
	switch eff.Category(state) {
	case CatDone:
		return "completed"
	case CatCancelled:
		return "declined"
	}
	return ""
}

// checkParent validates a parent and returns its id: it must be visible,
// not the item itself or one of its descendants, and of a type that may hold
// the item (an epic holds anything but epics; stories, issues and bugs hold
// tasks; tasks hold nothing).
func (s *Store) checkParent(ctx context.Context, tx DB, c Caller, id, typ, parentRef string) (string, error) {
	pid, err := s.idOf(ctx, tx, parentRef)
	if err != nil {
		return "", err
	}
	p, err := s.load(ctx, tx, pid)
	if err != nil {
		return "", err
	}
	if !c.canSee(p) {
		return "", notFound("no item " + parentRef)
	}
	if pid == id {
		return "", invalid("an item cannot be its own parent")
	}
	switch {
	case p.Type == TypeTask:
		return "", invalid("a task holds nothing")
	case typ == TypeEpic:
		return "", invalid("an epic has no parent")
	case p.Type != TypeEpic && typ != TypeTask:
		return "", invalid("only an epic holds stories, issues and bugs; only tasks go under a story")
	}
	if p.Scope != (Scope{}) && !c.canWrite(p.Scope) && p.Scope.Kind != ScopeProject {
		// Any level above a project is readable by those who read it; a
		// parent at another scope needs only to be readable.
		_ = p
	}
	// A parent that is a descendant of the item would make a cycle.
	for cur, depth := p.ParentID, 0; cur != "" && depth < 10; depth++ {
		if cur == id {
			return "", invalid("that would make the item its own ancestor")
		}
		var next string
		if err := tx.QueryRow(ctx, `SELECT parent_id FROM work_items WHERE id = ?`, cur).Scan(&next); err != nil {
			break
		}
		cur = next
	}
	return pid, nil
}

// hydrateChunk loads the related rows of up to a few hundred items.
func (s *Store) hydrateChunk(ctx context.Context, db DB, byID map[string]*Item, chunk []string, full bool) error {
	in, args := inList(chunk)
	labels, err := queryAll(ctx, db, func(r scanner) ([2]string, error) {
		var x [2]string
		err := r.Scan(&x[0], &x[1])
		return x, err
	}, `SELECT item_id, label FROM work_labels WHERE item_id IN (`+in+`) ORDER BY label`, args...)
	if err != nil {
		return err
	}
	for _, x := range labels {
		byID[x[0]].Labels = append(byID[x[0]].Labels, x[1])
	}
	type assignee struct {
		id string
		a  Assignee
	}
	assignees, err := queryAll(ctx, db, func(r scanner) (assignee, error) {
		var x assignee
		err := r.Scan(&x.id, &x.a.Name, &x.a.Kind)
		return x, err
	}, `SELECT item_id, who, kind FROM work_assignees WHERE item_id IN (`+in+`) ORDER BY who`, args...)
	if err != nil {
		return err
	}
	for _, x := range assignees {
		byID[x.id].Assignees = append(byID[x.id].Assignees, x.a)
	}
	if err := s.hydrateBlockers(ctx, db, byID, in, args); err != nil {
		return err
	}
	if err := s.hydrateClaims(ctx, db, byID, in, args); err != nil {
		return err
	}
	if !full {
		return nil
	}
	return s.hydrateFull(ctx, db, byID, in, args)
}

// hydrateBlockers marks the items that an open item blocks.
func (s *Store) hydrateBlockers(ctx context.Context, db DB, byID map[string]*Item, in string, args []any) error {
	type blk struct {
		to, state string
		sc        Scope
		n         int
	}
	blks, err := queryAll(ctx, db, func(r scanner) (blk, error) {
		var b blk
		err := r.Scan(&b.to, &b.state, &b.sc.Kind, &b.sc.ID, &b.n)
		return b, err
	}, `SELECT l.to_id, b.state, b.scope_kind, b.scope_id, b.number FROM work_links l JOIN work_items b ON b.id = l.from_id
			WHERE l.kind = 'blocks' AND l.to_id IN (`+in+`)`, args...)
	if err != nil {
		return err
	}
	for _, b := range blks {
		eff, err := s.settingsCachedIn(ctx, db, b.sc)
		if err != nil {
			return err
		}
		if eff.Closed(b.state) {
			continue
		}
		p, err := s.prefixIn(ctx, db, b.sc)
		if err != nil {
			return err
		}
		byID[b.to].Blocked = true
		byID[b.to].BlockedBy = append(byID[b.to].BlockedBy, fmt.Sprintf("%s-%d", p, b.n))
	}
	return nil
}

// hydrateClaims loads the claim of each item.
func (s *Store) hydrateClaims(ctx context.Context, db DB, byID map[string]*Item, in string, args []any) error {
	now := ms(s.now())
	type claimRow struct {
		id string
		cl Claim
	}
	rows, err := queryAll(ctx, db, func(r scanner) (claimRow, error) {
		var x claimRow
		var a, h, l int64
		if err := r.Scan(&x.id, &x.cl.Agent, &a, &h, &l, &x.cl.Note); err != nil {
			return x, err
		}
		x.cl.ClaimedAt, x.cl.Heartbeat, x.cl.LeaseUntil, x.cl.Expired = iso(a), iso(h), iso(l), l < now
		return x, nil
	}, `SELECT item_id, agent, claimed_at, heartbeat_at, lease_until, note FROM work_claims WHERE item_id IN (`+in+`)`, args...)
	if err != nil {
		return err
	}
	for i := range rows {
		byID[rows[i].id].Claim = &rows[i].cl
	}
	return nil
}

// hydrateFull loads the criteria and watchers of each item.
func (s *Store) hydrateFull(ctx context.Context, db DB, byID map[string]*Item, in string, args []any) error {
	type crit struct {
		id string
		c  Criterion
	}
	crits, err := queryAll(ctx, db, func(r scanner) (crit, error) {
		var x crit
		var d int
		if err := r.Scan(&x.id, &x.c.ID, &x.c.Text, &d); err != nil {
			return x, err
		}
		x.c.Done = d != 0
		return x, nil
	}, `SELECT item_id, id, text, done FROM work_criteria WHERE item_id IN (`+in+`) ORDER BY item_id, pos`, args...)
	if err != nil {
		return err
	}
	for _, x := range crits {
		byID[x.id].Criteria = append(byID[x.id].Criteria, x.c)
	}
	watchers, err := queryAll(ctx, db, func(r scanner) ([2]string, error) {
		var x [2]string
		err := r.Scan(&x[0], &x[1])
		return x, err
	}, `SELECT item_id, who FROM work_watchers WHERE item_id IN (`+in+`) ORDER BY who`, args...)
	if err != nil {
		return err
	}
	for _, x := range watchers {
		byID[x[0]].Watchers = append(byID[x[0]].Watchers, x[1])
	}
	return nil
}
