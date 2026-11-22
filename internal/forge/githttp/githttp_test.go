package githttp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// fake is a Backend that runs a function, so the transport is tested without git.
type fake struct {
	upload func(ctx context.Context, rpc storage.RPC) error
}

func (f fake) Upload(ctx context.Context, _ string, rpc storage.RPC) error { return f.upload(ctx, rpc) }
func (f fake) Receive(ctx context.Context, _ string, rpc storage.RPC) (storage.ReceiveResult, error) {
	return storage.ReceiveResult{}, f.upload(ctx, rpc)
}

func serve(h *Handler, user string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc, adv, err := Service(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		who := user
		if u := r.Header.Get("X-User"); u != "" {
			who = u
		}
		h.Serve(w, r, Request{Repo: forge.Repository{ID: "rep_x"}, Service: svc, Advertise: adv, User: who})
	})
}

func post(url, user string, body io.Reader) (*http.Response, error) {
	req, _ := http.NewRequest("POST", url+"/git-upload-pack", body)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("X-User", user)
	return http.DefaultClient.Do(req)
}

func TestConcurrencyCapQueuesThenRefuses(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	h := New(Options{MaxConcurrent: 2, MaxPerUser: 2, QueueWait: 150 * time.Millisecond, Backend: fake{func(ctx context.Context, rpc storage.RPC) error {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}}})
	srv := httptest.NewServer(serve(h, ""))
	defer srv.Close()
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := post(srv.URL, fmt.Sprintf("u%d", i), strings.NewReader(""))
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	<-started
	<-started
	// The cap is full: a third request from another person waits, then is refused (not run, not piled up).
	resp, err := post(srv.URL, "u3", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("past the cap: %d", resp.StatusCode)
	}
	close(release)
	wg.Wait()
	// Once they finish, requests are served again.
	resp, err = post(srv.URL, "u3", strings.NewReader(""))
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("after: %v", err)
	}
}

func TestOnePersonCannotTakeEveryProcess(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	h := New(Options{MaxConcurrent: 8, MaxPerUser: 2, Backend: fake{func(ctx context.Context, rpc storage.RPC) error {
		started <- struct{}{}
		<-release
		return nil
	}}})
	srv := httptest.NewServer(serve(h, ""))
	defer srv.Close()
	for range 2 {
		go func() {
			if r, err := post(srv.URL, "greedy", strings.NewReader("")); err == nil {
				r.Body.Close()
			}
		}()
	}
	<-started
	<-started
	resp, err := post(srv.URL, "greedy", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Errorf("a third request from one person: %d", resp.StatusCode)
	}
	// Someone else is not affected (their request is admitted at once; the backend answers once released).
	done := make(chan *http.Response, 1)
	go func() {
		r, _ := post(srv.URL, "someone-else", strings.NewReader(""))
		done <- r
	}()
	<-started
	close(release)
	other := <-done
	err = nil
	if other == nil {
		err = errors.New("no answer")
	} else {
		other.Body.Close()
	}
	if err != nil || other.StatusCode != 200 {
		t.Errorf("another person was refused too: %v", err)
	}
}

func TestAdvertiseHasADeadline(t *testing.T) {
	h := New(Options{AdvertiseTimeout: 100 * time.Millisecond, Backend: fake{func(ctx context.Context, rpc storage.RPC) error {
		<-ctx.Done()
		return ctx.Err()
	}}})
	srv := httptest.NewServer(serve(h, "u"))
	defer srv.Close()
	start := time.Now()
	resp, err := http.Get(srv.URL + "/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body) // headers arrive at once; the body ends at the deadline
	resp.Body.Close()
	// The service header was already written, so the status is 200 or the timeout's; what matters is it ended.
	if d := time.Since(start); d < 90*time.Millisecond || d > testlib.Slow(3*time.Second) {
		t.Errorf("the advertisement did not stop at its deadline: %s", time.Since(start))
	}
}

func TestStalledClientIsCutOffButASlowOneIsNot(t *testing.T) {
	var got int
	h := New(Options{IdleTimeout: 300 * time.Millisecond, Backend: fake{func(ctx context.Context, rpc storage.RPC) error {
		b, err := io.ReadAll(rpc.In)
		got = len(b)
		return err
	}}})
	ts := httptest.NewServer(serve(h, "u"))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")
	send := func(pause time.Duration) (string, error) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return "", err
		}
		defer c.Close()
		fmt.Fprintf(c, "POST /git-upload-pack HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-git-upload-pack-request\r\nContent-Length: 10\r\n\r\n12345")
		time.Sleep(pause)
		fmt.Fprint(c, "67890")
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		line, err := bufio.NewReader(c).ReadString('\n')
		return line, err
	}
	// 100 ms between pieces is progress: served.
	if line, err := send(100 * time.Millisecond); err != nil || !strings.Contains(line, "200") || got != 10 {
		t.Errorf("a slow but moving client: %q %v (read %d)", line, err, got)
	}
	// 1 s of silence is a stall: the request does not complete with all of its body.
	got = 0
	if line, _ := send(time.Second); strings.Contains(line, "200") && got == 10 {
		t.Errorf("a stalled client was served in full: %q", line)
	}
}

func TestRequestBodyLimitsAndTypes(t *testing.T) {
	h := New(Options{MaxFetchBytes: 100, Backend: fake{func(ctx context.Context, rpc storage.RPC) error {
		_, err := io.Copy(io.Discard, rpc.In)
		return err
	}}})
	srv := httptest.NewServer(serve(h, "u"))
	defer srv.Close()
	for _, c := range []struct {
		body string
		want int
	}{{strings.Repeat("x", 50), 200}, {strings.Repeat("x", 500), 413}} {
		resp, err := post(srv.URL, "u", strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%d bytes: %d, want %d", len(c.body), resp.StatusCode, c.want)
		}
	}
	// The ref advertisement of a service is announced before git's own.
	resp, _ := http.Get(srv.URL + "/info/refs?service=git-upload-pack")
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/x-git-upload-pack-advertisement" || !strings.Contains(resp.Header.Get("Cache-Control"), "no-cache") {
		t.Errorf("headers: %v", resp.Header)
	}
	if _, _, err := Service(httptest.NewRequest("GET", "/info/refs", nil)); err == nil {
		t.Error("the dumb protocol was accepted")
	}
}

func TestRunTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k := RunTokens{Key: []byte("0123456789abcdef0123456789abcdef"), Now: func() time.Time { return now }}
	tok, err := k.MintForRun("rep_a", "run_1", time.Hour)
	if err != nil || !strings.HasPrefix(tok, RunTokenPrefix) {
		t.Fatal(tok, err)
	}
	c, err := k.Verify(tok)
	if err != nil || c.Repo != "rep_a" || c.Run != "run_1" {
		t.Fatalf("%+v %v", c, err)
	}
	other := RunTokens{Key: []byte("another key another key another!"), Now: k.Now}
	if _, err := other.Verify(tok); !errors.Is(err, ErrBadRunToken) {
		t.Error("a token verified under another key")
	}
	if _, err := k.Verify(tok[:len(tok)-1] + "A"); err == nil {
		t.Error("a changed MAC verified")
	}
	forged := RunTokenPrefix + "cmVwX2J8cnVuXzF8emlk" + tok[strings.Index(tok, "."):]
	if _, err := k.Verify(forged); err == nil {
		t.Error("changed claims verified")
	}
	now = now.Add(2 * time.Hour)
	if _, err := k.Verify(tok); !errors.Is(err, ErrBadRunToken) {
		t.Error("an expired token verified")
	}
	if _, err := k.MintForRun("rep_a", "r", 48*time.Hour); err == nil {
		t.Error("a token for two days")
	}
	if _, err := k.Mint("rep_a", true, time.Hour); err == nil {
		t.Error("a write token")
	}
}
