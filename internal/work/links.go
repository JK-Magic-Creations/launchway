package work

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Link kinds between two items.
const (
	LinkBlocks     = "blocks"
	LinkRelates    = "relates"
	LinkDuplicates = "duplicates"
)

// Link is a typed link from one item to another. blocks and duplicates have a
// direction (from blocks to; from duplicates to); relates is symmetric.
type Link struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	From      string `json:"from"` // ref
	To        string `json:"to"`
	FromTitle string `json:"from_title,omitempty"`
	ToTitle   string `json:"to_title,omitempty"`
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AddLink links two items. Linking across projects needs the caller to see
// both; a blocks link that would make a cycle is refused and the chain named.
func (s *Store) AddLink(ctx context.Context, c Caller, fromRef, kind, toRef string) (*Link, error) {
	if !slices.Contains([]string{LinkBlocks, LinkRelates, LinkDuplicates}, kind) {
		return nil, invalid("a link is blocks, relates or duplicates")
	}
	from, err := s.Get(ctx, c, fromRef)
	if err != nil {
		return nil, err
	}
	to, err := s.Get(ctx, c, toRef)
	if err != nil {
		return nil, err
	}
	if from.ID == to.ID {
		return nil, invalid("an item cannot be linked to itself")
	}
	if !c.canWrite(from.Scope) {
		return nil, forbidden("you may not change work in %s", from.Scope)
	}
	if kind == LinkRelates && from.ID > to.ID {
		from, to = to, from
	}
	l := &Link{ID: newID("wl")}
	err = s.tx(ctx, func(tx DB) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_links WHERE from_id = ? AND to_id = ? AND kind = ?`, from.ID, to.ID, kind).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return conflict("%s already %s %s", from.Ref, kind, to.Ref)
		}
		if kind == LinkBlocks {
			if path, err := s.blockPath(ctx, tx, to.ID, from.ID); err != nil {
				return err
			} else if path != nil {
				names := []string{from.Ref}
				for _, id := range path {
					names = append(names, s.refOf(ctx, tx, id))
				}
				return invalid("that would make a cycle: %s blocks itself through %s", from.Ref, strings.Join(names, " blocks "))
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_links(id, from_id, to_id, kind, created_by, created_at) VALUES(?,?,?,?,?,?)`, l.ID, from.ID, to.ID, kind, c.Name, ms(s.now())); err != nil {
			return err
		}
		if err := s.event(ctx, tx, from.ID, c.Actor, "linked", 0, map[string]any{"kind": kind, "other": to.Ref}); err != nil {
			return err
		}
		return s.event(ctx, tx, to.ID, c.Actor, "linked", 0, map[string]any{"kind": "inverse:" + kind, "other": from.Ref})
	})
	if err != nil {
		return nil, err
	}
	s.invalidate()
	l.Kind, l.From, l.To = kind, from.Ref, to.Ref
	return l, nil
}

// blockPath finds a chain of blocks links from one item to another (so that
// adding the opposite link would close a cycle), returning the ids along it.
func (s *Store) blockPath(ctx context.Context, tx DB, start, target string) ([]string, error) {
	type node struct {
		id   string
		prev int
	}
	nodes := []node{{start, -1}}
	seen := map[string]bool{start: true}
	for i := 0; i < len(nodes) && len(nodes) < 5000; i++ {
		if nodes[i].id == target {
			var path []string
			for j := i; j >= 0; j = nodes[j].prev {
				path = append([]string{nodes[j].id}, path...)
			}
			return path, nil
		}
		next, err := queryAll(ctx, tx, scanString, `SELECT to_id FROM work_links WHERE from_id = ? AND kind = 'blocks'`, nodes[i].id)
		if err != nil {
			return nil, err
		}
		for _, id := range next {
			if !seen[id] {
				seen[id] = true
				nodes = append(nodes, node{id, i})
			}
		}
	}
	return nil, nil
}

func (s *Store) refOf(ctx context.Context, db DB, id string) string {
	var k, sid string
	var n int
	if db.QueryRow(ctx, `SELECT scope_kind, scope_id, number FROM work_items WHERE id = ?`, id).Scan(&k, &sid, &n) != nil {
		return id
	}
	p, _ := s.prefixIn(ctx, db, Scope{Kind: k, ID: sid})
	return fmt.Sprintf("%s-%d", p, n)
}

// RemoveLink removes a link.
func (s *Store) RemoveLink(ctx context.Context, c Caller, id string) error {
	var from, to, kind string
	err := s.db.QueryRow(ctx, `SELECT from_id, to_id, kind FROM work_links WHERE id = ?`, id).Scan(&from, &to, &kind)
	if isNoRows(err) {
		return notFound("no link " + id)
	}
	if err != nil {
		return err
	}
	fi, err := s.Get(ctx, c, from)
	if err != nil {
		return err
	}
	if !c.canWrite(fi.Scope) {
		return forbidden("you may not change work in %s", fi.Scope)
	}
	return s.tx(ctx, func(tx DB) error {
		if _, err := tx.Exec(ctx, `DELETE FROM work_links WHERE id = ?`, id); err != nil {
			return err
		}
		return s.event(ctx, tx, from, c.Actor, "unlinked", 0, map[string]any{"kind": kind, "other": s.refOf(ctx, tx, to)})
	})
}

// Links lists an item's links, both directions, hiding items the caller may
// not see.
func (s *Store) Links(ctx context.Context, c Caller, ref string) ([]Link, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	type raw struct {
		l        Link
		from, to string
		at       int64
	}
	rs, err := queryAll(ctx, s.db, func(sc scanner) (raw, error) {
		var r raw
		err := sc.Scan(&r.l.ID, &r.from, &r.to, &r.l.Kind, &r.l.CreatedBy, &r.at)
		return r, err
	}, `SELECT id, from_id, to_id, kind, created_by, created_at FROM work_links WHERE from_id = ? OR to_id = ? ORDER BY created_at`, it.ID, it.ID)
	if err != nil {
		return nil, err
	}
	out := []Link{}
	for _, r := range rs {
		a, err := s.load(ctx, s.db, r.from)
		if err != nil {
			continue
		}
		b, err := s.load(ctx, s.db, r.to)
		if err != nil {
			continue
		}
		if !c.canSee(a) || !c.canSee(b) {
			continue
		}
		r.l.From, r.l.To, r.l.FromTitle, r.l.ToTitle, r.l.FromState, r.l.ToState, r.l.CreatedAt = a.Ref, b.Ref, a.Title, b.Title, a.State, b.State, iso(r.at)
		out = append(out, r.l)
	}
	return out, nil
}

// Code links: commits, branches, pull requests and Sorties that belong to an item.
const (
	CodeCommit = "commit"
	CodeBranch = "branch"
	// CodeRepository ties an item to a repository as a whole (Ref is the repository path).
	CodeRepository = "repository"
	CodePull       = "pull"
	CodeSortie     = "run"
)

// CodeLink ties an item to something in the code or in delivery.
type CodeLink struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Repo      string `json:"repo,omitempty"`
	Ref       string `json:"ref"` // a commit hash, a branch name, a pull request number, a run id
	Title     string `json:"title,omitempty"`
	State     string `json:"state,omitempty"` // open, merged, closed; success, failed
	Closes    bool   `json:"closes,omitempty"`
	Source    string `json:"source,omitempty"` // message, description, manual, agent
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// LinkCode records or updates a code link of an item. It is the same link
// when kind, repo and ref are: calling it again changes the title, state and closes.
func (s *Store) LinkCode(ctx context.Context, c Caller, itemRef string, l CodeLink) (*CodeLink, error) {
	it, err := s.Get(ctx, c, itemRef)
	if err != nil {
		return nil, err
	}
	if !slices.Contains([]string{CodeCommit, CodeBranch, CodePull, CodeSortie, CodeRepository}, l.Kind) || l.Ref == "" || len(l.Ref) > 300 {
		return nil, invalid("a code link is a commit, branch, pull, run or repository with a reference")
	}
	if !c.canWrite(it.Scope) && c.Kind != KindSystem {
		return nil, forbidden("you may not change work in %s", it.Scope)
	}
	l.Title = cleanTitle(l.Title)
	if len(l.Title) > 300 {
		l.Title = l.Title[:300]
	}
	now := ms(s.now())
	closes := 0
	if l.Closes {
		closes = 1
	}
	var out CodeLink
	err = s.tx(ctx, func(tx DB) error {
		var id string
		var oldState string
		qerr := tx.QueryRow(ctx, `SELECT id, state FROM work_code_links WHERE item_id = ? AND kind = ? AND repo = ? AND ref = ?`, it.ID, l.Kind, l.Repo, l.Ref).Scan(&id, &oldState)
		switch {
		case isNoRows(qerr):
			id = newID("wk")
			if _, err := tx.Exec(ctx, `INSERT INTO work_code_links(id, item_id, kind, repo, ref, title, state, closes, source, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
				id, it.ID, l.Kind, l.Repo, l.Ref, l.Title, l.State, closes, l.Source, now, now); err != nil {
				return err
			}
			if err := s.event(ctx, tx, it.ID, c.Actor, "code_linked", 0, map[string]any{"kind": l.Kind, "repo": l.Repo, "ref": l.Ref, "source": l.Source}); err != nil {
				return err
			}
		case qerr != nil:
			return qerr
		default:
			if _, err := tx.Exec(ctx, `UPDATE work_code_links SET title = CASE WHEN ? <> '' THEN ? ELSE title END, state = ?, closes = ?, updated_at = ? WHERE id = ?`, l.Title, l.Title, l.State, closes, now, id); err != nil {
				return err
			}
			if oldState != l.State && l.State != "" {
				if err := s.event(ctx, tx, it.ID, c.Actor, "code_state", 0, map[string]any{"kind": l.Kind, "repo": l.Repo, "ref": l.Ref, "from": oldState, "to": l.State}); err != nil {
					return err
				}
			}
		}
		out = CodeLink{ID: id, Kind: l.Kind, Repo: l.Repo, Ref: l.Ref, Title: l.Title, State: l.State, Closes: l.Closes, Source: l.Source, CreatedAt: iso(now)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CodeLinks lists what an item is tied to in the code.
func (s *Store) CodeLinks(ctx context.Context, c Caller, ref string) ([]CodeLink, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	return s.codeLinksOf(ctx, it.ID)
}

func (s *Store) codeLinksOf(ctx context.Context, id string) ([]CodeLink, error) {
	rows, err := s.db.Query(ctx, `SELECT id, kind, repo, ref, title, state, closes, source, created_at, updated_at FROM work_code_links WHERE item_id = ? ORDER BY kind, created_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CodeLink{}
	for rows.Next() {
		var l CodeLink
		var cl int
		var cr, up int64
		if err := rows.Scan(&l.ID, &l.Kind, &l.Repo, &l.Ref, &l.Title, &l.State, &cl, &l.Source, &cr, &up); err != nil {
			return nil, err
		}
		l.Closes, l.CreatedAt, l.UpdatedAt = cl == 1, iso(cr), iso(up)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ItemsForCode finds the items tied to a piece of code (a commit, a pull
// request), as the caller may see them.
func (s *Store) ItemsForCode(ctx context.Context, c Caller, kind, repo, ref string) ([]*Item, error) {
	idList, err := queryAll(ctx, s.db, scanString, `SELECT item_id FROM work_code_links WHERE kind = ? AND repo = ? AND ref = ?`, kind, repo, ref)
	if err != nil {
		return nil, err
	}
	out := []*Item{}
	for _, id := range idList {
		it, err := s.load(ctx, s.db, id)
		if err == nil && c.canSee(it) {
			out = append(out, it)
		}
	}
	return out, nil
}

// Reference is something in a text that names an item.
type Reference struct {
	Verb   string `json:"verb"`   // the keyword, lower case, or "" for a bare mention
	Closes bool   `json:"closes"` // the verb is one that closes
	Repo   string `json:"repo"`   // owner/name when given, else ""
	Ref    string `json:"ref"`    // PAY-12, or #12 when no key was given
	Number int    `json:"number"`
	Prefix string `json:"prefix"`
}

var refPattern = regexp.MustCompile(`(?i)(?:\b(fix(?:e[sd])?|close[sd]?|resolve[sd]?|refs?|references?|see|part of|related to|relates to)\b[:\s]+)?(?:\b([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)#(\d+)|\b([A-Z][A-Z0-9]{1,9})-(\d+)\b|(?:^|[\s(\[,])?#(\d+)\b)`)

var closingVerbs = map[string]bool{"fix": true, "fixes": true, "fixed": true, "close": true, "closes": true, "closed": true, "resolve": true, "resolves": true, "resolved": true}

// ParseReferences finds item references in a commit message or a pull
// request description: "fixes #12", "closes PAY-12", "refs owner/repo#7", and
// bare mentions. A closing verb applies to the references that follow it in the
// same list ("fixes #1, #2").
func ParseReferences(text string) []Reference {
	var out []Reference
	seen := map[string]bool{}
	lastClosing, lastVerb := false, ""
	for _, line := range strings.Split(text, "\n") {
		lastClosing, lastVerb = false, ""
		for _, idx := range refPattern.FindAllStringSubmatchIndex(line, -1) {
			m := make([]string, len(idx)/2)
			for i := range m {
				if idx[2*i] >= 0 {
					m[i] = line[idx[2*i]:idx[2*i+1]]
				}
			}
			if m[6] != "" && m[1] == "" {
				// A bare #12 must not be the tail of a word (C#12).
				if h := idx[12] - 1; h > 0 && isWordByte(line[h-1]) {
					continue
				}
			}
			r := Reference{}
			if m[1] != "" {
				r.Verb = strings.ToLower(m[1])
				lastClosing, lastVerb = closingVerbs[r.Verb], r.Verb
			} else if lastVerb != "" {
				// A list: "fixes #1, #2" carries the verb on.
				r.Verb = lastVerb
			}
			r.Closes = closingVerbs[r.Verb]
			switch {
			case m[2] != "":
				r.Repo, r.Number = m[2], atoi(m[3])
				r.Ref = fmt.Sprintf("%s#%d", m[2], r.Number)
			case m[4] != "":
				r.Prefix, r.Number = strings.ToUpper(m[4]), atoi(m[5])
				r.Ref = fmt.Sprintf("%s-%d", r.Prefix, r.Number)
			default:
				r.Number = atoi(m[6])
				r.Ref = fmt.Sprintf("#%d", r.Number)
			}
			_ = lastClosing
			if r.Number <= 0 || seen[r.Ref+"|"+r.Verb] {
				continue
			}
			seen[r.Ref+"|"+r.Verb] = true
			out = append(out, r)
		}
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' || n > 1e8 {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// Resolve turns a reference found in text written in the context of a project
// into an item: "#12" means that project's item 12, "PAY-12" any project's with
// that prefix, "owner/name#12" the named project's. Nothing is returned for
// something that does not exist.
func (s *Store) ResolveRef(ctx context.Context, home Scope, r Reference) (*Item, error) {
	var sc Scope
	switch {
	case r.Repo != "":
		sc = Scope{Kind: ScopeProject, ID: r.Repo}
	case r.Prefix != "":
		var err error
		if sc, err = s.ScopeOfPrefix(ctx, r.Prefix); err != nil {
			return nil, err
		}
	default:
		sc = home
	}
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM work_items WHERE scope_kind = ? AND scope_id = ? AND number = ?`, sc.Kind, sc.ID, r.Number).Scan(&id)
	if isNoRows(err) {
		return nil, notFound("no item " + r.Ref)
	}
	if err != nil {
		return nil, err
	}
	return s.load(ctx, s.db, id)
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
