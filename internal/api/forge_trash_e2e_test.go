package api

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
)

func trashPolicy(t *testing.T) *auth.Policy {
	t.Helper()
	p, err := auth.NewPolicy([]auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{User: "dana", Role: auth.Admin, Projects: []string{"acme/*"}},
		{Group: "pay-view", Role: auth.Viewer, Projects: []string{"acme/*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ledgerRows(t *testing.T, e *authEnv, table, repo string) int {
	t.Helper()
	var n int
	if err := e.eng.Database().QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE repo_id = ?`, repo).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A deleted repository is listed for the people who administer it (who deleted it, when, when it goes), restored
// under its old address or, when another repository has taken that, under another name; removed for good only by an
// administrator who types the address, with its Git storage and rows, and with a Logbook line; and the background
// worker removes the ones whose retention ended and no others.
func TestRepositoryTrashListRestoreAndPurge(t *testing.T) {
	e, h := hostedEnvWith(t, envOpts{policy: trashPolicy(t)}, hosting.Options{}, githttp.Options{})
	shop := hostedRepo(t, e, "acme/shop")
	hostedRepo(t, e, "other/secret")
	ada, dana, vic := e.signIn(t, "ada"), e.signIn(t, "dana"), e.signIn(t, "vic")
	push := withToken(e.srv.URL, "/git/acme/shop.git", e.tokenFor(t, "ada", "git"))
	src := workingCopy(t)
	mustGit(t, src, "branch", "-M", "main")
	mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/main")
	if ledgerRows(t, e, "repo_packs", shop.ID) == 0 || ledgerRows(t, e, "repo_refs", shop.ID) == 0 {
		t.Fatal("the push left no pack or ref")
	}
	base := e.srv.URL + "/api/v1/repository-trash"

	if r := dana.do("DELETE", e.srv.URL+"/api/v1/repositories/acme/shop", ""); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	ada.do("DELETE", e.srv.URL+"/api/v1/repositories/other/secret", "")
	var page forge.Page[forge.TrashedRepository]
	dana.get(base).json(t, &page)
	if len(page.Items) != 1 || page.Items[0].Path != "acme/shop" || page.Items[0].DeletedBy != "dana" || page.Items[0].ID != shop.ID {
		t.Fatalf("the owner's own trash: %+v", page.Items)
	}
	if d := page.Items[0].PurgeAt.Sub(page.Items[0].DeletedAt); d != 30*24*time.Hour {
		t.Errorf("it goes after %v, want the 30 days of the setting", d)
	}
	if time.Since(page.Items[0].DeletedAt) > time.Minute {
		t.Errorf("deleted at %v", page.Items[0].DeletedAt)
	}
	vic.get(base).json(t, &page)
	if len(page.Items) != 0 {
		t.Errorf("a viewer sees deleted repositories: %+v", page.Items)
	}
	ada.get(base).json(t, &page)
	if len(page.Items) != 2 {
		t.Errorf("an administrator sees %d, want 2", len(page.Items))
	}
	if r := newBrowser(t).get(base); r.status != 401 {
		t.Errorf("signed out: %d", r.status)
	}

	// Another repository takes the address: restoring under it is refused in plain words, another name works.
	hostedRepo(t, e, "acme/shop")
	if r := vic.do("POST", base+"/"+shop.ID+"/restore", `{}`); r.status != 404 {
		t.Errorf("a viewer restored: %d", r.status)
	}
	if r := dana.do("POST", base+"/"+shop.ID+"/restore", `{}`); r.status != 409 || !strings.Contains(string(r.body), "another name") {
		t.Fatalf("restoring onto a taken address: %d %s", r.status, r.body)
	}
	if r := dana.do("POST", base+"/"+shop.ID+"/restore", `{"owner":"elsewhere"}`); r.status != 403 {
		t.Errorf("a project admin moved a repository to another owner: %d", r.status)
	}
	var back forge.Repository
	if r := dana.do("POST", base+"/"+shop.ID+"/restore", `{"name":"shop-old"}`); r.status != 200 {
		t.Fatalf("restore under another name: %d %s", r.status, r.body)
	} else {
		r.json(t, &back)
	}
	if back.Path != "acme/shop-old" || back.ID != shop.ID || !strings.HasSuffix(back.Clone.HTTPS, "/git/acme/shop-old.git") {
		t.Fatalf("restored: %+v", back)
	}
	if got := dana.get(e.srv.URL + "/api/v1/repositories/acme/shop-old"); got.status != 200 {
		t.Errorf("the restored repository: %d", got.status)
	}
	if ledgerRows(t, e, "repo_refs", shop.ID) == 0 {
		t.Error("restoring lost the refs")
	}
	if n := ledgerRows(t, e, "repo_trash", shop.ID); n != 0 {
		t.Errorf("a restored repository keeps its trash note: %d", n)
	}

	// Delete again: the administrator removes it for good, typing the address.
	dana.do("DELETE", e.srv.URL+"/api/v1/repositories/acme/shop-old", "")
	if r := dana.do("DELETE", base+"/"+shop.ID, `{"confirm":"acme/shop-old"}`); r.status != 403 {
		t.Errorf("a project admin removed for good: %d", r.status)
	}
	if r := ada.do("DELETE", base+"/"+shop.ID, `{}`); r.status != 400 || !strings.Contains(string(r.body), "type the address") {
		t.Fatalf("no confirmation: %d %s", r.status, r.body)
	}
	if r := ada.do("DELETE", base+"/"+shop.ID, `{"confirm":"acme/shop"}`); r.status != 400 {
		t.Errorf("the wrong address: %d", r.status)
	}
	var sum forge.PurgeSummary
	r := ada.do("DELETE", base+"/"+shop.ID, `{"confirm":"acme/shop-old"}`)
	if r.status != 200 {
		t.Fatalf("remove for good: %d %s", r.status, r.body)
	}
	r.json(t, &sum)
	if sum.Packs == 0 || sum.BlobsDeleted == 0 {
		t.Errorf("summary: %+v", sum)
	}
	for _, table := range []string{"repo_refs", "repo_packs", "repo_generation", "repo_trash", "forge_repositories"} {
		col := "repo_id"
		if table == "forge_repositories" {
			col = "id"
		}
		var n int
		if err := e.eng.Database().QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE `+col+` = ?`, shop.ID).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s keeps %d rows: %v", table, n, err)
		}
	}
	if r := ada.do("DELETE", base+"/"+shop.ID, `{"confirm":"acme/shop-old"}`); r.status != 404 {
		t.Errorf("removing it twice: %d", r.status)
	}
	var restoredLine, purgeLine int
	for _, a := range e.auditEntries(t) {
		switch {
		case a.Action == "repository.restore" && a.User == "dana" && a.Target == "acme/shop-old" && strings.Contains(a.Detail, "it was acme/shop"):
			restoredLine++
		case a.Action == "repository.purge" && a.User == "ada" && a.Outcome == "ok" && strings.Contains(a.Detail, "removed for good"):
			purgeLine++
		}
	}
	if restoredLine == 0 || purgeLine == 0 {
		t.Errorf("Logbook lines: restore %d, purge %d", restoredLine, purgeLine)
	}

	// The worker: the other repository is in the trash, its retention not over: it stays. Over: it goes, with a line.
	if n := h.PurgeExpired(t.Context()); n != 0 {
		t.Errorf("the worker removed %d repositories inside the retention", n)
	}
	if _, err := e.eng.Database().Exec(t.Context(), `UPDATE repo_trash SET deleted_at = ?`, time.Now().Add(-31*24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if n := h.PurgeExpired(t.Context()); n != 1 {
		t.Fatalf("the worker removed %d, want 1", n)
	}
	ada.get(base).json(t, &page)
	if len(page.Items) != 0 {
		t.Errorf("the trash after the worker: %+v", page.Items)
	}
	var workerLine bool
	for _, a := range e.auditEntries(t) {
		if a.Action == "repository.purge" && a.User == "system" && a.Target == "other/secret" {
			workerLine = true
		}
	}
	if !workerLine {
		t.Error("no Logbook line for the worker's removal")
	}
}
