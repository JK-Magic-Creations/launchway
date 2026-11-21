package api

import "net/http"

// uiContentSecurityPolicy is what the browser may do with the web UI: run its own scripts only (no
// inline script, no eval, nothing from another site), load its own styles (React sets style
// attributes, so inline styles stay allowed), images and fonts, call its own API, and nothing
// else: no plug-ins, no <base>, forms only to itself, no framing. A cross-site script that
// somehow got into a page could not load more code or send data to another origin. Artifacts and
// reports set stricter policies of their own, which replace this one.
const uiContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// hsts tells browsers to use HTTPS for this host from now on, on responses the server knows were
// sent over HTTPS (directly, or through a proxy it is told to trust). It is never sent over plain
// HTTP, where it would be ignored anyway and would only confuse a local trial.
func (s *Server) hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
