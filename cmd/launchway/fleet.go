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

func cmdFleet(args []string) error {
	if len(args) == 0 || (args[0] != "run" && args[0] != "probe") {
		fmt.Fprint(os.Stderr, fleetUsage)
		return exitErr(2)
	}
	verb := args[0]
	fs := flag.NewFlagSet("fleet "+verb, flag.ExitOnError)
	file := fs.String("file", "", "pipeline file (default .launchway/pipeline.yaml)")
	step := fs.String("step", "", "the deploy step with a fleet: block")
	inv := fs.String("inventory", "", "the inventory file of the Airfield's hosts")
	release := fs.String("release", "", "the release id (run)")
	image := fs.String("image", "", "the image by digest, name@sha256:..., for method bootc")
	workspace := fs.String("workspace", ".", "the directory the artifact path is relative to")
	auditFile := fs.String("audit", "", "write every command sent to a host here, one JSON line each")
	eventsFile := fs.String("events", "", "write the progress events here, one JSON line each")
	secretsConfig := secretsFlag(fs)
	_ = fs.Parse(args[1:])
	if *step == "" || *inv == "" {
		fmt.Fprint(os.Stderr, fleetUsage)
		return exitErr(2)
	}
	p, err := load(pipelineFile([]string{*file}[:boolInt(*file != "")]))
	if err != nil {
		return err
	}
	s := p.Step(*step)
	if s == nil || s.Fleet == nil {
		return fmt.Errorf("%s has no deploy step %q with a fleet: block", *file, *step)
	}
	spec := s.Fleet
	data, err := os.ReadFile(*inv)
	if err != nil {
		return err
	}
	var in inventoryFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", *inv, err)
	}
	for _, h := range in.Hosts {
		if err := h.Validate(); err != nil {
			return fmt.Errorf("%s: %w", *inv, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := localSecrets(*secretsConfig)
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
	var auditOut, eventsOut *os.File
	if *auditFile != "" {
		if auditOut, err = os.OpenFile(*auditFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
			return err
		}
		defer auditOut.Close()
	}
	if *eventsFile != "" {
		if eventsOut, err = os.OpenFile(*eventsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
			return err
		}
		defer eventsOut.Close()
	}
	line := func(f *os.File, v any) {
		if f != nil {
			b, _ := json.Marshal(v)
			_, _ = f.Write(append(b, '\n'))
		}
	}
	runID := *release
	audited := &fleet.Audited{Remote: rem, RunID: runID, Sink: func(e fleet.AuditEntry) { line(auditOut, e) }}

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
	f := &fleet.Fleet{Remote: audited, Traffic: traffic, Check: chk, Layout: layout, AllowNoTraffic: spec.AllowNoTraffic,
		CheckURL: func(h fleet.Host) string { return chk.URLFor(h.Service) },
		Events: func(e fleet.Event) {
			line(eventsOut, e)
			fmt.Printf("%s %-8s %-10s %s\n", e.Time.Format("15:04:05"), e.Host, e.Phase, e.Message)
		}}

	// What the environment allows, read before anything is changed.
	var prov fleet.Provider = &fleet.StaticProvider{Hosts: in.Hosts, Fleet: f}
	probe, err := prov.Probe(ctx)
	if err != nil {
		return err
	}
	dec2 := fleet.Plan(probe, fleet.Want{Prefer: fleet.StrategyRolling, Hosts: len(in.Hosts)})
	b, _ := json.MarshalIndent(map[string]any{"probe": probe, "decision": dec2}, "", "  ")
	fmt.Println(string(b))
	if err := dec2.Err(); err != nil {
		return err
	}
	if verb == "probe" {
		return nil
	}
	if *release == "" {
		return errors.New("--release is required")
	}

	switch {
	case spec.Method == "bootc":
		ref := *image
		if ref == "" && spec.Image != nil {
			ref = spec.Image.Literal
		}
		if ref == "" {
			return errors.New("the image is not known: give --image name@sha256:... (an output of an earlier step is not available to a Station run by hand)")
		}
		rep, err := f.BootcRoll(ctx, in.Hosts, fleet.BootcSpec{Image: ref, MaxUnavailable: spec.MaxUnavailable, DrainMax: spec.Drain.D(),
			CheckWithin: 2 * time.Minute})
		printReport(rep)
		return err
	case spec.Strategy == "pathfinder":
		pf := spec.Pathfinder
		art, err := readArtifact(*workspace, spec.Artifact)
		if err != nil {
			return err
		}
		var stable, fresh []fleet.Host
		for _, h := range in.Hosts {
			switch h.Group {
			case pf.StableGroup:
				stable = append(stable, h)
			case pf.NewGroup:
				fresh = append(fresh, h)
			}
		}
		rep, err := f.Pathfinder(ctx, stable, fresh, fleet.PathfinderSpec{Release: *release, Artifact: art, Steps: pf.Steps, Bake: pf.Bake.D(), Watch: pf.Watch.D(),
			DrainMax: spec.Drain.D(), Verify: func(ctx context.Context, percent int) error {
				return verifyThrough(ctx, chk, spec.Traffic.URL, max(pf.Bake.D(), 2*time.Second))
			}})
		printReport(rep)
		return err
	default:
		art, err := readArtifact(*workspace, spec.Artifact)
		if err != nil {
			return err
		}
		rep, err := f.Roll(ctx, in.Hosts, fleet.RollSpec{Release: *release, Artifact: art, MaxUnavailable: spec.MaxUnavailable, DrainMax: spec.Drain.D()})
		printReport(rep)
		return err
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func readArtifact(workspace, rel string) ([]byte, error) {
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	if r, err := filepath.Rel(workspace, path); err != nil || strings.HasPrefix(r, "..") {
		return nil, fmt.Errorf("the artifact %q is outside the workspace", rel)
	}
	return os.ReadFile(path)
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
