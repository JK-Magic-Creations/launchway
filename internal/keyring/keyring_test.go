package keyring

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

type testDB struct{ *sql.DB }

func (d testDB) Exec(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return d.ExecContext(ctx, q, a...)
}
func (d testDB) Query(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return d.QueryContext(ctx, q, a...)
}
func (d testDB) QueryRow(ctx context.Context, q string, a ...any) *sql.Row {
	return d.QueryRowContext(ctx, q, a...)
}

func newDB(t *testing.T) testDB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "k.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(strings.ReplaceAll(SchemaSQL, "{{BIG}}", "INTEGER")); err != nil {
		t.Fatal(err)
	}
	return testDB{db}
}

func holder(t *testing.T) Holder {
	t.Helper()
	k, err := NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := FromKey("key_file", "test", k)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

var ctx = context.Background()

func bind(row string) Binding {
	return Binding{Purpose: PurposeSecrets, Table: "t", Column: "c", Row: row}
}

func TestSealOpenRoundTripAndFormat(t *testing.T) {
	db := newDB(t)
	k, err := Open(ctx, db, holder(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := k.Seal(ctx, bind("r1"), []byte("hunter2-canary"))
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(s) || strings.Contains(s, "hunter2") {
		t.Fatalf("sealed value: %q", s)
	}
	if v, err := SealedVersion(s); err != nil || v != 1 {
		t.Fatalf("version %d %v", v, err)
	}
	got, err := k.Open(ctx, bind("r1"), s)
	if err != nil || string(got) != "hunter2-canary" {
		t.Fatalf("%q %v", got, err)
	}
	// Two sealings of one value differ (random nonce).
	s2, _ := k.Seal(ctx, bind("r1"), []byte("hunter2-canary"))
	if s == s2 {
		t.Fatal("the nonce was reused")
	}
}

// A sealed value opens only where it was sealed: another row, column, table,
// organization or purpose fails; so does a changed byte and another installation.
func TestAuthenticatedDataBindsTheValueToItsPlace(t *testing.T) {
	db := newDB(t)
	h := holder(t)
	k, _ := Open(ctx, db, h, Options{})
	b := Binding{Purpose: PurposeSecrets, Org: "acme", Table: "repo_webhooks", Column: "secret_sealed", Row: "h1"}
	s, _ := k.Seal(ctx, b, []byte("s3cret"))
	for name, moved := range map[string]Binding{
		"row":     {Purpose: b.Purpose, Org: b.Org, Table: b.Table, Column: b.Column, Row: "h2"},
		"column":  {Purpose: b.Purpose, Org: b.Org, Table: b.Table, Column: "other", Row: b.Row},
		"table":   {Purpose: b.Purpose, Org: b.Org, Table: "other", Column: b.Column, Row: b.Row},
		"org":     {Purpose: b.Purpose, Org: "evil", Table: b.Table, Column: b.Column, Row: b.Row},
		"purpose": {Purpose: PurposeTokens, Org: b.Org, Table: b.Table, Column: b.Column, Row: b.Row},
	} {
		if _, err := k.Open(ctx, moved, s); err == nil {
			t.Errorf("a value moved to another %s opened", name)
		}
	}
	// A bit flip anywhere fails.
	flipped := []byte(s)
	flipped[len(flipped)-3] ^= 1
	if _, err := k.Open(ctx, b, string(flipped)); err == nil {
		t.Error("an altered value opened")
	}
	// The same database under another installation id does not open it.
	if _, err := db.Exec(ctx, `UPDATE encryption_state SET value = 'another' WHERE key = 'installation_id'`); err != nil {
		t.Fatal(err)
	}
	// (the keys' own AAD binds the installation too, so even opening fails)
	if _, err := Open(ctx, db, h, Options{}); err == nil {
		t.Error("a database with another installation id opened")
	}
}

func TestWrongKeyRefusesAndNeverRekeys(t *testing.T) {
	db := newDB(t)
	if _, err := Open(ctx, db, holder(t), Options{}); err != nil {
		t.Fatal(err)
	}
	var kinds []Event
	_, err := Open(ctx, db, holder(t), Options{Audit: func(_ context.Context, e Event) { kinds = append(kinds, e) }})
	var wk *ErrWrongKey
	if !errors.As(err, &wk) {
		t.Fatalf("a different key: %v", err)
	}
	if !strings.Contains(err.Error(), "never re-keys") {
		t.Errorf("the message is not plain: %v", err)
	}
	if len(kinds) != 1 || kinds[0].Action != "encryption.unseal" || kinds[0].Outcome != "failed" {
		t.Errorf("failed unseal not audited: %+v", kinds)
	}
	var n int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM encryption_keks`).Scan(&n)
	if n != 1 {
		t.Fatalf("a wrong key created %d key-encryption keys", n)
	}
	// Another kind of holder gets its own plain message.
	other, _ := FromKey("env", "x", mustKey(t))
	if _, err := Open(ctx, db, other, Options{}); err == nil || !strings.Contains(err.Error(), "key_file") {
		t.Errorf("another kind of holder: %v", err)
	}
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRotateKEKKeepsOldValuesReadable(t *testing.T) {
	db := newDB(t)
	h := holder(t)
	k, _ := Open(ctx, db, h, Options{})
	old, _ := k.Seal(ctx, bind("r1"), []byte("before"))
	res, err := k.RotateKEK(ctx)
	if err != nil || res.KEKVersion != 2 || res.Rewrapped != 1 || res.Retired != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if got, err := k.Open(ctx, bind("r1"), old); err != nil || string(got) != "before" {
		t.Fatalf("after rotation: %q %v", got, err)
	}
	// A server that starts afterwards, and one that started before, both read it.
	k2, err := Open(ctx, db, h, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k2.Open(ctx, bind("r1"), old); err != nil || string(got) != "before" {
		t.Fatalf("restarted: %q %v", got, err)
	}
	// Data key rotation: new values use version 2, old stay readable.
	if n, err := k.RotateDataKeys(ctx); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	fresh, _ := k.Seal(ctx, bind("r2"), []byte("after"))
	if v, _ := SealedVersion(fresh); v != 2 {
		t.Fatalf("the new value used data key %d", v)
	}
	for s, want := range map[string]string{old: "before"} {
		if got, err := k.Open(ctx, bind("r1"), s); err != nil || string(got) != want {
			t.Fatalf("old version after data key rotation: %q %v", got, err)
		}
	}
}

// Replacing the root key: old and new are given together, Rewrap moves the
// wrapping, and afterwards the new key alone works and the old one does not.
func TestRewrapReplacesTheRootKey(t *testing.T) {
	db := newDB(t)
	oldH, newH := holder(t), holder(t)
	k, _ := Open(ctx, db, oldH, Options{})
	s, _ := k.Seal(ctx, bind("r1"), []byte("kept"))
	if _, err := k.RotateKEK(ctx); err != nil { // two versions, one retired, one live
		t.Fatal(err)
	}
	if _, err := Open(ctx, db, newH, Options{}); err == nil {
		t.Fatal("the new key opened before the re-wrap")
	}
	k2, err := Open(ctx, db, newH, Options{Previous: []Holder{oldH}})
	if err != nil {
		t.Fatal(err)
	}
	if !k2.NeedsRewrap() {
		t.Fatal("a previous holder was used but no re-wrap is needed")
	}
	if got, err := k2.Open(ctx, bind("r1"), s); err != nil || string(got) != "kept" {
		t.Fatalf("with both keys: %q %v", got, err)
	}
	n, err := k2.Rewrap(ctx)
	if err != nil || n < 1 {
		t.Fatalf("%d %v", n, err)
	}
	k3, err := Open(ctx, db, newH, Options{})
	if err != nil {
		t.Fatalf("the new key alone: %v", err)
	}
	if k3.NeedsRewrap() {
		t.Error("still needs a re-wrap")
	}
	if got, err := k3.Open(ctx, bind("r1"), s); err != nil || string(got) != "kept" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Open(ctx, db, oldH, Options{}); err == nil {
		t.Fatal("the old key still opens the database after the re-wrap")
	}
	// Idempotent: a second run has nothing to do.
	if n, err := k3.Rewrap(ctx); err != nil || n != 0 {
		t.Fatalf("second run: %d %v", n, err)
	}
}

// Servers starting together create one installation id and one key set.
func TestServersStartingTogetherShareOneKeySet(t *testing.T) {
	db := newDB(t)
	h := holder(t)
	var wg sync.WaitGroup
	krs := make([]*Keyring, 6)
	errs := make([]error, 6)
	for i := range krs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			krs[i], errs[i] = Open(ctx, db, h, Options{})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("server %d: %v", i, err)
		}
	}
	var keks int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM encryption_keks`).Scan(&keks)
	if keks != 1 {
		t.Fatalf("%d key-encryption keys", keks)
	}
	s, _ := krs[0].Seal(ctx, bind("r"), []byte("shared"))
	for i, k := range krs {
		if got, err := k.Open(ctx, bind("r"), s); err != nil || string(got) != "shared" {
			t.Fatalf("server %d: %q %v", i, got, err)
		}
	}
}

func TestKeyFileRules(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "root.key")
	if err := WriteKeyFile(p); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyFile(p); err == nil {
		t.Fatal("an existing key was overwritten")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if _, err := OpenKeyFile(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeyFile(p); err == nil || !strings.Contains(err.Error(), "every user") {
		t.Fatalf("a world-readable key file was accepted: %v", err)
	}
	if err := os.Chmod(p, 0o440); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeyFile(p); err != nil {
		t.Fatalf("a group-readable key file (a mounted Secret) was refused: %v", err)
	}
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeyFile(bad); err == nil || strings.Contains(err.Error(), "not a key") {
		t.Fatalf("a bad key file: %v", err)
	}
}

func TestEnvHolder(t *testing.T) {
	key := mustKey(t)
	_ = key
	b64 := "" // built from a file the way init does
	p := filepath.Join(t.TempDir(), "k")
	if err := WriteKeyFile(p); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	b64 = string(raw)
	h, err := OpenEnv("X", func(string) string { return b64 })
	if err != nil || h.Kind() != "env" {
		t.Fatalf("%v", err)
	}
	if _, err := OpenEnv("X", func(string) string { return "" }); err == nil {
		t.Fatal("an empty variable was accepted")
	}
	if _, err := OpenEnv("X", func(string) string { return "short" }); err == nil {
		t.Fatal("a short key was accepted")
	}
}

func TestPassphraseKeystore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ks.json")
	if err := CreateKeystore(ctx, p, []byte("short")); err == nil {
		t.Fatal("a short passphrase was accepted")
	}
	pass := []byte("correct horse battery staple")
	if err := CreateKeystore(ctx, p, pass); err != nil {
		t.Fatal(err)
	}
	if err := CreateKeystore(ctx, p, pass); err == nil {
		t.Fatal("overwrote a keystore")
	}
	h1, err := OpenKeystore(ctx, p, pass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeystore(ctx, p, []byte("wrong passphrase here")); err == nil {
		t.Fatal("a wrong passphrase unlocked the keystore")
	}
	// The same keystore unlocks to the same root key: a value wrapped by one opens with the other.
	h2, _ := OpenKeystore(ctx, p, pass)
	w, _ := h1.Wrap(ctx, mustKey(t), []byte("aad"))
	if _, err := h2.Unwrap(ctx, w, []byte("aad")); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.Unwrap(ctx, w, []byte("other")); err == nil {
		t.Fatal("wrapped under other authenticated data opened")
	}
	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, append(pass, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadPassphrase(pf); err != nil || string(got) != string(pass) {
		t.Fatalf("%q %v", got, err)
	}
}

func TestParserNeverPanics(t *testing.T) {
	db := newDB(t)
	k, _ := Open(ctx, db, holder(t), Options{})
	s, _ := k.Seal(ctx, bind("r"), []byte("x"))
	for i := 0; i <= len(s); i++ {
		_, _ = k.Open(ctx, bind("r"), s[:i]) // truncated
	}
	for _, junk := range []string{"", Prefix, Prefix + "!!!", Prefix + "AAAA", s + "AAAA", "plain text"} {
		if _, err := k.Open(ctx, bind("r"), junk); err == nil {
			t.Errorf("%q opened", junk)
		}
	}
}

func FuzzOpen(f *testing.F) {
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(f.TempDir(), "f.db"))
	db.SetMaxOpenConns(1)
	_, _ = db.Exec(strings.ReplaceAll(SchemaSQL, "{{BIG}}", "INTEGER"))
	k, err := Open(ctx, testDB{db}, func() Holder { h, _ := FromKey("key_file", "f", make([]byte, 32)); return h }(), Options{})
	if err != nil {
		f.Fatal(err)
	}
	s, _ := k.Seal(ctx, bind("r"), []byte("x"))
	f.Add(s)
	f.Add(Prefix)
	f.Fuzz(func(t *testing.T, in string) {
		if got, err := k.Open(ctx, bind("r"), in); err == nil && in != s {
			t.Fatalf("%q opened to %q", in, got)
		}
	})
}
