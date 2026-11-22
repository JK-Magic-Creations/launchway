package fakeidp

import (
	"net/http"
	"net/url"
	"testing"
)

func TestAuthorizeRefusesNonWebRedirects(t *testing.T) {
	p := New()
	defer p.Close()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, bad := range []string{"//evil.example/x", "javascript:alert(1)", "/relative", "ftp://h/x"} {
		q := url.Values{"client_id": {p.ClientID}, "redirect_uri": {bad}, "response_type": {"code"}, "scope": {"openid"},
			"code_challenge_method": {"S256"}, "code_challenge": {"c"}, "state": {"s"}, "nonce": {"n"}}
		resp, err := c.Get(p.URL + "/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", bad, resp.StatusCode)
		}
	}
}
