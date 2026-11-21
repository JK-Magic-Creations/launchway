package api

import "net/url"

// setLogStream asks the logs route for one stream, or one service's lines.
func setLogStream(q url.Values, stream, service string) {
	if stream != "" {
		q.Set("stream", stream)
	}
	if service != "" {
		q.Set("service", service)
	}
}
