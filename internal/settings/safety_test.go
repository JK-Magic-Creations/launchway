package settings

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
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

// Deleting a project that a run is using is refused with what depends on it;
// with force it goes, and the history and audit say so.
func TestDeletingWhatActiveRunsUseNeedsForce(t *testing.T) {
	eng := openEngine(t, testdb.New(t))
	s := New(eng, bootOIDC(t, t.TempDir(), true), quietLog())
	if _, err := s.Apply("ada", []Change{change("project", "shop", `{"repo":"https://git.example.com/shop.git"}`)}); err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Parse([]byte("pipeline: x\nsteps:\n  a:\n    image: none\n    run: echo hi\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.CreateRun(engine.NewRun{Project: "shop", Pipeline: p, Source: engine.Source{Kind: "git", URL: "https://git.example.com/shop.git"},
		Trigger: engine.Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: strings.Repeat("1", 40), Actor: "ann"}}); err != nil {
		t.Fatal(err)
	}
	del := Change{Kind: kindByName("project"), Name: "shop", Action: engine.ConfigDelete, IfVersion: -1, Comment: "tidy"}
	if _, err := s.Apply("ada", []Change{del}); err == nil || !strings.Contains(err.Error(), "in use") || !strings.Contains(err.Error(), "force") {
		t.Fatalf("a delete under a running run: %v", err)
	}
	del.Force = true
	res, err := s.Apply("ada", []Change{del})
	if err != nil || len(res[0].Forced) != 1 {
		t.Fatalf("forced: %v %+v", err, res)
	}
	h, _ := s.History(engine.ConfigHistoryFilter{Kind: "project", Name: "shop"})
	if len(h) == 0 || !strings.Contains(h[0].Comment, "forced while in use") || !strings.Contains(h[0].Comment, "tidy") {
		t.Errorf("the history does not say it was forced: %+v", h)
	}
}

// Settings that name a file on the server are the operator's: the bootstrap
// file brings them in, the API keeps them but cannot set or change one.
func TestServerFilePathsAreNotSettableThroughTheAPI(t *testing.T) {
	eng := openEngine(t, testdb.New(t))
	s := New(eng, bootOIDC(t, t.TempDir(), true), quietLog())
	for name, c := range map[string]Change{
		"token_file":     change("runner-pool", "p", `{"token_file":"/var/run/secrets/kubernetes.io/serviceaccount/token","targets":["home"]}`),
		"artifacts.dir":  change("installation", "installation", `{"artifacts":{"store":"filesystem","dir":"/etc"}}`),
		"ca_file":        change("installation", "installation", `{"artifacts":{"store":"s3","s3":{"endpoint":"https://s3.example.com","bucket":"b","ca_file":"/etc/shadow"}}}`),
		"webhook secret": change("project", "web", `{"repo":"https://git.example.com/web.git","git_webhook":{"secret_file":"/etc/passwd"}}`),
	} {
		if _, err := s.Apply("ada", []Change{c}); err == nil || !strings.Contains(err.Error(), "only be set in the bootstrap file") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// What the bootstrap import brought in stays and can be left alone.
	tokenFile := filepath.Join(t.TempDir(), "pool-token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("ab", 24)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("ada", []Change{change("environment", "home", `{"target":"home","projects":["*"]}`), change("environment", "edge", `{"target":"edge","projects":["*"]}`)}); err != nil {
		t.Fatal(err)
	}
	rev, _ := eng.ConfigRevision()
	if _, _, err := eng.WriteConfig(rev, "import", []engine.ConfigWrite{{Kind: "runner-pool", Name: "p", Action: engine.ConfigUpdate, IfVersion: -1,
		Spec: json.RawMessage(`{"token_file":"` + tokenFile + `","targets":["home"]}`)}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("ada", []Change{change("runner-pool", "p", `{"token_file":"`+tokenFile+`","targets":["home","edge"]}`)}); err != nil {
		t.Errorf("changing another setting of an object that has a file path: %v", err)
	}
	if _, err := s.Apply("ada", []Change{change("runner-pool", "p", `{"token_file":"/etc/shadow","targets":["home"]}`)}); err == nil {
		t.Error("changing the path was accepted")
	}
}

// An object Terraform owns is read-only to everyone else unless they take
// ownership explicitly; the manager label a client sends never claims an
// object by itself.
func TestOwnershipIsEnforced(t *testing.T) {
	eng := openEngine(t, testdb.New(t))
	s := New(eng, bootOIDC(t, t.TempDir(), true), quietLog())
	as := func(manager string, take bool, c Change) (*Result, error) {
		c.ManagedBy, c.TakeOwnership = manager, take
		r, err := s.Apply("ada", []Change{c})
		if err != nil {
			return nil, err
		}
		return &r[0], nil
	}
	spec := func(branch string) Change {
		return change("project", "web", `{"repo":"https://git.example.com/web.git","branch":"`+branch+`"}`)
	}
	if _, err := as("terraform", false, spec("main")); err != nil {
		t.Fatal(err)
	}
	if _, err := as("terraform", false, spec("dev")); err != nil {
		t.Errorf("the owner changing its own object: %v", err)
	}
	for name, c := range map[string]Change{"edit": spec("hotfix"),
		"delete": {Kind: kindByName("project"), Name: "web", Action: engine.ConfigDelete, IfVersion: -1}} {
		if _, err := as("api", false, c); err == nil || !strings.Contains(err.Error(), "managed by terraform") {
			t.Errorf("%s by the UI: %v", name, err)
		}
	}
	r, err := as("api", true, spec("hotfix"))
	if err != nil || r.TookOwnership != "terraform" || r.Object.ManagedBy != "api" {
		t.Fatalf("taking ownership: %v %+v", err, r)
	}
	h, _ := s.History(engine.ConfigHistoryFilter{Kind: "project", Name: "web", Limit: 1})
	if !strings.Contains(h[0].Comment, "took ownership from terraform") {
		t.Errorf("not in the history: %+v", h[0])
	}
	// Terraform cannot claim an object the UI manages by sending a label.
	if _, err := as("terraform", false, spec("main")); err == nil || !strings.Contains(err.Error(), "take ownership") {
		t.Errorf("a claim by label: %v", err)
	}
	if _, err := as("terraform", true, spec("main")); err != nil {
		t.Errorf("an explicit claim: %v", err)
	}
}
