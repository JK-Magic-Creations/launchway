package work

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// GroupEdit is what a person changed when accepting a group.
type GroupEdit struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Priority string `json:"priority"`
}

func (g *group) occurrences() []string {
	var out []string
	for _, p := range g.members {
		if !p.resolved {
			out = append(out, p.title)
		}
	}
	return out
}

func (g *group) labels(source string) []string {
	l := []string{"machine-proposed", "source:" + source}
	for _, p := range g.members {
		for _, x := range p.labels {
			if !slices.Contains(l, strings.ToLower(x)) {
				l = append(l, strings.ToLower(x))
			}
		}
	}
	return l
}

func (g *group) body(b Batch, edit GroupEdit) string {
	if edit.Body != "" {
		return edit.Body
	}
	var w strings.Builder
	first := g.members[0]
	if first.body != "" {
		w.WriteString(first.body + "\n\n")
	}
	fmt.Fprintf(&w, "Proposed by the **%s** analysis. ", b.Source)
	if b.ReportURL != "" {
		fmt.Fprintf(&w, "Full report: %s\n\n", b.ReportURL)
	}
	if len(g.members) > 1 {
		w.WriteString("Each occurrence is a line of the checklist below; it is ticked off when a later run no longer finds it.\n\n")
	}
	for _, p := range g.members {
		for _, e := range p.evidence {
			fmt.Fprintf(&w, "- Evidence: %s\n", e)
		}
	}
	return strings.TrimSpace(w.String())
}

// createProposed makes an item for a proposal group, marked as machine-proposed.
func (s *Store) createProposed(ctx context.Context, tx DB, sc Scope, eff *Effective, b Batch, key string, in NewItem) (*Item, error) {
	in.SourceKey, in.Reporter = key, "source:"+b.Source
	in.Labels = append(in.Labels, "machine-proposed", "source:"+b.Source)
	p, err := s.prepareNew(ctx, System("analysis"), in)
	if err != nil {
		return nil, err
	}
	id, err := s.insertNewIn(ctx, tx, System("analysis"), p)
	if err != nil {
		return nil, err
	}
	if err := s.event(ctx, tx, id, Actor{Name: "launchway", Kind: KindSystem, Via: "analysis"}, "proposed", 0, map[string]any{"source": b.Source, "run": b.RunID, "report": b.ReportURL}); err != nil {
		return nil, err
	}
	return s.load(ctx, tx, id)
}

// openGroup files a group as one item and links its proposals to it.
func (s *Store) openGroup(ctx context.Context, tx DB, sc Scope, eff *Effective, b Batch, g *group, edit GroupEdit, res *ProposeResult) error {
	first := g.members[0]
	title := edit.Title
	if title == "" {
		title = g.title()
	}
	priority := first.priority
	if edit.Priority != "" {
		priority = edit.Priority
	}
	typ := first.typ
	if !eff.TypeAllowed(typ) {
		typ = TypeIssue
	}
	it, err := s.createProposed(ctx, tx, sc, eff, b, fmt.Sprintf("proposal:%s:%s", b.Source, g.key), NewItem{Scope: sc, Type: typ, Title: truncate(title, MaxTitle), Body: g.body(b, edit),
		Priority: priority, Labels: g.labels(b.Source), Criteria: g.occurrences()})
	if err != nil {
		return err
	}
	res.Opened++
	g.itemID = it.ID
	for _, p := range g.members {
		if _, err := tx.Exec(ctx, `UPDATE work_proposals SET state = 'accepted', item_id = ? WHERE id = ?`, it.ID, p.id); err != nil {
			return err
		}
	}
	return nil
}

// syncGroup updates a group's item to match the latest run: occurrences that
// are new are added, fixed ones ticked, and the item closes when none is open
// (and opens again if one comes back).
func (s *Store) syncGroup(ctx context.Context, tx DB, sc Scope, eff *Effective, b Batch, g *group, res *ProposeResult) error {
	it, err := s.load(ctx, tx, g.itemID)
	if err != nil {
		return nil // the item is gone: nothing to keep in line
	}
	have := map[string]Criterion{}
	for _, c := range it.Criteria {
		have[c.Text] = c
	}
	var crit []CriterionIn
	changed := false
	for _, p := range g.members {
		c, ok := have[p.title]
		if !ok {
			changed = true
		} else if c.Done != p.resolved {
			changed = true
		}
		crit = append(crit, CriterionIn{ID: c.ID, Text: p.title, Done: p.resolved})
		delete(have, p.title)
	}
	for _, c := range have { // an occurrence the proposals no longer hold stays as it was
		crit = append(crit, CriterionIn(c))
	}
	if changed {
		if err := s.setCriteria(ctx, tx, it.ID, crit, map[string]any{}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET version = version + 1, updated_at = ? WHERE id = ?`, ms(s.now()), it.ID); err != nil {
			return err
		}
		res.Synced++
	}
	open := g.unresolved()
	closed := eff.Closed(it.State)
	switch {
	case open == 0 && !closed:
		return s.closeByRule(ctx, tx, eff, it, b, fmt.Sprintf("All %d occurrences are fixed, so the %s analysis closed this item.", len(g.members), b.Source), res)
	case open > 0 && closed && eff.Category(it.State) == CatDone:
		return s.reopenByRule(ctx, tx, eff, it, b, fmt.Sprintf("The %s analysis found %d occurrence(s) again, so this item is open again.", b.Source, open), res)
	}
	return nil
}

func (s *Store) closeByRule(ctx context.Context, tx DB, eff *Effective, it *Item, b Batch, why string, res *ProposeResult) error {
	done := eff.FirstOf(CatDone)
	if err := s.ruleMove(ctx, tx, it, done, closedNow(s), why, "closed_by_analysis"); err != nil {
		return err
	}
	res.Closed++
	return nil
}

func (s *Store) reopenByRule(ctx context.Context, tx DB, eff *Effective, it *Item, b Batch, why string, res *ProposeResult) error {
	if err := s.ruleMove(ctx, tx, it, eff.FirstOf(CatTodo), 0, why, "reopened_by_analysis"); err != nil {
		return err
	}
	res.Reopened++
	return nil
}

func closedNow(s *Store) int64 { return ms(s.now()) }

// ruleMove moves an item by the server's own rule and says why in a comment.
func (s *Store) ruleMove(ctx context.Context, tx DB, it *Item, to string, closedAt int64, why, kind string) error {
	res := ""
	if closedAt > 0 {
		res = "completed"
	}
	now := ms(s.now())
	if _, err := tx.Exec(ctx, `UPDATE work_items SET state = ?, resolution = ?, closed_at = ?, version = version + 1, updated_at = ? WHERE id = ?`, to, res, closedAt, now, it.ID); err != nil {
		return err
	}
	actor := Actor{Name: "launchway", Kind: KindSystem, Via: "analysis"}
	if err := s.event(ctx, tx, it.ID, actor, "state", 0, map[string]any{"from": it.State, "to": to, "by": kind}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_comments(id, item_id, author, author_kind, body, created_at, edited_at) VALUES(?,?,?,?,?,?,0)`, newID("wcm"), it.ID, "launchway", KindSystem, why, now)
	return err
}
