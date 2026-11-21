package fleet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Checker judges one host (or the balancer) healthy.
type Checker interface {
	Healthy(ctx context.Context, url string) error
}

// HTTPCheck is the pipeline's health check (check:) made against one
// address. {service} in the check's URL stands for the host's service
// address, so the same check is made against each host directly and against
// the balancer.
type HTTPCheck struct {
	Check  pipeline.HealthCheck
	Client *http.Client
}

// URLFor returns the check's URL for an address.
func (c *HTTPCheck) URLFor(service string) string {
	return strings.ReplaceAll(c.Check.URL, "{service}", service)
}

// Healthy makes one call and says why it was not healthy.
func (c *HTTPCheck) Healthy(ctx context.Context, url string) error {
	cl := c.Client
	if cl == nil {
		to := c.Check.Timeout.D()
		if to <= 0 {
			to = 3 * time.Second
		}
		cl = &http.Client{Timeout: to, Transport: &http.Transport{DisableKeepAlives: true}}
	}
	method := c.Check.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	for k, v := range c.Check.Headers {
		if strings.EqualFold(k, "host") {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if !statusOK(resp.StatusCode, c.Check.Status) {
		return fmt.Errorf("answered %d", resp.StatusCode)
	}
	if c.Check.BodyContains != "" && !strings.Contains(string(body), c.Check.BodyContains) {
		return fmt.Errorf("the answer lacks %q", c.Check.BodyContains)
	}
	return nil
}

func statusOK(code int, want []string) bool {
	if len(want) == 0 {
		return code >= 200 && code < 300
	}
	for _, w := range want {
		switch {
		case len(w) == 3 && w[1:] == "xx":
			if code/100 == int(w[0]-'0') {
				return true
			}
		case strings.Contains(w, "-"):
			var lo, hi int
			if _, err := fmt.Sscanf(w, "%d-%d", &lo, &hi); err == nil && code >= lo && code <= hi {
				return true
			}
		default:
			var n int
			if _, err := fmt.Sscanf(w, "%d", &n); err == nil && n == code {
				return true
			}
		}
	}
	return false
}

// waitHealthy calls until the check passes `need` times in a row or the time
// runs out.
func waitHealthy(ctx context.Context, c Checker, url string, need int, every, within time.Duration) error {
	deadline := time.Now().Add(within)
	var last error
	ok := 0
	for {
		if err := c.Healthy(ctx, url); err == nil {
			if ok++; ok >= need {
				return nil
			}
		} else {
			ok, last = 0, err
		}
		if time.Now().After(deadline) {
			if last == nil {
				last = fmt.Errorf("not enough consecutive passes")
			}
			return fmt.Errorf("not healthy within %s: %w", within, last)
		}
		select {
		case <-time.After(every):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
