package source

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func checkSources(t *testing.T, cfg Config, lookup Lookup) *Sources {
	t.Helper()
	if cfg.Projects == nil {
		cfg.Projects = []string{"shop"}
	}
	s, err := Open(map[string]Config{"checks": cfg}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func healthCheck(url string) *pipeline.HealthCheck {
	return &pipeline.HealthCheck{Kind: "http", URL: url, Method: "GET", Success: 1,
		Timeout: pipeline.Duration(2 * time.Second), Every: pipeline.Duration(150 * time.Millisecond)}
}

func runCheck(t *testing.T, s *Sources, c *pipeline.HealthCheck) (CheckResult, error) {
	t.Helper()
	now := time.Now()
	return s.Check(context.Background(), "checks", "shop", c, now, now.Add(700*time.Millisecond))
}

func TestCheckPassesOnHealthyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }))
	defer srv.Close()
	s := checkSources(t, Config{Type: Check, Hosts: []string{"127.0.0.1"}}, nil)
	c := healthCheck(srv.URL + "/healthz")
	c.BodyContains, c.JSONPath, c.JSONEquals = "ok", "status", "ok"
	res, err := runCheck(t, s, c)
	if err != nil || len(res.Attempts) < 3 || res.Note != "" {
		t.Fatalf("%+v %v", res, err)
	}
	for _, a := range res.Attempts {
		if !a.OK || a.Status != 200 {
			t.Fatalf("attempt %+v", a)
		}
	}
	for _, p := range res.Points {
		if p.V != 1 {
			t.Fatalf("points %v", res.Points)
		}
	}
}

func TestCheckFailsWithAPlainSentence(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 0 {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	s := checkSources(t, Config{Type: Check, Hosts: []string{"127.0.0.0/8"}}, nil)
	res, err := runCheck(t, s, healthCheck(srv.URL+"/healthz?x=1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Note, " of ") || !strings.Contains(res.Note, " requests to /healthz returned 503") || strings.Contains(res.Note, "x=1") {
		t.Fatalf("note %q", res.Note)
	}
}

func TestCheckBodyAndJSONMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"degraded"}`)) }))
	defer srv.Close()
	s := checkSources(t, Config{Type: Check, Hosts: []string{"127.0.0.1"}}, nil)
	c := healthCheck(srv.URL)
	c.JSONPath, c.JSONEquals = "status", "ok"
	res, _ := runCheck(t, s, c)
	if !strings.Contains(res.Note, `status was "degraded", not "ok"`) {
		t.Fatalf("note %q", res.Note)
	}
	c = healthCheck(srv.URL)
	c.BodyContains = "healthy"
	if res, _ = runCheck(t, s, c); !strings.Contains(res.Note, `the body did not contain "healthy"`) {
		t.Fatalf("note %q", res.Note)
	}
}

func TestCheckRefusesWhatIsNotAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	s := checkSources(t, Config{Type: Check, Hosts: []string{"*.shop.svc.cluster.local"}, Namespaces: []string{"shop-dev"}}, nil)
	for url, want := range map[string]string{
		srv.URL:                        "is not in this source's hosts",
		"http://169.254.169.254/":      "is not in this source's hosts",
		"http://evil.example.com/":     "is not one this source allows",
		"http://api.other.svc/":        "is not one this source allows",
		"http://api.shop.svc/healthz/": "is not one this source allows",
	} {
		_, err := runCheck(t, s, healthCheck(url))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", url, err, want)
		}
	}
	// A Service of an allowed namespace is accepted before it is called.
	for _, h := range []string{"api.shop-dev", "api.shop-dev.svc", "api.shop-dev.svc.cluster.local", "x.shop.svc.cluster.local"} {
		if err := s.byName["checks"].check.permits(h); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	// Other projects, and an empty list, are refused.
	if _, err := s.Check(context.Background(), "checks", "other", healthCheck("http://api.shop-dev/"), time.Now(), time.Now().Add(time.Second)); err == nil || !strings.Contains(err.Error(), "may not use") {
		t.Errorf("project: %v", err)
	}
	if _, err := Open(map[string]Config{"c": {Type: Check, Projects: []string{"shop"}}}, nil); err == nil {
		t.Error("a check source with no hosts was accepted")
	}
}

func TestCheckSecretHeaderIsSentAndNeverReported(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization") + "|" + r.Host)
		w.WriteHeader(401)
	}))
	defer srv.Close()
	lookup := func(_ context.Context, project string, ref pipeline.SecretRef) (string, error) {
		return "Bearer s3cret-token", nil
	}
	s := checkSources(t, Config{Type: Check, Hosts: []string{"127.0.0.1"}}, lookup)
	c := healthCheck(srv.URL)
	c.SecretHeaders = map[string]pipeline.SecretRef{"Authorization": {Store: "vault", Key: "k"}}
	c.Headers = map[string]string{"Host": "api.example.com"}
	res, _ := runCheck(t, s, c)
	if v, _ := got.Load().(string); v != "Bearer s3cret-token|api.example.com" {
		t.Fatalf("server saw %q", v)
	}
	for _, a := range res.Attempts {
		if strings.Contains(a.Error, "s3cret") {
			t.Fatal("secret in attempt")
		}
	}
	if strings.Contains(res.Note, "s3cret") {
		t.Fatal("secret in note")
	}
}

func TestCheckDoesNotFollowRedirectsAndNeedsOptInForInsecureTLS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	}))
	defer srv.Close()
	s := checkSources(t, Config{Type: Check, Hosts: []string{"127.0.0.1"}}, nil)
	res, _ := runCheck(t, s, healthCheck(srv.URL))
	if !strings.Contains(res.Note, "returned 302 (a redirect") {
		t.Fatalf("note %q", res.Note)
	}
	c := healthCheck(strings.Replace(srv.URL, "http:", "https:", 1))
	c.InsecureTLS = true
	if _, err := runCheck(t, s, c); err == nil || !strings.Contains(err.Error(), "allow_insecure_tls") {
		t.Fatalf("insecure TLS: %v", err)
	}
}

func TestCheckTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	s := checkSources(t, Config{Type: Check, Hosts: []string{"127.0.0.1"}}, nil)
	c := &pipeline.HealthCheck{Kind: "tcp", URL: l.Addr().String(), Success: 1, Timeout: pipeline.Duration(time.Second), Every: pipeline.Duration(200 * time.Millisecond)}
	if res, err := runCheck(t, s, c); err != nil || res.Note != "" || len(res.Attempts) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	_ = l.Close()
	res, _ := runCheck(t, s, c)
	if !strings.Contains(res.Note, "connections to "+c.URL+" could not connect") && !strings.Contains(res.Note, "connection refused") {
		t.Fatalf("note %q", res.Note)
	}
}

// A slow answer fails a call that has max_latency, and every call keeps a
// short, redacted excerpt of what came back.
func TestCheckMaxLatencyAndExcerpt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(80 * time.Millisecond)
		}
		_, _ = w.Write([]byte(`{"status":"ok","note":"password=hunter2-LWCANARY"}`))
	}))
	defer srv.Close()
	s := checkSources(t, Config{Type: Check, Hosts: []string{"127.0.0.1"}}, nil)
	fast := healthCheck(srv.URL + "/fast")
	fast.MaxLatency = pipeline.Duration(2 * time.Second)
	res, err := runCheck(t, s, fast)
	if err != nil || len(res.Attempts) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, a := range res.Attempts {
		if !a.OK || !strings.Contains(a.Excerpt, `"status":"ok"`) || strings.Contains(a.Excerpt, "hunter2-LWCANARY") {
			t.Fatalf("a fast answer: %+v", a)
		}
	}
	slow := healthCheck(srv.URL + "/slow")
	slow.MaxLatency = pipeline.Duration(20 * time.Millisecond)
	res, err = runCheck(t, s, slow)
	if err != nil || len(res.Attempts) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, a := range res.Attempts {
		if a.OK || !strings.Contains(a.Error, "over max_latency") || a.Status != 200 {
			t.Fatalf("a slow answer should fail: %+v", a)
		}
	}
}
