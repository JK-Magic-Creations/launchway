package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/files"
)

func TestBrowsingThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, otto, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	hostedRepo(t, e, "other/secret")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	api := e.srv.URL + "/api/v1/repositories/acme/shop"

	src := t.TempDir()
	gitAt(t, src, "2026-06-01T10:00:00Z", "init", "-q", "-b", "main")
	write := func(p, c string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# Shop\n")
	write("cmd/main.go", "package main\n\nfunc Needle() {}\n")
	write("icon.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	gitAt(t, src, "2026-06-01T10:00:00Z", "add", "-A")
	gitAt(t, src, "2026-06-01T10:00:00Z", "commit", "-q", "-m", "first")
	write("cmd/main.go", "package main\n\nfunc Needle() {}\nfunc Two() {}\n")
	gitAt(t, src, "2026-06-02T10:00:00Z", "add", "-A")
	gitAt(t, src, "2026-06-02T10:00:00Z", "commit", "-q", "-m", "second")
	gitAt(t, src, "2026-06-02T10:00:00Z", "tag", "-a", "v1", "-m", "version one")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main", "refs/tags/v1")
	head := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	root := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD~1"))

	// Reading: the tree with the last commit of each entry, the same for a pinned ref.
	var dir files.Item
	r := vic.get(api + "/files?path=cmd&last=1&ref=" + head)
	r.json(t, &dir)
	if len(dir.Entries) != 1 || dir.Entries[0].Last == nil || dir.Entries[0].Last.Subject != "second" {
		t.Fatalf("tree: %s", r.body)
	}
	if cc := r.header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("a pinned read is not immutable: %q", cc)
	}
	if cc := vic.get(api + "/files?path=cmd").header.Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("a branch read is immutable: %q", cc)
	}

	var br struct {
		Items []struct {
			Name      string `json:"name"`
			Default   bool   `json:"default"`
			Protected bool   `json:"protected"`
			Ahead     int    `json:"ahead"`
		} `json:"items"`
	}
	vic.get(api+"/branches").json(t, &br)
	if len(br.Items) != 1 || br.Items[0].Name != "main" || !br.Items[0].Default {
		t.Fatalf("branches: %+v", br)
	}
	var tags struct {
		Items []files.RefInfo `json:"items"`
	}
	vic.get(api+"/tags").json(t, &tags)
	if len(tags.Items) != 1 || !tags.Items[0].Annotated || tags.Items[0].Message != "version one" || tags.Items[0].Commit != head {
		t.Fatalf("tags: %+v", tags)
	}

	var cm struct {
		Commit files.Commit `json:"commit"`
		Runs   []any        `json:"runs"`
	}
	vic.get(api+"/commits/"+head).json(t, &cm)
	if cm.Commit.Subject != "second" || len(cm.Commit.ParentSHAs) != 1 || cm.Commit.ParentSHAs[0] != root || cm.Runs == nil {
		t.Fatalf("commit: %+v", cm)
	}
	var df struct {
		Files []struct{ Path string } `json:"files"`
	}
	vic.get(api+"/diff?from="+root+"&to="+head).json(t, &df)
	if len(df.Files) != 1 || df.Files[0].Path != "cmd/main.go" {
		t.Fatalf("diff: %+v", df)
	}
	vic.get(api+"/diff?from=root&to="+root).json(t, &df)
	if len(df.Files) != 3 {
		t.Fatalf("root diff: %+v", df)
	}
	var pt struct{ Diff string }
	vic.get(api+"/diff/patch?from="+root+"&to="+head+"&path=cmd/main.go").json(t, &pt)
	if !strings.Contains(pt.Diff, "+func Two() {}") {
		t.Fatalf("patch: %q", pt.Diff)
	}

	var bl files.Blame
	vic.get(api+"/blame?path=cmd/main.go&ref=main").json(t, &bl)
	if bl.Total != 4 || bl.Commits[bl.Lines[3].SHA].Subject != "second" || bl.Commits[bl.Lines[0].SHA].Subject != "first" {
		t.Fatalf("blame: %+v", bl)
	}
	var sr files.SearchResult
	vic.get(api+"/search?q=needle&ext=go").json(t, &sr)
	if sr.Matches != 1 || sr.Files[0].Path != "cmd/main.go" {
		t.Fatalf("search: %+v", sr)
	}
	if r := vic.get(api + "/search?q=(&regex=1"); r.status != 400 {
		t.Errorf("bad regex: %d %s", r.status, r.body)
	}

	// Raw: never inline, never sniffed, never able to run.
	r = vic.get(api + "/raw?path=icon.svg&ref=main")
	h := r.header
	if r.status != 200 || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("raw svg: %d %v", r.status, h)
	}
	r = vic.get(api + "/raw?path=icon.svg&ref=main&inline=1")
	if h := r.header; h.Get("Content-Type") != "image/svg+xml" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("inline picture: %v", h)
	}
	if r := vic.get(api + "/raw?path=cmd&ref=main"); r.status != 404 {
		t.Errorf("raw directory: %d", r.status)
	}
	if r := vic.get(api + "/raw?path=../x&ref=main"); r.status != 400 {
		t.Errorf("raw escape: %d", r.status)
	}

	// Everything above asks for clone permission: a repository the caller cannot see is not there.
	for _, p := range []string{"/branches", "/tags", "/commits/" + head, "/diff?from=root&to=" + head, "/blame?path=a", "/search?q=a", "/raw?path=a", "/files?last=1"} {
		if r := vic.get(e.srv.URL + "/api/v1/repositories/other/secret" + p); r.status != 404 {
			t.Errorf("hidden %s: %d", p, r.status)
		}
		if r := newBrowser(t).get(api + p); r.status != 401 && r.status != 404 {
			t.Errorf("anonymous %s: %d", p, r.status)
		}
	}

	// Branches: a viewer may not make or delete one; an operator may; protection is the push rule.
	if r := vic.do("POST", api+"/branches", `{"name":"x","from":"main"}`); r.status != 403 {
		t.Errorf("a viewer made a branch: %d", r.status)
	}
	if r := otto.do("POST", api+"/branches", `{"name":"feature/a","from":"main"}`); r.status != 201 {
		t.Fatalf("make a branch: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", api+"/branches", `{"name":"feature/a","from":"main"}`); r.status != 409 {
		t.Errorf("twice: %d", r.status)
	}
	if r := otto.do("POST", api+"/branches", `{"name":"--force","from":"main"}`); r.status != 400 {
		t.Errorf("odd name: %d", r.status)
	}
	if r := ada.do("PUT", api+"/protected-branches", `{"patterns":["main","keep/*"]}`); r.status != 200 {
		t.Fatalf("protect: %d", r.status)
	}
	if r := otto.do("DELETE", api+"/branches?name=main", ""); r.status < 400 {
		t.Errorf("deleted the default branch: %d", r.status)
	}
	if r := otto.do("POST", api+"/branches", `{"name":"keep/a","from":"main"}`); r.status < 400 || r.status == 429 {
		t.Errorf("an operator made a protected branch: %d %s", r.status, r.body)
	}
	vic.get(api+"/branches").json(t, &br)
	if len(br.Items) != 2 {
		t.Fatalf("branches: %+v", br)
	}
	r = otto.do("DELETE", api+"/branches?name=feature/a", "")
	if r.status != 200 || !strings.Contains(string(r.body), head) || !strings.Contains(string(r.body), `"undo"`) {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	log, _ := e.eng.AuditLog(engine.AuditFilter{Limit: 100})
	seen := map[string]bool{}
	for _, a := range log {
		if a.Outcome == "ok" && a.User == "otto" && strings.Contains(a.Detail, "feature/a") {
			seen[a.Action] = true
		}
	}
	if !seen["repository.branch_create"] || !seen["repository.branch_delete"] {
		t.Errorf("audit: %v", seen)
	}
}

func TestBrowsingReadsAreRateLimited(t *testing.T) {
	b := &bucket{last: map[string]time.Time{}, n: map[string]float64{}}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		if !b.take("a", 3, 6, now) {
			t.Fatalf("burst %d refused", i)
		}
	}
	if b.take("a", 3, 6, now) {
		t.Fatal("over the burst")
	}
	if !b.take("b", 3, 6, now) {
		t.Fatal("another person shares the bucket")
	}
	if !b.take("a", 3, 6, now.Add(11*time.Second)) {
		t.Fatal("no refill")
	}
}
