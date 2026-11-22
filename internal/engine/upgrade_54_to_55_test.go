package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 54 (measured step use) is installed first, with a person's token, an unlimited one and a
// limited one; the next build must be a pure upgrade from it: one start applies exactly 55 (narrow credentials),
// every token is kept and behaves as before (no repositories, no source ranges), and the build of schema 54 still
// starts against the upgraded database and reads the old tokens. On SQLite and, with LAUNCHWAY_TEST_POSTGRES set,
// on PostgreSQL.
func TestUpgradeFromSchema54KeepsTokensAndAddsNarrowCredentials(t *testing.T) {
	ctx := context.Background()
	if schemaUsage != 54 || schemaNarrow != 55 || schemaLast < 55 {
		t.Fatalf("the schemas are usage %d, narrow %d, last %d; this test names 54 and 55", schemaUsage, schemaNarrow, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaUsage, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO api_tokens(id, hash, username, name, created_at, expires_at) VALUES('tok_plain','h-plain','ada','plain',1,9999999999999)`)
	exec(`INSERT INTO api_tokens(id, hash, username, name, created_at, expires_at, role, projects) VALUES('tok_ltd','v2:h-ltd','ada','limited',1,9999999999999,'viewer','pay-*')`)
	exec(`INSERT INTO audit(at, username, source, action, project, target, outcome, detail) VALUES(1,'ada','203.0.113.5','git.push','','acme/shop','ok','token plain')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaNarrow, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), `applied a database migration" schema=55 `) || strings.Count(log.String(), "applied a database migration") != 1 {
		t.Errorf("exactly migration 55 should have been applied:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaNarrow)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 55 || info.Code != 55 || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	// The old tokens are found by their stored hashes, with no new limits.
	for hash, want := range map[string]string{"h-plain": "tok_plain", "v2:h-ltd": "tok_ltd"} {
		var id, repos, access, ranges string
		if err := cur.db.QueryRow(`SELECT id, repos, repo_access, source_ranges FROM api_tokens WHERE hash = ?`, hash).Scan(&id, &repos, &access, &ranges); err != nil {
			t.Fatal(err)
		}
		if id != want || repos != "" || access != "" || ranges != "" {
			t.Errorf("%s: %q %q %q %q", hash, id, repos, access, ranges)
		}
	}
	tok, _, err := cur.LookupToken("h-plain")
	if err != nil || tok.RepoAccess != "" || len(tok.Repositories) != 0 || len(tok.SourceRanges) != 0 {
		t.Fatalf("the old token after the upgrade: %+v %v", tok, err)
	}
	var tokenID string
	if err := cur.db.QueryRow(`SELECT token_id FROM audit WHERE action = 'git.push'`).Scan(&tokenID); err != nil || tokenID != "" {
		t.Errorf("an old audit line has token_id %q (%v)", tokenID, err)
	}
	// New things work: a limited token, an audit line naming it, a service account.
	if err := cur.CreateLimitedToken(APIToken{ID: "tok_new", Name: "mirror", User: "service:gitops", CreatedAt: 1, ExpiresAt: 9999999999999,
		Repositories: []TokenRepo{{Pattern: "acme/shop", ID: "rep_1"}}, RepoAccess: "read", SourceRanges: []string{"203.0.113.0/24"}}, "h-new"); err != nil {
		t.Fatal(err)
	}
	got, _, err := cur.LookupToken("h-new")
	if err != nil || len(got.Repositories) != 1 || got.Repositories[0].ID != "rep_1" || got.RepoAccess != "read" || got.SourceRanges[0] != "203.0.113.0/24" {
		t.Fatalf("a repository-limited token: %+v %v", got, err)
	}
	if err := cur.Audit(AuditEntry{At: 2, User: "service:gitops", Action: "git.push", Outcome: "ok", TokenID: "tok_new", TokenName: "mirror"}); err != nil {
		t.Fatal(err)
	}
	if err := cur.CreateServiceAccount(ServiceAccount{Name: "gitops", Role: "viewer", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	older := mustOpenAs(t, addr, clk, schemaUsage, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 55 || oi.Code != 54 || !oi.Compatible() {
		t.Fatalf("the build of schema 54 against schema 55: %+v %v", oi, err)
	}
	if tok, _, err := older.LookupToken("h-plain"); err != nil || tok.User != "ada" {
		t.Fatalf("the build of schema 54 cannot read its token: %+v %v", tok, err)
	}
	// What a build of schema 54 looks up is the plain hash and "v2:"+hash; a repository-limited token is stored under
	// neither, so that build refuses it instead of accepting it without the limit it does not know.
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM api_tokens WHERE hash IN ('h-new', 'v2:h-new')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a repository-limited token is stored where an older build looks (%d, %v)", n, err)
	}
	if err := cur.db.QueryRow(`SELECT count(*) FROM api_tokens WHERE hash = 'v3:h-new'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("a repository-limited token is not stored under v3: (%d, %v)", n, err)
	}
}
