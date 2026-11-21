package plan

import "strings"

// Glob matches a name against a pattern where * matches any run of
// characters (including "/") and nothing else is special. Matching ignores
// case, as package names do.
func Glob(pattern, name string) bool {
	p, n := strings.ToLower(pattern), strings.ToLower(name)
	if p == "*" || p == n {
		return true
	}
	parts := strings.Split(p, "*")
	if len(parts) == 1 {
		return false
	}
	if !strings.HasPrefix(n, parts[0]) {
		return false
	}
	n = n[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(n, mid)
		if i < 0 {
			return false
		}
		n = n[i+len(mid):]
	}
	return strings.HasSuffix(n, parts[len(parts)-1])
}

func anyGlob(patterns []string, name string) bool {
	for _, p := range patterns {
		if Glob(p, name) {
			return true
		}
	}
	return false
}
