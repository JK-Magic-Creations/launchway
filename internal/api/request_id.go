package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
)

// Every request has an identifier: the caller's X-Request-Id when it is a
// plain token of 8 to 64 characters, otherwise a new random one. It is sent
// back in the response and written on the request's audit stream lines, so a
// line can be matched to the call that caused it.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

func (s *Server) requestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !requestIDPattern.MatchString(id) {
			var b [12]byte
			_, _ = rand.Read(b[:])
			id = "req_" + hex.EncodeToString(b[:])
			r.Header.Set("X-Request-Id", id)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r)
	})
}

// reqID is the identifier of a request (see requestIDs); "" outside a server.
func reqID(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Header.Get("X-Request-Id")
}
