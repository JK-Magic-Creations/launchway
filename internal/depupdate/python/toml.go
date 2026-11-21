package python

import "strings"

// A tiny TOML reader for exactly what Python projects need: tables, arrays
// of tables, "key = value" pairs whose value may span lines (arrays, inline
// tables). It reads nothing else and never fails: what it does not
// understand it skips.

type tomlPair struct {
	Table string // "project", "tool.poetry.dependencies", "package" for [[package]]
	Index int    // for [[x]]: which element (0-based), else -1
	Key   string
	Value string // raw text of the value
	Line  int    // 1-based line of the key
}

func readTOML(src string) []tomlPair {
	var out []tomlPair
	table, idx := "", -1
	counts := map[string]int{}
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSpace(stripTomlComment(lines[i]))
		switch {
		case l == "":
			continue
		case strings.HasPrefix(l, "[["):
			table = strings.Trim(strings.TrimSuffix(strings.TrimPrefix(l, "[["), "]]"), " ")
			idx = counts[table]
			counts[table]++
			continue
		case strings.HasPrefix(l, "["):
			table, idx = strings.Trim(strings.TrimSuffix(strings.TrimPrefix(l, "["), "]"), " "), -1
			continue
		}
		eq := strings.Index(l, "=")
		if eq < 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(l[:eq]), `"'`)
		val := strings.TrimSpace(l[eq+1:])
		start := i + 1
		for depth(val) > 0 && i+1 < len(lines) {
			i++
			val += "\n" + strings.TrimSpace(stripTomlComment(lines[i]))
		}
		out = append(out, tomlPair{table, idx, key, val, start})
	}
	return out
}

// depth counts unclosed [ and { outside strings.
func depth(s string) int {
	d := 0
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == '\\' && q == '"' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '[' || c == '{':
			d++
		case c == ']' || c == '}':
			d--
		}
	}
	return d
}

func stripTomlComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == '\\' && q == '"' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#':
			return s[:i]
		}
	}
	return s
}

// strings of an array value: ["a", 'b'] gives a, b with their lines.
func tomlStrings(val string) []string {
	var out []string
	for i := 0; i < len(val); i++ {
		if val[i] == '"' || val[i] == '\'' {
			q := val[i]
			j := i + 1
			for j < len(val) && val[j] != q {
				if val[j] == '\\' && q == '"' {
					j++
				}
				j++
			}
			if j <= len(val) && j > i {
				end := j
				if end > len(val) {
					end = len(val)
				}
				out = append(out, val[i+1:end])
			}
			i = j
		}
	}
	return out
}

// tomlString is the single string a value is, or "".
func tomlString(val string) string {
	val = strings.TrimSpace(val)
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
		if s := tomlStrings(val); len(s) > 0 {
			return s[0]
		}
	}
	return ""
}

// inlineField returns field "version" of an inline table value.
func inlineField(val, field string) string {
	val = strings.TrimSpace(val)
	if !strings.HasPrefix(val, "{") {
		return ""
	}
	k := strings.Index(val, field)
	for k >= 0 {
		rest := strings.TrimSpace(val[k+len(field):])
		before := byte(' ')
		if k > 0 {
			before = val[k-1]
		}
		if strings.HasPrefix(rest, "=") && (before == ' ' || before == '{' || before == ',') {
			return tomlString(strings.TrimSpace(rest[1:]))
		}
		n := strings.Index(val[k+1:], field)
		if n < 0 {
			break
		}
		k += 1 + n
	}
	return ""
}
