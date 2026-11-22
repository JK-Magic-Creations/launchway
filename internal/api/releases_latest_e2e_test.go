package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// "latest" names the newest published release that is not a pre-release, for the page and for each file by name,
// signed in and (when all three switches say yes) public; the list marks which is the latest and which are
// pre-releases; each file counts the downloads that completed: a whole file once, a broken download's rest once, a
// HEAD, a part in the middle and a refusal not at all.
func TestLatestReleaseAndDownloadCounts(t *testing.T) {
	e := setupShip(t)
	cut := func(version, body string, public bool) {
		t.Helper()
		e.post(t, e.otto, "/releases", map[string]any{"version": version, "from": "main"}, 201, nil)
		if got := e.otto.do("PUT", e.url("/releases/"+version+"/assets/a.bin"), body); got.status != 201 {
			t.Fatalf("upload: %d %s", got.status, got.body)
		}
		who := e.otto
		if public {
			who = e.ada // only an administrator makes a release public
		}
		e.post(t, who, "/releases/"+version+"/publish", map[string]any{"public": public}, 200, nil)
	}
	if got := e.vic.get(e.url("/releases/latest")); got.status != 404 {
		t.Fatalf("latest of a project with no release: %d %s", got.status, got.body)
	}
	cut("2.0.0", "ten bytes!", false)
	cut("2.1.0-rc.1", "release candidate", false) // a pre-release is never the latest
	var latest struct{ Release engine.Release }
	e.vic.get(e.url("/releases/latest")).json(t, &latest)
	if latest.Release.Version != "2.0.0" || !latest.Release.Latest {
		t.Fatalf("latest with a pre-release newer: %+v", latest.Release)
	}
	cut("2.1.0", "eleven byte", false)
	cut("2.2.0", "twelve bytes", false)
	e.post(t, e.ada, "/releases/2.2.0/yank", map[string]any{"reason": "oops"}, 200, nil) // a withdrawn release is never it
	e.vic.get(e.url("/releases/latest")).json(t, &latest)
	if latest.Release.Version != "2.1.0" {
		t.Fatalf("latest after yanking 2.2.0: %s", latest.Release.Version)
	}

	// The list marks the latest and the pre-releases.
	var list []engine.Release
	e.vic.get(e.url("/releases")).json(t, &list)
	marks := map[string]string{}
	for _, r := range list {
		marks[r.Version] = map[bool]string{true: "latest", false: ""}[r.Latest] + map[bool]string{true: " pre", false: ""}[r.Prerelease]
	}
	if marks["2.1.0"] != "latest" || marks["2.0.0"] != "" || marks["2.1.0-rc.1"] != " pre" || marks["2.2.0"] != "" {
		t.Fatalf("marks: %v", marks)
	}

	// A file by name, signed in; nobody else.
	file := e.url("/releases/latest/assets/a.bin")
	if got := newBrowser(t).get(file); got.status != http.StatusUnauthorized {
		t.Fatalf("an unsigned download: %d", got.status)
	}
	counts := func() int64 {
		t.Helper()
		var v struct{ Release engine.Release }
		e.vic.get(e.url("/releases/2.1.0")).json(t, &v)
		for _, a := range v.Release.Assets {
			if a.Name == "a.bin" {
				return a.Downloads
			}
		}
		t.Fatal("no a.bin")
		return -1
	}
	if got := e.vic.get(file); got.status != 200 || string(got.body) != "eleven byte" {
		t.Fatalf("latest file: %d %q", got.status, got.body)
	}
	if n := counts(); n != 1 {
		t.Fatalf("after a whole download: %d", n)
	}
	e.vic.get(file, "Range", "bytes=0-3")   // a part in the middle: not a download
	e.vic.get(file, "Range", "bytes=20-30") // out of range: refused
	if n := counts(); n != 1 {
		t.Fatalf("after parts: %d", n)
	}
	if got := e.vic.get(file, "Range", "bytes=4-"); got.status != http.StatusPartialContent || string(got.body) != "en byte" {
		t.Fatalf("the rest: %d %q", got.status, got.body)
	}
	if n := counts(); n != 2 {
		t.Fatalf("after the rest of a broken download: %d (once, not twice)", n)
	}
	var viaVersion struct{ Release engine.Release }
	e.vic.get(e.url("/releases/2.0.0")).json(t, &viaVersion)
	if viaVersion.Release.Assets[0].Downloads != 0 {
		t.Fatalf("another release's file counted: %+v", viaVersion.Release.Assets)
	}

	// Public: three switches, the release's own, and only the newest.
	pub := func(path string) response { return newBrowser(t).get(e.srv.URL + "/public/releases/pay-ship/" + path) }
	if got := pub("latest"); got.status != 404 {
		t.Fatalf("public page with the switches off: %d", got.status)
	}
	e.ada.do("PUT", e.srv.URL+"/api/v1/release-installation", `{"public_downloads":true}`)
	e.ada.do("PUT", e.url("/release-settings"), `{"scheme":"semver","tag_prefix":"v","changelog":"CHANGELOG.md","public":true}`)
	if got := pub("latest"); got.status != 404 {
		t.Fatalf("the latest release did not ask to be public: %d", got.status)
	}
	cut("2.3.0", "public bytes", true)
	got := pub("latest")
	var page struct {
		Version string
		Latest  bool
		Files   []struct {
			Name      string
			Downloads int64
			URL       string
		}
	}
	got.json(t, &page)
	if got.status != 200 || page.Version != "2.3.0" || !page.Latest || len(page.Files) != 1 || page.Files[0].Name != "a.bin" || !strings.HasSuffix(page.Files[0].URL, "/public/releases/pay-ship/2.3.0/a.bin") {
		t.Fatalf("public page: %d %s", got.status, got.body)
	}
	if strings.Contains(string(got.body), "created_by") || strings.Contains(string(got.body), "run_id") {
		t.Fatalf("the public page tells more than the release: %s", got.body)
	}
	if f := pub("latest/a.bin"); f.status != 200 || string(f.body) != "public bytes" {
		t.Fatalf("public latest file: %d %q", f.status, f.body)
	}
	if f := pub("latest/nope.bin"); f.status != 404 {
		t.Fatalf("a file the release does not have: %d", f.status)
	}
	if p := pub("2.1.0"); p.status != 404 { // an older release that was not made public stays private
		t.Fatalf("an older private release: %d", p.status)
	}
	e.vic.get(e.url("/releases/2.3.0")).json(t, &viaVersion)
	if viaVersion.Release.Assets[0].Downloads != 1 {
		t.Fatalf("the public download was not counted: %+v", viaVersion.Release.Assets)
	}
	// Withdrawing the newest hands "latest" back, and the public page with it (2.1.0 is not public: not found).
	e.post(t, e.ada, "/releases/2.3.0/yank", map[string]any{"reason": "oops"}, 200, nil)
	if got := pub("latest"); got.status != 404 {
		t.Fatalf("latest after the public release was withdrawn: %d", got.status)
	}
}
