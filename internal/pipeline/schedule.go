package pipeline

// Schedules: on: { schedule: [ { cron: "0 3 * * *", timezone:
// America/Chicago, ref: main, inputs: { ... } } ] }.
//
// A cron expression has five fields: minute (0-59), hour (0-23), day of
// the month (1-31), month (1-12 or JAN-DEC) and day of the week (0-7 or
// SUN-SAT; 0 and 7 are Sunday). Each field is *, a value, a range a-b, a
// list a,b,c, or any of those with a step (*/15, 1-30/2, 5/10). When both
// day fields are restricted, a day matches if either does, as cron has
// always done. @hourly, @daily, @weekly, @monthly and @yearly are short for
// the usual expressions.
//
// Times are wall-clock times in the schedule's time zone (UTC if none is
// given). When the clocks go forward, a time that does not exist that day
// is skipped; when they go back, a time that happens twice fires once, the
// first time.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // time zones are part of the program, so schedules mean the same everywhere

	"gopkg.in/yaml.v3"
)

// Limits on schedules.
const (
	MaxSchedules = 20 // per pipeline
	maxCronLen   = 200
)

// Schedule is one entry of on.schedule.
type Schedule struct {
	Cron     string `json:"cron"`
	Timezone string `json:"timezone,omitempty"` // an IANA name; empty is UTC
	// Ref is the branch or tag whose pipeline runs; empty is the project's
	// default branch.
	Ref string `json:"ref,omitempty"`
	// Inputs are the values the run is started with, canonical; inputs not
	// listed take their defaults.
	Inputs map[string]string `json:"inputs,omitempty"`
	Line   int               `json:"-"`

	cron *Cron
	loc  *time.Location
	// raw inputs as written, checked once the pipeline's inputs are known
	rawInputs map[string]*yaml.Node
}

// Key names the schedule by what it says, so that the same entry is the
// same schedule from one commit to the next, and a changed entry is a new
// one.
func (s *Schedule) Key() string {
	names := make([]string, 0, len(s.Inputs))
	for n := range s.Inputs {
		names = append(names, n)
	}
	slices.Sort(names)
	parts := []any{s.Cron, s.Timezone, s.Ref}
	for _, n := range names {
		parts = append(parts, n, s.Inputs[n])
	}
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// Location is the schedule's time zone.
func (s *Schedule) Location() *time.Location {
	if s.loc == nil {
		s.loc = time.UTC
		if s.Timezone != "" {
			if l, err := time.LoadLocation(s.Timezone); err == nil {
				s.loc = l
			}
		}
	}
	return s.loc
}

// Next is the first time after t that the schedule fires; the zero time if
// it never does.
func (s *Schedule) Next(t time.Time) time.Time {
	if s.cron == nil {
		c, err := ParseCron(s.Cron)
		if err != nil {
			return time.Time{}
		}
		s.cron = c
	}
	return s.cron.Next(t, s.Location())
}

// Latest is the last time at or before now that the schedule fired, at or
// after from; the zero time if there was none.
func (s *Schedule) Latest(from, now time.Time) time.Time {
	var last time.Time
	t := from.Add(-time.Minute)
	for range maxLatestSteps {
		n := s.Next(t)
		if n.IsZero() || n.After(now) {
			return last
		}
		last, t = n, n
	}
	return last
}

// maxLatestSteps bounds Latest: a schedule that fires every minute and was
// not looked at for most of a year still takes only a moment.
const maxLatestSteps = 600_000

// Cron is a parsed cron expression.
type Cron struct {
	minute, hour, dom, month, dow [64]bool
	domAny, dowAny                bool
}

var cronMacros = map[string]string{
	"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *",
	"@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *",
}

var (
	monthNames = []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}
	dayNames   = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}
)

// ParseCron reads a five-field cron expression, or one of the @ macros.
func ParseCron(expr string) (*Cron, error) {
	expr = strings.TrimSpace(expr)
	if len(expr) > maxCronLen {
		return nil, fmt.Errorf("the expression is longer than %d characters", maxCronLen)
	}
	if strings.HasPrefix(expr, "@") {
		m, ok := cronMacros[strings.ToLower(expr)]
		if !ok {
			return nil, fmt.Errorf("%q is not one of @hourly, @daily, @weekly, @monthly, @yearly", expr)
		}
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("a cron expression has five fields (minute hour day-of-month month day-of-week); %q has %d", expr, len(f))
	}
	c := &Cron{}
	specs := []struct {
		name     string
		lo, hi   int
		names    []string
		nameBase int
		into     *[64]bool
	}{
		{"minute", 0, 59, nil, 0, &c.minute},
		{"hour", 0, 23, nil, 0, &c.hour},
		{"day of the month", 1, 31, nil, 0, &c.dom},
		{"month", 1, 12, monthNames, 1, &c.month},
		{"day of the week", 0, 7, dayNames, 0, &c.dow},
	}
	for i, sp := range specs {
		if err := parseCronField(f[i], sp.lo, sp.hi, sp.names, sp.nameBase, sp.into); err != nil {
			return nil, fmt.Errorf("the %s field %q: %v", sp.name, f[i], err)
		}
	}
	if c.dow[7] {
		c.dow[0] = true
	}
	c.domAny = strings.HasPrefix(f[2], "*")
	c.dowAny = strings.HasPrefix(f[4], "*")
	// A schedule that never fires (February 30th) is a mistake.
	if c.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC).IsZero() {
		return nil, fmt.Errorf("%q never happens", expr)
	}
	return c, nil
}

func parseCronField(field string, lo, hi int, names []string, base int, into *[64]bool) error {
	value := func(s string) (int, error) {
		for i, n := range names {
			if strings.EqualFold(s, n) {
				return i + base, nil
			}
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", s)
		}
		if v < lo || v > hi {
			return 0, fmt.Errorf("%d is outside %d-%d", v, lo, hi)
		}
		return v, nil
	}
	for _, item := range strings.Split(field, ",") {
		rng, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || n > hi {
				return fmt.Errorf("the step %q is not a number from 1 to %d", stepText, hi)
			}
			step = n
		}
		first, last := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if first, err = value(a); err != nil {
				return err
			}
			if last, err = value(b); err != nil {
				return err
			}
			if first > last {
				return fmt.Errorf("the range %s goes backwards", rng)
			}
		default:
			v, err := value(rng)
			if err != nil {
				return err
			}
			first = v
			if !hasStep {
				last = v
			}
		}
		for v := first; v <= last; v += step {
			into[v] = true
		}
	}
	return nil
}

func (c *Cron) dayMatches(t time.Time) bool {
	dom, dow := c.dom[t.Day()], c.dow[int(t.Weekday())]
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	}
	return dom || dow
}

// Next is the first minute after t, in loc, that the expression matches;
// the zero time if there is none within eight years.
func (c *Cron) Next(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(8, 0, 0)
	for t.Before(limit) {
		switch {
		case !c.month[int(t.Month())]:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		case !c.hour[t.Hour()]:
			n := time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			if !n.After(t) {
				n = t.Add(time.Minute)
			}
			t = n
		case !c.minute[t.Minute()]:
			t = t.Add(time.Minute)
		case repeatedWallTime(t):
			// The clocks went back and this wall-clock time has already
			// happened once today: it fired then.
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// repeatedWallTime reports whether the wall-clock time of t already
// happened earlier the same day, because the clocks went back.
func repeatedWallTime(t time.Time) bool {
	for _, back := range []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour} {
		p := t.Add(-back).In(t.Location())
		if p.Hour() == t.Hour() && p.Minute() == t.Minute() && p.Day() == t.Day() {
			return true
		}
	}
	return false
}

// ---- reading schedules ----

var scheduleKeys = []string{"cron", "timezone", "ref", "inputs"}

func (ps *parser) parseSchedules(n *yaml.Node, p *Pipeline) {
	if n.Kind != yaml.SequenceNode {
		ps.add(n, "on.schedule", "schedule is a list, for example: schedule: [ { cron: \"0 3 * * *\", timezone: Europe/Berlin } ]")
		return
	}
	if len(n.Content) == 0 {
		ps.add(n, "on.schedule", "list at least one schedule")
	}
	if len(n.Content) > MaxSchedules {
		ps.add(n, "on.schedule", "a pipeline may have at most %d schedules", MaxSchedules)
		return
	}
	for i, v := range n.Content {
		at := fmt.Sprintf("on.schedule[%d]", i)
		s := &Schedule{Line: v.Line}
		ps.eachKey(v, at, scheduleKeys, func(k, v2 *yaml.Node) {
			where := at + "." + k.Value
			switch k.Value {
			case "cron":
				s.Cron = strings.TrimSpace(ps.str(v2, where))
				c, err := ParseCron(s.Cron)
				if err != nil {
					ps.add(v2, where, "%v", err)
				}
				s.cron = c
			case "timezone":
				s.Timezone = ps.str(v2, where)
				if l, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" || s.Timezone == "Local" {
					ps.add(v2, where, "%q is not a time zone; write an IANA name such as Europe/Berlin or America/Chicago, or leave it out for UTC", clipText(s.Timezone, 60))
				} else {
					s.loc = l
				}
			case "ref":
				s.Ref = ps.str(v2, where)
				if !ValidVersion(s.Ref) {
					ps.add(v2, where, "name a branch or tag, such as main")
				}
			case "inputs":
				if v2.Kind != yaml.MappingNode {
					ps.add(v2, where, "inputs is a mapping of input names to values")
					return
				}
				s.rawInputs = map[string]*yaml.Node{}
				for j := 0; j+1 < len(v2.Content); j += 2 {
					name := v2.Content[j]
					if _, dup := s.rawInputs[name.Value]; dup {
						ps.add(name, where+"."+name.Value, "%q is written twice", name.Value)
						continue
					}
					s.rawInputs[name.Value] = v2.Content[j+1]
				}
			}
		})
		if v.Kind == yaml.MappingNode && s.Cron == "" {
			ps.add(v, at+".cron", "say when, for example: cron: \"0 3 * * *\" (03:00 every day)")
		}
		p.On.Schedule = append(p.On.Schedule, s)
	}
	seen := map[string]int{}
	for i, s := range p.On.Schedule {
		if s.rawInputs != nil || s.cron == nil {
			continue // compared once inputs are canonical (checkScheduleInputs)
		}
		if j, dup := seen[s.Key()]; dup {
			ps.problems = append(ps.problems, Problem{Line: s.Line, Path: fmt.Sprintf("on.schedule[%d]", i), Message: fmt.Sprintf("this is the same schedule as on.schedule[%d]", j)})
		}
		seen[s.Key()] = i
	}
}

// checkScheduleInputs checks the values each schedule gives, now that the
// pipeline's inputs are known.
func (ps *parser) checkScheduleInputs(p *Pipeline) {
	seen := map[string]int{}
	for i, s := range p.On.Schedule {
		at := fmt.Sprintf("on.schedule[%d].inputs", i)
		if len(s.rawInputs) > 0 {
			s.Inputs = map[string]string{}
		}
		for name, n := range s.rawInputs {
			in := p.Input(name)
			if in == nil {
				ps.add(n, at+"."+name, "there is no input called %q", clipText(name, 60))
				continue
			}
			raw, ok := ps.inputLiteral(n, at+"."+name, in.Type)
			if !ok {
				continue
			}
			v, msg := in.Canon(raw)
			if msg != "" {
				ps.add(n, at+"."+name, "%s: %s", name, msg)
				continue
			}
			s.Inputs[name] = v
		}
		if s.rawInputs == nil || s.cron == nil {
			continue
		}
		if j, dup := seen[s.Key()]; dup {
			ps.problems = append(ps.problems, Problem{Line: s.Line, Path: fmt.Sprintf("on.schedule[%d]", i), Message: fmt.Sprintf("this is the same schedule as on.schedule[%d]", j)})
		}
		seen[s.Key()] = i
	}
}
