package fleet

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeWorld is a fleet in memory: hosts with releases and a balancer.
type fakeWorld struct {
	mu          sync.Mutex
	current     map[string]string // host -> release
	files       map[string]string // host/release -> sha
	bad         map[string]bool   // release -> unhealthy
	state       map[string]string // member -> up, drain
	weight      map[string]int
	drained     int
	maxDrain    int
	cmds        []string
	failRestart map[string]bool
}

func newWorld(hosts []Host, rel string) *fakeWorld {
	w := &fakeWorld{current: map[string]string{}, files: map[string]string{}, bad: map[string]bool{},
		state: map[string]string{}, weight: map[string]int{}, failRestart: map[string]bool{}}
	for _, h := range hosts {
		w.current[h.Name] = rel
		w.state[h.Member] = "up"
		w.weight[h.Member] = 100
	}
	return w
}

func (w *fakeWorld) Run(_ context.Context, h Host, cmd string, in io.Reader) (Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cmds = append(w.cmds, h.Name+": "+cmd)
	switch {
	case strings.HasPrefix(cmd, "readlink"):
		return Result{Stdout: "releases/" + w.current[h.Name]}, nil
	case strings.HasPrefix(cmd, "mkdir"):
	case strings.HasPrefix(cmd, "cat >"):
		b, _ := io.ReadAll(in)
		s := sha256.Sum256(b)
		w.files[h.Name] = hex.EncodeToString(s[:])
	case strings.HasPrefix(cmd, "sha256sum"):
		return Result{Stdout: w.files[h.Name] + "  file"}, nil
	case strings.HasPrefix(cmd, "ln -sfn"):
		i := strings.Index(cmd, "'releases/")
		rest := cmd[i+len("'releases/"):]
		w.current[h.Name] = rest[:strings.Index(rest, "'")]
	case strings.Contains(cmd, "systemctl restart"):
		if w.failRestart[h.Name] && w.current[h.Name] == "v2" {
			return Result{Exit: 1, Stderr: "unit failed"}, nil
		}
	default:
		return Result{Exit: 127, Stderr: "unknown " + cmd}, nil
	}
	return Result{}, nil
}

func (w *fakeWorld) Drain(_ context.Context, m string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state[m] = "drain"
	w.drained++
	w.maxDrain = max(w.maxDrain, w.drained)
	return nil
}
func (w *fakeWorld) Admit(_ context.Context, m string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state[m] == "drain" {
		w.drained--
	}
	w.state[m] = "up"
	return nil
}
func (w *fakeWorld) SetWeight(_ context.Context, m string, n int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.weight[m] = n
	return nil
}
func (w *fakeWorld) Observe(context.Context) (map[string]Member, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]Member{}
	for m, s := range w.state {
		out[m] = Member{Member: m, State: s, Weight: w.weight[m]}
	}
	return out, nil
}

// Healthy: a host is healthy unless its current release is marked bad.
type fakeCheck struct{ w *fakeWorld }

func (c fakeCheck) Healthy(_ context.Context, url string) error {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	host := strings.TrimPrefix(url, "http://")
	rel := c.w.current[host]
	if c.w.bad[rel] {
		return fmt.Errorf("answered 500")
	}
	return nil
}

func testHosts(n int) []Host {
	var hs []Host
	for i := 1; i <= n; i++ {
		hs = append(hs, Host{Name: fmt.Sprintf("web-%d", i), Address: "x", HostKey: "ssh-ed25519 AAAA", Service: fmt.Sprintf("web-%d", i), Member: fmt.Sprintf("app/web-%d", i)})
	}
	return hs
}

func newFleet(t *testing.T, hs []Host) (*Fleet, *fakeWorld, *[]Event) {
	t.Helper()
	w := newWorld(hs, "v1")
	lay, _ := NewLayout("web")
	var evs []Event
	var mu sync.Mutex
	f := &Fleet{Remote: w, Traffic: w, Check: fakeCheck{w}, Layout: lay,
		CheckURL: func(h Host) string { return "http://" + h.Service },
		Events:   func(e Event) { mu.Lock(); evs = append(evs, e); mu.Unlock() }}
	return f, w, &evs
}

func quick(spec RollSpec) RollSpec {
	spec.CheckEvery, spec.CheckWithin, spec.DrainMax = time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	return spec
}

func hostKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, string(ssh.MarshalAuthorizedKey(s.PublicKey()))
}
