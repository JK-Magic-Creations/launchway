package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// /readyz answers 200 only once the server has answered properly; an error
// of the server's own does not count.
func TestHealth(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(status.Load())) }))
	defer srv.Close()
	r := &Runner{Client: NewClient(srv.URL, "tok")}
	h := r.Health()
	code := func(path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code
	}
	if code("/healthz") != 200 || code("/readyz") != 503 {
		t.Fatalf("before any contact: healthz %d, readyz %d", code("/healthz"), code("/readyz"))
	}
	_ = r.Client.Leave(context.Background(), "r")
	if code("/readyz") != 503 {
		t.Fatal("a server error counted as contact")
	}
	status.Store(http.StatusNoContent)
	if err := r.Client.Leave(context.Background(), "r"); err != nil {
		t.Fatal(err)
	}
	if code("/readyz") != 200 {
		t.Fatal("not ready after the server answered")
	}
	r.Client.contact.Store(r.Client.contact.Load() - readyWithin.Milliseconds() - 1000)
	if code("/readyz") != 503 {
		t.Fatal("still ready long after the last answer")
	}
}
