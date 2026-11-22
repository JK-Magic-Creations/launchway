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

func TestTagsAndBranchRenameThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, otto, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	api := e.srv.URL + "/api/v1/repositories/acme/shop"
	src := t.TempDir()
	gitAt(t, src, "2026-06-01T10:00:00Z", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, "2026-06-01T10:00:00Z", "add", "-A")
	gitAt(t, src, "2026-06-01T10:00:00Z", "commit", "-q", "-m", "first")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main")
	head := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))

	if r := vic.do("POST", api+"/tags", `{"name":"v1","from":"main"}`); r.status != 403 {
		t.Errorf("a viewer made a tag: %d", r.status)
	}
	if r := otto.do("POST", api+"/tags", `{"name":"v1","from":"main"}`); r.status != 201 || !strings.Contains(string(r.body), head) {
		t.Fatalf("make a tag: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", api+"/tags", `{"name":"v1","from":"main"}`); r.status != 409 {
		t.Errorf("a tag was moved: %d", r.status)
	}
	if r := otto.do("POST", api+"/tags", `{"name":"--force","from":"main"}`); r.status != 400 {
		t.Errorf("odd tag name: %d", r.status)
	}
	var tags struct{ Items []struct{ Name string } }
	vic.get(api+"/tags").json(t, &tags)
	if len(tags.Items) != 1 || tags.Items[0].Name != "v1" {
		t.Fatalf("tags: %+v", tags)
	}
	if r := ada.do("PUT", api+"/protected-branches", `{"patterns":["main","keep/*"]}`); r.status != 200 {
		t.Fatalf("protect: %d", r.status)
	}
	if r := otto.do("DELETE", api+"/tags?name=nope", ""); r.status != 404 {
		t.Errorf("deleted a tag that is not there: %d", r.status)
	}
	if r := otto.do("DELETE", api+"/tags?name=v1", ""); r.status != 200 || !strings.Contains(string(r.body), `"undo"`) {
		t.Fatalf("delete tag: %d %s", r.status, r.body)
	}

	// Rename: never the default branch, never onto a name that exists, protection is the push rule.
	if r := otto.do("POST", api+"/branches", `{"name":"feature/a","from":"main"}`); r.status != 201 {
		t.Fatalf("make a branch: %d %s", r.status, r.body)
	}
	if r := vic.do("POST", api+"/branches/rename", `{"name":"feature/a","new_name":"feature/b"}`); r.status != 403 {
		t.Errorf("a viewer renamed: %d", r.status)
	}
	if r := otto.do("POST", api+"/branches/rename", `{"name":"main","new_name":"trunk"}`); r.status != 409 {
		t.Errorf("renamed the default branch: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", api+"/branches/rename", `{"name":"feature/a","new_name":"main"}`); r.status != 409 {
		t.Errorf("renamed onto an existing branch: %d", r.status)
	}
	if r := otto.do("POST", api+"/branches/rename", `{"name":"feature/a","new_name":"keep/a"}`); r.status < 400 || r.status == 429 {
		t.Errorf("renamed into a protected name: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", api+"/branches/rename", `{"name":"feature/a","new_name":"feature/b"}`); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	var br struct{ Items []struct{ Name string } }
	vic.get(api+"/branches").json(t, &br)
	names := []string{}
	for _, b := range br.Items {
		names = append(names, b.Name)
	}
	if len(names) != 2 || names[0] == "feature/a" && names[1] == "feature/a" || strings.Join(names, ",") == "main,feature/a" {
		t.Errorf("branches after the rename: %v", names)
	}
	log, _ := e.eng.AuditLog(engine.AuditFilter{Limit: 100})
	seen := map[string]bool{}
	for _, a := range log {
		if a.Outcome == "ok" && a.User == "otto" {
			seen[a.Action] = true
		}
	}
	for _, want := range []string{"repository.tag_create", "repository.tag_delete", "repository.branch_rename"} {
		if !seen[want] {
			t.Errorf("no audit event %s: %v", want, seen)
		}
	}
}

func TestUploadingFilesThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, otto, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	api := e.srv.URL + "/api/v1/repositories/acme/shop"
	src := t.TempDir()
	gitAt(t, src, "2026-06-01T10:00:00Z", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, "2026-06-01T10:00:00Z", "add", "-A")
	gitAt(t, src, "2026-06-01T10:00:00Z", "commit", "-q", "-m", "first")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main")

	body := `{"branch":"main","message":"Add two","files":[{"path":"docs/x.txt","content":"eA=="},{"path":"y.txt","content":"y","encoding":"utf8"}]}`
	if r := vic.do("POST", api+"/files", body); r.status != 403 {
		t.Errorf("a viewer uploaded: %d", r.status)
	}
	if r := ada.do("PUT", api+"/protected-branches", `{"patterns":["main"]}`); r.status != 200 {
		t.Fatalf("protect: %d", r.status)
	}
	if r := otto.do("POST", api+"/files", body); r.status < 400 || r.status == 429 || !strings.Contains(string(r.body), "main") {
		t.Errorf("an operator uploaded to a protected branch: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", api+"/files", body); r.status != 201 {
		t.Fatalf("upload: %d %s", r.status, r.body)
	}
	var it files.Item
	vic.get(api+"/files?path=docs/x.txt").json(t, &it)
	if it.Content != "x" {
		t.Errorf("uploaded content: %+v", it)
	}
	if r := ada.do("POST", api+"/files", body); r.status != 409 || !strings.Contains(string(r.body), "nothing was uploaded") {
		t.Errorf("an existing file was overwritten: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", api+"/files", strings.Replace(body, `"files"`, `"replace":true,"files"`, 1)); r.status != 201 {
		t.Errorf("replace: %d %s", r.status, r.body)
	}
	for _, bad := range []string{`{"message":"m","files":[]}`, `{"message":"","files":[{"path":"q","content":"eA=="}]}`, `{"message":"m","files":[{"path":"../q","content":"eA=="}]}`,
		`{"message":"m","files":[{"path":"q","content":"eA=="},{"path":"q","content":"eA=="}]}`, `{"message":"m","files":[{"path":"q","content":"!!"}]}`} {
		if r := ada.do("POST", api+"/files", bad); r.status != 400 {
			t.Errorf("%s: %d %s", bad, r.status, r.body)
		}
	}
	log, _ := e.eng.AuditLog(engine.AuditFilter{Limit: 100})
	ok := false
	for _, a := range log {
		ok = ok || a.Action == "repository.files_upload" && a.Outcome == "ok" && strings.Contains(a.Detail, "docs/x.txt")
	}
	if !ok {
		t.Error("no audit event for the upload")
	}
}

// An Inspection that cannot start says why in a sentence, to everyone who may read the repository, and is audited.
func TestInspectingARepositoryExplainsWhenItCannot(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	otto, vic := e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	api := e.srv.URL + "/api/v1/repositories/acme/shop/inspections"
	var st struct {
		Available bool
		Reason    string
		Runs      []any
	}
	vic.get(api).json(t, &st)
	if st.Available || !strings.Contains(st.Reason, "Standing orders are not available") || st.Runs == nil {
		t.Errorf("state: %+v", st)
	}
	if r := vic.do("POST", api, `{}`); r.status != 403 {
		t.Errorf("a viewer started an Inspection: %d", r.status)
	}
	if r := otto.do("POST", api, `{}`); r.status != 409 || !strings.Contains(string(r.body), "Standing orders are not available") {
		t.Errorf("refusal: %d %s", r.status, r.body)
	}
	if r := newBrowser(t).get(api); r.status != 401 && r.status != 404 {
		t.Errorf("anonymous: %d", r.status)
	}
}
