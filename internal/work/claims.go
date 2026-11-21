package work

import (
	"context"
	"slices"
	"strings"
	"time"
)

// ---- claims ----

// Lease times.
const (
	DefaultLease = 30 * time.Minute
	MaxLease     = 4 * time.Hour
)

// SweepLeases releases every claim whose lease has run out: the item goes
// back to Ready, the owner of the agent is told, the history says why.
func (s *Store) SweepLeases(ctx context.Context) (int, error) {
	now := ms(s.now())
	rows, err := s.db.Query(ctx, `SELECT item_id, agent FROM work_claims WHERE lease_until < ?`, now)
	if err != nil {
		return 0, err
	}
	type cl struct{ item, agent string }
	var list []cl
	for rows.Next() {
		var x cl
		if err := rows.Scan(&x.item, &x.agent); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, x)
	}
	rows.Close()
	n := 0
	for _, x := range list {
		err := s.tx(ctx, func(tx DB) error {
			res, err := tx.Exec(ctx, `DELETE FROM work_claims WHERE item_id = ? AND lease_until < ?`, x.item, now)
			if err != nil {
				return err
			}
			if k, _ := res.RowsAffected(); k == 0 {
				return nil
			}
			n++
			return s.returnItem(ctx, tx, Actor{Name: x.agent, Kind: KindAgent, Via: "system"}, x.item, x.agent, "the lease ran out without a heartbeat", "lease_expired")
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// returnItem puts an item back to Ready after an agent let go of it, tells
// the people who care, and logs it.
func (s *Store) returnItem(ctx context.Context, tx DB, by Actor, itemID, agent, reason, event string) error {
	it, err := s.load(ctx, tx, itemID)
	if err != nil {
		return nil
	}
	eff, err := s.settingsCached(ctx, it.Scope)
	if err != nil {
		return err
	}
	now := ms(s.now())
	if eff.Category(it.State) == CatDoing {
		ready := "ready"
		if _, ok := eff.StateByKey(ready); !ok {
			ready = eff.FirstOf(CatTodo)
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET state = ?, version = version + 1, updated_at = ? WHERE id = ?`, ready, now, itemID); err != nil {
			return err
		}
	}
	if err := s.event(ctx, tx, itemID, by, event, 0, map[string]any{"agent": agent, "reason": reason}); err != nil {
		return err
	}
	if err := s.logAgent(ctx, tx, agent, itemID, event, reason); err != nil {
		return err
	}
	who := map[string]bool{it.Reporter: true}
	for _, a := range it.Assignees {
		if a.Kind == KindPerson {
			who[a.Name] = true
		}
	}
	var owner string
	_ = tx.QueryRow(ctx, `SELECT owner FROM work_agents WHERE name = ?`, agent).Scan(&owner)
	who[owner] = true
	for w := range who {
		if err := s.notify(ctx, tx, w, agent, InboxAgentGone, itemID, "Agent "+agent+" gave back "+it.Ref+": "+reason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) releaseAll(ctx context.Context, tx DB, by Actor, agent, reason string) error {
	rows, err := tx.Query(ctx, `SELECT item_id FROM work_claims WHERE agent = ?`, agent)
	if err != nil {
		return err
	}
	var idList []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		idList = append(idList, id)
	}
	rows.Close()
	for _, id := range idList {
		if _, err := tx.Exec(ctx, `DELETE FROM work_claims WHERE item_id = ?`, id); err != nil {
			return err
		}
		if err := s.returnItem(ctx, tx, by, id, agent, reason, "agent_released"); err != nil {
			return err
		}
	}
	return nil
}

// Queue lists what is waiting for an agent: items assigned to it, and items
// in a pool it works from that nobody holds, within its projects and types,
// not blocked, not confidential, in the order of the backlog.
func (s *Store) Queue(ctx context.Context, agent *Agent, limit int) ([]*Item, error) {
	if _, err := s.SweepLeases(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	c := System("system")
	closed, err := s.closedStates(ctx)
	if err != nil {
		return nil, err
	}
	cin, cargs := inList(closed)
	var conds []string
	var args []any
	assigned := `EXISTS (SELECT 1 FROM work_assignees x WHERE x.item_id = i.id AND x.who = ? AND x.kind = 'agent')`
	args = append(args, agent.Name)
	pool := ""
	if len(agent.Labels) > 0 {
		in, a := inList(lowerAll(agent.Labels))
		pool = ` OR (NOT EXISTS (SELECT 1 FROM work_assignees y WHERE y.item_id = i.id) AND EXISTS (SELECT 1 FROM work_labels l WHERE l.item_id = i.id AND l.label IN (` + in + `)))`
		args = append(args, a...)
	}
	conds = append(conds, `(`+assigned+pool+`)`, `i.confidential = 0`, `i.state NOT IN (`+cin+`)`,
		`NOT EXISTS (SELECT 1 FROM work_claims k WHERE k.item_id = i.id AND k.lease_until >= ?)`,
		`NOT EXISTS (SELECT 1 FROM work_links bl JOIN work_items bi ON bi.id = bl.from_id WHERE bl.kind = 'blocks' AND bl.to_id = i.id AND bi.state NOT IN (`+cin+`))`)
	args = append(args, cargs...)
	args = append(args, ms(s.now()))
	args = append(args, cargs...)
	rows, err := s.db.Query(ctx, `SELECT `+itemColsI+` FROM work_items i WHERE `+strings.Join(conds, " AND ")+` ORDER BY CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, i.rank LIMIT ?`, append(args, limit*5)...)
	if err != nil {
		return nil, err
	}
	var items []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := s.hydrate(ctx, s.db, items, false); err != nil {
		return nil, err
	}
	_ = c
	out := []*Item{}
	for _, it := range items {
		eff, err := s.settingsCached(ctx, it.Scope)
		if err != nil {
			return nil, err
		}
		// Only work that has not started: an item an agent handed over for
		// review is not offered again.
		if eff.Category(it.State) != CatTodo && eff.Category(it.State) != CatDoing {
			continue
		}
		if !s.AgentCovers(agent, it.Scope) || !agentAccepts(agent, it) || s.checkClass(ctx, agent, it) != nil {
			continue
		}
		if ok, _, err := s.levelEnabled(ctx, it.Scope); err != nil || !ok {
			continue
		}
		out = append(out, it)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = strings.ToLower(v)
	}
	return out
}

// ClaimOptions say what to claim.
type ClaimOptions struct {
	Item  string        // a ref, or "" for the next in the queue
	Lease time.Duration // 0 is the default
	Note  string
}

// Claim gives an agent an item with a lease. All of the server's rules are
// applied here: the agent is on, its level is on, its limits are not past, the
// item is one it may take, and nobody else holds it. Two agents asking for the
// same item cannot both get it.
func (s *Store) Claim(ctx context.Context, agent *Agent, o ClaimOptions) (*Item, error) {
	lease := o.Lease
	if lease <= 0 {
		lease = DefaultLease
	}
	if lease > MaxLease {
		lease = MaxLease
	}
	if _, err := s.SweepLeases(ctx); err != nil {
		return nil, err
	}
	a, err := s.AgentByName(ctx, agent.Name)
	if err != nil {
		return nil, err
	}
	if a.Disabled {
		return nil, forbidden("agent %s is switched off", a.Name)
	}
	if a.Active >= a.Concurrency {
		return nil, conflict("agent %s already holds %d item(s), its limit; finish or hand one back first", a.Name, a.Active)
	}
	if a.Today >= a.PerDay {
		return nil, conflict("agent %s has taken %d items in the last 24 hours, its limit", a.Name, a.Today)
	}
	if (a.BudgetTokensDay > 0 && a.UsedTokensDay >= a.BudgetTokensDay) || (a.BudgetCentsDay > 0 && a.UsedCentsDay >= a.BudgetCentsDay) {
		return nil, conflict("agent %s has used its budget for the last 24 hours (%d tokens, %d cents reported); a person may raise it", a.Name, a.UsedTokensDay, a.UsedCentsDay)
	}
	var target *Item
	if o.Item != "" {
		id, err := s.idOf(ctx, s.db, o.Item)
		if err != nil {
			return nil, err
		}
		target, err = s.load(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		assigned := slices.ContainsFunc(target.Assignees, func(x Assignee) bool { return x.Name == a.Name })
		if !assigned && !(len(target.Assignees) == 0 && a.inPool(target)) {
			return nil, forbidden("%s is not assigned to agent %s, and is not in its pool", target.Ref, a.Name)
		}
	} else {
		q, err := s.Queue(ctx, a, 1)
		if err != nil {
			return nil, err
		}
		if len(q) == 0 {
			return nil, notFound("nothing is waiting for agent " + a.Name)
		}
		target = q[0]
	}
	if err := s.checkClass(ctx, a, target); err != nil {
		return nil, err
	}
	if target.Confidential || !s.AgentCovers(a, target.Scope) || !agentAccepts(a, target) {
		return nil, forbidden("agent %s may not take %s", a.Name, target.Ref)
	}
	if ok, _, err := s.levelEnabled(ctx, target.Scope); err != nil {
		return nil, err
	} else if !ok {
		return nil, forbidden("agents are switched off for %s", target.Scope)
	}
	actor := Actor{Name: a.Name, Kind: KindAgent, Via: "api"}
	err = s.tx(ctx, func(tx DB) error {
		now := ms(s.now())
		cur, err := s.load(ctx, tx, target.ID)
		if err != nil {
			return err
		}
		eff, err := s.settingsCached(ctx, cur.Scope)
		if err != nil {
			return err
		}
		if eff.Closed(cur.State) {
			return conflict("%s is finished", cur.Ref)
		}
		if cur.Blocked {
			return conflict("%s is blocked by %s", cur.Ref, strings.Join(cur.BlockedBy, ", "))
		}
		res, err := tx.Exec(ctx, `INSERT INTO work_claims(item_id, agent, claimed_at, heartbeat_at, lease_until, note)
			SELECT ?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM work_claims WHERE item_id = ?)`, cur.ID, a.Name, now, now, now+lease.Milliseconds(), cleanTitle(o.Note), cur.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("%s is held by another agent", cur.Ref)
		}
		// Concurrency again, now that the claim is in the same transaction
		// that decides: another claim of this agent may have landed.
		var held int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_claims WHERE agent = ? AND lease_until >= ?`, a.Name, now).Scan(&held); err != nil {
			return err
		}
		if held > a.Concurrency {
			return conflict("agent %s already holds its limit", a.Name)
		}
		if !slices.ContainsFunc(cur.Assignees, func(x Assignee) bool { return x.Name == a.Name }) {
			if _, err := tx.Exec(ctx, `INSERT INTO work_assignees(item_id, who, kind) VALUES(?,?,?)`, cur.ID, a.Name, KindAgent); err != nil {
				return err
			}
		}
		to := cur.State
		if eff.Category(cur.State) == CatTodo {
			to = "in_progress"
			if _, ok := eff.StateByKey(to); !ok {
				to = firstDoing(eff, cur.State)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET state = ?, version = version + 1, updated_at = ? WHERE id = ?`, to, now, cur.ID); err != nil {
			return err
		}
		if err := s.event(ctx, tx, cur.ID, actor, "claimed", 0, map[string]any{"agent": a.Name, "lease_until": iso(now + lease.Milliseconds()), "from": cur.State, "to": to}); err != nil {
			return err
		}
		return s.logAgent(ctx, tx, a.Name, cur.ID, "claim", cur.Ref)
	})
	if err != nil {
		return nil, err
	}
	s.invalidate()
	return s.load(ctx, s.db, target.ID)
}

func firstDoing(eff *Effective, fallback string) string {
	if k := eff.FirstOf(CatDoing); k != "" {
		return k
	}
	return fallback
}

// holds loads an item and checks that the agent holds it with a live lease.
func (s *Store) holds(ctx context.Context, agent *Agent, ref string) (*Item, error) {
	id, err := s.idOf(ctx, s.db, ref)
	if err != nil {
		return nil, err
	}
	it, err := s.load(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if it.Claim == nil || it.Claim.Agent != agent.Name {
		return nil, forbidden("agent %s does not hold %s", agent.Name, it.Ref)
	}
	if it.Claim.Expired {
		return nil, conflict("the lease on %s ran out; claim it again", it.Ref)
	}
	a, err := s.AgentByName(ctx, agent.Name)
	if err != nil {
		return nil, err
	}
	if a.Disabled {
		return nil, forbidden("agent %s is switched off", agent.Name)
	}
	return it, nil
}

// Heartbeat extends the lease on an item the agent holds.
func (s *Store) Heartbeat(ctx context.Context, agent *Agent, ref, note string, lease time.Duration) (*Claim, error) {
	if _, err := s.holds(ctx, agent, ref); err != nil {
		return nil, err
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	lease = min(lease, MaxLease)
	id, _ := s.idOf(ctx, s.db, ref)
	if u, err := s.ItemUsage(ctx, id, agent.Name); err == nil && u.Exceeded {
		return nil, conflict("%s: hand it back or ask a person to raise the budget", u.Message)
	}
	now := ms(s.now())
	if _, err := s.db.Exec(ctx, `UPDATE work_claims SET heartbeat_at = ?, lease_until = ?, note = CASE WHEN ? <> '' THEN ? ELSE note END WHERE item_id = ? AND agent = ?`, now, now+lease.Milliseconds(), cleanTitle(note), cleanTitle(note), id, agent.Name); err != nil {
		return nil, err
	}
	it, err := s.load(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	return it.Claim, nil
}

// HandBack gives an item back with a reason. It returns to Ready.
func (s *Store) HandBack(ctx context.Context, agent *Agent, ref, reason string) (*Item, error) {
	reason = cleanTitle(reason)
	if reason == "" {
		return nil, invalid("say why you are handing it back")
	}
	it, err := s.holds(ctx, agent, ref)
	if err != nil {
		return nil, err
	}
	err = s.tx(ctx, func(tx DB) error {
		if _, err := tx.Exec(ctx, `DELETE FROM work_claims WHERE item_id = ?`, it.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM work_assignees WHERE item_id = ? AND who = ? AND kind = 'agent'`, it.ID, agent.Name); err != nil {
			return err
		}
		return s.returnItem(ctx, tx, Actor{Name: agent.Name, Kind: KindAgent, Via: "api"}, it.ID, agent.Name, reason, "agent_handed_back")
	})
	if err != nil {
		return nil, err
	}
	s.fire(ctx, Event{ItemID: it.ID, Kind: "agent.handed_back", Data: map[string]any{"agent": agent.Name, "ref": it.Ref, "reason": reason}})
	return s.load(ctx, s.db, it.ID)
}

// RequestReview is how an agent says it has finished: the item moves to In
// review, the claim ends, and the people who care are told that a review is
// needed. It does not finish the item; only a person does that.
func (s *Store) RequestReview(ctx context.Context, agent *Agent, ref, summary string) (*Item, error) {
	it, err := s.holds(ctx, agent, ref)
	if err != nil {
		return nil, err
	}
	eff, err := s.settingsCached(ctx, it.Scope)
	if err != nil {
		return nil, err
	}
	review := eff.FirstOf(CatReview)
	if review == "" {
		return nil, conflict("this workflow has no review state")
	}
	actor := Actor{Name: agent.Name, Kind: KindAgent, Via: "api"}
	err = s.tx(ctx, func(tx DB) error {
		now := ms(s.now())
		if _, err := tx.Exec(ctx, `DELETE FROM work_claims WHERE item_id = ?`, it.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET state = ?, version = version + 1, updated_at = ? WHERE id = ?`, review, now, it.ID); err != nil {
			return err
		}
		if err := s.event(ctx, tx, it.ID, actor, "review_requested", 0, map[string]any{"agent": agent.Name, "summary": cleanTitle(summary), "from": it.State, "to": review}); err != nil {
			return err
		}
		if err := s.logAgent(ctx, tx, agent.Name, it.ID, "review", summary); err != nil {
			return err
		}
		who := map[string]bool{it.Reporter: true}
		for _, a := range it.Assignees {
			if a.Kind == KindPerson {
				who[a.Name] = true
			}
		}
		var owner string
		_ = tx.QueryRow(ctx, `SELECT owner FROM work_agents WHERE name = ?`, agent.Name).Scan(&owner)
		who[owner] = true
		for w := range who {
			if err := s.notify(ctx, tx, w, agent.Name, InboxAgentDone, it.ID, "Agent "+agent.Name+" finished "+it.Ref+": a review is needed"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.fire(ctx, Event{ItemID: it.ID, Kind: "agent.review_requested", Data: map[string]any{"agent": agent.Name, "ref": it.Ref}})
	return s.load(ctx, s.db, it.ID)
}

// AgentLog is an agent's recent actions.
type AgentLogEntry struct {
	At     string `json:"at"`
	Item   string `json:"item,omitempty"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
}

// AgentActions lists what an agent did, newest first.
func (s *Store) AgentActions(ctx context.Context, name string, limit int) ([]AgentLogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT at, item_id, action, detail FROM work_agent_log WHERE agent = ? ORDER BY seq DESC LIMIT ?`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentLogEntry{}
	for rows.Next() {
		var e AgentLogEntry
		var at int64
		var id string
		if err := rows.Scan(&at, &id, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.At = iso(at)
		if id != "" {
			e.Item = s.refOf(ctx, s.db, id)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AgentWork is what the home page shows a person about agents: what agents did
// for them and what waits for their review.
type AgentWork struct {
	Review []*Item         `json:"review"` // handed over by an agent, waiting for a person
	Recent []AgentLogEntry `json:"recent"`
}

// AgentWorkFor lists items in review that an agent handed over and the person
// reported or is assigned to, with what agents did for them lately.
func (s *Store) AgentWorkFor(ctx context.Context, c Caller) (*AgentWork, error) {
	page, err := s.List(ctx, c, ListOptions{Query: `state:in_review`, Sort: "updated", Desc: true, Limit: 100})
	if err != nil {
		return nil, err
	}
	out := &AgentWork{Review: []*Item{}, Recent: []AgentLogEntry{}}
	for _, it := range page.Items {
		mine := it.Reporter == c.Name || slices.ContainsFunc(it.Assignees, func(a Assignee) bool { return a.Name == c.Name }) || c.isMaintainer(it.Scope)
		agent := slices.ContainsFunc(it.Assignees, func(a Assignee) bool { return a.Kind == KindAgent })
		if mine && agent {
			out.Review = append(out.Review, it)
		}
	}
	rows, err := s.db.Query(ctx, `SELECT l.agent, l.at, l.item_id, l.action, l.detail FROM work_agent_log l JOIN work_agents a ON a.name = l.agent
		WHERE a.owner = ? AND l.action IN ('claim','review','agent_handed_back','lease_expired') ORDER BY l.seq DESC LIMIT 20`, c.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var agent, id string
		var at int64
		var e AgentLogEntry
		if err := rows.Scan(&agent, &at, &id, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.At = iso(at)
		e.Detail = agent + ": " + e.Detail
		if id != "" {
			e.Item = s.refOf(ctx, s.db, id)
		}
		out.Recent = append(out.Recent, e)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
