package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// A server looks up versions at package registries and advisories at the advisory service. When its network
// refuses those connections (a closed egress is a normal way to run a server), every lookup would wait for the
// refusal or the time-out again, for every dependency of every repository. A Breaker remembers, per host, that a
// dial was refused or timed out, and for an hour makes no further attempt to that host: the lookups that need it
// answer at once, and one log line says so and what it limits.

// ErrHostPaused is the answer for a lookup to a host the breaker has paused.
var ErrHostPaused = errors.New("lookups to this host are paused for an hour after a refused or timed-out connection")

// PauseFor is how long a host is left alone after a refused or timed-out dial.
const PauseFor = time.Hour

// Breaker is a set of per-host circuit breakers. The zero value is ready to use; Hosts is the one a server shares.
type Breaker struct {
	Now func() time.Time // default time.Now
	Log *slog.Logger     // default slog.Default()

	mu     sync.Mutex
	paused map[string]time.Time // host -> until
}

// Hosts is the breaker the server's lookups share (the clients are made again for each request; this is not).
var Hosts = &Breaker{}

func (b *Breaker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Paused says whether no attempt is made to the host now.
func (b *Breaker) Paused(host string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.paused[host]
	return ok && b.now().Before(until)
}

func (b *Breaker) trip(host string, err error) {
	b.mu.Lock()
	now := b.now()
	if until, ok := b.paused[host]; ok && now.Before(until) {
		b.mu.Unlock()
		return // another request tripped it a moment ago: one line only
	}
	if b.paused == nil {
		b.paused = map[string]time.Time{}
	}
	b.paused[host] = now.Add(PauseFor)
	b.mu.Unlock()
	log := b.Log
	if log == nil {
		log = slog.Default()
	}
	log.Warn("a connection to "+host+" was refused or timed out, so no lookup is made to it for an hour: dependency-update version lookups and the software graph's advisory lookups that need it show nothing new until then (maintenance.lookups: off switches the lookups off)",
		"host", host, "for", PauseFor.String(), "err", err)
}

// refused says whether err is a dial that was refused, blocked or timed out (not a caller that gave up).
func refused(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Wrap returns a client that goes through the breaker; the client's own settings and transport are kept.
func (b *Breaker) Wrap(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if _, again := base.(*breakerTransport); again {
		return c
	}
	cp := *c
	cp.Transport = &breakerTransport{b: b, base: base}
	return &cp
}

type breakerTransport struct {
	b    *Breaker
	base http.RoundTripper
}

func (t *breakerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	if t.b.Paused(host) {
		return nil, fmt.Errorf("%s: %w", host, ErrHostPaused)
	}
	resp, err := t.base.RoundTrip(req)
	if refused(req.Context(), err) {
		t.b.trip(host, err)
	}
	return resp, err
}
