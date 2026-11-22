package gitea

import (
	"encoding/json"
	"path"
	"time"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// parseTime reads the host's RFC 3339 times; zero when it cannot.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// matches says whether a branch protection rule name (a branch or a glob such as release/*) covers a branch.
func matches(rule, branch string) bool {
	if rule == branch {
		return true
	}
	ok, err := path.Match(rule, branch)
	return err == nil && ok
}
