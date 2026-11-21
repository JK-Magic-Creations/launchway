package webhooks

import (
	"strings"
	"testing"
)

func TestSignMatchesAKnownVector(t *testing.T) {
	// The documented example of HMAC-SHA256 as webhooks use it.
	got := Sign("It's a Secret to Everybody", []byte("Hello, World!"))
	if got != "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17" {
		t.Errorf("signature %s", got)
	}
}

func TestNormaliseAndCheckURL(t *testing.T) {
	if ev, msg := Normalise([]string{"push", "create", "push"}); msg != "" || strings.Join(ev, ",") != "create,push" {
		t.Errorf("%v %q", ev, msg)
	}
	if _, msg := Normalise([]string{"push", "nope"}); !strings.Contains(msg, "unknown event nope") {
		t.Errorf("%q", msg)
	}
	if _, msg := Normalise(nil); !strings.Contains(msg, "at least one event") {
		t.Errorf("%q", msg)
	}
	for _, bad := range []string{"", "ftp://x/", "https://u:p@x/", "https:///x", "x"} {
		if CheckURL(bad) == "" {
			t.Errorf("%q was accepted", bad)
		}
	}
	if CheckURL("https://ci.example.com/hook") != "" || CheckURL("http://10.0.0.5:8080/x") != "" {
		t.Error("a good address was refused")
	}
	h := Hook{Events: []string{"push"}}
	if !h.Wants("push") || h.Wants("create") || !h.Wants(EventPing) {
		t.Error("Wants")
	}
}
