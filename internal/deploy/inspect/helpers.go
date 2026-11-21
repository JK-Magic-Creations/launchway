package inspect

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func l(v any) []any          { x, _ := v.([]any); return x }

// dig reads a nested value by path; nil when any part is missing.
func dig(obj map[string]any, path ...string) any {
	var cur any = obj
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func digS(obj map[string]any, path ...string) string {
	switch v := dig(obj, path...).(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func digM(obj map[string]any, path ...string) map[string]any { return m(dig(obj, path...)) }
func digL(obj map[string]any, path ...string) []any          { return l(dig(obj, path...)) }

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int32:
		return int(t)
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func digI(obj map[string]any, path ...string) int { return toInt(dig(obj, path...)) }

func has(obj map[string]any, path ...string) bool { return dig(obj, path...) != nil }

func ts(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func labels(obj map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range digM(obj, "metadata", "labels") {
		out[k], _ = v.(string)
	}
	return out
}

func annotation(obj map[string]any, key string) string {
	return digS(obj, "metadata", "annotations", key)
}

func name(obj map[string]any) string { return digS(obj, "metadata", "name") }
func ns(obj map[string]any) string   { return digS(obj, "metadata", "namespace") }
func uid(obj map[string]any) string  { return digS(obj, "metadata", "uid") }

func ownerUIDs(obj map[string]any) []string {
	var out []string
	for _, o := range digL(obj, "metadata", "ownerReferences") {
		if u := digS(m(o), "uid"); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func sortedKeys[V any](mm map[string]V) []string {
	out := make([]string, 0, len(mm))
	for k := range mm {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// subset reports whether every pair of sel is in lab.
func subset(sel, lab map[string]string) bool {
	if len(sel) == 0 {
		return false
	}
	for k, v := range sel {
		if lab[k] != v {
			return false
		}
	}
	return true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
