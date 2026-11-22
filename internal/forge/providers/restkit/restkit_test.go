package restkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/egress"
)

const testToken = "tok-0123456789-not-a-real-credential"

func client(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	g, err := egress.New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	c := NewClient(Endpoints{API: u, Hosts: map[string]bool{u.Host: true}}, g,
		func() (string, error) { return testToken, nil },
		func(h http.Header, tok string) { h.Set("Authorization", "Bearer "+tok) })
	return c
}

// A redirect is never followed, so the credential cannot reach the host it names.
func TestARedirectIsNotFollowedAndCarriesNoCredential(t *testing.T) {
	var other atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the credential reached the second host")
		}
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/x", http.StatusFound)
	}))
	defer srv.Close()
	_, err := client(t, srv).Get(context.Background(), "/a", nil)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v", err)
	}
	if other.Load() != 0 {
		t.Errorf("the second host was called %d times", other.Load())
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("the error holds the credential")
	}
}

// An address taken from an answer that names another host is refused before anything is sent.
func TestAnAddressOnAnotherHostIsRefused(t *testing.T) {
	var hits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := client(t, srv).Do(context.Background(), http.MethodGet, elsewhere.URL+"/next", nil, 0, "")
	if err == nil || hits.Load() != 0 {
		t.Fatalf("err = %v, hits = %d", err, hits.Load())
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("the error holds the credential")
	}
}

// The host's words are returned without the credential even when it echoes it.
func TestAnErrorBodyThatEchoesTheCredentialIsScrubbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad ` + testToken + ` at https://u:p@h/x?token=abc"}`))
	}))
	defer srv.Close()
	c := client(t, srv)
	c.Message = func(b []byte) string { return string(b) }
	_, err := c.Get(context.Background(), "/a", nil)
	if err == nil {
		t.Fatal("no error")
	}
	for _, leak := range []string{testToken, "u:p@", "token=abc"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error holds %q: %v", leak, err)
		}
	}
}

// An answer larger than the cap is refused whole, not cut and then misread.
func TestAnOversizeAnswerIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"a":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", MaxAnswer)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()
	var out map[string]string
	if _, err := client(t, srv).Get(context.Background(), "/a", &out); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("err = %v", err)
	}
}

// JSON followed by anything else is not accepted.
func TestTrailingGarbageAfterJSONIsRefused(t *testing.T) {
	for _, body := range []string{`{"a":"b"} {"a":"c"}`, `{"a":"b"}x`, `{"a":"b"}]`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		var out map[string]string
		if _, err := client(t, srv).Get(context.Background(), "/a", &out); err == nil {
			t.Errorf("%q is accepted", body)
		}
		srv.Close()
	}
}
