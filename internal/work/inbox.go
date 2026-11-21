package work

import (
	"context"
	"slices"
)

// Inbox entry kinds. Email is not part of the product; these appear in the
// person's inbox and on the home page.
const (
	InboxAssigned  = "assigned"
	InboxMentioned = "mentioned"
	InboxUnblocked = "unblocked"
	InboxAgentDone = "agent_review"
	InboxAgentGone = "agent_returned"
	InboxState     = "state"
	InboxComment   = "comment"
)

// InboxEntry is one line of a person's inbox.
type InboxEntry struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	ItemID string `json:"item_id,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Title  string `json:"title,omitempty"`
	Text   string `json:"text"`
	At     string `json:"at"`
	Read   bool   `json:"read"`
}

// Hook receives what happened after it has been stored; the server turns
// these into events and notifications. It must not block.
type Hook func(ctx context.Context, e Event)

// SetHook sets the receiver of events.
func (s *Store) SetHook(h Hook) { s.hook = h }

func (s *Store) fire(ctx context.Context, e Event) {
	if s.hook != nil {
		s.hook(ctx, e)
	}
}

func (s *Store) invalidate() {
	s.mu.Lock()
	s.cache = nil
	s.closed = nil
	s.prefixes = nil
	s.mu.Unlock()
}

// notify puts a line in someone's inbox (never the actor's own).
func (s *Store) notify(ctx context.Context, tx DB, who, by, kind, itemID, text string) error {
	if who == "" || who == by || len(who) > 200 {
		return nil
	}
	// An agent has no inbox: its queue is how it hears.
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_agents WHERE name = ?`, who).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_inbox(who, kind, item_id, text, at) VALUES(?,?,?,?,?)`, who, kind, itemID, text, ms(s.now()))
	return err
}

func (s *Store) notifyAssigned(ctx context.Context, tx DB, c Caller, itemID, title string, names map[string]bool) error {
	for n := range names {
		if err := s.notify(ctx, tx, n, c.Name, InboxAssigned, itemID, c.Name+" assigned you: "+title); err != nil {
			return err
		}
	}
	return nil
}

// notifyState tells watchers about a move, the reporter about a finished
// item, and the watchers of items that were waiting for this one.
func (s *Store) notifyState(ctx context.Context, tx DB, c Caller, it *Item, to string, eff *Effective) error {
	who := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT who FROM work_watchers WHERE item_id = ?`, it.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			rows.Close()
			return err
		}
		who[w] = true
	}
	rows.Close()
	name := to
	if st, ok := eff.StateByKey(to); ok {
		name = st.Name
	}
	for w := range who {
		if err := s.notify(ctx, tx, w, c.Name, InboxState, it.ID, it.Ref+" moved to "+name); err != nil {
			return err
		}
	}
	if !eff.Closed(to) {
		return nil
	}
	// Items this one blocked, that nothing else blocks now.
	rows, err = tx.Query(ctx, `SELECT to_id FROM work_links WHERE from_id = ? AND kind = 'blocks'`, it.ID)
	if err != nil {
		return err
	}
	var blocked []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		blocked = append(blocked, id)
	}
	rows.Close()
	for _, id := range blocked {
		b, err := s.load(ctx, tx, id)
		if err != nil {
			continue
		}
		// This item's own state is not yet written: ignore it.
		others := slices.DeleteFunc(slices.Clone(b.BlockedBy), func(r string) bool { return r == it.Ref })
		if len(others) > 0 {
			continue
		}
		recipients := map[string]bool{b.Reporter: true}
		for _, a := range b.Assignees {
			recipients[a.Name] = true
		}
		wr, err := tx.Query(ctx, `SELECT who FROM work_watchers WHERE item_id = ?`, id)
		if err != nil {
			return err
		}
		for wr.Next() {
			var w string
			if err := wr.Scan(&w); err != nil {
				wr.Close()
				return err
			}
			recipients[w] = true
		}
		wr.Close()
		for w := range recipients {
			if err := s.notify(ctx, tx, w, c.Name, InboxUnblocked, id, b.Ref+" is no longer blocked: "+it.Ref+" is finished"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Notify adds an entry for a person, for the interfaces that learn of things
// the store cannot (a pull request opened for an item).
func (s *Store) Notify(ctx context.Context, who, by, kind, itemID, text string) error {
	return s.tx(ctx, func(tx DB) error { return s.notify(ctx, tx, who, by, kind, itemID, text) })
}

// Inbox lists a person's entries, newest first.
func (s *Store) Inbox(ctx context.Context, who string, unreadOnly bool, limit int) ([]InboxEntry, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT n.id, n.kind, n.item_id, n.text, n.at, n.read_at FROM work_inbox n WHERE n.who = ?`
	if unreadOnly {
		q += ` AND n.read_at = 0`
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY n.id DESC LIMIT ?`, who, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []InboxEntry{}
	for rows.Next() {
		var e InboxEntry
		var at, read int64
		if err := rows.Scan(&e.ID, &e.Kind, &e.ItemID, &e.Text, &at, &read); err != nil {
			return nil, 0, err
		}
		e.At, e.Read = iso(at), read > 0
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for i := range out {
		if out[i].ItemID == "" {
			continue
		}
		var n int
		var k, id string
		if s.db.QueryRow(ctx, `SELECT number, scope_kind, scope_id FROM work_items WHERE id = ?`, out[i].ItemID).Scan(&n, &k, &id) == nil {
			p, _ := s.Prefix(ctx, Scope{Kind: k, ID: id})
			out[i].Ref = p + "-" + itoa(n)
			_ = s.db.QueryRow(ctx, `SELECT title FROM work_items WHERE id = ?`, out[i].ItemID).Scan(&out[i].Title)
		}
	}
	var unread int
	err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_inbox WHERE who = ? AND read_at = 0`, who).Scan(&unread)
	return out, unread, err
}

// MarkRead marks entries read (all of them when ids is empty).
func (s *Store) MarkRead(ctx context.Context, who string, ids []int64) error {
	if len(ids) == 0 {
		_, err := s.db.Exec(ctx, `UPDATE work_inbox SET read_at = ? WHERE who = ? AND read_at = 0`, ms(s.now()), who)
		return err
	}
	for _, id := range ids {
		if _, err := s.db.Exec(ctx, `UPDATE work_inbox SET read_at = ? WHERE who = ? AND id = ? AND read_at = 0`, ms(s.now()), who, id); err != nil {
			return err
		}
	}
	return nil
}
