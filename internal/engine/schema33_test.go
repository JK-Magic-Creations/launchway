package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 31 (a protected branch recorded) upgrades to 33 in one
// start: the rollback migration (32) and the pull request migration are applied, the
// branch rule keeps its row and gets the merge rules' defaults, the pull
// request tables are there and empty, and an older build then runs against
// the newer database and leaves what is in it alone.
func TestSchema33UpgradesFrom31KeepingBranchRules(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaSSH, &syncBuffer{})
	if hasTable(t, old, "pull_requests") || hasColumn(t, old, "branch_rules", "required_approvals") {
		t.Fatal("schema 31 already has pull requests")
	}
	if _, err := old.db.Exec(`INSERT INTO branch_rules(id, repo_id, pattern, push_role, created_at, created_by) VALUES('r1','repo1','main','admin',1,'ada')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaPulls, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	for _, v := range []int{schemaRollback, schemaPulls, schemaPulls} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaSSH)) {
		t.Errorf("migration 31, already applied, was applied again")
	}
	checkMarkers(t, cur, schemaAccounts)
	var approvals, noSelf int
	var role, checks string
	if err := cur.db.QueryRow(`SELECT required_approvals, approval_role, required_checks, no_self_approval FROM branch_rules WHERE id = 'r1'`).Scan(&approvals, &role, &checks, &noSelf); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 || role != "operator" || checks != "[]" || noSelf != 1 {
		t.Fatalf("defaults: %d %q %q %d", approvals, role, checks, noSelf)
	}
	if _, err := cur.db.Exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('p1','repo1',1,'t','ada','f','main')`); err != nil {
		t.Fatal(err)
	}
	// The number is unique per repository.
	if _, err := cur.db.Exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('p2','repo1',1,'t','ada','g','main')`); err == nil {
		t.Fatal("two pull requests of one repository got the same number")
	}
	cur.Close()

	older, err := openAs(t, addr, clk, schemaSSH, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != schemaAccounts || info.Code != schemaSSH || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	var n int
	if err := older.db.QueryRow(`SELECT count(*) FROM pull_requests`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the pull request did not survive the older build: %d %v", n, err)
	}
}

// A database of schema 32 (rollbacks recorded) upgrades to 33 in one start:
// only the pull request migration is applied, and the rollback rows stay.
func TestSchema33UpgradesFrom32KeepingRollbacks(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaRollback, &syncBuffer{})
	if hasTable(t, old, "pull_requests") || !hasTable(t, old, "rollbacks") {
		t.Fatal("schema 32 must have rollbacks and no pull requests")
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaPulls, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaPulls)) {
		t.Errorf("migration 33 was not applied:\n%s", log.String())
	}
	if strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaRollback)) {
		t.Errorf("migration 32, already applied, was applied again")
	}
	checkMarkers(t, cur, schemaAccounts)
	if !hasTable(t, cur, "rollbacks") || !hasTable(t, cur, "rollback_reverts") || !hasTable(t, cur, "pull_requests") {
		t.Fatal("tables after 32 to 33")
	}
}
