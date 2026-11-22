package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The wall between Bases, Stage 1: the identity tables. These tests are the
// first slice of the isolation suite (docs/development/the-wall.md): two
// Bases with identical-looking people, and each asks for the other's things.

func openMulti(t *testing.T, addr string, clk *clock) *Engine {
	t.Helper()
	var buf syncBuffer
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Logger: slog.New(slog.NewTextHandler(&buf, nil)), MultiBase: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func tableNames(t *testing.T, e *Engine) []string {
	t.Helper()
	q := `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
	if e.db.pg {
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`
	}
	rows, err := e.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// keyColumns returns the columns of a table's primary key in order.
func keyColumns(t *testing.T, e *Engine, table string) []string {
	t.Helper()
	var out []string
	if !e.db.pg {
		rows, err := e.db.Query(`SELECT name FROM pragma_table_info('` + table + `') WHERE pk > 0 ORDER BY pk`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			out = append(out, n)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	rows, err := e.db.Query(`SELECT a.attname FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = ?::regclass AND i.indisprimary ORDER BY array_position(i.indkey::int[], a.attnum::int)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every table that exists is in the registry as global, Base or pending, and
// every registered table exists: a branch that adds a table without a line in
// store.Tables fails here.
func TestEveryTableIsInTheRegistry(t *testing.T) {
	e := mustOpenAs(t, testdb.New(t), newClock(), 0, &syncBuffer{})
	have := map[string]bool{}
	for _, n := range tableNames(t, e) {
		have[n] = true
		if _, ok := store.Lookup(n); !ok {
			t.Errorf("table %s is not in store.Tables: say whether it is global (the installation's), Base (carries base_id) or pending, in internal/store/tables.go (docs/development/the-wall.md)", n)
		}
	}
	seen := map[string]bool{}
	for _, tb := range store.Tables {
		if seen[tb.Name] {
			t.Errorf("table %s is listed twice in store.Tables", tb.Name)
		}
		seen[tb.Name] = true
		if !have[tb.Name] {
			t.Errorf("store.Tables lists %s, which the schema does not have", tb.Name)
		}
		if tb.Class == store.Pending && tb.Stage == "" {
			t.Errorf("pending table %s names no stage", tb.Name)
		}
		if tb.Class == store.Global && tb.Why == "" {
			t.Errorf("global table %s says nothing of why it is global", tb.Name)
		}
	}
}

// A Base table has base_id, and with several Bases on its primary key begins
// with it; with them off, nothing about the keys changed from before.
func TestBaseTablesCarryBaseIDAndBeginTheirKeysWithIt(t *testing.T) {
	addr, clk := testdb.New(t), newClock()
	single := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	for _, name := range store.Names(store.Scoped) {
		if !hasColumn(t, single, name, "base_id") {
			t.Errorf("Base table %s has no base_id column", name)
		}
	}
	if ok, err := single.wall.env.KeysAreComposite(); err != nil || ok {
		t.Fatalf("keys composite with several Bases off: %v %v", ok, err)
	}
	if got := keyColumns(t, single, "users"); strings.Join(got, ",") != "username" {
		t.Fatalf("with several Bases off, users keeps its old key: %v", got)
	}
	single.Close()

	multi := openMulti(t, addr, clk)
	for _, name := range store.Names(store.Scoped) {
		if got := keyColumns(t, multi, name); len(got) == 0 || got[0] != "base_id" {
			t.Errorf("Base table %s: primary key %v does not begin with base_id", name, got)
		}
	}
	// Opening again changes nothing and fails nothing.
	openMulti(t, addr, clk)
}

func TestSeveralBasesAreOffByDefault(t *testing.T) {
	e := mustOpenAs(t, testdb.New(t), newClock(), 0, &syncBuffer{})
	ctx := context.Background()
	if _, err := e.Bases().CreateBase(ctx, store.NewBase{Handle: "acme"}); !errors.Is(err, store.ErrDisabled) {
		t.Fatalf("creating a Base with the switch off: %v", err)
	}
	sc, err := e.Bases().ScopeForHost(ctx, "anything.example")
	if err != nil || sc.ID() != store.DefaultBaseID {
		t.Fatalf("with the switch off every host is the default Base: %v %v", sc, err)
	}
	bases, err := e.Bases().List(ctx)
	if err != nil || len(bases) != 1 || bases[0].ID != store.DefaultBaseID || bases[0].Handle != "default" {
		t.Fatalf("an installation has exactly the default Base: %+v %v", bases, err)
	}
}

type twoBases struct {
	e       *Engine
	id      *store.Identity
	a, b, c store.Scope // c is a Base with nothing in it: the control for "does not exist"
}

func newTwoBases(t *testing.T, e *Engine) twoBases {
	t.Helper()
	ctx := context.Background()
	mk := func(handle string) store.Scope {
		b, err := e.Bases().CreateBase(ctx, store.NewBase{Handle: handle, Host: handle + ".example.test", Now: 1})
		if err != nil {
			t.Fatalf("creating Base %s: %v", handle, err)
		}
		sc, err := e.Bases().ServiceScope(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		return sc
	}
	return twoBases{e: e, id: e.Identity(), a: mk("acme"), b: mk("bravo"), c: mk("empty")}
}

// fill gives a Base the same-looking people: the same names, in different
// roles, with different canary text.
func fill(t *testing.T, tb twoBases, sc store.Scope, canary, role string) {
	t.Helper()
	for _, name := range []string{"alice", "bob"} {
		a := &store.LocalAccount{Username: name, DisplayName: name + " " + canary, Email: name + "@" + canary + ".test", Role: role, CreatedAt: 1, PasswordHash: "hash-" + canary}
		if err := tb.id.CreateLocalAccount(sc, a, nil); err != nil {
			t.Fatalf("%s in %s: %v", name, canary, err)
		}
		if err := tb.id.ReplaceRecoveryCodes(sc, name, []string{"rc-" + canary + "-" + name}); err != nil {
			t.Fatal(err)
		}
		if err := tb.id.SignIn(sc, name, []string{"g-" + canary}, store.Profile{Name: name + " " + canary, Email: canary + "@x.test"}, "sess-"+canary+"-"+name, 10, 1<<40, 0); err != nil {
			t.Fatal(err)
		}
		tok := store.APIToken{ID: "tok-" + canary + "-" + name, Name: "ci", User: name, CreatedAt: 1, ExpiresAt: 1 << 40}
		if err := tb.id.CreateToken(sc, tok, "th-"+canary+"-"+name); err != nil {
			t.Fatal(err)
		}
		if err := tb.id.CreateLink(sc, store.Link{Hash: "lh-" + canary + "-" + name, Username: name, Kind: "invite", CreatedAt: 1, ExpiresAt: 1 << 40}); err != nil {
			t.Fatal(err)
		}
	}
}

func sameError(t *testing.T, what string, got, control error) {
	t.Helper()
	if !errors.Is(got, store.ErrNotFound) || !errors.Is(control, store.ErrNotFound) {
		t.Errorf("%s: want ErrNotFound both from the other Base and from an empty one; got %v and %v", what, got, control)
		return
	}
	if got.Error() != control.Error() {
		t.Errorf("%s: another Base's thing answers %q but a thing that is nowhere answers %q", what, got, control)
	}
}

// The first slice of the isolation suite: A asks for B's people, sessions,
// tokens, links and memberships by name, by hash and by id, and learns nothing
// that a Base with nobody in it would not tell it; nothing A does changes B.
func TestIdentityIsolationBetweenBases(t *testing.T) {
	e := openMulti(t, testdb.New(t), newClock())
	tb := newTwoBases(t, e)
	ctx := context.Background()
	fill(t, tb, tb.a, "AAA", "admin")
	fill(t, tb, tb.b, "BBB", "viewer")

	t.Run("the same name in two Bases is two people", func(t *testing.T) {
		a, err := tb.id.LocalAccountByName(tb.a, "alice")
		if err != nil || a.Role != "admin" || !strings.Contains(a.DisplayName, "AAA") {
			t.Fatalf("A's alice: %+v %v", a, err)
		}
		b, err := tb.id.LocalAccountByName(tb.b, "alice")
		if err != nil || b.Role != "viewer" || !strings.Contains(b.DisplayName, "BBB") {
			t.Fatalf("B's alice: %+v %v", b, err)
		}
		// Creating the name again in the same Base clashes; in another it does not,
		// so a name taken elsewhere cannot be discovered by trying to take it.
		dup := &store.LocalAccount{Username: "alice", Role: "viewer", CreatedAt: 2}
		if err := tb.id.CreateLocalAccount(tb.a, dup, nil); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("a name taken in A is taken in A: %v", err)
		}
		scratch, err := e.Bases().CreateBase(ctx, store.NewBase{Handle: "scratch", Now: 1})
		if err != nil {
			t.Fatal(err)
		}
		sc, _ := e.Bases().ServiceScope(ctx, scratch.ID)
		if err := tb.id.CreateLocalAccount(sc, dup, nil); err != nil {
			t.Fatalf("a name taken in A is free in another Base: %v", err)
		}
	})

	t.Run("lists hold only the Base's own", func(t *testing.T) {
		accts, _ := tb.id.ListLocalAccounts(tb.a)
		for _, a := range accts {
			if strings.Contains(a.DisplayName+a.Email+a.PasswordHash, "BBB") {
				t.Errorf("B's text in A's accounts: %+v", a)
			}
		}
		if len(accts) != 2 {
			t.Errorf("A has 2 accounts, listed %d", len(accts))
		}
		toks, _ := tb.id.Tokens(tb.a, "")
		if len(toks) != 2 {
			t.Errorf("A has 2 tokens, listed %d", len(toks))
		}
		for _, tk := range toks {
			if strings.Contains(tk.ID, "BBB") {
				t.Errorf("B's token in A's list: %+v", tk)
			}
		}
		mem, _ := tb.id.Members(tb.a)
		for _, m := range mem {
			if m.Role == "viewer" {
				t.Errorf("B's member in A's list: %+v", m)
			}
		}
		if n, _ := tb.id.CountLocalAccounts(tb.a); n != 2 {
			t.Errorf("A counts %d accounts", n)
		}
		if n, _ := tb.id.CountLocalAdmins(tb.b); n != 0 {
			t.Errorf("B has no administrators; A's leaked into the count: %d", n)
		}
	})

	t.Run("B's sessions, tokens, links and codes are not found from A, and say what an empty Base says", func(t *testing.T) {
		_, errB := tb.id.SessionByHash(tb.a, "sess-BBB-alice")
		_, errC := tb.id.SessionByHash(tb.c, "sess-BBB-alice")
		sameError(t, "session by hash", errB, errC)

		_, _, errB = tb.id.TokenByHash(tb.a, "th-BBB-alice", "v2:th-BBB-alice")
		_, _, errC = tb.id.TokenByHash(tb.c, "th-BBB-alice", "v2:th-BBB-alice")
		sameError(t, "token by hash", errB, errC)

		_, errB = tb.id.RevokeAnyToken(tb.a, "tok-BBB-alice")
		_, errC = tb.id.RevokeAnyToken(tb.c, "tok-BBB-alice")
		sameError(t, "revoke any token", errB, errC)
		_, errB = tb.id.RevokeToken(tb.a, "alice", "tok-BBB-alice")
		_, errC = tb.id.RevokeToken(tb.c, "alice", "tok-BBB-alice")
		sameError(t, "revoke token", errB, errC)

		_, errB = tb.id.LinkByHash(tb.a, "lh-BBB-alice", 20)
		_, errC = tb.id.LinkByHash(tb.c, "lh-BBB-alice", 20)
		sameError(t, "link by hash", errB, errC)
		if used, err := tb.id.UseLink(tb.a, "lh-BBB-alice", 20); err != nil || used {
			t.Errorf("A used B's link: %v %v", used, err)
		}

		if hs, _ := tb.id.RecoveryCodeHashes(tb.a, "alice"); len(hs) != 1 || strings.Contains(hs[0], "BBB") {
			t.Errorf("A's alice sees recovery codes %v", hs)
		}
		if ok, _ := tb.id.SpendRecoveryCode(tb.a, "alice", "rc-BBB-alice", 5); ok {
			t.Error("A spent B's recovery code")
		}
		_, _, errB = tb.id.LastSignIn(tb.c, "alice") // control: nobody in C
		_, _, errA := tb.id.LastSignIn(tb.a, "nobody")
		if errB == nil || errA == nil {
			t.Error("a person who never signed in is found")
		}

		_, errB = tb.id.MemberByHandle(tb.c, "alice")
		_, errC = tb.id.MemberByHandle(tb.a, "zed")
		if errB.Error() != strings.Replace(errC.Error(), "zed", "alice", 1) {
			t.Errorf("member lookups differ: %q %q", errB, errC)
		}
	})

	t.Run("what A does to a name does not touch B's of the same name", func(t *testing.T) {
		if err := tb.id.SetRole(tb.a, "alice", "viewer"); err != nil {
			t.Fatal(err)
		}
		if b, _ := tb.id.LocalAccountByName(tb.b, "alice"); b.Role != "viewer" {
			t.Fatalf("setup: B's alice is %q", b.Role)
		}
		if err := tb.id.SetRole(tb.a, "alice", "admin"); err != nil {
			t.Fatal(err)
		}
		if err := tb.id.SetDisabled(tb.a, "bob", true); err != nil {
			t.Fatal(err)
		}
		if b, _ := tb.id.LocalAccountByName(tb.b, "bob"); b.Disabled {
			t.Error("disabling A's bob disabled B's bob")
		}
		if n, _ := tb.id.CountSessions(tb.b, "bob"); n != 1 {
			t.Errorf("disabling A's bob ended B's bob's session: %d left", n)
		}
		if n, _ := tb.id.EndUserSessions(tb.a, "alice"); n != 1 {
			t.Errorf("A ended %d sessions of its own alice", n)
		}
		if n, _ := tb.id.CountSessions(tb.b, "alice"); n != 1 {
			t.Errorf("A signing alice out ended B's alice's session: %d left", n)
		}
		if err := tb.id.DeleteLocalAccount(tb.a, "bob"); err != nil {
			t.Fatal(err)
		}
		if _, err := tb.id.LocalAccountByName(tb.b, "bob"); err != nil {
			t.Errorf("deleting A's bob deleted B's: %v", err)
		}
		if m, err := tb.id.MemberByHandle(tb.b, "bob"); err != nil || m.Role != "viewer" {
			t.Errorf("deleting A's bob changed B's membership: %+v %v", m, err)
		}
		if hs, _ := tb.id.RecoveryCodeHashes(tb.b, "bob"); len(hs) != 1 {
			t.Errorf("deleting A's bob deleted B's recovery codes: %v", hs)
		}
		if err := tb.id.SetPassword(tb.a, "alice", "new", 30, ""); err != nil {
			t.Fatal(err)
		}
		if b, _ := tb.id.LocalAccountByName(tb.b, "alice"); b.PasswordHash != "hash-BBB" {
			t.Errorf("changing A's password changed B's: %q", b.PasswordHash)
		}
		if n, _ := tb.id.CountSessions(tb.b, "alice"); n != 1 {
			t.Errorf("changing A's password ended B's session")
		}
	})

	t.Run("a person who belongs to two Bases has one account and a handle and role in each", func(t *testing.T) {
		acct, err := tb.id.AddMember(tb.a, "pat", "admin", "", 5)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tb.id.AddMember(tb.b, "patricia", "viewer", acct, 6); err != nil {
			t.Fatal(err)
		}
		ma, _ := tb.id.MemberByAccount(tb.a, acct)
		mb, _ := tb.id.MemberByAccount(tb.b, acct)
		if ma == nil || mb == nil || ma.Handle != "pat" || ma.Role != "admin" || mb.Handle != "patricia" || mb.Role != "viewer" {
			t.Fatalf("memberships: %+v %+v", ma, mb)
		}
		if _, err := tb.id.MemberByAccount(tb.c, acct); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an account is not a member of a Base it did not join: %v", err)
		}
		if _, err := tb.id.AddMember(tb.a, "pat", "viewer", "", 7); !errors.Is(err, store.ErrConflict) {
			t.Errorf("a handle is unique in its own Base: %v", err)
		}
		if _, err := tb.id.AddMember(tb.c, "pat", "viewer", "", 7); err != nil { // a Base whose "pat" is nobody else's
			t.Errorf("a handle taken in another Base is free here: %v", err)
		}
	})

	t.Run("a token or session of one Base is not accepted by the other even though both are real", func(t *testing.T) {
		if _, _, err := tb.id.TokenByHash(tb.b, "th-BBB-alice", "v2:th-BBB-alice"); err != nil {
			t.Fatalf("B's own token: %v", err)
		}
		if s, err := tb.id.SessionByHash(tb.b, "sess-BBB-alice"); err != nil || s.User != "alice" {
			t.Fatalf("B's own session: %v %v", s, err)
		}
		if _, err := tb.id.SessionByHash(tb.a, "sess-BBB-alice"); err == nil {
			t.Fatal("B's session was accepted in A")
		}
	})
}

// A router that cannot tell which Bases exist: an unknown host, a suspended
// Base and a label nobody has all answer the same; a label that is reserved,
// malformed or taken is refused with the same words.
func TestBaseNamesAreNotAnOracle(t *testing.T) {
	e := openMulti(t, testdb.New(t), newClock())
	ctx := context.Background()
	reg := e.Bases()
	b, err := reg.CreateBase(ctx, store.NewBase{Handle: "acme", Host: "acme.example.test", Now: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.CreateBase(ctx, store.NewBase{Handle: "gone", Host: "gone.example.test", Now: 1}); err != nil {
		t.Fatal(err)
	}
	gone, _ := reg.ScopeForHost(ctx, "gone.example.test")
	if err := reg.SetStatus(ctx, gone.ID(), "suspended"); err != nil {
		t.Fatal(err)
	}
	_, errUnknown := reg.ScopeForHost(ctx, "nobody.example.test")
	_, errSuspended := reg.ScopeForHost(ctx, "gone.example.test")
	_, errEmpty := reg.ScopeForHost(ctx, "")
	_, errHandle := reg.ScopeForHandle(ctx, "nobody")
	for _, err := range []error{errUnknown, errSuspended, errEmpty, errHandle} {
		if !errors.Is(err, store.ErrNotFound) || err.Error() != errUnknown.Error() {
			t.Errorf("a host that is not served answers %q, want the same for all: %q", err, errUnknown)
		}
	}
	if sc, err := reg.ScopeForHost(ctx, "ACME.example.test"); err != nil || sc.ID() != b.ID {
		t.Errorf("a host resolves to its Base, ignoring case: %v %v", sc, err)
	}
	var texts []string
	for _, h := range []string{"acme", "www", "default", "A", "has space", "vendor"} {
		_, err := reg.CreateBase(ctx, store.NewBase{Handle: h, Now: 2})
		if !errors.Is(err, store.ErrConflict) {
			t.Errorf("label %q: %v", h, err)
			continue
		}
		texts = append(texts, err.Error())
	}
	for _, s := range texts {
		if s != texts[0] {
			t.Errorf("taken, reserved and malformed labels answer differently: %q and %q", texts[0], s)
		}
	}
	if err := reg.SetStatus(ctx, store.DefaultBaseID, "suspended"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("the default Base cannot be suspended: %v", err)
	}
	// ForEachBase visits active Bases only, each with its own scope, and one
	// failing Base does not stop the others.
	var seen []store.BaseID
	err = reg.ForEachBase(ctx, func(sc store.Scope) error {
		seen = append(seen, sc.ID())
		if sc.ID() == b.ID {
			return errors.New("boom")
		}
		return nil
	})
	if err == nil || len(seen) != 2 {
		t.Errorf("ForEachBase: %v visited %v (the default and acme; gone is suspended)", err, seen)
	}
}

// An installation with data at schema 39 upgrades keeping everyone, their
// roles and their sessions and tokens in the default Base; the build of
// schema 39 still runs against it (nothing was taken away); turning several
// Bases on afterwards keeps it all and is a step that cannot be repeated by
// accident.
func TestUpgradeFromSchema39KeepsIdentityInTheDefaultBase(t *testing.T) {
	addr, clk := testdb.New(t), newClock()
	old := mustOpenAs(t, addr, clk, schemaArchive, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO local_accounts(username, role, password_hash, created_at, disabled) VALUES('root','admin','h1',1,0)`)
	exec(`INSERT INTO local_accounts(username, role, password_hash, created_at, disabled) VALUES('ops','operator','h2',2,1)`)
	exec(`INSERT INTO local_recovery_codes(username, code_hash) VALUES('root','rc1')`)
	exec(`INSERT INTO local_links(token_hash, username, kind, created_at, expires_at) VALUES('lh','ops','invite',1,99999999999999)`)
	exec(`INSERT INTO users(username, group_list, last_login, display_name, email) VALUES('root','[]',5,'Root','r@x.test')`)
	exec(`INSERT INTO users(username, group_list, last_login, display_name, email) VALUES('oidc-ann','["eng"]',6,'Ann','a@x.test')`)
	exec(`INSERT INTO sessions(id_hash, username, created_at, last_seen, expires_at) VALUES('sh','root',1,1,99999999999999)`)
	exec(`INSERT INTO api_tokens(id, hash, username, name, created_at, expires_at) VALUES('tk1','th','root','ci',1,99999999999999)`)
	exec(`INSERT INTO api_tokens(id, hash, username, name, created_at, expires_at, role, projects) VALUES('tk2','v2:th2','root','lim',1,99999999999999,'viewer','["shop"]')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	ctx := context.Background()
	if !strings.Contains(log.String(), `applied a database migration" schema=52 compatibility=additive minimum=7`) {
		t.Errorf("migration 52 is additive and leaves the minimum at 7:\n%s", log.String())
	}
	check := func(e *Engine, when string) {
		t.Helper()
		sc := e.Bases().DefaultScope()
		if sc.ID() != store.DefaultBaseID {
			t.Fatalf("%s: default scope %s", when, sc.ID())
		}
		a, err := e.LocalAccountByName("root")
		if err != nil || a.Role != "admin" || a.PasswordHash != "h1" || a.Disabled {
			t.Errorf("%s: root: %+v %v", when, a, err)
		}
		if o, err := e.LocalAccountByName("ops"); err != nil || o.Role != "operator" || !o.Disabled {
			t.Errorf("%s: ops: %+v %v", when, o, err)
		}
		if s, err := e.SessionByHash("sh"); err != nil || s.User != "root" || s.Profile.Name != "Root" {
			t.Errorf("%s: session: %+v %v", when, s, err)
		}
		if tk, groups, err := e.LookupToken("th"); err != nil || tk.User != "root" || len(groups) != 0 {
			t.Errorf("%s: token: %+v %v %v", when, tk, groups, err)
		}
		if tk, _, err := e.LookupToken("th2"); err != nil || tk.Role != "viewer" || len(tk.Projects) != 1 || tk.Projects[0] != "shop" {
			t.Errorf("%s: limited token: %+v %v", when, tk, err)
		}
		if l, err := e.LinkByHash("lh", 10); err != nil || l.Username != "ops" {
			t.Errorf("%s: link: %+v %v", when, l, err)
		}
		if hs, _ := e.RecoveryCodeHashes("root"); len(hs) != 1 {
			t.Errorf("%s: recovery codes: %v", when, hs)
		}
		if g, _, err := e.LastSignIn("oidc-ann"); err != nil || len(g) != 1 || g[0] != "eng" {
			t.Errorf("%s: oidc user: %v %v", when, g, err)
		}
		mem, err := e.Identity().Members(sc)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, m := range mem {
			got[m.Handle] = m.Role + "/" + m.State
		}
		want := map[string]string{"root": "admin/active", "ops": "operator/disabled", "oidc-ann": "/active"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: memberships %v, want %v", when, got, want)
		}
	}
	check(cur, "after the upgrade")
	if n := scalar(t, cur, `SELECT count(*) FROM accounts`); n != 3 {
		t.Errorf("accounts: %d, want one per person (3)", n)
	}
	if n := scalar(t, cur, `SELECT count(*) FROM users WHERE base_id <> 'bs_default'`); n != 0 {
		t.Errorf("%d users outside the default Base", n)
	}
	cur.Close()

	// The previous build still runs against the upgraded database and reads the old data.
	older := mustOpenAs(t, addr, clk, schemaArchive, &syncBuffer{})
	if a, err := older.LocalAccountByName("root"); err != nil || a.Role != "admin" {
		t.Errorf("the build of schema 39 against schema 52: %+v %v", a, err)
	}
	older.Close()

	multi := openMulti(t, addr, clk)
	check(multi, "after turning several Bases on")
	if info, err := multi.Schema(ctx); err != nil || info.Minimum != schemaWall {
		t.Errorf("turning several Bases on raises the lowest schema a build may run against to %d: %+v %v", schemaWall, info, err)
	}
	if _, err := openAs(t, addr, clk, schemaArchive, &syncBuffer{}); err == nil {
		t.Error("the build of schema 39 started against a database that has several Bases turned on")
	}
}

func scalar(t *testing.T, e *Engine, q string) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// ---- PostgreSQL: the second lock ----

// appRole creates a database role that is not the owner and has no BYPASSRLS,
// and returns a connection to the test schema as that role.
func appRole(t *testing.T, e *Engine, addr string) *sql.DB {
	t.Helper()
	u, err := url.Parse(addr)
	if err != nil {
		t.Fatal(err)
	}
	schema := u.Query().Get("search_path")
	var rnd string
	if err := e.db.QueryRow(`SELECT substr(md5(random()::text), 1, 8)`).Scan(&rnd); err != nil {
		t.Fatal(err)
	}
	role := "lw_app_" + rnd
	for _, stmt := range []string{
		`CREATE ROLE ` + role + ` LOGIN PASSWORD 'wall' NOSUPERUSER NOBYPASSRLS`,
		`GRANT USAGE ON SCHEMA ` + schema + ` TO ` + role,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ` + schema + ` TO ` + role,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Skipf("the test database role cannot create roles (%v); the second-lock tests need it", err)
		}
	}
	t.Cleanup(func() {
		_, _ = e.db.Exec(`DROP OWNED BY ` + role)
		_, _ = e.db.Exec(`DROP ROLE IF EXISTS ` + role)
	})
	u.User = url.UserPassword(role, "wall")
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// The second lock catches a statement that forgot its scope: with a role that
// the policies bind, an unscoped query sees only its own Base's rows inside a
// scoped transaction and nothing outside one, and a write into another Base
// is refused. The mutation test in miniature: remove the predicate, the data
// still does not leak.
func TestRowLevelSecurityCatchesAnUnscopedQuery(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("row-level security is PostgreSQL's: set " + testdb.EnvVar)
	}
	addr := testdb.New(t)
	e := openMulti(t, addr, newClock())
	tb := newTwoBases(t, e)
	ctx := context.Background()
	fill(t, tb, tb.a, "AAA", "admin")
	fill(t, tb, tb.b, "BBB", "viewer")

	state, err := e.StoreEnv().RLSState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range store.Names(store.Scoped) {
		if !state[name] {
			t.Errorf("table %s has no forced row-level security with the restrictive policy", name)
		}
	}
	for _, name := range append(store.Names(store.Global), store.Names(store.Pending)...) {
		if state[name] {
			t.Errorf("table %s is not a Base table but carries the wall: it would hide its rows from the application", name)
		}
	}
	if role, _ := e.StoreEnv().CheckRole(ctx); role.Restricted {
		t.Errorf("the test connects as the owner; it must not count as restricted: %+v", role)
	}

	app := appRole(t, e, addr)
	env := &store.Env{DB: store.SQLConn{DB: app, PG: true}, PG: true}
	if role, err := env.CheckRole(ctx); err != nil || !role.Restricted {
		t.Fatalf("the application role is not bound by the wall: %+v %v", role, err)
	}
	id := store.NewIdentity(env)

	// The ordinary store still works as the restricted role.
	if a, err := id.LocalAccountByName(tb.a, "alice"); err != nil || a.Role != "admin" {
		t.Fatalf("the store as the application role: %+v %v", a, err)
	}
	if _, err := id.LocalAccountByName(tb.a, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}

	count := func(q store.Querier, query string) (n int) {
		t.Helper()
		if err := q.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	// 1. A statement with no base_id predicate, inside Base A's transaction.
	for _, table := range store.Names(store.Scoped) {
		err := env.Do(tb.a, false, func(q *store.Q) error {
			own := count(q, `SELECT count(*) FROM `+table+` WHERE base_id = '`+string(tb.a.ID())+`'`)
			all := count(q, `SELECT count(*) FROM `+table)
			if own != all {
				t.Errorf("%s: an unscoped count inside A's transaction is %d but A has %d: another Base's rows are visible", table, all, own)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// 2. The same with no scope at all: nothing.
	for _, table := range store.Names(store.Scoped) {
		if n := count(env.DB, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s: %d rows are visible to a connection that set no Base", table, n)
		}
	}
	// 3. A write for another Base, from inside A's transaction, is refused, and
	//    so is one that would move a row away.
	err = env.Do(tb.a, true, func(q *store.Q) error {
		_, err := q.Exec(`INSERT INTO sessions(base_id, id_hash, username, created_at, last_seen, expires_at) VALUES(?,?,?,?,?,?)`,
			string(tb.b.ID()), "planted", "alice", 1, 1, 1)
		return err
	})
	if err == nil {
		t.Error("a session could be written into another Base from inside A's transaction")
	}
	err = env.Do(tb.a, true, func(q *store.Q) error {
		_, err := q.Exec(`UPDATE users SET base_id = ? WHERE username = 'alice'`, string(tb.b.ID()))
		return err
	})
	if err == nil {
		t.Error("a user could be moved into another Base from inside A's transaction")
	}
	// An unscoped delete inside A's transaction touches only A.
	if err := env.Do(tb.a, true, func(q *store.Q) error {
		_, err := q.Exec(`DELETE FROM sessions`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n, _ := id.CountSessions(tb.b, "alice"); n != 1 {
		t.Errorf("an unscoped DELETE in A's transaction removed B's session")
	}
	if n, _ := id.CountSessions(tb.a, "alice"); n != 0 {
		t.Errorf("the unscoped DELETE should have removed A's own sessions: %d", n)
	}

	// 4. Pooling: one connection serves A, then B, then nobody; nothing carries over.
	app.SetMaxOpenConns(1)
	for _, sc := range []store.Scope{tb.a, tb.b, tb.a} {
		if err := env.Do(sc, false, func(q *store.Q) error {
			var got string
			if err := q.QueryRow(`SELECT current_setting('app.base_id', true)`).Scan(&got); err != nil {
				return err
			}
			if got != string(sc.ID()) {
				t.Errorf("the transaction for %s sees app.base_id = %q", sc.ID(), got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var left sql.NullString
	if err := app.QueryRow(`SELECT current_setting('app.base_id', true)`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left.String != "" {
		t.Errorf("the connection kept app.base_id = %q after the transaction", left.String)
	}
	// The role cannot widen its own view.
	if _, err := app.Exec(`SET ROLE launchway`); err == nil {
		t.Error("the application role could SET ROLE")
	}
	if _, err := app.Exec(`ALTER TABLE users DISABLE ROW LEVEL SECURITY`); err == nil {
		t.Error("the application role could switch the wall off")
	}
}
