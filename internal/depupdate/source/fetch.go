package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Cache stores response bodies. The default is in memory; a server can give
// one backed by its database.
type Cache interface {
	Get(key string) ([]byte, bool)
	Put(key string, body []byte, ttl time.Duration)
}

type memEntry struct {
	body    []byte
	expires time.Time
}

// MemCache is a Cache in memory.
type MemCache struct {
	mu  sync.Mutex
	m   map[string]memEntry
	Now func() time.Time
}

func (c *MemCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Get returns an unexpired entry.
func (c *MemCache) Get(k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || c.now().After(e.expires) {
		return nil, false
	}
	return e.body, true
}

// Put stores an entry.
func (c *MemCache) Put(k string, b []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]memEntry{}
	}
	c.m[k] = memEntry{b, c.now().Add(ttl)}
}

// Fetcher does the HTTP for every source.
type Fetcher struct {
	Client   *http.Client // the egress guard's client in a server; nil: a plain one with a timeout
	Cache    Cache
	TTL      time.Duration // default 1 hour
	MaxBytes int64         // default 32 MiB per response
	// Offline: never contact an address the installation did not name. A
	// source whose BaseURL is empty then answers ErrOffline.
	Offline bool
	// Off: the installation switched its lookups off (maintenance.lookups: off): nothing is requested, and every
	// source answers ErrLookupsOff.
	Off bool
	// Requests counts the network requests made (cache hits are not counted).
	mu       sync.Mutex
	Requests int
}

const notFoundMark = "\x00not-found"

func (f *Fetcher) ttl() time.Duration {
	if f.TTL == 0 {
		return time.Hour
	}
	return f.TTL
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Get fetches a URL with optional headers; the body is cached by URL and
// Accept header for the TTL.
func (f *Fetcher) Get(ctx context.Context, url string, header http.Header) ([]byte, error) {
	body, _, err := f.do(ctx, http.MethodGet, url, header)
	return body, err
}

// Head fetches response headers only.
func (f *Fetcher) Head(ctx context.Context, url string, header http.Header) (http.Header, error) {
	_, h, err := f.do(ctx, http.MethodHead, url, header)
	return h, err
}

func (f *Fetcher) do(ctx context.Context, method, url string, header http.Header) ([]byte, http.Header, error) {
	if f.Off {
		return nil, nil, ErrLookupsOff
	}
	key := method + " " + url + " " + header.Get("Accept")
	if f.Cache != nil && method == http.MethodGet {
		if b, ok := f.Cache.Get(key); ok {
			if string(b) == notFoundMark {
				return nil, nil, ErrNotFound
			}
			return b, nil, nil
		}
	}
	h, err := f.request(ctx, method, url, header, "")
	if err != nil {
		if errors.Is(err, ErrNotFound) && f.Cache != nil && method == http.MethodGet {
			f.Cache.Put(key, []byte(notFoundMark), f.ttl())
		}
		return nil, nil, err
	}
	if f.Cache != nil && method == http.MethodGet {
		f.Cache.Put(key, h.body, f.ttl())
	}
	return h.body, h.header, nil
}

type reply struct {
	body   []byte
	header http.Header
}

func (f *Fetcher) request(ctx context.Context, method, url string, header http.Header, bearer string) (reply, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return reply{}, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "launchway-dependency-updates")
	if bearer != "" && bearer != "-" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	f.mu.Lock()
	f.Requests++
	f.mu.Unlock()
	resp, err := f.client().Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	limit := f.MaxBytes
	if limit == 0 {
		limit = 32 << 20
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return reply{}, err
	}
	if int64(len(b)) > limit {
		return reply{}, fmt.Errorf("%s: the answer is larger than %d bytes", url, limit)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized && bearer == "":
		tok, terr := f.token(ctx, resp.Header.Get("Www-Authenticate"))
		if terr != nil {
			return reply{}, fmt.Errorf("%s: %w", url, terr)
		}
		return f.request(ctx, method, url, header, tok)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return reply{}, ErrNotFound
	case resp.StatusCode/100 != 2:
		return reply{}, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return reply{b, resp.Header}, nil
}

// token answers a registry's Bearer challenge anonymously (public images).
func (f *Fetcher) token(ctx context.Context, challenge string) (string, error) {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("the registry wants credentials; only anonymous reads are made")
	}
	attrs := map[string]string{}
	for _, part := range splitChallenge(challenge[len("Bearer "):]) {
		if k, v, ok := strings.Cut(part, "="); ok {
			attrs[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	realm := attrs["realm"]
	if realm == "" {
		return "", fmt.Errorf("the registry's challenge has no realm")
	}
	q := "?"
	if strings.Contains(realm, "?") {
		q = "&"
	}
	u := realm
	if s := attrs["service"]; s != "" {
		u += q + "service=" + s
		q = "&"
	}
	if s := attrs["scope"]; s != "" {
		u += q + "scope=" + s
	}
	r, err := f.request(ctx, http.MethodGet, u, nil, "-")
	if err != nil {
		return "", err
	}
	return tokenFrom(r.body)
}

func splitChallenge(s string) []string {
	var out []string
	in := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			in = !in
		case ',':
			if !in {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}
