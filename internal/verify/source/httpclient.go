package source

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// userAgent is sent with every request.
const userAgent = "launchway-verify"

// client returns the source's HTTP client: it follows no redirects and
// trusts the source's extra CA. Timeouts come from the query's context.
func (s *src) client() *http.Client {
	s.once.Do(func() {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.roots}
		tr.ResponseHeaderTimeout = s.timeout
		s.hc = &http.Client{
			Transport: tr,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	})
	return s.hc
}

// response is an answer read within the size limit.
type response struct {
	status int
	header http.Header
	body   []byte
}

// send sends one request and reads the answer, at most the source's
// max_bytes of it. It returns an error for no answer, a redirect, a
// refusal (401, 403) or an answer too large; other statuses are the
// caller's to judge.
func (s *src) send(ctx context.Context, cred credential, method, u string, body []byte, contentType string, extra http.Header) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, errorf("could not build the request: %v", err)
	}
	if body == nil {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range extra {
		req.Header[k] = v
	}
	if a := s.cfg.Auth; a != nil && s.cfg.Type != ClickHouse {
		switch a.Type {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+cred.secret)
		case "basic":
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cred.user+":"+cred.secret)))
		case "header":
			req.Header.Set(a.Header, cred.secret)
		}
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, s.transportError(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(s.limits.MaxBytes)+1))
	if err != nil {
		return nil, s.transportError(ctx, err)
	}
	if len(data) > s.limits.MaxBytes {
		return nil, errorf("the response was larger than %s, the most this source allows (limits.max_bytes); narrow the query", sizeText(s.limits.MaxBytes))
	}
	r := &response{status: resp.StatusCode, header: resp.Header, body: data}
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		where := resp.Header.Get("Location")
		if lu, err := url.Parse(where); err == nil {
			lu.RawQuery, lu.User = "", nil
			where = lu.String()
		}
		return nil, errorf("answered with a redirect (%s) to %s; health sources do not follow redirects, so set the source's url to where it points", resp.Status, where)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errorf("refused the request (%s): check the deploy runner's credentials for this source", resp.Status)
	}
	return r, nil
}

// transportError says why a request got no answer.
func (s *src) transportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return unavailablef("the source did not answer within %s", s.timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return errorf("the query was cancelled")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "dial" {
		return unavailablef("could not connect to the source: %v", oe.Err)
	}
	return unavailablef("the request failed: %v", err)
}

// statusError describes a failed status with the server's message.
func statusError(r *response, msg string) error {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = snippet(r.body)
	}
	text := fmt.Sprintf("answered %d %s", r.status, http.StatusText(r.status))
	if msg != "" {
		text += ": " + msg
	}
	if r.status == http.StatusNotFound {
		text += " (check the source's url and type)"
	}
	if r.status == http.StatusTooManyRequests || r.status >= 500 {
		return unavailablef("%s", text)
	}
	return errorf("%s", text)
}

// snippet is the start of a body, for an error message.
func snippet(b []byte) string {
	const max = 300
	s := strings.TrimSpace(string(b))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "..."
	}
	return s
}

// decodeJSON decodes a JSON answer, or says that it is not JSON.
func decodeJSON(r *response, out any) error {
	dec := json.NewDecoder(bytes.NewReader(r.body))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		ct := r.header.Get("Content-Type")
		if ct == "" {
			ct = "no content type"
		}
		return errorf("returned something that is not JSON (%d, %s): %q", r.status, ct, snippet(r.body))
	}
	return nil
}

func sizeText(n int) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// joinURL appends p to the source's base URL.
func joinURL(base, p string) string {
	return strings.TrimRight(base, "/") + p
}

// unixSeconds formats t as Unix seconds with milliseconds.
func unixSeconds(t time.Time) string {
	ms := t.UnixMilli()
	sign := ""
	if ms < 0 {
		sign, ms = "-", -ms
	}
	return fmt.Sprintf("%s%d.%03d", sign, ms/1000, ms%1000)
}
