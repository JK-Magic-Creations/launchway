package fleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Fleet is what a delivery needs: the way to reach hosts, the way to move
// traffic, the health check, and where services live.
type Fleet struct {
	Remote  Remote
	Traffic Traffic // nil: no balancer, so no drain; refused unless AllowNoTraffic
	Check   Checker
	// CheckURL is the URL the check calls for a host (direct).
	CheckURL func(h Host) string
	Layout   Layout
	Events   Sink
	// AllowNoTraffic permits a plan with no traffic provider; the report then
	// says downtime is possible.
	AllowNoTraffic bool
	// Refuse, if set, is asked about every host before anything is done to
	// it; a non-nil error stops the plan. The Station sets it to refuse its
	// own machine.
	Refuse func(h Host) error
}

// RollSpec is one rolling in-place update.
type RollSpec struct {
	Release  string
	Artifact []byte
	// MaxUnavailable is how many hosts are out of rotation at once: the
	// batch size. It must leave at least one host serving.
	MaxUnavailable int
	// DrainMax is the longest to wait for in-flight work; default 30s.
	DrainMax time.Duration
	// Healthy is how many checks in a row must pass; default 2.
	Healthy                 int
	CheckEvery, CheckWithin time.Duration
	// Bake is how long a batch is watched through the balancer before the
	// next starts; Verify, if set, is called at its end and may veto.
	Bake   time.Duration
	Verify func(ctx context.Context) error
}

// Report says what a plan did.
type Report struct {
	Release       string            `json:"release"`
	Updated       []string          `json:"updated"`
	Previous      map[string]string `json:"previous"`
	Returned      []string          `json:"returned,omitempty"`
	Quarantined   []string          `json:"quarantined,omitempty"`
	DrainTimeouts []string          `json:"drain_timeouts,omitempty"`
	Failure       string            `json:"failure,omitempty"`
}

// ErrReturned means the update failed and the hosts were returned.
var ErrReturned = errors.New("the update failed and the fleet was returned to the previous release")

type hostState struct {
	prev      string
	installed bool
	drained   bool
}

type roller struct {
	f     *Fleet
	spec  RollSpec
	mu    sync.Mutex
	state map[string]*hostState
	rep   *Report
}

func (s *RollSpec) defaults() {
	if s.DrainMax <= 0 {
		s.DrainMax = 30 * time.Second
	}
	if s.Healthy <= 0 {
		s.Healthy = 2
	}
	if s.CheckEvery <= 0 {
		s.CheckEvery = 500 * time.Millisecond
	}
	if s.CheckWithin <= 0 {
		s.CheckWithin = 60 * time.Second
	}
}

// Roll updates the hosts in batches. A failure returns every host it touched
// to the previous release; the report says what was done either way.
func (f *Fleet) Roll(ctx context.Context, hosts []Host, spec RollSpec) (*Report, error) {
	spec.defaults()
	rep := &Report{Release: spec.Release, Previous: map[string]string{}}
	if !releaseRe.MatchString(spec.Release) {
		return rep, fmt.Errorf("release %q: use letters, digits, dots, hyphens and underscores", spec.Release)
	}
	if len(spec.Artifact) == 0 {
		return rep, errors.New("the release artifact is empty")
	}
	if len(hosts) == 0 {
		return rep, errors.New("the fleet has no hosts")
	}
	if spec.MaxUnavailable < 1 {
		spec.MaxUnavailable = 1
	}
	if f.Traffic == nil && !f.AllowNoTraffic {
		return rep, errors.New("this fleet has no traffic provider, so hosts cannot be drained and the update would drop requests; add one, or say allow_no_traffic: true")
	}
	if f.Traffic != nil && spec.MaxUnavailable >= len(hosts) {
		return rep, fmt.Errorf("max_unavailable %d would take all %d hosts out of rotation at once; use at most %d", spec.MaxUnavailable, len(hosts), len(hosts)-1)
	}
	for _, h := range hosts {
		if err := h.Validate(); err != nil {
			return rep, err
		}
		if f.Refuse != nil {
			if err := f.Refuse(h); err != nil {
				return rep, err
			}
		}
		if f.Traffic != nil && h.Member == "" {
			return rep, fmt.Errorf("host %s has no load balancer member", h.Name)
		}
	}
	r := &roller{f: f, spec: spec, state: map[string]*hostState{}, rep: rep}
	// Preflight: every host healthy now, and known to the balancer.
	f.Events.emit("", "preflight", "checking %d hosts before changing any", len(hosts))
	for _, h := range hosts {
		if err := f.Check.Healthy(ctx, f.CheckURL(h)); err != nil {
			return rep, fmt.Errorf("host %s is not healthy before the update (%v); nothing was changed", h.Name, err)
		}
	}
	if f.Traffic != nil {
		seen, err := f.Traffic.Observe(ctx)
		if err != nil {
			return rep, err
		}
		for _, h := range hosts {
			if m, ok := seen[h.Member]; !ok {
				return rep, fmt.Errorf("the load balancer does not know %s (host %s)", h.Member, h.Name)
			} else if m.State != "up" {
				return rep, fmt.Errorf("the load balancer reports %s as %s before the update; nothing was changed", h.Member, m.State)
			}
		}
	}
	for i := 0; i < len(hosts); i += spec.MaxUnavailable {
		batch := hosts[i:min(i+spec.MaxUnavailable, len(hosts))]
		f.Events.emit("", "batch", "batch of %d: %s", len(batch), names(batch))
		err := r.batch(ctx, batch)
		if err == nil && (spec.Bake > 0 || spec.Verify != nil) {
			err = r.bake(ctx)
		}
		if err != nil {
			rep.Failure = err.Error()
			f.Events.emit("", "failed", "%v; returning to %s", err, "the previous release")
			r.returnAll(context.WithoutCancel(ctx), hosts)
			return rep, fmt.Errorf("%w: %v", ErrReturned, err)
		}
	}
	f.Events.emit("", "done", "release %s is on all %d hosts", spec.Release, len(hosts))
	return rep, nil
}

func (r *roller) bake(ctx context.Context) error {
	select {
	case <-time.After(r.spec.Bake):
	case <-ctx.Done():
		return ctx.Err()
	}
	if r.spec.Verify != nil {
		if err := r.spec.Verify(ctx); err != nil {
			return fmt.Errorf("verification after the batch failed: %w", err)
		}
	}
	return nil
}

func (r *roller) st(h Host) *hostState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state[h.Name]
	if s == nil {
		s = &hostState{}
		r.state[h.Name] = s
	}
	return s
}

func (r *roller) batch(ctx context.Context, batch []Host) error {
	var wg sync.WaitGroup
	errs := make([]error, len(batch))
	for i, h := range batch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = r.one(ctx, h)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (r *roller) one(ctx context.Context, h Host) error {
	f, s := r.f, r.st(h)
	if f.Traffic != nil {
		f.Events.emit(h.Name, "drain", "taking %s out of rotation", h.Member)
		if err := f.Traffic.Drain(ctx, h.Member); err != nil {
			return fmt.Errorf("host %s: %w", h.Name, err)
		}
		s.drained = true
		if err := r.waitDrained(ctx, h); err != nil {
			return fmt.Errorf("host %s: %w", h.Name, err)
		}
	}
	prev, err := f.currentRelease(ctx, h)
	if err != nil {
		return fmt.Errorf("host %s: %w", h.Name, err)
	}
	s.prev = prev
	r.mu.Lock()
	r.rep.Previous[h.Name] = prev
	r.mu.Unlock()
	f.Events.emit(h.Name, "install", "uploading release %s (was %s)", r.spec.Release, orNone(prev))
	s.installed = true // from here on a failure returns it
	if err := f.install(ctx, h, r.spec.Release, r.spec.Artifact); err != nil {
		return fmt.Errorf("host %s: %w", h.Name, err)
	}
	f.Events.emit(h.Name, "restart", "restarting %s", f.Layout.Unit)
	if err := f.restart(ctx, h); err != nil {
		return fmt.Errorf("host %s: %w", h.Name, err)
	}
	f.Events.emit(h.Name, "check", "waiting for %s to be healthy", f.CheckURL(h))
	if err := waitHealthy(ctx, f.Check, f.CheckURL(h), r.spec.Healthy, r.spec.CheckEvery, r.spec.CheckWithin); err != nil {
		return fmt.Errorf("host %s on release %s: %w", h.Name, r.spec.Release, err)
	}
	if err := r.admit(ctx, h, s); err != nil {
		return err
	}
	r.mu.Lock()
	r.rep.Updated = append(r.rep.Updated, h.Name)
	r.mu.Unlock()
	return nil
}

func (r *roller) admit(ctx context.Context, h Host, s *hostState) error {
	if r.f.Traffic == nil {
		return nil
	}
	r.f.Events.emit(h.Name, "admit", "putting %s back in rotation", h.Member)
	if err := r.f.Traffic.Admit(ctx, h.Member); err != nil {
		return fmt.Errorf("host %s: %w", h.Name, err)
	}
	s.drained = false
	// The next batch must not start until the balancer itself sees this host
	// serving, or capacity dips below what the plan promised.
	return r.f.waitMember(ctx, h.Member, "up", 30*time.Second)
}

func (r *roller) waitDrained(ctx context.Context, h Host) error {
	deadline := time.Now().Add(r.spec.DrainMax)
	for {
		seen, err := r.f.Traffic.Observe(ctx)
		if err != nil {
			return err
		}
		n := seen[h.Member].Sessions
		if n == 0 {
			r.f.Events.emit(h.Name, "drain", "no requests in flight")
			return nil
		}
		if time.Now().After(deadline) {
			r.mu.Lock()
			r.rep.DrainTimeouts = append(r.rep.DrainTimeouts, h.Name)
			r.mu.Unlock()
			r.f.Events.emit(h.Name, "drain", "%d connections were still open after %s; they will be closed", n, r.spec.DrainMax)
			return nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (f *Fleet) waitMember(ctx context.Context, member, state string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		seen, err := f.Traffic.Observe(ctx)
		if err == nil && seen[member].State == state {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the load balancer did not report %s as %s within %s", member, state, within)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// returnAll puts every touched host back on its previous release and into
// rotation. A host that cannot be returned is left drained and reported.
func (r *roller) returnAll(ctx context.Context, hosts []Host) {
	f := r.f
	for _, h := range hosts {
		s := r.state[h.Name]
		if s == nil || (!s.installed && !s.drained) {
			continue
		}
		if s.installed && s.prev != "" {
			f.Events.emit(h.Name, "return", "returning to %s", s.prev)
			if err := f.switchTo(ctx, h, s.prev); err != nil {
				r.quarantine(h, err)
				continue
			}
			if err := waitHealthy(ctx, f.Check, f.CheckURL(h), r.spec.Healthy, r.spec.CheckEvery, r.spec.CheckWithin); err != nil {
				r.quarantine(h, err)
				continue
			}
			r.rep.Returned = append(r.rep.Returned, h.Name)
		} else if s.installed {
			r.quarantine(h, errors.New("it had no previous release to return to"))
			continue
		}
		if f.Traffic != nil && s.drained {
			if err := f.Traffic.Admit(ctx, h.Member); err != nil {
				r.quarantine(h, err)
				continue
			}
			s.drained = false
			_ = f.waitMember(ctx, h.Member, "up", 30*time.Second)
		}
	}
}

func (r *roller) quarantine(h Host, why error) {
	r.rep.Quarantined = append(r.rep.Quarantined, h.Name)
	r.f.Events.emit(h.Name, "quarantine", "left out of rotation for inspection: %v", why)
}

// ---- what is done on a host ----

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (f *Fleet) run(ctx context.Context, h Host, cmd string, stdin []byte) (string, error) {
	var in *bytes.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	var res Result
	var err error
	if in != nil {
		res, err = f.Remote.Run(ctx, h, cmd, in)
	} else {
		res, err = f.Remote.Run(ctx, h, cmd, nil)
	}
	if err != nil {
		return "", err
	}
	if res.Exit != 0 {
		return res.Stdout, fmt.Errorf("%q exited %d: %s", cmd, res.Exit, res.Stderr)
	}
	return res.Stdout, nil
}

func (f *Fleet) currentRelease(ctx context.Context, h Host) (string, error) {
	out, err := f.run(ctx, h, "readlink "+q(f.Layout.Dir+"/current")+" || true", nil)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if i := strings.LastIndex(out, "/"); i >= 0 {
		out = out[i+1:]
	}
	return out, nil
}

func (f *Fleet) install(ctx context.Context, h Host, release string, artifact []byte) error {
	dir := f.Layout.Dir + "/releases/" + release
	bin := dir + "/" + f.Layout.Binary
	if _, err := f.run(ctx, h, "mkdir -p "+q(dir), nil); err != nil {
		return err
	}
	if _, err := f.run(ctx, h, "cat > "+q(bin+".partial")+" && chmod 0755 "+q(bin+".partial")+" && mv -f "+q(bin+".partial")+" "+q(bin), artifact); err != nil {
		return err
	}
	out, err := f.run(ctx, h, "sha256sum "+q(bin), nil)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(artifact)
	if !strings.HasPrefix(out, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("the uploaded file's checksum differs from the artifact's; refusing to start it")
	}
	return f.link(ctx, h, release)
}

func (f *Fleet) link(ctx context.Context, h Host, release string) error {
	d := f.Layout.Dir
	_, err := f.run(ctx, h, "ln -sfn "+q("releases/"+release)+" "+q(d+"/current.new")+" && mv -T "+q(d+"/current.new")+" "+q(d+"/current"), nil)
	return err
}

func (f *Fleet) restart(ctx context.Context, h Host) error {
	_, err := f.run(ctx, h, "sudo -n /usr/bin/systemctl restart "+q(f.Layout.Unit), nil)
	return err
}

func (f *Fleet) switchTo(ctx context.Context, h Host, release string) error {
	if err := f.link(ctx, h, release); err != nil {
		return err
	}
	return f.restart(ctx, h)
}

func names(hs []Host) string {
	var n []string
	for _, h := range hs {
		n = append(n, h.Name)
	}
	return strings.Join(n, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}
