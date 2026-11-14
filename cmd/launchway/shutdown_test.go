package main

import (
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// A stopping server reports not ready at once, keeps serving while it
// drains, waits for a request in flight, and ends a runner's long poll
// rather than waiting for it.
func TestShutdownDrainsGracefully(t *testing.T) {
	eng, err := engine.Open(engine.Options{Database: filepath.Join(t.TempDir(), "lw.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	srv := &api.Server{Engine: eng, RunnerToken: "tok", Actor: "me"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var active atomic.Int32
	hs := &http.Server{Handler: srv.Handler(), ConnState: func(_ net.Conn, s http.ConnState) {
		if s == http.StateActive {
			active.Add(1)
		}
	}}
	go hs.Serve(ln)
	base := "http://" + ln.Addr().String()
	get := func(path string) int {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Errorf("GET %s: %v", path, err)
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("/readyz"); code != 200 {
		t.Fatalf("/readyz before stopping: %d", code)
	}

	// A request whose body is still arriving, and a runner waiting for work.
	body, sendBody := io.Pipe()
	slow := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("POST", base+"/api/v1/validate", body)
		req.Header.Set("X-Launchway-CSRF", "1") // what the UI sends
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			slow <- 0
			return
		}
		resp.Body.Close()
		slow <- resp.StatusCode
	}()
	sendBody.Write([]byte("pipeline: p\nsteps:\n"))
	poll := make(chan int, 1)
	pollStart := time.Now()
	go func() {
		req, _ := http.NewRequest("POST", base+"/api/runner/v1/lease", strings.NewReader(`{"runner":{"id":"r"},"wait_seconds":50}`))
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			poll <- 0
			return
		}
		resp.Body.Close()
		poll <- resp.StatusCode
	}()
	for deadline := time.Now().Add(5 * time.Second); active.Load() < 3; {
		if time.Now().After(deadline) {
			t.Fatal("the requests did not arrive")
		}
		time.Sleep(10 * time.Millisecond)
	}

	runnerStopping := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- shutdown(hs, srv, time.Second, func() { close(runnerStopping) }) }()

	// While draining: not ready, but everything else still served.
	for deadline := time.Now().Add(time.Second); get("/readyz") != http.StatusServiceUnavailable; {
		if time.Now().After(deadline) {
			t.Fatal("/readyz did not report not ready")
		}
	}
	if code := get("/healthz"); code != 200 {
		t.Fatalf("/healthz while draining: %d", code)
	}
	select {
	case <-runnerStopping:
		t.Fatal("the drain delay was not kept")
	default:
	}

	<-runnerStopping
	if code := <-poll; code != http.StatusNoContent || time.Since(pollStart) > 10*time.Second {
		t.Fatalf("long poll: %d after %s", code, time.Since(pollStart))
	}
	time.Sleep(300 * time.Millisecond) // the server is now waiting for the slow request
	select {
	case err := <-done:
		t.Fatalf("shutdown finished with a request in flight: %v", err)
	default:
	}
	sendBody.Write([]byte("  a: {image: x, run: a}\n"))
	sendBody.Close()
	if code := <-slow; code != 200 {
		t.Fatalf("the request in flight got %d", code)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
