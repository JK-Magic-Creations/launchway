package release

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Window says when a change may reach an Airfield: the days of the week and
// the hours, in a time zone. An empty window allows any time.
type Window struct {
	Days []string `json:"days,omitempty"` // mon tue wed thu fri sat sun; none: every day
	From string   `json:"from,omitempty"` // HH:MM, inclusive; with To empty: all day
	To   string   `json:"to,omitempty"`   // HH:MM, exclusive
	Zone string   `json:"zone,omitempty"` // an IANA name; default UTC
}

var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func parseClock(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && m != 0) || len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("%q is not a time of day as HH:MM", s)
	}
	return h*60 + m, nil
}

// Check validates a window.
func (w Window) Check() error {
	for _, d := range w.Days {
		ok := false
		for _, n := range dayNames {
			ok = ok || n == strings.ToLower(d)
		}
		if !ok {
			return fmt.Errorf("%q is not a day: use mon, tue, wed, thu, fri, sat or sun", d)
		}
	}
	if (w.From == "") != (w.To == "") {
		return fmt.Errorf("give both from and to, or neither")
	}
	if w.From != "" {
		a, err := parseClock(w.From)
		if err != nil {
			return err
		}
		b, err := parseClock(w.To)
		if err != nil {
			return err
		}
		if a >= b {
			return fmt.Errorf("from (%s) must be before to (%s); a window cannot pass midnight, add the next day's early hours as another rule", w.From, w.To)
		}
	}
	if w.Zone != "" {
		if _, err := time.LoadLocation(w.Zone); err != nil {
			return fmt.Errorf("%q is not a time zone name (such as America/Chicago)", w.Zone)
		}
	}
	return nil
}

func (w Window) empty() bool { return len(w.Days) == 0 && w.From == "" }

// Allows reports whether t is inside the window and, when it is not, when it
// next opens (within a week), or "".
func (w Window) Allows(t time.Time) (bool, time.Time) {
	if w.empty() {
		return true, time.Time{}
	}
	loc := time.UTC
	if w.Zone != "" {
		if l, err := time.LoadLocation(w.Zone); err == nil {
			loc = l
		}
	}
	in := func(x time.Time) bool {
		x = x.In(loc)
		if len(w.Days) > 0 {
			day := dayNames[x.Weekday()]
			ok := false
			for _, d := range w.Days {
				ok = ok || strings.ToLower(d) == day
			}
			if !ok {
				return false
			}
		}
		if w.From != "" {
			a, _ := parseClock(w.From)
			b, _ := parseClock(w.To)
			mins := x.Hour()*60 + x.Minute()
			return mins >= a && mins < b
		}
		return true
	}
	if in(t) {
		return true, time.Time{}
	}
	// Look for the next minute-aligned opening: step by minutes for a week.
	x := t.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 7*24*60; i++ {
		if in(x) {
			return false, x
		}
		x = x.Add(time.Minute)
	}
	return false, time.Time{}
}

// Freeze is a period in which nothing is promoted, to every Airfield or one,
// for every project or one.
type Freeze struct {
	ID       string `json:"id"`
	Project  string `json:"project,omitempty"`  // "": every project
	Airfield string `json:"airfield,omitempty"` // "": every Airfield
	From     int64  `json:"from"`               // milliseconds
	To       int64  `json:"to"`
	Reason   string `json:"reason"`
	By       string `json:"by,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
}

// Covers reports whether the freeze stops a promotion of a project to an
// Airfield at a time.
func (f Freeze) Covers(project, airfield string, at time.Time) bool {
	ms := at.UnixMilli()
	return !f.Canceled && ms >= f.From && ms < f.To && (f.Project == "" || f.Project == project) && (f.Airfield == "" || f.Airfield == airfield)
}

// Gate is the checklist a release must pass to be promoted to an Airfield.
type Gate struct {
	RequireChecks    bool     `json:"require_checks,omitempty"` // every required check green on the release's commit
	RequireNotes     bool     `json:"require_notes,omitempty"`  // the release has notes
	RequireNoBlock   bool     `json:"require_no_blocking_findings,omitempty"`
	SoakMinutes      int      `json:"soak_minutes,omitempty"`    // time in the previous Airfield, healthy
	Clearances       int      `json:"clearances,omitempty"`      // how many clearances are needed
	ClearanceRoles   []string `json:"clearance_roles,omitempty"` // who may give them: operator, admin
	Window           *Window  `json:"window,omitempty"`
	PreviousAirfield string   `json:"previous_airfield,omitempty"` // the one the release must be in first ("" none)
}

// Check validates a gate.
func (g Gate) Check() error {
	if g.SoakMinutes < 0 || g.SoakMinutes > 60*24*30 {
		return fmt.Errorf("soak_minutes must be 0 to 43200 (30 days)")
	}
	if g.Clearances < 0 || g.Clearances > 10 {
		return fmt.Errorf("clearances must be 0 to 10")
	}
	for _, r := range g.ClearanceRoles {
		if r != "operator" && r != "admin" {
			return fmt.Errorf("clearance_roles: %q is not a role that can give clearance: use operator or admin", r)
		}
	}
	if g.Window != nil {
		return g.Window.Check()
	}
	return nil
}

// Status of one line of a checklist.
type Status string

const (
	Pass    Status = "pass"
	Fail    Status = "fail"
	Waiting Status = "waiting" // may become pass with time or a person's action
	Skipped Status = "skipped" // the gate does not ask for it
)

// Item is one line of a checklist, with what it found.
type Item struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	// Overridable says an administrator may override it with a reason
	// (a window or a freeze: yes; the digest of the release: never).
	Overridable bool `json:"overridable,omitempty"`
}

// Facts are what the server knows about a release when it is judged.
type Facts struct {
	Now              time.Time
	Project          string
	Airfield         string
	State            string // the release's state
	HasNotes         bool
	ChecksGreen      bool
	ChecksDetail     string
	BlockingFindings int
	// InPrevious, when the gate names a previous Airfield: whether the release
	// is there, since when, and whether it is healthy.
	InPrevious      bool
	PreviousSince   time.Time
	PreviousHealthy bool
	Clearances      []string // who gave clearance, in roles that count
	Publisher       string
	Freezes         []Freeze
}

// Evaluate judges a release against a gate. The release must be published
// (or a candidate in an Airfield that is not the last one: the caller says by
// passing the state) to be promoted at all.
func Evaluate(g Gate, f Facts) []Item {
	var out []Item
	add := func(id, label string, st Status, detail string, over bool) {
		out = append(out, Item{ID: id, Label: label, Status: st, Detail: detail, Overridable: over})
	}
	switch f.State {
	case "published", "candidate":
		add("state", "The release is ready to promote", Pass, "it is "+f.State, false)
	default:
		add("state", "The release is ready to promote", Fail, "it is "+f.State+"; only a candidate or a published release is promoted", false)
	}
	if g.RequireNotes {
		if f.HasNotes {
			add("notes", "Release notes are written", Pass, "", false)
		} else {
			add("notes", "Release notes are written", Fail, "the release has no notes", false)
		}
	} else {
		add("notes", "Release notes are written", Skipped, "not required here", false)
	}
	if g.RequireChecks {
		if f.ChecksGreen {
			add("checks", "Checks are green on the release's commit", Pass, f.ChecksDetail, false)
		} else {
			add("checks", "Checks are green on the release's commit", Fail, f.ChecksDetail, false)
		}
	} else {
		add("checks", "Checks are green on the release's commit", Skipped, "not required here", false)
	}
	switch {
	case !g.RequireNoBlock:
		add("findings", "No blocking findings are open", Skipped, "not required here", false)
	case f.BlockingFindings > 0:
		add("findings", "No blocking findings are open", Fail, fmt.Sprintf("%d open", f.BlockingFindings), false)
	default:
		add("findings", "No blocking findings are open", Pass, "", false)
	}
	if g.PreviousAirfield != "" {
		switch {
		case !f.InPrevious:
			add("previous", "It has been in "+g.PreviousAirfield, Fail, "it has not reached "+g.PreviousAirfield+" yet", false)
		case !f.PreviousHealthy:
			add("previous", "It has been in "+g.PreviousAirfield, Fail, "it is in "+g.PreviousAirfield+" but not healthy there", false)
		default:
			add("previous", "It has been in "+g.PreviousAirfield, Pass, "since "+f.PreviousSince.UTC().Format("2006-01-02 15:04 MST"), false)
		}
		if g.SoakMinutes > 0 && f.InPrevious {
			ready := f.PreviousSince.Add(time.Duration(g.SoakMinutes) * time.Minute)
			if f.Now.Before(ready) {
				add("soak", fmt.Sprintf("Soaked %s in %s", dur(g.SoakMinutes), g.PreviousAirfield), Waiting,
					"ready at "+ready.UTC().Format("2006-01-02 15:04 MST"), true)
			} else {
				add("soak", fmt.Sprintf("Soaked %s in %s", dur(g.SoakMinutes), g.PreviousAirfield), Pass, "", true)
			}
		}
	} else if g.SoakMinutes > 0 {
		add("soak", "Soak time", Skipped, "there is no previous Airfield to soak in", false)
	}
	if g.Clearances > 0 {
		if len(f.Clearances) >= g.Clearances {
			add("clearance", fmt.Sprintf("%d clearance(s) from %s", g.Clearances, roleList(g.ClearanceRoles)), Pass, strings.Join(f.Clearances, ", "), false)
		} else {
			add("clearance", fmt.Sprintf("%d clearance(s) from %s", g.Clearances, roleList(g.ClearanceRoles)), Waiting,
				fmt.Sprintf("%d of %d given", len(f.Clearances), g.Clearances), false)
		}
	}
	if g.Window != nil && !g.Window.empty() {
		if ok, next := g.Window.Allows(f.Now); ok {
			add("window", "Inside the change window", Pass, "", true)
		} else {
			d := "outside the window"
			if !next.IsZero() {
				d += "; it opens " + next.UTC().Format("2006-01-02 15:04 MST")
			}
			add("window", "Inside the change window", Waiting, d, true)
		}
	}
	frozen := ""
	for _, fr := range f.Freezes {
		if fr.Covers(f.Project, f.Airfield, f.Now) {
			frozen = fr.Reason + " (until " + time.UnixMilli(fr.To).UTC().Format("2006-01-02 15:04 MST") + ")"
			break
		}
	}
	if frozen != "" {
		add("freeze", "No freeze is in force", Fail, frozen, true)
	} else {
		add("freeze", "No freeze is in force", Pass, "", true)
	}
	return out
}

func roleList(r []string) string {
	if len(r) == 0 {
		return "an operator or administrator"
	}
	return strings.Join(r, " or ")
}

func dur(min int) string {
	switch {
	case min%1440 == 0:
		return fmt.Sprintf("%d day(s)", min/1440)
	case min%60 == 0:
		return fmt.Sprintf("%d hour(s)", min/60)
	}
	return fmt.Sprintf("%d minute(s)", min)
}

// Ready reports whether every line passes (or is skipped).
func Ready(items []Item) bool {
	for _, i := range items {
		if i.Status == Fail || i.Status == Waiting {
			return false
		}
	}
	return true
}

// Blockers returns the lines that stop a promotion and whether every one of
// them may be overridden by an administrator.
func Blockers(items []Item) (list []Item, allOverridable bool) {
	allOverridable = true
	for _, i := range items {
		if i.Status == Fail || i.Status == Waiting {
			list = append(list, i)
			if !i.Overridable {
				allOverridable = false
			}
		}
	}
	return list, allOverridable
}

// SortFreezes orders freezes by start.
func SortFreezes(f []Freeze) {
	sort.SliceStable(f, func(i, j int) bool { return f[i].From < f[j].From })
}
