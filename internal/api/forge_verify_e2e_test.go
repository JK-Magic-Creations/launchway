package api

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

func TestVerifyThroughTheAPI(t *testing.T) {
	guard, _ := egress.New([]string{"127.0.0.1"})
	e, _ := hostedEnvWith(t, envOpts{}, hosting.Options{Guard: guard}, githttp.Options{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	root := t.TempDir()
	src := work(t)
	mustGit(t, src, "tag", "v1")
	mustGit(t, root, "clone", "-q", "--mirror", src, filepath.Join(root, "old", "shop.git"))
	other := otherServer(t, root)
	if r := otto.do("POST", e.srv.URL+"/api/v1/imports/verify", `{"urls":["`+other.URL+`/old/shop.git"]}`); r.status != 403 {
		t.Errorf("an operator verified: %d", r.status)
	}
	if r := ada.do("POST", e.srv.URL+"/api/v1/imports", `{"urls":["`+other.URL+`/old/shop.git"],"wait":true}`); r.status != 200 {
		t.Fatalf("import: %d %s", r.status, r.body)
	}
	var ver mirror.VerifyResult
	r := ada.do("POST", e.srv.URL+"/api/v1/imports/verify", `{"urls":["`+other.URL+`/old/shop.git"]}`)
	r.json(t, &ver)
	if r.status != 200 || !ver.Equal || ver.Summary["equal"] != 1 {
		t.Fatalf("verify: %d %s", r.status, r.body)
	}
	commit(t, src, "x", "y", "later")
	mustGit(t, src, "push", "-q", filepath.Join(root, "old", "shop.git"), "HEAD:refs/heads/main")
	r = ada.do("POST", e.srv.URL+"/api/v1/imports/verify", `{"urls":["`+other.URL+`/old/shop.git"]}`)
	r.json(t, &ver)
	if ver.Equal || ver.Items[0].Status != "different" || !strings.Contains(strings.Join(ver.Items[0].Differences, " "), "refs/heads/main") {
		t.Fatalf("verify after a change: %s", r.body)
	}

	seen := false
	for _, a := range e.auditEntries(t) {
		seen = seen || a.Action == "repository.verify"
	}
	if !seen {
		t.Error("no audit entry repository.verify")
	}
}
