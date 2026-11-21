package togglestore

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/toggles"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func openServer(t *testing.T, addr string, clk *clock) *Service {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: addr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return New(Deps{DB: eng.Database(), Now: clk.now, CheckEvery: time.Millisecond,
		UnitOf: func(p string) string {
			if p == "cart" || p == "ledger" {
				return "pay"
			}
			return ""
		},
		Environment: func(n string) Env {
			if n == "production" {
				return Env{Known: true, Protected: true}
			}
			return Env{Known: true}
		}})
}

func newService(t *testing.T) (*Service, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	return openServer(t, testdb.New(t), clk), clk
}

var ada = Actor{User: "ada", Via: "api"}
var bob = Actor{User: "bob", Via: "api"}

func boolDef(project, key string) DefinitionInput {
	return DefinitionInput{Key: Key{Where: ProjectWhere(project), Key: key}, Type: toggles.Boolean, Default: false, Owner: "team-pay"}
}

func onFor(v any) toggles.State {
	return toggles.State{On: true, OffValue: false, Fallthrough: toggles.Serve{Value: v}}
}

func mustCreate(t *testing.T, s *Service, project, key string) Key {
	t.Helper()
	d, err := s.Create(context.Background(), ada, boolDef(project, key), "new")
	if err != nil {
		t.Fatal(err)
	}
	return d.Key
}

func TestReasonIsRequiredAndAnApprovalIsKeptForProtectedEnvironments(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	k := mustCreate(t, s, "cart", "checkout-v2")
	// dev: a reason is needed, an approval is not.
	if _, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "dev", State: onFor(true)}); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("no reason: %v", err)
	}
	out, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "dev", State: onFor(true), Reason: "try it"})
	if err != nil || out.Waiting || out.Change.Status != StatusApplied {
		t.Fatalf("%+v %v", out, err)
	}
	// production: waits.
	out, err = s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "production", State: onFor(true), Reason: "ship it"})
	if err != nil || !out.Waiting || out.Change.Status != StatusPending {
		t.Fatalf("%+v %v", out, err)
	}
	rs, _ := s.Build(ctx, "cart", "production")
	if toggles.Evaluate(rs, "checkout-v2", nil).Value != false {
		t.Fatal("a change waiting for approval took effect")
	}
	// The one who asked may not approve; someone else may; twice is refused.
	if _, err := s.Approve(ctx, ada, out.Change.ID, ""); !errors.Is(err, engine.ErrForbidden) {
		t.Fatalf("self approval: %v", err)
	}
	done, err := s.Approve(ctx, bob, out.Change.ID, "looks right")
	if err != nil || done.Waiting || done.Change.Status != StatusApplied || done.Change.Actor != "ada" {
		t.Fatalf("%+v %v", done, err)
	}
	rs, _ = s.Build(ctx, "cart", "production")
	if toggles.Evaluate(rs, "checkout-v2", nil).Value != true {
		t.Fatal("the approved change did not take effect")
	}
	if _, err := s.Approve(ctx, Actor{User: "cy"}, out.Change.ID, ""); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("approving a change already made: %v", err)
	}
}

func TestApprovalCountsRejectAndWithdraw(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	k := mustCreate(t, s, "cart", "a")
	two := 2
	if _, err := s.SetSafeguards(ctx, ada, ProjectWhere("cart"), "production", toggles.Setting{Approvals: &two}, ""); err != nil {
		t.Fatal(err)
	}
	out, _ := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "production", State: onFor(true), Reason: "x"})
	if o, err := s.Approve(ctx, bob, out.Change.ID, ""); err != nil || !o.Waiting {
		t.Fatalf("one of two approvals: %+v %v", o, err)
	}
	if _, err := s.Approve(ctx, bob, out.Change.ID, ""); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("the same person twice: %v", err)
	}
	if o, err := s.Approve(ctx, Actor{User: "cy"}, out.Change.ID, ""); err != nil || o.Waiting {
		t.Fatalf("two of two: %+v %v", o, err)
	}
	// Reject and withdraw.
	o2, _ := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "production", State: onFor(false), Reason: "y"})
	if ch, err := s.Reject(ctx, bob, o2.Change.ID, "no"); err != nil || ch.Status != StatusRejected {
		t.Fatalf("%+v %v", ch, err)
	}
	o3, _ := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "production", State: onFor(false), Reason: "z"})
	if _, err := s.Withdraw(ctx, bob, o3.Change.ID); !errors.Is(err, engine.ErrForbidden) {
		t.Fatalf("withdrawn by someone else: %v", err)
	}
	if ch, err := s.Withdraw(ctx, ada, o3.Change.ID); err != nil || ch.Status != StatusWithdrawn {
		t.Fatalf("%+v %v", ch, err)
	}
}

func TestKillSwitchRetreatAndRevertAreNeverHeld(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	in := boolDef("cart", "payments-kill")
	in.Kind, in.Default, in.SafeValue = toggles.KindKill, true, false
	d, err := s.Create(ctx, ada, in, "kill switch")
	if err != nil {
		t.Fatal(err)
	}
	// Turning it on in production needs approval; turning to the safe value does not.
	on, _ := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: d.Key, Environment: "production", State: onFor(true), Reason: "normal"})
	if !on.Waiting {
		t.Fatalf("turning on was not held: %+v", on)
	}
	if _, err := s.Approve(ctx, bob, on.Change.ID, ""); err != nil {
		t.Fatal(err)
	}
	kill, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: d.Key, Environment: "production", State: onFor(false), Reason: "provider down"})
	if err != nil || kill.Waiting {
		t.Fatalf("the kill was held: %+v %v", kill, err)
	}
	// A revert is a retreat: it needs a reason, never an approval.
	back, err := s.Revert(ctx, ada, kill.Change.ID, "the provider is back")
	if err != nil || back.Waiting || back.Change.Reverts != kill.Change.ID {
		t.Fatalf("%+v %v", back, err)
	}
	if _, err := s.Revert(ctx, ada, kill.Change.ID, "again"); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("reverting twice: %v", err)
	}
	if _, err := s.Revert(ctx, ada, back.Change.ID, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("revert with no reason: %v", err)
	}
	rs, _ := s.Build(ctx, "cart", "production")
	if toggles.Evaluate(rs, "payments-kill", nil).Value != true {
		t.Fatal("the revert did not restore the state")
	}
}

func TestHistoryRevertOfAnOlderChangeSaysWhatElseItUndoes(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	k := mustCreate(t, s, "cart", "a")
	var ids []int64
	for _, v := range []any{true, false, true} {
		o, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "dev", State: onFor(v), Reason: "step"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.Change.ID)
		clk.add(time.Minute)
	}
	h, err := s.History(ctx, k, "dev", 10)
	if err != nil || len(h) != 3 || h[0].ID != ids[2] {
		t.Fatalf("%+v %v", h, err)
	}
	o, err := s.Revert(ctx, bob, ids[1], "undo the middle one")
	if err != nil || o.AlsoUndone != 1 {
		t.Fatalf("%+v %v", o, err)
	}
	rs, _ := s.Build(ctx, "cart", "dev")
	if toggles.Evaluate(rs, "a", nil).Value != true { // the middle change went from true to false: back to true
		t.Fatal("state after reverting the middle change")
	}
}

func TestSafeguardsRelaxedWithAReasonAndLockedFromAbove(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	off := false
	// Relaxing needs a reason.
	if _, err := s.SetSafeguards(ctx, ada, ProjectWhere("cart"), "production", toggles.Setting{RequireApproval: &off}, ""); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("relaxing with no reason: %v", err)
	}
	r, err := s.SetSafeguards(ctx, ada, ProjectWhere("cart"), "production", toggles.Setting{RequireApproval: &off}, "a small team: reviews happen in the pull request")
	if err != nil || r.RequireApproval || r.From[toggles.GuardApproval] != "project cart, production" {
		t.Fatalf("%+v %v", r, err)
	}
	k := mustCreate(t, s, "cart", "a")
	if o, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "production", State: onFor(true), Reason: "go"}); err != nil || o.Waiting {
		t.Fatalf("with approval off the change is made: %+v %v", o, err)
	}
	// Another project is untouched and still holds.
	k2 := mustCreate(t, s, "web", "a")
	if o, _ := s.ChangeState(ctx, ada, StateChange{Project: "web", Toggle: k2, Environment: "production", State: onFor(true), Reason: "go"}); !o.Waiting {
		t.Fatal("a setting of one project relaxed another")
	}
	// The organization locks it on: the project's setting no longer counts, and a new one is refused.
	on := true
	if _, err := s.SetSafeguards(ctx, ada, Where{Level: LevelOrganization}, "", toggles.Setting{RequireApproval: &on, Locked: []string{toggles.GuardApproval}}, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Safeguards(ctx, ProjectWhere("cart"), "production"); !got.RequireApproval || got.LockedBy[toggles.GuardApproval] != "organization" {
		t.Fatalf("%+v", got)
	}
	if _, err := s.SetSafeguards(ctx, ada, ProjectWhere("cart"), "dev", toggles.Setting{RequireApproval: &off}, "no"); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("setting a locked safeguard: %v", err)
	}
	h, _ := s.History(ctx, Key{Where: ProjectWhere("cart")}, "", 5)
	_ = h
}

func TestSharedToggleAtAHigherLevelAndANearerOverrideAndALock(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	org := Key{Where: Where{Level: LevelOrganization}, Key: "dark-mode"}
	if _, err := s.Create(ctx, ada, DefinitionInput{Key: org, Type: toggles.Boolean, Default: false}, "shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeState(ctx, ada, StateChange{Toggle: org, Environment: "dev", State: onFor(true), Reason: "everyone"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"cart", "web"} {
		rs, err := s.Build(ctx, p, "dev")
		if err != nil || toggles.Evaluate(rs, "dark-mode", nil).Value != true {
			t.Fatalf("%s: %v", p, err)
		}
	}
	// web overrides it nearer; cart keeps the shared one.
	if _, err := s.Create(ctx, ada, boolDef("web", "dark-mode"), "web differs"); err != nil {
		t.Fatal(err)
	}
	rs, _ := s.Build(ctx, "web", "dev")
	if toggles.Evaluate(rs, "dark-mode", nil).Value != false {
		t.Fatal("the nearer definition did not win")
	}
	// A lock at the org stops a new override.
	if err := s.SetLock(ctx, ada, org, true, "a fleet rule"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, ada, boolDef("ledger", "dark-mode"), "no"); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("defining a locked key nearer: %v", err)
	}
	rs, _ = s.Build(ctx, "web", "dev")
	if toggles.Evaluate(rs, "dark-mode", nil).Value != true {
		t.Fatal("a lock above did not hide the override")
	}
	list, _ := s.List(ctx, "ledger", []string{"dev"})
	if len(list) != 1 || !list[0].Inherited {
		t.Fatalf("%+v", list)
	}
}

func TestSnapshotsAreInMemoryAndAnotherServersChangeIsSeen(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	a, b := openServer(t, addr, clk), openServer(t, addr, clk)
	k := mustCreate(t, a, "cart", "checkout-v2")
	s1, err := b.Current(ctx, "cart", "dev")
	if err != nil {
		t.Fatal(err)
	}
	s1b, _ := b.Current(ctx, "cart", "dev")
	if s1 != s1b {
		t.Fatal("a second request rebuilt the snapshot")
	}
	changed := b.Changed()
	if _, err := a.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "dev", State: onFor(true), Reason: "go"}); err != nil {
		t.Fatal(err)
	}
	clk.add(10 * time.Millisecond) // past CheckEvery: server b looks at the revision
	s2, err := b.Current(ctx, "cart", "dev")
	if err != nil || s2.ETag == s1.ETag || toggles.Evaluate(s2.Ruleset, "checkout-v2", nil).Value != true {
		t.Fatalf("server b did not see server a's change: %+v %v", s2, err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("a stream waiting on server b was not woken")
	}
	// A change that does not touch an environment's rules leaves its ETag alone.
	other := mustCreate(t, a, "web", "other")
	_ = other
	clk.add(10 * time.Millisecond)
	s3, _ := b.Current(ctx, "cart", "dev")
	if s3.ETag != s2.ETag {
		t.Fatal("an unrelated project's toggle changed this project's ETag")
	}
}

func TestKeysAreScopedHashedRotatedAndRevoked(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	full, info, err := s.NewKey(ctx, ada, "cart", "production", KindServer, "api", nil)
	if err != nil || !strings.HasPrefix(full, "lwt_srv_") || info.Environment != "production" {
		t.Fatalf("%q %+v %v", full, info, err)
	}
	var stored string
	if err := s.d.DB.QueryRow(ctx, `SELECT secret_hash FROM toggle_keys WHERE id = ?`, info.ID).Scan(&stored); err != nil || stored == "" || strings.Contains(full, stored) || strings.Contains(stored, full[len(full)-20:]) {
		t.Fatalf("the secret is stored as a hash: %q %v", stored, err)
	}
	got, err := s.Authenticate(ctx, full)
	if err != nil || got.Project != "cart" || got.Environment != "production" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"", "lwt_srv_", full[:len(full)-1] + "x", strings.Replace(full, info.ID, "000000000000", 1), "lwt_cli_" + info.ID} {
		if _, err := s.Authenticate(ctx, bad); !errors.Is(err, ErrBadKey) {
			t.Errorf("%q was accepted: %v", bad, err)
		}
	}
	// Rotating: both work during the overlap, only the new one after.
	nfull, ninfo, err := s.RotateKey(ctx, ada, "cart", info.ID, time.Hour)
	if err != nil || ninfo.Name != "api" {
		t.Fatal(err)
	}
	for _, k := range []string{full, nfull} {
		if _, err := s.Authenticate(ctx, k); err != nil {
			t.Errorf("during the overlap: %v", err)
		}
	}
	clk.add(2 * time.Hour)
	if _, err := s.Authenticate(ctx, full); !errors.Is(err, ErrBadKey) {
		t.Fatalf("the old key after the overlap: %v", err)
	}
	// Revoking is at once, even for a key that was cached.
	if _, err := s.Authenticate(ctx, nfull); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeKey(ctx, ada, "cart", ninfo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, nfull); !errors.Is(err, ErrBadKey) {
		t.Fatalf("a revoked key: %v", err)
	}
	// Another project cannot touch it.
	if err := s.RevokeKey(ctx, ada, "web", ninfo.ID); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("revoking another project's key: %v", err)
	}
	// A client key is public and bound to origins.
	cli, cinfo, err := s.NewKey(ctx, ada, "cart", "production", KindClient, "web", []string{"https://shop.example.com"})
	if err != nil || cli != cinfo.ID || !strings.HasPrefix(cli, "lwt_cli_") {
		t.Fatalf("%q %+v %v", cli, cinfo, err)
	}
	if _, _, err := s.NewKey(ctx, ada, "cart", "production", KindClient, "web", []string{"shop.example.com/x"}); err == nil {
		t.Fatal("an origin with no scheme was accepted")
	}
}

func TestEvaluationCountsKeepNoOneAndShowWhereATogglelIsUsed(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	k := mustCreate(t, s, "cart", "a")
	if _, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "dev", State: onFor(true), Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Evaluate(ctx, "cart", "dev", toggles.Context{"key": "user-" + string(rune('a'+i)), "email": "x@example.com"}, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := s.Usage(ctx, "cart", "a", 30)
	if err != nil || len(u) != 1 || u[0].Evaluations != 5 || u[0].Variations["true"] != 5 || u[0].LastEvaluated != "2026-11-21" {
		t.Fatalf("%+v %v", u, err)
	}
	// Nothing about who: the table has no column that could hold it, and no stored text mentions the context.
	rows, err := s.d.DB.Query(ctx, `SELECT project, key, environment, variation, reason FROM toggle_evals`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c, d, e string
		_ = rows.Scan(&a, &b, &c, &d, &e)
		if strings.Contains(a+b+c+d+e, "user-") || strings.Contains(a+b+c+d+e, "example.com") {
			t.Fatalf("a context value was stored: %q", a+b+c+d+e)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Flushing adds to what is there.
	s.Count("cart", "dev", "a", true, "fallthrough", 3)
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.Usage(ctx, "cart", "a", 30); u[0].Evaluations != 8 {
		t.Fatalf("%+v", u)
	}
}

func TestScheduledChangesRunOnceAtTheirTime(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	a, b := openServer(t, addr, clk), openServer(t, addr, clk)
	k := mustCreate(t, a, "cart", "a")
	at := clk.now().Add(time.Hour).UnixMilli()
	sc, err := a.ScheduleChange(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "dev", State: onFor(true), Reason: "launch"}, at)
	if err != nil || sc.Status != "pending" {
		t.Fatalf("%+v %v", sc, err)
	}
	if _, err := a.ScheduleChange(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "dev", State: onFor(true), Reason: "x"}, at-2*3600*1000); err == nil {
		t.Fatal("a time in the past was accepted")
	}
	if n, _ := a.RunDue(ctx); n != 0 {
		t.Fatalf("ran early: %d", n)
	}
	clk.add(2 * time.Hour)
	var wg sync.WaitGroup
	var total int
	var mu sync.Mutex
	for _, s := range []*Service{a, b} {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			n, _ := s.RunDue(ctx)
			mu.Lock()
			total += n
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("two servers made the change %d times", total)
	}
	rs, _ := a.Build(ctx, "cart", "dev")
	if toggles.Evaluate(rs, "a", nil).Value != true {
		t.Fatal("the scheduled change was not made")
	}
	h, _ := a.History(ctx, k, "dev", 5)
	if len(h) != 1 || h[0].Via != "schedule" || h[0].Actor != "ada" {
		t.Fatalf("%+v", h)
	}
	list, _ := a.Schedules(ctx, "cart", "", true)
	if len(list) != 1 || list[0].Status != "done" {
		t.Fatalf("%+v", list)
	}
}

func TestToggleChangesSinceAndRevertTogether(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	off := false
	if _, err := s.SetSafeguards(ctx, ada, ProjectWhere("cart"), "production", toggles.Setting{RequireApproval: &off}, "test"); err != nil {
		t.Fatal(err)
	}
	k1, k2 := mustCreate(t, s, "cart", "one"), mustCreate(t, s, "cart", "two")
	good := clk.now().UnixMilli()
	clk.add(time.Minute)
	for _, k := range []Key{k1, k2} {
		if _, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "production", State: onFor(true), Reason: "launch"}); err != nil {
			t.Fatal(err)
		}
		clk.add(time.Second)
	}
	list, err := s.ChangesSince(ctx, "cart", "production", good)
	if err != nil || len(list) != 2 || list[0].Toggle != "two" {
		t.Fatalf("%+v %v", list, err)
	}
	res, err := s.RevertSince(ctx, bob, "cart", "production", good, "rolling back to the last good state")
	if err != nil || len(res) != 2 || res[0].Error != "" || res[1].Error != "" {
		t.Fatalf("%+v %v", res, err)
	}
	rs, _ := s.Build(ctx, "cart", "production")
	for _, k := range []string{"one", "two"} {
		if toggles.Evaluate(rs, k, nil).Value != false {
			t.Fatalf("%s is not back", k)
		}
	}
	if list, _ := s.ChangesSince(ctx, "cart", "production", good); len(list) != 0 {
		t.Fatalf("changes still listed after reverting them: %+v", list)
	}
	tl, _ := s.Timeline(ctx, "cart", "production", good, 20)
	if len(tl) < 4 {
		t.Fatalf("the timeline has %d entries", len(tl))
	}
}

func TestStaleNudgesAreAdviceOnly(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	k := mustCreate(t, s, "cart", "old-flag")
	for _, env := range []string{"dev", "production"} {
		in := onFor(true)
		if env == "production" {
			off := false
			_, _ = s.SetSafeguards(ctx, ada, ProjectWhere("cart"), "production", toggles.Setting{RequireApproval: &off}, "test")
		}
		if _, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: env, State: in, Reason: "on"}); err != nil {
			t.Fatal(err)
		}
	}
	sm, _ := s.Get(ctx, "cart", "old-flag", []string{"dev", "production"})
	if len(sm.Stale) != 0 {
		t.Fatalf("a new toggle is not stale: %+v", sm.Stale)
	}
	clk.add(45 * 24 * time.Hour)
	sm, _ = s.Get(ctx, "cart", "old-flag", []string{"dev", "production"})
	reasons := map[string]bool{}
	for _, n := range sm.Stale {
		reasons[n.Reason] = true
	}
	if !reasons["settled"] || !reasons["unused"] {
		t.Fatalf("%+v", sm.Stale)
	}
	// Still there, still on: nothing is done to it.
	rs, _ := s.Build(ctx, "cart", "dev")
	if toggles.Evaluate(rs, "old-flag", nil).Value != true {
		t.Fatal("a stale toggle was changed")
	}
}

func TestFileExportPlanApplyNeverRemoves(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	file, err := toggles.ParseFile([]byte(`
toggles:
  checkout-v2:
    type: boolean
    default: false
    owner: team-pay
    expires: 2027-03-01
    environments:
      dev:
        on: true
        off_value: false
        fallthrough: {rollout: {buckets: [{value: true, weight: 2500}, {value: false, weight: 7500}]}}
segments:
  staff:
    rules: [{clauses: [{attribute: email, op: suffix, values: ["@corp.example"]}]}]
`))
	if err != nil {
		t.Fatal(err)
	}
	drift, err := s.Plan(ctx, "cart", file)
	if err != nil || len(drift) < 3 {
		t.Fatalf("%+v %v", drift, err)
	}
	// Planning changed nothing.
	if l, _ := s.List(ctx, "cart", nil); len(l) != 0 {
		t.Fatal("a plan created something")
	}
	res, err := s.Apply(ctx, ada, "cart", file, "from the repository", "config:abc123")
	if err != nil || len(res.Failed) != 0 || len(res.Made) != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	d, _ := s.Get(ctx, "cart", "checkout-v2", []string{"dev"})
	if d.Origin != "config:abc123" || d.Environments[0].Mode != "rollout" {
		t.Fatalf("%+v", d)
	}
	if drift, _ := s.Plan(ctx, "cart", file); len(drift) != 0 {
		t.Fatalf("drift after apply: %+v", drift)
	}
	// A toggle made by hand is never removed by applying a file that lacks it.
	mustCreate(t, s, "cart", "by-hand")
	drift, _ = s.Plan(ctx, "cart", file)
	if len(drift) != 1 || drift[0].Kind != "only_live" {
		t.Fatalf("%+v", drift)
	}
	if res, err := s.Apply(ctx, ada, "cart", file, "again", ""); err != nil || len(res.Made) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if l, _ := s.List(ctx, "cart", nil); len(l) != 2 {
		t.Fatalf("a toggle was removed: %d", len(l))
	}
	// The export reads back as the same file.
	exp, err := s.Export(ctx, "cart")
	if err != nil {
		t.Fatal(err)
	}
	yml, err := exp.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toggles.ParseFile(yml); err != nil {
		t.Fatalf("%v\n%s", err, yml)
	}
}

func TestDefinitionsAreChecked(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	bad := []DefinitionInput{
		{Key: Key{Where: ProjectWhere("cart"), Key: "Bad Key"}, Type: toggles.Boolean, Default: false},
		{Key: Key{Where: ProjectWhere("cart"), Key: "ok"}, Type: "float", Default: 1},
		{Key: Key{Where: ProjectWhere("cart"), Key: "ok"}, Type: toggles.Boolean, Default: "yes"},
		{Key: Key{Where: Where{Level: LevelUnit}, Key: "ok"}, Type: toggles.Boolean, Default: false},
	}
	for i, in := range bad {
		if _, err := s.Create(ctx, ada, in, "x"); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	k := mustCreate(t, s, "cart", "a")
	if _, err := s.Create(ctx, ada, boolDef("cart", "a"), "again"); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("creating twice: %v", err)
	}
	// A loop of prerequisites is refused.
	if _, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: k, Environment: "dev", Reason: "x",
		State: toggles.State{On: true, OffValue: false, Fallthrough: toggles.Serve{Value: true}, Prerequisites: []toggles.Prerequisite{{Toggle: "b", Value: true}}}}); err != nil {
		t.Fatal(err)
	}
	b := mustCreate(t, s, "cart", "b")
	if _, err := s.ChangeState(ctx, ada, StateChange{Project: "cart", Toggle: b, Environment: "dev", Reason: "x",
		State: toggles.State{On: true, OffValue: false, Fallthrough: toggles.Serve{Value: true}, Prerequisites: []toggles.Prerequisite{{Toggle: "a", Value: true}}}}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("a loop was accepted: %v", err)
	}
	// A client-visible toggle cannot need one that is not visible.
	vis := true
	if _, err := s.Update(ctx, ada, k, Patch{ClientVisible: &vis}, "x"); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("visible toggle with a hidden prerequisite: %v", err)
	}
	// Deleting needs a reason and can be reverted, keeping the buckets.
	salt := mustSalt(t, s, k)
	if err := s.Delete(ctx, ada, k, ""); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("delete without a reason: %v", err)
	}
	if err := s.Delete(ctx, ada, k, "finished"); err != nil {
		t.Fatal(err)
	}
	h, _ := s.History(ctx, k, "", 5)
	if _, err := s.Revert(ctx, ada, h[0].ID, "deleted by mistake"); err != nil {
		t.Fatal(err)
	}
	if mustSalt(t, s, k) != salt {
		t.Fatal("a restored toggle moved people between buckets")
	}
	if sm, err := s.Get(ctx, "cart", "a", []string{"dev"}); err != nil || !sm.Environments[0].State.On {
		t.Fatalf("the restored toggle lost its state: %+v %v", sm, err)
	}
}

func mustSalt(t *testing.T, s *Service, k Key) string {
	t.Helper()
	d, err := getDef(context.Background(), s.d.DB, k)
	if err != nil {
		t.Fatal(err)
	}
	return d.Salt
}
