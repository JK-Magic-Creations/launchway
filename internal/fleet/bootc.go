package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Hosts built as bootable container images (bootc). An update is staged on
// the host without a reboot; the host is drained, reboots into the new image,
// is health-checked, and is admitted. A host that does not pass its check is
// returned to the image it came from with `bootc rollback` and one more
// reboot, and the plan stops.
//
// A reboot is an outage for that host: the plan keeps the other hosts
// serving (batches smaller than the fleet, a drain first), and a fleet of
// one drops requests while it reboots; the report says so.
//
// Every mutation happens on the target host only, after the plan has read the
// target's machine id and found it differs from the machine the Station runs
// on (a Station that deployed to itself would reboot itself).

// BootcStatus is what `bootc status --json` says, reduced to what a plan
// needs.
type BootcStatus struct {
	Booted   string `json:"booted"`   // image digest of the booted deployment
	Staged   string `json:"staged"`   // digest of the staged deployment, or ""
	Rollback string `json:"rollback"` // digest of the rollback deployment, or ""
	Image    string `json:"image"`    // the image reference it follows
}

type bootcJSON struct {
	Spec   struct{ Image struct{ Image string } }
	Status struct {
		Booted, Staged, Rollback *struct {
			Image struct{ ImageDigest string }
		}
	}
}

// ParseBootcStatus reads the output of `bootc status --json`.
func ParseBootcStatus(out string) (BootcStatus, error) {
	var j bootcJSON
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		return BootcStatus{}, fmt.Errorf("bootc status could not be read: %w", err)
	}
	s := BootcStatus{Image: j.Spec.Image.Image}
	if b := j.Status.Booted; b != nil {
		s.Booted = b.Image.ImageDigest
	}
	if b := j.Status.Staged; b != nil {
		s.Staged = b.Image.ImageDigest
	}
	if b := j.Status.Rollback; b != nil {
		s.Rollback = b.Image.ImageDigest
	}
	if s.Booted == "" {
		return s, errors.New("the host is not booted from an image (bootc reports no booted image)")
	}
	return s, nil
}

var imageRefRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]{0,200}@sha256:[0-9a-f]{64}$`)

// Digest returns the sha256 part of an image reference pinned by digest.
func ImageDigest(ref string) (string, error) {
	if !imageRefRe.MatchString(ref) {
		return "", fmt.Errorf("image %q: a host takes an image by digest (name@sha256:...); a tag can move", ref)
	}
	return ref[strings.Index(ref, "@")+1:], nil
}

// BootcSpec is one image rollout.
type BootcSpec struct {
	Image          string // name@sha256:..., as the host reaches the registry
	MaxUnavailable int
	DrainMax       time.Duration
	PollEvery      time.Duration // how often a rebooting host is asked if it is back; default 2s
	// BootWithin is how long a host may take to come back; default 5m.
	BootWithin  time.Duration
	Healthy     int
	CheckEvery  time.Duration
	CheckWithin time.Duration
	// AllowSelf permits a target that is the Station's own machine. It is
	// for nothing but tests of the refusal.
	AllowSelf bool
}

// BootcReport says what an image rollout did, per host.
type BootcReport struct {
	Image       string            `json:"image"`
	Hosts       map[string]string `json:"hosts"` // name -> digest it ended on
	Updated     []string          `json:"updated"`
	Returned    []string          `json:"returned,omitempty"`
	Quarantined []string          `json:"quarantined,omitempty"`
	Failure     string            `json:"failure,omitempty"`
}

// ErrBootcReturned means a host failed and the touched hosts were returned.
var ErrBootcReturned = errors.New("the image update failed and the hosts were returned to their previous image")

// BootcStatus reads a host's status.
func (f *Fleet) BootcStatus(ctx context.Context, h Host) (BootcStatus, error) {
	out, err := f.run(ctx, h, "sudo -n /usr/bin/bootc status --json", nil)
	if err != nil {
		return BootcStatus{}, err
	}
	return ParseBootcStatus(out)
}

// Drift reports, for each host, the digest it booted: the fleet's image view.
func (f *Fleet) Drift(ctx context.Context, hosts []Host, want string) (map[string]string, []string, error) {
	got := map[string]string{}
	var off []string
	for _, h := range hosts {
		st, err := f.BootcStatus(ctx, h)
		if err != nil {
			return got, off, fmt.Errorf("host %s: %w", h.Name, err)
		}
		got[h.Name] = st.Booted
		if want != "" && st.Booted != want {
			off = append(off, h.Name)
		}
	}
	return got, off, nil
}

func localMachineID() string {
	b, _ := os.ReadFile("/etc/machine-id")
	return strings.TrimSpace(string(b))
}

// refuseSelf stops a plan whose target is the machine the Station runs on.
func (f *Fleet) refuseSelf(ctx context.Context, h Host) error {
	mine := localMachineID()
	out, err := f.run(ctx, h, "cat /etc/machine-id", nil)
	if err != nil {
		return fmt.Errorf("host %s: its machine id could not be read, so it cannot be told apart from the Station's own machine: %w", h.Name, err)
	}
	if mine != "" && strings.TrimSpace(out) == mine {
		return fmt.Errorf("host %s is the machine this Station runs on; an image update reboots the machine and is refused", h.Name)
	}
	return nil
}

func (f *Fleet) bootID(ctx context.Context, h Host) (string, error) {
	out, err := f.run(ctx, h, "cat /proc/sys/kernel/random/boot_id", nil)
	return strings.TrimSpace(out), err
}

// reboot asks the host to reboot and waits until it answers with a new boot.
func (f *Fleet) reboot(ctx context.Context, h Host, within, every time.Duration) error {
	before, err := f.bootID(ctx, h)
	if err != nil {
		return err
	}
	if _, err := f.run(ctx, h, "sudo -n /usr/bin/systemctl reboot --no-block", nil); err != nil {
		return err
	}
	deadline := time.Now().Add(within)
	for {
		select {
		case <-time.After(every):
		case <-ctx.Done():
			return ctx.Err()
		}
		if id, err := f.bootID(ctx, h); err == nil && id != "" && id != before {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("host %s did not come back within %s", h.Name, within)
		}
	}
}

// BootcRoll moves the hosts to the image, in batches.
func (f *Fleet) BootcRoll(ctx context.Context, hosts []Host, spec BootcSpec) (*BootcReport, error) {
	rep := &BootcReport{Image: spec.Image, Hosts: map[string]string{}}
	want, err := ImageDigest(spec.Image)
	if err != nil {
		return rep, err
	}
	if spec.MaxUnavailable < 1 {
		spec.MaxUnavailable = 1
	}
	if spec.BootWithin <= 0 {
		spec.BootWithin = 5 * time.Minute
	}
	if spec.Healthy <= 0 {
		spec.Healthy = 2
	}
	if spec.CheckEvery <= 0 {
		spec.CheckEvery = time.Second
	}
	if spec.CheckWithin <= 0 {
		spec.CheckWithin = time.Minute
	}
	if f.Traffic == nil && !f.AllowNoTraffic {
		return rep, errors.New("this fleet has no traffic provider, so hosts cannot be drained before their reboot; add one, or say allow_no_traffic: true (the host is then unavailable while it reboots)")
	}
	if f.Traffic != nil && spec.MaxUnavailable >= len(hosts) {
		return rep, fmt.Errorf("max_unavailable %d would reboot all %d hosts at once; use at most %d", spec.MaxUnavailable, len(hosts), len(hosts)-1)
	}
	prev := map[string]string{}
	for _, h := range hosts {
		if err := h.Validate(); err != nil {
			return rep, err
		}
		if f.Traffic != nil && h.Member == "" {
			return rep, fmt.Errorf("host %s has no load balancer member", h.Name)
		}
		if f.Refuse != nil {
			if err := f.Refuse(h); err != nil {
				return rep, err
			}
		}
		if !spec.AllowSelf {
			if err := f.refuseSelf(ctx, h); err != nil {
				return rep, err
			}
		}
		if err := f.Check.Healthy(ctx, f.CheckURL(h)); err != nil {
			// a host with no service yet (a first install of the image) is
			// allowed to fail this once; any other host must be healthy.
			if st, serr := f.BootcStatus(ctx, h); serr != nil || st.Booted == want {
				return rep, fmt.Errorf("host %s is not healthy before the update (%v)", h.Name, err)
			}
		}
		st, err := f.BootcStatus(ctx, h)
		if err != nil {
			return rep, fmt.Errorf("host %s: %w", h.Name, err)
		}
		prev[h.Name] = st.Booted
		rep.Hosts[h.Name] = st.Booted
	}
	var touched []Host
	for i := 0; i < len(hosts); i += spec.MaxUnavailable {
		batch := hosts[i:min(i+spec.MaxUnavailable, len(hosts))]
		for _, h := range batch {
			touched = append(touched, h)
			if err := f.bootcOne(ctx, h, want, spec, rep); err != nil {
				rep.Failure = err.Error()
				f.Events.emit(h.Name, "failed", "%v", err)
				f.bootcReturn(context.WithoutCancel(ctx), touched, prev, spec, rep)
				return rep, fmt.Errorf("%w: %v", ErrBootcReturned, err)
			}
		}
	}
	f.Events.emit("", "done", "all %d hosts booted %s", len(hosts), want)
	return rep, nil
}

func (f *Fleet) bootcOne(ctx context.Context, h Host, want string, spec BootcSpec, rep *BootcReport) error {
	f.Events.emit(h.Name, "stage", "staging %s (no reboot yet)", spec.Image)
	if _, err := f.run(ctx, h, "sudo -n /usr/bin/bootc switch --transport registry "+q(spec.Image), nil); err != nil {
		return fmt.Errorf("host %s: %w", h.Name, err)
	}
	st, err := f.BootcStatus(ctx, h)
	if err != nil {
		return err
	}
	if st.Staged != want {
		return fmt.Errorf("host %s staged %q, not the image asked for %s", h.Name, st.Staged, want)
	}
	if f.Traffic != nil {
		f.Events.emit(h.Name, "drain", "taking %s out of rotation before its reboot", h.Member)
		if err := f.Traffic.Drain(ctx, h.Member); err != nil {
			return err
		}
		r := &roller{f: f, spec: RollSpec{DrainMax: orDur(spec.DrainMax, 30*time.Second)}, rep: &Report{}}
		if err := r.waitDrained(ctx, h); err != nil {
			return err
		}
	}
	f.Events.emit(h.Name, "reboot", "rebooting into %s", want)
	if err := f.reboot(ctx, h, spec.BootWithin, orDur(spec.PollEvery, 2*time.Second)); err != nil {
		return err
	}
	st, err = f.BootcStatus(ctx, h)
	if err != nil {
		return err
	}
	if st.Booted != want {
		return fmt.Errorf("host %s booted %s, not %s", h.Name, st.Booted, want)
	}
	f.Events.emit(h.Name, "check", "booted %s; waiting for %s", want, f.CheckURL(h))
	if err := waitHealthy(ctx, f.Check, f.CheckURL(h), spec.Healthy, spec.CheckEvery, spec.CheckWithin); err != nil {
		return fmt.Errorf("host %s on %s: %w", h.Name, want, err)
	}
	rep.Hosts[h.Name] = want
	rep.Updated = append(rep.Updated, h.Name)
	if f.Traffic != nil {
		f.Events.emit(h.Name, "admit", "putting %s back in rotation", h.Member)
		if err := f.Traffic.Admit(ctx, h.Member); err != nil {
			return err
		}
		return f.waitMember(ctx, h.Member, "up", 30*time.Second)
	}
	return nil
}

// bootcReturn puts every touched host back on the image it came from.
func (f *Fleet) bootcReturn(ctx context.Context, touched []Host, prev map[string]string, spec BootcSpec, rep *BootcReport) {
	for _, h := range touched {
		want := prev[h.Name]
		st, err := f.BootcStatus(ctx, h)
		if err != nil {
			rep.Quarantined = append(rep.Quarantined, h.Name)
			f.Events.emit(h.Name, "quarantine", "its state is unknown: %v", err)
			continue
		}
		if st.Booted != want {
			f.Events.emit(h.Name, "return", "bootc rollback to %s", want)
			if _, err := f.run(ctx, h, "sudo -n /usr/bin/bootc rollback", nil); err != nil {
				rep.Quarantined = append(rep.Quarantined, h.Name)
				f.Events.emit(h.Name, "quarantine", "%v", err)
				continue
			}
			if err := f.reboot(ctx, h, spec.BootWithin, orDur(spec.PollEvery, 2*time.Second)); err != nil {
				rep.Quarantined = append(rep.Quarantined, h.Name)
				f.Events.emit(h.Name, "quarantine", "%v", err)
				continue
			}
			if st, err = f.BootcStatus(ctx, h); err != nil || st.Booted != want {
				rep.Quarantined = append(rep.Quarantined, h.Name)
				f.Events.emit(h.Name, "quarantine", "after the rollback it booted %s, not %s", st.Booted, want)
				continue
			}
		} else if st.Staged != "" {
			// Staged but never booted: the next reboot would take it. Undo it.
			f.Events.emit(h.Name, "return", "undoing a staged update")
			if _, err := f.run(ctx, h, "sudo -n /usr/bin/bootc switch --transport registry "+q(st.Image), nil); err != nil {
				f.Events.emit(h.Name, "quarantine", "%v", err)
			}
		}
		rep.Hosts[h.Name] = want
		rep.Returned = append(rep.Returned, h.Name)
		if f.Traffic != nil {
			if err := waitHealthy(ctx, f.Check, f.CheckURL(h), spec.Healthy, spec.CheckEvery, spec.CheckWithin); err != nil {
				rep.Quarantined = append(rep.Quarantined, h.Name)
				f.Events.emit(h.Name, "quarantine", "not healthy after the return: %v", err)
				continue
			}
			_ = f.Traffic.Admit(ctx, h.Member)
		}
	}
}
