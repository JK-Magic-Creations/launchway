package work

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// ChangedFile is a file an agent's pull request changes.
type ChangedFile struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted,omitempty"`
}

// AgentChange describes an agent's pull request for the merge gate.
type AgentChange struct {
	Scope          Scope
	Agent          string
	ItemType       string
	Files          []ChangedFile
	Lines          int    // lines added and removed
	TestsSkipped   bool   // the diff adds a skipped test
	ChecksPassed   bool   // every required check passed
	ChecksRequired int    // how many checks are required; auto-merge needs at least one
	ChecksSkipped  bool   // a required check was skipped or has not run
	HumanApproved  bool   // a person other than the author approved it
	Repo, Pull     string // the pull request, to find the agent reviews that count
}

// Decision is the gate's answer. It advises and explains; the caller enforces.
type Decision struct {
	Level         int      `json:"level"`          // the level the policy says
	EnforcedLevel int      `json:"enforced_level"` // what the server acts on (3 and 4 act as 2)
	Source        string   `json:"source"`         // where the policy comes from
	Paused        bool     `json:"paused"`
	AutoMerge     bool     `json:"auto_merge"`  // may merge without a person
	NeedsHuman    bool     `json:"needs_human"` // a person must approve before it merges
	CanMerge      bool     `json:"can_merge"`
	AgentApproved bool     `json:"agent_approved"` // an independent agent's approval counts   // may merge now (auto, or a person approved)
	Reasons       []string `json:"reasons"`
}

// alwaysHuman are the paths whose change by an agent always needs a person,
// whatever the policy: pipeline files and everything under .launchway
// (branch rules, standards and security settings, the autonomy policy, the
// quarantine file), owners files and the quarantine file wherever they are.
var alwaysHuman = []string{".launchway/**", "**/pipeline.y*ml", "pipeline.y*ml", "CODEOWNERS", "**/CODEOWNERS", "OWNERS", "**/OWNERS", "**/quarantine*", "quarantine*"}

func matchGlob(pattern, name string) bool {
	if strings.HasSuffix(pattern, "/**") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "**"))
	}
	if strings.HasPrefix(pattern, "**/") {
		if ok, _ := path.Match(strings.TrimPrefix(pattern, "**/"), path.Base(name)); ok {
			return true
		}
	}
	ok, _ := path.Match(pattern, name)
	return ok
}

// IsTestPath reports whether a file is a test.
func IsTestPath(p string) bool {
	base := path.Base(p)
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_") ||
		strings.HasPrefix(p, "test/") || strings.HasPrefix(p, "tests/") || strings.Contains(p, "/__tests__/") || strings.Contains(p, "/testdata/")
}

// SkipsTests reports whether added diff lines skip or disable a test.
func SkipsTests(addedLines []string) bool {
	for _, l := range addedLines {
		l = strings.TrimSpace(l)
		for _, m := range []string{"t.Skip(", "t.SkipNow(", ".skip(", "xit(", "xdescribe(", "@pytest.mark.skip", "@Disabled", "@Ignore", "it.todo("} {
			if strings.Contains(l, m) {
				return true
			}
		}
	}
	return false
}

// Paused reports whether a project's agent autonomy is paused: its recent
// agent changes were reverted or failed delivery at least the policy's
// threshold of times since a person last cleared the pause.
func (s *Store) Paused(ctx context.Context, sc Scope, au *Autonomy) (bool, int, error) {
	after, days := 3, 14
	if au != nil {
		if au.PauseAfter > 0 {
			after = au.PauseAfter
		}
		if au.PauseDays > 0 {
			days = au.PauseDays
		}
	}
	var cleared int64
	_ = s.db.QueryRow(ctx, `SELECT cleared_at FROM work_autonomy_state WHERE scope_kind = ? AND scope_id = ?`, sc.Kind, sc.ID).Scan(&cleared)
	since := ms(s.now()) - int64(days)*86400000
	if cleared > since {
		since = cleared
	}
	var n int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_agent_changes WHERE scope_kind = ? AND scope_id = ? AND outcome IN ('reverted','delivery_failed') AND at > ?`, sc.Kind, sc.ID, since).Scan(&n)
	return n >= after, n, err
}

// RecordAgentChange counts what became of an agent's change: merged, reverted
// or delivery_failed. The pause rule reads these.
func (s *Store) RecordAgentChange(ctx context.Context, sc Scope, agent, repo, pull, outcome string) error {
	if outcome != "merged" && outcome != "reverted" && outcome != "delivery_failed" {
		return invalid("an outcome is merged, reverted or delivery_failed")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO work_agent_changes(scope_kind, scope_id, agent, repo, pull, outcome, at) VALUES(?,?,?,?,?,?,?)`, sc.Kind, sc.ID, agent, repo, pull, outcome, ms(s.now()))
	return err
}

// ClearPause lets autonomy resume; a person says why, and it is recorded.
func (s *Store) ClearPause(ctx context.Context, c Caller, sc Scope, reason string) error {
	if !c.Human() || !c.isMaintainer(sc) {
		return forbidden("only a person who maintains %s clears a pause", sc)
	}
	if strings.TrimSpace(reason) == "" {
		return invalid("say why autonomy may resume")
	}
	return s.tx(ctx, func(tx DB) error {
		if _, err := tx.Exec(ctx, `DELETE FROM work_autonomy_state WHERE scope_kind = ? AND scope_id = ?`, sc.Kind, sc.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO work_autonomy_state(scope_kind, scope_id, cleared_at, cleared_by, reason) VALUES(?,?,?,?,?)`, sc.Kind, sc.ID, ms(s.now()), c.Name, cleanTitle(reason))
		return err
	})
}

// AutonomyStatus is the effective policy shown with where it comes from.
type AutonomyStatus struct {
	Level         int       `json:"level"`
	EnforcedLevel int       `json:"enforced_level"`
	Available     bool      `json:"available"` // false for levels the server does not act on yet
	Source        string    `json:"source"`
	Policy        *Autonomy `json:"policy"`
	Paused        bool      `json:"paused"`
	Failures      int       `json:"failures"`
	PauseAfter    int       `json:"pause_after"`
	AgentsOn      bool      `json:"agents_enabled"`
}

// Autonomy reports the effective policy of a project.
func (s *Store) Autonomy(ctx context.Context, sc Scope) (*AutonomyStatus, error) {
	eff, err := s.Settings(ctx, sc)
	if err != nil {
		return nil, err
	}
	st := &AutonomyStatus{Level: AutonomyPerson, Source: "default", PauseAfter: 3, AgentsOn: eff.Agents.AgentsEnabled()}
	var au *Autonomy
	if eff.Agents != nil && eff.Agents.Autonomy != nil {
		au = eff.Agents.Autonomy
		st.Level, st.Policy, st.Source = au.Level, au, eff.AutonomySource.String()
		if au.PauseAfter > 0 {
			st.PauseAfter = au.PauseAfter
		}
	}
	st.EnforcedLevel = min(st.Level, AutonomyAvailable)
	st.Available = st.Level <= AutonomyAvailable
	st.Paused, st.Failures, err = s.Paused(ctx, sc, au)
	return st, err
}

// Gate decides whether an agent's pull request may merge without a person. It
// is the merge gate's one question about agents. Whatever the policy, a
// change that touches protected files, deletes or skips tests, or has checks
// that did not pass cannot merge on its own, and nothing here lets an agent
// approve its own work.
func (s *Store) Gate(ctx context.Context, ch AgentChange) (*Decision, error) {
	eff, err := s.Settings(ctx, ch.Scope)
	if err != nil {
		return nil, err
	}
	d := &Decision{Level: AutonomyPerson, Source: "default", Reasons: []string{}}
	var au *Autonomy
	if eff.Agents != nil && eff.Agents.Autonomy != nil {
		au = eff.Agents.Autonomy
		d.Level, d.Source = au.Level, eff.AutonomySource.String()
	}
	d.EnforcedLevel = min(d.Level, AutonomyAvailable)
	if !eff.Agents.AgentsEnabled() {
		d.NeedsHuman = true
		d.Reasons = append(d.Reasons, "agents are switched off here")
		return d, nil
	}
	d.NeedsHuman = true // until the policy says otherwise
	protected, reasons := protectedReasons(ch)
	d.Reasons = append(d.Reasons, reasons...)
	if d.Level == AutonomyProposeOnly {
		// Said before the file reasons, as the policy comes first.
		d.Reasons = append([]string{"policy: agents here only propose; they open no pull requests"}, d.Reasons...)
	}
	if ch.ChecksSkipped || !ch.ChecksPassed {
		d.Reasons = append(d.Reasons, "required checks have not all passed (a failing or skipped check blocks every merge)")
	} else if ch.ChecksRequired == 0 && d.EnforcedLevel >= AutonomyAutoMerge {
		d.Reasons = append(d.Reasons, "no check is required on this branch, so nothing can show that the change is safe to merge without a person")
	}
	if d.EnforcedLevel < AutonomyAutoMerge {
		if d.Level < AutonomyPerson+1 {
			d.Reasons = append(d.Reasons, "policy: a person approves and merges")
		}
	} else if !protected && ch.ChecksPassed && !ch.ChecksSkipped && ch.ChecksRequired > 0 {
		if err := s.autoMergeCheck(ctx, ch, au, d); err != nil {
			return nil, err
		}
	}
	s.settleMerge(ctx, ch, d, protected)
	return d, nil
}

// settleMerge counts the approvals of an agent and of a person and says
// whether the change can merge.
func (s *Store) settleMerge(ctx context.Context, ch AgentChange, d *Decision, protected bool) {
	// An independent agent's approval counts only where the review policy
	// allows it for this change, and never for what a person always approves.
	agentApproved := false
	if ch.Repo != "" && !protected {
		if n, err := s.countedAgentApprovals(ctx, ch.Repo, ch.Pull); err == nil && n > 0 {
			agentApproved = true
			d.Reasons = append(d.Reasons, "an independent agent approved it, as the review policy allows for this change")
		}
	}
	d.AgentApproved = agentApproved
	// A person's approval lets it merge when the checks have passed.
	d.CanMerge = d.AutoMerge || ((ch.HumanApproved || agentApproved) && ch.ChecksPassed && !ch.ChecksSkipped && d.Level != AutonomyProposeOnly)
	if d.CanMerge && d.NeedsHuman && agentApproved && !ch.HumanApproved {
		d.NeedsHuman = false
	}
	if d.NeedsHuman && ch.HumanApproved && len(d.Reasons) > 0 {
		d.Reasons = append(d.Reasons, "a person approved it")
	}
}

// protectedReasons says whether the change touches what a person always
// approves, and why.
func protectedReasons(ch AgentChange) (bool, []string) {
	protected := false
	var r []string
	for _, f := range ch.Files {
		for _, p := range alwaysHuman {
			if matchGlob(p, f.Path) {
				protected = true
				r = append(r, fmt.Sprintf("%s is a protected file (pipelines, branch rules, owners, quarantine, standards, the autonomy policy): a person always approves a change to it", f.Path))
				break
			}
		}
		if f.Deleted && IsTestPath(f.Path) {
			protected = true
			r = append(r, fmt.Sprintf("%s is a test and the change deletes it: a person always approves that", f.Path))
		}
	}
	if ch.TestsSkipped {
		protected = true
		r = append(r, "the change skips or disables a test: a person always approves that")
	}
	return protected, r
}

// autoMergeCheck applies the level-2 policy to a change whose checks passed:
// it sets the decision to auto-merge or says why not.
func (s *Store) autoMergeCheck(ctx context.Context, ch AgentChange, au *Autonomy, d *Decision) error {
	paused, n, err := s.Paused(ctx, ch.Scope, au)
	if err != nil {
		return err
	}
	d.Paused = paused
	maxFiles, maxLines := 10, 200
	if au.MaxFiles > 0 {
		maxFiles = au.MaxFiles
	}
	if au.MaxLines > 0 {
		maxLines = au.MaxLines
	}
	ok := true
	why := func(f string, a ...any) { ok = false; d.Reasons = append(d.Reasons, fmt.Sprintf(f, a...)) }
	switch {
	case paused:
		why("autonomy is paused: %d of this project's recent agent changes were reverted or failed delivery; a person clears the pause", n)
	case len(ch.Files) == 0 || len(ch.Files) > maxFiles || ch.Lines > maxLines:
		why("the change is outside the policy's size (up to %d files and %d lines)", maxFiles, maxLines)
	case len(au.Types) > 0 && !contains(au.Types, ch.ItemType):
		why("the policy auto-merges only %s work", strings.Join(au.Types, ", "))
	}
	for _, f := range ch.Files {
		listed := false
		for _, p := range au.Paths {
			if matchGlob(p, f.Path) {
				listed = true
			}
		}
		if !listed {
			why("%s is not among the paths the policy lets an agent change on its own", f.Path)
		}
	}
	if ok {
		d.AutoMerge, d.NeedsHuman = true, false
		d.Reasons = append(d.Reasons, "policy level 2: every check passed and the change is of a listed kind")
	}
	return nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// ---- usage and budgets ----

// Usage is what an agent reports it spent on an item.
type Usage struct {
	Tokens  int64  `json:"tokens"`
	Cents   int64  `json:"cents"`
	Seconds int64  `json:"seconds"`
	Note    string `json:"note"`
}

// UsageTotal is the sum on an item.
type UsageTotal struct {
	Tokens   int64  `json:"tokens"`
	Cents    int64  `json:"cents"`
	Seconds  int64  `json:"seconds"`
	Exceeded bool   `json:"exceeded"`
	Message  string `json:"message,omitempty"`
}

// ReportUsage records what an agent spent working an item it holds. When the
// item's budget or the agent's daily budget is passed the answer says so and
// further claims are refused; nothing is stopped mid-way.
func (s *Store) ReportUsage(ctx context.Context, agent *Agent, ref string, u Usage) (*UsageTotal, error) {
	if u.Tokens < 0 || u.Cents < 0 || u.Seconds < 0 || u.Tokens > 1e12 || u.Cents > 1e9 {
		return nil, invalid("usage is zero or more")
	}
	it, err := s.holds(ctx, agent, ref)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO work_usage(agent, item_id, at, tokens, cents, seconds, note) VALUES(?,?,?,?,?,?,?)`, agent.Name, it.ID, ms(s.now()), u.Tokens, u.Cents, u.Seconds, cleanTitle(u.Note)); err != nil {
		return nil, err
	}
	return s.ItemUsage(ctx, it.ID, agent.Name)
}

// ItemUsage totals the usage on an item (every agent's).
func (s *Store) ItemUsage(ctx context.Context, itemID, agentName string) (*UsageTotal, error) {
	t := &UsageTotal{}
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(SUM(tokens),0), COALESCE(SUM(cents),0), COALESCE(SUM(seconds),0) FROM work_usage WHERE item_id = ?`, itemID).Scan(&t.Tokens, &t.Cents, &t.Seconds); err != nil {
		return nil, err
	}
	if agentName != "" {
		a, err := s.AgentByName(ctx, agentName)
		if err == nil {
			switch {
			case a.ItemTokens > 0 && t.Tokens >= a.ItemTokens:
				t.Exceeded, t.Message = true, fmt.Sprintf("the item's token budget (%d) is used up", a.ItemTokens)
			case a.ItemCents > 0 && t.Cents >= a.ItemCents:
				t.Exceeded, t.Message = true, fmt.Sprintf("the item's cost budget (%d cents) is used up", a.ItemCents)
			case a.ItemSeconds > 0 && t.Seconds >= a.ItemSeconds:
				t.Exceeded, t.Message = true, fmt.Sprintf("the item's time budget (%d seconds) is used up", a.ItemSeconds)
			case a.BudgetTokensDay > 0 && a.UsedTokensDay >= a.BudgetTokensDay, a.BudgetCentsDay > 0 && a.UsedCentsDay >= a.BudgetCentsDay:
				t.Exceeded, t.Message = true, "the agent's budget for the last 24 hours is used up"
			}
		}
	}
	return t, nil
}

// TakeOver lets a person take work from an agent: the person becomes the
// assignee, the agent's lease ends, and the branch stays where it is.
func (s *Store) TakeOver(ctx context.Context, c Caller, ref string) (*Item, error) {
	if !c.Human() {
		return nil, forbidden("a person takes work over")
	}
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(it.Scope) {
		return nil, forbidden("you may not change work in %s", it.Scope)
	}
	err = s.tx(ctx, func(tx DB) error {
		var agents []string
		for _, a := range it.Assignees {
			if a.Kind == KindAgent {
				agents = append(agents, a.Name)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM work_claims WHERE item_id = ?`, it.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM work_assignees WHERE item_id = ? AND kind = 'agent'`, it.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM work_assignees WHERE item_id = ? AND who = ?`, it.ID, c.Name); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_assignees(item_id, who, kind) VALUES(?,?,?)`, it.ID, c.Name, KindPerson); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET version = version + 1, updated_at = ? WHERE id = ?`, ms(s.now()), it.ID); err != nil {
			return err
		}
		for _, a := range agents {
			if err := s.logAgent(ctx, tx, a, it.ID, "taken_over", c.Name); err != nil {
				return err
			}
		}
		return s.event(ctx, tx, it.ID, c.Actor, "taken_over", 0, map[string]any{"agents": agents, "note": "the agent's lease ended; its branch is untouched"})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, c, it.ID)
}
