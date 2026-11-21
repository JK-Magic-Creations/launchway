package api

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// otherServer serves bare repositories under root with git's http-backend.
func otherServer(t *testing.T, root string) *httptest.Server {
	t.Helper()
	gp, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	h := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(gp)), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	t.Cleanup(srv.Close)
	return srv
}

func TestImportThroughTheAPI(t *testing.T) {
	guard, _ := egress.New([]string{"127.0.0.1"})
	e, _ := hostedEnvWith(t, envOpts{}, hosting.Options{Guard: guard}, githttp.Options{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	root := t.TempDir()
	src := work(t)
	mustGit(t, src, "tag", "v1")
	mustGit(t, root, "clone", "-q", "--mirror", src, filepath.Join(root, "old", "shop.git"))
	other := otherServer(t, root)
	body := `{"urls":["` + other.URL + `/old/shop.git"],"wait":true`

	if r := otto.do("POST", e.srv.URL+"/api/v1/imports", body+`}`); r.status != 403 {
		t.Errorf("an operator imported: %d %s", r.status, r.body)
	}
	var res mirror.ImportResult
	r := ada.do("POST", e.srv.URL+"/api/v1/imports", `{"urls":["`+other.URL+`/old/shop.git"],"dry_run":true}`)
	r.json(t, &res)
	if r.status != 200 || len(res.Items) != 1 || res.Items[0].State != "planned" || res.Items[0].Repository != "old/shop" {
		t.Fatalf("dry run: %d %s", r.status, r.body)
	}
	if r := ada.get(e.srv.URL + "/api/v1/repositories/old/shop"); r.status != 404 {
		t.Errorf("a dry run made the repository: %d", r.status)
	}
	r = ada.do("POST", e.srv.URL+"/api/v1/imports", body+`}`)
	r.json(t, &res)
	if r.status != 200 || res.Summary["verified"] != 1 || !res.Items[0].Verified || res.Items[0].HostedRefs != 2 {
		t.Fatalf("import: %d %s", r.status, r.body)
	}
	// Connections hold references only.
	if r := ada.do("POST", e.srv.URL+"/api/v1/connections", `{"name":"old","kind":"gitea","base_url":"`+other.URL+`","credential":{"env":"ghp_realtokenvalue"}}`); r.status != 400 || strings.Contains(string(r.body), "ghp_real") {
		t.Errorf("a token as the credential: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", e.srv.URL+"/api/v1/connections", `{"name":"old","kind":"github","base_url":"https://github.com"}`); r.status != 400 {
		t.Errorf("an unsupported kind: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", e.srv.URL+"/api/v1/connections", `{"name":"old","kind":"gitea","base_url":"`+other.URL+`","credential":{"env":"OLD_TOKEN"}}`); r.status != 201 {
		t.Fatalf("connection: %d %s", r.status, r.body)
	}
	if r := ada.get(e.srv.URL + "/api/v1/connections"); !strings.Contains(string(r.body), `"credential":"env:OLD_TOKEN"`) {
		t.Errorf("connections: %s", r.body)
	}
	if r := ada.do("DELETE", e.srv.URL+"/api/v1/connections/old", ""); r.status != 204 {
		t.Errorf("delete: %d", r.status)
	}
	seen := map[string]bool{}
	for _, a := range e.auditEntries(t) {
		seen[a.Action] = seen[a.Action] || a.Outcome == "ok" || a.Outcome == ""
	}
	for _, want := range []string{"repository.import", "connection.create", "connection.delete"} {
		if !seen[want] {
			t.Errorf("no audit entry %s", want)
		}
	}
	log, _ := e.eng.AuditLog(engine.AuditFilter{Action: "repository.import", Limit: 10})
	if len(log) == 0 || !strings.Contains(log[0].Detail+log[len(log)-1].Detail, "old/shop") {
		t.Errorf("import audit: %+v", log)
	}
}
