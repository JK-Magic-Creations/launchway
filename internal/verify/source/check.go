package source

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/deploy/inspect"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/verify"
)

// The built-in health proof. A source of type check is the deploy runner's
// own list of what a pipeline's health check may call. A pipeline names the
// source and an address; the runner makes the calls itself, from where it
// sits (in the cluster), over the whole window, and reports each one.
//
// Safety limits, all enforced here and not by the pipeline:
//
//   - the project must be one of the source's projects;
//   - the address's host must match the source's hosts, or be a Service in
//     one of its namespaces (<service>.<namespace>[.svc[.cluster.local]]);
//     an address literal must be in a listed range; nothing else is called;
//   - the connection goes only to what the host resolves to when the call is
//     made (egress.Guard, so a name that answers differently later cannot
//     lead somewhere else), no redirect is followed and no proxy is used;
//   - calls are GET or HEAD, with at most 30 s each, and the answer read is
//     at most 64 KiB; credentials come from the runner's secret stores and
//     are never reported or logged.
type checkAllow struct {
	hosts []string
	nets  []netip.Prefix
	ns    []string
	roots *x509.CertPool
}

const maxCheckBody = 64 << 10

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}

// openCheck opens a check source. For any other type it only refuses the
// settings that belong to checks, and says it is not done (done false).
func openCheck(name string, c Config) (s *src, done bool, err error) {
	if c.Type != Check {
		if len(c.Hosts) > 0 || len(c.Namespaces) > 0 || c.AllowInsecureTLS {
			return nil, true, fmt.Errorf("hosts, namespaces and allow_insecure_tls are for check sources")
		}
		return nil, false, nil
	}
	s, err = openCheckSource(name, c)
	return s, true, err
}

func openCheckSource(name string, c Config) (*src, error) {
	switch {
	case c.URL != "" || c.Auth != nil || len(c.Statements) > 0 || len(c.Headers) > 0 || len(c.Indices) > 0:
		return nil, fmt.Errorf("a check source calls the addresses pipelines give; it takes no url, auth, headers or statements, only hosts and namespaces")
	case len(c.Hosts) == 0 && len(c.Namespaces) == 0:
		return nil, fmt.Errorf("a check source needs hosts or namespaces: where checks may call (an empty list allows nothing)")
	}
	for _, p := range c.Projects {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("project pattern %q is not a valid pattern", p)
		}
	}
	a := &checkAllow{}
	for _, h := range c.Hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if p, err := netip.ParsePrefix(h); err == nil {
			a.nets = append(a.nets, p.Masked())
			continue
		}
		if ip, err := netip.ParseAddr(h); err == nil {
			a.nets = append(a.nets, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
			continue
		}
		if h == "" || h == "*" || strings.ContainsAny(strings.TrimPrefix(h, "*."), "/:@ *") {
			return nil, fmt.Errorf("hosts: %q: give a host name (api.example.com or *.shop.svc.cluster.local), an address or a range such as 10.0.0.0/8", h)
		}
		a.hosts = append(a.hosts, h)
	}
	for _, ns := range c.Namespaces {
		if !dnsLabel.MatchString(ns) {
			return nil, fmt.Errorf("namespaces: %q is not a Kubernetes namespace name", ns)
		}
		a.ns = append(a.ns, ns)
	}
	s := &src{name: name, cfg: c, timeout: DefaultTimeout, check: a}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading ca_file: %v", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s has no PEM certificates", c.CAFile)
		}
		a.roots = pool
	}
	return s, nil
}

// permits says whether the host may be called, and why not.
func (a *checkAllow) permits(host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		for _, n := range a.nets {
			if n.Contains(ip.Unmap()) {
				return nil
			}
		}
		return fmt.Errorf("the address %s is not in this source's hosts (it allows: %s)", host, a.describe())
	}
	for _, h := range a.hosts {
		if h == host || (strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:])) {
			return nil
		}
	}
	// <service>.<namespace>[.svc[.cluster.local]]
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		rest := strings.Join(parts[2:], ".")
		if rest == "" || rest == "svc" || rest == "svc.cluster.local" {
			for _, ns := range a.ns {
				if parts[1] == ns {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("the host %s is not one this source allows (it allows: %s)", host, a.describe())
}

func (a *checkAllow) describe() string {
	var l []string
	l = append(l, a.hosts...)
	for _, n := range a.nets {
		l = append(l, n.String())
	}
	for _, ns := range a.ns {
		l = append(l, "Services in namespace "+ns)
	}
	return strings.Join(l, ", ")
}

// CheckResult is what a health check read over a window.
type CheckResult struct {
	Points   []verify.Point
	Attempts []verify.Attempt
	// Note is one sentence about the failed calls, empty when none failed.
	Note string
}

// Check calls what the check names, from the start of the window until its
// end, one call every check.Every, and returns what each call saw.
func (s *Sources) Check(ctx context.Context, name, project string, c *pipeline.HealthCheck, from, to time.Time) (CheckResult, error) {
	v, ok := s.byName[name]
	fail := func(format string, a ...any) (CheckResult, error) {
		return CheckResult{}, &Error{Source: name, Reason: fmt.Sprintf(format, a...)}
	}
	switch {
	case !ok:
		return fail("this deploy runner has no health source of that name")
	case v.check == nil:
		return fail("this source is not of type check")
	case len(v.cfg.Projects) == 0:
		return fail("this source allows no project: its projects list in the deploy runner's configuration is empty, and an empty list denies every project")
	case !matchAny(v.cfg.Projects, project):
		return fail("project %s may not use this source (it allows: %s)", project, strings.Join(v.cfg.Projects, ", "))
	case c == nil:
		return fail("the metric has no check")
	}
	host, port, _, err := c.Target()
	if err != nil || host == "" {
		return fail("the address %q is not valid", clip(c.URL, 100))
	}
	if err := v.check.permits(host); err != nil {
		return fail("%v", err)
	}
	if c.InsecureTLS && !v.cfg.AllowInsecureTLS {
		return fail("this source does not allow insecure_tls (its allow_insecure_tls is off); the check can name server_name instead")
	}
	headers := map[string]string{}
	var secrets []string
	for k, val := range c.Headers {
		headers[k] = val
	}
	for k, ref := range c.SecretHeaders {
		if s.lookup == nil {
			return fail("the check needs the secret for header %s, but this runner has no secret stores", k)
		}
		val, err := s.lookup(ctx, project, ref)
		if err != nil {
			return fail("could not read the secret for header %s (%s): %v", k, ref.String(), err)
		}
		headers[k] = val
		secrets = append(secrets, val)
	}
	every := c.Every.D()
	if every < 100*time.Millisecond {
		every = pipeline.DefaultCheckEvery
	}
	timeout := c.Timeout.D()
	if timeout <= 0 || timeout > pipeline.MaxCheckTimeout {
		timeout = pipeline.DefaultCheckTimeout
	}
	guard := egress.New(egress.Policy{})
	if err := guard.Set(egress.Policy{Allow: []string{host}}); err != nil {
		return fail("the host %q cannot be called", host)
	}
	call := s.httpCall(v, c, guard, headers, timeout)
	if c.Kind == pipeline.CheckTCP {
		call = tcpCall(guard, net.JoinHostPort(host, port), timeout)
	}
	var res CheckResult
	for at := from; at.Before(to) && len(res.Attempts) < verify.MaxAttempts; at = at.Add(every) {
		if w := time.Until(at); w > 0 {
			select {
			case <-time.After(w):
			case <-ctx.Done():
				return s.finishCheck(res, c, secrets), nil
			}
		}
		if ctx.Err() != nil {
			break
		}
		a := call(ctx)
		for _, sec := range secrets {
			a.Error = mask(a.Error, sec)
		}
		res.Attempts = append(res.Attempts, a)
		val := 0.0
		if a.OK {
			val = 1
		}
		res.Points = append(res.Points, verify.Point{T: a.T, V: val})
	}
	return s.finishCheck(res, c, secrets), nil
}

func (s *Sources) finishCheck(res CheckResult, c *pipeline.HealthCheck, _ []string) CheckResult {
	res.Note = CheckNote(res.Attempts, c)
	return res
}

type callFunc func(ctx context.Context) verify.Attempt

func tcpCall(g *egress.Guard, addr string, timeout time.Duration) callFunc {
	dial := g.Dialer(timeout)
	return func(ctx context.Context) verify.Attempt {
		start := time.Now()
		a := verify.Attempt{T: start.UnixMilli()}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		conn, err := dial(cctx, "tcp", addr)
		a.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			a.Error = transportWords(err, timeout)
			return a
		}
		_ = conn.Close()
		a.OK = true
		return a
	}
}

func (s *Sources) httpCall(v *src, c *pipeline.HealthCheck, g *egress.Guard, headers map[string]string, timeout time.Duration) callFunc {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: v.check.roots, ServerName: c.ServerName}
	if c.InsecureTLS {
		tc.InsecureSkipVerify = true // #nosec G402 -- the operator's source allows it (allow_insecure_tls)
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           g.Dialer(timeout),
			TLSClientConfig:       tc,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			DisableKeepAlives:     true,
			Proxy:                 nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(ctx context.Context) verify.Attempt {
		start := time.Now()
		a := verify.Attempt{T: start.UnixMilli()}
		req, err := http.NewRequestWithContext(ctx, c.Method, c.URL, nil)
		if err != nil {
			a.Error = "the request could not be made"
			return a
		}
		req.Header.Set("User-Agent", userAgent)
		for k, val := range headers {
			if strings.EqualFold(k, "host") {
				req.Host = val
				continue
			}
			req.Header.Set(k, val)
		}
		resp, err := client.Do(req)
		if err != nil {
			a.LatencyMS = time.Since(start).Milliseconds()
			a.Error = transportWords(err, timeout)
			return a
		}
		defer resp.Body.Close()
		a.Status = resp.StatusCode
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxCheckBody))
		a.LatencyMS = time.Since(start).Milliseconds()
		a.Excerpt = excerpt(body)
		switch {
		case c.MaxLatency > 0 && time.Since(start) > time.Duration(c.MaxLatency) && c.StatusMatches(resp.StatusCode):
			a.Error = fmt.Sprintf("answered %d but took %d ms, over max_latency %s", resp.StatusCode, a.LatencyMS, time.Duration(c.MaxLatency))
		case !c.StatusMatches(resp.StatusCode):
			a.Error = "returned " + strconv.Itoa(resp.StatusCode)
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				a.Error += " (a redirect; health checks do not follow redirects, so call the address it points to)"
			}
		case c.BodyContains != "" && !bytes.Contains(body, []byte(c.BodyContains)):
			a.Error = fmt.Sprintf("answered %d but the body did not contain %q", resp.StatusCode, c.BodyContains)
		case c.JSONPath != "":
			if why := jsonMatch(body, c.JSONPath, c.JSONEquals); why != "" {
				a.Error = fmt.Sprintf("answered %d but %s", resp.StatusCode, why)
				break
			}
			a.OK = true
		default:
			a.OK = true
		}
		return a
	}
}

// jsonMatch checks the JSON body's value at a dotted path (numbers index
// arrays) against the text, and says what is wrong, or "".
func jsonMatch(body []byte, p, want string) string {
	var doc any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return "the body is not JSON"
	}
	cur := doc
	for _, part := range strings.Split(p, ".") {
		switch x := cur.(type) {
		case map[string]any:
			n, ok := x[part]
			if !ok {
				return fmt.Sprintf("the JSON has no %q", p)
			}
			cur = n
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return fmt.Sprintf("the JSON has no %q", p)
			}
			cur = x[i]
		default:
			return fmt.Sprintf("the JSON has no %q", p)
		}
	}
	got := ""
	switch x := cur.(type) {
	case string:
		got = x
	case json.Number:
		got = x.String()
	case bool:
		got = strconv.FormatBool(x)
	case nil:
		got = "null"
	default:
		return fmt.Sprintf("%q is not a single value", p)
	}
	if got != want {
		return fmt.Sprintf("%s was %q, not %q", p, clip(got, 40), want)
	}
	return ""
}

// transportWords says why a call got no answer, without addresses.
func transportWords(err error, timeout time.Duration) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "did not answer within " + timeout.String()
	case errors.Is(err, context.Canceled):
		return "was cancelled"
	case errors.Is(err, egress.ErrBlocked):
		return "was refused by the egress guard"
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "dial" {
		msg := oe.Err.Error()
		switch {
		case strings.Contains(msg, "refused"):
			return "could not connect (connection refused)"
		case strings.Contains(msg, "no route"), strings.Contains(msg, "unreachable"):
			return "could not connect (no route to the service)"
		}
		return "could not connect"
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return "could not find the host"
	}
	var ce *tls.CertificateVerificationError
	if errors.As(err, &ce) {
		return "the certificate was not accepted (" + clip(ce.Err.Error(), 80) + ")"
	}
	return "failed (" + clip(strings.TrimSpace(err.Error()), 100) + ")"
}

// CheckNote says in one sentence which calls failed: "3 of 20 requests to
// /healthz returned 503". It is empty when all were healthy.
func CheckNote(as []verify.Attempt, c *pipeline.HealthCheck) string {
	failed := 0
	reasons := map[string]int{}
	for _, a := range as {
		if !a.OK {
			failed++
			reasons[a.Error]++
		}
	}
	if failed == 0 {
		return ""
	}
	what, where := "requests", ""
	if c != nil {
		if _, _, p, err := c.Target(); err == nil && c.Kind != pipeline.CheckTCP {
			where = p
			if i := strings.IndexByte(where, '?'); i >= 0 {
				where = where[:i]
			}
		} else {
			what, where = "connections", c.URL
		}
	}
	head := fmt.Sprintf("%d of %d %s", failed, len(as), what)
	if where != "" {
		head += " to " + where
	}
	if len(reasons) == 1 {
		for r := range reasons {
			return head + " " + r
		}
	}
	type kv struct {
		r string
		n int
	}
	var list []kv
	for r, n := range reasons {
		list = append(list, kv{r, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n || (list[i].n == list[j].n && list[i].r < list[j].r) })
	var parts []string
	for _, e := range list {
		parts = append(parts, fmt.Sprintf("%d %s", e.n, e.r))
	}
	return head + " failed: " + strings.Join(parts, ", ")
}

// maxExcerpt bounds the part of an answer a call keeps.
const maxExcerpt = 200

// excerpt is the start of an answer as text a person can read, with
// anything shaped like a credential hidden.
func excerpt(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	n := min(len(body), maxExcerpt*2)
	s := strings.ToValidUTF8(string(body[:n]), "?")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = inspect.NewRedactor().Text(s)
	if len(s) > maxExcerpt {
		s = s[:maxExcerpt] + "…"
	}
	return s
}
