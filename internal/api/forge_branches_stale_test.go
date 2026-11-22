package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Branches that are merged and quiet are listed with a stale mark and removed in one go, but only those named from
// the list; protected ones, the default branch, branches with work of their own and recent ones stay.
func TestStaleBranchesAreListedAndRemoved(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	api := e.srv.URL + "/api/v1/repositories/acme/shop"
	src := t.TempDir()
	date := "2026-01-01T10:00:00Z"
	gitAt(t, src, date, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "first")
	gitAt(t, src, date, "branch", "old/done") // merged: nothing main lacks, and quiet since January
	gitAt(t, src, date, "branch", "keep/old") // merged and quiet, but protected
	gitAt(t, src, date, "checkout", "-q", "-b", "old/work")
	if err := os.WriteFile(filepath.Join(src, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "unmerged work")
	mustGit(t, src, "push", "-q", url, "main:refs/heads/main", "old/done:refs/heads/old/done", "keep/old:refs/heads/keep/old", "old/work:refs/heads/old/work")
	if r := ada.do("PUT", api+"/protected-branches", `{"patterns":["main","keep/*"]}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}

	var list struct {
		StaleDays int `json:"stale_days"`
		Items     []struct {
			Name   string `json:"name"`
			Stale  bool   `json:"stale"`
			Merged bool   `json:"merged"`
		} `json:"items"`
	}
	vic.get(api+"/branches").json(t, &list)
	marks := map[string][2]bool{}
	for _, b := range list.Items {
		marks[b.Name] = [2]bool{b.Stale, b.Merged}
	}
	if list.StaleDays != 90 || !marks["old/done"][0] || !marks["old/done"][1] || marks["old/work"][1] || marks["main"][1] {
		t.Fatalf("branch marks: %+v (days %d)", marks, list.StaleDays)
	}

	// Listing is for operators too, deleting needs a write right.
	if r := vic.do("POST", api+"/branches/delete-stale", `{"confirm":false}`); r.status != 403 {
		t.Fatalf("a viewer: %d %s", r.status, r.body)
	}
	var out staleBranchesOut
	ada.do("POST", api+"/branches/delete-stale", `{"confirm":false}`).json(t, &out)
	if len(out.Candidates) != 1 || out.Candidates[0] != "old/done" || len(out.Deleted) != 0 {
		t.Fatalf("candidates: %+v", out)
	}
	if r := ada.do("POST", api+"/branches/delete-stale", `{"confirm":true}`); r.status != 400 {
		t.Fatalf("confirm without names: %d %s", r.status, r.body)
	}
	r := ada.do("POST", api+"/branches/delete-stale", `{"confirm":true,"names":["old/done","old/work","main","keep/old"]}`)
	r.json(t, &out)
	if r.status != 200 || len(out.Deleted) != 1 || out.Deleted[0] != "old/done" || len(out.Skipped) != 3 {
		t.Fatalf("deleted %d %s", r.status, r.body)
	}
	var after struct {
		Items []struct{ Name string } `json:"items"`
	}
	vic.get(api+"/branches").json(t, &after)
	var names []string
	for _, b := range after.Items {
		names = append(names, b.Name)
	}
	if got := strings.Join(sorted(names), ","); got != "keep/old,main,old/work" {
		t.Fatalf("branches left: %s", got)
	}

	// The days are a hosting setting.
	if r := ada.do("PUT", e.srv.URL+"/api/v1/hosting-settings", `{"stale_branch_days":0}`); r.status != 400 {
		t.Fatalf("zero days: %d", r.status)
	}
	if r := ada.do("PUT", e.srv.URL+"/api/v1/hosting-settings", `{"stale_branch_days":400}`); r.status != 200 || !strings.Contains(string(r.body), `"stale_branch_days":400`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
}
