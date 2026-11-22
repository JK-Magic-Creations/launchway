package source

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A host whose connections are refused is left alone for an hour, with one log line; then it is tried again.
func TestBreakerPausesAHostThatRefusesConnections(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens there now: the connection is refused

	now := time.Now()
	var logs bytes.Buffer
	b := &Breaker{Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	var dials atomic.Int32
	base := &http.Transport{DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, a)
	}}
	f := &Fetcher{Client: b.Wrap(&http.Client{Transport: base, Timeout: 5 * time.Second})}

	if _, err := f.Get(context.Background(), "http://"+addr+"/a", nil); err == nil || errors.Is(err, ErrHostPaused) {
		t.Fatalf("the first lookup should be refused by the network: %v", err)
	}
	if dials.Load() != 1 || !b.Paused(addr) {
		t.Fatalf("dials %d, paused %v", dials.Load(), b.Paused(addr))
	}
	for i := 0; i < 5; i++ { // many lookups for many dependencies: none reaches the network
		if _, err := f.Get(context.Background(), "http://"+addr+"/b", nil); !errors.Is(err, ErrHostPaused) {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}
	if dials.Load() != 1 {
		t.Fatalf("the network was tried again while paused: %d dials", dials.Load())
	}
	if n := strings.Count(logs.String(), "\n"); n != 1 || !strings.Contains(logs.String(), "maintenance.lookups") || !strings.Contains(logs.String(), addr) {
		t.Fatalf("want one log line naming the host and what it limits, got %d:\n%s", n, logs.String())
	}

	now = now.Add(PauseFor + time.Second) // an hour later it is tried once more, and pauses again with one more line
	if _, err := f.Get(context.Background(), "http://"+addr+"/c", nil); err == nil || errors.Is(err, ErrHostPaused) {
		t.Fatalf("after the hour: %v", err)
	}
	if dials.Load() != 2 || strings.Count(logs.String(), "\n") != 2 {
		t.Fatalf("dials %d, log:\n%s", dials.Load(), logs.String())
	}
}

// A host that answers, even with an error status, is not paused; neither is a caller that gives up.
func TestBreakerLeavesAnAnsweringHostAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 500) }))
	defer srv.Close()
	b := &Breaker{}
	f := &Fetcher{Client: b.Wrap(srv.Client())}
	for i := 0; i < 3; i++ {
		if _, err := f.Get(context.Background(), srv.URL, nil); err == nil || errors.Is(err, ErrHostPaused) {
			t.Fatalf("%v", err)
		}
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if b.Paused(host) {
		t.Fatal("a host that answers was paused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = f.Get(ctx, srv.URL+"/x", nil)
	if b.Paused(host) {
		t.Fatal("a canceled caller paused the host")
	}
}
