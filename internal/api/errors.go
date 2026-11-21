package api

import "net/http"

// errorCodes and errorNext give every error answer the same three parts (docs/development/interface-quality.md):
// error, a plain sentence of what went wrong; code, a stable word a program can branch on; and next, what to do
// about it. A handler that has something more specific puts its own code or next in the body first.
var errorCodes = map[int][2]string{
	http.StatusBadRequest:            {"bad_request", "Check the request against the API reference and send it again."},
	http.StatusUnauthorized:          {"unauthorized", "Sign in again, or send a valid API token in the Authorization header."},
	http.StatusForbidden:             {"forbidden", "Ask an administrator for the role this needs; the message says which."},
	http.StatusNotFound:              {"not_found", "Check the name or id, and that your role may see it."},
	http.StatusMethodNotAllowed:      {"method_not_allowed", "Use the method the API reference lists for this address."},
	http.StatusConflict:              {"conflict", "Read the current state and try again; the message says what it conflicts with."},
	http.StatusGone:                  {"gone", "The thing is no longer there; start again from the list."},
	http.StatusRequestEntityTooLarge: {"too_large", "Send less data, or split it into several requests."},
	http.StatusUnprocessableEntity:   {"invalid", "Fix the problems listed in the answer and send it again."},
	http.StatusTooManyRequests:       {"rate_limited", "Wait as long as the message or the Retry-After header says, then try again."},
	http.StatusInternalServerError:   {"internal", "Try again. If it keeps failing, give an administrator the time and the request, and ask them to look at the server log."},
	http.StatusBadGateway:            {"upstream", "The system behind Launchway did not answer; try again shortly."},
	http.StatusServiceUnavailable:    {"unavailable", "Try again shortly; the Retry-After header, when present, says when."},
}

func withErrorShape(status int, v any) any {
	if status < 400 {
		return v
	}
	def, ok := errorCodes[status]
	if !ok {
		def = [2]string{"error", "Try again, or read the message for what to change."}
	}
	switch b := v.(type) {
	case map[string]string:
		if _, has := b["error"]; !has {
			return v
		}
		out := make(map[string]string, len(b)+2)
		for k, x := range b {
			out[k] = x
		}
		if out["code"] == "" {
			out["code"] = def[0]
		}
		if out["next"] == "" {
			out["next"] = def[1]
		}
		return out
	case map[string]any:
		if _, has := b["error"]; !has {
			return v
		}
		out := make(map[string]any, len(b)+2)
		for k, x := range b {
			out[k] = x
		}
		if _, has := out["code"]; !has {
			out["code"] = def[0]
		}
		if _, has := out["next"]; !has {
			out["next"] = def[1]
		}
		return out
	}
	return v
}
