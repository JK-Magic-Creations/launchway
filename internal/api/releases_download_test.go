package api

import (
	"strings"
	"testing"
)

func TestAssetContentType(t *testing.T) {
	for name, want := range map[string]string{
		"hub-app.apk": "application/vnd.android.package-archive", "HUB.APK": "application/vnd.android.package-archive",
		"a.tar.gz": "application/gzip", "a.zip": "application/zip", "SHA256SUMS": "text/plain; charset=utf-8",
		"page.html": "application/octet-stream", "script.js": "application/octet-stream", "noext": "application/octet-stream",
	} {
		if got := assetContentType(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

func TestFirstParagraphDropsHeadings(t *testing.T) {
	got := firstParagraph("## 1.0.0\n\n### Features\n\n- one\n- two\n\n\n\n### Fixes\n\n- three\n")
	if strings.Contains(got, "#") || !strings.Contains(got, "one") || !strings.Contains(got, "three") {
		t.Fatalf("%q", got)
	}
}
