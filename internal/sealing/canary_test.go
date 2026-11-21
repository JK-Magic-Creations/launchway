package sealing

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/sshkeys"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// dumpAll returns everything the database holds as bytes: on SQLite the
// file and its write-ahead log; on PostgreSQL every text, character and
// bytea column of every table of the test's schema.
func dumpAll(t *testing.T, e *engine.Engine, addr string) []byte {
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
	db := e.Database()
	type col struct{ table, column, typ string }
	var cols []col
	if err := eachRow(ctx, db, `SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type IN ('text', 'character varying', 'character', 'bytea')`, nil, func(rs *sql.Rows) error {
		var c col
		if err := rs.Scan(&c.table, &c.column, &c.typ); err != nil {
			return err
		}
		cols = append(cols, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		sel := `SELECT "` + c.column + `"::text FROM "` + c.table + `"`
		if c.typ == "bytea" {
			sel = `SELECT encode("` + c.column + `", 'escape') FROM "` + c.table + `"`
		}
		if err := eachRow(ctx, db, sel, nil, func(r *sql.Rows) error {
			var v sqlNullString
			if err := r.Scan(&v); err != nil {
				return err
			}
			out.WriteString(v.S)
			out.WriteByte('\n')
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

type sqlNullString struct{ S string }

func (n *sqlNullString) Scan(v any) error {
	switch x := v.(type) {
	case string:
		n.S = x
	case []byte:
		n.S = string(x)
	}
	return nil
}

// encodings returns the ways a stored value may carry a secret.
func encodings(secret []byte) map[string][]byte {
	return map[string][]byte{
		"raw":       secret,
		"base64":    []byte(base64.StdEncoding.EncodeToString(secret)),
		"base64url": []byte(base64.RawURLEncoding.EncodeToString(secret)),
		"hex":       []byte(hex.EncodeToString(secret)),
	}
}

func mustNotAppear(t *testing.T, dump []byte, what string, secret []byte) {
	t.Helper()
	for name, enc := range encodings(secret) {
		if len(enc) >= 8 && bytes.Contains(dump, enc) {
			t.Errorf("%s appears in the database dump (%s encoded)", what, name)
		}
	}
}

func mustAppear(t *testing.T, dump []byte, what string, secret []byte) {
	t.Helper()
	if !bytes.Contains(dump, secret) {
		t.Fatalf("the test cannot see %s in the dump before sealing, so it proves nothing", what)
	}
}

const (
	cRunner  = "CANARY-runner-token-7f3a91c2d8e4b605"
	cLogin   = "CANARY-login-state-key-3c19e4a7b2d6f850"
	cReport  = "CANARY-hosted-report-key-9d2b6e1f4a8c7350"
	cRunTok  = "CANARY-git-run-token-key-5e8a2c9b1d7f4036"
	cWebhook = "CANARY-webhook-secret-1b4d7a9e2c6f8053"
	// cHookPath is the token of a chat service's incoming webhook: it sits in the address's path.
	cHookPath = "CANARY-webhook-path-token-6a0e3d8c5b2f9147"
)

// The canary test: every kind of secret the server stores, made with a
// recognizable value; a full dump of the database; the marker must not be in
// it, in any encoding the stores use. First on a fresh installation that has
// a root key from the start, then on an old one whose plaintext is sealed in
// place: there the same dump showed the markers before, which is what makes
// the test mean something.
func TestCanaryNoSecretAppearsInADatabaseDump(t *testing.T) {
	addr := testdb.New(t)
	e := openEngine(t, addr)
	s := start(t, e, newHolder(t))

	for key, val := range map[string]string{"runner_token": cRunner, "login_state_key": cLogin, "hosted_report_key": cReport, "git_run_token_key": cRunTok} {
		if got, err := s.SharedValue(key, val); err != nil || got != val {
			t.Fatalf("%s: %q %v", key, got, err)
		}
	}
	hooks := webhooks.New(webhooks.Options{DB: e.Database(), Sealer: s.Webhooks(), Log: quiet})
	hook, _, err := hooks.Create(ctx, webhooks.NewHook{RepoID: "repo1", Name: "ci", URL: "https://example.test/services/" + cHookPath, Events: []string{"push"}, Secret: cWebhook, CreatedBy: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	signers, err := sshkeys.New(e.Database()).HostSigners(ctx, s.HostKeys())
	if err != nil || len(signers) != 3 {
		t.Fatalf("host keys: %d %v", len(signers), err)
	}
	// The host keys' private bytes, read back through the sealer: they must not be in the dump either.
	var privates [][]byte
	for _, algo := range sshkeys.HostAlgorithms {
		var stored string
		if err := e.Database().QueryRow(ctx, `SELECT sealed FROM ssh_host_keys WHERE algo = ?`, algo).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		der, err := s.HostKeys().Open(ctx, algo, stored)
		if err != nil || len(der) < 32 {
			t.Fatalf("%s: %v", algo, err)
		}
		privates = append(privates, der, privateScalar(t, der))
	}
	_ = hook

	dump := dumpAll(t, e, addr)
	for what, v := range map[string]string{"the shared runner token": cRunner, "the login state key": cLogin, "the report link key": cReport,
		"the Git run token key": cRunTok, "a webhook secret": cWebhook, "the token in a webhook address": cHookPath} {
		mustNotAppear(t, dump, what, []byte(v))
	}
	for i, p := range privates {
		mustNotAppear(t, dump, "an SSH host private key ("+string(rune('0'+i))+")", p)
	}
	if bytes.Contains(dump, []byte("BEGIN OPENSSH PRIVATE KEY")) || bytes.Contains(dump, []byte("BEGIN PRIVATE KEY")) {
		t.Error("a PEM private key is in the dump")
	}
	// What the dump does hold is ciphertext, in the format.
	if !bytes.Contains(dump, []byte(keyring.Prefix)) {
		t.Error("no sealed value in the dump at all")
	}
}

// privateScalar is the secret number inside a PKCS#8 private key (not the
// public half, which is stored in the clear on purpose).
func privateScalar(t *testing.T, der []byte) []byte {
	t.Helper()
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		t.Fatal(err)
	}
	switch k := k.(type) {
	case ed25519.PrivateKey:
		return k.Seed()
	case *ecdsa.PrivateKey:
		d, err := k.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		return d
	case *rsa.PrivateKey:
		return k.D.Bytes()[:32]
	}
	t.Fatalf("unexpected key type %T", k)
	return nil
}

func TestCanaryOldPlaintextIsGoneAfterSealingInPlace(t *testing.T) {
	addr := testdb.New(t)
	e := openEngine(t, addr)
	put := func(k, v string) {
		if _, err := e.Database().Exec(ctx, `INSERT INTO meta(key, value) VALUES(?, ?)`, "shared:"+k, v); err != nil {
			t.Fatal(err)
		}
	}
	put("runner_token", cRunner)
	put("login_state_key", cLogin)
	put("hosted_report_key", cReport)
	put("git_run_token_key", cRunTok)

	// Before: an installation without a root key keeps them in the clear, and the test sees it.
	plain, err := Start(ctx, Options{DB: e.Database(), Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := plain.Report(ctx); r.DumpAloneIsUseless {
		t.Fatal("an installation without a root key claims a dump is useless")
	}
	before := dumpAll(t, e, addr)
	for _, v := range []string{cRunner, cLogin, cReport, cRunTok} {
		mustAppear(t, before, v, []byte(v))
	}

	s := start(t, e, newHolder(t))
	after := dumpAll(t, e, addr)
	for _, v := range []string{cRunner, cLogin, cReport, cRunTok} {
		mustNotAppear(t, after, "a legacy secret after sealing in place", []byte(v))
	}
	if r, _ := s.Report(ctx); !r.DumpAloneIsUseless {
		t.Fatalf("%+v", r)
	}
}
