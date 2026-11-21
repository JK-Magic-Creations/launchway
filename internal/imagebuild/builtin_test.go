package imagebuild

import "testing"

func TestWithBuiltinGivesTheRunItsOwnProjectOnly(t *testing.T) {
	base := &Config{Registries: map[string]RegistryConfig{"harbor": {Host: "harbor.example.com"}}}
	cfg := base.WithBuiltin(Builtin{Host: "launchway.example.com", Username: "launchway-run", Push: true}, "cart")
	rc, err := cfg.Registry(BuiltinName)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Host != "launchway.example.com" || rc.Credentials == nil || rc.PullCredentials == nil {
		t.Fatalf("%+v", rc)
	}
	if rc.Credentials.Password.Store != BuiltinStore {
		t.Fatalf("the credential is read from %+v, not from the run's own store", rc.Credentials.Password)
	}
	for _, c := range []struct {
		project, repo string
		want          bool
	}{{"cart", "cart/api", true}, {"cart", "cart", true}, {"cart", "cart/a/b/c", true}, {"cart", "shop/api", false}, {"shop", "cart/api", false}, {"cart", "cartx/api", false}} {
		if got := rc.MayPush(c.project, c.repo); got != c.want {
			t.Errorf("project %s may push %s: %v, want %v", c.project, c.repo, got, c.want)
		}
	}
	if _, err := cfg.Registry("harbor"); err != nil {
		t.Fatalf("the operator's registry is gone: %v", err)
	}
	if _, ok := base.Registries[BuiltinName]; ok {
		t.Fatal("the runner's own configuration was changed")
	}
	// A pull-request run has a pull credential and no push credential or rule.
	pr, _ := base.WithBuiltin(Builtin{Host: "h", Push: false}, "cart").Registry(BuiltinName)
	if pr.Credentials != nil || pr.PullCredentials == nil || pr.MayPush("cart", "cart/api") {
		t.Fatalf("a run that may not push can: %+v", pr)
	}
	// What an operator configured under the name is kept.
	own := &Config{Registries: map[string]RegistryConfig{BuiltinName: {Host: "mine.example.com"}}}
	if rc, _ := own.WithBuiltin(Builtin{Host: "theirs"}, "cart").Registry(BuiltinName); rc.Host != "mine.example.com" {
		t.Fatalf("%+v", rc)
	}
	// With no registry given by the server, the error says why.
	if _, err := (&Config{}).Registry(BuiltinName); err == nil {
		t.Fatal("no error")
	}
}
