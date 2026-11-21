// Package runner takes steps from a Launchway server and executes them.
// It only ever makes outbound connections to the server.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
)

// ErrLeaseLost means the server no longer recognises the lease; the step
// has been given to someone else or canceled, and this runner must stop it.
var ErrLeaseLost = errors.New("lease lost")

// ErrTooLarge means the server refused a request as too large: a result
// with more report data than it accepts.
var ErrTooLarge = errors.New("too large for the server")

// Client talks to the server's runner API.
type Client struct {
	Server string // base URL, e.g. http://127.0.0.1:8080
	Token  string
	HTTP   *http.Client
	// Auth, for an enrolled runner, gives each request an access token in
	// place of Token.
	Auth *IdentityAuth

	contact atomic.Int64 // Unix milliseconds of the last good answer; see LastContact
}

// LastContact is when the server last answered a request in a way that
// shows the runner can work with it: not an error of the server's own, and
// not a refused token. It is zero until the first such answer.
func (c *Client) LastContact() time.Time {
	ms := c.contact.Load()
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func NewClient(server, token string) *Client {
	return &Client{Server: strings.TrimRight(server, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// bearer is the token for a request: the access token of an enrolled
// runner, or the shared or pool token.
func (c *Client) bearer(ctx context.Context) (string, error) {
	if c.Auth != nil {
		return c.Auth.Token(ctx)
	}
	return c.Token, nil
}

// do sends a request with the runner's token. An enrolled runner whose
// access token is refused (it expired, or a rotation replaced it) gets a
// new one and sends the request once more.
func (c *Client) do(ctx context.Context, method, url, contentType string, body []byte) (*http.Response, error) {
	for try := 0; ; try++ {
		tok, err := c.bearer(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+tok)
		if k := idemKey(url, body); k != "" {
			req.Header.Set("Idempotency-Key", k)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && c.Auth != nil && try == 0 {
			resp.Body.Close()
			c.Auth.Invalidate(tok)
			continue
		}
		return resp, nil
	}
}

func (c *Client) call(ctx context.Context, path string, in, out any) (int, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	resp, err := c.do(ctx, http.MethodPost, c.Server+"/api/runner/v1/"+path, "application/json", body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 500 && resp.StatusCode != http.StatusUnauthorized {
		c.contact.Store(time.Now().UnixMilli())
	}
	switch {
	case resp.StatusCode == http.StatusGone:
		return resp.StatusCode, ErrLeaseLost
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return resp.StatusCode, fmt.Errorf("%s: %w (HTTP 413)", path, ErrTooLarge)
	case resp.StatusCode == http.StatusNoContent:
		return resp.StatusCode, nil
	case resp.StatusCode >= 300:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		if out != nil && resp.StatusCode == http.StatusConflict {
			_ = json.Unmarshal(data, out) // a conflict may say where to carry on
		}
		return resp.StatusCode, fmt.Errorf("%s: %s (HTTP %d)", path, e.Error, resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// Lease waits up to wait for a step. It returns nil when there is none.
func (c *Client) Lease(ctx context.Context, info engine.RunnerInfo, wait time.Duration) (*engine.Job, error) {
	var job engine.Job
	code, err := c.call(ctx, "lease", map[string]any{"runner": info, "wait_seconds": int(wait / time.Second)}, &job)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	return &job, nil
}

type cancelReply struct {
	Cancel bool `json:"cancel"`
}

func (c *Client) Start(ctx context.Context, lease string) (bool, error) {
	var r cancelReply
	_, err := c.call(ctx, "start", map[string]string{"lease_id": lease}, &r)
	return r.Cancel, err
}

func (c *Client) Heartbeat(ctx context.Context, lease string) (bool, error) {
	var r cancelReply
	_, err := c.call(ctx, "heartbeat", map[string]string{"lease_id": lease}, &r)
	return r.Cancel, err
}

func (c *Client) Logs(ctx context.Context, lease string, lines []engine.LogLine) error {
	_, err := c.call(ctx, "logs", map[string]any{"lease_id": lease, "lines": lines}, nil)
	return err
}

// LogsAt sends lines that follow the first offset lines of the step
// attempt. The server keeps what it already has, so a batch may be sent
// again after a lost answer; a server that answers 409 has fewer lines than
// offset (see LogGap).
func (c *Client) LogsAt(ctx context.Context, lease string, offset int, lines []engine.LogLine) error {
	var gap struct {
		Received int `json:"received"`
	}
	code, err := c.call(ctx, "logs", map[string]any{"lease_id": lease, "offset": offset, "lines": lines}, &gap)
	if code == http.StatusConflict {
		return &LogGap{Have: gap.Received}
	}
	return err
}

// LogGap means the server has only Have lines of a step attempt, fewer
// than the runner's batch follows.
type LogGap struct{ Have int }

func (g *LogGap) Error() string { return fmt.Sprintf("the server has %d lines of this step", g.Have) }

// RunInfos asks the server about runs, for tidying up resources held for them.
func (c *Client) RunInfos(ctx context.Context, ids []string) (map[string]executor.RunInfo, error) {
	var list []executor.RunInfo
	if _, err := c.call(ctx, "runs", map[string]any{"ids": ids}, &list); err != nil {
		return nil, err
	}
	out := make(map[string]executor.RunInfo, len(list))
	for _, r := range list {
		out[r.ID] = r
	}
	return out, nil
}

// Leave tells the server that this runner is shutting down and holds no
// steps, so that other runners may take over its runs at once.
func (c *Client) Leave(ctx context.Context, runnerID string) error {
	_, err := c.call(ctx, "leave", map[string]string{"runner_id": runnerID}, nil)
	return err
}

func (c *Client) Finish(ctx context.Context, lease string, res engine.Result) error {
	_, err := c.call(ctx, "finish", map[string]any{"lease_id": lease, "result": res}, nil)
	return err
}

// LeaseDeploy waits up to wait for a deploy action of the runner's targets.
// It returns nil when there is none.
func (c *Client) LeaseDeploy(ctx context.Context, info engine.RunnerInfo, wait time.Duration) (*engine.Job, error) {
	var job engine.Job
	code, err := c.call(ctx, "deploy/lease", map[string]any{"runner": info, "wait_seconds": int(wait / time.Second)}, &job)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	return &job, nil
}

// WatchList is what a deploy runner should observe.
type WatchList struct {
	Items   []engine.WatchItem `json:"items"`
	Version string             `json:"version"`
	Targets []string           `json:"targets"` // the targets the server lets it serve
}

// Watch asks which deployments to observe, waiting up to wait for the list
// to differ from known. A nil list means the server is stopping; ask again.
func (c *Client) Watch(ctx context.Context, info engine.RunnerInfo, known string, wait time.Duration) (*WatchList, error) {
	var out WatchList
	code, err := c.call(ctx, "deploy/watch", map[string]any{"runner": info, "known": known, "wait_seconds": int(wait / time.Second)}, &out)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	return &out, nil
}

// Observe reports what the runner saw.
func (c *Client) Observe(ctx context.Context, info engine.RunnerInfo, obs []engine.Observation) ([]engine.ObserveResult, error) {
	var out struct {
		Results []engine.ObserveResult `json:"results"`
	}
	_, err := c.call(ctx, "deploy/observe", map[string]any{"runner": info, "observations": obs}, &out)
	return out.Results, err
}

// TakeCommand asks for a command on a rollout of the runner's targets,
// waiting up to wait for one; nil when there is none.
func (c *Client) TakeCommand(ctx context.Context, info engine.RunnerInfo, wait time.Duration) (*engine.CommandJob, error) {
	var job engine.CommandJob
	code, err := c.call(ctx, "deploy/commands", map[string]any{"runner": info, "wait_seconds": int(wait / time.Second)}, &job)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	return &job, nil
}

// FinishCommand reports what was done with a command.
func (c *Client) FinishCommand(ctx context.Context, info engine.RunnerInfo, res engine.CommandResult) error {
	_, err := c.call(ctx, "deploy/command-done", map[string]any{"runner": info, "result": res}, nil)
	return err
}
