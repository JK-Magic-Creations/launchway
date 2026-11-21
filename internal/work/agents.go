package work

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"time"
)

// An agent is a registered non-human principal that may be assigned work. It
// has its own token, a set of projects it may touch, the types and labels it
// accepts, a limit on how much it holds at once and per day, and a person
// accountable for it. What it may do with Git is decided elsewhere, by the same
// branch rules and reviews everyone meets; the server's hard rules about
// agents are listed in docs/guide/agents.md and tested in agents_test.go.

// TokenPrefix starts every agent token.
const TokenPrefix = "lwa_"

// Agent is a registered agent.
type Agent struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Owner       string   `json:"owner"` // the person accountable for it
	Scope       Scope    `json:"scope"` // the level it is registered at
	Projects    []string `json:"projects"`
	Types       []string `json:"types,omitempty"`
	Labels      []string `json:"labels,omitempty"` // pools it works from: agent:docs
	Concurrency int      `json:"concurrency"`
	PerDay      int      `json:"per_day"`
	MayMerge    bool     `json:"may_merge"`
	// Model and Class are declared by whoever registers the agent: which model
	// family it runs on, and how large a job it takes. The server compares
	// models when a policy asks for a different model, and routes work by class.
	Model string `json:"model,omitempty"`
	Class string `json:"class,omitempty"`
	// Budgets reported by the agent and enforced as counters (0 is no limit).
	BudgetTokensDay int64  `json:"budget_tokens_day,omitempty"`
	BudgetCentsDay  int64  `json:"budget_cents_day,omitempty"`
	ItemTokens      int64  `json:"item_tokens,omitempty"`
	ItemCents       int64  `json:"item_cents,omitempty"`
	ItemSeconds     int64  `json:"item_seconds,omitempty"`
	UsedTokensDay   int64  `json:"used_tokens_day"`
	UsedCentsDay    int64  `json:"used_cents_day"`
	Disabled        bool   `json:"disabled"`
	TokenHint       string `json:"token_hint,omitempty"`
	TokenAt         string `json:"token_at,omitempty"`
	LastSeen        string `json:"last_seen,omitempty"`
	CreatedAt       string `json:"created_at"`
	Active          int    `json:"active"`    // items it holds now
	Today           int    `json:"today"`     // items it took in the last 24 hours
	Queued          int    `json:"queued"`    // items waiting for it
	Review          int    `json:"in_review"` // items it handed over that wait for a person
}

// NewAgent registers an agent.
type NewAgent struct {
	Model           string   `json:"model"`
	Class           string   `json:"class"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Scope           Scope    `json:"scope"`
	Projects        []string `json:"projects"`
	Types           []string `json:"types"`
	Labels          []string `json:"labels"`
	Concurrency     int      `json:"concurrency"`
	PerDay          int      `json:"per_day"`
	MayMerge        bool     `json:"may_merge"`
	BudgetTokensDay int64    `json:"budget_tokens_day"`
	BudgetCentsDay  int64    `json:"budget_cents_day"`
	ItemTokens      int64    `json:"item_tokens"`
	ItemCents       int64    `json:"item_cents"`
	ItemSeconds     int64    `json:"item_seconds"`
}

// AgentPatch changes an agent. Nil is left alone.
type AgentPatch struct {
	Model           *string   `json:"model"`
	Class           *string   `json:"class"`
	Description     *string   `json:"description"`
	Projects        *[]string `json:"projects"`
	Types           *[]string `json:"types"`
	Labels          *[]string `json:"labels"`
	Concurrency     *int      `json:"concurrency"`
	PerDay          *int      `json:"per_day"`
	Disabled        *bool     `json:"disabled"`
	MayMerge        *bool     `json:"may_merge"`
	BudgetTokensDay *int64    `json:"budget_tokens_day"`
	BudgetCentsDay  *int64    `json:"budget_cents_day"`
	ItemTokens      *int64    `json:"item_tokens"`
	ItemCents       *int64    `json:"item_cents"`
	ItemSeconds     *int64    `json:"item_seconds"`
	Owner           *string   `json:"owner"`
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return TokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])), nil
}

func checkNames(what string, in []string, limit int) ([]string, error) {
	out := []string{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > 200 {
			return nil, invalid("%s: each entry is 1 to 200 characters", what)
		}
		if _, err := path.Match(v, ""); err != nil {
			return nil, invalid("%s: %q is not a valid pattern", what, v)
		}
		out = append(out, v)
	}
	if len(out) > limit {
		return nil, invalid("%s: at most %d entries", what, limit)
	}
	return out, nil
}

// CreateAgent registers an agent and returns its token, which is shown once.
// The caller must maintain the level the agent is registered at.
func (s *Store) CreateAgent(ctx context.Context, c Caller, in NewAgent) (*Agent, string, error) {
	if err := in.Scope.Validate(); err != nil {
		return nil, "", err
	}
	if c.Agent != nil || !c.Human() || !c.isMaintainer(in.Scope) {
		return nil, "", forbidden("only a person who maintains %s registers an agent there", in.Scope)
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if len(name) < 2 || len(name) > 40 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return nil, "", invalid("an agent's name is 2 to 40 characters: letters, digits, - and _")
	}
	projects, err := checkNames("projects", in.Projects, 100)
	if err != nil {
		return nil, "", err
	}
	if len(projects) == 0 {
		return nil, "", invalid("an agent names the projects it may work in; there is no default of all of them")
	}
	if slices.Contains(projects, "*") && in.Scope.Kind != ScopeInstallation {
		return nil, "", invalid("only an agent registered at Base may work in every project")
	}
	types, err := checkNames("types", in.Types, 10)
	if err != nil {
		return nil, "", err
	}
	for _, t := range types {
		if !slices.Contains(Types, t) {
			return nil, "", invalid("%q is not an item type", t)
		}
	}
	labels, err := checkNames("labels", in.Labels, 20)
	if err != nil {
		return nil, "", err
	}
	if in.Concurrency <= 0 {
		in.Concurrency = 1
	}
	if in.PerDay <= 0 {
		in.PerDay = 10
	}
	if in.Concurrency > 20 || in.PerDay > 500 {
		return nil, "", invalid("at most 20 items at once and 500 a day")
	}
	if !slices.Contains([]string{"", "small", "medium", "large"}, in.Class) {
		return nil, "", invalid("class is small, medium or large")
	}
	tok, err := newToken()
	if err != nil {
		return nil, "", err
	}
	pj, _ := json.Marshal(projects)
	tj, _ := json.Marshal(types)
	lj, _ := json.Marshal(labels)
	id := newID("wag")
	err = s.tx(ctx, func(tx DB) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_agents WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return conflict("there is already an agent called %s", name)
		}
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_assignees WHERE who = ? AND kind = 'person'`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return conflict("%s is already the name of a person on some work; pick another name", name)
		}
		now := ms(s.now())
		_, err := tx.Exec(ctx, `INSERT INTO work_agents(id, name, scope_kind, scope_id, description, owner, projects, types, labels, concurrency, per_day, may_merge, model, class, budget_tokens_day, budget_cents_day, item_tokens, item_cents, item_seconds, disabled, token_hash, token_hint, token_at, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?,?,?)`,
			id, name, in.Scope.Kind, in.Scope.ID, cleanBody(in.Description), c.Name, string(pj), string(tj), string(lj), in.Concurrency, in.PerDay, boolInt(in.MayMerge), cleanTitle(in.Model), in.Class, in.BudgetTokensDay, in.BudgetCentsDay, in.ItemTokens, in.ItemCents, in.ItemSeconds, hashToken(tok), tok[len(tok)-4:], now, now)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	a, err := s.AgentByName(ctx, name)
	return a, tok, err
}

// RotateAgentToken replaces the agent's token; the old one stops working.
func (s *Store) RotateAgentToken(ctx context.Context, c Caller, name string) (*Agent, string, error) {
	a, err := s.AgentByName(ctx, name)
	if err != nil {
		return nil, "", err
	}
	if c.Agent != nil || !c.Human() || (!c.isMaintainer(a.Scope) && c.Name != a.Owner) {
		return nil, "", forbidden("only the agent's owner or a maintainer of %s rotates its token", a.Scope)
	}
	tok, err := newToken()
	if err != nil {
		return nil, "", err
	}
	if _, err := s.db.Exec(ctx, `UPDATE work_agents SET token_hash = ?, token_hint = ?, token_at = ? WHERE id = ?`, hashToken(tok), tok[len(tok)-4:], ms(s.now()), a.ID); err != nil {
		return nil, "", err
	}
	_ = s.logAgent(ctx, s.db, a.Name, "", "token_rotated", c.Name)
	a, err = s.AgentByName(ctx, name)
	return a, tok, err
}

// applyAgentPatch checks a patch and applies it to the agent.
func applyAgentPatch(a *Agent, p AgentPatch) (err error) {
	if p.Description != nil {
		a.Description = cleanBody(*p.Description)
	}
	if p.Projects != nil {
		if a.Projects, err = checkNames("projects", *p.Projects, 100); err != nil || len(a.Projects) == 0 {
			return invalid("an agent names the projects it may work in")
		}
	}
	if p.Types != nil {
		if a.Types, err = checkNames("types", *p.Types, 10); err != nil {
			return err
		}
	}
	if p.Labels != nil {
		if a.Labels, err = checkNames("labels", *p.Labels, 20); err != nil {
			return err
		}
	}
	if p.Concurrency != nil {
		if *p.Concurrency < 1 || *p.Concurrency > 20 {
			return invalid("concurrency is 1 to 20")
		}
		a.Concurrency = *p.Concurrency
	}
	if p.PerDay != nil {
		if *p.PerDay < 1 || *p.PerDay > 500 {
			return invalid("per_day is 1 to 500")
		}
		a.PerDay = *p.PerDay
	}
	if p.Owner != nil && *p.Owner != "" {
		a.Owner = *p.Owner
	}
	if p.Disabled != nil {
		a.Disabled = *p.Disabled
	}
	if p.MayMerge != nil {
		a.MayMerge = *p.MayMerge
	}
	if p.Model != nil {
		a.Model = cleanTitle(*p.Model)
	}
	if p.Class != nil {
		a.Class = *p.Class
	}
	if !slices.Contains([]string{"", "small", "medium", "large"}, a.Class) {
		return invalid("class is small, medium or large")
	}
	for _, f := range []struct {
		v   *int64
		dst *int64
	}{{p.BudgetTokensDay, &a.BudgetTokensDay}, {p.BudgetCentsDay, &a.BudgetCentsDay}, {p.ItemTokens, &a.ItemTokens}, {p.ItemCents, &a.ItemCents}, {p.ItemSeconds, &a.ItemSeconds}} {
		if f.v != nil {
			if *f.v < 0 {
				return invalid("a budget is zero (no limit) or more")
			}
			*f.dst = *f.v
		}
	}
	return nil
}

// UpdateAgent changes an agent: its limits, its work, the kill switch.
func (s *Store) UpdateAgent(ctx context.Context, c Caller, name string, p AgentPatch) (*Agent, error) {
	a, err := s.AgentByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if c.Agent != nil || !c.Human() || (!c.isMaintainer(a.Scope) && c.Name != a.Owner) {
		return nil, forbidden("only the agent's owner or a maintainer of %s changes it", a.Scope)
	}
	if err := applyAgentPatch(a, p); err != nil {
		return nil, err
	}
	pj, _ := json.Marshal(a.Projects)
	tj, _ := json.Marshal(a.Types)
	lj, _ := json.Marshal(a.Labels)
	dis := 0
	if a.Disabled {
		dis = 1
	}
	err = s.tx(ctx, func(tx DB) error {
		if _, err := tx.Exec(ctx, `UPDATE work_agents SET description = ?, owner = ?, projects = ?, types = ?, labels = ?, concurrency = ?, per_day = ?, may_merge = ?, model = ?, class = ?, budget_tokens_day = ?, budget_cents_day = ?, item_tokens = ?, item_cents = ?, item_seconds = ?, disabled = ? WHERE id = ?`,
			a.Description, a.Owner, string(pj), string(tj), string(lj), a.Concurrency, a.PerDay, boolInt(a.MayMerge), a.Model, a.Class, a.BudgetTokensDay, a.BudgetCentsDay, a.ItemTokens, a.ItemCents, a.ItemSeconds, dis, a.ID); err != nil {
			return err
		}
		if p.Disabled != nil && *p.Disabled {
			// The kill switch: everything it holds goes back at once.
			return s.releaseAll(ctx, tx, c.Actor, a.Name, "the agent was switched off by "+c.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if p.Disabled != nil {
		act := "enabled"
		if *p.Disabled {
			act = "disabled"
		}
		_ = s.logAgent(ctx, s.db, a.Name, "", act, c.Name)
	}
	return s.AgentByName(ctx, name)
}

// DeleteAgent removes an agent; what it held goes back, and its token stops working.
func (s *Store) DeleteAgent(ctx context.Context, c Caller, name string) error {
	a, err := s.AgentByName(ctx, name)
	if err != nil {
		return err
	}
	if c.Agent != nil || !c.Human() || (!c.isMaintainer(a.Scope) && c.Name != a.Owner) {
		return forbidden("only the agent's owner or a maintainer of %s removes it", a.Scope)
	}
	return s.tx(ctx, func(tx DB) error {
		if err := s.releaseAll(ctx, tx, c.Actor, a.Name, "the agent was removed"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM work_assignees WHERE who = ? AND kind = 'agent'`, a.Name); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM work_agents WHERE id = ?`, a.ID)
		return err
	})
}

const agentCols = `id, name, scope_kind, scope_id, description, owner, projects, types, labels, concurrency, per_day, may_merge, model, class, budget_tokens_day, budget_cents_day, item_tokens, item_cents, item_seconds, disabled, token_hint, token_at, last_seen, created_at`

func scanAgent(sc scanner) (*Agent, error) {
	a := &Agent{}
	var pj, tj, lj string
	var merge, dis int
	var tokAt, seen, created int64
	if err := sc.Scan(&a.ID, &a.Name, &a.Scope.Kind, &a.Scope.ID, &a.Description, &a.Owner, &pj, &tj, &lj, &a.Concurrency, &a.PerDay, &merge, &a.Model, &a.Class, &a.BudgetTokensDay, &a.BudgetCentsDay, &a.ItemTokens, &a.ItemCents, &a.ItemSeconds, &dis, &a.TokenHint, &tokAt, &seen, &created); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(pj), &a.Projects)
	_ = json.Unmarshal([]byte(tj), &a.Types)
	_ = json.Unmarshal([]byte(lj), &a.Labels)
	a.MayMerge, a.Disabled = merge == 1, dis == 1
	a.TokenAt, a.LastSeen, a.CreatedAt = iso(tokAt), iso(seen), iso(created)
	return a, nil
}

// AgentByName reads an agent with its counts.
func (s *Store) AgentByName(ctx context.Context, name string) (*Agent, error) {
	a, err := scanAgent(s.db.QueryRow(ctx, `SELECT `+agentCols+` FROM work_agents WHERE name = ?`, name))
	if isNoRows(err) {
		return nil, notFound("no agent " + name)
	}
	if err != nil {
		return nil, err
	}
	return a, s.agentCounts(ctx, a)
}

func (s *Store) agentCounts(ctx context.Context, a *Agent) error {
	now := ms(s.now())
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_claims WHERE agent = ? AND lease_until >= ?`, a.Name, now).Scan(&a.Active); err != nil {
		return err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_agent_log WHERE agent = ? AND action = 'claim' AND at > ?`, a.Name, now-24*3600*1000).Scan(&a.Today); err != nil {
		return err
	}
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(SUM(tokens),0), COALESCE(SUM(cents),0) FROM work_usage WHERE agent = ? AND at > ?`, a.Name, now-24*3600*1000).Scan(&a.UsedTokensDay, &a.UsedCentsDay); err != nil {
		return err
	}
	closed, err := s.closedStates(ctx)
	if err != nil {
		return err
	}
	in, args := inList(closed)
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_assignees x JOIN work_items i ON i.id = x.item_id WHERE x.who = ? AND x.kind = 'agent' AND i.state NOT IN (`+in+`) AND i.state IN ('backlog','ready')`,
		append([]any{a.Name}, args...)...).Scan(&a.Queued); err != nil {
		return err
	}
	return s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_assignees x JOIN work_items i ON i.id = x.item_id WHERE x.who = ? AND x.kind = 'agent' AND i.state = 'in_review'`, a.Name).Scan(&a.Review)
}

// Agents lists the registered agents the caller may see: those of levels they read.
func (s *Store) Agents(ctx context.Context, c Caller) ([]*Agent, error) {
	all, err := queryAll(ctx, s.db, scanAgent, `SELECT `+agentCols+` FROM work_agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := []*Agent{}
	for _, a := range all {
		if !c.canRead(a.Scope) && c.Kind != KindSystem {
			continue
		}
		if err := s.agentCounts(ctx, a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// AgentForToken finds the agent a token belongs to, in constant time per
// stored hash; nil when there is none. A disabled agent is returned, so the
// caller can say why it is refused.
func (s *Store) AgentForToken(ctx context.Context, token string) (*Agent, error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, nil
	}
	h := hashToken(token)
	a, err := scanAgent(s.db.QueryRow(ctx, `SELECT `+agentCols+` FROM work_agents WHERE token_hash = ?`, h))
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored string
	if err := s.db.QueryRow(ctx, `SELECT token_hash FROM work_agents WHERE id = ?`, a.ID).Scan(&stored); err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1 {
		return nil, nil
	}
	now := ms(s.now())
	if now-parseMs(a.LastSeen) > 60_000 {
		_, _ = s.db.Exec(ctx, `UPDATE work_agents SET last_seen = ? WHERE id = ?`, now, a.ID)
	}
	return a, nil
}

func parseMs(iso string) int64 {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// Covers says whether the agent may work in a scope: the project matches one of
// its patterns and lies under the level it was registered at.
func (s *Store) AgentCovers(a *Agent, sc Scope) bool {
	name := sc.ID
	if sc.Kind != ScopeProject {
		name = sc.String()
	}
	matched := false
	for _, p := range a.Projects {
		if ok, _ := path.Match(p, name); ok || p == "*" {
			matched = true
		}
	}
	if !matched {
		return false
	}
	if a.Scope.Kind == ScopeInstallation {
		return true
	}
	for _, x := range s.chain(sc) {
		if x == a.Scope {
			return true
		}
	}
	return false
}

// agentAccepts says whether an item is the kind of work the agent takes.
func agentAccepts(a *Agent, it *Item) bool {
	if len(a.Types) > 0 && !slices.Contains(a.Types, it.Type) {
		return false
	}
	return true
}

func (a *Agent) inPool(it *Item) bool {
	for _, l := range a.Labels {
		if slices.Contains(it.Labels, strings.ToLower(l)) {
			return true
		}
	}
	return false
}

func (s *Store) logAgent(ctx context.Context, db DB, agent, itemID, action, detail string) error {
	_, err := db.Exec(ctx, `INSERT INTO work_agent_log(agent, at, item_id, action, detail) VALUES(?,?,?,?,?)`, agent, ms(s.now()), itemID, action, detail)
	return err
}

// levelEnabled says whether agents may work under a scope's settings (the
// kill switch of a level).
func (s *Store) levelEnabled(ctx context.Context, sc Scope) (bool, *Effective, error) {
	eff, err := s.Settings(ctx, sc)
	if err != nil {
		return false, nil, err
	}
	return eff.Agents.AgentsEnabled(), eff, nil
}

// checkAgentMayTake is the check made when someone assigns an item to an agent.
func (s *Store) checkAgentMayTake(ctx context.Context, tx DB, name string, it *Item, c Caller) error {
	if c.Agent != nil || !c.Human() {
		return forbidden("an agent does not assign work to agents; a person does")
	}
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentCols+` FROM work_agents WHERE name = ?`, name))
	if err != nil {
		return notFound("no agent " + name)
	}
	if a.Disabled {
		return conflict("agent %s is switched off; switch it on before assigning it work", name)
	}
	if !s.AgentCovers(a, it.Scope) {
		return invalid("agent %s may not work in %s (it works in %s)", name, it.Scope, strings.Join(a.Projects, ", "))
	}
	if !agentAccepts(a, it) {
		return invalid("agent %s takes %s, not %s", name, strings.Join(a.Types, ", "), it.Type)
	}
	if it.Confidential {
		return invalid("confidential work is not assigned to agents")
	}
	if ok, _, err := s.levelEnabled(ctx, it.Scope); err != nil {
		return err
	} else if !ok {
		return conflict("agents are switched off for %s", it.Scope)
	}
	return nil
}

// agentMayEdit limits what an agent may change on an item: only what it holds,
// and not who is assigned, how it is ranked or planned, or whether it is done.
func (s *Store) agentMayEdit(ctx context.Context, c Caller, cur *Item, p Patch) error {
	if cur.Claim == nil || cur.Claim.Agent != c.Agent.Name || cur.Claim.Expired {
		return forbidden("an agent changes only the work it holds; claim %s first", cur.Ref)
	}
	if p.Assignees != nil || len(p.AddAssignees) > 0 || len(p.RemoveAssign) > 0 || p.Confidential != nil || p.Parent != nil || p.Milestone != nil || p.Sprint != nil ||
		p.Type != nil || p.Difficulty != nil || p.Priority != nil || p.DueAt != nil || p.StartAt != nil || p.Estimate != nil {
		return forbidden("an agent does not change who is assigned, or how work is planned, ranked or classified; say so in a comment")
	}
	if p.State != nil && *p.State != "in_progress" && *p.State != "in_review" && *p.State != "ready" {
		return forbidden("an agent moves work only to in progress, in review, or hands it back")
	}
	return nil
}
