package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *logCapture) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }

// setupFor makes a server setup over a database, with the given encryption settings.
func setupFor(t *testing.T, addr string, enc config.EncryptionConfig) (*serverSetup, *logCapture) {
	t.Helper()
	dir := t.TempDir()
	lc := &logCapture{}
	cfg := &config.Config{Data: dir, Database: addr, Encryption: enc}
	return &serverSetup{cfg: cfg, log: slog.New(slog.NewTextHandler(lc, nil))}, lc
}

func engineAt(t *testing.T, addr string) *engine.Engine {
	t.Helper()
	e, err := engine.Open(engine.Options{Database: addr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// seedLegacyShared writes a shared value as releases before encryption at rest did: in the clear.
func seedLegacyShared(e *engine.Engine, key, value string) error {
	_, err := e.Database().Exec(context.Background(), `INSERT INTO meta(key, value) VALUES(?, ?)`, "shared:"+key, value)
	return err
}

func TestNewInstallationGetsAGeneratedKeyFile(t *testing.T) {
	if testdb.Postgres() {
		t.Skip("a key is generated for SQLite only")
	}
	addr := testdb.New(t)
	e := engineAt(t, addr)
	su, lc := setupFor(t, addr, config.EncryptionConfig{})
	svc, err := su.startSecrets(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Encrypted() {
		t.Fatal("a new installation is not encrypted at rest")
	}
	path := config.DefaultKeyFile(su.cfg.Data)
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the generated key file: %v %v", fi, err)
	}
	if !strings.Contains(lc.String(), "root key was generated") || strings.Contains(lc.String(), mustRead(t, path)) {
		t.Fatalf("the log must say a key was made, and never show it:\n%s", lc.String())
	}
	if v, err := svc.SharedValue("runner_token", "tok-canary"); err != nil || v != "tok-canary" {
		t.Fatal(err)
	}
	// The next start (same data directory) finds the file and carries on.
	su2 := &serverSetup{cfg: su.cfg, log: su.log}
	svc2, err := su2.startSecrets(context.Background(), engineAt(t, addr))
	if err != nil || !svc2.Encrypted() {
		t.Fatalf("restart: %v", err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// An installation that already holds secrets and has no root key keeps
// starting, says so loudly, and nothing is generated behind its back.
func TestExistingInstallationStartsNotEncryptedWithAWarning(t *testing.T) {
	// The advice differs inside a cluster (apply the chart) and outside (make a key); this test is
	// about the second, wherever it runs. A pipeline step runs in a pod, where the variable is set.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	addr := testdb.New(t)
	e := engineAt(t, addr)
	if err := seedLegacyShared(e, "runner_token", "tok-plain"); err != nil { // what an older release left
		t.Fatal(err)
	}
	su, lc := setupFor(t, addr, config.EncryptionConfig{})
	svc, err := su.startSecrets(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Encrypted() {
		t.Fatal("encrypted without a key")
	}
	if !strings.Contains(lc.String(), "NOT ENCRYPTED AT REST") || !strings.Contains(lc.String(), "launchway encryption init") {
		t.Fatalf("the warning is missing or not plain:\n%s", lc.String())
	}
	if config.FileExists(config.DefaultKeyFile(su.cfg.Data)) {
		t.Fatal("a key was generated for an installation that has secrets")
	}
	if v, err := svc.SharedValue("runner_token", "x"); err != nil || v != "tok-plain" {
		t.Fatalf("%q %v", v, err)
	}
	// encryption.require refuses instead.
	su2, _ := setupFor(t, addr, config.EncryptionConfig{Require: true})
	if _, err := su2.startSecrets(context.Background(), e); err == nil || !strings.Contains(err.Error(), "encryption.require") {
		t.Fatalf("require: %v", err)
	}
	// And so does asking never to generate, on a new installation, run without a key: it only warns.
	addr2 := testdb.New(t)
	su3, lc3 := setupFor(t, addr2, config.EncryptionConfig{NoGenerate: true})
	if s, err := su3.startSecrets(context.Background(), engineAt(t, addr2)); err != nil || s.Encrypted() || !strings.Contains(lc3.String(), "NOT ENCRYPTED") {
		t.Fatalf("no_generate: %v", err)
	}
}

func writeKey(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "root.key")
	if err := keyring.WriteKeyFile(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A wrong key, and no key for an encrypted database, refuse to start with a
// plain message, and never re-key.
func TestWrongKeyAndMissingKeyRefuseToStart(t *testing.T) {
	addr := testdb.New(t)
	e := engineAt(t, addr)
	good := writeKey(t)
	su, _ := setupFor(t, addr, config.EncryptionConfig{KeyFile: good})
	if _, err := su.startSecrets(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	bad, _ := setupFor(t, addr, config.EncryptionConfig{KeyFile: writeKey(t)})
	_, err := bad.startSecrets(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "does not open this database's encryption keys") || !strings.Contains(err.Error(), "never re-keys") {
		t.Fatalf("wrong key: %v", err)
	}
	none, _ := setupFor(t, addr, config.EncryptionConfig{NoGenerate: true})
	_, err = none.startSecrets(context.Background(), e)
	if !errors.Is(err, sealing.ErrDatabaseEncrypted) {
		t.Fatalf("no key for an encrypted database: %v", err)
	}
	// With no key and generation allowed, an encrypted database is still not "fresh": it refuses, it does not make a new key.
	none2, _ := setupFor(t, addr, config.EncryptionConfig{})
	if _, err = none2.startSecrets(context.Background(), e); !errors.Is(err, sealing.ErrDatabaseEncrypted) {
		t.Fatalf("no key, generation allowed: %v", err)
	}
	if config.FileExists(config.DefaultKeyFile(none2.cfg.Data)) {
		t.Fatal("a refused start generated a key")
	}
	// Still opens with the right key.
	if _, err := su.startSecrets(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationOfSeveralHoldersIsRefused(t *testing.T) {
	t.Setenv(config.EnvRootKey, mustRead(t, writeKey(t)))
	_, err := openRootKeys(context.Background(), config.EncryptionConfig{KeyFile: writeKey(t)}, os.Getenv, nil)
	if err == nil || !strings.Contains(err.Error(), "encryption.holder") {
		t.Fatalf("%v", err)
	}
	rk, err := openRootKeys(context.Background(), config.EncryptionConfig{KeyFile: writeKey(t), Holder: "key_file"}, os.Getenv, nil)
	if err != nil || rk.kind != "key_file" {
		t.Fatalf("%v", err)
	}
	rk, err = openRootKeys(context.Background(), config.EncryptionConfig{}, os.Getenv, nil)
	if err != nil || rk.kind != "env" {
		t.Fatalf("the environment holder: %v", err)
	}
	// A world-readable key file is refused at start.
	p := writeKey(t)
	_ = os.Chmod(p, 0o644)
	if _, err := openRootKeys(context.Background(), config.EncryptionConfig{KeyFile: p}, func(string) string { return "" }, nil); err == nil || !strings.Contains(err.Error(), "every user") {
		t.Fatalf("%v", err)
	}
}

func TestEncryptionInitNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "launchway.yaml")
	if err := os.WriteFile(cfgFile, []byte("data: "+dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keys", "root.key")
	out, err := captureStdout(t, func() error { return cmdEncryption([]string{"init", "--config", cfgFile, "--key-file", path}) })
	if err != nil || !strings.Contains(out, "Keep a copy") || !strings.Contains(out, "encryption.key_file: "+path) {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, mustRead(t, path)) {
		t.Fatal("init printed the key")
	}
	if _, err := captureStdout(t, func() error { return cmdEncryption([]string{"init", "--config", cfgFile, "--key-file", path}) }); err == nil || !strings.Contains(err.Error(), "never overwrites") {
		t.Fatalf("%v", err)
	}
	// A passphrase keystore, with the passphrase from a file.
	pf := filepath.Join(dir, "pass")
	_ = os.WriteFile(pf, []byte("a long enough passphrase\n"), 0o600)
	ks := filepath.Join(dir, "root.keystore")
	if _, err := captureStdout(t, func() error {
		return cmdEncryption([]string{"init", "--passphrase", "--config", cfgFile, "--keystore", ks, "--passphrase-file", pf})
	}); err != nil {
		t.Fatal(err)
	}
	rk, err := openRootKeys(context.Background(), config.EncryptionConfig{Keystore: ks, PassphraseFile: pf}, func(string) string { return "" }, nil)
	if err != nil || rk.kind != "passphrase" {
		t.Fatalf("%v", err)
	}
}

// The restore drill: a database dump and the root key make a working server;
// the dump alone refuses with the plain message; a wrong key refuses. The
// doctor says "a dump alone cannot reveal secrets" only when that is true.
func TestRestoreDrillAndDoctorEncryption(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "launchway.db")
	e, err := engine.Open(engine.Options{Database: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := seedLegacyShared(e, "runner_token", "restore-canary"); err != nil { // an installation from before
		t.Fatal(err)
	}
	e.Close()
	keyFile := filepath.Join(dir, "keys", "root.key")
	cfgFile := filepath.Join(dir, "launchway.yaml")
	writeCfg := func(path, database, key string) {
		t.Helper()
		body := "data: " + dir + "\ndatabase: " + database + "\n"
		if key != "" {
			body += "encryption:\n  key_file: " + key + "\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LAUNCHWAY_DATABASE", "")
	writeCfg(cfgFile, db, "")

	// Not encrypted: the doctor says so, and exits 1.
	out, err := captureStdout(t, func() error { return cmdDoctor([]string{"encryption", "--config", cfgFile}) })
	var ex exitErr
	if !errors.As(err, &ex) || !strings.Contains(out, "NOT ENCRYPTED AT REST") || !strings.Contains(out, "dump alone cannot reveal secrets: NO") {
		t.Fatalf("before: %v\n%s", err, out)
	}
	if out, _ := captureStdout(t, func() error { return cmdDoctor([]string{"--config", cfgFile}) }); !strings.Contains(out, "Encryption at rest: NOT ENCRYPTED") {
		t.Fatalf("the plain doctor does not say it:\n%s", out)
	}

	// init, configure, and let a server (rotate here) seal in place.
	if _, err := captureStdout(t, func() error { return cmdEncryption([]string{"init", "--config", cfgFile, "--key-file", keyFile}) }); err != nil {
		t.Fatal(err)
	}
	writeCfg(cfgFile, db, keyFile)
	out, err = captureStdout(t, func() error { return cmdDoctor([]string{"encryption", "--config", cfgFile}) })
	if err == nil || !strings.Contains(out, "configured, but no server has started with it yet") {
		t.Fatalf("configured but unused: %v\n%s", err, out)
	}
	if out, err := captureStdout(t, func() error { return cmdEncryption([]string{"rotate", "--config", cfgFile}) }); err != nil || !strings.Contains(out, "now current") {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = captureStdout(t, func() error { return cmdDoctor([]string{"encryption", "--config", cfgFile, "--restore-drill"}) })
	if err != nil || !strings.Contains(out, "dump alone cannot reveal secrets: yes") || !strings.Contains(out, "1 sealed secrets opened, 0 failed") {
		t.Fatalf("after: %v\n%s", err, out)
	}

	// The dump: a copy of the database file. With the key it works; alone it refuses.
	dump := filepath.Join(dir, "restored.db")
	raw, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("restore-canary")) {
		t.Fatal("the database file still holds the secret in the clear")
	}
	if err := os.WriteFile(dump, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(dir, "restored.yaml")
	writeCfg(restored, dump, keyFile)
	if out, err := captureStdout(t, func() error { return cmdDoctor([]string{"encryption", "--config", restored, "--restore-drill"}) }); err != nil || !strings.Contains(out, "1 sealed secrets opened") {
		t.Fatalf("restore with the key: %v\n%s", err, out)
	}
	writeCfg(restored, dump, "")
	out, err = captureStdout(t, func() error { return cmdDoctor([]string{"encryption", "--config", restored}) })
	if err == nil || !strings.Contains(err.Error(), "no root key is configured") || !strings.Contains(out, "dump alone cannot reveal secrets: yes") {
		t.Fatalf("restore without the key: %v\n%s", err, out)
	}
	other := filepath.Join(dir, "other.key")
	if err := keyring.WriteKeyFile(other); err != nil {
		t.Fatal(err)
	}
	writeCfg(restored, dump, other)
	if _, err := captureStdout(t, func() error { return cmdDoctor([]string{"encryption", "--config", restored}) }); err == nil || !strings.Contains(err.Error(), "never re-keys") {
		t.Fatalf("restore with a wrong key: %v", err)
	}
}

// Root key replacement through the commands: new key as the root key, old as
// previous, rewrap; then the new key alone works and the old does not.
func TestRootKeyReplacementThroughTheCommands(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "launchway.db")
	t.Setenv("LAUNCHWAY_DATABASE", "")
	oldKey, newKey := filepath.Join(dir, "old.key"), filepath.Join(dir, "new.key")
	for _, k := range []string{oldKey, newKey} {
		if err := keyring.WriteKeyFile(k); err != nil {
			t.Fatal(err)
		}
	}
	cfg := func(key, prev string) string {
		p := filepath.Join(t.TempDir(), "c.yaml")
		body := fmt.Sprintf("data: %s\ndatabase: %s\nencryption:\n  key_file: %s\n", dir, db, key)
		if prev != "" {
			body += "  previous_key_file: " + prev + "\n"
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := captureStdout(t, func() error { return cmdEncryption([]string{"rotate", "--config", cfg(oldKey, "")}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return cmdEncryption([]string{"rewrap", "--config", cfg(newKey, "")}) }); err == nil {
		t.Fatal("rewrap without the old key")
	}
	if out, err := captureStdout(t, func() error { return cmdEncryption([]string{"rewrap", "--config", cfg(newKey, oldKey)}) }); err != nil || !strings.Contains(out, "now wrapped by the configured key_file root key") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := captureStdout(t, func() error { return cmdDoctor([]string{"encryption", "--config", cfg(newKey, "")}) }); err != nil {
		t.Fatalf("the new key alone: %v", err)
	}
	if _, err := captureStdout(t, func() error { return cmdDoctor([]string{"encryption", "--config", cfg(oldKey, "")}) }); err == nil {
		t.Fatal("the old key still opens the database")
	}
}

// Servers starting at once over one new installation (one data directory, one
// database, no key anywhere yet) all end up with the same one key: exactly one
// makes the file, the rest use it, none fails, and every secret opens from
// each.
func TestServersStartingTogetherOnANewInstallationShareOneGeneratedKey(t *testing.T) {
	if testdb.Postgres() {
		t.Skip("a key is generated for SQLite only")
	}
	addr := testdb.New(t)
	dir := t.TempDir()
	const n = 6
	svcs := make([]*sealing.Service, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		e := engineAt(t, addr)
		go func() {
			defer wg.Done()
			su := &serverSetup{cfg: &config.Config{Data: dir, Database: addr}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			<-start
			svcs[i], errs[i] = su.startSecrets(context.Background(), e)
		}()
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil || !svcs[i].Encrypted() {
			t.Fatalf("server %d: %v", i, errs[i])
		}
	}
	if v, err := svcs[0].SharedValue("runner_token", "tok-canary"); err != nil || v != "tok-canary" {
		t.Fatal(v, err)
	}
	for i := range n {
		if v, err := svcs[i].SharedValue("runner_token", "other"); err != nil || v != "tok-canary" {
			t.Fatalf("server %d reads %q: %v (it holds a different key)", i, v, err)
		}
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".root.key.") {
			t.Errorf("a temporary key file was left: %s", f.Name())
		}
	}
	if fi, err := os.Stat(config.DefaultKeyFile(dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
}

// A key file inside the data directory is stored with the database (SQLite
// lives there, and file backups and volume snapshots copy the directory), and
// the server, the report and the doctor say so in plain words; a key file
// elsewhere draws no such warning.
func TestKeyInTheDataDirectoryIsWarnedAbout(t *testing.T) {
	if testdb.Postgres() {
		t.Skip("a key is generated for SQLite only")
	}
	addr := testdb.New(t)
	su, lc := setupFor(t, addr, config.EncryptionConfig{})
	svc, err := su.startSecrets(context.Background(), engineAt(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lc.String(), "stored together") {
		t.Fatalf("the log lacks the warning:\n%s", lc.String())
	}
	rep, err := svc.Report(context.Background())
	if err != nil || !strings.Contains(strings.Join(rep.Warnings, "\n"), "stored together") {
		t.Fatalf("the report lacks the warning: %v %v", rep.Warnings, err)
	}

	addr2 := testdb.New(t)
	other := filepath.Join(t.TempDir(), "root.key")
	if err := keyring.WriteKeyFile(other); err != nil {
		t.Fatal(err)
	}
	su2, lc2 := setupFor(t, addr2, config.EncryptionConfig{KeyFile: other})
	svc2, err := su2.startSecrets(context.Background(), engineAt(t, addr2))
	if err != nil {
		t.Fatal(err)
	}
	rep2, _ := svc2.Report(context.Background())
	if strings.Contains(lc2.String(), "stored together") || strings.Contains(strings.Join(rep2.Warnings, "\n"), "stored together") {
		t.Fatal("a key outside the data directory was warned about")
	}
}

func kekCount(t *testing.T, e *engine.Engine) int {
	t.Helper()
	var n int
	if err := e.Database().QueryRow(context.Background(), `SELECT count(*) FROM encryption_keks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// An image-only upgrade under an old chart (PostgreSQL, no root key mounted)
// must not invent a root key or a key-encryption key: it serves, not
// encrypted, and says how to set encryption up. A later start with a
// configured key creates the first key-encryption key once.
func TestPostgresWithNoRootKeyCreatesNoKeys(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("PostgreSQL only")
	}
	addr := testdb.New(t)
	e := engineAt(t, addr)
	su, lc := setupFor(t, addr, config.EncryptionConfig{})
	svc, err := su.startSecrets(context.Background(), e)
	if err != nil || svc.Encrypted() {
		t.Fatalf("%v", err)
	}
	if kekCount(t, e) != 0 || config.FileExists(config.DefaultKeyFile(su.cfg.Data)) {
		t.Fatal("a key was made for a PostgreSQL installation with no root key")
	}
	if !strings.Contains(lc.String(), "NOT ENCRYPTED") || strings.Contains(lc.String(), "root key was generated") {
		t.Fatalf("log:\n%s", lc.String())
	}
	rep, err := svc.Report(context.Background())
	if err != nil || !strings.Contains(strings.Join(rep.Warnings, "\n"), "encryption is not set up: no root key is configured") {
		t.Fatalf("%v %v", rep.Warnings, err)
	}
	// Later, with a configured key: one key-encryption key, and sealing proceeds.
	key := writeKey(t)
	for range 2 {
		su2, _ := setupFor(t, addr, config.EncryptionConfig{KeyFile: key})
		s2, err := su2.startSecrets(context.Background(), engineAt(t, addr))
		if err != nil || !s2.Encrypted() {
			t.Fatalf("%v", err)
		}
	}
	if n := kekCount(t, e); n != 1 {
		t.Fatalf("%d key-encryption keys", n)
	}
}

// A database that holds data from an earlier release, on SQLite, with no key
// and no key file: nothing is generated. A key file already in the data
// directory is still used.
func TestUpgradedSQLiteWithDataGeneratesNoKey(t *testing.T) {
	if testdb.Postgres() {
		t.Skip("SQLite only")
	}
	addr := testdb.New(t)
	e := engineAt(t, addr)
	if _, err := e.Database().Exec(context.Background(), `INSERT INTO meta(key, value) VALUES('something_an_earlier_release_kept', '1')`); err != nil {
		t.Fatal(err)
	}
	su, lc := setupFor(t, addr, config.EncryptionConfig{})
	svc, err := su.startSecrets(context.Background(), e)
	if err != nil || svc.Encrypted() || kekCount(t, e) != 0 || config.FileExists(config.DefaultKeyFile(su.cfg.Data)) {
		t.Fatalf("generated for an upgrade: %v", err)
	}
	if !strings.Contains(lc.String(), "already holds data") {
		t.Fatalf("log:\n%s", lc.String())
	}
	// An existing key file in the data directory is still used.
	if err := keyring.WriteKeyFile(config.DefaultKeyFile(su.cfg.Data)); err != nil {
		t.Fatal(err)
	}
	if s, err := su.startSecrets(context.Background(), e); err != nil || !s.Encrypted() {
		t.Fatalf("existing key file not used: %v", err)
	}
}

// A data directory that cannot be written gets no generated key.
func TestUnwritableDataDirectoryGeneratesNoKey(t *testing.T) {
	if testdb.Postgres() {
		t.Skip("SQLite only")
	}
	addr := testdb.New(t)
	e := engineAt(t, addr)
	su, _ := setupFor(t, addr, config.EncryptionConfig{})
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	su.cfg.Data = filepath.Join(f, "data") // a path below a file: cannot be made
	svc, err := su.startSecrets(context.Background(), e)
	if err != nil || svc.Encrypted() || kekCount(t, e) != 0 {
		t.Fatalf("%v", err)
	}
}
