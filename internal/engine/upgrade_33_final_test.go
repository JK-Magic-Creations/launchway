package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The upgrade that is deployed today: a database at schema 33 (the release in use) with representative
// data (a pull request with a review and a comment, a webhook with a secret, an SSH host key, build
// cache entries, a rollback, a runner token in the clear) is opened by THIS build in one start. Exactly
// migrations 34 to 39 apply (encryption, cache, accounts, queues, delivery, the issue archive: contiguous, no stand-ins),
// every row survives, the new tables exist and are empty, a build of schema 33 still starts against the
// result and reads the old data (the product promises that for a rolling update), and sealing of what
// was in the clear waits while an older server is seen and then completes without losing a value. On
// SQLite and, with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromSchema33ToTheFinalSchemaKeepsEverything(t *testing.T) {
	ctx := context.Background()
	if len(migrations) != schemaToggles || schemaToggles != 41 {
		t.Fatalf("the final schema is %d with %d migrations; this test and the upgrade notes name 41", schemaToggles, len(migrations))
	}
	for i := schemaEncryption; i <= schemaToggles; i++ {
		if strings.TrimSpace(migrations[i-1].sql) == "" {
			t.Fatalf("migration %d is an empty stand-in", i)
		}
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaPulls, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	hostSeal, whSeal := auth.NewKey(), auth.NewKey()
	exec(`INSERT INTO meta(key, value) VALUES('shared:ssh_host_key_seal', ?)`, hostSeal)
	exec(`INSERT INTO meta(key, value) VALUES('shared:webhook_secret_seal', ?)`, whSeal)
	exec(`INSERT INTO meta(key, value) VALUES('shared:runner_token', 'tok-before')`)
	hs, _ := auth.NewSealer(hostSeal, "ssh-host-key")
	hostBlob, err := hs.Seal([]byte("host-key-der-before"))
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ssh_host_keys(algo, public, sealed, fingerprint) VALUES('ed25519','pub',?,'fp')`, hostBlob)
	ws, _ := auth.NewSealer(whSeal, "webhook-secret")
	whBlob, _ := ws.Seal("webhook-secret-before")
	exec(`INSERT INTO repo_webhooks(id, name, url, secret_sealed) VALUES('hok_1','h','https://example.test/h',?)`, whBlob)
	exec(`INSERT INTO rollbacks(run_id, project, run_number, environment, service, to_deployment, to_version, reason, actor, created_at)
		VALUES('run_rb','shop',3,'prod','api','dep_1','v1','bad release','ada',5)`)
	exec(`INSERT INTO rollback_reverts(run_id, deployment, position) VALUES('run_rb','dep_2',0)`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('pr_1','rep1',1,'Fix rounding','ada','fix','main')`)
	exec(`INSERT INTO pull_reviews(id, pull_id, author, verdict) VALUES('rv_1','pr_1','bob','approve')`)
	exec(`INSERT INTO pull_comments(id, pull_id, review_id, author, body, suggestion, has_suggestion) VALUES('cm_1','pr_1','rv_1','bob','use half-even','return 1',1)`)
	for i := 1; i <= 3; i++ {
		exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status, created_at, last_used_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("cch_%d", i), "shop", "branch:main", "gomod", fmt.Sprintf("k%d", i), "linux/amd64", fmt.Sprintf("caches/%d", i), "kept", 10, 20)
	}
	old.Close()

	// One start of this build: exactly 34 to 39.
	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for v := schemaEncryption; v <= schemaToggles; v++ {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if n := strings.Count(log.String(), "applied a database migration"); n != schemaToggles-schemaPulls {
		t.Errorf("%d migrations applied, want %d:\n%s", n, schemaToggles-schemaPulls, log.String())
	}
	checkMarkers(t, cur, schemaToggles)
	info, err := cur.Schema(ctx)
	if err != nil || info.Database != schemaToggles || info.Code != schemaToggles || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	count := func(q string) (n int) {
		t.Helper()
		if err := cur.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM rollbacks`: 1, `SELECT count(*) FROM rollback_reverts`: 1, `SELECT count(*) FROM pull_requests`: 1,
		`SELECT count(*) FROM pull_reviews`: 1, `SELECT count(*) FROM pull_comments WHERE has_suggestion = 1`: 1,
		`SELECT count(*) FROM repo_webhooks`: 1, `SELECT count(*) FROM ssh_host_keys`: 1, `SELECT count(*) FROM caches WHERE status = 'kept'`: 3,
		// The tables the migrations brought are there and empty.
		`SELECT count(*) FROM encryption_keks`: 0, `SELECT count(*) FROM local_accounts`: 0, `SELECT count(*) FROM run_queue`: 0,
		`SELECT count(*) FROM step_waits`: 0, `SELECT count(*) FROM imported_issues`: 0, `SELECT count(*) FROM imported_comments`: 0, `SELECT count(*) FROM branch_writers`: 0, `SELECT count(*) FROM notifications`: 0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	var runner string
	if err := cur.db.QueryRow(`SELECT value FROM meta WHERE key = 'shared:runner_token'`).Scan(&runner); err != nil || runner != "tok-before" {
		t.Fatalf("a migration changed the runner token: %q %v", runner, err)
	}

	// A build of schema 33 (a server of the release in use during a rolling update, or after a rollback)
	// still starts against the upgraded database and reads the old data.
	older := mustOpenAs(t, addr, clk, schemaPulls, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != schemaToggles || oi.Code != schemaPulls || !oi.Compatible() {
		t.Fatalf("the build of schema 33 against the final schema: %+v %v", oi, err)
	}
	var title, whSecret string
	if err := older.db.QueryRow(`SELECT title FROM pull_requests WHERE id = 'pr_1'`).Scan(&title); err != nil || title != "Fix rounding" {
		t.Fatalf("the older build cannot read the pull request: %q %v", title, err)
	}
	_ = older.db.QueryRow(`SELECT secret_sealed FROM repo_webhooks WHERE id = 'hok_1'`).Scan(&whSecret)
	if got, err := mustSealerOpen(ws, whSecret); err != nil || got != "webhook-secret-before" {
		t.Fatalf("the older build cannot open the webhook secret with its own key: %q %v", got, err)
	}
	older.Close()

	// This build with a root key: sealing waits while an older server is seen, then completes.
	k, err := keyring.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := keyring.FromKey("key_file", "test", k)
	if err != nil {
		t.Fatal(err)
	}
	var seen atomic.Int64
	seen.Store(1)
	s, err := sealing.Start(ctx, sealing.Options{DB: cur.Database(), Holder: h, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Instance: "t",
		OlderServers: func(context.Context) (int, error) { return int(seen.Load()), nil }, QuietFor: 100 * time.Millisecond, Poll: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	waiting := false
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end) && !waiting; time.Sleep(20 * time.Millisecond) {
		rep, err := s.Report(ctx)
		if err != nil || rep.Remaining == 0 {
			t.Fatalf("something sealed, or the report failed: %+v %v", rep.Migration, err)
		}
		waiting = rep.Migration.Waiting
	}
	if !waiting {
		t.Fatal("sealing should wait while an older server is seen")
	}
	var stillClear string
	_ = cur.db.QueryRow(`SELECT value FROM meta WHERE key = 'shared:runner_token'`).Scan(&stillClear)
	if stillClear != "tok-before" {
		t.Fatalf("sealed while an older server was seen: %q", stillClear)
	}
	seen.Store(0)
	deadline := time.Now().Add(2 * time.Minute) // a small, shared build machine seals slowly
	for time.Now().Before(deadline) {
		if rep, err := s.Report(ctx); err == nil && rep.Remaining == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	rep, err := s.Report(ctx)
	if err != nil || rep.Remaining != 0 || !rep.DumpAloneIsUseless {
		t.Fatalf("sealing did not complete: %+v %v", rep, err)
	}
	if v, err := s.SharedValue("runner_token", "unused"); err != nil || v != "tok-before" {
		t.Fatalf("runner token after sealing: %q %v", v, err)
	}
	var host, hook string
	_ = cur.db.QueryRow(`SELECT sealed FROM ssh_host_keys WHERE algo = 'ed25519'`).Scan(&host)
	_ = cur.db.QueryRow(`SELECT secret_sealed FROM repo_webhooks WHERE id = 'hok_1'`).Scan(&hook)
	if got, err := s.HostKeys().Open(ctx, "ed25519", host); err != nil || string(got) != "host-key-der-before" {
		t.Fatalf("host key after sealing: %q %v", got, err)
	}
	if got, err := s.Webhooks().Open(ctx, "hok_1", hook); err != nil || string(got) != "webhook-secret-before" {
		t.Fatalf("webhook secret after sealing: %q %v", got, err)
	}
	if count(`SELECT count(*) FROM caches WHERE status = 'kept'`) != 3 || count(`SELECT count(*) FROM pull_reviews`) != 1 || count(`SELECT count(*) FROM rollbacks`) != 1 {
		t.Fatal("rows were lost while sealing")
	}
}

func mustSealerOpen(s *auth.Sealer, stored string) (string, error) {
	var out string
	err := s.Open(stored, &out)
	return out, err
}
