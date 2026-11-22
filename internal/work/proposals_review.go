package work

import (
	"context"
	"sort"
	"strings"
)

// ProposalGroup is what a person reviews: the occurrences of one group.
type ProposalGroup struct {
	Source     string     `json:"source"`
	Group      string     `json:"group"`
	Title      string     `json:"title"`
	State      string     `json:"state"` // pending, accepted, folded, dismissed or fixed
	Severity   int        `json:"severity"`
	Count      int        `json:"count"`
	Unresolved int        `json:"unresolved"`
	Item       string     `json:"item,omitempty"`
	Items      []Proposal `json:"occurrences"`
	ReportURL  string     `json:"report_url,omitempty"`
}

func (s *Store) allGroups(ctx context.Context, tx DB, sc Scope, includeDismissed bool) ([]*group, map[*group]string, error) {
	q := `SELECT ` + propCols + `, report_url FROM work_proposals WHERE scope_kind = ? AND scope_id = ?`
	if !includeDismissed {
		q += ` AND state <> 'dismissed'`
	}
	type withReport struct {
		p propRow
		r string
	}
	rows, err := queryAll(ctx, tx, func(r scanner) (withReport, error) {
		var w withReport
		var labels, evidence string
		var resolved int64
		err := r.Scan(&w.p.id, &w.p.source, &w.p.dedupe, &w.p.group, &w.p.groupTitle, &w.p.title, &w.p.body, &w.p.typ, &w.p.priority, &w.p.state, &w.p.itemID, &labels, &evidence, &w.p.severity, &resolved, &w.r)
		w.p.resolved = resolved > 0
		return w, err
	}, q+` ORDER BY source, group_key, first_seen, id`, sc.Kind, sc.ID)
	if err != nil {
		return nil, nil, err
	}
	idx := map[string]*group{}
	var out []*group
	reports := map[*group]string{}
	for _, w := range rows {
		k := w.p.source + "\x00" + w.p.group
		g := idx[k]
		if g == nil {
			g = &group{key: w.p.group}
			idx[k] = g
			out = append(out, g)
		}
		g.members = append(g.members, w.p)
		if w.p.itemID != "" {
			g.itemID = w.p.itemID
		}
		reports[g] = w.r
	}
	return out, reports, nil
}

func (g *group) state() string {
	allDismissed, anyAccepted, anyOpen := true, false, false
	for _, p := range g.members {
		if p.state != "dismissed" {
			allDismissed = false
		}
		if p.state == "accepted" {
			anyAccepted = true
		}
		if !p.resolved {
			anyOpen = true
		}
	}
	switch {
	case allDismissed:
		return "dismissed"
	case anyAccepted:
		return "accepted"
	case !anyOpen:
		return "fixed"
	}
	return "pending"
}

// Proposals lists the groups of proposals of a scope for review, most severe first.
func (s *Store) Proposals(ctx context.Context, c Caller, sc Scope, state string) ([]ProposalGroup, error) {
	if !c.canRead(sc) {
		return nil, notFound("no such project")
	}
	gs, reports, err := s.allGroups(ctx, s.db, sc, true)
	if err != nil {
		return nil, err
	}
	out := []ProposalGroup{}
	for _, g := range gs {
		st := g.state()
		if state != "" && state != st {
			continue
		}
		pg := ProposalGroup{Source: g.members[0].source, Group: g.key, Title: g.title(), State: st, Severity: g.maxSeverity(), Count: len(g.members), Unresolved: g.unresolved(), ReportURL: reports[g]}
		if g.itemID != "" {
			pg.Item = s.refOf(ctx, s.db, g.itemID)
		}
		for _, p := range g.members {
			pg.Items = append(pg.Items, Proposal{DedupeKey: p.dedupe, GroupKey: p.group, Title: p.title, Body: p.body, Type: p.typ, Labels: p.labels, Priority: p.priority, Severity: p.severity, Evidence: p.evidence})
		}
		out = append(out, pg)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity > out[j].Severity })
	return out, nil
}

// reviewer checks that a person is reviewing.
func (s *Store) reviewer(c Caller, sc Scope) error {
	if c.Agent != nil || c.Kind == KindAgent || !c.Human() || !c.canWrite(sc) {
		return forbidden("a person who may change work in %s reviews proposals", sc)
	}
	return nil
}

// AcceptProposals files the named groups as items, as edited. A group of the
// folded summary can be accepted on its own.
func (s *Store) AcceptProposals(ctx context.Context, c Caller, sc Scope, source string, groups []string, edits map[string]GroupEdit) (*ProposeResult, error) {
	if err := s.reviewer(c, sc); err != nil {
		return nil, err
	}
	res := &ProposeResult{}
	eff, err := s.primeFor(ctx, sc)
	if err != nil {
		return nil, err
	}
	err = s.tx(ctx, func(tx DB) error {
		*res = ProposeResult{}
		gs, reports, err := s.allGroups(ctx, tx, sc, false)
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, k := range groups {
			want[k] = true
		}
		for _, g := range gs {
			src := g.members[0].source
			if (source != "" && src != source) || !want[g.key] || g.itemID != "" || g.unresolved() == 0 {
				continue
			}
			if err := s.openGroup(ctx, tx, sc, eff, Batch{Source: src, ReportURL: reports[g]}, g, edits[g.key], res); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

// DismissProposals dismisses groups: the same findings are not proposed again.
func (s *Store) DismissProposals(ctx context.Context, c Caller, sc Scope, source string, groups []string, reason string) (int, error) {
	if err := s.reviewer(c, sc); err != nil {
		return 0, err
	}
	if strings.TrimSpace(reason) == "" {
		return 0, invalid("say why the findings are dismissed: they are not proposed again")
	}
	n := 0
	err := s.tx(ctx, func(tx DB) error {
		n = 0
		for _, k := range groups {
			res, err := tx.Exec(ctx, `UPDATE work_proposals SET state = 'dismissed', dismissed_by = ?, dismissed_reason = ? WHERE scope_kind = ? AND scope_id = ? AND (source = ? OR ? = '') AND group_key = ? AND item_id = ''`,
				c.Name, cleanTitle(reason), sc.Kind, sc.ID, source, source, k)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		return nil
	})
	return n, err
}

// MergeProposals puts several groups into one, to be filed as one item.
func (s *Store) MergeProposals(ctx context.Context, c Caller, sc Scope, source string, groups []string, into string) error {
	if err := s.reviewer(c, sc); err != nil {
		return err
	}
	if into == "" {
		return invalid("say which group to merge them into")
	}
	return s.tx(ctx, func(tx DB) error {
		for _, k := range groups {
			if _, err := tx.Exec(ctx, `UPDATE work_proposals SET group_key = ? WHERE scope_kind = ? AND scope_id = ? AND source = ? AND group_key = ? AND item_id = ''`, into, sc.Kind, sc.ID, source, k); err != nil {
				return err
			}
		}
		return nil
	})
}
