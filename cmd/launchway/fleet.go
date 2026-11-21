package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/fleet"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

const fleetUsage = `usage:
  launchway fleet probe --file F --step S --inventory I [--secrets-config C]
  launchway fleet run   --file F --step S --inventory I --release R [--image DIGEST_REF]
                        [--workspace DIR] [--secrets-config C] [--audit FILE] [--events FILE]

A Station runs a deploy step's fleet: block against the hosts of an inventory file
(hosts: [{ name, address, user, host_key, service, member, group }]). probe reads what
the environment allows and says what it would do, changing nothing. run does it: drains
each host, changes it, checks it, admits it, and returns every touched host to the
previous release when a check fails. Every command sent to a host is written to the
audit file as one JSON line.
`

type inventoryFile struct {
	Hosts []fleet.Host `yaml:"hosts"`
}

// fleetOpts are the flags of fleet probe and run.
type fleetOpts struct {
	verb, file, step, inventory, release, image, workspace, audit, events, secrets string
}

func parseFleetFlags(args []string) (*fleetOpts, error) {
	if len(args) == 0 || (args[0] != "run" && args[0] != "probe") {
		fmt.Fprint(os.Stderr, fleetUsage)
		return nil, exitErr(2)
	}
	o := &fleetOpts{verb: args[0]}
	fs := flag.NewFlagSet("fleet "+o.verb, flag.ExitOnError)
	fs.StringVar(&o.file, "file", "", "pipeline file (default .launchway/pipeline.yaml)")
	fs.StringVar(&o.step, "step", "", "the deploy step with a fleet: block")
	fs.StringVar(&o.inventory, "inventory", "", "the inventory file of the Airfield's hosts")
	fs.StringVar(&o.release, "release", "", "the release id (run)")
	fs.StringVar(&o.image, "image", "", "the image by digest, name@sha256:..., for method bootc")
	fs.StringVar(&o.workspace, "workspace", ".", "the directory the artifact path is relative to")
	fs.StringVar(&o.audit, "audit", "", "write every command sent to a host here, one JSON line each")
	fs.StringVar(&o.events, "events", "", "write the progress events here, one JSON line each")
	fs.StringVar(&o.secrets, "secrets-config", "", "YAML file of secret stores")
	_ = fs.Parse(args[1:])
	if o.step == "" || o.inventory == "" {
		fmt.Fprint(os.Stderr, fleetUsage)
		return nil, exitErr(2)
	}
	return o, nil
}

// loadFleetInput reads the step's fleet block and the inventory.
func loadFleetInput(o *fleetOpts) (*pipeline.FleetSpec, []fleet.Host, error) {
	var args []string
	if o.file != "" {
		args = []string{o.file}
	}
	p, err := load(pipelineFile(args))
	if err != nil {
		return nil, nil, err
	}
	s := p.Step(o.step)
	if s == nil || s.Fleet == nil {
		return nil, nil, fmt.Errorf("the pipeline has no deploy step %q with a fleet: block", o.step)
	}
	data, err := os.ReadFile(o.inventory) //nolint:gosec // the person named their own inventory file
	if err != nil {
		return nil, nil, err
	}
	var in inventoryFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("%s: %w", o.inventory, err)
	}
	for _, h := range in.Hosts {
		if err := h.Validate(); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", o.inventory, err)
		}
	}
	return s.Fleet, in.Hosts, nil
}

// jsonLines appends JSON lines to a file the person named, if they named one.
type jsonLines struct{ f *os.File }

func openLines(path string) (*jsonLines, error) {
	if path == "" {
		return &jsonLines{}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // a file the person asked for
	return &jsonLines{f}, err
}

func (j *jsonLines) write(v any) {
	if j.f != nil {
		b, _ := json.Marshal(v)
		_, _ = j.f.Write(append(b, '\n'))
	}
}

func (j *jsonLines) close() {
	if j.f != nil {
		_ = j.f.Close()
	}
}

func cmdFleet(args []string) error {
	o, err := parseFleetFlags(args)
	if err != nil {
		return err
	}
	spec, hosts, err := loadFleetInput(o)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := localSecrets(o.secrets)
	if err != nil {
		return err
	}
	pemKey, err := res.Lookup(ctx, "", spec.SSH.Key)
	if err != nil {
		return fmt.Errorf("the deploy key %s could not be read: %w", spec.SSH.Key, err)
	}
	rem, err := fleet.NewSSH([]byte(pemKey))
	if err != nil {
		return err
	}
	audit, err := openLines(o.audit)
	if err != nil {
		return err
	}
	defer audit.close()
	events, err := openLines(o.events)
	if err != nil {
		return err
	}
	defer events.close()

	chk := &fleet.HTTPCheck{Check: *spec.Check}
	var traffic fleet.Traffic
	if spec.Traffic != nil {
		network, addr, _ := strings.Cut(spec.Traffic.Admin, ":")
		traffic = &fleet.HAProxy{Network: network, Address: addr}
	}
	layout, err := fleet.NewLayout(spec.Service)
	if spec.Method == "systemd" && err != nil {
		return err
	}
	f := &fleet.Fleet{Remote: &fleet.Audited{Remote: rem, RunID: o.release, Sink: func(e fleet.AuditEntry) { audit.write(e) }}, Traffic: traffic, Check: chk, Layout: layout,
		AllowNoTraffic: spec.AllowNoTraffic, CheckURL: func(h fleet.Host) string { return chk.URLFor(h.Service) },
		Events: func(e fleet.Event) {
			events.write(e)
			fmt.Printf("%s %-8s %-10s %s\n", e.Time.Format("15:04:05"), e.Host, e.Phase, e.Message)
		}}

	// What the environment allows, read before anything is changed.
	probe, err := (&fleet.StaticProvider{Hosts: hosts, Fleet: f}).Probe(ctx)
	if err != nil {
		return err
	}
	decision := fleet.Plan(probe, fleet.Want{Prefer: fleet.StrategyRolling, Hosts: len(hosts)})
	printReport(map[string]any{"probe": probe, "decision": decision})
	if err := decision.Err(); err != nil || o.verb == "probe" {
		return err
	}
	if o.release == "" {
		return errors.New("--release is required")
	}
	return runFleetPlan(ctx, o, spec, f, chk, hosts)
}

// runFleetPlan does the delivery the step's method and strategy say.
func runFleetPlan(ctx context.Context, o *fleetOpts, spec *pipeline.FleetSpec, f *fleet.Fleet, chk *fleet.HTTPCheck, hosts []fleet.Host) error {
	if spec.Method == "bootc" {
		ref := o.image
		if ref == "" && spec.Image != nil {
			ref = spec.Image.Literal
		}
		if ref == "" {
			return errors.New("the image is not known: give --image name@sha256:... (an output of an earlier step is not available to a Station run by hand)")
		}
		rep, err := f.BootcRoll(ctx, hosts, fleet.BootcSpec{Image: ref, MaxUnavailable: spec.MaxUnavailable, DrainMax: spec.Drain.D(), CheckWithin: 2 * time.Minute})
		printReport(rep)
		return err
	}
	art, err := readArtifact(o.workspace, spec.Artifact)
	if err != nil {
		return err
	}
	if spec.Strategy != "pathfinder" {
		rep, err := f.Roll(ctx, hosts, fleet.RollSpec{Release: o.release, Artifact: art, MaxUnavailable: spec.MaxUnavailable, DrainMax: spec.Drain.D()})
		printReport(rep)
		return err
	}
	pf := spec.Pathfinder
	var stable, fresh []fleet.Host
	for _, h := range hosts {
		switch h.Group {
		case pf.StableGroup:
			stable = append(stable, h)
		case pf.NewGroup:
			fresh = append(fresh, h)
		}
	}
	rep, err := f.Pathfinder(ctx, stable, fresh, fleet.PathfinderSpec{Release: o.release, Artifact: art, Steps: pf.Steps, Bake: pf.Bake.D(), Watch: pf.Watch.D(),
		DrainMax: spec.Drain.D(), Verify: func(ctx context.Context, _ int) error {
			return verifyThrough(ctx, chk, spec.Traffic.URL, max(pf.Bake.D(), 2*time.Second))
		}})
	printReport(rep)
	return err
}

func readArtifact(workspace, rel string) ([]byte, error) {
	path := filepath.Join(workspace, filepath.FromSlash(rel)) //nolint:gosec // checked to stay inside the workspace just below
	if r, err := filepath.Rel(workspace, path); err != nil || strings.HasPrefix(r, "..") {
		return nil, fmt.Errorf("the artifact %q is outside the workspace", rel)
	}
	return os.ReadFile(path) //nolint:gosec // inside the workspace, checked above
}

func printReport(rep any) {
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
}

// verifyThrough calls the check against the balancer's own address for the
// whole bake and fails if any call fails: what users would have seen.
func verifyThrough(ctx context.Context, chk *fleet.HTTPCheck, lb string, d time.Duration) error {
	end := time.Now().Add(d)
	url := strings.TrimRight(lb, "/") + chk.Check.Target0()
	var n, bad int
	var last error
	for time.Now().Before(end) {
		n++
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := chk.Healthy(cctx, url); err != nil {
			bad++
			last = err
		}
		cancel()
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d calls through %s failed (last: %v)", bad, n, lb, last)
	}
	return nil
}
