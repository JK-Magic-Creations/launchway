// Package runner takes steps from a Slipway server and executes them.
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
	"time"

	"slipway/internal/engine"
)

// ErrLeaseLost means the server no longer recognises the lease; the step
// has been given to someone else or canceled, and this runner must stop it.
var ErrLeaseLost = errors.New("lease lost")

// Client talks to the server's runner API.
type Client struct {
	Server string // base URL, e.g. http://127.0.0.1:8080
	Token  string
	HTTP   *http.Client
}

func NewClient(server, token string) *Client {
	return &Client{Server: strings.TrimRight(server, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

func (c *Client) call(ctx context.Context, path string, in, out any) (int, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/runner/v1/"+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusGone:
		return resp.StatusCode, ErrLeaseLost
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

func (c *Client) Finish(ctx context.Context, lease string, res engine.Result) error {
	_, err := c.call(ctx, "finish", map[string]any{"lease_id": lease, "result": res}, nil)
	return err
}
