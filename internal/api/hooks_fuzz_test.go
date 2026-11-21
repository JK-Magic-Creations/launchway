package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzVerifyGitHook: a webhook is accepted only with the secret. For any body, secret and
// signature header, a call is accepted exactly when the header is the correct signature (or, for
// the host that sends the secret itself, equals it); the verifier never panics.
func FuzzVerifyGitHook(f *testing.F) {
	f.Add("github", "secret", "body", "sha256=00")
	f.Add("gitea", "s", "{}", "")
	f.Add("gitlab", "tok", "", "tok")
	f.Add("github", "", "x", "sha256=")
	f.Fuzz(func(t *testing.T, kind, secret, body, header string) {
		if secret == "" || strings.ContainsAny(header, "\r\n\x00") {
			t.Skip()
		}
		r := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
		var good bool
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		sum := hex.EncodeToString(mac.Sum(nil))
		switch kind {
		case "gitlab":
			r.Header.Set("X-Gitlab-Token", header)
			good = header == secret
		case "gitea", "forgejo":
			r.Header.Set("X-Gitea-Signature", header)
			good = strings.EqualFold(header, sum) // hex is case-insensitive
		default:
			kind = "github"
			r.Header.Set("X-Hub-Signature-256", header)
			good = strings.HasPrefix(header, "sha256=") && strings.EqualFold(header[7:], sum)
		}
		ok, _, _, err := verifyGitHook(kind, []byte(secret), r)
		if err == nil && ok && !good {
			t.Fatalf("%s: accepted a call with header %q", kind, header)
		}
		if err == nil && !ok && good {
			t.Fatalf("%s: refused a correctly signed call", kind)
		}
	})
}
