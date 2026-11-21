package webhooks

import (
	"strings"
	"testing"
)

func TestMaskURLShowsTheHostAndElidesThePath(t *testing.T) {
	for in, want := range map[string]string{
		"https://hooks.chat.example/services/T000/B000/tokenvalue": "https://hooks.chat.example/...",
		"https://ci.example.com:8443/hook?token=abc":               "https://ci.example.com:8443/...",
		"https://ci.example.com":                                   "https://ci.example.com",
		"not an address":                                           "",
	} {
		if got := MaskURL(in); got != want {
			t.Errorf("MaskURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResponseTextIsRedactedBeforeItIsStored(t *testing.T) {
	addr := "https://hooks.chat.example/services/T000/B000/s3cretpathtoken"
	got := redactFor(addr, `Post "`+addr+`": dial failed; echo /services/T000/B000/s3cretpathtoken; Authorization: Bearer abc123def456; Cookie: sid=zzz999yyy`)
	for _, leak := range []string{"s3cretpathtoken", "abc123def456", "zzz999yyy"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q survived: %s", leak, got)
		}
	}
	if !strings.Contains(got, "hooks.chat.example") {
		t.Errorf("the host should stay: %s", got)
	}
}
