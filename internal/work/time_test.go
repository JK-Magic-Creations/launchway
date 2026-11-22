package work_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func TestTimersEntriesTotalsAndRemaining(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	clk := withClock(s)
	a := mustCreate(t, s, ada, work.NewItem{Title: "Crash", Estimate: 4})
	b := mustCreate(t, s, ada, work.NewItem{Title: "Slow"})

	// One running timer per person.
	if _, err := s.StartTimer(ctx, ada, a.Ref); err != nil {
		t.Fatal(err)
	}
	_, err := s.StartTimer(ctx, ada, b.Ref)
	wantErr(t, err, "already have a timer running on "+a.Ref)
	if _, err := s.StartTimer(ctx, bob, b.Ref); err != nil {
		t.Fatalf("another person's timer: %v", err)
	}
	if r, err := s.RunningTimerOf(ctx, ada); err != nil || r == nil || r.Item != a.Ref {
		t.Fatalf("running: %+v %v", r, err)
	}
	clk.advance(37*time.Minute + 20*time.Second)
	e, err := s.StopTimer(ctx, ada, "found it")
	if err != nil || e.Minutes != 37 || e.Note != "found it" || e.Kind != "timer" || e.Capped {
		t.Fatalf("stop: %+v %v", e, err)
	}
	if _, err := s.StopTimer(ctx, ada, ""); err == nil {
		t.Fatal("stopped a timer that was not running")
	}
	if r, _ := s.RunningTimerOf(ctx, ada); r != nil {
		t.Fatalf("still running: %+v", r)
	}
	// A timer stopped at once counts for a minute; one left for days counts for a day and says so.
	clk.advance(3 * 24 * time.Hour)
	long, err := s.StopTimer(ctx, bob, "")
	if err != nil || long.Minutes != work.MaxTimeMinutes || !long.Capped {
		t.Fatalf("a forgotten timer: %+v %v", long, err)
	}

	// Manual entries, with rules.
	m, err := s.LogTime(ctx, ada, a.Ref, 90, "2026-03-01", "review")
	if err != nil || m.Day != "2026-03-01" {
		t.Fatalf("log: %+v %v", m, err)
	}
	for _, bad := range []struct {
		min      int
		day, why string
	}{{0, "", "1 to"}, {1441, "", "1 to"}, {5, "yesterday", "written"}, {5, "2030-01-01", "has not come"}} {
		_, err := s.LogTime(ctx, ada, a.Ref, bad.min, bad.day, "")
		wantErr(t, err, bad.why)
	}
	_, err = s.LogTime(ctx, ada, a.Ref, 5, "", strings.Repeat("x", work.MaxTimeNote+1))
	wantErr(t, err, "at most")
	if _, err := s.LogTime(ctx, carol, a.Ref, 5, "", ""); err == nil {
		t.Fatal("a reader logged time")
	}
	agent := ada
	agent.Kind = work.KindAgent
	if _, err := s.LogTime(ctx, agent, a.Ref, 5, "", ""); err == nil {
		t.Fatal("an agent logged time")
	}

	// Totals per item: 37 + 90 = 127 minutes of the 4 hours; per person.
	tl, err := s.TimeOn(ctx, carol, a.Ref) // a reader sees it
	if err != nil || tl.TotalMinutes != 127 || len(tl.Entries) != 2 || len(tl.ByPerson) != 1 || tl.EstimateUnit != "points" || tl.RemainingHours != nil || !strings.Contains(tl.Note, "points") {
		t.Fatalf("time: %+v %v", tl, err)
	}
	// In hours the remaining time is the estimate less the logged time.
	if err := s.PutSettings(ctx, proj, work.Settings{Unit: "hours"}, work.Actor{Name: "ada", Kind: work.KindPerson}); err != nil {
		t.Fatal(err)
	}
	tl, err = s.TimeOn(ctx, ada, a.Ref)
	if err != nil || tl.EstimateUnit != "hours" || tl.RemainingHours == nil || *tl.RemainingHours != 1.88 || len(tl.Running) != 0 {
		t.Fatalf("remaining: %+v %v", tl, err)
	}
	over, _ := s.LogTime(ctx, ada, a.Ref, 200, "2026-03-02", "")
	_ = over
	tl, _ = s.TimeOn(ctx, ada, a.Ref)
	if tl.RemainingHours == nil || *tl.RemainingHours >= 0 {
		t.Fatalf("past the estimate the remaining time is negative: %+v", tl.RemainingHours)
	}
}

func TestTimeEntriesAreYoursToChangeAndTheReportKeepsToWhatYouMaySee(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	withClock(s)
	a := mustCreate(t, s, ada, work.NewItem{Title: "Crash"})
	secret := mustCreate(t, s, ada, work.NewItem{Title: "Vulnerability =cmd", Confidential: true})
	e1, _ := s.LogTime(ctx, ada, a.Ref, 60, "2026-03-01", `=HYPERLINK("x"),note`)
	e2, _ := s.LogTime(ctx, bob, a.Ref, 30, "2026-03-02", "")
	hidden, _ := s.LogTime(ctx, ada, secret.Ref, 45, "2026-03-02", "")

	if _, err := s.EditTime(ctx, bob, e1.ID, ptr(5), nil, nil); err == nil {
		t.Fatal("edited somebody else's entry")
	}
	if err := s.DeleteTime(ctx, bob, e1.ID); err == nil {
		t.Fatal("deleted somebody else's entry")
	}
	got, err := s.EditTime(ctx, ada, e1.ID, ptr(75), ptr("2026-03-03"), ptr("changed"))
	if err != nil || got.Minutes != 75 || got.Day != "2026-03-03" || got.Note != "changed" {
		t.Fatalf("edit: %+v %v", got, err)
	}
	if _, err := s.EditTime(ctx, ada, e1.ID, ptr(0), nil, nil); err == nil {
		t.Fatal("zero minutes")
	}

	// The report: bob (who cannot see the confidential item) never meets its time, and a reader sees the rest.
	reader := person("carol", false, false)
	reader.Read = func(sc work.Scope) bool { return true }
	rep, err := s.TimeReport(ctx, bob, work.TimeQuery{})
	if err != nil || rep.TotalMinutes != 105 || len(rep.People) != 2 || rep.People[0].Who != "ada" {
		t.Fatalf("report: %+v %v", rep, err)
	}
	for _, e := range rep.Entries {
		if e.ID == hidden.ID {
			t.Fatal("the time of an item bob may not see is in his report")
		}
	}
	all, _ := s.TimeReport(ctx, ada, work.TimeQuery{})
	if all.TotalMinutes != 150 {
		t.Fatalf("the maintainer's report: %d", all.TotalMinutes)
	}
	one, _ := s.TimeReport(ctx, ada, work.TimeQuery{Who: "bob", From: "2026-03-02", To: "2026-03-02"})
	if one.TotalMinutes != 30 || len(one.Entries) != 1 || one.Entries[0].ID != e2.ID {
		t.Fatalf("one person and day: %+v", one)
	}
	if _, err := s.TimeReport(ctx, ada, work.TimeQuery{From: "soon"}); err == nil {
		t.Fatal("a day that is not one")
	}
	if _, err := s.LogTime(ctx, bob, a.Ref, 5, "2026-03-04", `=HYPERLINK("x"),note`); err != nil {
		t.Fatal(err)
	}
	all, _ = s.TimeReport(ctx, ada, work.TimeQuery{})
	csv := work.TimeCSV(all)
	if !strings.HasPrefix(csv, "day,person,item,title,minutes,hours,kind,note\r\n") || strings.Contains(csv, ",=") || !strings.Contains(csv, `"'=HYPERLINK(""x""),note"`) {
		t.Fatalf("csv:\n%s", csv)
	}
	if err := s.DeleteTime(ctx, ada, e1.ID); err != nil {
		t.Fatal(err)
	}
	if rep, _ := s.TimeReport(ctx, ada, work.TimeQuery{}); rep.TotalMinutes != 80 {
		t.Fatalf("after delete: %d", rep.TotalMinutes)
	}
}
