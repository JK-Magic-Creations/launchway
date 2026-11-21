package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Every write the runner makes can be sent again after a lost answer, and
// the server applies it once (see engine/idempotent.go). A call whose body
// is the same is the same call, so its key is a hash of the route and the
// body: the repeat of a request carries the key of the first, with no state
// kept in the client.
var idempotentRoutes = []string{
	"/finish", "/artifacts/begin", "/artifacts/commit", "/caches/begin", "/caches/commit", "/deploy/command-done",
}

// idemKey is the Idempotency-Key header for a request, or "" for a route
// whose calls are naturally safe to repeat (heartbeat, start, logs with an
// offset, chunks with an offset, observations) or are not writes.
func idemKey(url string, body []byte) string {
	for _, r := range idempotentRoutes {
		if strings.HasSuffix(url, "/api/runner/v1"+r) {
			sum := sha256.New()
			sum.Write([]byte(r))
			sum.Write(body)
			return hex.EncodeToString(sum.Sum(nil))
		}
	}
	return ""
}
