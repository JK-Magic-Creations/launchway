package pipeline

// Plain words for the mistakes people make most (the sentences are pinned by
// internal/source/messages_test.go).

import "strings"

// plainSyntax says what is wrong in words a person can act on, for the
// mistakes YAML reports in its own: a tab where spaces belong.
func plainSyntax(p Problem, data []byte) Problem {
	if p.Line > 0 && strings.Contains(p.Message, "cannot start any token") {
		lines := strings.Split(string(data), "\n")
		if p.Line <= len(lines) && strings.Contains(strings.TrimRight(lines[p.Line-1], " "), "\t") {
			p.Message = "this line has a tab: YAML is indented with spaces, never tabs; replace the tab with two spaces"
		}
	}
	return p
}

// mappingHints say what a key holds when it is not a mapping.
var mappingHints = map[string]string{
	"on":         "write what starts a run as a mapping, for example on: { push: { branches: [main] } }",
	"defaults":   "defaults is a mapping, for example: defaults: { image: alpine:3.22, env: { TZ: UTC } }",
	"containers": "containers is a mapping of names to containers",
}

// Keys other tools call something else.
var renamed = map[string]string{
	"runs-on": "choose the runners with runner: { pool: gpu, labels: [cuda] } at the top of the file, or on one step",
	"matrix":  "to repeat a step over lists, use for_each: its combinations are the matrix. For example for_each: { go: [\"1.26\", \"1.27\"] }, with exclude, add, max_parallel and fail_fast beside it",
	"env":     "settings for every step go under defaults, as defaults: { env: { TZ: UTC } }; a step's own env is written in the step",
}

// nearest returns the known key that a mistyped one is most likely meant to
// be: within two edits, and not so short that anything is close.
func nearest(key string, known []string) string {
	if len(key) < 3 {
		return ""
	}
	best, bestD := "", 3
	for _, k := range known {
		if d := editDistance(key, k); d < bestD {
			best, bestD = k, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
