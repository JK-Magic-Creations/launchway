package source

import "testing"

func TestSkipMarker(t *testing.T) {
	for subject, want := range map[string]string{
		"docs only [skip ci]":        "[skip ci]",
		"Fix typo [CI SKIP]":         "[ci skip]",
		"[no ci] wip":                "[no ci]",
		"release [skip launchway]":   "[skip launchway]",
		"skip ci in prose":           "",
		"a [skip] marker is not one": "",
		"":                           "",
	} {
		if got := skipMarker(subject); got != want {
			t.Errorf("%q: %q, want %q", subject, got, want)
		}
	}
}
