package engine

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/notify"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const (
	cNoteURLToken = "CANARY-notification-address-token-5e2c9a71"
	cNoteKey      = "CANARY-notification-signing-key-83b0d6f4"
	cNoteEcho     = "CANARY-token-echoed-by-the-receiver-1d94e7ab"
)

// A notification channel's address and signing key are references to a secret store; their values are
// read to send a message and must reach no table: not the configuration, not the delivery log (even when
// a delivery fails and the receiver echoes what it was given), on SQLite or PostgreSQL.
func TestNotificationAddressAndKeyAreInNoTable(t *testing.T) {
	var sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "bad gateway for "+r.URL.Path+" Authorization: Bearer "+cNoteEcho+" signature "+r.Header.Get("X-Launchway-Signature-256"))
	}))
	defer srv.Close()
	addr := testdb.New(t)
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	n := &Notifier{BaseURL: "https://ci.example.com", Client: http.DefaultClient,
		Secrets: fakeSecrets{"hook-url": srv.URL + "/services/" + cNoteURLToken, "signing": cNoteKey}}
	cfg := notify.Config{
		Channels: []notify.Channel{{Name: "ops", Kind: notify.Webhook, URL: pipeline.SecretRef{Store: "vault", Key: "hook-url"}, Secret: &pipeline.SecretRef{Store: "vault", Key: "signing"}}},
		Rules:    []notify.Rule{{On: []string{notify.ApprovalRequested}, To: []string{"ops"}}},
	}
	if err := e.Reconfigure(LiveOptions{Notifications: cfg}); err != nil {
		t.Fatal(err)
	}
	toApproval(t, e)
	e.notifyOnce(context.Background(), n)
	if sawPath == "" {
		t.Fatal("the delivery did not happen, so the test proves nothing")
	}
	log, err := e.Notifications(10, nil)
	if err != nil || len(log) != 1 || log[0].LastError == "" {
		t.Fatalf("the failed delivery should be logged with a reason: %v %+v", err, log)
	}
	dump := dumpEverything(t, e, addr)
	for what, secret := range map[string]string{"the channel's address token": cNoteURLToken, "the channel's signing key": cNoteKey, "a token the receiver echoed": cNoteEcho} {
		if bytes.Contains(dump, []byte(secret)) {
			t.Errorf("%s is in a table", what)
		}
	}
	if !bytes.Contains(dump, []byte("approval.requested")) {
		t.Error("the dump holds no notification: the test cannot see what it should")
	}
}

// dumpEverything is every stored text of the database, as the sealing package's canary dumps it.
func dumpEverything(t *testing.T, e *Engine, addr string) []byte {
	t.Helper()
	var out bytes.Buffer
	if !testdb.Postgres() {
		for _, suffix := range []string{"", "-wal"} {
			if b, err := os.ReadFile(addr + suffix); err == nil {
				out.Write(b)
			}
		}
		return out.Bytes()
	}
	type col struct{ table, column, typ string }
	var cols []col
	rows, err := e.db.Query(`SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type IN ('text', 'character varying', 'character', 'bytea')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.column, &c.typ); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	for _, c := range cols {
		sel := `SELECT "` + c.column + `"::text FROM "` + c.table + `"`
		if c.typ == "bytea" {
			sel = `SELECT encode("` + c.column + `", 'escape') FROM "` + c.table + `"`
		}
		rs, err := e.db.Query(sel)
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			var v sql.NullString
			if err := rs.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out.WriteString(v.String)
			out.WriteByte('\n')
		}
		rs.Close()
	}
	return out.Bytes()
}
