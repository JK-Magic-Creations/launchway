package source

import (
	"fmt"
	"regexp"
	"strings"
)

// sqlText is an operator's SQL with its strings and comments blanked out,
// so that keywords and placeholders are found only in code. Positions are
// kept: code[i] is the SQL's byte i, or a space inside a string or comment.
type sqlText struct {
	sql     string
	code    string
	strings []string // the bodies of quoted strings
}

// lexSQL blanks out the strings and comments of a statement. It is a
// best-effort lexer for the checks Open makes; the real guarantees are the
// server's read-only mode and the operator writing the SQL.
func lexSQL(dialect, sql string) (*sqlText, error) {
	code := []byte(sql)
	var bodies []string
	blank := func(from, to int) {
		for k := from; k < to && k < len(code); k++ {
			if code[k] != '\n' {
				code[k] = ' '
			}
		}
	}
	n := len(sql)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				j = n - i
			}
			blank(i, i+j)
			i += j
		case c == '/' && i+1 < n && sql[i+1] == '*':
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				return nil, fmt.Errorf("a /* comment is not closed")
			}
			blank(i, i+j+4)
			i += j + 4
		case c == '\'' || c == '"' || (c == '`' && dialect == ClickHouse):
			backslash := dialect == ClickHouse || (c == '\'' && i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e'))
			j := i + 1
			for ; j < n; j++ {
				if backslash && sql[j] == '\\' {
					j++
					continue
				}
				if sql[j] == c {
					if j+1 < n && sql[j+1] == c { // a doubled quote
						j++
						continue
					}
					break
				}
			}
			if j >= n {
				return nil, fmt.Errorf("a %c-quoted string or name is not closed", c)
			}
			if c == '\'' {
				bodies = append(bodies, sql[i+1:j])
			}
			blank(i+1, j)
			i = j + 1
		case c == '$' && dialect == Postgres:
			// Dollar quoting: $$...$$ or $tag$...$tag$.
			m := dollarTagRE.FindString(sql[i:])
			if m == "" {
				i++
				continue
			}
			j := strings.Index(sql[i+len(m):], m)
			if j < 0 {
				return nil, fmt.Errorf("a %s-quoted string is not closed", m)
			}
			bodies = append(bodies, sql[i+len(m):i+len(m)+j])
			blank(i+len(m), i+len(m)+j)
			i += len(m) + j + len(m)
		default:
			i++
		}
	}
	return &sqlText{sql: sql, code: string(code), strings: bodies}, nil
}

var (
	dollarTagRE   = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)
	firstWordRE   = regexp.MustCompile(`^\s*\(*\s*([A-Za-z]+)`)
	forbiddenSQL  = regexp.MustCompile(`(?i)\b(INTO|FORMAT|SETTINGS|OUTFILE|INSERT|UPDATE|DELETE)\b`)
	chParamRE     = regexp.MustCompile(`\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*:\s*([^{}]+?)\s*\}`)
	chTypeRE      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\([A-Za-z0-9_ ,'()]*\))?$`)
	pgParamTypes  = map[string]bool{"text": true, "int": true, "float": true, "timestamptz": true}
	httpParamType = map[string]bool{"string": true}
)

// checkSelect checks that a statement is a single SELECT (or WITH ...
// SELECT) with no keyword that writes, changes settings or chooses the
// output.
func (t *sqlText) checkSelect() error {
	code := strings.TrimSpace(t.code)
	code = strings.TrimSpace(strings.TrimSuffix(code, ";"))
	if strings.Contains(code, ";") {
		return fmt.Errorf("the SQL has more than one statement; give a single SELECT")
	}
	m := firstWordRE.FindStringSubmatch(code)
	if m == nil || (!strings.EqualFold(m[1], "SELECT") && !strings.EqualFold(m[1], "WITH")) {
		return fmt.Errorf("the SQL must be a single SELECT (or WITH ... SELECT)")
	}
	if k := forbiddenSQL.FindString(code); k != "" {
		return fmt.Errorf("the SQL uses %s, which a health query may not; write a plain SELECT (Launchway chooses the output format and limits)", strings.ToUpper(k))
	}
	return nil
}
