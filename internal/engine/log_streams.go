package engine

import "strings"

// Log streams. A service container's output is shipped as the stream
// service, each line starting with the service's name and a colon.
const StreamService = "service"

var logStreams = []string{"stdout", "stderr", "system", StreamService}

// CheckLogStream says what is wrong with a stream and service asked for in a
// log query, or "".
func CheckLogStream(stream, service string) string {
	switch {
	case stream != "" && !contains(logStreams, stream):
		return "stream is stdout, stderr, system or service"
	case service != "" && stream != StreamService:
		return "service goes with stream=service"
	case strings.ContainsAny(service, " :\r\n") || len(service) > 63:
		return "a service is named as in containers:"
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// streamFilter adds the stream conditions to a log query: one stream, a
// service's lines, or, without a stream, everything but service output (so
// that what a step printed is not buried by what its services printed).
func (q LogQuery) streamFilter(where string, args []any) (string, []any) {
	switch {
	case q.Stream == "":
		return where + ` AND stream <> '` + StreamService + `'`, args
	case q.Service != "":
		return where + ` AND stream = ? AND text LIKE ? ESCAPE '\'`,
			append(args, q.Stream, strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Service)+": %")
	}
	return where + ` AND stream = ?`, append(args, q.Stream)
}
