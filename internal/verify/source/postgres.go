package source

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgBackend runs operator statements on PostgreSQL.
//
// The operator's {{name}} placeholders become positional parameters ($1,
// $2, ...) sent with typed values, never SQL text. Each query opens its own
// connection and runs in a read-only transaction (BEGIN READ ONLY) with a
// statement timeout and an idle-in-transaction timeout, reads at most the
// limit's rows, and always rolls back. A read-only transaction refuses
// writes to tables, DDL and nextval(), but not everything (dblink to
// another server, for one): operators should still log in as a role that
// can only read what verifications need.
type pgBackend struct {
	stmts map[string]pgStatement
}

// pgStatement is an operator statement with its placeholders turned into
// positional parameters.
type pgStatement struct {
	Statement
	sql   string
	order []string // the name of $1, $2, ...
}

func newPostgres(c Config) (*pgBackend, error) {
	b := &pgBackend{stmts: map[string]pgStatement{}}
	for name, st := range c.Statements {
		if st.Path != "" || st.Method != "" || st.Body != "" || st.Value != "" {
			return nil, fmt.Errorf("statement %s: path, method, body and value are for http sources", name)
		}
		if strings.TrimSpace(st.SQL) == "" {
			return nil, fmt.Errorf("statement %s: sql is missing", name)
		}
		t, err := lexSQL(Postgres, st.SQL)
		if err != nil {
			return nil, fmt.Errorf("statement %s: %v", name, err)
		}
		if err := t.checkSelect(); err != nil {
			return nil, fmt.Errorf("statement %s: %v", name, err)
		}
		for _, s := range t.strings {
			if m := placeholderRE.FindStringSubmatch(s); m != nil {
				return nil, fmt.Errorf("statement %s: {{%s}} is inside a quoted string, where it would be text; write it bare, such as WHERE service = {{%s}}", name, m[1], m[1])
			}
		}
		for p, typ := range st.Params {
			if !pgParamTypes[typ] {
				return nil, fmt.Errorf("statement %s: parameter %s has type %q; postgres parameters are text, int, float or timestamptz", name, p, typ)
			}
		}
		ps := pgStatement{Statement: st}
		num := map[string]int{}
		var sb strings.Builder
		last := 0
		for _, loc := range placeholderRE.FindAllStringSubmatchIndex(t.code, -1) {
			p := st.SQL[loc[2]:loc[3]]
			if _, ok := st.Params[p]; !ok && p != "start" && p != "end" {
				return nil, fmt.Errorf("statement %s: the SQL uses {{%s}}, but params does not declare it", name, p)
			}
			if num[p] == 0 {
				ps.order = append(ps.order, p)
				num[p] = len(ps.order)
			}
			// The cast makes the server read the parameter as its declared
			// type, rather than guess one from where it is used (a float
			// compared with 0 would otherwise be read as an integer).
			typ := "timestamptz"
			if t, ok := st.Params[p]; ok {
				typ = pgCast[t]
			}
			sb.WriteString(st.SQL[last:loc[0]])
			sb.WriteString("$" + strconv.Itoa(num[p]) + "::" + typ)
			last = loc[1]
		}
		sb.WriteString(st.SQL[last:])
		for p := range st.Params {
			if num[p] == 0 {
				return nil, fmt.Errorf("statement %s: parameter %s is declared but the SQL does not use {{%s}}", name, p, p)
			}
		}
		ps.sql = sb.String()
		b.stmts[name] = ps
	}
	return b, nil
}

func (b *pgBackend) query(ctx context.Context, s *src, q *Query, cred credential) (Result, error) {
	st := b.stmts[q.Statement]
	args := make([]any, len(st.order))
	for i, p := range st.order {
		switch p {
		case "start":
			args[i] = q.Start.UTC()
			continue
		case "end":
			args[i] = q.End.UTC()
			continue
		}
		v, err := pgValue(st.Params[p], q.Params[p])
		if err != nil {
			return Result{}, errorf("parameter %s: %v", p, err)
		}
		args[i] = v
	}
	cfg, err := pgx.ParseConfig(s.cfg.URL)
	if err != nil {
		return Result{}, errorf("the source's url could not be read: %v", err)
	}
	if cred.user != "" {
		cfg.User = cred.user
	}
	if cred.secret != "" {
		cfg.Password = cred.secret
	}
	cfg.ConnectTimeout = s.timeout
	cfg.RuntimeParams["application_name"] = userAgent
	if s.roots != nil {
		set := func(t *tls.Config) {
			if t != nil {
				t.RootCAs = s.roots
			}
		}
		set(cfg.TLSConfig)
		for _, f := range cfg.Fallbacks {
			set(f.TLSConfig)
		}
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return Result{}, pgError(ctx, s, err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return Result{}, pgError(ctx, s, err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rctx)
	}()
	ms := strconv.FormatInt(s.timeout.Milliseconds(), 10)
	for _, set := range []string{
		"SET LOCAL statement_timeout = " + ms,
		"SET LOCAL idle_in_transaction_session_timeout = " + ms,
		"SET LOCAL lock_timeout = " + ms,
	} {
		if _, err := tx.Exec(ctx, set); err != nil {
			return Result{}, pgError(ctx, s, err)
		}
	}
	rows, err := tx.Query(ctx, st.sql, args...)
	if err != nil {
		return Result{}, pgError(ctx, s, err)
	}
	defer rows.Close()
	var cols []string
	for _, f := range rows.FieldDescriptions() {
		cols = append(cols, f.Name)
	}
	limit := q.MaxLines
	if q.Kind == Metric {
		limit = s.limits.MaxPoints
	}
	var out [][]json.RawMessage
	size, truncated := 0, false
	for rows.Next() {
		if len(out) == limit {
			truncated = true
			break
		}
		vals, err := rows.Values()
		if err != nil {
			return Result{}, pgError(ctx, s, err)
		}
		row := make([]json.RawMessage, len(vals))
		for i, v := range vals {
			if t, ok := v.(time.Time); ok {
				v = float64(t.UnixMilli()) / 1000
			}
			if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
				v = nil
			}
			j, err := json.Marshal(v)
			if err != nil {
				j, _ = json.Marshal(fmt.Sprint(v))
			}
			size += len(j)
			row[i] = j
		}
		if size > s.limits.MaxBytes {
			return Result{}, errorf("the response was larger than %s, the most this source allows (limits.max_bytes); narrow the query", sizeText(s.limits.MaxBytes))
		}
		out = append(out, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Result{}, pgError(ctx, s, err)
	}
	if q.Kind == Logs {
		lines, err := rowsToLines(cols, out, func(c json.RawMessage) (string, bool) {
			var s string
			return s, json.Unmarshal(c, &s) == nil
		}, chTime)
		return Result{Lines: lines, Truncated: truncated}, err
	}
	pts, err := rowsToPoints(cols, out, q.End, chNumber, chTime)
	return Result{Points: pts, Truncated: truncated}, err
}

// pgCast is the PostgreSQL type of each parameter type.
var pgCast = map[string]string{"text": "text", "int": "bigint", "float": "double precision", "timestamptz": "timestamptz"}

// pgValue converts a parameter's text to its declared type.
func pgValue(typ, v string) (any, error) {
	switch typ {
	case "text":
		return v, nil
	case "int":
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an int", v)
		}
		return n, nil
	case "float":
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%q is not a float", v)
		}
		return f, nil
	case "timestamptz":
		t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("%q is not a time such as 2026-11-19T10:00:00Z", v)
		}
		return t, nil
	}
	return nil, fmt.Errorf("unknown type %s", typ)
}

// pgError says what went wrong talking to PostgreSQL.
func pgError(ctx context.Context, s *src, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return unavailablef("the source did not answer within %s", s.timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return errorf("the query was cancelled")
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "57014": // query_canceled: the statement timeout
			return unavailablef("the source did not finish the query within %s", s.timeout)
		case "28P01", "28000": // invalid_password, invalid_authorization_specification
			return errorf("refused the login (%s): check the deploy runner's credentials for this source", pe.Message)
		case "25006": // read_only_sql_transaction
			return errorf("the server refused the statement because health queries are read-only: %s", pe.Message)
		case "42501": // insufficient_privilege
			return errorf("the database user may not read this: %s", pe.Message)
		}
		if strings.HasPrefix(pe.Code, "08") || strings.HasPrefix(pe.Code, "53") || strings.HasPrefix(pe.Code, "57") {
			return unavailablef("the server failed: %s (SQLSTATE %s)", pe.Message, pe.Code)
		}
		return errorf("the query failed: %s (SQLSTATE %s)", pe.Message, pe.Code)
	}
	var ne net.Error
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) || errors.As(err, &ne) {
		if errors.Is(err, context.DeadlineExceeded) || (ne != nil && ne.Timeout()) {
			return unavailablef("the source did not answer within %s", s.timeout)
		}
		return unavailablef("could not connect to the source: %v", trimConnError(err))
	}
	return errorf("the query failed: %v", err)
}

// trimConnError keeps the last part of a pgx connection error, which says
// why; the earlier parts repeat the host.
func trimConnError(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		return msg[i+2:]
	}
	return msg
}
