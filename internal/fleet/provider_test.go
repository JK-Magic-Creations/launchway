package fleet

import (
	"context"
	"strings"
	"testing"
)

func TestPlanPicksWhatTheEnvironmentSupportsAndRefusesExactly(t *testing.T) {
	ok := &Probe{KVM: true, Capacity: Capacity{VCPUs: 8, MemoryMB: 8192, DiskMB: 50000}}
	d := Plan(ok, Want{Prefer: StrategyBlueGreen, Hosts: 4, VCPUs: 1, MemoryMB: 1024, DiskMB: 6000})
	if d.Strategy != StrategyBlueGreen || d.Err() != nil {
		t.Fatalf("%+v", d)
	}
	// A second fleet of 4 needs 4096 MB; only 2048 is free: rolling, and why.
	tight := &Probe{KVM: true, Capacity: Capacity{VCPUs: 8, MemoryMB: 2048, DiskMB: 50000}}
	d = Plan(tight, Want{Prefer: StrategyBlueGreen, Hosts: 4, VCPUs: 1, MemoryMB: 1024})
	if d.Strategy != StrategyRolling || !strings.Contains(strings.Join(d.Why, " "), "4096 MB of memory (2048 MB available)") {
		t.Fatalf("%+v", d)
	}
	// Missing permission and no KVM: refused before anything, naming both.
	bad := &Probe{Permissions: []Permission{{Action: "elasticloadbalancing:RegisterTargets", Detail: "the Station's role lacks it"}}}
	d = Plan(bad, Want{Prefer: StrategyRolling, NeedKVM: true})
	if d.Strategy != "" || d.Err() == nil || !strings.Contains(d.Err().Error(), "elasticloadbalancing:RegisterTargets") || !strings.Contains(d.Err().Error(), "/dev/kvm") {
		t.Fatalf("%+v", d)
	}
	off := false
	d = Plan(&Probe{Internet: &off}, Want{NeedInternet: true})
	if d.Err() == nil || !strings.Contains(d.Err().Error(), "Depot") {
		t.Fatalf("%+v", d)
	}
}

func TestLocalHypervisorProbeReadsThisMachineAndRefusesTheSystemConnection(t *testing.T) {
	l := &LocalHypervisor{Dir: t.TempDir()}
	p, err := l.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Capacity.MemoryMB <= 0 || p.Capacity.VCPUs <= 0 || p.Capacity.DiskMB <= 0 {
		t.Fatalf("%+v", p.Capacity)
	}
	t.Logf("probe: kvm=%v capacity=%+v permissions=%+v", p.KVM, p.Capacity, p.Permissions)
	if _, err := (&LocalHypervisor{Connect: "qemu:///system"}).Probe(context.Background()); err == nil {
		t.Fatal("the system connection was accepted")
	}
}

func TestStaticProviderBehindTheInterface(t *testing.T) {
	var _ Provider = (*StaticProvider)(nil)
	var _ Provider = (*LocalHypervisor)(nil)
	hs := testHosts(2)
	hs[1].HostKey = ""
	if _, err := (&StaticProvider{Hosts: hs}).Inventory(context.Background()); err == nil {
		t.Fatal("an unpinned host was listed")
	}
}
