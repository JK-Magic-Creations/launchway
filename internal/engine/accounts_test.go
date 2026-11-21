package engine

import (
	"errors"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func accountsEngine(t *testing.T) *Engine {
	t.Helper()
	var buf syncBuffer
	return mustOpenAs(t, testdb.New(t), newClock(), 0, &buf)
}

func TestSetupCodeIsSingleUseAndExpires(t *testing.T) {
	e := accountsEngine(t)
	now := time.Now().UnixMilli()
	if err := e.IssueSetupCode("h1", now, now+1000, "generated"); err != nil {
		t.Fatal(err)
	}
	if err := e.IssueSetupCode("h2", now, now+1000, "console"); err != nil { // an unused code is replaced
		t.Fatal(err)
	}
	acct := func(n string) *LocalAccount {
		return &LocalAccount{Username: n, Role: "admin", PasswordHash: "x", CreatedAt: now}
	}
	if err := e.CompleteSetup("h1", acct("a"), nil, now); !errors.Is(err, ErrSetupCode) {
		t.Fatalf("the replaced code worked: %v", err)
	}
	if err := e.CompleteSetup("h2", acct("a"), nil, now+2000); !errors.Is(err, ErrSetupCode) {
		t.Fatalf("an expired code worked: %v", err)
	}
	if err := e.CompleteSetup("h2", acct("a"), []string{"r1", "r2"}, now+10); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.RecoveryCodesLeft("a"); n != 2 {
		t.Fatalf("recovery codes %d", n)
	}
	if err := e.CompleteSetup("h2", acct("b"), nil, now+20); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second administrator through setup: %v", err)
	}
	if err := e.IssueSetupCode("h3", now, now+1000, "console"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a new code after setup: %v", err)
	}
}

func TestLocalAccountLifecycle(t *testing.T) {
	e := accountsEngine(t)
	now := time.Now()
	ms := now.UnixMilli()
	a := &LocalAccount{Username: "ann", Role: "viewer", PasswordHash: "h", CreatedAt: ms}
	if err := e.CreateLocalAccount(a); err != nil {
		t.Fatal(err)
	}
	if err := e.CreateLocalAccount(a); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	lock := func(n int) time.Duration {
		if n >= 3 {
			return time.Minute
		}
		return 0
	}
	for i := 1; i <= 3; i++ {
		n, until, err := e.LocalFailure("ann", now, lock)
		if err != nil || n != int64(i) || (i == 3) != (until > 0) {
			t.Fatalf("failure %d: %d %d %v", i, n, until, err)
		}
	}
	if n, until, _ := e.LocalFailure("nobody", now, lock); n != 0 || until != 0 {
		t.Fatal("a failure was counted for an unknown account")
	}
	got, _ := e.LocalAccountByName("ann")
	if got.FailedCount != 3 || got.LockedUntil == 0 {
		t.Fatalf("%+v", got)
	}
	if err := e.SetLocalPassword("ann", "h2", ms, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = e.LocalAccountByName("ann")
	if got.FailedCount != 0 || got.LockedUntil != 0 || got.PasswordHash != "h2" {
		t.Fatalf("a new password did not clear the lock: %+v", got)
	}
	// A second factor: a step is never accepted twice.
	if err := e.SetLocalTOTP("ann", "SECRET", true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.RecordTOTPStep("ann", 100); !ok {
		t.Fatal("first use")
	}
	if ok, _ := e.RecordTOTPStep("ann", 100); ok {
		t.Fatal("replay accepted")
	}
	if err := e.ReplaceRecoveryCodes("ann", []string{"c1", "c2"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.UseRecoveryCode("ann", "c1", ms); !ok {
		t.Fatal("recovery code")
	}
	if ok, _ := e.UseRecoveryCode("ann", "c1", ms); ok {
		t.Fatal("a recovery code worked twice")
	}
	if err := e.SetLocalTOTP("ann", "", false); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.RecoveryCodesLeft("ann"); n != 0 {
		t.Fatal("removing the second factor kept the recovery codes")
	}
	// Links: only the newest works, once, before it expires.
	if err := e.CreateLink(Link{Hash: "l1", Username: "ann", Kind: LinkReset, CreatedAt: ms, ExpiresAt: ms + 1000}); err != nil {
		t.Fatal(err)
	}
	if err := e.CreateLink(Link{Hash: "l2", Username: "ann", Kind: LinkReset, CreatedAt: ms, ExpiresAt: ms + 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LinkByHash("l1", ms); !errors.Is(err, ErrNotFound) {
		t.Fatal("an older link still works")
	}
	if ok, _ := e.UseLink("l2", ms+2000); ok {
		t.Fatal("an expired link worked")
	}
	if ok, _ := e.UseLink("l2", ms); !ok {
		t.Fatal("link")
	}
	if ok, _ := e.UseLink("l2", ms); ok {
		t.Fatal("a link worked twice")
	}
	if n, _ := e.CountLocalAdmins(); n != 0 {
		t.Fatal("admins")
	}
	if err := e.DeleteLocalAccount("ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LocalAccountByName("ann"); !errors.Is(err, ErrNotFound) {
		t.Fatal("still there")
	}
}
