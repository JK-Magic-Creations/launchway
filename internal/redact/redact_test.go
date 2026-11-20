package redact

import "testing"

func TestURLs(t *testing.T) {
	for in, want := range map[string]string{
		"https://git.example/a/b.git":                         "https://git.example/a/b.git",
		"https://bot:s3cret@git.example/a/b.git":              "https://***@git.example/a/b.git",
		"fetching https://tok@h/r: fatal: not found":          "fetching https://***@h/r: fatal: not found",
		"ssh://git@host/r and http://u:p@x/y":                 "ssh://***@host/r and http://***@x/y",
		"/var/data/repo":                                      "/var/data/repo",
		"mail someone@example.com about https://example.com/": "mail someone@example.com about https://example.com/",
	} {
		if got := URLs(in); got != want {
			t.Errorf("URLs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestURLsHideTokenQueryParameters(t *testing.T) {
	if got := URLs("GET https://h/r.git?token=abc&x=1 failed"); got != "GET https://h/r.git?token=***&x=1 failed" {
		t.Errorf("%q", got)
	}
	if !SecretQueryName("Access_Token") || SecretQueryName("ref") {
		t.Error("SecretQueryName")
	}
}
