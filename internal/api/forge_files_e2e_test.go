package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/files"
)

func TestCommitsAndFilesThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, otto, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	hostedRepo(t, e, "other/secret")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	api := e.srv.URL + "/api/v1/repositories/acme/shop"

	src := t.TempDir()
	gitAt(t, src, "2026-06-01T10:00:00Z", "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(src, "security", "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "security", "COMPLIANCE.md"), []byte("# Compliance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "security", "evidence", "2026-11.md"), []byte("evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, "2026-06-01T10:00:00Z", "add", "-A")
	gitAt(t, src, "2026-06-01T10:00:00Z", "commit", "-q", "-m", "deploy(finances): 20261114051003-2e5429c8")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main")

	// Readers read.
	var log struct {
		Items []files.Commit `json:"items"`
	}
	vic.get(api+"/commits?ref=main&limit=5").json(t, &log)
	if len(log.Items) != 1 || log.Items[0].Subject != "deploy(finances): 20261114051003-2e5429c8" || len(log.Items[0].SHA) != 40 {
		t.Fatalf("commits: %+v", log)
	}
	var dir, file files.Item
	vic.get(api+"/files?path=security").json(t, &dir)
	if dir.Type != "dir" || len(dir.Entries) != 2 {
		t.Fatalf("dir: %+v", dir)
	}
	vic.get(api+"/files?path=security/COMPLIANCE.md&ref=main").json(t, &file)
	if file.Type != "file" || file.Content != "# Compliance\n" || file.Encoding != "utf8" {
		t.Fatalf("file: %+v", file)
	}
	for path, want := range map[string]int{"/files?path=nope.md": 404, "/files?path=../x": 400, "/files?path=a&ref=-x": 400, "/commits?ref=nope": 404} {
		if r := vic.get(api + path); r.status != want {
			t.Errorf("%s: %d %s", path, r.status, r.body)
		}
	}
	if r := vic.get(e.srv.URL + "/api/v1/repositories/other/secret/files"); r.status != 404 {
		t.Errorf("a hidden repository: %d", r.status)
	}

	// Writing is for operators; the body is checked; a conflict is a 409 with the next step.
	body := func(path, content, extra string) string {
		return `{"branch":"main","path":"` + path + `","content":"` + content + `","message":"file it"` + extra + `}`
	}
	if r := vic.do("PUT", api+"/files", body("openspec/changes/x/spec.md", "hi", "")); r.status != 403 {
		t.Errorf("a viewer wrote: %d %s", r.status, r.body)
	}
	var out files.Written
	r := otto.do("PUT", api+"/files", body("openspec/changes/x/spec.md", "hi", ""))
	r.json(t, &out)
	if r.status != 200 || len(out.Commit) != 40 || out.Branch != "main" {
		t.Fatalf("write: %d %s", r.status, r.body)
	}
	if r := otto.do("PUT", api+"/files", body("openspec/changes/x/spec.md", "again", "")); r.status != 409 || !strings.Contains(string(r.body), `"next"`) {
		t.Errorf("a blind overwrite: %d %s", r.status, r.body)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte{0, 1, 2})
	if r := otto.do("PUT", api+"/files", body("bin/x.dat", b64, `,"encoding":"base64"`)); r.status != 200 {
		t.Errorf("a binary file: %d %s", r.status, r.body)
	}
	if r := otto.do("PUT", api+"/files", body("x", "!!!", `,"encoding":"base64"`)); r.status != 400 {
		t.Errorf("bad base64: %d", r.status)
	}
	if r := otto.do("PUT", api+"/files", `{"branch":"main","path":"y","content":"x","message":""}`); r.status != 400 {
		t.Errorf("no message: %d", r.status)
	}
	var got files.Item
	vic.get(api+"/files?path=openspec/changes/x/spec.md").json(t, &got)
	if got.Content != "hi" {
		t.Errorf("read back: %+v", got)
	}

	// A protected branch takes this only from the repository's administrators, as a push would.
	if r := ada.do("PUT", api+"/protected-branches", `{"patterns":["main"]}`); r.status != 200 {
		t.Fatalf("protect: %d %s", r.status, r.body)
	}
	if r := otto.do("PUT", api+"/files", body("z.md", "z", "")); r.status < 400 || r.status == 409 && !strings.Contains(string(r.body), "protected") {
		t.Errorf("an operator wrote to a protected branch: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", api+"/files", body("z.md", "z", "")); r.status != 200 {
		t.Errorf("an administrator on a protected branch: %d %s", r.status, r.body)
	}
	// The write is audited and is a commit that clones see.
	log2, _ := e.eng.AuditLog(engine.AuditFilter{Limit: 100})
	seen := false
	for _, a := range log2 {
		seen = seen || (a.Action == "repository.file_write" && a.Outcome == "ok" && a.User == "otto" && a.Target == "acme/shop" && strings.Contains(a.Detail, "openspec/changes/x/spec.md"))
	}
	if !seen {
		t.Errorf("audit: %+v", log2)
	}
	clone := t.TempDir()
	mustGit(t, clone, "clone", "-q", url, "c")
	if b, err := os.ReadFile(filepath.Join(clone, "c", "openspec", "changes", "x", "spec.md")); err != nil || string(b) != "hi" {
		t.Errorf("clone: %v %q", err, b)
	}
}
