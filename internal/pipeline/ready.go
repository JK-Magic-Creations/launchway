package pipeline

// Readiness of a service container (schema 27):
//
//	containers:
//	  postgres:
//	    image: docker.io/library/postgres:17
//	    service: true
//	    ready: { run: "pg_isready -U postgres", every: 2s, within: 60s }
//	  cache:
//	    image: docker.io/library/redis:7
//	    service: true
//	    ready: { tcp: 6379 }
//
// Exactly one of run (a command executed inside the service container; ready
// when it exits 0), tcp (a port on localhost) or http (a path and a port on
// localhost; ready on a 2xx answer). The first step starts only when every
// service is ready, or fails, saying why, after within.

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Readiness limits.
const (
	DefaultReadyEvery  = 2 * time.Second
	DefaultReadyWithin = 60 * time.Second
	MinReadyEvery      = time.Second
	MaxReadyWithin     = 10 * time.Minute
)

// Ready says when a service is ready to be used.
type Ready struct {
	Run    string     `json:"run,omitempty"`  // a command run in the service's container
	TCP    int        `json:"tcp,omitempty"`  // a port on localhost
	HTTP   *ReadyHTTP `json:"http,omitempty"` // an HTTP GET on localhost
	Every  Duration   `json:"every"`
	Within Duration   `json:"within"`
}

// ReadyHTTP is an HTTP check: a GET of Path on Port, ready on a 2xx answer.
type ReadyHTTP struct {
	Path string `json:"path"`
	Port int    `json:"port"`
}

// Kind says which check it is: run, tcp or http.
func (r *Ready) Kind() string {
	switch {
	case r.Run != "":
		return "run"
	case r.TCP > 0:
		return "tcp"
	}
	return "http"
}

// Describe says what is waited for, for the log.
func (r *Ready) Describe() string {
	switch r.Kind() {
	case "run":
		return "`" + r.Run + "` to succeed"
	case "tcp":
		return fmt.Sprintf("port %d to accept connections", r.TCP)
	}
	return fmt.Sprintf("GET localhost:%d%s to answer 2xx", r.HTTP.Port, r.HTTP.Path)
}

func (ps *parser) parseReady(n *yaml.Node, at string) *Ready {
	r := &Ready{Every: Duration(DefaultReadyEvery), Within: Duration(DefaultReadyWithin)}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "ready is a mapping with one of run, tcp or http, for example: ready: { run: \"pg_isready\" } or { tcp: 6379 }")
		return r
	}
	checks := 0
	dur := func(v *yaml.Node, w string) (Duration, bool) {
		d, err := time.ParseDuration(ps.scalar(v, w))
		if err != nil {
			ps.add(v, w, "write a duration such as 2s or 1m")
			return 0, false
		}
		return Duration(d), true
	}
	port := func(v *yaml.Node, w string) int {
		p := ps.int(v, w)
		if p < 1 || p > 65535 {
			ps.add(v, w, "a port is 1 to 65535")
		}
		return p
	}
	ps.eachKey(n, at, []string{"run", "tcp", "http", "every", "within"}, func(k, v *yaml.Node) {
		w := at + "." + k.Value
		switch k.Value {
		case "run":
			checks++
			r.Run = strings.TrimSpace(ps.scalar(v, w))
			if r.Run == "" {
				ps.add(v, w, "write the command that exits 0 when the service is ready, for example: pg_isready -U postgres")
			}
		case "tcp":
			checks++
			r.TCP = port(v, w)
		case "http":
			checks++
			r.HTTP = &ReadyHTTP{Path: "/"}
			if v.Kind != yaml.MappingNode {
				ps.add(v, w, "http is { path: /health, port: 8080 }")
				return
			}
			ps.eachKey(v, w, []string{"path", "port"}, func(k2, v2 *yaml.Node) {
				if k2.Value == "path" {
					r.HTTP.Path = ps.scalar(v2, w+".path")
					if !strings.HasPrefix(r.HTTP.Path, "/") || strings.ContainsAny(r.HTTP.Path, " \r\n'\"`$;|&") {
						ps.add(v2, w+".path", "a path starts with / and has no spaces or shell characters, for example: /health")
					}
				} else {
					r.HTTP.Port = port(v2, w+".port")
				}
			})
			if r.HTTP.Port == 0 {
				ps.add(v, w+".port", "say which port, for example: http: { path: /health, port: 8080 }")
			}
		case "every":
			if d, ok := dur(v, w); ok {
				r.Every = d
				if d.D() < MinReadyEvery {
					ps.add(v, w, "every is at least %s", Duration(MinReadyEvery).Short())
				}
			}
		case "within":
			if d, ok := dur(v, w); ok {
				r.Within = d
				if d.D() < time.Second || d.D() > MaxReadyWithin {
					ps.add(v, w, "within is between 1s and %s", Duration(MaxReadyWithin).Short())
				}
			}
		}
	})
	if checks != 1 {
		ps.add(n, at, "give exactly one of run, tcp or http")
	}
	if r.Every.D() > r.Within.D() {
		ps.add(n, at+".every", "every (%s) is longer than within (%s), so the service would be checked once", r.Every.Short(), r.Within.Short())
	}
	return r
}

// readyContainers reports whether any container asks for readiness.
func (p *Pipeline) usesReady() bool {
	for _, c := range p.Containers {
		if c.Ready != nil {
			return true
		}
	}
	return false
}
