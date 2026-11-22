package work

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// Time on work items: a person starts a timer on an item (one running timer per person) and stops it, or logs a
// manual entry with a note. The entries of an item add up per person; where the level's estimate is in hours the
// item says how much of it is left (the estimate less what was logged); where it is in points no remaining time is
// worked out, because points are not hours. Anyone who can see an item sees its time; to log time one needs the
// right to change work in its scope, and an agent does not log. A person edits and deletes only their own entries.

// Limits.
const (
	MaxTimeMinutes = 24 * 60 // one entry, and the most a forgotten timer counts for
	MaxTimeNote    = 500
	// MaxTimeExport is how many entries one report or export holds.
	MaxTimeExport = 20000
)

// TimeEntry is one entry as the API shows it.
type TimeEntry struct {
	ID      string `json:"id"`
	Item    string `json:"item"`
	Title   string `json:"title,omitempty"`
	Who     string `json:"who"`
	Kind    string `json:"kind"` // timer or manual
	Day     string `json:"day"`
	Minutes int    `json:"minutes"`
	Note    string `json:"note,omitempty"`
	// Started and Ended are the times of a timer; Capped says a timer was left running past a day and counts for one.
	Started string `json:"started_at,omitempty"`
	Ended   string `json:"ended_at,omitempty"`
	Capped  bool   `json:"capped,omitempty"`
}

// RunningTimer is a timer that has not been stopped.
type RunningTimer struct {
	ID      string `json:"id"`
	Item    string `json:"item"`
	Title   string `json:"title,omitempty"`
	Who     string `json:"who"`
	Started string `json:"started_at"`
	Seconds int64  `json:"seconds"`
}

// PersonMinutes is a person's logged time.
type PersonMinutes struct {
	Who     string `json:"who"`
	Minutes int    `json:"minutes"`
}

// TimeLog is everything about the time on one item.
type TimeLog struct {
	Item         string          `json:"item"`
	Entries      []TimeEntry     `json:"entries"`
	TotalMinutes int             `json:"total_minutes"`
	ByPerson     []PersonMinutes `json:"by_person"`
	Running      []RunningTimer  `json:"running"`
	Estimate     float64         `json:"estimate,omitempty"`
	EstimateUnit string          `json:"estimate_unit"`
	// RemainingHours is the estimate less the logged time, when the estimate is in hours (it may be negative: over);
	// nil when there is no estimate or the unit is points, with Note saying why.
	RemainingHours *float64 `json:"remaining_hours,omitempty"`
	Note           string   `json:"note,omitempty"`
}

func (s *Store) timeActor(c Caller) error {
	if c.Agent != nil || c.Kind == KindAgent {
		return forbidden("an agent does not log time")
	}
	return nil
}

func dayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

func cleanNote(n string) (string, error) {
	n = strings.TrimSpace(cleanBody(n))
	if len(n) > MaxTimeNote {
		return "", invalid("a note is at most %d bytes", MaxTimeNote)
	}
	return n, nil
}

func (s *Store) checkDay(day string) (string, error) {
	if day == "" {
		return dayOf(s.now()), nil
	}
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return "", invalid("a day is written 2026-12-31")
	}
	if d.After(s.now().Add(48 * time.Hour)) {
		return "", invalid("time is not logged for a day that has not come")
	}
	return day, nil
}

func entryOf(t store.TimeRow, ref, title string) TimeEntry {
	e := TimeEntry{ID: t.ID, Item: ref, Title: title, Who: t.Who, Kind: t.Kind, Day: t.Day, Minutes: t.Minutes, Note: t.Note}
	if t.Kind == "timer" {
		e.Started, e.Ended = iso(t.StartedAt), iso(t.EndedAt)
		e.Capped = t.Minutes >= MaxTimeMinutes && t.EndedAt-t.StartedAt > int64(MaxTimeMinutes)*60_000
	}
	return e
}

// TimeOn reads the time on an item.
func (s *Store) TimeOn(ctx context.Context, c Caller, ref string) (*TimeLog, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	rows, err := store.TimeOfItem(ctx, s.db, it.ID)
	if err != nil {
		return nil, err
	}
	out := &TimeLog{Item: it.Ref, Entries: []TimeEntry{}, ByPerson: []PersonMinutes{}, Running: []RunningTimer{}, Estimate: it.Estimate}
	per := map[string]int{}
	for _, r := range rows {
		out.Entries = append(out.Entries, entryOf(r, it.Ref, ""))
		out.TotalMinutes += r.Minutes
		per[r.Who] += r.Minutes
	}
	out.ByPerson = sortedPeople(per)
	run, err := store.TimeRunningOn(ctx, s.db, it.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range run {
		out.Running = append(out.Running, s.runningOf(r, it.Ref, it.Title))
	}
	eff, err := s.Settings(ctx, it.Scope)
	if err != nil {
		return nil, err
	}
	out.EstimateUnit = eff.Unit
	if out.EstimateUnit == "" {
		out.EstimateUnit = "points"
	}
	switch {
	case it.Estimate <= 0:
		out.Note = "the item has no estimate"
	case out.EstimateUnit != "hours":
		out.Note = "the estimate is in points, so no remaining time is worked out (set the level's estimate unit to hours to get it)"
	default:
		rem := math.Round((it.Estimate-float64(out.TotalMinutes)/60)*100) / 100
		out.RemainingHours = &rem
	}
	return out, nil
}

func sortedPeople(m map[string]int) []PersonMinutes {
	out := make([]PersonMinutes, 0, len(m))
	for w, n := range m {
		out = append(out, PersonMinutes{w, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Minutes != out[j].Minutes {
			return out[i].Minutes > out[j].Minutes
		}
		return out[i].Who < out[j].Who
	})
	return out
}

func (s *Store) runningOf(r store.TimeRow, ref, title string) RunningTimer {
	return RunningTimer{ID: r.ID, Item: ref, Title: title, Who: r.Who, Started: iso(r.StartedAt), Seconds: max(0, (ms(s.now())-r.StartedAt)/1000)}
}

func (s *Store) timeWritable(ctx context.Context, c Caller, ref string) (*Item, error) {
	if err := s.timeActor(c); err != nil {
		return nil, err
	}
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(it.Scope) {
		return nil, forbidden("you may not log time on work in %s", it.Scope)
	}
	return it, nil
}

// StartTimer starts the caller's timer on an item. A person has one running timer; starting another says where it is.
func (s *Store) StartTimer(ctx context.Context, c Caller, ref string) (*RunningTimer, error) {
	it, err := s.timeWritable(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	var out RunningTimer
	err = s.tx(ctx, func(tx DB) error {
		if cur, ok, err := store.TimeRunning(ctx, tx, c.Name); err != nil {
			return err
		} else if ok {
			where := cur.Item
			if other, err := s.load(ctx, tx, cur.Item); err == nil {
				where = other.Ref
			}
			return conflict("you already have a timer running on %s; stop it first", where)
		}
		now := ms(s.now())
		row := store.TimeRow{ID: newID("wt"), Item: it.ID, Who: c.Name, Kind: "timer", Day: dayOf(s.now()), StartedAt: now, CreatedAt: now, UpdatedAt: now}
		if err := store.TimeInsert(ctx, tx, row); err != nil {
			return err
		}
		out = s.runningOf(row, it.Ref, it.Title)
		return nil
	})
	return &out, err
}

// RunningTimer returns the caller's running timer, or nil.
func (s *Store) RunningTimerOf(ctx context.Context, c Caller) (*RunningTimer, error) {
	cur, ok, err := store.TimeRunning(ctx, s.db, c.Name)
	if err != nil || !ok {
		return nil, err
	}
	it, err := s.load(ctx, s.db, cur.Item)
	if err != nil {
		return nil, err
	}
	out := s.runningOf(cur, it.Ref, it.Title)
	return &out, nil
}

// StopTimer stops the caller's timer and keeps what it measured (a whole number of minutes, at least one, and at most
// a day: a timer left running for longer is counted as a day and marked, for the person to correct).
func (s *Store) StopTimer(ctx context.Context, c Caller, note string) (*TimeEntry, error) {
	note, err := cleanNote(note)
	if err != nil {
		return nil, err
	}
	cur, ok, err := store.TimeRunning(ctx, s.db, c.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound("you have no timer running")
	}
	it, err := s.load(ctx, s.db, cur.Item)
	if err != nil {
		return nil, err
	}
	end := ms(s.now())
	minutes := int(math.Round(float64(end-cur.StartedAt) / 60000))
	minutes = min(max(minutes, 1), MaxTimeMinutes)
	done, err := store.TimeFinish(ctx, s.db, cur.ID, end, minutes, note)
	if err != nil {
		return nil, err
	}
	if !done {
		return nil, conflict("the timer was stopped already")
	}
	got, _, err := store.TimeGet(ctx, s.db, cur.ID)
	if err != nil {
		return nil, err
	}
	e := entryOf(got, it.Ref, it.Title)
	return &e, nil
}

// LogTime adds a manual entry for the caller.
func (s *Store) LogTime(ctx context.Context, c Caller, ref string, minutes int, day, note string) (*TimeEntry, error) {
	it, err := s.timeWritable(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if minutes < 1 || minutes > MaxTimeMinutes {
		return nil, invalid("an entry is 1 to %d minutes", MaxTimeMinutes)
	}
	if day, err = s.checkDay(day); err != nil {
		return nil, err
	}
	if note, err = cleanNote(note); err != nil {
		return nil, err
	}
	now := ms(s.now())
	row := store.TimeRow{ID: newID("wt"), Item: it.ID, Who: c.Name, Kind: "manual", Day: day, Minutes: minutes, Note: note, CreatedAt: now, UpdatedAt: now}
	if err := store.TimeInsert(ctx, s.db, row); err != nil {
		return nil, err
	}
	e := entryOf(row, it.Ref, it.Title)
	return &e, nil
}

// ownEntry finds an entry that is the caller's, as seen through an item the caller may still see.
func (s *Store) ownEntry(ctx context.Context, c Caller, id string) (store.TimeRow, *Item, error) {
	row, ok, err := store.TimeGet(ctx, s.db, id)
	if err != nil {
		return row, nil, err
	}
	if !ok {
		return row, nil, notFound("no time entry " + id)
	}
	it, err := s.Get(ctx, c, row.Item)
	if err != nil {
		return row, nil, notFound("no time entry " + id)
	}
	if row.Who != c.Name {
		return row, nil, forbidden("you may change only your own time")
	}
	return row, it, nil
}

// EditTime changes the minutes, the day or the note of one of the caller's own finished entries; a field left out
// (nil) keeps its value.
func (s *Store) EditTime(ctx context.Context, c Caller, id string, minutes *int, day, note *string) (*TimeEntry, error) {
	if err := s.timeActor(c); err != nil {
		return nil, err
	}
	row, it, err := s.ownEntry(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if row.Kind == "timer" && row.EndedAt == 0 {
		return nil, conflict("the timer is still running; stop it first")
	}
	if minutes != nil {
		if *minutes < 1 || *minutes > MaxTimeMinutes {
			return nil, invalid("an entry is 1 to %d minutes", MaxTimeMinutes)
		}
		row.Minutes = *minutes
	}
	if day != nil {
		if row.Day, err = s.checkDay(*day); err != nil {
			return nil, err
		}
	}
	if note != nil {
		if row.Note, err = cleanNote(*note); err != nil {
			return nil, err
		}
	}
	if _, err := store.TimeUpdate(ctx, s.db, id, c.Name, row.Minutes, row.Note, row.Day, ms(s.now())); err != nil {
		return nil, err
	}
	e := entryOf(row, it.Ref, it.Title)
	return &e, nil
}

// DeleteTime removes one of the caller's own entries (or discards a running timer).
func (s *Store) DeleteTime(ctx context.Context, c Caller, id string) error {
	if err := s.timeActor(c); err != nil {
		return err
	}
	if _, _, err := s.ownEntry(ctx, c, id); err != nil {
		return err
	}
	_, err := store.TimeDelete(ctx, s.db, id, c.Name)
	return err
}

// TimeQuery narrows a report: a person (default: everyone), days (inclusive) and a scope.
type TimeQuery struct {
	Who      string
	From, To string
	Scope    *Scope
}

// TimeReport is the time over the items the caller may see.
type TimeReport struct {
	Entries      []TimeEntry    `json:"entries"`
	TotalMinutes int            `json:"total_minutes"`
	People       []PersonReport `json:"people"`
	Truncated    bool           `json:"truncated"`
}

// PersonReport is one person's total and where it went.
type PersonReport struct {
	Who     string        `json:"who"`
	Minutes int           `json:"minutes"`
	Items   []ItemMinutes `json:"items"`
}

// ItemMinutes is time on one item.
type ItemMinutes struct {
	Item    string `json:"item"`
	Title   string `json:"title"`
	Minutes int    `json:"minutes"`
}

// TimeReport adds up the logged time over the items the caller may see (never more than they may read).
func (s *Store) TimeReport(ctx context.Context, c Caller, q TimeQuery) (*TimeReport, error) {
	for _, d := range []string{q.From, q.To} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return nil, invalid("a day is written 2026-12-31")
			}
		}
	}
	var only []Scope
	if q.Scope != nil {
		only = append(only, *q.Scope)
	}
	vis, args, err := s.visibility(ctx, c, only)
	if err != nil {
		return nil, err
	}
	rows, err := store.TimeEntries(ctx, s.db, store.TimeFilter{Visible: vis, VisibleArgs: args, Who: q.Who, FromDay: q.From, ToDay: q.To}, MaxTimeExport+1)
	if err != nil {
		return nil, err
	}
	rep := &TimeReport{Entries: []TimeEntry{}, People: []PersonReport{}}
	if len(rows) > MaxTimeExport {
		rows, rep.Truncated = rows[:MaxTimeExport], true
	}
	prefixes := map[Scope]string{}
	type key struct{ who, item string }
	per, perItem, titles := map[string]int{}, map[key]int{}, map[string]string{}
	for _, r := range rows {
		sc := Scope{Kind: r.Scope, ID: r.ScopeID}
		p, ok := prefixes[sc]
		if !ok {
			if p, err = s.prefixIn(ctx, s.db, sc); err != nil {
				return nil, err
			}
			prefixes[sc] = p
		}
		ref := fmt.Sprintf("%s-%d", p, r.Number)
		rep.Entries = append(rep.Entries, entryOf(r.TimeRow, ref, r.Title))
		rep.TotalMinutes += r.Minutes
		per[r.Who] += r.Minutes
		perItem[key{r.Who, ref}] += r.Minutes
		titles[ref] = r.Title
	}
	for _, p := range sortedPeople(per) {
		pr := PersonReport{Who: p.Who, Minutes: p.Minutes, Items: []ItemMinutes{}}
		for k, n := range perItem {
			if k.who == p.Who {
				pr.Items = append(pr.Items, ItemMinutes{Item: k.item, Title: titles[k.item], Minutes: n})
			}
		}
		sort.Slice(pr.Items, func(i, j int) bool {
			if pr.Items[i].Minutes != pr.Items[j].Minutes {
				return pr.Items[i].Minutes > pr.Items[j].Minutes
			}
			return pr.Items[i].Item < pr.Items[j].Item
		})
		rep.People = append(rep.People, pr)
	}
	return rep, nil
}

// TimeCSV writes a report's entries as CSV: day, person, item, title, minutes, hours, kind, note. A cell that a
// spreadsheet could take for a formula gets a leading apostrophe.
func TimeCSV(rep *TimeReport) string {
	var b strings.Builder
	b.WriteString("day,person,item,title,minutes,hours,kind,note\r\n")
	for _, e := range rep.Entries {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%d,%.2f,%s,%s\r\n", csvCell(e.Day), csvCell(e.Who), csvCell(e.Item), csvCell(e.Title), e.Minutes, float64(e.Minutes)/60, csvCell(e.Kind), csvCell(e.Note))
	}
	return b.String()
}

func csvCell(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, v)
	if v != "" && strings.ContainsRune("=+-@", rune(v[0])) {
		v = "'" + v
	}
	if strings.ContainsAny(v, `",`) {
		v = `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}
	return v
}

// MinutesText writes minutes for an audit detail: "1 h 05 min".
func MinutesText(n int) string {
	if n < 60 {
		return fmt.Sprintf("%d min", n)
	}
	return fmt.Sprintf("%d h %02d min", n/60, n%60)
}
