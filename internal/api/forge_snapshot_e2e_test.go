package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
)

// A snapshot of a ref is had as a tar.gz or a zip by whoever may read the repository (and nobody else), under a
// folder named for the repository and the ref, is refused before its first byte when it is bigger than the setting
// allows (and the setting is an administrator's), is in the Logbook, and says in plain words what is wrong with a ref or
// a format that is not there.
func TestDownloadASnapshotOfARef(t *testing.T) {
	e, _ := hostedEnvWith(t, envOpts{}, hosting.Options{}, githttp.Options{})
	hostedRepo(t, e, "acme/shop")
	ada, vic, otto := e.signIn(t, "ada"), e.signIn(t, "vic"), e.signIn(t, "otto")
	push := withToken(e.srv.URL, "/git/acme/shop.git", e.tokenFor(t, "ada", "git"))
	src := workingCopy(t)
	commit(t, src, "docs/guide.md", "# guide\n", "docs")
	mustGit(t, src, "branch", "-M", "main")
	mustGit(t, src, "tag", "v1.0")
	mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/main", "refs/tags/v1.0")
	api := e.srv.URL + "/api/v1/repositories/acme/shop/download"

	names := func(format string, body []byte) map[string]string {
		t.Helper()
		out := map[string]string{}
		if format == "zip" {
			zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range zr.File {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				out[f.Name] = string(b)
			}
			return out
		}
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(tr)
			out[h.Name] = string(b)
		}
	}
	// A viewer reads it; the folder is the repository and the ref.
	got := vic.get(api + "?ref=v1.0")
	if got.status != 200 || got.header.Get("Content-Type") != "application/gzip" || !strings.Contains(got.header.Get("Content-Disposition"), `shop-v1.0.tar.gz`) || got.header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("tar.gz: %d %v", got.status, got.header)
	}
	if m := names("tar.gz", got.body); m["shop-v1.0/a.txt"] != "one\n" || m["shop-v1.0/docs/guide.md"] != "# guide\n" {
		t.Fatalf("tar.gz content: %v", m)
	}
	z := ada.get(api + "?format=zip") // the default branch
	if z.status != 200 || z.header.Get("Content-Type") != "application/zip" {
		t.Fatalf("zip: %d %v", z.status, z.header)
	}
	if m := names("zip", z.body); m["shop-main/docs/guide.md"] != "# guide\n" {
		t.Fatalf("zip content: %v", m)
	}
	// Nobody signed in, and nobody who may not read it.
	if got := newBrowser(t).get(api); got.status != 401 {
		t.Fatalf("unsigned: %d", got.status)
	}
	// Plain refusals.
	if got := vic.get(api + "?ref=nope"); got.status != 404 || !strings.Contains(string(got.body), "no branch, tag or commit") {
		t.Fatalf("a ref that is not there: %d %s", got.status, got.body)
	}
	if got := vic.get(api + "?format=rar"); got.status != 400 || !strings.Contains(string(got.body), "tar.gz or zip") {
		t.Fatalf("a format that is not there: %d %s", got.status, got.body)
	}
	// The limit is a setting an administrator changes, and the refusal comes before any byte.
	if r := otto.do("PUT", e.srv.URL+"/api/v1/hosting-settings", `{"archive_max_mib":1}`); r.status != 403 {
		t.Fatalf("an operator changed the limit: %d", r.status)
	}
	if r := ada.do("PUT", e.srv.URL+"/api/v1/hosting-settings", `{"archive_max_mib":0}`); r.status != 400 {
		t.Fatalf("a limit of nothing: %d", r.status)
	}
	if r := ada.do("PUT", e.srv.URL+"/api/v1/hosting-settings", `{"archive_max_mib":1}`); r.status != 200 {
		t.Fatalf("set the limit: %d %s", r.status, r.body)
	}
	commit(t, src, "big.bin", strings.Repeat("0123456789abcdef", 1<<17)+"\n", "a file over a MiB") // 2 MiB
	mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/main")
	big := vic.get(api)
	if big.status != 413 || !strings.Contains(string(big.body), "more than 1 MiB") || big.header.Get("Content-Disposition") != "" {
		t.Fatalf("a snapshot over the limit: %d %s", big.status, big.body)
	}
	if ok := vic.get(api + "?ref=v1.0"); ok.status != 200 { // the older tag is small
		t.Fatalf("the small ref: %d", ok.status)
	}
	var settings struct {
		ArchiveMaxMiB      int `json:"archive_max_mib"`
		TrashRetentionDays int `json:"trash_retention_days"`
	}
	vic.get(e.srv.URL+"/api/v1/hosting-settings").json(t, &settings)
	if settings.ArchiveMaxMiB != 1 || settings.TrashRetentionDays != 30 {
		t.Fatalf("settings: %+v", settings)
	}
	var snapshots, changes int
	for _, a := range e.auditEntries(t) {
		if a.Action == "repository.archive" && a.Outcome == "ok" && a.User == "vic" && strings.Contains(a.Detail, "v1.0") {
			snapshots++
		}
		if a.Action == "hosting.settings" && strings.Contains(a.Detail, "1 MiB") {
			changes++
		}
	}
	if snapshots == 0 || changes == 0 {
		t.Errorf("audit: %d snapshots, %d changes of the setting", snapshots, changes)
	}
}
