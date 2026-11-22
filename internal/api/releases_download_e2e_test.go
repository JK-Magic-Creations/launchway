package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// A phone scans a code, is not signed in, and must be told to sign in (a 401 that says so, never a redirect
// to a page of HTML), and a signed-in reader gets the file named and typed so the phone offers to install it.
func TestReleaseFileDownloadForAPhone(t *testing.T) {
	e := setupShip(t)
	e.post(t, e.otto, "/releases", map[string]any{"version": "1.1.0", "generate_notes": true}, 201, nil)
	if got := e.otto.do("PUT", e.url("/releases/1.1.0/assets/hub-app.apk"), "0123456789"); got.status != 201 {
		t.Fatalf("upload: %d %s", got.status, got.body)
	}
	e.post(t, e.otto, "/releases/1.1.0/publish", map[string]any{}, 200, nil)
	file := e.url("/releases/1.1.0/assets/hub-app.apk")

	// Nobody signed in: 401 with a JSON body, even for a browser that asks for HTML, and no redirect.
	for _, accept := range []string{"text/html,application/xhtml+xml", "*/*"} {
		got := newBrowser(t).get(file, "Accept", accept)
		if got.status != http.StatusUnauthorized || got.header.Get("Location") != "" || !strings.Contains(got.header.Get("Content-Type"), "json") {
			t.Fatalf("unsigned (Accept %s): %d location %q type %q", accept, got.status, got.header.Get("Location"), got.header.Get("Content-Type"))
		}
	}

	// A signed-in reader: the type an Android package has, saved under its own name, never shown as a page.
	got := e.vic.get(file)
	h := got.header
	if got.status != 200 || string(got.body) != "0123456789" || h.Get("Content-Type") != "application/vnd.android.package-archive" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="hub-app.apk"`) || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Accept-Ranges") != "bytes" || h.Get("Content-Length") != "10" {
		t.Fatalf("download: %d %q %v", got.status, got.body, h)
	}
	// A resumed download answers the range asked for, and says so.
	part := e.vic.get(file, "Range", "bytes=4-6")
	if part.status != http.StatusPartialContent || string(part.body) != "456" || part.header.Get("Content-Range") != "bytes 4-6/10" {
		t.Fatalf("range: %d %q %v", part.status, part.body, part.header)
	}

	// Every download is in the Logbook, with the person and the file.
	log, err := e.eng.AuditLog(engine.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var plain, continued int
	for _, a := range log {
		if a.Action == "release.asset.download" && a.User == "vic" && strings.Contains(a.Detail, "hub-app.apk") {
			if strings.Contains(a.Detail, "continued") {
				continued++
			} else {
				plain++
			}
		}
	}
	if plain != 1 || continued != 1 {
		t.Fatalf("audit lines: %d plain, %d continued, in %d entries", plain, continued, len(log))
	}
}
