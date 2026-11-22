package bitbucketdc

import (
	"encoding/json"
	"path"
	"time"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// fromMillis reads the host's timestamps: milliseconds since the epoch.
func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func matches(pattern, branch string) bool {
	if pattern == branch {
		return true
	}
	ok, err := path.Match(pattern, branch)
	return err == nil && ok
}
