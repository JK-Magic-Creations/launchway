package work

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
)

// checkClass refuses an agent whose declared class the level's mapping does
// not allow for the item's difficulty.
func (s *Store) checkClass(ctx context.Context, a *Agent, it *Item) error {
	if it.Difficulty == "" {
		return nil
	}
	eff, err := s.settingsCached(ctx, it.Scope)
	if err != nil {
		return err
	}
	allowed, ok := eff.Classes[it.Difficulty]
	if !ok {
		return nil
	}
	if !slices.Contains(allowed, a.Class) {
		return forbidden("%s is %s work, for agents of class %s; agent %s is class %q", it.Ref, it.Difficulty, strings.Join(allowed, " or "), a.Name, a.Class)
	}
	return nil
}

// Verdict is the structured result an agent's review must carry.
type Verdict struct {
	Decision   string          `json:"decision"` // approve, request_changes or comment
	Criteria   []CriterionEvid `json:"criteria"` // each criterion checked, with the evidence
	Risks      []string        `json:"risks"`
	ScopeOK    bool            `json:"scope_ok"`
	Confidence float64         `json:"confidence"` // 0 to 1
	Summary    string          `json:"summary"`
}

// CriterionEvid is one criterion an agent checked and what it found.
type CriterionEvid struct {
	Criterion string `json:"criterion"`
	Met       bool   `json:"met"`
	Evidence  string `json:"evidence"`
}

// AgentReview is a stored review by an agent.
type AgentReview struct {
	ID      string  `json:"id"`
	Repo    string  `json:"repo"`
	Pull    string  `json:"pull"`
	Agent   string  `json:"agent"`
	Verdict Verdict `json:"verdict"`
	// Counts says whether this review counts toward the required approvals.
	// When it does not, it is advice and shown as advice, with Advice saying why.
	Counts bool   `json:"counts_as_approval"`
	Advice string `json:"advice,omitempty"`
	At     string `json:"at"`
}

// ReviewInput is what the server learned about a pull request when an agent
// reviews it.
type ReviewInput struct {
	Change       AgentChange // the pull request's files, size, checks and its item
	Repo, Pull   string
	AuthorAgents []string // registered agents that opened it or authored or pushed any commit in it
	ItemIDs      []string // items the pull request is linked to
	Verdict      Verdict
}

func (v *Verdict) check() error {
	switch v.Decision {
	case "approve", "request_changes", "comment":
	default:
		return invalid("a verdict's decision is approve, request_changes or comment")
	}
	if len(v.Criteria) == 0 || len(v.Criteria) > 200 {
		return invalid("a verdict lists the criteria it checked, each with evidence")
	}
	for i := range v.Criteria {
		v.Criteria[i].Criterion = cleanTitle(v.Criteria[i].Criterion)
		v.Criteria[i].Evidence = cleanBody(v.Criteria[i].Evidence)
		if v.Criteria[i].Criterion == "" || strings.TrimSpace(v.Criteria[i].Evidence) == "" {
			return invalid("every criterion in a verdict needs its text and the evidence")
		}
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return invalid("confidence is between 0 and 1")
	}
	if len(v.Risks) > 50 {
		return invalid("at most 50 risks")
	}
	for i := range v.Risks {
		v.Risks[i] = cleanTitle(v.Risks[i])
	}
	v.Summary = cleanBody(v.Summary)
	return nil
}

// SubmitAgentReview stores an agent's review of a pull request. Whether it
// counts as an approval is decided here, by the policy and by independence:
// the reviewer is a registered agent other than any that wrote the change or
// holds or held the work; it is on a different model when the policy says so;
// the change is of a kind the policy lets an agent approve; and nothing on
// the always-a-person list is touched. Otherwise it is stored as advice.
func (s *Store) SubmitAgentReview(ctx context.Context, reviewer *Agent, in ReviewInput) (*AgentReview, error) {
	if err := in.Verdict.check(); err != nil {
		return nil, err
	}
	a, err := s.AgentByName(ctx, reviewer.Name)
	if err != nil {
		return nil, err
	}
	if a.Disabled {
		return nil, forbidden("agent %s is switched off", a.Name)
	}
	if !s.AgentCovers(a, in.Change.Scope) {
		return nil, forbidden("agent %s does not work in %s", a.Name, in.Change.Scope)
	}
	var advice []string
	if slices.Contains(in.AuthorAgents, a.Name) {
		return nil, forbidden("an agent never reviews a pull request it opened or pushed to")
	}
	// Whoever holds or held the lease of a linked item is not independent.
	for _, id := range in.ItemIDs {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_agent_log WHERE agent = ? AND item_id = ? AND action IN ('claim','review')`, a.Name, id).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, forbidden("agent %s held the work this pull request is for, so it is not independent of it", a.Name)
		}
		if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_claims WHERE item_id = ? AND agent = ?`, id, a.Name).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, forbidden("agent %s holds the work this pull request is for", a.Name)
		}
	}
	eff, err := s.Settings(ctx, in.Change.Scope)
	if err != nil {
		return nil, err
	}
	pol := &AgentReviewPolicy{Mode: ReviewHumanOnly}
	if eff.Agents != nil && eff.Agents.Review != nil {
		pol = eff.Agents.Review
	}
	counts := in.Verdict.Decision == "approve"
	no := func(f string, a ...any) { counts = false; advice = append(advice, fmt.Sprintf(f, a...)) }
	if !eff.Agents.AgentsEnabled() {
		no("agents are switched off here")
	}
	if pol.Mode != ReviewIndependent {
		no("the policy here is that a person reviews; this is advice")
	}
	if !in.Verdict.ScopeOK {
		no("the reviewer judged the change out of scope")
	}
	if pol.RequireDifferentModel {
		if a.Model == "" {
			no("the policy needs a different model, and this agent declares none")
		}
		for _, name := range in.AuthorAgents {
			au, err := s.AgentByName(ctx, name)
			if err != nil {
				continue
			}
			if au.Model == "" || strings.EqualFold(au.Model, a.Model) {
				no("the policy needs a different model than the author's (%s); this agent declares %q", name, a.Model)
			}
		}
	}
	for _, f := range in.Change.Files {
		for _, p := range alwaysHuman {
			if matchGlob(p, f.Path) {
				no("%s is on the list of files only a person approves", f.Path)
			}
		}
		if f.Deleted && IsTestPath(f.Path) {
			no("the change deletes a test: only a person approves that")
		}
		listed := false
		for _, p := range pol.Paths {
			if matchGlob(p, f.Path) {
				listed = true
			}
		}
		if !listed {
			no("%s is not among the paths the policy lets an agent approve", f.Path)
		}
	}
	if in.Change.TestsSkipped {
		no("the change skips a test: only a person approves that")
	}
	maxFiles, maxLines := 10, 200
	if pol.MaxFiles > 0 {
		maxFiles = pol.MaxFiles
	}
	if pol.MaxLines > 0 {
		maxLines = pol.MaxLines
	}
	if len(in.Change.Files) == 0 || len(in.Change.Files) > maxFiles || in.Change.Lines > maxLines {
		no("the change is outside the size the policy lets an agent approve (%d files, %d lines)", maxFiles, maxLines)
	}
	if len(pol.Types) > 0 && !slices.Contains(pol.Types, in.Change.ItemType) {
		no("the policy lets an agent approve only %s work", strings.Join(pol.Types, ", "))
	}
	detail, _ := json.Marshal(in.Verdict)
	rv := &AgentReview{ID: newID("war"), Repo: in.Repo, Pull: in.Pull, Agent: a.Name, Verdict: in.Verdict, Counts: counts, Advice: strings.Join(advice, "; ")}
	now := ms(s.now())
	rv.At = iso(now)
	if _, err := s.db.Exec(ctx, `INSERT INTO work_agent_reviews(id, scope_kind, scope_id, repo, pull, agent, verdict, counts, detail, advice, at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		rv.ID, in.Change.Scope.Kind, in.Change.Scope.ID, in.Repo, in.Pull, a.Name, in.Verdict.Decision, boolInt(counts), string(detail), rv.Advice, now); err != nil {
		return nil, err
	}
	_ = s.logAgent(ctx, s.db, a.Name, "", "pull_review", in.Repo+"#"+in.Pull+" "+in.Verdict.Decision)
	return rv, nil
}

// AgentReviews lists the agents' reviews of a pull request, newest first.
func (s *Store) AgentReviews(ctx context.Context, repo, pull string) ([]AgentReview, error) {
	rows, err := s.db.Query(ctx, `SELECT id, repo, pull, agent, counts, detail, advice, at FROM work_agent_reviews WHERE repo = ? AND pull = ? ORDER BY at DESC, id DESC`, repo, pull)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentReview{}
	for rows.Next() {
		var r AgentReview
		var counts int
		var detail string
		var at int64
		if err := rows.Scan(&r.ID, &r.Repo, &r.Pull, &r.Agent, &counts, &detail, &r.Advice, &at); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(detail), &r.Verdict)
		r.Counts, r.At = counts == 1, iso(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// countedAgentApprovals is how many independent agent approvals count for a
// pull request, given the effective policy for it. A request for changes by
// any agent withdraws them.
func (s *Store) countedAgentApprovals(ctx context.Context, repo, pull string) (int, error) {
	var n, changes int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(DISTINCT agent) FROM work_agent_reviews WHERE repo = ? AND pull = ? AND counts = 1 AND verdict = 'approve'`, repo, pull).Scan(&n); err != nil {
		return 0, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_agent_reviews WHERE repo = ? AND pull = ? AND verdict = 'request_changes'`, repo, pull).Scan(&changes); err != nil {
		return 0, err
	}
	if changes > 0 {
		return 0, nil
	}
	return n, nil
}

// ---- human audit sampling ----

// Sample is a merge an agent approved that a person should look at afterwards.
type Sample struct {
	ID         string `json:"id"`
	Scope      Scope  `json:"scope"`
	Repo       string `json:"repo"`
	Pull       string `json:"pull"`
	Agent      string `json:"agent,omitempty"`
	FlaggedAt  string `json:"flagged_at"`
	ReviewedBy string `json:"reviewed_by,omitempty"`
	ReviewedAt string `json:"reviewed_at,omitempty"`
	Note       string `json:"note,omitempty"`
}

// SampleMerge is called when a pull request that an agent approved is merged.
// It flags the merge for a person's later look with the policy's probability
// (a stable choice per pull request, so asking twice gives the same answer).
func (s *Store) SampleMerge(ctx context.Context, sc Scope, repo, pull string) (bool, error) {
	eff, err := s.Settings(ctx, sc)
	if err != nil {
		return false, err
	}
	pct := 0
	if eff.Agents != nil && eff.Agents.Review != nil {
		pct = eff.Agents.Review.SamplePercent
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(repo + "#" + pull))
	if pct <= 0 || int(h.Sum32()%100) >= pct {
		return false, nil
	}
	var agent string
	_ = s.db.QueryRow(ctx, `SELECT agent FROM work_agent_reviews WHERE repo = ? AND pull = ? AND counts = 1 ORDER BY at LIMIT 1`, repo, pull).Scan(&agent)
	_, err = s.db.Exec(ctx, `INSERT INTO work_review_samples(id, scope_kind, scope_id, repo, pull, agent, flagged_at)
		SELECT ?,?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM work_review_samples WHERE repo = ? AND pull = ?)`, newID("wsm"), sc.Kind, sc.ID, repo, pull, agent, ms(s.now()), repo, pull)
	return err == nil, err
}

// Samples lists the flagged merges in the scopes the caller maintains.
func (s *Store) Samples(ctx context.Context, c Caller, pendingOnly bool) ([]Sample, error) {
	q := `SELECT id, scope_kind, scope_id, repo, pull, agent, flagged_at, reviewed_by, reviewed_at, note FROM work_review_samples`
	if pendingOnly {
		q += ` WHERE reviewed_at = 0`
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY flagged_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sample{}
	for rows.Next() {
		var x Sample
		var f, r int64
		if err := rows.Scan(&x.ID, &x.Scope.Kind, &x.Scope.ID, &x.Repo, &x.Pull, &x.Agent, &f, &x.ReviewedBy, &r, &x.Note); err != nil {
			return nil, err
		}
		x.FlaggedAt, x.ReviewedAt = iso(f), iso(r)
		if c.isMaintainer(x.Scope) || c.Kind == KindSystem {
			out = append(out, x)
		}
	}
	return out, rows.Err()
}

// CompleteSample records that a person looked at a flagged merge.
func (s *Store) CompleteSample(ctx context.Context, c Caller, id, note string) error {
	var sc Scope
	if err := s.db.QueryRow(ctx, `SELECT scope_kind, scope_id FROM work_review_samples WHERE id = ?`, id).Scan(&sc.Kind, &sc.ID); err != nil {
		return notFound("no such sample")
	}
	if !c.Human() || !c.isMaintainer(sc) {
		return forbidden("a person who maintains %s reviews a sample", sc)
	}
	_, err := s.db.Exec(ctx, `UPDATE work_review_samples SET reviewed_by = ?, reviewed_at = ?, note = ? WHERE id = ?`, c.Name, ms(s.now()), cleanTitle(note), id)
	return err
}

// ---- spend ----

// SpendRow is a budget, what was spent and what remains.
type SpendRow struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"` // agent, project or item
	BudgetCents     int64  `json:"budget_cents,omitempty"`
	SpentCents      int64  `json:"spent_cents"`
	RemainingCents  int64  `json:"remaining_cents,omitempty"`
	BudgetTokens    int64  `json:"budget_tokens,omitempty"`
	SpentTokens     int64  `json:"spent_tokens"`
	RemainingTokens int64  `json:"remaining_tokens,omitempty"`
	Seconds         int64  `json:"seconds"`
	Period          string `json:"period,omitempty"`
}

func remaining(budget, spent int64) int64 {
	if budget <= 0 {
		return 0
	}
	return max(budget-spent, 0)
}

// Spend reports what agents reported spending: per agent over the last 24 hours
// against their daily budget, per project over all time, and the costliest items
// (item budgets come from the agent that worked them).
func (s *Store) Spend(ctx context.Context, c Caller) ([]SpendRow, error) {
	var out []SpendRow
	agents, err := s.Agents(ctx, c)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		out = append(out, SpendRow{Name: a.Name, Kind: "agent", Period: "24h", BudgetCents: a.BudgetCentsDay, SpentCents: a.UsedCentsDay, RemainingCents: remaining(a.BudgetCentsDay, a.UsedCentsDay),
			BudgetTokens: a.BudgetTokensDay, SpentTokens: a.UsedTokensDay, RemainingTokens: remaining(a.BudgetTokensDay, a.UsedTokensDay)})
	}
	rows, err := s.db.Query(ctx, `SELECT i.scope_kind, i.scope_id, COALESCE(SUM(u.cents),0), COALESCE(SUM(u.tokens),0), COALESCE(SUM(u.seconds),0) FROM work_usage u JOIN work_items i ON i.id = u.item_id GROUP BY i.scope_kind, i.scope_id ORDER BY 3 DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sc Scope
		var cents, tokens, secs int64
		if err := rows.Scan(&sc.Kind, &sc.ID, &cents, &tokens, &secs); err != nil {
			rows.Close()
			return nil, err
		}
		if c.canRead(sc) {
			out = append(out, SpendRow{Name: sc.String(), Kind: "project", Period: "all time", SpentCents: cents, SpentTokens: tokens, Seconds: secs})
		}
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT u.item_id, COALESCE(SUM(u.cents),0), COALESCE(SUM(u.tokens),0), COALESCE(SUM(u.seconds),0), MAX(u.agent) FROM work_usage u GROUP BY u.item_id ORDER BY 2 DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	type it struct {
		id      string
		c, t, s int64
		agent   string
	}
	var its []it
	for rows.Next() {
		var x it
		if err := rows.Scan(&x.id, &x.c, &x.t, &x.s, &x.agent); err != nil {
			rows.Close()
			return nil, err
		}
		its = append(its, x)
	}
	rows.Close()
	for _, x := range its {
		item, err := s.load(ctx, s.db, x.id)
		if err != nil || !c.canSee(item) {
			continue
		}
		row := SpendRow{Name: item.Ref, Kind: "item", Period: "all time", SpentCents: x.c, SpentTokens: x.t, Seconds: x.s}
		if a, err := s.AgentByName(ctx, x.agent); err == nil {
			row.BudgetCents, row.BudgetTokens = a.ItemCents, a.ItemTokens
			row.RemainingCents, row.RemainingTokens = remaining(a.ItemCents, x.c), remaining(a.ItemTokens, x.t)
		}
		out = append(out, row)
	}
	return out, nil
}
