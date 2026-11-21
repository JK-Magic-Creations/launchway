package work

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Analyzers (a security scan, a code-quality check, the repository health
// check) do not file items. They send PROPOSALS: a batch with a source name,
// and for each finding a stable dedupe key and a group key. The server groups
// proposals that share a group key into one item with a checklist of the
// occurrences, caps how many new items one source opens in one run (the rest
// fold into one "and N more" summary item), waits for a person to accept them
// unless the level lets that source file without a review, remembers what was
// dismissed, and on a re-run updates the same items: new occurrences are added,
// fixed ones are ticked off, and an item whose occurrences are all fixed closes.

// Proposal is one finding.
type Proposal struct {
	DedupeKey  string   `json:"dedupe_key"`  // stable across runs: the same finding has the same key
	GroupKey   string   `json:"group_key"`   // findings with the same group key become one item
	GroupTitle string   `json:"group_title"` // the item's title; {n} stands for the open occurrences
	Title      string   `json:"title"`       // this occurrence, a line of the checklist
	Body       string   `json:"body"`
	Type       string   `json:"type"`
	Labels     []string `json:"labels"`
	Priority   string   `json:"priority"`
	Severity   int      `json:"severity"` // 0 none, 1 low, 2 medium, 3 high, 4 critical
	Evidence   []string `json:"evidence"` // links to what shows it
}

// Batch is what an analyzer sends after one run.
type Batch struct {
	Source    string     `json:"source"`
	RunID     string     `json:"run_id"`
	ReportURL string     `json:"report_url"` // the full report
	Complete  bool       `json:"complete"`   // this is every finding of the source: what is missing is fixed
	Proposals []Proposal `json:"proposals"`
}

// ProposeResult says what a batch did.
type ProposeResult struct {
	Received  int `json:"received"`
	New       int `json:"new"`
	Updated   int `json:"updated"`
	Dismissed int `json:"dismissed"` // matched a dismissed finding: left out
	Fixed     int `json:"fixed"`     // no longer reported
	Waiting   int `json:"waiting"`   // groups waiting for a review
	Opened    int `json:"opened"`    // items opened
	Folded    int `json:"folded"`    // groups folded into the summary
	Synced    int `json:"synced"`    // items updated
	Closed    int `json:"closed"`
	Reopened  int `json:"reopened"`
}

const (
	maxBatch        = 5000
	defaultMaxItems = 10
)

type propRow struct {
	id, source, dedupe, group, title, body, typ, priority, state, itemID, groupTitle string
	labels, evidence                                                                 []string
	severity                                                                         int
	resolved                                                                         bool
}

const propCols = `id, source, dedupe_key, group_key, group_title, title, body, type, priority, state, item_id, labels, evidence, severity, resolved_at`

func scanProp(r scanner) (propRow, error) {
	var p propRow
	var labels, evidence string
	var resolved int64
	if err := r.Scan(&p.id, &p.source, &p.dedupe, &p.group, &p.groupTitle, &p.title, &p.body, &p.typ, &p.priority, &p.state, &p.itemID, &labels, &evidence, &p.severity, &resolved); err != nil {
		return p, err
	}
	_ = json.Unmarshal([]byte(labels), &p.labels)
	_ = json.Unmarshal([]byte(evidence), &p.evidence)
	p.resolved = resolved > 0
	return p, nil
}

func (b *Batch) check() error {
	b.Source = strings.ToLower(cleanTitle(b.Source))
	if b.Source == "" || len(b.Source) > 60 || strings.ContainsAny(b.Source, " :") {
		return invalid("a source is a short name such as repository-health")
	}
	if len(b.Proposals) > maxBatch {
		return invalid("a batch holds at most %d proposals", maxBatch)
	}
	seen := map[string]bool{}
	for i := range b.Proposals {
		p := &b.Proposals[i]
		p.Title, p.Body = cleanTitle(p.Title), cleanBody(p.Body)
		if p.DedupeKey == "" || len(p.DedupeKey) > 300 || p.Title == "" || len(p.Title) > MaxTitle || seen[p.DedupeKey] {
			return invalid("proposal %d needs a unique dedupe_key and a title", i+1)
		}
		seen[p.DedupeKey] = true
		if p.GroupKey == "" {
			p.GroupKey = "one:" + p.DedupeKey
		}
		if p.Type == "" {
			p.Type = TypeIssue
		}
		if p.Priority == "" {
			p.Priority = "normal"
		}
		if p.Severity < 0 || p.Severity > 4 || len(p.Evidence) > 20 || len(p.Labels) > 10 {
			return invalid("proposal %d: severity is 0 to 4, at most 20 evidence links and 10 labels", i+1)
		}
	}
	return nil
}

// Propose takes a batch from an analyzer.
func (s *Store) Propose(ctx context.Context, c Caller, sc Scope, b Batch) (*ProposeResult, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	if c.Agent != nil || c.Kind == KindAgent || !c.canWrite(sc) {
		return nil, forbidden("only a person or the server files proposals for %s", sc)
	}
	if err := b.check(); err != nil {
		return nil, err
	}
	res := &ProposeResult{Received: len(b.Proposals)}
	err := s.tx(ctx, func(tx DB) error {
		*res = ProposeResult{Received: len(b.Proposals)}
		if err := s.ensureScope(ctx, tx, sc); err != nil {
			return err
		}
		if err := s.upsertProposals(ctx, tx, sc, b, res); err != nil {
			return err
		}
		return s.reconcile(ctx, tx, sc, b, res)
	})
	return res, err
}

func (s *Store) upsertProposals(ctx context.Context, tx DB, sc Scope, b Batch, res *ProposeResult) error {
	now := ms(s.now())
	for _, p := range b.Proposals {
		labels, _ := json.Marshal(p.Labels)
		evidence, _ := json.Marshal(p.Evidence)
		var state string
		err := tx.QueryRow(ctx, `SELECT state FROM work_proposals WHERE scope_kind = ? AND scope_id = ? AND source = ? AND dedupe_key = ?`, sc.Kind, sc.ID, b.Source, p.DedupeKey).Scan(&state)
		switch {
		case isNoRows(err):
			res.New++
			_, err = tx.Exec(ctx, `INSERT INTO work_proposals(id, scope_kind, scope_id, source, dedupe_key, group_key, group_title, title, body, type, labels, priority, severity, evidence, state, run_id, report_url, first_seen, last_seen)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, newID("wp"), sc.Kind, sc.ID, b.Source, p.DedupeKey, p.GroupKey, p.GroupTitle, p.Title, p.Body, p.Type, string(labels), p.Priority, p.Severity, string(evidence), "pending", b.RunID, b.ReportURL, now, now)
		case err != nil:
			return err
		case state == "dismissed":
			res.Dismissed++
			_, err = tx.Exec(ctx, `UPDATE work_proposals SET last_seen = ? WHERE scope_kind = ? AND scope_id = ? AND source = ? AND dedupe_key = ?`, now, sc.Kind, sc.ID, b.Source, p.DedupeKey)
		default:
			res.Updated++
			// A finding that came back is open again.
			_, err = tx.Exec(ctx, `UPDATE work_proposals SET group_title = ?, title = ?, body = ?, labels = ?, priority = ?, severity = ?, evidence = ?, run_id = ?, report_url = ?, last_seen = ?, resolved_at = 0
				WHERE scope_kind = ? AND scope_id = ? AND source = ? AND dedupe_key = ?`, p.GroupTitle, p.Title, p.Body, string(labels), p.Priority, p.Severity, string(evidence), b.RunID, b.ReportURL, now, sc.Kind, sc.ID, b.Source, p.DedupeKey)
		}
		if err != nil {
			return err
		}
	}
	if b.Complete {
		keys := map[string]bool{}
		for _, p := range b.Proposals {
			keys[p.DedupeKey] = true
		}
		open, err := queryAll(ctx, tx, scanString, `SELECT dedupe_key FROM work_proposals WHERE scope_kind = ? AND scope_id = ? AND source = ? AND state <> 'dismissed' AND resolved_at = 0`, sc.Kind, sc.ID, b.Source)
		if err != nil {
			return err
		}
		for _, k := range open {
			if keys[k] {
				continue
			}
			res.Fixed++
			if _, err := tx.Exec(ctx, `UPDATE work_proposals SET resolved_at = ? WHERE scope_kind = ? AND scope_id = ? AND source = ? AND dedupe_key = ?`, now, sc.Kind, sc.ID, b.Source, k); err != nil {
				return err
			}
		}
	}
	return nil
}

// group is the proposals that share a group key.
type group struct {
	key     string
	members []propRow
	itemID  string
}

func (g *group) maxSeverity() int {
	m := 0
	for _, p := range g.members {
		m = max(m, p.severity)
	}
	return m
}

func (g *group) unresolved() int {
	n := 0
	for _, p := range g.members {
		if !p.resolved {
			n++
		}
	}
	return n
}

func (s *Store) groupsOf(ctx context.Context, tx DB, sc Scope, source string) ([]*group, error) {
	rows, err := queryAll(ctx, tx, scanProp, `SELECT `+propCols+` FROM work_proposals WHERE scope_kind = ? AND scope_id = ? AND source = ? AND state <> 'dismissed' ORDER BY group_key, first_seen, id`, sc.Kind, sc.ID, source)
	if err != nil {
		return nil, err
	}
	var out []*group
	idx := map[string]*group{}
	for _, p := range rows {
		g := idx[p.group]
		if g == nil {
			g = &group{key: p.group}
			idx[p.group] = g
			out = append(out, g)
		}
		g.members = append(g.members, p)
		if p.itemID != "" {
			g.itemID = p.itemID
		}
	}
	return out, nil
}

func (s *Store) autoAccepts(eff *Effective, source string, severity int) bool {
	if eff.Proposals == nil {
		return false
	}
	for _, a := range eff.Proposals.AutoAccept {
		if a.Source == source && severity >= a.MinSeverity {
			return true
		}
	}
	return false
}

// reconcile brings items in line with the proposals of a source: items that
// exist are synced; groups that may be filed without a review are opened, up to
// the cap, and the rest fold into the summary item.
func (s *Store) reconcile(ctx context.Context, tx DB, sc Scope, b Batch, res *ProposeResult) error {
	eff, err := s.Settings(ctx, sc)
	if err != nil {
		return err
	}
	groups, err := s.groupsOf(ctx, tx, sc, b.Source)
	if err != nil {
		return err
	}
	limit := defaultMaxItems
	if eff.Proposals != nil && eff.Proposals.MaxItems > 0 {
		limit = eff.Proposals.MaxItems
	}
	var fresh, folded []*group
	for _, g := range groups {
		isFolded := slices.ContainsFunc(g.members, func(p propRow) bool { return p.state == "folded" })
		switch {
		case g.itemID != "":
			if err := s.syncGroup(ctx, tx, sc, eff, b, g, res); err != nil {
				return err
			}
		case g.unresolved() == 0 || g.key == overflowGroup:
		case isFolded:
			// Folded stays folded: a re-run does not open more items.
			folded = append(folded, g)
		case s.autoAccepts(eff, b.Source, g.maxSeverity()):
			fresh = append(fresh, g)
		default:
			res.Waiting++
		}
	}
	sort.SliceStable(fresh, func(i, j int) bool {
		if fresh[i].maxSeverity() != fresh[j].maxSeverity() {
			return fresh[i].maxSeverity() > fresh[j].maxSeverity()
		}
		return fresh[i].unresolved() > fresh[j].unresolved()
	})
	for i, g := range fresh {
		if i < limit {
			if err := s.openGroup(ctx, tx, sc, eff, b, g, GroupEdit{}, res); err != nil {
				return err
			}
			continue
		}
		res.Folded++
		folded = append(folded, g)
		if _, err := tx.Exec(ctx, `UPDATE work_proposals SET state = 'folded' WHERE scope_kind = ? AND scope_id = ? AND source = ? AND group_key = ? AND state = 'pending'`, sc.Kind, sc.ID, b.Source, g.key); err != nil {
			return err
		}
	}
	return s.syncOverflow(ctx, tx, sc, eff, b, folded, res)
}

const overflowGroup = "~overflow"

// syncOverflow keeps the single "and N more" item for what the cap folded.
func (s *Store) syncOverflow(ctx context.Context, tx DB, sc Scope, eff *Effective, b Batch, folded []*group, res *ProposeResult) error {
	key := fmt.Sprintf("proposal:%s:%s", b.Source, overflowGroup)
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM work_items WHERE scope_kind = ? AND scope_id = ? AND source_key = ?`, sc.Kind, sc.ID, key).Scan(&id)
	if len(folded) == 0 && isNoRows(err) {
		return nil
	}
	if len(folded) == 0 {
		it, lerr := s.load(ctx, tx, id)
		if lerr == nil && !eff.Closed(it.State) {
			return s.closeByRule(ctx, tx, eff, it, b, "Nothing is left of what the cap folded, so this summary closed.", res)
		}
		return nil
	}
	if err != nil && !isNoRows(err) {
		return err
	}
	var lines []string
	total := 0
	for _, g := range folded {
		total += g.unresolved()
		lines = append(lines, g.title())
	}
	title := fmt.Sprintf("%s: and %d more findings", b.Source, total)
	body := fmt.Sprintf("The %s analysis found more than the %s may open at once. The full report is at %s. Open the proposals for this project to accept the ones that matter.", b.Source, "limit", orNone(b.ReportURL))
	if isNoRows(err) {
		it, err := s.createProposed(ctx, tx, sc, eff, b, key, NewItem{Scope: sc, Type: TypeIssue, Title: title, Body: body, Priority: "low", Criteria: lines})
		if err != nil {
			return err
		}
		res.Opened++
		_ = it
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE work_items SET title = ?, body = ?, updated_at = ? WHERE id = ?`, title, body, ms(s.now()), id)
	res.Synced++
	return err
}

func orNone(s string) string {
	if s == "" {
		return "(not given)"
	}
	return s
}

func (g *group) title() string {
	t := g.members[0].title
	for _, p := range g.members {
		if p.groupTitle != "" {
			t = strings.ReplaceAll(p.groupTitle, "{n}", fmt.Sprint(g.unresolved()))
		}
	}
	return t
}
