package source

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A placeholder is {{name}}. Any other text in double braces, such as a
// LogQL line_format template's {{.label}}, is not one and is left as
// written.
var (
	paramRE         = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	placeholderRE   = regexp.MustCompile(`\{\{([a-z][a-z0-9_]{0,39})\}\}`)
	placeholderAtRE = regexp.MustCompile(`^\{\{([a-z][a-z0-9_]{0,39})\}\}`)
)

// CheckQuery is the static check of a pipeline's query: the server runs it
// when a pipeline is created, and the deploy runner again before a query is
// sent. It checks that the query is not empty or too long, that every
// placeholder is declared, and that placeholders are only where a value
// can be put safely. For clickhouse, postgres and http sources, text is the
// name of the operator's statement.
func CheckQuery(sourceType, text string, declared []string) error {
	decl := map[string]bool{}
	for _, d := range declared {
		if !paramRE.MatchString(d) {
			return fmt.Errorf("parameter name %q must be lower-case letters, digits and '_', starting with a letter, at most 40 long", d)
		}
		decl[d] = true
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("the query is empty")
	}
	if len(text) > MaxQueryBytes {
		return fmt.Errorf("the query is %d bytes long; at most %d are allowed", len(text), MaxQueryBytes)
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("the query is not valid UTF-8")
	}
	switch strings.ToLower(sourceType) {
	case Prometheus, Loki:
		_, err := rewriteQL(strings.ToLower(sourceType), text, nil, decl)
		return err
	case Elasticsearch, "opensearch":
		_, err := rewriteDSL(text, nil, decl)
		return err
	case ClickHouse, Postgres, "postgresql", HTTP:
		if !paramRE.MatchString(text) {
			return fmt.Errorf("a %s source runs only statements its operator wrote; give the statement's name (lower-case letters, digits and '_'), not a query", sourceType)
		}
		return nil
	}
	return fmt.Errorf("unknown source type %q", sourceType)
}

// usedPlaceholders lists the placeholder names in s.
func usedPlaceholders(s string) []string {
	var out []string
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// substitute replaces the placeholders in s with esc(value). With values
// nil it only checks that each is declared.
func substitute(s string, values map[string]string, declared map[string]bool, esc func(string) (string, error)) (string, error) {
	var err error
	out := placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-2]
		if err != nil {
			return m
		}
		if values == nil {
			if !declared[name] {
				err = fmt.Errorf("{{%s}} is not a declared parameter (declared: %s)", name, list(declared))
			}
			return m
		}
		v, ok := values[name]
		if !ok {
			err = fmt.Errorf("parameter %s is used by the query but has no value", name)
			return m
		}
		e, eerr := esc(v)
		if eerr != nil {
			err = fmt.Errorf("parameter %s: %v", name, eerr)
			return m
		}
		return e
	})
	return out, err
}

func list(m map[string]bool) string {
	if len(m) == 0 {
		return "none"
	}
	var n []string
	for k := range m {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}

// Where a string literal is, in PromQL or LogQL, decides how a value is
// put into it.
type litMode int

const (
	litRefuse litMode = iota
	litPlain          // compared as is: a label value or line filter
	litRegexp         // a regular expression: the value is quoted first
)

// Loki pipeline stages whose strings are templates, patterns or label
// expressions; a value there could change what the stage does.
var unsafeStages = map[string]string{
	"line_format":  "a line_format template",
	"label_format": "a label_format template",
	"pattern":      "a pattern expression",
	"json":         "a json parser expression",
	"logfmt":       "a logfmt parser expression",
	"unpack":       "an unpack stage",
	"drop":         "a drop stage",
	"keep":         "a keep stage",
	"decolorize":   "a decolorize stage",
	"unwrap":       "an unwrap stage",
}

// rewriteQL walks a PromQL (lang prometheus) or LogQL (lang loki) query,
// and replaces the placeholders inside its string literals with values
// escaped for each literal. With values nil it only checks the query.
//
// The walk knows the languages' lexical rules: strings in double or single
// quotes with backslash escapes, raw strings in backticks, and # comments
// to the end of a line. A placeholder must be in a quoted string that is a
// label matcher's value ({ns="{{ns}}"}) or, in LogQL, a line filter
// (|= "{{id}}") or a label filter (| level="{{level}}"); after =~, !~ and
// |~, and in a regexp stage, the string is a regular expression and the
// value is quoted with regexp.QuoteMeta first, so it matches only itself.
func rewriteQL(lang, text string, values map[string]string, declared map[string]bool) (string, error) {
	var (
		out      strings.Builder
		depth    int    // of { } outside strings
		prev     string // the last token
		lastOp   string // the last line or label filter operator, for "or"
		stage    string // the Loki pipeline stage we are in
		afterBar bool
	)
	n := len(text)
	pos := func(i int) string {
		line := 1 + strings.Count(text[:i], "\n")
		col := i - strings.LastIndex(text[:i], "\n")
		return fmt.Sprintf("line %d column %d", line, col)
	}
	for i := 0; i < n; {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			out.WriteByte(c)
			i++
		case c == '#':
			j := strings.IndexByte(text[i:], '\n')
			if j < 0 {
				j = n - i
			}
			out.WriteString(text[i : i+j])
			i += j
		case c == '\'' && lang == Loki:
			// LogQL has no single-quoted strings; Loki refuses the query.
			return "", fmt.Errorf("LogQL strings use double quotes or backticks, not single quotes (%s)", pos(i))
		case c == '"' || c == '\'':
			j := i + 1
			for j < n && text[j] != c {
				if text[j] == '\n' {
					return "", fmt.Errorf("the string at %s runs past the end of the line; close it with %c", pos(i), c)
				}
				if text[j] == '\\' {
					j++
				}
				j++
			}
			if j >= n {
				return "", fmt.Errorf("the string at %s is not closed", pos(i))
			}
			body := text[i+1 : j]
			if placeholderRE.MatchString(body) {
				mode, why := literalMode(lang, depth, prev, lastOp, stage)
				name := placeholderRE.FindStringSubmatch(body)[1]
				if mode == litRefuse {
					return "", fmt.Errorf("{{%s}} at %s is in %s; a placeholder may only be a label value (namespace=\"{{%s}}\")%s",
						name, pos(i), why, name, map[bool]string{true: " or a line or label filter (|= \"{{" + name + "}}\")", false: ""}[lang == Loki])
				}
				q := c
				rewritten, err := substitute(body, values, declared, func(v string) (string, error) {
					if mode == litRegexp {
						v = regexp.QuoteMeta(v)
					}
					return escapeQL(v, q)
				})
				if err != nil {
					return "", err
				}
				body = rewritten
			}
			out.WriteByte(c)
			out.WriteString(body)
			out.WriteByte(c)
			i = j + 1
			prev, afterBar = "string", false
		case c == '`':
			j := strings.IndexByte(text[i+1:], '`')
			if j < 0 {
				return "", fmt.Errorf("the raw string at %s is not closed", pos(i))
			}
			body := text[i+1 : i+1+j]
			if m := placeholderRE.FindStringSubmatch(body); m != nil {
				return "", fmt.Errorf("{{%s}} at %s is in a raw string (`...`), where a value cannot be escaped; use double quotes", m[1], pos(i))
			}
			out.WriteString(text[i : i+j+2])
			i += j + 2
			prev, afterBar = "string", false
		case c == '{' && placeholderAtRE.MatchString(text[i:]):
			name := placeholderAtRE.FindStringSubmatch(text[i:])[1]
			return "", fmt.Errorf("{{%s}} at %s must be inside quotes, as a label value: namespace=\"{{%s}}\"", name, pos(i), name)
		case c == '{':
			depth++
			out.WriteByte(c)
			i++
			prev, afterBar = "{", false
		case c == '}':
			if depth > 0 {
				depth--
			}
			out.WriteByte(c)
			i++
			prev, afterBar = "}", false
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < n && (text[j] == '_' || text[j] == ':' || text[j] >= 'a' && text[j] <= 'z' || text[j] >= 'A' && text[j] <= 'Z' || text[j] >= '0' && text[j] <= '9') {
				j++
			}
			word := strings.ToLower(text[i:j])
			if afterBar && depth == 0 {
				stage = word
			}
			out.WriteString(text[i:j])
			i = j
			prev, afterBar = word, false
		case strings.IndexByte("=!~|<>+-*/%^", c) >= 0:
			j := i
			for j < n && strings.IndexByte("=!~|<>", text[j]) >= 0 {
				j++
			}
			if j == i {
				j = i + 1
			}
			op := text[i:j]
			out.WriteString(op)
			i = j
			afterBar = op == "|" && depth == 0
			if afterBar {
				stage = ""
			}
			if depth == 0 {
				switch op {
				case "|=", "|~", "|>":
					stage, lastOp = "", op
				case "!=", "!~", "!>", "=", "==", "=~":
					lastOp = op
				}
			}
			prev = op
		default:
			out.WriteByte(c)
			i++
			prev, afterBar = string(c), false
		}
	}
	return out.String(), nil
}

// literalMode decides how a quoted string that holds a placeholder is
// treated, from what comes before it. why says where it is, when refused.
func literalMode(lang string, depth int, prev, lastOp, stage string) (litMode, string) {
	if depth > 0 {
		switch prev {
		case "=", "!=":
			return litPlain, ""
		case "=~", "!~":
			return litRegexp, ""
		}
		return litRefuse, "a selector, but not as a label matcher's value"
	}
	if lang != Loki {
		return litRefuse, "a function argument or other expression"
	}
	if what, bad := unsafeStages[stage]; bad {
		return litRefuse, what
	}
	op := prev
	if prev == "or" && lastOp != "" {
		op = lastOp
	}
	switch op {
	case "|=", "!=", "=", "==":
		return litPlain, ""
	case "|~", "!~", "=~":
		return litRegexp, ""
	case "|>", "!>":
		return litRefuse, "a pattern line filter, where <_> in a value would match anything"
	case "regexp":
		if stage == "regexp" {
			return litRegexp, ""
		}
	}
	return litRefuse, "a function argument or other expression"
}

// escapeQL escapes v for a PromQL or LogQL string in quote q. Both
// languages read Go's escapes.
func escapeQL(v string, q byte) (string, error) {
	if !utf8.ValidString(v) {
		return "", fmt.Errorf("the value is not valid UTF-8")
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(q):
			b.WriteByte('\\')
			b.WriteByte(q)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && !unicode.IsPrint(r)):
			if r > 0xffff {
				fmt.Fprintf(&b, `\U%08x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}
