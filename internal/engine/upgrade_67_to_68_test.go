package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 67 holds a work item with a comment and a code link; the next build applies exactly 68, keeps every row, and the new statements work against the upgraded data. The
// build of schema 67 still starts against the upgraded database.
func TestUpgradeFromSchema67KeepsEverythingAndAddsTheWorkTables(t *testing.T) {
	ctx := context.Background()
	if schemaOverview != 67 || schemaWorkExtras != 68 || schemaLast < 68 {
		t.Fatalf("the schemas are overview %d, work extras %d, last %d; this test names 67 and 68", schemaOverview, schemaWorkExtras, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaOverview, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO work_scopes(scope_kind, scope_id, prefix) VALUES('project','acme/shop','SHOP')`)
	exec(`INSERT INTO work_items(id, scope_kind, scope_id, number, title, created_at, updated_at) VALUES('wi_1','project','acme/shop',1,'Crash',1,2)`)
	exec(`INSERT INTO work_comments(id, item_id, author, body, created_at) VALUES('wcm_1','wi_1','ada','hello',3)`)
	exec(`INSERT INTO deployments(id, run_id, run_number, project, step_id, environment, service, target, application, mode, version, opened_at)
		VALUES('dep_1','run_1',3,'shop','deploy','prod','shop','cluster-a','shop','write_back','1.0',9)`)
	exec(`INSERT INTO work_code_links(id, item_id, kind, repo, ref, created_at, updated_at) VALUES('wk_1','wi_1','pull','acme/shop','1',4,4)`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaWorkExtras, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if n := strings.Count(log.String(), "applied a database migration"); n != 1 || !strings.Contains(log.String(), "applied a database migration\" schema=68 ") {
		t.Errorf("want exactly migration 68 applied:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaWorkExtras)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 68 || info.Code != 68 || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	count := func(q string) (n int) {
		t.Helper()
		if err := cur.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for q, want := range map[string]int{`SELECT count(*) FROM work_items`: 1, `SELECT count(*) FROM work_comments`: 1, `SELECT count(*) FROM work_code_links`: 1, `SELECT count(*) FROM work_reactions`: 0} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	// A deployment from before has no address, and one made now can have it.
	var addr0 string
	if err := cur.db.QueryRow(`SELECT address FROM deployments WHERE id = 'dep_1'`).Scan(&addr0); err != nil || addr0 != "" {
		t.Fatalf("the address of an old deployment: %q %v", addr0, err)
	}
	if _, err := cur.db.Exec(`UPDATE deployments SET address = ? WHERE id = 'dep_1'`, "https://preview.example.com/x"); err != nil {
		t.Fatal(err)
	}
	// The new statements work on the old rows: the repository condition finds the item, reactions count.
	if n := count(`SELECT count(*) FROM work_items i WHERE ` + strings.NewReplacer("?", "'acme/shop'").Replace(store.WorkInRepositorySQL)); n != 1 {
		t.Errorf("the repository condition found %d items", n)
	}
	db := cur.Database()
	for _, who := range []string{"ada", "bob"} {
		if added, err := store.ReactionSet(ctx, db, "comment", "wcm_1", who, "heart", 5); err != nil || !added {
			t.Fatalf("%v %v", added, err)
		}
	}
	if added, _ := store.ReactionSet(ctx, db, "comment", "wcm_1", "ada", "heart", 6); added {
		t.Fatal("the same reaction twice counted twice")
	}
	rows, err := store.ReactionCounts(ctx, db, "comment", []string{"wcm_1"}, "ada")
	if err != nil || len(rows) != 1 || rows[0].Count != 2 || !rows[0].Mine {
		t.Fatalf("%+v %v", rows, err)
	}
	// Time entries: a person has one running timer, and the item's old rows are the ones the new table points at.
	now := store.TimeRow{ID: "wt_1", Item: "wi_1", Who: "ada", Kind: "timer", Day: "2026-03-01", StartedAt: 5}
	if err := store.TimeInsert(ctx, db, now); err != nil {
		t.Fatal(err)
	}
	now.ID = "wt_2"
	if err := store.TimeInsert(ctx, db, now); err == nil {
		t.Fatal("a second running timer for one person was stored")
	}
	if ok, err := store.TimeFinish(ctx, db, "wt_1", 65000, 1, "x"); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if err := store.TimeInsert(ctx, db, now); err != nil {
		t.Fatalf("a new timer after the first was stopped: %v", err)
	}
	// Decision records: no row is a proposal; the old item can be answered once and then replaced.
	if _, ok, err := store.DecisionGet(ctx, db, "wi_1"); err != nil || ok {
		t.Fatalf("an old item has a decision row: %v %v", ok, err)
	}
	if added, err := store.DecisionAccept(ctx, db, "wi_1", "Yes.", "ada", 10); err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	if again, _ := store.DecisionAccept(ctx, db, "wi_1", "No.", "bob", 11); again {
		t.Fatal("a decision was answered twice")
	}
	if ok, err := store.DecisionSupersede(ctx, db, "wi_1", "wi_2", 12); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if row, _, _ := store.DecisionGet(ctx, db, "wi_1"); row.Status != "superseded" || row.SupersededBy != "wi_2" || row.Answer != "Yes." {
		t.Fatalf("%+v", row)
	}
	cur.Close()

	older := mustOpenAs(t, addr, clk, schemaOverview, &syncBuffer{})
	defer older.Close()
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 68 || oi.Code != 67 || !oi.Compatible() {
		t.Fatalf("the build of schema 67 against schema 68: %+v %v", oi, err)
	}
}
