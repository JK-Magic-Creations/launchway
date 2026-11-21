package pipeline

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// A built-in health proof. A verify metric may carry a check instead of a
// query: the deploy runner itself calls the service it just deployed, again
// and again over the window, and the share of calls that answered as
// expected is the metric. Nothing has to scrape the service first.
//
//	metrics:
//	  - name: healthy
//	    source: checks            # a source of type check on the deploy runner
//	    check:
//	      url: http://api.shop.svc.cluster.local:8080/healthz
//	      status: [200]
//	      success: 0.95           # the share of calls that must answer as expected, per interval
const (
	CheckHTTP = "http"
	CheckTCP  = "tcp"
)

// Limits of health checks.
const (
	DefaultCheckTimeout = 5 * time.Second
	MaxCheckTimeout     = 30 * time.Second
	DefaultCheckEvery   = 5 * time.Second
	MinCheckEvery       = time.Second
	MaxCheckHeaders     = 10
	MaxCheckMatchBytes  = 512
)

// HealthCheck is what the deploy runner calls to prove a deployment healthy.
type HealthCheck struct {
	Kind string `json:"kind"` // http (the default) or tcp
	// URL is http://host:port/path or https://...; for tcp, host:port.
	URL    string `json:"url"`
	Method string `json:"method,omitempty"` // GET (the default) or HEAD
	// Status lists the statuses that count as healthy: 200, or a class such
	// as 2xx, or a range such as 200-299. Default: 2xx.
	Status []string `json:"status,omitempty"`
	// BodyContains must appear in the first 64 KiB of the answer.
	BodyContains string `json:"body_contains,omitempty"`
	// JSONPath and JSONEquals say that the answer is JSON whose value at the
	// dotted path equals the text (for example path status, equals ok).
	JSONPath   string `json:"json_path,omitempty"`
	JSONEquals string `json:"json_equals,omitempty"`
	// Headers are sent as written (not secret); Host sets the request's
	// host, for a service that routes by it.
	Headers map[string]string `json:"headers,omitempty"`
	// SecretHeaders take their values from secret stores, looked up by the
	// deploy runner for each interval; the values are never shown.
	SecretHeaders map[string]SecretRef `json:"secret_headers,omitempty"`
	Timeout       Duration             `json:"timeout,omitempty"`
	Every         Duration             `json:"every,omitempty"`
	// Success is the share of calls in an interval that must be healthy.
	Success float64 `json:"success"`
	// ServerName is the name to verify in the TLS certificate when the
	// address is an IP or a Service name that differs from it.
	ServerName string `json:"server_name,omitempty"`
	// InsecureTLS skips certificate verification; the deploy runner's source
	// must allow it (allow_insecure_tls).
	InsecureTLS bool `json:"insecure_tls,omitempty"`
}

var (
	checkKeys     = []string{"kind", "url", "method", "status", "body_contains", "json", "headers", "secret_headers", "timeout", "every", "success", "server_name", "insecure_tls"}
	checkJSONKeys = []string{"path", "equals"}
	statusRe      = regexp.MustCompile(`^([1-5][0-9]{2}|[1-5]xx|[1-5][0-9]{2}-[1-5][0-9]{2})$`)
)

// StatusMatches reports whether a status is one of the listed (default 2xx).
func (c *HealthCheck) StatusMatches(code int) bool {
	if len(c.Status) == 0 {
		return code >= 200 && code < 300
	}
	for _, s := range c.Status {
		switch {
		case strings.HasSuffix(s, "xx"):
			if code/100 == int(s[0]-'0') {
				return true
			}
		case strings.Contains(s, "-"):
			lo, hi, _ := strings.Cut(s, "-")
			a, _ := strconv.Atoi(lo)
			b, _ := strconv.Atoi(hi)
			if code >= a && code <= b {
				return true
			}
		default:
			if n, _ := strconv.Atoi(s); n == code {
				return true
			}
		}
	}
	return false
}

// Target is where the check connects: host and port, and the path for http.
func (c *HealthCheck) Target() (host, port, path string, err error) {
	if c.Kind == CheckTCP {
		host, port, err = net.SplitHostPort(c.URL)
		return host, port, "", err
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return "", "", "", err
	}
	port = u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return strings.ToLower(u.Hostname()), port, u.RequestURI(), nil
}

func (ps *parser) parseHealthCheck(n *yaml.Node, at string) *HealthCheck {
	c := &HealthCheck{Kind: CheckHTTP, Success: 1, Timeout: Duration(DefaultCheckTimeout), Every: Duration(DefaultCheckEvery)}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "check is a mapping, for example: { url: http://api.shop.svc.cluster.local:8080/healthz, status: [200] }")
		return c
	}
	dur := func(v *yaml.Node, where string) (Duration, bool) {
		d, err := time.ParseDuration(ps.scalar(v, where))
		if err != nil {
			ps.add(v, where, "write a duration such as 5s")
			return 0, false
		}
		return Duration(d), true
	}
	ps.eachKey(n, at, checkKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "kind":
			c.Kind = ps.scalar(v, where)
		case "url":
			c.URL = strings.TrimSpace(ps.scalar(v, where))
		case "method":
			c.Method = strings.ToUpper(ps.scalar(v, where))
		case "status":
			c.Status = ps.valueList(v, where)
		case "body_contains":
			c.BodyContains = ps.scalar(v, where)
		case "json":
			ps.eachKey(v, where, checkJSONKeys, func(k2, v2 *yaml.Node) {
				if k2.Value == "path" {
					c.JSONPath = ps.scalar(v2, where+".path")
				} else {
					c.JSONEquals = ps.scalar(v2, where+".equals")
				}
			})
		case "headers":
			c.Headers = ps.checkHeaders(v, where)
		case "secret_headers":
			c.SecretHeaders = ps.checkSecretHeaders(v, where)
		case "timeout":
			if d, ok := dur(v, where); ok {
				c.Timeout = d
			}
		case "every":
			if d, ok := dur(v, where); ok {
				c.Every = d
			}
		case "success":
			f, err := strconv.ParseFloat(ps.scalar(v, where), 64)
			if err != nil || f <= 0 || f > 1 {
				ps.add(v, where, "success is the share of calls that must be healthy, above 0 and at most 1 (0.95 allows one failure in twenty)")
			} else {
				c.Success = f
			}
		case "server_name":
			c.ServerName = ps.scalar(v, where)
		case "insecure_tls":
			c.InsecureTLS = ps.bool(v, where)
		}
	})
	ps.checkHealthCheck(n, at, c)
	return c
}

func (ps *parser) checkHealthCheck(n *yaml.Node, at string, c *HealthCheck) {
	if c.Kind != CheckHTTP && c.Kind != CheckTCP {
		ps.add(n, at+".kind", "kind is http (the default) or tcp (gRPC health checks are not built in yet)")
		return
	}
	ps.checkCheckAddress(n, at, c)
	ps.checkCheckSettings(n, at, c)
}

func (ps *parser) checkCheckAddress(n *yaml.Node, at string, c *HealthCheck) {
	switch {
	case c.URL == "":
		ps.add(n, at+".url", "give the address the deploy runner calls, for example http://api.shop.svc.cluster.local:8080/healthz")
	case strings.Contains(c.URL, "{{"):
		ps.add(n, at+".url", "the address is written out in full; it takes no placeholders")
	case c.Kind == CheckHTTP:
		u, err := url.Parse(c.URL)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
			ps.add(n, at+".url", "%q is not an http or https address", clipText(c.URL, 80))
		case u.User != nil:
			ps.add(n, at+".url", "the address has a user or password in it; use secret_headers for a credential")
		case u.Fragment != "":
			ps.add(n, at+".url", "the address has a fragment, which is never sent")
		}
	default:
		if _, port, err := net.SplitHostPort(c.URL); err != nil || port == "" {
			ps.add(n, at+".url", "a tcp check's address is host:port, for example db.shop.svc.cluster.local:5432")
		}
	}
}

func (ps *parser) checkCheckSettings(n *yaml.Node, at string, c *HealthCheck) {
	if c.Kind == CheckTCP && (c.Method != "" || len(c.Status) > 0 || c.BodyContains != "" || c.JSONPath != "" || len(c.Headers) > 0 || len(c.SecretHeaders) > 0) {
		ps.add(n, at, "a tcp check only connects; method, status, body_contains, json and headers are for http checks")
	}
	if c.Method == "" {
		c.Method = "GET"
	}
	if c.Method != "GET" && c.Method != "HEAD" {
		ps.add(n, at+".method", "method is GET or HEAD: a health check never changes anything")
	}
	if c.Method == "HEAD" && (c.BodyContains != "" || c.JSONPath != "") {
		ps.add(n, at+".method", "a HEAD answer has no body to match")
	}
	for _, s := range c.Status {
		if !statusRe.MatchString(s) {
			ps.add(n, at+".status", "%q is not a status: write 200, a class such as 2xx, or a range such as 200-299", clipText(s, 20))
		}
	}
	if (c.JSONPath == "") != (c.JSONEquals == "") {
		ps.add(n, at+".json", "json needs both path and equals, for example json: { path: status, equals: ok }")
	}
	if len(c.BodyContains) > MaxCheckMatchBytes || len(c.JSONEquals) > MaxCheckMatchBytes {
		ps.add(n, at, "a text to match is at most %d bytes", MaxCheckMatchBytes)
	}
	if t := c.Timeout.D(); t < time.Second || t > MaxCheckTimeout {
		ps.add(n, at+".timeout", "timeout is 1s to %s", Duration(MaxCheckTimeout).Short())
	}
	if e := c.Every.D(); e < MinCheckEvery {
		ps.add(n, at+".every", "every is at least %s", Duration(MinCheckEvery).Short())
	}
	if c.InsecureTLS && c.Kind == CheckHTTP && !strings.HasPrefix(c.URL, "https:") {
		ps.add(n, at+".insecure_tls", "insecure_tls is for https addresses")
	}
}

func (ps *parser) checkHeaders(n *yaml.Node, at string) map[string]string {
	out := map[string]string{}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "headers is a mapping of header names to values")
		return out
	}
	if len(n.Content)/2 > MaxCheckHeaders {
		ps.add(n, at, "at most %d headers", MaxCheckHeaders)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		val := ps.scalar(v, at+"."+k.Value)
		switch low := strings.ToLower(k.Value); {
		case !headerNameRe.MatchString(k.Value) || strings.ContainsAny(val, "\r\n"):
			ps.add(k, at+"."+k.Value, "%q is not a valid header", clipText(k.Value, 40))
		case slices.Contains([]string{"authorization", "proxy-authorization", "cookie", "content-length", "transfer-encoding"}, low):
			ps.add(k, at+"."+k.Value, "put %s under secret_headers, so its value comes from a secret store and is never written in the file", k.Value)
		}
		out[k.Value] = val
	}
	return out
}

var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

func (ps *parser) checkSecretHeaders(n *yaml.Node, at string) map[string]SecretRef {
	out := map[string]SecretRef{}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "secret_headers is a mapping of header names to references, for example: Authorization: { store: vault, key: apps/shop/health, field: header }")
		return out
	}
	if len(n.Content)/2 > MaxCheckHeaders {
		ps.add(n, at, "at most %d headers", MaxCheckHeaders)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		where := at + "." + k.Value
		if !headerNameRe.MatchString(k.Value) {
			ps.add(k, where, "%q is not a valid header name", clipText(k.Value, 40))
		}
		var ref SecretRef
		ps.eachKey(v, where, secretKeys, func(k2, v2 *yaml.Node) {
			switch k2.Value {
			case "store":
				ref.Store = ps.secretText(v2, where+".store")
			case "key":
				ref.Key = ps.secretText(v2, where+".key")
			case "field":
				ref.Field = ps.secretText(v2, where+".field")
			}
		})
		if v.Kind == yaml.MappingNode && (!ValidStoreName(ref.Store) || strings.TrimSpace(ref.Key) == "") {
			ps.add(v, where, "write a secret reference: { store: vault, key: apps/shop/health, field: token }")
		}
		out[k.Value] = ref
	}
	return out
}

// String says in a few words what the check calls, without credentials.
func (c *HealthCheck) String() string {
	return fmt.Sprintf("%s %s", c.Kind, c.URL)
}

// finishVerifyMetric checks a parsed metric as a whole.
func (ps *parser) finishVerifyMetric(n *yaml.Node, at string, m *VerifyMetric) *VerifyMetric {
	if n.Kind != yaml.MappingNode {
		return m
	}
	if m.Check != nil {
		ps.checkMetricCheck(n, at, m)
	} else {
		ps.checkVerifySource(n, at, m.Name, m.Source, m.Query, m.StableQuery, m.Statement)
	}
	if m.Direction == "" {
		m.Direction = "higher-is-worse"
	}
	if m.Max != nil && m.Min != nil && *m.Min > *m.Max {
		ps.add(n, at, "min is above max")
	}
	return m
}

// checkMetricCheck completes a metric that carries a check: the metric is
// the share of healthy calls in each interval, which is worse when lower,
// and must reach the check's success share; earlier verifications are not
// compared (a health proof is not a quantity that drifts).
func (ps *parser) checkMetricCheck(n *yaml.Node, at string, m *VerifyMetric) {
	switch {
	case m.Name == "":
		ps.add(n, at+".name", "a name is required, for example: name: healthy")
	case m.Source == "":
		ps.add(n, at+".source", "name the deploy runner's source of type check that may make this call, for example: source: checks")
	case !verifyNameRe.MatchString(m.Source):
		ps.add(n, at+".source", "%q is not a health source name", m.Source)
	case m.Query != "" || m.Statement != "" || m.StableQuery != "" || m.Index != "":
		ps.add(n, at, "a metric has a check or a query, not both")
	}
	if m.Direction != "" && m.Direction != "lower-is-worse" {
		ps.add(n, at+".direction", "a check's metric is the share of healthy calls, so lower is worse; leave direction out")
	}
	m.Direction = "lower-is-worse"
	if m.Min == nil && m.Max == nil && m.FailFastMin == nil {
		lo := m.Check.Success
		m.Min = &lo
	}
	m.NoBaseline = true
}
