package plan

import (
	"fmt"
	"strings"
	"time"
)

var days = map[string]time.Weekday{"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6}

func (s Schedule) loc() *time.Location {
	if s.Timezone == "" {
		return time.UTC
	}
	l, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.UTC
	}
	return l
}

// lastSlot is the most recent scheduled moment at or before now.
func lastSlot(now time.Time, s Schedule) time.Time {
	l := s.loc()
	n := now.In(l)
	slot := time.Date(n.Year(), n.Month(), n.Day(), s.Hour, 0, 0, 0, l)
	switch s.Interval {
	case "daily":
		if slot.After(n) {
			slot = slot.AddDate(0, 0, -1)
		}
	case "monthly":
		slot = time.Date(n.Year(), n.Month(), 1, s.Hour, 0, 0, 0, l)
		if slot.After(n) {
			slot = slot.AddDate(0, -1, 0)
		}
	default:
		want := days[strings.ToLower(s.Day)]
		if s.Day == "" {
			want = time.Monday
		}
		back := (int(n.Weekday()) - int(want) + 7) % 7
		slot = slot.AddDate(0, 0, -back)
		if slot.After(n) {
			slot = slot.AddDate(0, 0, -7)
		}
	}
	return slot
}

// Due says whether routine updates should be looked for now: a scheduled
// moment has passed since the last run.
func Due(now time.Time, s Schedule, last time.Time) bool {
	return last.Before(lastSlot(now, s))
}

// Describe says the schedule in words.
func Describe(s Schedule) string {
	tz := s.Timezone
	if tz == "" {
		tz = "UTC"
	}
	at := fmt.Sprintf("%02d:00 %s", s.Hour, tz)
	switch s.Interval {
	case "daily":
		return "every day at " + at
	case "monthly":
		return "on the first of each month at " + at
	}
	d := s.Day
	if d == "" {
		d = "monday"
	}
	return "every " + d + " at " + at
}
