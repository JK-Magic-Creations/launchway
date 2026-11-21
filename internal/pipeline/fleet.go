package pipeline

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Fleet steps. A deploy step with a fleet: block delivers to a fleet of
// hosts (virtual machines or bare metal) instead of through Argo CD: a
// Station reaches each host over SSH, takes it out of the load balancer's
// rotation, changes it, checks it and puts it back, in batches, and returns
// every touched host to the previous release when a check fails. With
// strategy pathfinder, a second group of hosts takes 10, 50, then 100 percent
// of the traffic with verification between steps. With method bootc the
// change is an operating-system image, switched to by digest and booted.
//
// A Station runs it today with `launchway fleet run`; the server does not
// schedule fleet steps yet and refuses a run that has one, saying so.

// FleetHostName stands in a fleet check's address for each host's own
// service address: write url: /healthz and the check is made against every
// host in turn (and against the load balancer's address for verification).
const FleetHostName = "fleet-host.invalid"

// Fleet limits and defaults.
const (
	MaxFleetBatch      = 100
	DefaultFleetDrain  = 30 * time.Second
	MaxFleetDrain      = time.Hour
	MaxPathfinderSteps = 10
)

// FleetSpec says how a deploy step delivers to a fleet.
type FleetSpec struct {
	// Method is systemd (a release artifact run by a systemd unit; the
	// default) or bootc (an operating-system image, by digest).
	Method string `json:"method"`
	// Strategy is rolling (in place, in batches; the default) or pathfinder.
	Strategy string `json:"strategy"`
	// Service is the systemd unit (method systemd).
	Service string `json:"service,omitempty"`
	// Artifact is the file to install, relative to the workspace (method
	// systemd).
	Artifact string `json:"artifact,omitempty"`
	// Image is the operating-system image, by digest: name@sha256:... (method
	// bootc), written in place or taken from an earlier step's output.
	Image          *Value   `json:"image,omitempty"`
	MaxUnavailable int      `json:"max_unavailable"`
	Drain          Duration `json:"drain"`
	// Check is the health check made against each host before it is admitted;
	// its url is a path (see FleetHostName).
	Check   *HealthCheck  `json:"check"`
	Traffic *FleetTraffic `json:"traffic,omitempty"`
	SSH     *FleetSSH     `json:"ssh,omitempty"`
	// Pathfinder is for strategy pathfinder.
	Pathfinder *FleetPathfinder `json:"pathfinder,omitempty"`
	// AllowNoTraffic says the fleet has no load balancer to drain from; hosts
	// are then unavailable while they change.
	AllowNoTraffic bool `json:"allow_no_traffic,omitempty"`
}

// FleetTraffic names the traffic provider and where to reach it.
type FleetTraffic struct {
	Provider string `json:"provider"` // haproxy
	// Admin is how the Station reaches the provider's admin socket: unix:/path
	// or tcp:host:port. It must never be open to a network.
	Admin string `json:"admin"`
	// URL is the balancer's own address (http://host:port), which a Pathfinder's
	// verification calls to judge each step as users would see it.
	URL string `json:"url,omitempty"`
}

// FleetSSH says how hosts are reached: a key by reference, never in the
// file. The hosts' public keys are pinned in the Airfield's settings.
type FleetSSH struct {
	Key SecretRef `json:"key"`
}

// FleetPathfinder is the weights plan.
type FleetPathfinder struct {
	Steps []int    `json:"steps"` // percent, rising
	Bake  Duration `json:"bake"`
	Watch Duration `json:"watch,omitempty"`
	// StableGroup and NewGroup are the inventory groups.
	StableGroup string `json:"stable_group"`
	NewGroup    string `json:"new_group"`
}

var (
	fleetKeys        = []string{"method", "strategy", "service", "artifact", "image", "max_unavailable", "drain", "check", "traffic", "ssh", "pathfinder", "allow_no_traffic"}
	fleetTrafficKeys = []string{"provider", "admin", "url"}
	fleetSSHKeys     = []string{"key"}
	fleetPathKeys    = []string{"steps", "bake", "watch", "stable_group", "new_group"}
	fleetNameRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	adminRe          = regexp.MustCompile(`^(unix:/[A-Za-z0-9_./-]{1,200}|tcp:[A-Za-z0-9_.-]{1,200}:[0-9]{1,5})$`)
)

// UsesFleet reports whether the pipeline has a deploy step with a fleet.
func (p *Pipeline) UsesFleet() bool {
	for _, s := range p.Steps {
		if s.Fleet != nil {
			return true
		}
	}
	return false
}

func (ps *parser) parseFleet(n *yaml.Node, at string, s *Step) *FleetSpec {
	f := &FleetSpec{Method: "systemd", Strategy: "rolling", MaxUnavailable: 1, Drain: Duration(DefaultFleetDrain)}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "fleet is a mapping, for example: { service: web, artifact: dist/web, check: { url: /healthz }, traffic: { provider: haproxy, admin: \"unix:/run/haproxy/admin.sock\" } }")
		return f
	}
	dur := func(v *yaml.Node, where string) Duration {
		d, err := time.ParseDuration(ps.scalar(v, where))
		if err != nil || d < 0 {
			ps.add(v, where, "write a duration such as 30s")
			return 0
		}
		return Duration(d)
	}
	ps.eachKey(n, at, fleetKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "method":
			f.Method = ps.scalar(v, where)
		case "strategy":
			f.Strategy = ps.scalar(v, where)
		case "service":
			f.Service = ps.scalar(v, where)
		case "artifact":
			f.Artifact = ps.scalar(v, where)
		case "image":
			f.Image = ps.value(v, where)
		case "max_unavailable":
			f.MaxUnavailable = ps.int(v, where)
		case "drain":
			f.Drain = dur(v, where)
		case "allow_no_traffic":
			f.AllowNoTraffic = ps.bool(v, where)
		case "check":
			f.Check = ps.parseFleetCheck(v, where)
		case "traffic":
			t := &FleetTraffic{}
			ps.eachKey(v, where, fleetTrafficKeys, func(k2, v2 *yaml.Node) {
				switch k2.Value {
				case "provider":
					t.Provider = ps.scalar(v2, where+".provider")
				case "admin":
					t.Admin = ps.scalar(v2, where+".admin")
				case "url":
					t.URL = ps.scalar(v2, where+".url")
				}
			})
			f.Traffic = t
		case "ssh":
			f.SSH = &FleetSSH{}
			ps.eachKey(v, where, fleetSSHKeys, func(_, v2 *yaml.Node) {
				ps.eachKey(v2, where+".key", secretKeys, func(k3, v3 *yaml.Node) {
					switch k3.Value {
					case "store":
						f.SSH.Key.Store = ps.secretText(v3, where+".key.store")
					case "key":
						f.SSH.Key.Key = ps.secretText(v3, where+".key.key")
					case "field":
						f.SSH.Key.Field = ps.secretText(v3, where+".key.field")
					}
				})
			})
		case "pathfinder":
			f.Pathfinder = &FleetPathfinder{Bake: Duration(30 * time.Second), StableGroup: "stable", NewGroup: "new"}
			ps.eachKey(v, where, fleetPathKeys, func(k2, v2 *yaml.Node) {
				w2 := where + "." + k2.Value
				switch k2.Value {
				case "steps":
					for _, x := range ps.valueList(v2, w2) {
						var p int
						if _, err := fmtSscan(x, &p); err != nil {
							ps.add(v2, w2, "steps are percentages, for example [10, 50, 100]")
							return
						}
						f.Pathfinder.Steps = append(f.Pathfinder.Steps, p)
					}
				case "bake":
					f.Pathfinder.Bake = dur(v2, w2)
				case "watch":
					f.Pathfinder.Watch = dur(v2, w2)
				case "stable_group":
					f.Pathfinder.StableGroup = ps.scalar(v2, w2)
				case "new_group":
					f.Pathfinder.NewGroup = ps.scalar(v2, w2)
				}
			})
		}
	})
	ps.checkFleet(n, at, f, s)
	return f
}

// parseFleetCheck reads the check: with a path as its url and the placeholder
// host in front, so that the one check parser (and its rules) is used.
func (ps *parser) parseFleetCheck(n *yaml.Node, at string) *HealthCheck {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "url" && strings.HasPrefix(n.Content[i+1].Value, "/") {
				cp := *n
				cp.Content = append([]*yaml.Node(nil), n.Content...)
				u := *n.Content[i+1]
				u.Value = "http://" + FleetHostName + u.Value
				cp.Content[i+1] = &u
				return ps.parseHealthCheck(&cp, at)
			}
		}
	}
	return ps.parseHealthCheck(n, at)
}

func (ps *parser) checkFleet(n *yaml.Node, at string, f *FleetSpec, s *Step) {
	switch f.Method {
	case "systemd", "bootc":
	default:
		ps.add(n, at+".method", "method is systemd (a release artifact under a systemd unit) or bootc (an operating-system image by digest)")
	}
	switch f.Strategy {
	case "rolling", "pathfinder":
	default:
		ps.add(n, at+".strategy", "strategy is rolling or pathfinder")
	}
	if f.Service == "" {
		f.Service = s.Service
	}
	if f.Method == "systemd" {
		if !fleetNameRe.MatchString(f.Service) {
			ps.add(n, at+".service", "name the systemd unit (lower-case letters, digits and hyphens), for example: service: web")
		}
		switch {
		case f.Artifact == "":
			ps.add(n, at+".artifact", "name the file to install, relative to the workspace, for example: artifact: dist/web")
		case strings.HasPrefix(f.Artifact, "/") || strings.Contains(f.Artifact, ".."):
			ps.add(n, at+".artifact", "the artifact is a path inside the workspace: no leading slash and no ..")
		}
		if f.Image != nil {
			ps.add(n, at+".image", "image is for method bootc")
		}
	} else {
		if f.Image == nil {
			ps.add(n, at+".image", "name the operating-system image by digest, for example: image: { output: build.image } (name@sha256:...)")
		} else if f.Image.Literal != "" && !strings.Contains(f.Image.Literal, "@sha256:") {
			ps.add(n, at+".image", "a host takes an image by digest (name@sha256:...), because a tag can move")
		}
		if f.Artifact != "" {
			ps.add(n, at+".artifact", "artifact is for method systemd")
		}
		if f.Strategy == "pathfinder" {
			ps.add(n, at+".strategy", "a Pathfinder for image hosts is not built yet; use rolling (hosts reboot in batches, drained first)")
		}
	}
	if f.MaxUnavailable < 1 || f.MaxUnavailable > MaxFleetBatch {
		ps.add(n, at+".max_unavailable", "max_unavailable is how many hosts change at once, from 1 to %d", MaxFleetBatch)
	}
	if f.Drain.D() > MaxFleetDrain {
		ps.add(n, at+".drain", "drain is at most %s", MaxFleetDrain)
	}
	if f.Check == nil {
		ps.add(n, at+".check", "say how a host is judged healthy before it is admitted, for example: check: { url: /healthz }")
	} else if f.Check.Kind == CheckHTTP {
		if u, err := url.Parse(f.Check.URL); err == nil && u.Hostname() != FleetHostName {
			ps.add(n, at+".check.url", "a fleet check's url is a path such as /healthz: it is called on each host in turn")
		}
	}
	if f.SSH == nil || f.SSH.Key.Store == "" || f.SSH.Key.Key == "" {
		ps.add(n, at+".ssh", "name the key the Station reaches hosts with, by reference, for example: ssh: { key: { store: vault, key: fleet/prod, field: private_key } }")
	}
	switch {
	case f.Traffic == nil && !f.AllowNoTraffic:
		ps.add(n, at+".traffic", "name the load balancer to drain hosts from, for example: traffic: { provider: haproxy, admin: \"unix:/run/haproxy/admin.sock\" }; "+
			"without one a host is unavailable while it changes (say allow_no_traffic: true to accept that)")
	case f.Traffic != nil:
		if f.Traffic.Provider != "haproxy" {
			ps.add(n, at+".traffic.provider", "the traffic provider is haproxy (others are not built yet)")
		}
		if !adminRe.MatchString(f.Traffic.Admin) {
			ps.add(n, at+".traffic.admin", "admin is unix:/path or tcp:host:port")
		}
		if f.Strategy == "pathfinder" {
			if u, err := url.Parse(f.Traffic.URL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				ps.add(n, at+".traffic.url", "a Pathfinder verifies each step through the balancer's own address, for example: url: http://lb.example:8000")
			}
		}
	}
	if f.Strategy == "pathfinder" {
		p := f.Pathfinder
		switch {
		case f.Traffic == nil:
			ps.add(n, at+".traffic", "a Pathfinder moves traffic by weights and needs a traffic provider")
		case p == nil:
			ps.add(n, at+".pathfinder", "say the steps, for example: pathfinder: { steps: [10, 50, 100], bake: 30s }")
		default:
			if len(p.Steps) == 0 {
				p.Steps = []int{10, 50, 100}
			}
			last := 0
			for _, x := range p.Steps {
				if x <= last || x > 100 {
					ps.add(n, at+".pathfinder.steps", "steps rise and end at most at 100, for example [10, 50, 100]")
					break
				}
				last = x
			}
			if len(p.Steps) > MaxPathfinderSteps {
				ps.add(n, at+".pathfinder.steps", "at most %d steps", MaxPathfinderSteps)
			}
			if !fleetNameRe.MatchString(p.StableGroup) || !fleetNameRe.MatchString(p.NewGroup) || p.StableGroup == p.NewGroup {
				ps.add(n, at+".pathfinder", "stable_group and new_group name two different groups of the Airfield's fleet")
			}
		}
	} else if f.Pathfinder != nil {
		ps.add(n, at+".pathfinder", "pathfinder is for strategy: pathfinder")
	}
}

func fmtSscan(s string, p *int) (int, error) { return fmt.Sscan(s, p) }

// Target0 returns the path of a fleet check's url (what is called on each
// host).
func (c *HealthCheck) Target0() string {
	if u, err := url.Parse(c.URL); err == nil {
		return u.RequestURI()
	}
	return c.URL
}
