package work

import (
	"context"
	"encoding/json"
	"path"
	"slices"
	"strings"
)

// State categories: what a state means to boards, charts and roll-ups.
const (
	CatTodo      = "todo"
	CatDoing     = "doing"
	CatReview    = "review"
	CatDone      = "done"
	CatCancelled = "cancelled"
)

// State is one step of a workflow.
type State struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Category string `json:"category"`
	WIP      int    `json:"wip,omitempty"` // a board column warns past this many; 0 is no limit
}

// Who may make a transition.
const (
	WhoAnyone     = "anyone"     // anyone who may change the item
	WhoHuman      = "human"      // a person, never an agent
	WhoMaintainer = "maintainer" // someone who maintains the scope
)

// Transition is a rule about moving into a state. When From is empty the rule
// holds from every state.
type Transition struct {
	From []string `json:"from,omitempty"`
	To   string   `json:"to"`
	Who  string   `json:"who"`
}

// FieldDef is a custom field of some item types.
type FieldDef struct {
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	Type     string   `json:"type"` // text, number, date, select, boolean
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
	Types    []string `json:"types,omitempty"` // item types that have it; empty is all
}

// TypeDef configures an item type at a level.
type TypeDef struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Template string `json:"template,omitempty"` // the body a new item of the type starts from
	Disabled bool   `json:"disabled,omitempty"`
}

// CloseRule says what happens to an item when a pull request that names it
// is merged.
type CloseRule struct {
	// On is "merge" (the item moves to State when the pull request merges),
	// "review" (to In review when the pull request opens, Done only by a
	// person) or "never" (links only).
	On    string `json:"on"`
	State string `json:"state"` // the state a merge moves it to; default done
	// Keywords that close (fixes, closes, resolves) versus only link.
	Verbs []string `json:"verbs,omitempty"`
}

// Settings configure work at one level. A field left empty is inherited.
type Settings struct {
	Prefix      string       `json:"prefix,omitempty"`
	Unit        string       `json:"estimate_unit,omitempty"` // points or hours
	States      []State      `json:"states,omitempty"`
	Transitions []Transition `json:"transitions,omitempty"`
	Types       []TypeDef    `json:"types,omitempty"`
	Fields      []FieldDef   `json:"fields,omitempty"`
	Close       *CloseRule   `json:"close,omitempty"`
	// Agents: whether agents may work here at all, and what a person must do
	// before an agent's work lands (see AgentRules).
	Agents *AgentRules `json:"agents,omitempty"`
	// Difficulty maps an item's difficulty to the agent classes that may take
	// it (small: [small, medium, large]; large: [large]). A difficulty with no
	// entry is open to every class. Rules set the difficulty of a new item.
	Classes         map[string][]string `json:"classes,omitempty"`
	DifficultyRules []DifficultyRule    `json:"difficulty_rules,omitempty"`
	// Environments names, in order, the environments the delivery status
	// shows ("dev", "prod"); empty uses every environment that has a deployment.
	Environments []string `json:"environments,omitempty"`
}

// DifficultyRule sets the difficulty of a new item from its label or its size.
type DifficultyRule struct {
	Label       string  `json:"label,omitempty"`
	MinEstimate float64 `json:"min_estimate,omitempty"`
	Difficulty  string  `json:"difficulty"`
}

// AgentReviewPolicy says whether an agent's review may count as an approval.
type AgentReviewPolicy struct {
	// Mode is human_only (the default: an agent's review is advice) or
	// independent_agent_allowed (a different agent's approval counts for
	// pull requests that match Paths, Types, MaxFiles and MaxLines).
	Mode                  string   `json:"mode"`
	Paths                 []string `json:"paths,omitempty"`
	Types                 []string `json:"types,omitempty"`
	MaxFiles              int      `json:"max_files,omitempty"`
	MaxLines              int      `json:"max_lines,omitempty"`
	RequireDifferentModel bool     `json:"require_different_model,omitempty"`
	// SamplePercent of merges an agent approved are flagged for a person to
	// look at afterwards (0 to 100).
	SamplePercent int    `json:"sample_percent,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// Agent review modes.
const (
	ReviewHumanOnly   = "human_only"
	ReviewIndependent = "independent_agent_allowed"
)

func (e *Effective) difficultyFor(labels []string, estimate float64) string {
	for _, r := range e.DifficultyRules {
		if r.Label != "" && slices.Contains(labels, strings.ToLower(r.Label)) {
			return r.Difficulty
		}
		if r.MinEstimate > 0 && estimate >= r.MinEstimate {
			return r.Difficulty
		}
	}
	return ""
}

// AgentRules are set per level and only ever loosened with an audited reason.
type AgentRules struct {
	Enabled       *bool  `json:"enabled,omitempty"`        // the kill switch of the level: false stops every agent here
	HumanApproval *bool  `json:"human_approval,omitempty"` // default true
	AllowMerge    *bool  `json:"allow_merge,omitempty"`    // default false
	Reason        string `json:"reason,omitempty"`         // why a safeguard was loosened
	// Review says whether another agent's approval may count.
	Review *AgentReviewPolicy `json:"review,omitempty"`
	// Autonomy says how far an agent's work may go without a person.
	Autonomy *Autonomy `json:"autonomy,omitempty"`
}

// Autonomy levels. 0: agents propose only (no pull requests); 1: a person
// approves and merges (the default); 2: an agent's pull request may merge by
// itself when every required check passes and the change is of a kind the
// policy lists; 3: and Launch to non-production Airfields; 4: and production
// through the normal delivery gates. Levels 3 and 4 can be written down but the
// server does not act on them yet: it enforces them as level 2.
const (
	AutonomyProposeOnly = 0
	AutonomyPerson      = 1
	AutonomyAutoMerge   = 2
	AutonomyAvailable   = 2
)

// Autonomy is the policy of one level.
type Autonomy struct {
	Level int `json:"level"`
	// For level 2: the change must be within these limits.
	Paths    []string `json:"paths,omitempty"`     // every changed file matches one of these patterns
	Types    []string `json:"types,omitempty"`     // the item's type is one of these (empty: any)
	MaxFiles int      `json:"max_files,omitempty"` // 0: 10
	MaxLines int      `json:"max_lines,omitempty"` // 0: 200
	// Pause: after this many of an agent's changes were reverted or failed
	// delivery within PauseDays, autonomy stops until a person clears it.
	PauseAfter int    `json:"pause_after,omitempty"` // 0: 3
	PauseDays  int    `json:"pause_days,omitempty"`  // 0: 14
	Reason     string `json:"reason,omitempty"`      // required when the level is above 1
}

// AgentsEnabled says whether agents may work under these settings.
func (r *AgentRules) AgentsEnabled() bool { return r == nil || r.Enabled == nil || *r.Enabled }

// NeedsHuman says whether an agent's pull request needs a person's approval.
func (r *AgentRules) NeedsHuman() bool { return r == nil || r.HumanApproval == nil || *r.HumanApproval }

// MayMerge says whether an agent may merge its own work.
func (r *AgentRules) MayMerge() bool { return r != nil && r.AllowMerge != nil && *r.AllowMerge }

// DefaultStates is the workflow of a level that defines none.
func DefaultStates() []State {
	return []State{
		{Key: "backlog", Name: "Backlog", Category: CatTodo},
		{Key: "ready", Name: "Ready", Category: CatTodo},
		{Key: "in_progress", Name: "In progress", Category: CatDoing},
		{Key: "in_review", Name: "In review", Category: CatReview},
		{Key: "done", Name: "Done", Category: CatDone},
		{Key: "wont_do", Name: "Won't do", Category: CatCancelled},
	}
}

// DefaultTransitions: anyone may move work around; only a person says it is
// finished or will not be done.
func DefaultTransitions() []Transition {
	return []Transition{
		{To: "done", Who: WhoHuman},
		{To: "wont_do", Who: WhoHuman},
	}
}

// DefaultTypes are the five item types.
func DefaultTypes() []TypeDef {
	return []TypeDef{{Key: TypeIssue, Name: "Issue"}, {Key: TypeBug, Name: "Bug"}, {Key: TypeStory, Name: "Story"}, {Key: TypeTask, Name: "Task"}, {Key: TypeEpic, Name: "Epic"}}
}

// DefaultClose moves an item to Done when its pull request merges.
func DefaultClose() CloseRule {
	return CloseRule{On: "merge", State: "done", Verbs: []string{"fixes", "closes", "resolves"}}
}

// Effective is the settings in force for a scope after inheritance.
type Effective struct {
	Settings
	Chain []Scope `json:"chain"`
	// AutonomySource is the level the autonomy policy comes from (the zero
	// Scope when it is the default).
	AutonomySource Scope `json:"autonomy_source"`
}

// Category of a state key ("" if the state is unknown).
func (e *Effective) Category(state string) string {
	for _, s := range e.States {
		if s.Key == state {
			return s.Category
		}
	}
	return ""
}

// StateByKey finds a state.
func (e *Effective) StateByKey(key string) (State, bool) {
	for _, s := range e.States {
		if s.Key == key {
			return s, true
		}
	}
	return State{}, false
}

// FirstOf returns the first state of a category (the one a board column and a
// carry-over use).
func (e *Effective) FirstOf(cat string) string {
	for _, s := range e.States {
		if s.Category == cat {
			return s.Key
		}
	}
	return ""
}

// Closed reports whether a state ends the work (done or cancelled).
func (e *Effective) Closed(state string) bool {
	c := e.Category(state)
	return c == CatDone || c == CatCancelled
}

// TypeAllowed reports whether new items may have the type.
func (e *Effective) TypeAllowed(t string) bool {
	for _, d := range e.Types {
		if d.Key == t {
			return !d.Disabled
		}
	}
	return false
}

// CheckMove says whether an actor may move an item from one state to another.
// maintainer tells whether the actor maintains the scope.
func (e *Effective) CheckMove(from, to string, a Actor, maintainer bool) error {
	if _, ok := e.StateByKey(to); !ok {
		return invalid("%q is not a state of this workflow", to)
	}
	if from == to {
		return nil
	}
	for _, t := range e.Transitions {
		if t.To != to || (len(t.From) > 0 && !slices.Contains(t.From, from)) {
			continue
		}
		switch t.Who {
		case WhoHuman:
			if !a.Human() {
				return forbidden("only a person may move work to %s; an agent hands it over for review instead", e.stateName(to))
			}
		case WhoMaintainer:
			if !maintainer || !a.Human() {
				return forbidden("only a maintainer may move work to %s", e.stateName(to))
			}
		}
	}
	return nil
}

func (e *Effective) stateName(key string) string {
	if s, ok := e.StateByKey(key); ok {
		return s.Name
	}
	return key
}

// Validate checks settings before they are stored.
func (s *Settings) Validate() error {
	if s.Prefix != "" {
		if len(s.Prefix) > 10 || strings.ToUpper(s.Prefix) != s.Prefix || strings.ContainsAny(s.Prefix, " -#/") {
			return invalid("a prefix is up to 10 capital letters or digits, such as PAY")
		}
	}
	if s.Unit != "" && s.Unit != "points" && s.Unit != "hours" {
		return invalid("estimate_unit is points or hours")
	}
	if len(s.States) > 0 {
		seen := map[string]bool{}
		done, todo := false, false
		for _, st := range s.States {
			if st.Key == "" || st.Name == "" || seen[st.Key] || len(st.Key) > 40 {
				return invalid("each state needs a unique key and a name")
			}
			seen[st.Key] = true
			switch st.Category {
			case CatTodo:
				todo = true
			case CatDoing, CatReview:
			case CatDone:
				done = true
			case CatCancelled:
			default:
				return invalid("state %s has category %q: use todo, doing, review, done or cancelled", st.Key, st.Category)
			}
			if st.WIP < 0 {
				return invalid("state %s has a negative limit", st.Key)
			}
		}
		if !todo || !done || len(s.States) > 20 {
			return invalid("a workflow has between 2 and 20 states and at least one to-do and one done state")
		}
		for _, t := range s.Transitions {
			if !seen[t.To] || (t.Who != WhoAnyone && t.Who != WhoHuman && t.Who != WhoMaintainer) {
				return invalid("a transition names a state of the workflow and who may make it (anyone, human or maintainer)")
			}
			for _, f := range t.From {
				if !seen[f] {
					return invalid("transition into %s names the unknown state %s", t.To, f)
				}
			}
		}
	}
	for _, t := range s.Types {
		if t.Key == "" || t.Name == "" || !slices.Contains(Types, t.Key) {
			return invalid("a type is one of %s", strings.Join(Types, ", "))
		}
	}
	for _, f := range s.Fields {
		if f.Key == "" || f.Name == "" || strings.ContainsAny(f.Key, " .") {
			return invalid("a custom field needs a key and a name")
		}
		switch f.Type {
		case "text", "number", "date", "boolean":
		case "select":
			if len(f.Options) == 0 {
				return invalid("field %s needs options", f.Key)
			}
		default:
			return invalid("field %s has type %q: use text, number, date, select or boolean", f.Key, f.Type)
		}
	}
	if s.Close != nil {
		if s.Close.On != "merge" && s.Close.On != "review" && s.Close.On != "never" {
			return invalid("close.on is merge, review or never")
		}
	}
	if s.Agents != nil && s.Agents.Autonomy != nil {
		au := s.Agents.Autonomy
		if au.Level < 0 || au.Level > 4 || au.MaxFiles < 0 || au.MaxLines < 0 || au.PauseAfter < 0 || au.PauseDays < 0 {
			return invalid("autonomy level is 0 to 4, and the limits are not negative")
		}
		if au.Level > AutonomyPerson && strings.TrimSpace(au.Reason) == "" {
			return invalid("raising agent autonomy above 'a person approves and merges' needs a reason, which is recorded")
		}
		if au.Level >= AutonomyAutoMerge && len(au.Paths) == 0 {
			return invalid("autonomy 2 lists the paths an agent may change without a person (for example docs/**); with none it would allow nothing")
		}
		for _, p := range au.Paths {
			if _, err := path.Match(p, ""); err != nil {
				return invalid("%q is not a path pattern", p)
			}
		}
	}
	if s.Agents != nil && s.Agents.Review != nil {
		r := s.Agents.Review
		if r.Mode != ReviewHumanOnly && r.Mode != ReviewIndependent {
			return invalid("review mode is human_only or independent_agent_allowed")
		}
		if r.SamplePercent < 0 || r.SamplePercent > 100 || r.MaxFiles < 0 || r.MaxLines < 0 {
			return invalid("sample_percent is 0 to 100 and the limits are not negative")
		}
		if r.Mode == ReviewIndependent && (strings.TrimSpace(r.Reason) == "" || len(r.Paths) == 0) {
			return invalid("letting an independent agent's approval count needs a reason and the paths it covers")
		}
	}
	for d, cl := range s.Classes {
		if !slices.Contains([]string{"small", "medium", "large"}, d) {
			return invalid("difficulty %q is small, medium or large", d)
		}
		for _, c := range cl {
			if !slices.Contains([]string{"small", "medium", "large"}, c) {
				return invalid("class %q is small, medium or large", c)
			}
		}
	}
	if s.Agents != nil {
		loosened := (s.Agents.HumanApproval != nil && !*s.Agents.HumanApproval) || (s.Agents.AllowMerge != nil && *s.Agents.AllowMerge)
		if loosened && strings.TrimSpace(s.Agents.Reason) == "" {
			return invalid("letting agents land work without a person's approval, or merge it, needs a reason, which is recorded")
		}
	}
	return nil
}

// merge lays over (a nearer level) on top of s (a farther one).
func (s Settings) merge(over Settings) Settings {
	if over.Prefix != "" {
		s.Prefix = over.Prefix
	}
	if over.Unit != "" {
		s.Unit = over.Unit
	}
	if len(over.States) > 0 {
		s.States = over.States
		s.Transitions = over.Transitions
	} else if len(over.Transitions) > 0 {
		s.Transitions = over.Transitions
	}
	if len(over.Types) > 0 {
		s.Types = over.Types
	}
	if len(over.Classes) > 0 {
		s.Classes = over.Classes
	}
	s.DifficultyRules = append(slices.Clone(s.DifficultyRules), over.DifficultyRules...)
	s.Fields = append(slices.Clone(s.Fields), over.Fields...)
	if over.Close != nil {
		s.Close = over.Close
	}
	if len(over.Environments) > 0 {
		s.Environments = over.Environments
	}
	if over.Agents != nil {
		// A nearer level may switch agents off or tighten, and may loosen
		// only with its recorded reason; unset fields stay as they were.
		a := AgentRules{}
		if s.Agents != nil {
			a = *s.Agents
		}
		if over.Agents.Enabled != nil {
			a.Enabled = over.Agents.Enabled
		}
		// Switched off above stays off below.
		if s.Agents != nil && s.Agents.Enabled != nil && !*s.Agents.Enabled {
			a.Enabled = s.Agents.Enabled
		}
		if over.Agents.HumanApproval != nil {
			a.HumanApproval = over.Agents.HumanApproval
		}
		if over.Agents.AllowMerge != nil {
			a.AllowMerge = over.Agents.AllowMerge
		}
		if over.Agents.Review != nil {
			a.Review = over.Agents.Review
		}
		if over.Agents.Autonomy != nil {
			a.Autonomy = over.Agents.Autonomy
		}
		if over.Agents.Reason != "" {
			a.Reason = over.Agents.Reason
		}
		s.Agents = &a
	}
	return s
}

// GetSettings reads the settings stored at one scope (empty if none).
func (s *Store) GetSettings(ctx context.Context, sc Scope) (Settings, error) {
	var raw string
	err := s.db.QueryRow(ctx, `SELECT settings FROM work_scopes WHERE scope_kind = ? AND scope_id = ?`, sc.Kind, sc.ID).Scan(&raw)
	var out Settings
	if isNoRows(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out, nil
}

// PutSettings stores the settings of a scope, creating the scope's record. A
// prefix must not be another scope's.
func (s *Store) PutSettings(ctx context.Context, sc Scope, st Settings, actor Actor) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	if err := st.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(st)
	defer s.invalidate()
	return s.tx(ctx, func(tx DB) error {
		if err := s.ensureScope(ctx, tx, sc); err != nil {
			return err
		}
		if st.Prefix != "" {
			var other int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_scopes WHERE prefix = ? AND NOT (scope_kind = ? AND scope_id = ?)`, st.Prefix, sc.Kind, sc.ID).Scan(&other); err != nil {
				return err
			}
			if other > 0 {
				return conflict("the prefix %s belongs to another project", st.Prefix)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE work_scopes SET settings = ? WHERE scope_kind = ? AND scope_id = ?`, string(raw), sc.Kind, sc.ID); err != nil {
			return err
		}
		if st.Prefix != "" {
			if _, err := tx.Exec(ctx, `UPDATE work_scopes SET prefix = ? WHERE scope_kind = ? AND scope_id = ?`, st.Prefix, sc.Kind, sc.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// Resolve works out the settings in force for the first scope of a chain
// (project, unit, organization, installation: nearest first), with the
// defaults underneath.
func (s *Store) Resolve(ctx context.Context, chain []Scope) (*Effective, error) {
	eff := Settings{Unit: "points", States: DefaultStates(), Transitions: DefaultTransitions(), Types: DefaultTypes()}
	cr := DefaultClose()
	eff.Close = &cr
	for i := len(chain) - 1; i >= 0; i-- {
		st, err := s.GetSettings(ctx, chain[i])
		if err != nil {
			return nil, err
		}
		eff = eff.merge(st)
	}
	e := &Effective{Settings: eff, Chain: chain}
	for i := len(chain) - 1; i >= 0; i-- {
		st, _ := s.GetSettings(ctx, chain[i])
		if st.Agents != nil && st.Agents.Autonomy != nil {
			e.AutonomySource = chain[i]
		}
	}
	return e, nil
}
