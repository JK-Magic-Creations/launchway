package bitbucket

import (
	"encoding/json"
	"path"
	"time"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func matches(pattern, branch string) bool {
	if pattern == branch {
		return true
	}
	ok, err := path.Match(pattern, branch)
	return err == nil && ok
}
