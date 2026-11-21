package toggles

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures a Client.
type Options struct {
	// ServerURL is the address of the Launchway server, such as
	// https://launchway.example.com. Empty: no server; only SnapshotFile.
	ServerURL string
	// Key is a server SDK key (lwt_srv_...). Keep it secret: it can read every rule.
	Key string
	// SnapshotFile, when set, is where the ruleset is kept on disk after each
	// change, and where it is read from at start when the server cannot be
	// reached: an application restarted while Launchway is down still has the
	// last rules it saw.
	SnapshotFile string
	// NoStream polls instead of keeping a stream open.
	NoStream bool
	// PollEvery is how often to poll when not streaming, and while a stream is
	// down (default 30s).
	PollEvery time.Duration
	// InitTimeout is how long New waits for the first ruleset (default 3s).
	// Past it, New returns what it has (the snapshot file, or nothing) and the
	// client goes on trying; toggles answer with the caller's default until it has one.
	InitTimeout time.Duration
	// ReportEvery is how often counts of evaluations are sent (default 60s;
	// negative: never). Counts hold the toggle, the value and the reason, and no
	// information about the context.
	ReportEvery time.Duration
	// HTTPClient is used for every call (default: one with no overall timeout, for streams).
	HTTPClient *http.Client
	// OnError hears of problems: an unreachable server, a refused key.
	OnError func(error)
}

// Detail is an evaluation's value, with why.
type Detail[T any] struct {
	Value T
	// Reason is one of off, rule_match, fallthrough, prerequisite_failed,
	// missing_bucket_key, not_found, type_mismatch, error, or not_ready (no
	// ruleset yet: the default was used).
	Reason string
	RuleID string
}

// Client evaluates toggles of one environment.
type Client struct {
	o      Options
	rs     atomic.Pointer[Ruleset]
	etag   atomic.Value // string
	cancel context.CancelFunc
	done   chan struct{}
	ready  chan struct{}
	once   sync.Once

	mu     sync.Mutex
	counts map[countKey]int64
}

type countKey struct {
	toggle, value, reason string
}

// New starts a client. It returns once it has a ruleset, or InitTimeout has passed.
func New(ctx context.Context, o Options) (*Client, error) {
	if o.ServerURL == "" && o.SnapshotFile == "" {
		return nil, errors.New("toggles: give a ServerURL and Key, or a SnapshotFile")
	}
	if o.ServerURL != "" && o.Key == "" {
		return nil, errors.New("toggles: a Key is needed with a ServerURL")
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 30 * time.Second
	}
	if o.InitTimeout <= 0 {
		o.InitTimeout = 3 * time.Second
	}
	if o.ReportEvery == 0 {
		o.ReportEvery = time.Minute
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	c := &Client{o: o, ready: make(chan struct{}), done: make(chan struct{}), counts: map[countKey]int64{}}
	ctx, c.cancel = context.WithCancel(ctx)
	if o.ServerURL == "" {
		if err := c.loadSnapshot(); err != nil {
			c.cancel()
			return nil, err
		}
		close(c.done)
		return c, nil
	}
	go c.run(ctx)
	select {
	case <-c.ready:
	case <-time.After(o.InitTimeout):
		if c.rs.Load() == nil {
			_ = c.loadSnapshot() // the server is slow or down: the last known rules
		}
	case <-ctx.Done():
	}
	return c, nil
}

// NewFromFile makes a client that only reads a snapshot file, for tests and
// for places that never reach Launchway.
func NewFromFile(path string) (*Client, error) {
	return New(context.Background(), Options{SnapshotFile: path})
}

// NewFromRuleset makes a client that holds a ruleset in memory and talks to nobody.
func NewFromRuleset(rs Ruleset) *Client {
	c := &Client{done: make(chan struct{}), ready: make(chan struct{}), counts: map[countKey]int64{}, cancel: func() {}}
	c.rs.Store(&rs)
	c.markReady()
	close(c.done)
	return c
}

func (c *Client) markReady() { c.once.Do(func() { close(c.ready) }) }

// Ready reports whether the client has a ruleset (fetched, or from the snapshot file).
func (c *Client) Ready() bool { return c.rs.Load() != nil }

// Ruleset returns the ruleset in use, or nil.
func (c *Client) Ruleset() *Ruleset { return c.rs.Load() }

// Close stops the client, sending the counts it still holds.
func (c *Client) Close() {
	c.cancel()
	<-c.done
}

func (c *Client) fail(err error) {
	if c.o.OnError != nil && err != nil {
		c.o.OnError(err)
	}
}

func (c *Client) loadSnapshot() error {
	if c.o.SnapshotFile == "" {
		return errors.New("toggles: no snapshot file")
	}
	b, err := os.ReadFile(c.o.SnapshotFile)
	if err != nil {
		return fmt.Errorf("toggles: reading the snapshot file: %w", err)
	}
	var rs Ruleset
	if err := json.Unmarshal(b, &rs); err != nil {
		return fmt.Errorf("toggles: the snapshot file is not a ruleset: %w", err)
	}
	if c.rs.Load() == nil {
		c.rs.Store(&rs)
		c.markReady()
	}
	return nil
}

func (c *Client) store(body []byte, etag string) error {
	var rs Ruleset
	if err := json.Unmarshal(body, &rs); err != nil {
		return err
	}
	if rs.Schema > SchemaVersion {
		return fmt.Errorf("toggles: the server speaks ruleset schema %d, newer than this SDK's %d: upgrade the SDK", rs.Schema, SchemaVersion)
	}
	c.rs.Store(&rs)
	c.etag.Store(etag)
	c.markReady()
	if c.o.SnapshotFile != "" {
		tmp := c.o.SnapshotFile + ".tmp"
		if err := os.MkdirAll(filepath.Dir(c.o.SnapshotFile), 0o755); err == nil {
			if os.WriteFile(tmp, body, 0o600) == nil {
				_ = os.Rename(tmp, c.o.SnapshotFile)
			}
		}
	}
	return nil
}

// ---- keeping the ruleset current ----

func (c *Client) run(ctx context.Context) {
	defer close(c.done)
	var wg sync.WaitGroup
	if c.o.ReportEvery > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(c.o.ReportEvery)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					c.report(ctx)
				}
			}
		}()
	}
	backoff := time.Second
	for ctx.Err() == nil {
		var err error
		if c.o.NoStream {
			err = c.poll(ctx)
			if err == nil {
				sleep(ctx, c.o.PollEvery)
				continue
			}
		} else {
			err = c.stream(ctx)
		}
		if ctx.Err() != nil {
			break
		}
		c.fail(err)
		// The stream is down: poll while waiting to reconnect, so a change is not missed.
		_ = c.poll(ctx)
		sleep(ctx, jitter(minDur(backoff, c.o.PollEvery)))
		if backoff < time.Minute {
			backoff *= 2
		}
	}
	c.report(context.Background()) // the counts still held
	wg.Wait()
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func jitter(d time.Duration) time.Duration { return d/2 + time.Duration(rand.Int63n(int64(d/2)+1)) }

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.o.ServerURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.o.Key)
	req.Header.Set("User-Agent", "launchway-go-sdk")
	return req, nil
}

// poll fetches the ruleset once; a 304 means the one held is current.
func (c *Client) poll(ctx context.Context) error {
	req, err := c.request(ctx, http.MethodGet, "/api/sdk/v1/ruleset", nil)
	if err != nil {
		return err
	}
	if e, _ := c.etag.Load().(string); e != "" {
		req.Header.Set("If-None-Match", e)
	}
	resp, err := c.o.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
		b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return err
		}
		return c.store(b, resp.Header.Get("ETag"))
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("toggles: the server refused the key (%d): is it a current server key for this environment?", resp.StatusCode)
	}
	return fmt.Errorf("toggles: the server answered %d", resp.StatusCode)
}

// stream holds a server-sent event stream open until it breaks.
func (c *Client) stream(ctx context.Context) error {
	req, err := c.request(ctx, http.MethodGet, "/api/sdk/v1/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if e, _ := c.etag.Load().(string); e != "" {
		req.Header.Set("Last-Event-ID", strings.Trim(e, `"`))
	}
	resp, err := c.o.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("toggles: the stream answered %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	var id, event string
	var data bytes.Buffer
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if event == "ruleset" && data.Len() > 0 {
				if err := c.store(data.Bytes(), `"`+id+`"`); err != nil {
					c.fail(err)
				}
			}
			id, event = "", ""
			data.Reset()
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id:"):
			id = strings.TrimSpace(line[3:])
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// ---- evaluating ----

func (c *Client) detail(key string, ctx Context, want ValueType) Result {
	rs := c.rs.Load()
	if rs == nil {
		return Result{Reason: "not_ready"}
	}
	f, ok := rs.Toggles[key]
	if !ok {
		c.count(key, nil, ReasonNotFound)
		return Result{Reason: ReasonNotFound}
	}
	if f.Type != want {
		return Result{Reason: "type_mismatch"}
	}
	r := Evaluate(rs, key, ctx)
	c.count(key, r.Value, r.Reason)
	return r
}

func (c *Client) count(key string, v any, reason string) {
	b, _ := json.Marshal(v)
	val := string(b)
	if len(val) > 64 {
		val = val[:61] + "..."
	}
	c.mu.Lock()
	if len(c.counts) < 5000 {
		c.counts[countKey{key, val, reason}]++
	}
	c.mu.Unlock()
}

// BoolDetail evaluates a boolean toggle; def is used when the toggle is
// unknown, not yet loaded, or not a boolean.
func (c *Client) BoolDetail(key string, ctx Context, def bool) Detail[bool] {
	r := c.detail(key, ctx, Boolean)
	if v, ok := r.Value.(bool); ok {
		return Detail[bool]{v, r.Reason, r.RuleID}
	}
	return Detail[bool]{def, r.Reason, ""}
}

// Bool is the value of a boolean toggle, or def.
func (c *Client) Bool(key string, ctx Context, def bool) bool {
	return c.BoolDetail(key, ctx, def).Value
}

// StringDetail evaluates a string toggle.
func (c *Client) StringDetail(key string, ctx Context, def string) Detail[string] {
	r := c.detail(key, ctx, String)
	if v, ok := r.Value.(string); ok {
		return Detail[string]{v, r.Reason, r.RuleID}
	}
	return Detail[string]{def, r.Reason, ""}
}

// String is the value of a string toggle, or def.
func (c *Client) String(key string, ctx Context, def string) string {
	return c.StringDetail(key, ctx, def).Value
}

// NumberDetail evaluates a number toggle.
func (c *Client) NumberDetail(key string, ctx Context, def float64) Detail[float64] {
	r := c.detail(key, ctx, Number)
	if v, ok := Normalize(r.Value).(float64); ok {
		return Detail[float64]{v, r.Reason, r.RuleID}
	}
	return Detail[float64]{def, r.Reason, ""}
}

// Number is the value of a number toggle, or def.
func (c *Client) Number(key string, ctx Context, def float64) float64 {
	return c.NumberDetail(key, ctx, def).Value
}

// JSONDetail evaluates a json toggle: the value is what the JSON decoded to
// (maps, lists, strings, numbers, booleans, nil).
func (c *Client) JSONDetail(key string, ctx Context, def any) Detail[any] {
	r := c.detail(key, ctx, JSON)
	if r.Reason == "not_ready" || r.Reason == ReasonNotFound || r.Reason == "type_mismatch" {
		return Detail[any]{def, r.Reason, ""}
	}
	return Detail[any]{r.Value, r.Reason, r.RuleID}
}

// JSON is the value of a json toggle, or def.
func (c *Client) JSON(key string, ctx Context, def any) any { return c.JSONDetail(key, ctx, def).Value }

// ---- counts ----

// report sends the counts held, once. A failure keeps them for the next try.
func (c *Client) report(ctx context.Context) {
	c.mu.Lock()
	held := c.counts
	c.counts = map[countKey]int64{}
	c.mu.Unlock()
	if len(held) == 0 || c.o.ServerURL == "" || c.o.ReportEvery < 0 {
		return
	}
	type entry struct {
		Toggle string `json:"toggle"`
		Value  any    `json:"value"`
		Reason string `json:"reason"`
		Count  int64  `json:"count"`
	}
	var body struct {
		Counts []entry `json:"counts"`
	}
	for k, n := range held {
		var v any
		_ = json.Unmarshal([]byte(k.value), &v)
		body.Counts = append(body.Counts, entry{k.toggle, v, k.reason, n})
	}
	b, _ := json.Marshal(body)
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	req, err := c.request(cctx, http.MethodPost, "/api/sdk/v1/telemetry", bytes.NewReader(b))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		var resp *http.Response
		if resp, err = c.o.HTTPClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("toggles: the server answered %d to the counts", resp.StatusCode)
			}
		}
	}
	if err != nil {
		c.mu.Lock()
		for k, n := range held {
			c.counts[k] += n
		}
		c.mu.Unlock()
		c.fail(err)
	}
}
