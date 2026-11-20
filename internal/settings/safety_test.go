package settings

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openEngine(t *testing.T, db string) *engine.Engine {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: db, Logger: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

// A boot file with sign-in and nobody named in it: only the database's role
// bindings say who administers.
func bootOIDC(t *testing.T, data string, admins bool) *config.Config {
	t.Helper()
	text := "data: " + data + "\negress: {allow_local: true}\nauth:\n  mode: oidc\n  oidc:\n    issuer: https://sso.example.test\n    client_id: l\n    client_secret_env: X\n    redirect_url: https://l.example.test/auth/callback\n"
	if admins {
		text += "  admins: {users: [root]}\n"
	}
	c, _, err := config.ParseSections([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func change(kind, name, spec string) Change {
	return Change{Kind: kindByName(kind), Name: name, Action: engine.ConfigUpdate, Spec: json.RawMessage(spec), IfVersion: -1}
}

// Nobody could undo a change that removes the last administrator, so it is
// refused whether it deletes the binding, narrows it or swaps its role.
func TestTheLastAdministratorCannotBeRemoved(t *testing.T) {
	eng := openEngine(t, testdb.New(t))
	s := New(eng, bootOIDC(t, t.TempDir(), false), quietLog())
	if _, err := s.Apply("ada", []Change{change("role-binding", "root", `{"user":"ada","role":"admin"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("ada", []Change{change("role-binding", "viewers", `{"group":"all","role":"viewer"}`)}); err != nil {
		t.Fatal(err)
	}
	bk := kindByName("role-binding")
	for name, c := range map[string]Change{
		"delete":                         {Kind: bk, Name: "root", Action: engine.ConfigDelete, IfVersion: -1},
		"narrow":                         change("role-binding", "root", `{"user":"ada","role":"admin","projects":["web"]}`),
		"demote":                         change("role-binding", "root", `{"user":"ada","role":"viewer"}`),
		"delete in a batch with another": {Kind: bk, Name: "root", Action: engine.ConfigDelete, IfVersion: -1},
	} {
		if _, err := s.Apply("ada", []Change{c}); err == nil || !strings.Contains(err.Error(), "nobody able to administer") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// With another everywhere-admin it is fine.
	if _, err := s.Apply("ada", []Change{change("role-binding", "ops", `{"group":"ops","role":"admin"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("ada", []Change{{Kind: bk, Name: "root", Action: engine.ConfigDelete, IfVersion: -1}}); err != nil {
		t.Errorf("deleting one of two administrators: %v", err)
	}
}

// A stored configuration that cannot be loaded starts the server in safe
// mode with the last one that worked; an object of a kind a newer version
// wrote is ignored with a warning.
func TestStartInSafeModeWithTheLastWorkingConfiguration(t *testing.T) {
	db, data := testdb.New(t), t.TempDir()
	eng := openEngine(t, db)
	apply := func(*config.Config, *Layout) error { return nil }
	s := New(eng, bootOIDC(t, data, true), quietLog())
	if _, err := s.Apply("ada", []Change{change("project", "web", `{"repo":"https://git.example.com/web.git"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(apply); err != nil {
		t.Fatal(err)
	}
	if s.SafeMode() != nil || s.Status().Revision != 1 {
		t.Fatalf("a good configuration: %+v", s.Status())
	}
	// A newer version writes a kind this one does not know: ignored.
	rev, _ := eng.ConfigRevision()
	if _, _, err := eng.WriteConfig(rev, "newer", []engine.ConfigWrite{{Kind: "gizmo", Name: "g", Action: engine.ConfigUpdate, Spec: json.RawMessage(`{"x":1}`), IfVersion: -1}}, nil); err != nil {
		t.Fatal(err)
	}
	s2 := New(eng, bootOIDC(t, data, true), quietLog())
	if c, err := s2.StartConfig(); err != nil || len(c.Projects) != 1 || s2.SafeMode() != nil {
		t.Fatalf("an unknown kind stopped the start: %v %v %v", c, err, s2.SafeMode())
	}
	// Then something is stored that cannot be loaded (written behind the
	// checks, as an older or newer version could).
	rev, _ = eng.ConfigRevision()
	if _, _, err := eng.WriteConfig(rev, "other", []engine.ConfigWrite{{Kind: "project", Name: "broken", Action: engine.ConfigUpdate, Spec: json.RawMessage(`{"repo":"x","no_such_setting":1}`), IfVersion: -1}}, nil); err != nil {
		t.Fatal(err)
	}
	s3 := New(eng, bootOIDC(t, data, true), quietLog())
	c, err := s3.StartConfig()
	if err != nil || len(c.Projects) != 1 || c.Projects[0].Name != "web" || s3.SafeMode() == nil {
		t.Fatalf("safe mode: %+v %v %+v", c, err, s3.SafeMode())
	}
	if got := s3.SafeMode().Using; !strings.Contains(got, "last working") {
		t.Errorf("using %q", got)
	}
	var applied *config.Config
	if err := s3.Load(func(c *config.Config, _ *Layout) error { applied = c; return nil }); err != nil {
		t.Fatalf("Load must not fail in safe mode: %v", err)
	}
	if applied == nil || len(applied.Projects) != 1 || s3.Status().Safe == nil || s3.Status().Problem == "" {
		t.Fatalf("applied %+v status %+v", applied, s3.Status())
	}
	// Repaired: the next refresh leaves safe mode.
	rev, _ = eng.ConfigRevision()
	if _, _, err := eng.WriteConfig(rev, "ada", []engine.ConfigWrite{{Kind: "project", Name: "broken", Action: engine.ConfigDelete, IfVersion: -1}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s3.Refresh(); err != nil || s3.SafeMode() != nil || s3.Status().Problem != "" {
		t.Fatalf("after the repair: %v %+v", err, s3.Status())
	}
	// With no last working configuration the bootstrap file alone runs.
	s4 := New(eng, bootOIDC(t, t.TempDir(), true), quietLog())
	rev, _ = eng.ConfigRevision()
	if _, _, err := eng.WriteConfig(rev, "other", []engine.ConfigWrite{{Kind: "project", Name: "worse", Action: engine.ConfigUpdate, Spec: json.RawMessage(`{"repo":"x","no_such_setting":1}`), IfVersion: -1}}, nil); err != nil {
		t.Fatal(err)
	}
	c, err = s4.StartConfig()
	if err != nil || len(c.Projects) != 0 || s4.SafeMode() == nil || !strings.Contains(s4.SafeMode().Using, "bootstrap") {
		t.Fatalf("no last working configuration: %+v %v %+v", c, err, s4.SafeMode())
	}
}

// An operator on the server host puts an earlier revision back, with no
// server and no sign-in.
func TestRestoreRevisionOffline(t *testing.T) {
	eng := openEngine(t, testdb.New(t))
	s := New(eng, bootOIDC(t, t.TempDir(), true), quietLog())
	for _, c := range []Change{change("project", "web", `{"repo":"https://git.example.com/web.git"}`),
		change("project", "api", `{"repo":"https://git.example.com/api.git"}`)} {
		if _, err := s.Apply("ada", []Change{c}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Apply("ada", []Change{{Kind: kindByName("project"), Name: "web", Action: engine.ConfigDelete, IfVersion: -1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("ada", []Change{change("project", "api", `{"repo":"https://git.example.com/api2.git"}`)}); err != nil {
		t.Fatal(err)
	}
	next, n, err := RestoreRevision(eng, 2, "operator")
	if err != nil || n != 2 || next != 5 {
		t.Fatalf("restore: rev %d, %d changes, %v", next, n, err)
	}
	_, objs, _ := eng.ConfigSnapshot()
	got := map[string]string{}
	for _, o := range objs {
		if !o.Deleted() {
			got[o.Name] = string(o.Spec)
		}
	}
	if len(got) != 2 || !strings.Contains(got["web"], "web.git") || !strings.Contains(got["api"], "/api.git") {
		t.Errorf("after the restore: %v", got)
	}
	if _, n, _ := RestoreRevision(eng, 2, "operator"); n != 0 {
		t.Errorf("a second restore changed %d objects", n)
	}
	if _, _, err := RestoreRevision(eng, 99, "operator"); err == nil {
		t.Error("a revision that does not exist")
	}
}

// A change that is stored but cannot be applied here says so (it is not a
// 200), is not lost, is tried again at the next poll, and every server's
// state, with the revision it applied, is visible.
func TestAStoredChangeThatCannotBeAppliedIsReportedAndRetried(t *testing.T) {
	eng := openEngine(t, testdb.New(t))
	s := New(eng, bootOIDC(t, t.TempDir(), true), quietLog())
	fail := false
	tries := 0
	if err := s.Load(func(*config.Config, *Layout) error {
		tries++
		if fail {
			return errors.New("the project manager is not ready")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fail = true
	res, err := s.Apply("ada", []Change{change("project", "web", `{"repo":"https://git.example.com/web.git"}`)})
	var na *NotAppliedError
	if !errors.As(err, &na) || na.Revision != 1 || len(res) != 1 || res[0].Object == nil {
		t.Fatalf("want a NotAppliedError with the stored object: %v %v", err, res)
	}
	if !strings.Contains(err.Error(), "stored") || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("the message: %v", err)
	}
	st := s.Status()
	if st.Revision != 0 || st.Stored != 1 || st.Problem == "" || len(st.Servers) != 1 || st.Servers[0].State != "failed" {
		t.Fatalf("status while failing: %+v", st)
	}
	// The next poll tries again, and succeeds.
	before := tries
	fail = false
	if err := s.Refresh(); err != nil || tries != before+1 {
		t.Fatalf("retry: %v after %d tries", err, tries-before)
	}
	st = s.Status()
	if st.Revision != 1 || st.Stored != 1 || st.Problem != "" || st.Servers[0].State != "applied" || st.Servers[0].Revision != 1 {
		t.Errorf("status after the retry: %+v", st)
	}
	// An apply that stops half way is finished by the retry: the second
	// try sees the whole configuration.
	var seen []int
	fail = false
	step := 0
	s2 := New(eng, bootOIDC(t, t.TempDir(), true), quietLog())
	if err := s2.Load(func(c *config.Config, _ *Layout) error {
		step++
		seen = append(seen, len(c.Projects))
		if step == 2 {
			return errors.New("half way")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Apply("ada", []Change{change("project", "api", `{"repo":"https://git.example.com/api.git"}`)}); err == nil {
		t.Fatal("the half-way failure was not reported")
	}
	if err := s2.Refresh(); err != nil || seen[len(seen)-1] != 2 {
		t.Errorf("the retry: %v, saw %v projects", err, seen)
	}
}
