// Package source reads the metrics and logs a deployment verification
// judges, from the health sources an operator configured on a deploy
// runner: Prometheus (and servers with its query API, such as Thanos, Mimir
// and VictoriaMetrics), Loki, ClickHouse, Elasticsearch or OpenSearch,
// PostgreSQL, and any HTTP service that answers with JSON.
//
// A pipeline's verify step names a source and gives a query. It must not be
// able to turn a source into a way to read arbitrary data or to write, so:
//
//   - a source serves only the projects its configuration lists (none when
//     the list is empty);
//   - for ClickHouse, PostgreSQL and HTTP, pipelines run only statements the
//     operator wrote, by name, and pass typed parameters to them; values
//     are sent as the backend's own query parameters, never pasted into SQL;
//   - in PromQL and LogQL a parameter may only appear inside a quoted
//     string, as a label value or line filter, and its value is escaped for
//     that string (and for a regular expression where the string is one);
//   - an Elasticsearch query is parsed as JSON, scripts are refused and
//     parameters are put into string values after parsing;
//   - every query runs read-only where the backend can enforce it, with a
//     timeout, and with limits on series, points, lines and response bytes.
//
// Operators should still give each source an account that can only read
// what verifications need.
package source

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/verify"
)

// Source types.
const (
	Prometheus    = "prometheus"
	Loki          = "loki"
	ClickHouse    = "clickhouse"
	Elasticsearch = "elasticsearch"
	Postgres      = "postgres"
	HTTP          = "http"
)

// Kinds of query.
const (
	Metric = "metric"
	Logs   = "logs"
)

// Defaults and bounds.
const (
	DefaultTimeout  = 10 * time.Second
	MaxTimeout      = 60 * time.Second
	DefaultStep     = 30 * time.Second
	MinStep         = 5 * time.Second
	DefaultMaxLines = 5000
	DefaultMaxBytes = 8 << 20
	MaxQueryBytes   = 8 << 10 // a pipeline's query text
	MaxValueBytes   = 4 << 10 // one parameter value
)

// Config is one health source, as an operator writes it in the deploy
// runner's configuration.
type Config struct {
	// Type is prometheus (also for Thanos, Mimir, VictoriaMetrics and other
	// servers with the same query API), loki, clickhouse, elasticsearch
	// (opensearch is the same), postgres or http.
	Type string `yaml:"type" json:"type"`
	// URL is the base URL, http or https. For postgres it is a postgres://
	// URL without a password: the password comes from Auth.
	URL string `yaml:"url" json:"url"`
	// Auth is how to log in, if the source needs it.
	Auth *Auth `yaml:"auth,omitempty" json:"auth,omitempty"`
	// Headers are fixed headers that are not secret, such as X-Scope-OrgID
	// to choose a Loki or Mimir tenant. Credentials go in Auth, so
	// Authorization, Cookie and the like are refused here.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// CAFile is a PEM file of extra certificate authorities to trust.
	CAFile string `yaml:"ca_file,omitempty" json:"ca_file,omitempty"`
	// Projects are path.Match patterns of the projects that may use the
	// source. An empty list allows none.
	Projects []string `yaml:"projects" json:"projects"`
	// Timeout bounds each query: default 10s, at most 60s.
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Limits  Limits `yaml:"limits,omitempty" json:"limits,omitempty"`
	// Statements, for clickhouse, postgres and http, are the only queries
	// pipelines can run there, by name.
	Statements map[string]Statement `yaml:"statements,omitempty" json:"statements,omitempty"`
	// Indices, for elasticsearch, are path.Match patterns of the indices
	// pipelines may search. Required.
	Indices []string `yaml:"indices,omitempty" json:"indices,omitempty"`
	// TimeField and MessageField, for elasticsearch, name the document's
	// time and log line; default @timestamp and message.
	TimeField    string `yaml:"time_field,omitempty" json:"time_field,omitempty"`
	MessageField string `yaml:"message_field,omitempty" json:"message_field,omitempty"`
}

// Auth is how a source logs in. The secret is looked up for each query, so
// a rotated secret is picked up without a restart.
type Auth struct {
	// Type is bearer (Authorization: Bearer <secret>), basic (User and the
	// secret as password) or header (the secret as the value of Header).
	// For clickhouse and postgres it may be left empty: User and the secret
	// are the database user and password.
	Type   string             `yaml:"type,omitempty" json:"type,omitempty"`
	User   string             `yaml:"user,omitempty" json:"user,omitempty"`
	Header string             `yaml:"header,omitempty" json:"header,omitempty"`
	Secret pipeline.SecretRef `yaml:"secret" json:"secret"`
}

// Limits bound what one query may return. Zero means the default.
type Limits struct {
	// MaxSeries is how many series a metric query may return. It can only
	// be 1: a verification judges one series per query.
	MaxSeries int `yaml:"max_series,omitempty" json:"max_series,omitempty"`
	MaxPoints int `yaml:"max_points,omitempty" json:"max_points,omitempty"` // default 500
	MaxLines  int `yaml:"max_lines,omitempty" json:"max_lines,omitempty"`   // default 5000
	MaxBytes  int `yaml:"max_bytes,omitempty" json:"max_bytes,omitempty"`   // default 8 MiB, of one response
}

// Statement is a query an operator wrote, which pipelines run by name.
type Statement struct {
	Kind string `yaml:"kind" json:"kind"` // metric or logs
	// SQL, for clickhouse, uses ClickHouse query parameters such as
	// {service:String}; {start:DateTime64(3)} and {end:DateTime64(3)} are
	// the window and are always passed. For postgres it uses {{name}}
	// placeholders, sent as typed parameters ($1, $2, ...); {{start}} and
	// {{end}} are the window, as timestamptz.
	//
	// A metric statement returns columns t (time) and value, or a single
	// value column; a logs statement returns a message column and
	// optionally t.
	SQL string `yaml:"sql,omitempty" json:"sql,omitempty"`
	// Path and Method, for http: the path appended to the source's URL, and
	// GET (the default) or POST.
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	Method string `yaml:"method,omitempty" json:"method,omitempty"`
	// Body, for an http POST, is a JSON document; {{name}} placeholders in
	// its string values are replaced after it is parsed.
	Body string `yaml:"body,omitempty" json:"body,omitempty"`
	// Value, for http, is a dotted path into the JSON response (numbers
	// index arrays): to a number, an array of numbers, or an array of
	// [time, value] pairs for metrics; to an array of strings for logs.
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	// Params are the parameters a pipeline must pass, by name, with their
	// types: for clickhouse a ClickHouse type such as String, UInt64 or
	// DateTime64(3); for postgres text, int, float or timestamptz; for http
	// string.
	Params map[string]string `yaml:"params,omitempty" json:"params,omitempty"`
	// Projects, if set, narrow which of the source's projects may run the
	// statement: a project must match the source's list and this one.
	Projects []string `yaml:"projects,omitempty" json:"projects,omitempty"`
}

// Lookup resolves a secret reference for a project, as
// secrets.Resolver.Lookup does.
type Lookup func(ctx context.Context, project string, ref pipeline.SecretRef) (string, error)

// SourceInfo describes a source without its URL or credentials.
type SourceInfo struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Projects   []string        `json:"projects"`
	Statements []StatementInfo `json:"statements,omitempty"`
}

// StatementInfo describes an operator statement.
type StatementInfo struct {
	Name     string            `json:"name"`
	Kind     string            `json:"kind"`
	Params   map[string]string `json:"params,omitempty"`
	Projects []string          `json:"projects,omitempty"`
}

// Query is one read a verification asks for.
type Query struct {
	// Project is the run's project. It must be allowed by the source and,
	// for a statement, by the statement.
	Project string
	Kind    string // metric or logs
	// Text is the query, for prometheus and loki (PromQL or LogQL) and for
	// elasticsearch (query DSL as JSON), with {{name}} placeholders.
	Text string
	// Statement names the operator statement, for clickhouse, postgres and
	// http.
	Statement string
	// Index is the index to search, for elasticsearch.
	Index string
	// Params are the parameter values, by name.
	Params     map[string]string
	Start, End time.Time
	// Step is the resolution of a metric: default 30s, at least 5s. It is
	// made longer if the window would have more points than the source
	// allows.
	Step time.Duration
	// MaxLines is how many log lines to read; zero or more than the source
	// allows means the source's limit.
	MaxLines int
}

// Result is what a query read.
type Result struct {
	Points    []verify.Point // metrics, by time, within [Start, End]
	Lines     []string       // logs, oldest first
	Truncated bool           // the source had more lines or rows than the limit
}

// Error is what Query returns. Its text names the source and says what went
// wrong in words; it never holds a credential.
type Error struct {
	Source string
	Reason string
	// Unavailable says the source could not answer (down, slow, or a
	// server error): trying again in the next interval may work.
	Unavailable bool
}

func (e *Error) Error() string { return e.Source + ": " + e.Reason }

// IsUnavailable reports whether err says a source could not answer.
func IsUnavailable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Unavailable
}

// Sources are the health sources configured on a deploy runner.
type Sources struct {
	byName map[string]*src
	lookup Lookup
}

// src is one validated source.
type src struct {
	name    string
	cfg     Config
	timeout time.Duration
	limits  Limits
	roots   *x509.CertPool // nil: the system's
	backend backend

	once sync.Once
	hc   *http.Client
}

// backend is one kind of source.
type backend interface {
	query(ctx context.Context, s *src, q *Query, cred credential) (Result, error)
}

// credential is a resolved secret for one query.
type credential struct {
	user, secret string
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9_.-]{0,62})$`)

// Open validates every source and returns them. Errors name the source.
func Open(cfg map[string]Config, lookup Lookup) (*Sources, error) {
	s := &Sources{byName: map[string]*src{}, lookup: lookup}
	names := make([]string, 0, len(cfg))
	for n := range cfg {
		names = append(names, n)
	}
	sort.Strings(names)
	var errs []error
	for _, n := range names {
		v, err := openOne(n, cfg[n])
		if err != nil {
			errs = append(errs, fmt.Errorf("health source %s: %w", n, err))
			continue
		}
		if v.cfg.Auth != nil && lookup == nil {
			errs = append(errs, fmt.Errorf("health source %s: it has auth, but the runner gave no way to look up secrets", n))
			continue
		}
		s.byName[n] = v
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return s, nil
}

func openOne(name string, c Config) (*src, error) {
	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("the name must be lower-case letters, digits, '.', '-' or '_', starting with a letter or digit")
	}
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	switch c.Type {
	case "opensearch":
		c.Type = Elasticsearch
	case "postgresql":
		c.Type = Postgres
	case "mysql", "mariadb":
		return nil, fmt.Errorf("type %s is not supported: Launchway has no MySQL driver and adds no dependencies for one; put a small HTTP service in front of the database and use type http", c.Type)
	case Prometheus, Loki, ClickHouse, Elasticsearch, Postgres, HTTP:
	case "":
		return nil, fmt.Errorf("type is missing: one of prometheus, loki, clickhouse, elasticsearch, opensearch, postgres or http")
	default:
		return nil, fmt.Errorf("unknown type %q: one of prometheus, loki, clickhouse, elasticsearch, opensearch, postgres or http", c.Type)
	}
	s := &src{name: name, cfg: c}
	for _, p := range c.Projects {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("project pattern %q is not a valid pattern", p)
		}
	}
	// Timeout.
	s.timeout = DefaultTimeout
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("timeout %q is not a duration such as 10s", c.Timeout)
		}
		if d > MaxTimeout {
			return nil, fmt.Errorf("timeout %s is longer than the most allowed, %s", d, MaxTimeout)
		}
		s.timeout = d
	}
	// Limits.
	l := c.Limits
	if l.MaxSeries == 0 {
		l.MaxSeries = 1
	}
	if l.MaxSeries != 1 {
		return nil, fmt.Errorf("limits.max_series can only be 1: a verification judges one series per query (aggregate with sum() or avg())")
	}
	if l.MaxPoints == 0 {
		l.MaxPoints = verify.MaxPointsPerSeries
	}
	if l.MaxPoints < 2 || l.MaxPoints > verify.MaxPointsPerSeries {
		return nil, fmt.Errorf("limits.max_points must be between 2 and %d", verify.MaxPointsPerSeries)
	}
	if l.MaxLines == 0 {
		l.MaxLines = DefaultMaxLines
	}
	if l.MaxLines < 1 || l.MaxLines > 100000 {
		return nil, fmt.Errorf("limits.max_lines must be between 1 and 100000")
	}
	if l.MaxBytes == 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxBytes < 1024 || l.MaxBytes > 256<<20 {
		return nil, fmt.Errorf("limits.max_bytes must be between 1 KiB and 256 MiB")
	}
	s.limits = l
	// URL.
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("url %q is not a URL", redactURL(c.URL))
	}
	if c.Type == Postgres {
		if u.Scheme != "postgres" && u.Scheme != "postgresql" {
			return nil, fmt.Errorf("url must start with postgres://")
		}
		if _, ok := u.User.Password(); ok || u.Query().Has("password") {
			return nil, fmt.Errorf("url has a password; leave it out and give it as auth.secret, so it is kept in a secret store")
		}
	} else {
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("url must start with http:// or https://")
		}
		if u.User != nil {
			return nil, fmt.Errorf("url has a user or password in it; give credentials as auth")
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("url must not have a query string or fragment")
		}
	}
	// Headers.
	for k, v := range c.Headers {
		switch strings.ToLower(k) {
		case "authorization", "proxy-authorization", "cookie", "x-clickhouse-key", "x-clickhouse-user", "host", "content-type", "content-length":
			return nil, fmt.Errorf("header %s may not be set in headers; credentials go in auth, where they come from a secret store", k)
		}
		if !validHeaderName(k) || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("header %q is not a valid header", k)
		}
		if c.Type == Postgres {
			return nil, fmt.Errorf("headers do not apply to postgres")
		}
	}
	// CA.
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
		s.roots = pool
	}
	// Auth.
	if a := c.Auth; a != nil {
		a.Type = strings.ToLower(a.Type)
		if a.Secret.Store == "" || a.Secret.Key == "" {
			return nil, fmt.Errorf("auth.secret needs a store and a key, for example {store: vault, key: apps/prometheus, field: token}")
		}
		switch c.Type {
		case ClickHouse, Postgres:
			if a.Type != "" && a.Type != "basic" {
				return nil, fmt.Errorf("auth.type for %s is basic (or empty): a user and a password", c.Type)
			}
			if a.User == "" && c.Type == ClickHouse {
				return nil, fmt.Errorf("auth.user is missing: the ClickHouse user to log in as")
			}
		default:
			switch a.Type {
			case "bearer":
			case "basic":
				if a.User == "" {
					return nil, fmt.Errorf("auth.user is missing, for basic authentication")
				}
			case "header":
				if a.Header == "" || !validHeaderName(a.Header) {
					return nil, fmt.Errorf("auth.header must name the header that carries the secret, such as X-API-Key")
				}
			default:
				return nil, fmt.Errorf("auth.type %q is not bearer, basic or header", a.Type)
			}
		}
	}
	// Per type.
	if c.Type != ClickHouse && c.Type != Postgres && c.Type != HTTP && len(c.Statements) > 0 {
		return nil, fmt.Errorf("statements are for clickhouse, postgres and http sources; a %s source takes the pipeline's query", c.Type)
	}
	if c.Type != Elasticsearch && (len(c.Indices) > 0 || c.TimeField != "" || c.MessageField != "") {
		return nil, fmt.Errorf("indices, time_field and message_field are for elasticsearch sources")
	}
	for sn, st := range c.Statements {
		if !paramRE.MatchString(sn) {
			return nil, fmt.Errorf("statement name %q must be lower-case letters, digits and '_', starting with a letter", sn)
		}
		if st.Kind != Metric && st.Kind != Logs {
			return nil, fmt.Errorf("statement %s: kind must be metric or logs", sn)
		}
		for _, p := range st.Projects {
			if _, err := path.Match(p, ""); err != nil {
				return nil, fmt.Errorf("statement %s: project pattern %q is not a valid pattern", sn, p)
			}
		}
		for pn := range st.Params {
			if !paramRE.MatchString(pn) {
				return nil, fmt.Errorf("statement %s: parameter name %q must match [a-z][a-z0-9_]{0,39}", sn, pn)
			}
			if pn == "start" || pn == "end" {
				return nil, fmt.Errorf("statement %s: parameters start and end are the verification window and are always passed; do not declare them", sn)
			}
		}
	}
	switch c.Type {
	case Prometheus:
		s.backend = promBackend{}
	case Loki:
		s.backend = lokiBackend{}
	case ClickHouse:
		b, err := newClickHouse(c)
		if err != nil {
			return nil, err
		}
		s.backend = b
	case Postgres:
		b, err := newPostgres(c)
		if err != nil {
			return nil, err
		}
		s.backend = b
	case HTTP:
		b, err := newHTTPJSON(c)
		if err != nil {
			return nil, err
		}
		s.backend = b
	case Elasticsearch:
		b, err := newElastic(&s.cfg)
		if err != nil {
			return nil, err
		}
		s.backend = b
	}
	if (c.Type == ClickHouse || c.Type == Postgres || c.Type == HTTP) && len(c.Statements) == 0 {
		return nil, fmt.Errorf("a %s source needs statements: the queries pipelines may run, by name", c.Type)
	}
	return s, nil
}

func validHeaderName(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// redactURL drops a password from a URL for an error message.
func redactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Redacted()
	}
	return "(unparseable)"
}

// Names describes the sources, by name, without URLs or credentials.
func (s *Sources) Names() []SourceInfo {
	var out []SourceInfo
	for n, v := range s.byName {
		info := SourceInfo{Name: n, Type: v.cfg.Type, Projects: append([]string(nil), v.cfg.Projects...)}
		for sn, st := range v.cfg.Statements {
			params := map[string]string{}
			for k, t := range st.Params {
				params[k] = t
			}
			info.Statements = append(info.Statements, StatementInfo{Name: sn, Kind: st.Kind, Params: params,
				Projects: append([]string(nil), st.Projects...)})
		}
		sort.Slice(info.Statements, func(i, j int) bool { return info.Statements[i].Name < info.Statements[j].Name })
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Query runs one query against the named source. The project allow-lists
// are checked, and the query is checked and its parameters substituted,
// before anything is sent.
func (s *Sources) Query(ctx context.Context, name string, q Query) (Result, error) {
	v, ok := s.byName[name]
	if !ok {
		return Result{}, &Error{Source: name, Reason: "this deploy runner has no health source of that name"}
	}
	fail := func(format string, a ...any) (Result, error) {
		return Result{}, &Error{Source: name, Reason: fmt.Sprintf(format, a...)}
	}
	if q.Project == "" {
		return fail("the query has no project")
	}
	if len(v.cfg.Projects) == 0 {
		return fail("this source allows no project: its projects list in the deploy runner's configuration is empty, and an empty list denies every project")
	}
	if !matchAny(v.cfg.Projects, q.Project) {
		return fail("project %s may not use this source (it allows: %s)", q.Project, strings.Join(v.cfg.Projects, ", "))
	}
	if q.Kind != Metric && q.Kind != Logs {
		return fail("kind must be metric or logs, not %q", q.Kind)
	}
	if q.Start.IsZero() || q.End.IsZero() || !q.End.After(q.Start) {
		return fail("the window must have a start before its end")
	}
	for k, val := range q.Params {
		if len(val) > MaxValueBytes {
			return fail("parameter %s is longer than %d bytes", k, MaxValueBytes)
		}
	}
	if q.Kind == Metric {
		if q.Step == 0 {
			q.Step = DefaultStep
		}
		if q.Step < MinStep {
			return fail("the step %s is shorter than the least allowed, %s", q.Step, MinStep)
		}
		// Make the step longer rather than ask for more points than allowed.
		span, most := q.End.Sub(q.Start), time.Duration(v.limits.MaxPoints-1)
		if span/q.Step > most {
			step := (span + most - 1) / most
			q.Step = (step + time.Millisecond - 1) / time.Millisecond * time.Millisecond
		}
	} else if q.MaxLines <= 0 || q.MaxLines > v.limits.MaxLines {
		q.MaxLines = v.limits.MaxLines
	}
	if q.Statement != "" || v.cfg.Type == ClickHouse || v.cfg.Type == Postgres || v.cfg.Type == HTTP {
		st, ok := v.cfg.Statements[q.Statement]
		if !ok {
			if q.Statement == "" {
				return fail("a %s source runs only statements its operator wrote; name one (it has: %s)", v.cfg.Type, strings.Join(statementNames(v.cfg), ", "))
			}
			return fail("there is no statement %s (it has: %s)", q.Statement, strings.Join(statementNames(v.cfg), ", "))
		}
		if len(st.Projects) > 0 && !matchAny(st.Projects, q.Project) {
			return fail("statement %s may not be used by project %s", q.Statement, q.Project)
		}
		if st.Kind != q.Kind {
			return fail("statement %s reads %s, but the query asks for %s", q.Statement, st.Kind, q.Kind)
		}
		for p := range st.Params {
			if _, ok := q.Params[p]; !ok {
				return fail("statement %s needs parameter %s, which the query does not give", q.Statement, p)
			}
		}
	}
	var cred credential
	if a := v.cfg.Auth; a != nil {
		sec, err := s.lookup(ctx, q.Project, a.Secret)
		if err != nil {
			return Result{}, &Error{Source: name, Reason: fmt.Sprintf("could not read its credentials (%s): %v", a.Secret.String(), err),
				Unavailable: errors.Is(err, context.DeadlineExceeded)}
		}
		cred = credential{user: a.User, secret: sec}
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	res, err := v.backend.query(ctx, v, &q, cred)
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = &Error{Reason: err.Error()}
		}
		e.Source = name
		e.Reason = mask(e.Reason, cred.secret)
		return Result{}, e
	}
	res.Points = clampPoints(res.Points, q.Start, q.End, v.limits.MaxPoints)
	if len(res.Lines) > q.MaxLines && q.Kind == Logs {
		res.Lines, res.Truncated = res.Lines[:q.MaxLines], true
	}
	return res, nil
}

func statementNames(c Config) []string {
	var n []string
	for k := range c.Statements {
		n = append(n, k)
	}
	sort.Strings(n)
	if len(n) == 0 {
		return []string{"none"}
	}
	return n
}

func matchAny(patterns []string, v string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, v); ok {
			return true
		}
	}
	return false
}

// mask replaces a secret in text.
func mask(text, secret string) string {
	if len(secret) < 4 {
		return text
	}
	text = strings.ReplaceAll(text, secret, "***")
	if e := url.QueryEscape(secret); e != secret {
		text = strings.ReplaceAll(text, e, "***")
	}
	return text
}

// clampPoints sorts points by time, keeps those within [start, end] and at
// most max of them (the latest).
func clampPoints(p []verify.Point, start, end time.Time, max int) []verify.Point {
	if len(p) == 0 {
		return nil
	}
	sort.SliceStable(p, func(i, j int) bool { return p[i].T < p[j].T })
	s, e := start.UnixMilli(), end.UnixMilli()
	out := p[:0]
	for _, x := range p {
		if x.T >= s && x.T <= e {
			out = append(out, x)
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// errorf makes a backend error.
func errorf(format string, a ...any) error {
	return &Error{Reason: fmt.Sprintf(format, a...)}
}

// unavailablef makes a backend error that trying again may cure.
func unavailablef(format string, a ...any) error {
	return &Error{Reason: fmt.Sprintf(format, a...), Unavailable: true}
}
