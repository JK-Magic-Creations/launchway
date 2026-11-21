package sealing

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func openEngine(t testing.TB, addr string) *engine.Engine {
	t.Helper()
	e, err := engine.Open(engine.Options{Database: addr, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func newHolder(t testing.TB) keyring.Holder {
	t.Helper()
	k, err := keyring.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := keyring.FromKey("key_file", "test", k)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func start(t testing.TB, e *engine.Engine, h keyring.Holder) *Service {
	t.Helper()
	s, err := Start(context.Background(), Options{DB: e.Database(), Holder: h, Log: quiet, Instance: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var ctx = context.Background()

func metaValue(t testing.TB, e *engine.Engine, key string) string {
	t.Helper()
	var v string
	if err := e.Database().QueryRow(ctx, `SELECT value FROM meta WHERE key = ?`, "shared:"+key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSharedValueIsSealedAtRestAndSharedByEveryServer(t *testing.T) {
	addr := testdb.New(t)
	e1, e2 := openEngine(t, addr), openEngine(t, addr)
	h := newHolder(t)
	s1, s2 := start(t, e1, h), start(t, e2, h)
	v1, err := s1.SharedValue("login_state_key", "proposal-one-canary")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s2.SharedValue("login_state_key", "proposal-two-canary")
	if err != nil {
		t.Fatal(err)
	}
	if v1 != v2 || (v1 != "proposal-one-canary" && v1 != "proposal-two-canary") {
		t.Fatalf("servers disagree: %q %q", v1, v2)
	}
	raw := metaValue(t, e1, "login_state_key")
	if !keyring.IsSealed(raw) || strings.Contains(raw, "canary") {
		t.Fatalf("stored as %q", raw)
	}
	// Moved to another meta row it does not open (bound to its row).
	if _, err := e1.Database().Exec(ctx, `INSERT INTO meta(key, value) VALUES('shared:other', ?)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.SharedValue("other", "x"); err == nil {
		t.Fatal("a sealed value moved to another row opened")
	}
}

func TestNoRootKeyKeepsWorkingAndSaysSo(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	s, err := Start(ctx, Options{DB: e.Database(), Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if s.Encrypted() {
		t.Fatal("encrypted without a key")
	}
	if v, err := s.SharedValue("runner_token", "tok-plain"); err != nil || v != "tok-plain" {
		t.Fatalf("%q %v", v, err)
	}
	if metaValue(t, e, "runner_token") != "tok-plain" {
		t.Fatal("not stored as before")
	}
	// New sensitive features refuse, and say exactly how to create a key.
	err = s.RequireRootKey("Git over SSH")
	if err == nil || !strings.Contains(err.Error(), "launchway encryption init") {
		t.Fatalf("%v", err)
	}
	if _, err := s.HostKeys().Seal(ctx, "ed25519", []byte("x")); err == nil || !strings.Contains(err.Error(), "launchway encryption init") {
		t.Fatalf("sealing without a key: %v", err)
	}
	r, err := s.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Encrypted || r.DumpAloneIsUseless || !strings.HasPrefix(r.Summary, "NOT ENCRYPTED AT REST") || len(r.Warnings) == 0 {
		t.Fatalf("%+v", r)
	}
	if r.Remaining != 1 {
		t.Fatalf("remaining %d", r.Remaining)
	}
}

// An encrypted database refuses to start with no key, and with a wrong one,
// plainly, and never re-keys.
func TestWrongOrMissingKeyRefuses(t *testing.T) {
	addr := testdb.New(t)
	e := openEngine(t, addr)
	h := newHolder(t)
	start(t, e, h)
	if _, err := Start(ctx, Options{DB: e.Database(), Log: quiet}); err == nil || err != ErrDatabaseEncrypted {
		t.Fatalf("no key: %v", err)
	}
	_, err := Start(ctx, Options{DB: e.Database(), Holder: newHolder(t), Log: quiet})
	var wk *keyring.ErrWrongKey
	if err == nil || !strings.Contains(err.Error(), "never re-keys") {
		t.Fatalf("wrong key: %v", err)
	}
	_ = wk
	var n int
	_ = e.Database().QueryRow(ctx, `SELECT count(*) FROM encryption_keks`).Scan(&n)
	if n != 1 {
		t.Fatalf("a failed start made keys: %d", n)
	}
	// The right key still starts it.
	start(t, e, h)
}

// legacySeed writes what the previous release stored: plaintext keys in
// meta, a host key and a webhook secret sealed with keys beside them.
func legacySeed(t *testing.T, e *engine.Engine) (hostDER []byte) {
	t.Helper()
	db := e.Database()
	put := func(k, v string) {
		if _, err := db.Exec(ctx, `INSERT INTO meta(key, value) VALUES(?, ?)`, "shared:"+k, v); err != nil {
			t.Fatal(err)
		}
	}
	hostSeal, whSeal := auth.NewKey(), auth.NewKey()
	put("ssh_host_key_seal", hostSeal)
	put("webhook_secret_seal", whSeal)
	put("login_state_key", "legacy-login-key-canary")
	put("hosted_report_key", "legacy-report-key-canary")
	put("git_run_token_key", "legacy-run-token-canary")
	put("runner_token", "legacy-runner-token-canary")
	hs, _ := auth.NewSealer(hostSeal, "ssh-host-key")
	hostDER = []byte("legacy-der-bytes-canary-0123456789")
	blob, err := hs.Seal(hostDER)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ssh_host_keys(algo, public, sealed, fingerprint) VALUES('ed25519','pub',?,'fp')`, blob); err != nil {
		t.Fatal(err)
	}
	ws, _ := auth.NewSealer(whSeal, "webhook-secret")
	wblob, _ := ws.Seal("legacy-webhook-secret-canary")
	if _, err := db.Exec(ctx, `INSERT INTO repo_webhooks(id, name, url, secret_sealed) VALUES('hok_1','h','https://example.test/h',?)`, wblob); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO repo_webhooks(id, name, url, secret_sealed) VALUES('hok_2','nosecret','https://example.test/n','')`); err != nil {
		t.Fatal(err)
	}
	return hostDER
}

func TestFirstStartWithAKeySealsExistingPlaintextInPlace(t *testing.T) {
	addr := testdb.New(t)
	e := openEngine(t, addr)
	der := legacySeed(t, e)

	// Before: a not-encrypted server reads them all.
	plain, err := Start(ctx, Options{DB: e.Database(), Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := plain.Report(ctx); r.Remaining != 8 { // six shared values (the two old sealing keys among them), a host key and a webhook secret; the empty secret is not one
		t.Fatalf("before: %d remaining", r.Remaining)
	}
	if v, _ := plain.SharedValue("login_state_key", "x"); v != "legacy-login-key-canary" {
		t.Fatalf("%q", v)
	}

	h := newHolder(t)
	s := start(t, e, h)
	r, err := s.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Remaining != 0 || !r.DumpAloneIsUseless || r.Migration.State != "complete" {
		t.Fatalf("after: %+v", r)
	}
	// The same values, now sealed.
	for key, want := range map[string]string{"login_state_key": "legacy-login-key-canary", "runner_token": "legacy-runner-token-canary", "git_run_token_key": "legacy-run-token-canary"} {
		got, err := s.SharedValue(key, "unused")
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", key, got, err)
		}
		if !keyring.IsSealed(metaValue(t, e, key)) {
			t.Fatalf("%s is not sealed", key)
		}
	}
	var host string
	_ = e.Database().QueryRow(ctx, `SELECT sealed FROM ssh_host_keys WHERE algo = 'ed25519'`).Scan(&host)
	if got, err := s.HostKeys().Open(ctx, "ed25519", host); err != nil || string(got) != string(der) {
		t.Fatalf("host key: %q %v", got, err)
	}
	var hook string
	_ = e.Database().QueryRow(ctx, `SELECT secret_sealed FROM repo_webhooks WHERE id = 'hok_1'`).Scan(&hook)
	if got, err := s.Webhooks().Open(ctx, "hok_1", hook); err != nil || string(got) != "legacy-webhook-secret-canary" {
		t.Fatalf("webhook: %q %v", got, err)
	}
	// A webhook secret does not open as another hook's.
	if _, err := s.Webhooks().Open(ctx, "hok_2", hook); err == nil {
		t.Fatal("a webhook secret opened as another hook's")
	}
	// The sealing keys that sat beside the data are gone.
	for _, k := range []string{"ssh_host_key_seal", "webhook_secret_seal"} {
		var n int
		_ = e.Database().QueryRow(ctx, `SELECT count(*) FROM meta WHERE key = ?`, "shared:"+k).Scan(&n)
		if n != 0 {
			t.Errorf("the old sealing key %s is still in the database", k)
		}
	}
	// Idempotent: starting again changes nothing and runs no pass.
	before := metaValue(t, e, "login_state_key")
	s2 := start(t, e, h)
	if metaValue(t, e, "login_state_key") != before || s2.passes.Load() != 0 {
		t.Fatalf("a second start did work: passes %d", s2.passes.Load())
	}
	// After everything is sealed, a value in the clear is refused, not believed.
	if _, err := e.Database().Exec(ctx, `UPDATE meta SET value = 'planted-in-the-clear' WHERE key = 'shared:login_state_key'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.SharedValue("login_state_key", "x"); err == nil || !strings.Contains(err.Error(), "in the clear") {
		t.Fatalf("a planted plaintext value: %v", err)
	}
}

// Servers starting together over one database: one seals, the others wait,
// and every row ends sealed exactly once with the same value.
func TestSeveralServersStartingTogetherSealOnce(t *testing.T) {
	addr := testdb.New(t)
	seed := openEngine(t, addr)
	legacySeed(t, seed)
	for i := 0; i < 150; i++ { // enough webhooks for several batches
		if _, err := seed.Database().Exec(ctx, `INSERT INTO repo_webhooks(id, name, url, secret_sealed) VALUES(?,?,?,'')`, "hk"+string(rune('a'+i%26))+strings.Repeat("x", i/26), "n", "https://example.test/x"); err != nil {
			t.Fatal(err)
		}
	}
	h := newHolder(t)
	const servers = 6
	svcs := make([]*Service, servers)
	errs := make([]error, servers)
	engines := make([]*engine.Engine, servers)
	for i := range engines {
		engines[i] = openEngine(t, addr)
	}
	var wg sync.WaitGroup
	for i := range svcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svcs[i], errs[i] = Start(ctx, Options{DB: engines[i].Database(), Holder: h, Log: quiet, Instance: string(rune('A' + i))})
		}()
	}
	wg.Wait()
	var sealed int64
	for i, err := range errs {
		if err != nil {
			t.Fatalf("server %d: %v", i, err)
		}
		sealed += svcs[i].sealedRows.Load()
	}
	if sealed != 8 {
		t.Errorf("rows sealed across all servers: %d (each exactly once is %d)", sealed, 8)
	}
	r, _ := svcs[0].Report(ctx)
	if r.Remaining != 0 || r.Migration.State != "complete" {
		t.Fatalf("%+v", r)
	}
	for i, s := range svcs {
		if v, err := s.SharedValue("runner_token", "x"); err != nil || v != "legacy-runner-token-canary" {
			t.Fatalf("server %d: %q %v", i, v, err)
		}
	}
	var keks int
	_ = seed.Database().QueryRow(ctx, `SELECT count(*) FROM encryption_keks`).Scan(&keks)
	if keks != 1 {
		t.Fatalf("%d key-encryption keys", keks)
	}
}

// A server that dies holding the lease hands the work to the next.
func TestAnExpiredLeaseIsTakenOver(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	legacySeed(t, e)
	h := newHolder(t)
	// Another server took the lease and died: its lease ran out long ago.
	if _, err := e.Database().Exec(ctx, `INSERT INTO encryption_state(key, value) VALUES('migration_lease', 'dead|1000')`); err != nil {
		t.Fatal(err)
	}
	s := start(t, e, h)
	if r, _ := s.Report(ctx); r.Remaining != 0 {
		t.Fatalf("%+v", r)
	}
	// A lease that is alive is respected: the starting server waits, then carries on.
	e2 := openEngine(t, testdb.New(t))
	legacySeed(t, e2)
	live := "other|" + millis(timeNowPlus(1))
	if _, err := e2.Database().Exec(ctx, `INSERT INTO encryption_state(key, value) VALUES('migration_lease', ?)`, live); err != nil {
		t.Fatal(err)
	}
	s2, err := Start(ctx, Options{DB: e2.Database(), Holder: h, Log: quiet, Wait: 600 * 1e6})
	if err != nil {
		t.Fatal(err)
	}
	if s2.passes.Load() != 0 {
		t.Fatal("worked under another server's live lease")
	}
}

func TestRotationOnlineKeepsEverythingReadable(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	legacySeed(t, e)
	h := newHolder(t)
	s := start(t, e, h)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var failures []string
	var mu sync.Mutex
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if v, err := s.SharedValue("runner_token", "x"); err != nil || v != "legacy-runner-token-canary" {
					mu.Lock()
					failures = append(failures, "read: "+err2s(err))
					mu.Unlock()
				}
				sealed, err := s.Keys().Seal(ctx, keyring.Binding{Purpose: keyring.PurposeSecrets, Table: "t", Column: "c", Row: "r"}, []byte("w"))
				if err == nil {
					_, err = s.Keys().Open(ctx, keyring.Binding{Purpose: keyring.PurposeSecrets, Table: "t", Column: "c", Row: "r"}, sealed)
				}
				if err != nil {
					mu.Lock()
					failures = append(failures, "write: "+err.Error())
					mu.Unlock()
				}
			}
		}()
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Rotate(ctx, i == 2); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("%d failures during rotation, first: %s", len(failures), failures[0])
	}
	r, _ := s.Report(ctx)
	if r.Remaining != 0 || r.Keys.CurrentKEK != 4 {
		t.Fatalf("%+v", r.Keys)
	}
	// After the data rotation every stored value uses the newest data key.
	if v, _ := keyring.SealedVersion(metaValue(t, e, "runner_token")); v != 2 {
		t.Fatalf("data key version %d", v)
	}
	// A server started afterwards reads it all.
	if v, err := start(t, e, h).SharedValue("runner_token", "x"); err != nil || v != "legacy-runner-token-canary" {
		t.Fatalf("%q %v", v, err)
	}
}

func err2s(err error) string {
	if err == nil {
		return "wrong value"
	}
	return err.Error()
}

// Replacing the root key: old and new given together, rewrap, then the new
// alone starts it and the old does not.
func TestRootKeyReplacementProcedure(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	legacySeed(t, e)
	oldH, newH := newHolder(t), newHolder(t)
	start(t, e, oldH)
	if _, err := Start(ctx, Options{DB: e.Database(), Holder: newH, Log: quiet}); err == nil {
		t.Fatal("the new key started the database before the re-wrap")
	}
	s, err := Start(ctx, Options{DB: e.Database(), Holder: newH, Previous: []keyring.Holder{oldH}, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Report(ctx); !r.Keys.NeedsRewrap || len(r.Warnings) == 0 {
		t.Fatalf("%+v", r.Warnings)
	}
	if n, err := s.Rewrap(ctx); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	s2, err := Start(ctx, Options{DB: e.Database(), Holder: newH, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s2.SharedValue("runner_token", "x"); err != nil || v != "legacy-runner-token-canary" {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := Start(ctx, Options{DB: e.Database(), Holder: oldH, Log: quiet}); err == nil {
		t.Fatal("the old key still opens the database")
	}
}

func TestIsFresh(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	if f, err := IsFresh(ctx, e.Database()); err != nil || !f {
		t.Fatalf("%v %v", f, err)
	}
	if _, err := Start(ctx, Options{DB: e.Database(), Log: quiet}); err != nil {
		t.Fatal(err)
	}
	s, _ := Start(ctx, Options{DB: e.Database(), Log: quiet})
	_, _ = s.SharedValue("runner_token", "x")
	if f, _ := IsFresh(ctx, e.Database()); f {
		t.Fatal("a database that holds a secret is fresh")
	}
}

func timeNowPlus(minutes int) time.Time { return time.Now().Add(time.Duration(minutes) * time.Minute) }
