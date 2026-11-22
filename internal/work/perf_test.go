package work_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// seedMany writes n items directly (the way an import of a large backlog
// would end up in the tables), so that the timings below are of reading.
func seedMany(t *testing.T, s *work.Store, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Prefix(ctx, proj); err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	tx, err := s.BeginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	states := []string{"backlog", "ready", "in_progress", "in_review", "done", "wont_do"}
	types := []string{"issue", "bug", "story", "task"}
	labels := []string{"ui", "api", "docs", "perf", "security"}
	now := time.Now().UnixMilli()
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("wi_perf%07d", i)
		if _, err := tx.Exec(ctx, `INSERT INTO work_items(id, scope_kind, scope_id, number, type, title, body, state, resolution, priority, estimate_milli, rank, reporter, created_at, updated_at, closed_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, proj.Kind, proj.ID, i, types[i%4], fmt.Sprintf("Item %d about %s", i, labels[i%5]), "Body text number "+fmt.Sprint(i)+" lorem ipsum", states[i%6], "", "normal", 3000, int64(i)*1024, "ada", now-int64(i)*1000, now-int64(i)*500, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_labels(item_id, label) VALUES(?,?)`, id, labels[i%5]); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO work_assignees(item_id, who, kind) VALUES(?,?,?)`, id, "bob", "person"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE work_scopes SET next_number = ? WHERE scope_kind = ? AND scope_id = ?`, n+1, proj.Kind, proj.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = db
}

// 50,000 items: a page of the list, a search, a filtered query, the board,
// and a new item all answer well within a second or two, on SQLite and (with
// LAUNCHWAY_TEST_POSTGRES) PostgreSQL.
func TestFiftyThousandItems(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 50,000 items")
	}
	s, ctx := newStore(t), context.Background()
	seedMany(t, s, 50000)
	timed := func(name string, limit time.Duration, f func() error) {
		t.Helper()
		start := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d := time.Since(start)
		t.Logf("%s: %v", name, d.Round(time.Millisecond))
		if d > limit {
			t.Errorf("%s took %v, over %v", name, d, limit)
		}
	}
	timed("list a page", 2*time.Second, func() error {
		p, err := s.List(ctx, bob, work.ListOptions{Scope: &proj, Limit: 50})
		if err == nil && (p.Total != 50000 || len(p.Items) != 50) {
			err = fmt.Errorf("%d %d", p.Total, len(p.Items))
		}
		return err
	})
	timed("list sorted by update, deep page", 2*time.Second, func() error {
		_, err := s.List(ctx, bob, work.ListOptions{Scope: &proj, Limit: 50, Offset: 40000, Sort: "updated", Desc: true})
		return err
	})
	timed("filter by state and label and assignee", 2*time.Second, func() error {
		p, err := s.List(ctx, bob, work.ListOptions{Scope: &proj, Query: "state:in_progress label:ui assignee:bob", Limit: 50})
		_ = p
		return err
	})
	timed("text search", 3*time.Second, func() error {
		p, err := s.List(ctx, bob, work.ListOptions{Scope: &proj, Query: "lorem number 49999", Limit: 50})
		if err == nil && p.Total != 1 {
			err = fmt.Errorf("found %d", p.Total)
		}
		return err
	})
	timed("is:blocked", 2*time.Second, func() error {
		_, err := s.List(ctx, bob, work.ListOptions{Scope: &proj, Query: "is:blocked", Limit: 50})
		return err
	})
	timed("board (six columns)", 3*time.Second, func() error {
		b, err := s.Board(ctx, bob, work.BoardOptions{Scope: proj, LaneBy: "assignee", PerColumn: 50})
		if err == nil && b.Columns[0].Count == 0 {
			err = fmt.Errorf("empty")
		}
		return err
	})
	// A create is one write, so its time includes the disk's flush. The limit guards against work that
	// grows with the number of items (seconds, at fifty thousand), not against a busy disk: on a shared
	// build disk under load a single flush has been measured at 0.9 s.
	timed("create one more", 2*time.Second, func() error {
		it, err := s.Create(ctx, bob, work.NewItem{Scope: proj, Title: "late"})
		if err == nil && it.Number != 50001 {
			err = fmt.Errorf("number %d", it.Number)
		}
		return err
	})
	timed("move to top", time.Second, func() error {
		_, err := s.Rank(ctx, ada, "SHOP-50001", work.Position{Where: "top"})
		return err
	})
	timed("cumulative flow, 30 days", 3*time.Second, func() error {
		_, err := s.CFD(ctx, bob, proj, 30)
		return err
	})
}
