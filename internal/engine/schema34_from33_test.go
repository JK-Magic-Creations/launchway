package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database at schema 33 (rollbacks, pull requests and reviews real) holds
// what the release before encryption stored: a runner token in the clear, a
// host key and a webhook secret sealed with keys kept beside them, and the
// data of the two features. It upgrades with only migration 34 applied, keeps
// every row, and the first start with a root key then seals what is secret
// and nothing else.
func TestSchema34UpgradesFrom33WithDataAndIsThenSealedCorrectly(t *testing.T) {
	ctx := context.Background()
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaPulls, &syncBuffer{})
	if hasTable(t, old, "encryption_keks") || !hasTable(t, old, "pull_requests") || !hasTable(t, old, "rollbacks") {
		t.Fatal("schema 33 is not what this test expects")
	}
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
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaEncryption)) || strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaPulls)) {
		t.Fatalf("only migration %d should apply:\n%s", schemaEncryption, log.String())
	}
	checkMarkers(t, cur, schemaEncryption)
	count := func(q string) (n int) {
		t.Helper()
		if err := cur.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for q, want := range map[string]int{`SELECT count(*) FROM rollbacks`: 1, `SELECT count(*) FROM rollback_reverts`: 1, `SELECT count(*) FROM pull_requests`: 1,
		`SELECT count(*) FROM pull_reviews`: 1, `SELECT count(*) FROM pull_comments WHERE has_suggestion = 1`: 1, `SELECT count(*) FROM encryption_keks`: 0} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	var runner string
	if err := cur.db.QueryRow(`SELECT value FROM meta WHERE key = 'shared:runner_token'`).Scan(&runner); err != nil || runner != "tok-before" {
		t.Fatalf("the migration changed the runner token: %q %v", runner, err)
	}

	// The first start with a root key seals the secret columns, in place.
	k, err := keyring.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := keyring.FromKey("key_file", "test", k)
	if err != nil {
		t.Fatal(err)
	}
	s, err := sealing.Start(ctx, sealing.Options{DB: cur.Database(), Holder: h, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Instance: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.SharedValue("runner_token", "unused"); err != nil || v != "tok-before" {
		t.Fatalf("runner token after sealing: %q %v", v, err)
	}
	var host, hook string
	_ = cur.db.QueryRow(`SELECT sealed FROM ssh_host_keys WHERE algo = 'ed25519'`).Scan(&host)
	_ = cur.db.QueryRow(`SELECT secret_sealed FROM repo_webhooks WHERE id = 'hok_1'`).Scan(&hook)
	if got, err := s.HostKeys().Open(ctx, "ed25519", host); err != nil || string(got) != "host-key-der-before" {
		t.Fatalf("host key: %q %v", got, err)
	}
	if got, err := s.Webhooks().Open(ctx, "hok_1", hook); err != nil || string(got) != "webhook-secret-before" {
		t.Fatalf("webhook secret: %q %v", got, err)
	}
	for _, v := range []string{host, hook} {
		if !keyring.IsSealed(v) {
			t.Fatalf("not sealed: %q", v[:min(len(v), 12)])
		}
	}
	// What is not secret was left alone.
	var title, body string
	if err := cur.db.QueryRow(`SELECT title FROM pull_requests WHERE id = 'pr_1'`).Scan(&title); err != nil || title != "Fix rounding" {
		t.Fatal(title, err)
	}
	if err := cur.db.QueryRow(`SELECT body FROM pull_comments WHERE id = 'cm_1'`).Scan(&body); err != nil || body != "use half-even" {
		t.Fatal(body, err)
	}
	if r, err := s.Report(ctx); err != nil || r.Remaining != 0 || !r.DumpAloneIsUseless {
		t.Fatalf("%+v %v", r, err)
	}
}

// The plain step: 33 to 34 on an empty installation applies only migration 34.
func TestSchema34UpgradesFrom33(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	mustOpenAs(t, addr, clk, schemaPulls, &syncBuffer{}).Close()
	var log syncBuffer
	cur := mustOpenAs(t, addr, clk, 0, &log)
	info, err := cur.Schema(context.Background())
	if err != nil || info.Database != schemaEncryption || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	if !strings.Contains(log.String(), `applied a database migration" schema=34 `) || strings.Count(log.String(), "applied a database migration") != 1 {
		t.Fatalf("only 34 should apply:\n%s", log.String())
	}
}
