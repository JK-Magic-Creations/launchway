package toggles_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	toggles "github.com/jk-magic-creations/launchway/sdk/go/toggles"
	"github.com/jk-magic-creations/launchway/sdk/go/toggles/toggletest"
)

// fakeServer is the part of Launchway's protocol an SDK talks to.
type fakeServer struct {
	mu        sync.Mutex
	on        bool
	version   int
	changed   chan struct{}
	telemetry []string
	rulesets  int
	lastID    string
	down      bool
}

func (f *fakeServer) ruleset() ([]byte, string) {
	rs := toggles.Ruleset{Schema: 1, Environment: "production", Toggles: map[string]toggles.Flag{
		"checkout": {Key: "checkout", Type: toggles.Boolean, Salt: "checkout", State: toggles.State{On: f.on, OffValue: false, Fallthrough: toggles.Serve{Value: true}}},
		"label":    {Key: "label", Type: toggles.String, Salt: "label", State: toggles.State{On: true, OffValue: "a", Fallthrough: toggles.Serve{Value: "b"}}},
	}}
	b, _ := json.Marshal(rs)
	return b, fmt.Sprintf(`"v%d"`, f.version)
}

func (f *fakeServer) set(on bool) {
	f.mu.Lock()
	f.on = on
	f.version++
	old := f.changed
	f.changed = make(chan struct{})
	f.mu.Unlock()
	close(old)
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer lwt_srv_test" {
			http.Error(w, "no", 401)
			return false
		}
		f.mu.Lock()
		down := f.down
		f.mu.Unlock()
		if down {
			http.Error(w, "down", 503)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/sdk/v1/ruleset", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		f.mu.Lock()
		body, etag := f.ruleset()
		f.rulesets++
		f.mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", etag)
		w.Write(body)
	})
	mux.HandleFunc("GET /api/sdk/v1/stream", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		f.mu.Lock()
		f.lastID = r.Header.Get("Last-Event-ID")
		f.mu.Unlock()
		last := strings.Trim(r.Header.Get("Last-Event-ID"), `"`)
		for {
			f.mu.Lock()
			body, etag := f.ruleset()
			ch := f.changed
			f.mu.Unlock()
			if id := strings.Trim(etag, `"`); id != last {
				fmt.Fprintf(w, "id: %s\nevent: ruleset\ndata: %s\n\n", id, body)
				fl.Flush()
				last = id
			}
			select {
			case <-r.Context().Done():
				return
			case <-ch:
			}
		}
	})
	mux.HandleFunc("POST /api/sdk/v1/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var b struct {
			Counts []struct{ Toggle, Reason string }
		}
		json.NewDecoder(r.Body).Decode(&b)
		raw, _ := json.Marshal(b)
		f.mu.Lock()
		f.telemetry = append(f.telemetry, string(raw))
		f.mu.Unlock()
		w.Write([]byte(`{"accepted":1}`))
	})
	return mux
}

func newFake(t *testing.T) (*fakeServer, *httptest.Server) {
	f := &fakeServer{version: 1, changed: make(chan struct{})}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, srv
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestClientStreamsChangesAndEvaluatesLocally(t *testing.T) {
	f, srv := newFake(t)
	c, err := toggles.New(context.Background(), toggles.Options{ServerURL: srv.URL, Key: "lwt_srv_test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Ready() || c.Bool("checkout", toggles.Context{"key": "u"}, true) != false {
		t.Fatalf("ready %v, checkout %v", c.Ready(), c.Bool("checkout", nil, true))
	}
	d := c.BoolDetail("checkout", nil, true)
	if d.Reason != "off" {
		t.Fatalf("%+v", d)
	}
	f.set(true)
	waitFor(t, "the change to arrive on the stream", func() bool { return c.Bool("checkout", nil, false) })
	if got := c.String("label", nil, "x"); got != "b" {
		t.Fatalf("string: %q", got)
	}
	// What is not there, or not that type, is the caller's default.
	if c.Bool("nope", nil, true) != true || c.BoolDetail("nope", nil, true).Reason != "not_found" {
		t.Fatal("unknown toggle")
	}
	if d := c.BoolDetail("label", nil, true); d.Value != true || d.Reason != "type_mismatch" {
		t.Fatalf("wrong type: %+v", d)
	}
}

func TestClientResumesTheStreamWithTheLastEventID(t *testing.T) {
	f, srv := newFake(t)
	c, _ := toggles.New(context.Background(), toggles.Options{ServerURL: srv.URL, Key: "lwt_srv_test", PollEvery: 50 * time.Millisecond})
	defer c.Close()
	f.set(true)
	waitFor(t, "the change", func() bool { return c.Bool("checkout", nil, false) })
	// Break the stream: the client reconnects with the id of the event it had.
	f.mu.Lock()
	f.lastID = ""
	f.mu.Unlock()
	srv.CloseClientConnections()
	waitFor(t, "a reconnect that says where it was", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.lastID == "v2"
	})
}

func TestClientPollsWithETagWhenNotStreaming(t *testing.T) {
	f, srv := newFake(t)
	c, _ := toggles.New(context.Background(), toggles.Options{ServerURL: srv.URL, Key: "lwt_srv_test", NoStream: true, PollEvery: 30 * time.Millisecond})
	defer c.Close()
	f.set(true)
	waitFor(t, "a poll to see the change", func() bool { return c.Bool("checkout", nil, false) })
	f.mu.Lock()
	n := f.rulesets
	f.mu.Unlock()
	if n < 2 {
		t.Fatalf("polled %d times", n)
	}
}

func TestSnapshotFileKeepsTheLastRulesWhenTheServerIsGone(t *testing.T) {
	f, srv := newFake(t)
	path := filepath.Join(t.TempDir(), "toggles.json")
	f.set(true)
	c, _ := toggles.New(context.Background(), toggles.Options{ServerURL: srv.URL, Key: "lwt_srv_test", SnapshotFile: path})
	waitFor(t, "the snapshot", func() bool { _, err := os.Stat(path); return err == nil })
	c.Close()
	srv.Close() // Launchway is down when the application restarts
	c2, err := toggles.New(context.Background(), toggles.Options{ServerURL: srv.URL, Key: "lwt_srv_test", SnapshotFile: path, InitTimeout: 300 * time.Millisecond, ReportEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if !c2.Ready() || !c2.Bool("checkout", nil, false) {
		t.Fatal("the restarted application did not have the last rules")
	}
	// Offline use, with a file alone.
	c3, err := toggles.NewFromFile(path)
	if err != nil || !c3.Bool("checkout", nil, false) {
		t.Fatalf("%v", err)
	}
	// Before there is anything: the defaults, and no error.
	c4, err := toggles.New(context.Background(), toggles.Options{ServerURL: srv.URL, Key: "lwt_srv_test", InitTimeout: 100 * time.Millisecond, ReportEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer c4.Close()
	if c4.Ready() || c4.Bool("checkout", nil, true) != true || c4.BoolDetail("checkout", nil, true).Reason != "not_ready" {
		t.Fatal("a client with nothing should answer with the default")
	}
}

func TestCountsAreReportedWithoutAnyContext(t *testing.T) {
	f, srv := newFake(t)
	f.set(true)
	c, _ := toggles.New(context.Background(), toggles.Options{ServerURL: srv.URL, Key: "lwt_srv_test", ReportEvery: 40 * time.Millisecond})
	defer c.Close()
	for i := 0; i < 5; i++ {
		c.Bool("checkout", toggles.Context{"key": fmt.Sprintf("user-%d", i), "email": "someone@example.com"}, false)
	}
	waitFor(t, "a report", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.telemetry) > 0
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	all := strings.Join(f.telemetry, " ")
	if !strings.Contains(all, "checkout") || strings.Contains(all, "user-") || strings.Contains(all, "example.com") {
		t.Fatalf("what was reported: %s", all)
	}
}

func TestARefusedKeyIsReportedNotHidden(t *testing.T) {
	_, srv := newFake(t)
	errs := make(chan error, 4)
	c, err := toggles.New(context.Background(), toggles.Options{ServerURL: srv.URL, Key: "lwt_srv_wrong", InitTimeout: 100 * time.Millisecond, ReportEvery: -1,
		OnError: func(err error) {
			select {
			case errs <- err:
			default:
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("nil error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a refused key was not reported")
	}
}

func TestTestHelper(t *testing.T) {
	c := toggletest.Values(map[string]any{"new-ui": true, "limit": 5, "banner": "hi", "cfg": map[string]any{"a": 1}})
	toggletest.MustBe(t, c, "new-ui", nil, true)
	if c.Number("limit", nil, 0) != 5 || c.String("banner", nil, "") != "hi" {
		t.Fatal("values")
	}
	if m, _ := c.JSON("cfg", nil, nil).(map[string]any); m["a"] != 1 {
		t.Fatalf("%v", c.JSON("cfg", nil, nil))
	}
}
