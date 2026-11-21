package githostaccept

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: the receiver only serves the host under test; the httpoxy issue is of Go before 1.6.3
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Delivery is one webhook request the test receiver got.
type Delivery struct {
	Event, Sig, Sig256 string
	Body               []byte
}

// Receiver is a test HTTP server on this machine that records webhook
// deliveries and serves bare Git repositories over smart HTTP (so the host
// can mirror to and from it). Its paths carry a random secret.
type Receiver struct {
	Host, Secret string
	Port         int
	mu           sync.Mutex
	got          []Delivery
	root         string
}

// startReceiver listens on every interface, on a free port.
func (c *Config) startReceiver(t *testing.T) *Receiver {
	t.Helper()
	host, err := c.receiverHost()
	if err != nil {
		t.Skip(err.Error())
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "0.0.0.0:0") //nolint:gosec // G102: the host under test must reach the receiver from another machine
	if err != nil {
		t.Fatal(err)
	}
	r := &Receiver{Host: host, Secret: randHex(8), Port: ln.Addr().(*net.TCPAddr).Port, root: t.TempDir()}
	mux := http.NewServeMux()
	mux.HandleFunc("/hook/"+r.Secret, r.hook)
	if be, err := gitHTTPBackend(); err == nil {
		mux.Handle("/git/"+r.Secret+"/", &cgi.Handler{Path: be, Root: "/git/" + r.Secret,
			Env: []string{"GIT_PROJECT_ROOT=" + r.root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null"}})
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return r
}

func gitHTTPBackend() (string, error) {
	out, err := exec.CommandContext(context.Background(), "git", "--exec-path").Output()
	if err != nil {
		return "", err
	}
	p := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	_, err = os.Stat(p)
	return p, err
}

func (r *Receiver) hook(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(io.LimitReader(req.Body, 8<<20))
	d := Delivery{Event: firstOf(req.Header, "X-Gitea-Event", "X-GitHub-Event"), Body: b,
		Sig: req.Header.Get("X-Gitea-Signature"), Sig256: req.Header.Get("X-Hub-Signature-256")}
	r.mu.Lock()
	r.got = append(r.got, d)
	r.mu.Unlock()
	w.WriteHeader(204)
}

func firstOf(h http.Header, names ...string) string {
	for _, n := range names {
		if v := h.Get(n); v != "" {
			return v
		}
	}
	return ""
}

// HookURL is where the host should deliver webhooks.
func (r *Receiver) HookURL() string {
	return fmt.Sprintf("http://%s:%d/hook/%s", r.Host, r.Port, r.Secret)
}

// Deliveries returns what has arrived so far.
func (r *Receiver) Deliveries() []Delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Delivery(nil), r.got...)
}

// GitRepo creates an empty bare repository that accepts pushes and returns
// its URL as the host sees it and its directory.
func (r *Receiver) GitRepo(c *Config, t *testing.T, name string) (url, dir string) {
	t.Helper()
	if _, err := gitHTTPBackend(); err != nil {
		t.Skip("git-http-backend is not installed: " + err.Error())
	}
	dir = filepath.Join(r.root, name+".git")
	if out, err := c.git("", false, "init", "-q", "--bare", "-b", "main", dir); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	c.must(t, dir, "config", "http.receivepack", "true")
	return fmt.Sprintf("http://%s:%d/git/%s/%s.git", r.Host, r.Port, r.Secret, name), dir
}

// signedBy reports whether a delivery carries a valid HMAC-SHA256 of its body.
func (d Delivery) signedBy(secret string) bool {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(d.Body)
	want := hex.EncodeToString(m.Sum(nil))
	return hmac.Equal([]byte(d.Sig), []byte(want)) || hmac.Equal([]byte(d.Sig256), []byte("sha256="+want))
}
