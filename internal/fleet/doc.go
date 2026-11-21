// Package fleet delivers a release to a fleet of hosts with no downtime: the
// hosts are reached over SSH from a Station, taken out of the load
// balancer's rotation (drained) before each change, changed in batches,
// health-checked one by one, and put back; a failed health check returns
// every touched host to the previous release (Return to base). A second
// plan, the Pathfinder, brings a new group of hosts into service by
// weights, with verification between steps and automatic weight rollback.
//
// Everything the package does to a host goes through a Remote, which is
// audited; everything it does to traffic goes through a Traffic provider
// (HAProxy's runtime API is the first). Neither needs the other's real
// implementation to be tested.
package fleet

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Host is one member of a fleet, as the Airfield's settings state it.
type Host struct {
	Name    string `json:"name" yaml:"name"`
	Address string `json:"address" yaml:"address"` // where SSH answers: host or host:port
	User    string `json:"user,omitempty" yaml:"user,omitempty"`
	// HostKey is the pinned public key in authorized_keys format. A host
	// whose key differs, or that has none, is refused: there is no "accept
	// new".
	HostKey string `json:"host_key" yaml:"host_key"`
	Group   string `json:"group,omitempty" yaml:"group,omitempty"`
	// Service is host:port where the service answers, as the Station sees it;
	// the health check's {service} stands for it.
	Service string `json:"service" yaml:"service"`
	// Member is the load balancer's name for the host: backend/server.
	Member string `json:"member,omitempty" yaml:"member,omitempty"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	releaseRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	userRe    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// Validate reports what is wrong with the host's settings.
func (h Host) Validate() error {
	switch {
	case !nameRe.MatchString(h.Name):
		return fmt.Errorf("host name %q: use lower-case letters, digits and hyphens", h.Name)
	case h.Address == "":
		return fmt.Errorf("host %s: address is required", h.Name)
	case h.User != "" && !userRe.MatchString(h.User):
		return fmt.Errorf("host %s: %q is not a valid user name", h.Name, h.User)
	case strings.TrimSpace(h.HostKey) == "":
		return fmt.Errorf("host %s: host_key is required: pin the host's public key (ssh-keyscan prints it); Launchway never trusts a key on first use", h.Name)
	case h.Service == "":
		return fmt.Errorf("host %s: service (host:port the service answers on) is required", h.Name)
	case h.Member != "" && strings.Count(h.Member, "/") != 1:
		return fmt.Errorf("host %s: member must be backend/server", h.Name)
	}
	return nil
}

// Layout says where a service lives on its hosts.
type Layout struct {
	Unit string // the systemd unit, which runs <Dir>/current/<Binary>
	Dir  string // /opt/<unit>
	// Binary is the file name of the release artifact.
	Binary string
}

// NewLayout returns the default layout for a service.
func NewLayout(unit string) (Layout, error) {
	if !nameRe.MatchString(unit) {
		return Layout{}, fmt.Errorf("service %q: use lower-case letters, digits and hyphens", unit)
	}
	return Layout{Unit: unit, Dir: "/opt/" + unit, Binary: unit}, nil
}

// Event is one thing that happened, for the run page and the log.
type Event struct {
	Time    time.Time      `json:"time"`
	Host    string         `json:"host,omitempty"`
	Phase   string         `json:"phase"` // drain, install, restart, check, admit, return, quarantine, weights, verify, done, failed
	Message string         `json:"message"`
	Weights map[string]int `json:"weights,omitempty"`
}

// Sink receives events; it may be nil.
type Sink func(Event)

func (s Sink) emit(host, phase, format string, a ...any) {
	if s != nil {
		s(Event{Time: time.Now(), Host: host, Phase: phase, Message: fmt.Sprintf(format, a...)})
	}
}
