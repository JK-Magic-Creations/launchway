package settings

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

// The pool token and OIDC secret the owner-shaped configuration reads.
func ownerEnv(t *testing.T) {
	t.Setenv("TEST_OIDC_SECRET", "not-a-real-secret")
	t.Setenv("TEST_POOL_TOKEN", strings.Repeat("ab", 24))
}

// configs are the real configurations in the repository and the shape of
// a real installation's.
func configs(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"examples/local.yaml":                               "../../examples/local.yaml",
		"examples/tutorials/deploy/launchway.yaml":          "../../examples/tutorials/deploy/launchway.yaml",
		"examples/tutorials/build-and-sign-image/launchway": "../../examples/tutorials/build-and-sign-image/launchway.yaml",
		"owner-shape": "testdata/owner-shape.yaml",
	}
}

// What the server runs with is the same whether the settings come from the
// file or from the objects imported from it: the one-time import changes
// nothing.
func TestImportedConfigurationIsTheFilesConfiguration(t *testing.T) {
	ownerEnv(t)
	t.Setenv("LAUNCHWAY_POOL_DEPLOY_KIND_TOKEN", strings.Repeat("cd", 24))
	for name, path := range configs(t) {
		t.Run(name, func(t *testing.T) {
			file, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			objs, err := FromConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			// As stored and read back.
			stored := storeRoundTrip(t, objs)
			got, _, err := Compose(file, stored)
			if err != nil {
				t.Fatal(err)
			}
			if err := Check(got, nil); err != nil {
				t.Fatalf("the imported configuration does not pass: %v", err)
			}
			same(t, "projects", byName(must(file.SourceProjects())), byName(must(got.SourceProjects())))
			same(t, "libraries", file.SourceLibraries(), got.SourceLibraries())
			same(t, "environments", byName(file.EngineEnvironments()), byName(got.EngineEnvironments()))
			fl, err1 := file.EngineLive()
			gl, err2 := got.EngineLive()
			if err1 != nil || err2 != nil {
				t.Fatal(err1, err2)
			}
			same(t, "engine settings", liveShape(fl), liveShape(gl))
			// Servers sharing a database compare policies by hash; the file's
			// have line numbers and the database's have none.
			if a, b := policy.Hash(fl.Policy.Inline), policy.Hash(gl.Policy.Inline); a != b {
				t.Errorf("the policies hash differently from the file (%s) and from the database (%s)", a, b)
			}
			fp := byName(file.Auth.Access)
			gp := byName(got.Auth.Access)
			same(t, "role bindings", fp, gp)
			fd, _ := file.DraftRules()
			gd, _ := got.DraftRules()
			same(t, "drafts", fd, gd)
			same(t, "token days", must(file.TokenMaxDays()), must(got.TokenMaxDays()))
			same(t, "runner admin", must(file.RunnerAdminRole()), must(got.RunnerAdminRole()))
			same(t, "waivers", must(file.WaiverRole()), must(got.WaiverRole()))
			same(t, "loops", must(file.LoopLimit()), must(got.LoopLimit()))

			// Export and import again: the same objects.
			text, err := Export(stored)
			if err != nil {
				t.Fatal(err)
			}
			again, err := ParseFile(text)
			if err != nil {
				t.Fatalf("%v\n%s", err, text)
			}
			same(t, "objects after export and import", specs(objs), specs(again))
		})
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func same(t *testing.T, what string, a, b any) {
	t.Helper()
	if !reflect.DeepEqual(a, b) {
		ja, _ := json.MarshalIndent(a, "", " ")
		jb, _ := json.MarshalIndent(b, "", " ")
		la, lb := strings.Split(string(ja), "\n"), strings.Split(string(jb), "\n")
		for i := 0; i < len(la) || i < len(lb); i++ {
			x, y := "", ""
			if i < len(la) {
				x = la[i]
			}
			if i < len(lb) {
				y = lb[i]
			}
			if x != y {
				lo := max(0, i-6)
				t.Errorf("%s differ at line %d:\nfile:\n%s\ndatabase:\n%s", what, i,
					strings.Join(la[lo:min(len(la), i+3)], "\n"), strings.Join(lb[lo:min(len(lb), i+3)], "\n"))
				return
			}
		}
		t.Errorf("%s differ (not in JSON): %#v\n%#v", what, a, b)
	}
}

// liveShape is what can be compared of engine settings (the model registry
// is a pointer).
func liveShape(o engine.LiveOptions) any {
	o2 := o
	o2.Models = nil
	o2.Environments, o2.RunnerPools, o2.HealthSources = nil, nil, nil
	var models any
	if o.Models != nil {
		models = o.Models.List()
	}
	b, _ := json.Marshal(o2)
	// Where a policy was written (its line) differs; nothing else may.
	return []any{lineRe.ReplaceAllString(string(b), ""), models, byName(o.Environments), byName(o.RunnerPools), byName(o.HealthSources)}
}

var lineRe = regexp.MustCompile(`,?"line":\d+`)

// byName sorts a list by how it prints: the order of the file and of the
// database differ, and nothing depends on it.
func byName[T any](list []T) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		b, _ := json.Marshal(x)
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

func specs(objs []engine.ConfigObject) map[string]string {
	out := map[string]string{}
	for _, o := range objs {
		out[o.Kind+"/"+o.Name+"@"+o.Unit] = string(compact(o.Spec))
	}
	return out
}

// storeRoundTrip passes objects through what a write stores.
func storeRoundTrip(t *testing.T, objs []engine.ConfigObject) []engine.ConfigObject {
	t.Helper()
	out := make([]engine.ConfigObject, 0, len(objs))
	for _, o := range objs {
		_, stored, err := decodeSpec(kindByName(o.Kind), o.Name, o.Spec)
		if err != nil {
			t.Fatalf("%s %s: %v", o.Kind, o.Name, err)
		}
		if string(stored) != string(compact(o.Spec)) {
			t.Errorf("%s %s: stored %s, imported %s", o.Kind, o.Name, stored, o.Spec)
		}
		o.Spec = stored
		out = append(out, o)
	}
	return out
}

// Settings left at their zero value are not stored, but a pointer that is
// set is, even when empty: push: {} lets the editor save, and
// pull_request: {} starts runs for pull requests.
func TestSpecsKeepWhatMeansSomething(t *testing.T) {
	k := kindByName("project")
	_, stored, err := decodeSpec(k, "web", json.RawMessage(`{"repo":"https://x.test/r.git","push":{},"poll":"","pipeline":{"library":"l","version":"v1","template":"t","on":{"pull_request":{}},"supersede":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"pipeline":{"library":"l","on":{"pull_request":{}},"template":"t","version":"v1"},"push":{},"repo":"https://x.test/r.git"}`
	if string(stored) != want {
		t.Fatalf("stored %s\nwant   %s", stored, want)
	}
	if _, _, err := decodeSpec(k, "web", json.RawMessage(`{"repo":"x","brnach":"main"}`)); err == nil || !strings.Contains(err.Error(), "brnach") {
		t.Fatalf("an unknown key was taken: %v", err)
	}
	if _, _, err := decodeSpec(k, "web", json.RawMessage(`{"name":"shop","repo":"x"}`)); err == nil {
		t.Fatal("a spec naming another object was taken")
	}
}

// Objects defined at a unit apply to its projects only.
func TestUnitsNarrowWhatTheirObjectsApplyTo(t *testing.T) {
	ownerEnv(t)
	boot, err := config.LoadBootstrap("testdata/oidc-boot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	obj := func(kind, name, unit, spec string) engine.ConfigObject {
		return engine.ConfigObject{Kind: kind, Name: name, Unit: unit, Spec: json.RawMessage(spec)}
	}
	objs := []engine.ConfigObject{
		obj("unit", "payments", "", `{"display_name":"Payments"}`),
		obj("unit", "empty", "", `{}`),
		obj("project", "pay-api", "payments", `{"repo":"https://x.test/a.git"}`),
		obj("project", "pay-web", "payments", `{"repo":"https://x.test/b.git"}`),
		obj("project", "blog", "", `{"repo":"https://x.test/c.git"}`),
		obj("environment", "pay-prod", "payments", `{"target":"k8s","projects":["*"]}`),
		obj("environment", "nothing", "empty", `{"target":"k8s","projects":["*"]}`),
		obj("environment", "all", "", `{"target":"k8s","projects":["*"]}`),
		obj("role-binding", "pay-ops", "payments", `{"group":"pay-ops","role":"operator"}`),
	}
	c, lay, err := Compose(boot, objs)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(c, nil); err != nil {
		t.Fatal(err)
	}
	envs := map[string][]string{}
	for _, e := range c.Environments {
		envs[e.Name] = e.Projects
	}
	same(t, "environments", map[string][]string{"pay-prod": {"pay-api", "pay-web"}, "nothing": {noProject}, "all": {"*"}}, envs)
	same(t, "binding", []string{"pay-api", "pay-web"}, c.Auth.Access[0].Projects)
	if !lay.UnitsInUse || len(lay.Units) != 2 || lay.ProjectUnit["pay-api"] != "payments" {
		t.Fatalf("%+v", lay)
	}
	// A unit that does not exist is refused.
	objs = append(objs, obj("library", "lib", "nowhere", `{"repo":"https://x.test/l.git"}`))
	if _, _, err := Compose(boot, objs); err == nil || !strings.Contains(err.Error(), `unit "nowhere" does not exist`) {
		t.Fatalf("%v", err)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
