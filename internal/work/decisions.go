package work

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// Decision records: a work item of type decision is a question, the options, a recommendation and what it blocks (its
// body and its links), and, once a person answers it, the answer with who gave it and when. A decision is proposed
// until it is accepted; an accepted decision may be superseded by another accepted decision, which is then named on
// it. What a decision decides is tied to it: items by a decides link, pull requests by a pull code link. Accepted
// and superseded decisions can be written out as Markdown files for a repository's docs folder (ExportDecisions).

// Decision statuses.
const (
	DecisionProposed   = "proposed"
	DecisionAccepted   = "accepted"
	DecisionSuperseded = "superseded"
)

// DecisionStatuses are the statuses, in the order a decision passes through them.
var DecisionStatuses = []string{DecisionProposed, DecisionAccepted, DecisionSuperseded}

// MaxAnswer bounds an answer's text.
const MaxAnswer = 4000

// DecisionTemplate is the body a new decision starts from.
const DecisionTemplate = "## Question\n\n## Options\n\n- \n\n## Recommendation\n\n## Consequences\n"

// DecidedItem is an item a decision decides, or blocks.
type DecidedItem struct {
	Ref   string `json:"ref"`
	Title string `json:"title"`
	State string `json:"state"`
}

// DecidedPull is a pull request a decision decides.
type DecidedPull struct {
	Repo   string `json:"repo"`
	Number string `json:"number"`
	Title  string `json:"title,omitempty"`
	State  string `json:"state,omitempty"`
}

// DecisionView is a decision record.
type DecisionView struct {
	Item         string        `json:"item"`
	Title        string        `json:"title"`
	Status       string        `json:"status"`
	Answer       string        `json:"answer,omitempty"`
	DecidedBy    string        `json:"decided_by,omitempty"`
	DecidedAt    string        `json:"decided_at,omitempty"`
	SupersededBy string        `json:"superseded_by,omitempty"` // the reference of the decision that replaced it
	Supersedes   []string      `json:"supersedes"`              // the references of the decisions this one replaced
	Decides      []DecidedItem `json:"decides"`
	Pulls        []DecidedPull `json:"pulls"`
	Blocks       []DecidedItem `json:"blocks"`
}

func (s *Store) decisionItem(ctx context.Context, c Caller, ref string) (*Item, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if it.Type != TypeDecision {
		return nil, invalid("%s is a %s, not a decision", it.Ref, it.Type)
	}
	return it, nil
}

// Decision reads a decision record.
func (s *Store) Decision(ctx context.Context, c Caller, ref string) (*DecisionView, error) {
	it, err := s.decisionItem(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	v := &DecisionView{Item: it.Ref, Title: it.Title, Status: DecisionProposed, Supersedes: []string{}, Decides: []DecidedItem{}, Pulls: []DecidedPull{}, Blocks: []DecidedItem{}}
	row, ok, err := store.DecisionGet(ctx, s.db, it.ID)
	if err != nil {
		return nil, err
	}
	if ok {
		v.Status, v.Answer, v.DecidedBy, v.DecidedAt = row.Status, row.Answer, row.DecidedBy, iso(row.DecidedAt)
		if row.SupersededBy != "" {
			v.SupersededBy = s.refOf(ctx, s.db, row.SupersededBy)
		}
	}
	older, err := store.DecisionsSuperseded(ctx, s.db, it.ID)
	if err != nil {
		return nil, err
	}
	for _, id := range older {
		if o, err := s.load(ctx, s.db, id); err == nil && c.canSee(o) {
			v.Supersedes = append(v.Supersedes, o.Ref)
		}
	}
	links, err := s.Links(ctx, c, it.Ref)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.From != it.Ref {
			continue
		}
		switch l.Kind {
		case LinkDecides:
			v.Decides = append(v.Decides, DecidedItem{Ref: l.To, Title: l.ToTitle, State: l.ToState})
		case LinkBlocks:
			v.Blocks = append(v.Blocks, DecidedItem{Ref: l.To, Title: l.ToTitle, State: l.ToState})
		}
	}
	code, err := s.CodeLinks(ctx, c, it.Ref)
	if err != nil {
		return nil, err
	}
	for _, l := range code {
		if l.Kind == CodePull {
			v.Pulls = append(v.Pulls, DecidedPull{Repo: l.Repo, Number: l.Ref, Title: l.Title, State: l.State})
		}
	}
	return v, nil
}

// AcceptDecision records the answer of a proposed decision: a person who may change work at its level says what was
// decided. The item is then finished (its state moves to done), and the answer, the person and the time are kept.
func (s *Store) AcceptDecision(ctx context.Context, c Caller, ref, answer string) (*DecisionView, error) {
	it, err := s.decisionItem(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if c.Kind != KindPerson || c.Agent != nil {
		return nil, forbidden("only a person answers a decision")
	}
	if !c.canWrite(it.Scope) {
		return nil, forbidden("you may not change work in %s", it.Scope)
	}
	answer = strings.TrimSpace(cleanBody(answer))
	if answer == "" || len(answer) > MaxAnswer {
		return nil, invalid("an answer is 1 to %d bytes: what was decided", MaxAnswer)
	}
	err = s.tx(ctx, func(tx DB) error {
		now := ms(s.now())
		added, err := store.DecisionAccept(ctx, tx, it.ID, answer, c.Name, now)
		if err != nil {
			return err
		}
		if !added {
			return conflict("%s is already decided", it.Ref)
		}
		if err := store.WorkItemFinish(ctx, tx, it.ID, now); err != nil {
			return err
		}
		return s.event(ctx, tx, it.ID, c.Actor, "decision_accepted", 0, map[string]any{"answer": clip(answer, 200)})
	})
	if err != nil {
		return nil, err
	}
	s.invalidate()
	return s.Decision(ctx, c, ref)
}

// SupersedeDecision says an accepted decision is replaced by another accepted decision. A decision is never replaced
// by itself, nor by one that it (through a chain of replacements) replaced.
func (s *Store) SupersedeDecision(ctx context.Context, c Caller, ref, byRef string) (*DecisionView, error) {
	it, err := s.decisionItem(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	by, err := s.decisionItem(ctx, c, byRef)
	if err != nil {
		return nil, err
	}
	if c.Kind != KindPerson || c.Agent != nil {
		return nil, forbidden("only a person supersedes a decision")
	}
	if !c.canWrite(it.Scope) {
		return nil, forbidden("you may not change work in %s", it.Scope)
	}
	if it.ID == by.ID {
		return nil, invalid("a decision cannot replace itself")
	}
	// The replacement must stand: accepted, and not itself replaced (which would also close a loop).
	row, ok, err := store.DecisionGet(ctx, s.db, by.ID)
	if err != nil {
		return nil, err
	}
	if !ok || row.Status != DecisionAccepted {
		return nil, invalid("%s must be accepted before it can replace %s", by.Ref, it.Ref)
	}
	err = s.tx(ctx, func(tx DB) error {
		changed, err := store.DecisionSupersede(ctx, tx, it.ID, by.ID, ms(s.now()))
		if err != nil {
			return err
		}
		if !changed {
			return conflict("%s is not an accepted decision", it.Ref)
		}
		return s.event(ctx, tx, it.ID, c.Actor, "decision_superseded", 0, map[string]any{"by": by.Ref})
	})
	if err != nil {
		return nil, err
	}
	s.invalidate()
	return s.Decision(ctx, c, ref)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// DecisionRow is a decision in a list.
type DecisionRow struct {
	Item      string `json:"item"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Answer    string `json:"answer,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`
	// SupersededBy is the reference of the decision that replaced it.
	SupersededBy string `json:"superseded_by,omitempty"`
}

// ListDecisions lists the decision records the caller may see, at one level or everywhere, for one repository (its own
// work and work tied to its code) and one status when given.
func (s *Store) ListDecisions(ctx context.Context, c Caller, scope *Scope, repository, status string) ([]DecisionRow, error) {
	if status != "" && !slices.Contains(DecisionStatuses, status) {
		return nil, invalid("a status is proposed, accepted or superseded")
	}
	var only []Scope
	if scope != nil {
		only = append(only, *scope)
	}
	vis, args, err := s.visibility(ctx, c, only)
	if err != nil {
		return nil, err
	}
	rows, err := store.DecisionsList(ctx, s.db, vis, args, repository, status, 1000)
	if err != nil {
		return nil, err
	}
	out := make([]DecisionRow, 0, len(rows))
	prefixes := map[Scope]string{}
	for _, r := range rows {
		sc := Scope{Kind: r.ScopeKind, ID: r.ScopeID}
		p, ok := prefixes[sc]
		if !ok {
			if p, err = s.prefixIn(ctx, s.db, sc); err != nil {
				return nil, err
			}
			prefixes[sc] = p
		}
		d := DecisionRow{Item: fmt.Sprintf("%s-%d", p, r.Number), Title: r.Title, Status: r.Status, Answer: r.Answer, DecidedBy: r.DecidedBy, DecidedAt: iso(r.DecidedAt)}
		if r.SupersededBy != "" {
			d.SupersededBy = s.refOf(ctx, s.db, r.SupersededBy)
		}
		out = append(out, d)
	}
	return out, nil
}

// ExportFile is one Markdown file of an export.
type ExportFile struct {
	Path    string
	Content string
	Item    string
}

// slug makes a short file name part from a title: lower-case words joined by dashes.
func slug(title string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(title) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 50 {
		out = strings.Trim(out[:50], "-")
	}
	if out == "" {
		out = "decision"
	}
	return out
}

// ExportDecisions writes the accepted and the superseded decisions the caller may see for a repository as Markdown
// files, one each, named KEY-slug.md under folder. A proposed decision is not a record yet and is left out.
func (s *Store) ExportDecisions(ctx context.Context, c Caller, repository, folder string) ([]ExportFile, error) {
	var out []ExportFile
	for _, status := range []string{DecisionAccepted, DecisionSuperseded} {
		rows, err := s.ListDecisions(ctx, c, nil, repository, status)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			it, err := s.Get(ctx, c, r.Item)
			if err != nil {
				continue
			}
			v, err := s.Decision(ctx, c, r.Item)
			if err != nil {
				continue
			}
			out = append(out, ExportFile{Path: path.Join(folder, strings.ToLower(it.Ref)+"-"+slug(it.Title)+".md"), Content: decisionMarkdown(it, v), Item: it.Ref})
		}
	}
	return out, nil
}

// decisionMarkdown writes one decision record. The first line is the title and the second the status line, so that a
// reader of the file sees what the record is before anything else.
func decisionMarkdown(it *Item, v *DecisionView) string {
	var b strings.Builder
	oneLine := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	fmt.Fprintf(&b, "# %s: %s\n\n", it.Ref, oneLine(it.Title))
	status := strings.ToUpper(v.Status[:1]) + v.Status[1:]
	if v.Status == DecisionSuperseded && v.SupersededBy != "" {
		status += " by " + v.SupersededBy
	}
	fmt.Fprintf(&b, "Status: %s\n", status)
	if v.DecidedAt != "" {
		fmt.Fprintf(&b, "Decided: %s by %s\n", v.DecidedAt[:10], oneLine(v.DecidedBy))
	}
	if len(v.Supersedes) > 0 {
		fmt.Fprintf(&b, "Supersedes: %s\n", strings.Join(v.Supersedes, ", "))
	}
	b.WriteString("\n")
	if v.Answer != "" {
		fmt.Fprintf(&b, "## Decision\n\n%s\n\n", strings.TrimSpace(v.Answer))
	}
	if body := strings.TrimSpace(it.Body); body != "" {
		fmt.Fprintf(&b, "%s\n\n", body)
	}
	list := func(heading string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n", heading)
		for _, i := range items {
			fmt.Fprintf(&b, "- %s\n", i)
		}
		b.WriteString("\n")
	}
	var decides, blocks, pulls []string
	for _, d := range v.Decides {
		decides = append(decides, d.Ref+" "+oneLine(d.Title))
	}
	for _, d := range v.Blocks {
		blocks = append(blocks, d.Ref+" "+oneLine(d.Title))
	}
	for _, p := range v.Pulls {
		pulls = append(pulls, fmt.Sprintf("%s#%s %s", p.Repo, p.Number, oneLine(p.Title)))
	}
	list("Decides", decides)
	list("Pull requests", pulls)
	list("Blocked until decided", blocks)
	return strings.TrimRight(b.String(), "\n") + "\n"
}
