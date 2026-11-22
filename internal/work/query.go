package work

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// The query language is a line of terms separated by spaces:
//
//	state:in_progress  type:bug  label:ui  -label:wontfix  assignee:me
//	is:open  is:closed  is:blocked  is:unassigned  is:claimed  is:confidential
//	repo:acme/shop  decision:accepted
//	milestone:"Q1"  sprint:"Sprint 4"  priority:high  parent:PAY-3  reporter:ada
//	updated:>2026-01-01  updated:<7d  created:>30d  due:<today
//	"words in quotes"  any other word  (searched in titles and bodies)
//
// A term with a leading minus is negated. Dates are 2026-01-31, or a number
// of days (7d) or weeks (2w) ago, or today.

type term struct {
	neg      bool
	key, val string
	op       string // "", ">" or "<" for dates
}

// Query is a parsed search.
type Query struct {
	Raw   string
	terms []term
}

// ParseQuery reads a query. me is the name that "me" stands for.
func ParseQuery(raw string) (*Query, error) {
	q := &Query{Raw: raw}
	for _, tok := range tokenize(raw) {
		t := term{}
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			t.neg, tok = true, tok[1:]
		}
		key, val, ok := strings.Cut(tok, ":")
		if !ok || !slices.Contains(queryKeys, strings.ToLower(key)) {
			t.key, t.val = "text", strings.Trim(tok, `"`)
			q.terms = append(q.terms, t)
			continue
		}
		t.key, t.val = strings.ToLower(key), strings.Trim(val, `"`)
		if t.val == "" {
			return nil, invalid("%s: needs a value", key)
		}
		if slices.Contains([]string{"updated", "created", "due", "closed"}, t.key) {
			if strings.HasPrefix(t.val, ">") || strings.HasPrefix(t.val, "<") {
				t.op, t.val = t.val[:1], t.val[1:]
			}
			if _, err := parseWhen(t.val, time.Now()); err != nil {
				return nil, err
			}
		}
		if t.key == "is" && !slices.Contains([]string{"open", "closed", "blocked", "unassigned", "claimed", "confidential", "overdue", "epic"}, t.val) {
			return nil, invalid("is: takes open, closed, blocked, unassigned, claimed, confidential, overdue or epic")
		}
		q.terms = append(q.terms, t)
	}
	if len(q.terms) > 30 {
		return nil, invalid("a query has at most 30 terms")
	}
	return q, nil
}

var queryKeys = []string{"state", "type", "label", "assignee", "is", "milestone", "sprint", "priority", "parent", "epic", "reporter", "updated", "created", "due", "closed", "ref", "agent", "key", "repo", "decision"}

func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			cur.WriteRune(r)
		case unicode.IsSpace(r) && !inQ:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// parseWhen reads a date, "today" or an age such as 7d, as a time.
func parseWhen(v string, now time.Time) (int64, error) {
	if v == "today" {
		y, m, d := now.UTC().Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli(), nil
	}
	if n := len(v); n > 1 && (v[n-1] == 'd' || v[n-1] == 'w') {
		if k, err := strconv.Atoi(v[:n-1]); err == nil && k >= 0 {
			days := k
			if v[n-1] == 'w' {
				days = k * 7
			}
			return now.AddDate(0, 0, -days).UnixMilli(), nil
		}
	}
	return ParseTime(v)
}

// ListOptions narrows and orders a list.
type ListOptions struct {
	Scope  *Scope  // one scope, or nil for all the caller may read
	Scopes []Scope // several scopes (a board of a unit, for instance)
	Query  string
	Sort   string // rank (the default), updated, created, priority, due, number
	Desc   bool
	Limit  int
	Offset int
	Parent string // only the children of this item (a ref)
	// Repository keeps the items of a repository: in its own scope, or tied to its code.
	Repository string
}

// Page is one page of a list.
type Page struct {
	Items  []*Item `json:"items"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// closedStates are the state keys, across all workflows, that end work.
func (s *Store) closedStates(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	if s.closed != nil {
		out := make([]string, 0, len(s.closed))
		for k := range s.closed {
			out = append(out, k)
		}
		s.mu.Unlock()
		slices.Sort(out)
		return out, nil
	}
	s.mu.Unlock()
	set := map[string]bool{"done": true, "wont_do": true}
	scopes, err := queryAll(ctx, s.db, scanScope, `SELECT scope_kind, scope_id FROM work_scopes WHERE settings <> '{}'`)
	if err != nil {
		return nil, err
	}
	for _, sc := range scopes {
		eff, err := s.Settings(ctx, sc)
		if err != nil {
			return nil, err
		}
		for _, st := range eff.States {
			if st.Category == CatDone || st.Category == CatCancelled {
				set[st.Key] = true
			}
		}
	}
	s.mu.Lock()
	s.closed = set
	s.mu.Unlock()
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out, nil
}

// scopeFilter builds the part of a WHERE that holds the caller to what they
// may read, and hides confidential items from those who may not see them.
func (s *Store) visibility(ctx context.Context, c Caller, only []Scope) (string, []any, error) {
	scopes, err := queryAll(ctx, s.db, scanScope, `SELECT scope_kind, scope_id FROM work_scopes`)
	if err != nil {
		return "", nil, err
	}
	var readable, maintained []string
	for _, sc := range scopes {
		if len(only) > 0 && !slices.Contains(only, sc) {
			continue
		}
		if c.canRead(sc) {
			key := sc.Kind + ":" + sc.ID
			readable = append(readable, key)
			if c.isMaintainer(sc) || c.Kind == KindSystem {
				maintained = append(maintained, key)
			}
		}
	}
	if len(readable) == 0 {
		return "1 = 0", nil, nil
	}
	in, args := inList(readable)
	w := `(i.scope_kind || ':' || i.scope_id) IN (` + in + `)`
	conf := `i.confidential = 0`
	if len(maintained) > 0 {
		mIn, margs := inList(maintained)
		conf += ` OR (i.scope_kind || ':' || i.scope_id) IN (` + mIn + `)`
		args = append(args, margs...)
	}
	conf += ` OR i.reporter = ? OR EXISTS (SELECT 1 FROM work_assignees ca WHERE ca.item_id = i.id AND ca.who = ?)`
	args = append(args, c.Name, c.Name)
	return w + ` AND (` + conf + `)`, args, nil
}

func likePattern(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(s)) + "%"
}

// termEnv is what the condition builders of a query term share.
type termEnv struct {
	ctx   context.Context
	s     *Store
	c     Caller
	cin   string
	cargs []any
	now   time.Time
}

// termBuilder turns one query term into an SQL condition and its arguments.
type termBuilder func(e *termEnv, t term) (string, []any)

const noAssignee = `NOT EXISTS (SELECT 1 FROM work_assignees x WHERE x.item_id = i.id)`
const hasAssignee = `EXISTS (SELECT 1 FROM work_assignees x WHERE x.item_id = i.id AND x.who = ?)`

func (e *termEnv) openClosed(val string) (string, []any, bool) {
	switch val {
	case "open":
		return `i.state NOT IN (` + e.cin + `)`, e.cargs, true
	case "closed":
		return `i.state IN (` + e.cin + `)`, e.cargs, true
	}
	return "", nil, false
}

func eq(col string) termBuilder {
	return func(_ *termEnv, t term) (string, []any) { return col + ` = ?`, []any{t.val} }
}

var termBuilders = map[string]termBuilder{
	"text": func(_ *termEnv, t term) (string, []any) {
		p := likePattern(t.val)
		return `(LOWER(i.title) LIKE ? ESCAPE '\' OR LOWER(i.body) LIKE ? ESCAPE '\')`, []any{p, p}
	},
	"state": func(e *termEnv, t term) (string, []any) {
		if cond, a, ok := e.openClosed(t.val); ok {
			return cond, a
		}
		return `i.state = ?`, []any{t.val}
	},
	"type":     eq("i.type"),
	"priority": eq("i.priority"),
	"label": func(_ *termEnv, t term) (string, []any) {
		return `EXISTS (SELECT 1 FROM work_labels l WHERE l.item_id = i.id AND l.label = ?)`, []any{strings.ToLower(t.val)}
	},
	"assignee": termAssignee,
	"agent":    termAssignee,
	"reporter": func(e *termEnv, t term) (string, []any) {
		v := t.val
		if v == "me" {
			v = e.c.Name
		}
		return `i.reporter = ?`, []any{v}
	},
	"milestone": func(_ *termEnv, t term) (string, []any) {
		return `i.milestone_id IN (SELECT id FROM work_milestones WHERE title = ?)`, []any{t.val}
	},
	"sprint": func(_ *termEnv, t term) (string, []any) {
		if t.val == "none" {
			return `i.sprint_id = ''`, nil
		}
		return `i.sprint_id IN (SELECT id FROM work_sprints WHERE name = ?)`, []any{t.val}
	},
	"parent": termParent,
	"epic":   termParent,
	"key":    eq("i.source_key"),
	"ref": func(_ *termEnv, t term) (string, []any) {
		prefix, n, ok := ParseRef(t.val)
		if !ok {
			return `1 = 0`, nil
		}
		return `i.number = ? AND EXISTS (SELECT 1 FROM work_scopes sc WHERE sc.scope_kind = i.scope_kind AND sc.scope_id = i.scope_id AND sc.prefix = ?)`, []any{n, prefix}
	},
	"decision": func(_ *termEnv, t term) (string, []any) {
		if !slices.Contains(DecisionStatuses, t.val) {
			return `1 = 0`, nil
		}
		return store.WorkIsDecisionSQL, []any{t.val}
	},
	"repo": func(_ *termEnv, t term) (string, []any) {
		return store.WorkInRepositorySQL, []any{t.val, t.val}
	},
	"updated": dateTerm("i.updated_at"),
	"created": dateTerm("i.created_at"),
	"due":     dateTerm("i.due_at"),
	"closed":  dateTerm("i.closed_at"),
	"is":      termIs,
}

func termAssignee(e *termEnv, t term) (string, []any) {
	switch t.val {
	case "none":
		return noAssignee, nil
	case "me":
		return hasAssignee, []any{e.c.Name}
	}
	return hasAssignee, []any{strings.TrimPrefix(t.val, "@")}
}

func termParent(e *termEnv, t term) (string, []any) {
	id, err := e.s.idOf(e.ctx, e.s.db, t.val)
	if err != nil {
		return `1 = 0`, nil
	}
	return `(i.parent_id = ? OR i.parent_id IN (SELECT id FROM work_items WHERE parent_id = ?))`, []any{id, id}
}

func dateTerm(col string) termBuilder {
	return func(e *termEnv, t term) (string, []any) {
		at, _ := parseWhen(t.val, e.now)
		op := t.op
		if op == "" {
			// A date alone is that day.
			return fmt.Sprintf(`(%s >= ? AND %s < ?)`, col, col), []any{at, at + 24*3600*1000}
		}
		// "<7d" is more recent than a week ago for ages, and "<2026-01-01"
		// before that date for dates: both are "before the instant" if a
		// date, "after the instant" if an age.
		age := len(t.val) > 1 && (strings.HasSuffix(t.val, "d") || strings.HasSuffix(t.val, "w")) && t.val != "today"
		if age {
			if op == "<" {
				op = ">"
			} else {
				op = "<"
			}
		}
		return fmt.Sprintf(`(%s <> 0 AND %s %s ?)`, col, col, op), []any{at}
	}
}

func termIs(e *termEnv, t term) (string, []any) {
	if cond, a, ok := e.openClosed(t.val); ok {
		return cond, a
	}
	switch t.val {
	case "unassigned":
		return noAssignee, nil
	case "claimed":
		return `EXISTS (SELECT 1 FROM work_claims k WHERE k.item_id = i.id AND k.lease_until >= ?)`, []any{ms(e.s.now())}
	case "confidential":
		return `i.confidential = 1`, nil
	case "epic":
		return `i.type = 'epic'`, nil
	case "overdue":
		return `(i.due_at <> 0 AND i.due_at < ? AND i.state NOT IN (` + e.cin + `))`, append([]any{ms(e.s.now())}, e.cargs...)
	case "blocked":
		return `EXISTS (SELECT 1 FROM work_links l JOIN work_items b ON b.id = l.from_id WHERE l.kind = 'blocks' AND l.to_id = i.id AND b.state NOT IN (` + e.cin + `))`, e.cargs
	}
	return "", nil
}

// where builds the conditions of a query.
func (s *Store) where(ctx context.Context, c Caller, q *Query) (string, []any, error) {
	var conds []string
	var args []any
	closed, err := s.closedStates(ctx)
	if err != nil {
		return "", nil, err
	}
	cin, cargs := inList(closed)
	env := &termEnv{ctx: ctx, s: s, c: c, cin: cin, cargs: cargs, now: time.Now()}
	for _, t := range q.terms {
		build, ok := termBuilders[t.key]
		if !ok {
			continue
		}
		cond, a := build(env, t)
		if cond == "" {
			continue
		}
		if t.neg {
			cond = `NOT (` + cond + `)`
		}
		conds = append(conds, cond)
		args = append(args, a...)
	}
	return strings.Join(conds, " AND "), args, nil
}

var sorts = map[string]string{
	"rank": "i.rank", "updated": "i.updated_at", "created": "i.created_at", "number": "i.number", "due": "(CASE WHEN i.due_at = 0 THEN 1 ELSE 0 END), i.due_at",
	"priority": "(CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END)",
}

// List returns the items the caller may see that match the options.
func (s *Store) List(ctx context.Context, c Caller, o ListOptions) (*Page, error) {
	q, err := ParseQuery(o.Query)
	if err != nil {
		return nil, err
	}
	only := o.Scopes
	if o.Scope != nil {
		only = append(only, *o.Scope)
	}
	vis, vargs, err := s.visibility(ctx, c, only)
	if err != nil {
		return nil, err
	}
	conds := []string{vis}
	args := vargs
	if w, a, err := s.where(ctx, c, q); err != nil {
		return nil, err
	} else if w != "" {
		conds, args = append(conds, w), append(args, a...)
	}
	if o.Parent != "" {
		id, err := s.idOf(ctx, s.db, o.Parent)
		if err != nil {
			return nil, err
		}
		conds, args = append(conds, `i.parent_id = ?`), append(args, id)
	}
	if o.Repository != "" {
		conds, args = append(conds, store.WorkInRepositorySQL), append(args, o.Repository, o.Repository)
	}
	where := strings.Join(conds, " AND ")
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_items i WHERE `+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	if o.Limit <= 0 || o.Limit > 500 {
		o.Limit = 50
	}
	order := sorts[o.Sort]
	if order == "" {
		order = sorts["rank"]
	}
	dir := "ASC"
	if o.Desc {
		dir = "DESC"
	}
	cols := itemColsI
	qargs := append(slices.Clone(args), o.Limit, o.Offset)
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM work_items i WHERE `+where+` ORDER BY `+order+` `+dir+`, i.number `+dir+` LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := s.hydrate(ctx, s.db, items, false); err != nil {
		return nil, err
	}
	if items == nil {
		items = []*Item{}
	}
	if err := s.fillItemReactions(ctx, c, items); err != nil {
		return nil, err
	}
	return &Page{Items: items, Total: total, Limit: o.Limit, Offset: o.Offset}, nil
}

// View is a saved query.
type View struct {
	ID     string `json:"id"`
	Scope  Scope  `json:"scope"`
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	Query  string `json:"query"`
	Shared bool   `json:"shared"`
}

// SaveView stores a query under a name for the caller (or for everyone who
// reads the scope, when shared and the caller may write it).
func (s *Store) SaveView(ctx context.Context, c Caller, sc Scope, name, query string, shared bool) (*View, error) {
	if !c.canRead(sc) {
		return nil, notFound("no such project")
	}
	if _, err := ParseQuery(query); err != nil {
		return nil, err
	}
	name = cleanTitle(name)
	if name == "" || len(name) > 80 {
		return nil, invalid("a view has a name of 1 to 80 characters")
	}
	if shared && !c.canWrite(sc) {
		return nil, forbidden("only someone who may change work here shares a view")
	}
	owner := c.Name
	if shared {
		owner = ""
	}
	v := &View{ID: newID("wv"), Scope: sc, Owner: owner, Name: name, Query: query, Shared: shared}
	err := s.tx(ctx, func(tx DB) error {
		if err := s.ensureScope(ctx, tx, sc); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM work_views WHERE scope_kind = ? AND scope_id = ? AND owner = ? AND name = ?`, sc.Kind, sc.ID, owner, name); err != nil {
			return err
		}
		sh := 0
		if shared {
			sh = 1
		}
		_, err := tx.Exec(ctx, `INSERT INTO work_views(id, scope_kind, scope_id, owner, name, query, shared, created_at) VALUES(?,?,?,?,?,?,?,?)`, v.ID, sc.Kind, sc.ID, owner, name, query, sh, ms(s.now()))
		return err
	})
	return v, err
}

// Views lists the views of a scope that the caller may use: their own and the shared ones.
func (s *Store) Views(ctx context.Context, c Caller, sc Scope) ([]View, error) {
	if !c.canRead(sc) {
		return nil, notFound("no such project")
	}
	rows, err := s.db.Query(ctx, `SELECT id, owner, name, query, shared FROM work_views WHERE scope_kind = ? AND scope_id = ? AND (shared = 1 OR owner = ?) ORDER BY name`, sc.Kind, sc.ID, c.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []View{}
	for rows.Next() {
		v := View{Scope: sc}
		var sh int
		if err := rows.Scan(&v.ID, &v.Owner, &v.Name, &v.Query, &sh); err != nil {
			return nil, err
		}
		v.Shared = sh == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteView removes a view the caller owns (or a shared one they may write).
func (s *Store) DeleteView(ctx context.Context, c Caller, id string) error {
	var sc Scope
	var owner string
	var shared int
	err := s.db.QueryRow(ctx, `SELECT scope_kind, scope_id, owner, shared FROM work_views WHERE id = ?`, id).Scan(&sc.Kind, &sc.ID, &owner, &shared)
	if isNoRows(err) || (err == nil && !c.canRead(sc)) {
		return notFound("no view " + id)
	}
	if err != nil {
		return err
	}
	if (shared == 0 && owner != c.Name) || (shared == 1 && !c.canWrite(sc)) {
		return forbidden("this view is not yours to remove")
	}
	_, err = s.db.Exec(ctx, `DELETE FROM work_views WHERE id = ?`, id)
	return err
}
