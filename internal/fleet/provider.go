package fleet

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Provider is a platform a fleet lives on: static hosts reached by SSH, a
// local hypervisor, and (in later releases) the clouds' VM services,
// vSphere, Proxmox, OpenStack, KubeVirt and bare metal. A provider is run by
// the Station inside the environment, so no credential for the platform
// reaches the hosted side. The verbs that create, replace and retire
// machines and import and publish images extend this interface when the
// first provider that has them is built (docs/62 section 7A of the design
// notes); the two here are what every provider has.
type Provider interface {
	Name() string
	// Inventory lists the fleet.
	Inventory(ctx context.Context) ([]Host, error)
	// Probe reads what the environment allows. It changes nothing.
	Probe(ctx context.Context) (*Probe, error)
}

// Probe is the capability probe's answer: facts the planner chooses by.
// A zero value in a Free field means "not known" only when Known says so.
type Probe struct {
	Provider string    `json:"provider"`
	At       time.Time `json:"at"`
	// Permissions are the actions the plan needs and whether the identity
	// holds each, with the exact name of what is missing.
	Permissions []Permission `json:"permissions"`
	// Capacity is what remains on the platform (hypervisor or quota).
	Capacity Capacity `json:"capacity"`
	// KVM says hardware virtualization is usable (needed to boot images in a
	// test VM).
	KVM bool `json:"kvm"`
	// Internet says whether outbound internet answered; when false, images
	// and packages must come from the Depot's mirror.
	Internet *bool    `json:"internet,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// Permission is one action the plan needs.
type Permission struct {
	Action string `json:"action"`
	Held   bool   `json:"held"`
	Detail string `json:"detail,omitempty"`
}

// Capacity is remaining resources. Hosts is how many more hosts of the
// asked size fit, when the platform can say.
type Capacity struct {
	VCPUs    int   `json:"vcpus"`
	MemoryMB int64 `json:"memory_mb"`
	DiskMB   int64 `json:"disk_mb"`
}

// Missing returns the permissions not held.
func (p *Probe) Missing() []Permission {
	var m []Permission
	for _, x := range p.Permissions {
		if !x.Held {
			m = append(m, x)
		}
	}
	return m
}

// Strategy names how a release reaches the fleet.
type Strategy string

// The strategies the planner chooses among.
const (
	StrategyBlueGreen Strategy = "blue-green" // a second fleet beside the first
	StrategyRolling   Strategy = "rolling"    // in place, in batches
)

// Want is what a plan needs of the environment.
type Want struct {
	Prefer Strategy
	Hosts  int
	// Per host, for a new machine (a second fleet or a surge host).
	VCPUs        int
	MemoryMB     int64
	DiskMB       int64
	NeedKVM      bool // the plan boots images in a test VM
	NeedInternet bool
	Actions      []string // permissions the plan needs (informational check against the probe)
}

// Decision is the planner's answer.
type Decision struct {
	Strategy Strategy `json:"strategy,omitempty"`
	// Why explains the choice, in plain words.
	Why []string `json:"why"`
	// Refusals are exact: what is missing, how much is needed, how much is
	// there. A non-empty list means nothing was started and nothing will be.
	Refusals []string `json:"refusals,omitempty"`
}

// Err returns the refusal as an error, or nil.
func (d Decision) Err() error {
	if len(d.Refusals) == 0 {
		return nil
	}
	return errors.New("refused before anything was changed: " + strings.Join(d.Refusals, "; "))
}

// Plan chooses the safest strategy the probe says the environment supports,
// or refuses naming exactly what is missing. Blue-green needs a second fleet
// (Hosts new machines); rolling in place needs none.
func Plan(p *Probe, w Want) Decision {
	var d Decision
	for _, m := range p.Missing() {
		d.Refusals = append(d.Refusals, fmt.Sprintf("the permission %s is not held (%s)", m.Action, orStr(m.Detail, "the identity lacks it")))
	}
	if w.NeedKVM && !p.KVM {
		d.Refusals = append(d.Refusals, "this environment has no usable /dev/kvm, so an image cannot be booted for a test here; a Station with KVM is needed")
	}
	if w.NeedInternet && p.Internet != nil && !*p.Internet {
		d.Refusals = append(d.Refusals, "there is no outbound internet and this plan needs it; mirror its inputs into the Depot")
	}
	if len(d.Refusals) > 0 {
		return d
	}
	fits := func(n int) (bool, string) {
		var miss []string
		if w.VCPUs > 0 && p.Capacity.VCPUs < n*w.VCPUs {
			miss = append(miss, fmt.Sprintf("%d vCPUs (%d available)", n*w.VCPUs, p.Capacity.VCPUs))
		}
		if w.MemoryMB > 0 && p.Capacity.MemoryMB < int64(n)*w.MemoryMB {
			miss = append(miss, fmt.Sprintf("%d MB of memory (%d MB available)", int64(n)*w.MemoryMB, p.Capacity.MemoryMB))
		}
		if w.DiskMB > 0 && p.Capacity.DiskMB < int64(n)*w.DiskMB {
			miss = append(miss, fmt.Sprintf("%d MB of disk (%d MB available)", int64(n)*w.DiskMB, p.Capacity.DiskMB))
		}
		return len(miss) == 0, strings.Join(miss, ", ")
	}
	if w.Prefer == StrategyBlueGreen {
		if ok, miss := fits(w.Hosts); ok {
			d.Strategy = StrategyBlueGreen
			d.Why = append(d.Why, fmt.Sprintf("a second fleet of %d hosts fits", w.Hosts))
			return d
		} else {
			d.Why = append(d.Why, "a second fleet would need "+miss+"; using a rolling update in place")
		}
	}
	d.Strategy = StrategyRolling
	if w.Prefer != StrategyBlueGreen {
		d.Why = append(d.Why, "rolling in place, in batches")
	}
	return d
}

func orStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// ---- static hosts over SSH ----

// StaticProvider is a fixed list of hosts reached over SSH.
type StaticProvider struct {
	Hosts []Host
	Fleet *Fleet
}

// Name implements Provider.
func (s *StaticProvider) Name() string { return "ssh-hosts" }

// Inventory implements Provider.
func (s *StaticProvider) Inventory(context.Context) ([]Host, error) {
	for _, h := range s.Hosts {
		if err := h.Validate(); err != nil {
			return nil, err
		}
	}
	return append([]Host(nil), s.Hosts...), nil
}

// Probe reads, on every host, whether it can be reached with the pinned
// key, whether the deploy user may restart the service with sudo and
// nothing else needed, and how much disk the release directory has. The
// capacity is the smallest free disk over the hosts.
func (s *StaticProvider) Probe(ctx context.Context) (*Probe, error) {
	p := &Probe{Provider: s.Name(), At: time.Now(), KVM: false}
	least := int64(-1)
	for _, h := range s.Hosts {
		if _, err := s.Fleet.run(ctx, h, "true", nil); err != nil {
			p.Permissions = append(p.Permissions, Permission{Action: "ssh to " + h.Name, Detail: err.Error()})
			continue
		}
		p.Permissions = append(p.Permissions, Permission{Action: "ssh to " + h.Name, Held: true})
		out, err := s.Fleet.run(ctx, h, "sudo -n -l /usr/bin/systemctl restart "+q(s.Fleet.Layout.Unit), nil)
		p.Permissions = append(p.Permissions, Permission{Action: "sudo systemctl restart " + s.Fleet.Layout.Unit + " on " + h.Name, Held: err == nil,
			Detail: strings.TrimSpace(out)})
		df, err := s.Fleet.run(ctx, h, "df -Pk "+q(s.Fleet.Layout.Dir)+" | tail -1", nil)
		if f := strings.Fields(df); err == nil && len(f) >= 4 {
			if kb, e := strconv.ParseInt(f[3], 10, 64); e == nil && (least < 0 || kb/1024 < least) {
				least = kb / 1024
			}
		}
	}
	if least >= 0 {
		p.Capacity.DiskMB = least
	}
	return p, nil
}

// ---- the local hypervisor (libvirt and qemu with KVM) ----

// LocalHypervisor is the machine the Station runs on, used to boot test
// virtual machines under the user's own session. Its probe is what makes a
// boot test refuse, rather than fail half way, when memory, disk or KVM is
// not there.
type LocalHypervisor struct {
	// Dir is where disks live; its free space is the disk capacity.
	Dir string
	// Connect is the libvirt connection, default qemu:///session. Only the
	// user's session is ever used.
	Connect string
	// Reserve is memory to leave free for the machine's own work, in MB.
	ReserveMB int64
}

// Name implements Provider.
func (l *LocalHypervisor) Name() string { return "local-hypervisor" }

func (l *LocalHypervisor) connect() string {
	if l.Connect == "" {
		return "qemu:///session"
	}
	return l.Connect
}

// Inventory lists the domains of the session, none of which are hosts of a
// fleet until an Airfield says so; it is read-only.
func (l *LocalHypervisor) Inventory(ctx context.Context) ([]Host, error) {
	if !strings.HasPrefix(l.connect(), "qemu:///session") {
		return nil, errors.New("only the user's own libvirt session is used; a system connection is refused")
	}
	return nil, nil
}

// Probe reads KVM, memory, vCPUs and disk of this machine.
func (l *LocalHypervisor) Probe(ctx context.Context) (*Probe, error) {
	p := &Probe{Provider: l.Name(), At: time.Now()}
	if !strings.HasPrefix(l.connect(), "qemu:///session") {
		return nil, errors.New("only the user's own libvirt session is used; a system connection is refused")
	}
	// KVM: the device must open for reading and writing by this user.
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
		p.KVM = true
	} else {
		p.Notes = append(p.Notes, "/dev/kvm: "+err.Error())
	}
	p.Permissions = append(p.Permissions, Permission{Action: "open /dev/kvm", Held: p.KVM, Detail: strings.Join(p.Notes, "; ")})
	if mem, err := memAvailableMB(); err == nil {
		p.Capacity.MemoryMB = max(mem-l.ReserveMB, 0)
	} else {
		p.Notes = append(p.Notes, "memory: "+err.Error())
	}
	p.Capacity.VCPUs = runtime.NumCPU()
	if l.Dir != "" {
		var st syscall.Statfs_t
		if err := syscall.Statfs(l.Dir, &st); err == nil {
			p.Capacity.DiskMB = int64(st.Bavail) * int64(st.Bsize) / (1 << 20)
		} else {
			p.Notes = append(p.Notes, "disk: "+err.Error())
		}
	}
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		p.Permissions = append(p.Permissions, Permission{Action: "run qemu-system-x86_64", Detail: "it is not installed"})
	} else {
		p.Permissions = append(p.Permissions, Permission{Action: "run qemu-system-x86_64", Held: true})
	}
	return p, nil
}

func memAvailableMB() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemAvailable:"); ok {
			kb, err := strconv.ParseInt(strings.Fields(rest)[0], 10, 64)
			return kb / 1024, err
		}
	}
	return 0, errors.New("MemAvailable is not in /proc/meminfo")
}
