package engine

import (
	"testing"
	"time"
)

func TestActivityTrailKeepsOneEntryPerObjectAndExpires(t *testing.T) {
	e, clk := newEngine(t)
	rec := func(user, obj, action string) {
		t.Helper()
		if err := e.RecordActivity(user, Activity{Kind: "run", Obj: obj, Project: "api", Title: obj, Href: "/runs/" + obj, Action: action}, 90); err != nil {
			t.Fatal(err)
		}
	}
	rec("ana", "r1", "started")
	clk.Add(time.Minute)
	rec("ana", "r1", "viewed") // seen again: one entry, and the stronger action is kept
	rec("bob", "r1", "viewed")
	list, _ := e.ActivityOf("ana", 90, 10)
	if len(list) != 1 || list[0].Action != "started" {
		t.Fatalf("ana's trail: %+v", list)
	}
	if other, _ := e.ActivityOf("bob", 90, 10); len(other) != 1 || other[0].Action != "viewed" {
		t.Fatalf("bob's trail is his own: %+v", other)
	}
	if err := e.RecordActivity("ana", Activity{Kind: "run", Obj: "x", Title: "x", Href: "https://elsewhere/x"}, 90); err == nil {
		t.Error("an address off this server is refused")
	}
	// Retention: an entry older than the limit is not listed and is deleted by the next write.
	clk.Add(100 * 24 * time.Hour)
	if list, _ := e.ActivityOf("ana", 90, 10); len(list) != 0 {
		t.Errorf("an entry older than 90 days is listed: %+v", list)
	}
	rec("ana", "r2", "viewed")
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM org_activity WHERE username='ana'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the old entry should be deleted by the next write: %d %v", n, err)
	}
	if err := e.ClearActivity("ana"); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.ActivityOf("ana", 90, 10); len(list) != 0 {
		t.Errorf("after clearing: %+v", list)
	}
	if other, _ := e.ActivityOf("bob", 0, 10); len(other) != 1 {
		t.Errorf("clearing ana's trail must not touch bob's: %+v", other)
	}
}

func TestActivityTrailIsCapped(t *testing.T) {
	e, clk := newEngine(t)
	for i := 0; i < activityKeep+30; i++ {
		clk.Add(time.Second)
		if err := e.RecordActivity("ana", Activity{Kind: "page", Obj: string(rune('a'+i%26)) + time.Duration(i).String(), Title: "t", Href: "/x"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := e.ActivityOf("ana", 0, 0); len(list) > activityKeep {
		t.Errorf("%d entries kept, the cap is %d", len(list), activityKeep)
	}
}
