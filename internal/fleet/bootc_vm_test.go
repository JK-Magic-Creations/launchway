package fleet

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// TestBootcInAVirtualMachine runs the image rollout against a real virtual
// machine booted from a Fedora CoreOS disk under qemu/KVM: switch to an image
// by digest, reboot, check, then to a bad image, which fails its check and
// is rolled back with `bootc rollback`. Prepare the lab with
// testdata/bootc/lab.sh up and export what it prints; it does not run
// otherwise. The only host it ever changes is that virtual machine.
func TestBootcInAVirtualMachine(t *testing.T) {
	addr, key, httpAddr := os.Getenv("LW_BOOTC_SSH"), os.Getenv("LW_BOOTC_KEY"), os.Getenv("LW_BOOTC_HTTP")
	local, guest := os.Getenv("LW_BOOTC_REGISTRY_LOCAL"), os.Getenv("LW_BOOTC_REGISTRY_GUEST")
	if addr == "" || key == "" || local == "" {
		t.Skip("no virtual machine lab: run testdata/bootc/lab.sh up and export its variables (needs /dev/kvm, qemu, podman)")
	}
	pem, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	rem, err := NewSSH(pem)
	if err != nil {
		t.Fatal(err)
	}
	// Pin the VM's key (read from the VM itself, over the forwarded port).
	scan, err := exec.Command("ssh-keyscan", "-p", strings.Split(addr, ":")[1], "-t", "ed25519", strings.Split(addr, ":")[0]).Output()
	if err != nil || len(strings.Fields(string(scan))) < 3 {
		t.Fatalf("the VM's host key could not be read: %v %s", err, scan)
	}
	f3 := strings.Fields(string(scan))
	h := Host{Name: "vm-a", Address: addr, User: "core", HostKey: f3[len(f3)-2] + " " + f3[len(f3)-1], Service: httpAddr}
	chk := &HTTPCheck{}
	chk.Check.URL = "http://" + pipeline.FleetHostName + "/healthz"
	var events []string
	f := &Fleet{Remote: rem, Check: chk, AllowNoTraffic: true, Layout: Layout{}, CheckURL: func(h Host) string { return chk.URLFor(h.Service) },
		Events: func(e Event) { events = append(events, e.Phase+": "+e.Message); t.Logf("%-8s %s", e.Phase, e.Message) }}
	ctx := context.Background()

	// The guest must be the VM, not this machine: Fleet refuses by machine id,
	// and here we also assert the names differ.
	out, err := f.run(ctx, h, "hostname", nil)
	if err != nil || strings.TrimSpace(out) != "lw-vmtest-a" {
		t.Fatalf("the target is %q (%v), not the throwaway VM", out, err)
	}
	if mine, _ := os.Hostname(); mine == strings.TrimSpace(out) {
		t.Fatal("the target has this machine's host name")
	}

	digest := func(tag string) string {
		req, _ := http.NewRequest(http.MethodHead, "http://"+local+"/v2/lw/os/manifests/"+tag, nil)
		req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		d := resp.Header.Get("Docker-Content-Digest")
		if d == "" {
			t.Fatalf("the registry has no %s", tag)
		}
		return d
	}
	ref := func(tag string) string { return guest + "/lw/os@" + digest(tag) }

	// A load on the single VM, to say what a reboot costs it (not asserted:
	// a host that reboots is unavailable; fleets use batches and drain).
	var sent, failed atomic.Int64
	stop := make(chan struct{})
	go func() {
		cl := &http.Client{Timeout: time.Second}
		for {
			select {
			case <-stop:
				return
			default:
			}
			sent.Add(1)
			resp, err := cl.Get("http://" + httpAddr + "/")
			if err != nil || resp.StatusCode != 200 {
				failed.Add(1)
			}
			if resp != nil {
				resp.Body.Close()
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	defer func() { close(stop) }()

	before, err := f.BootcStatus(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("start: booted %s following %s", before.Booted, before.Image)

	spec := BootcSpec{MaxUnavailable: 1, BootWithin: 5 * time.Minute, PollEvery: 3 * time.Second, CheckWithin: 90 * time.Second}
	spec.Image = ref("b1")
	rep, err := f.BootcRoll(ctx, []Host{h}, spec)
	if err != nil {
		t.Fatalf("b1: %v", err)
	}
	b1, _ := ImageDigest(spec.Image)
	st, _ := f.BootcStatus(ctx, h)
	t.Logf("after b1: booted %s (want %s) rollback slot %s", st.Booted, b1, st.Rollback)
	if st.Booted != b1 || rep.Hosts[h.Name] != b1 {
		t.Fatalf("booted %s, want %s", st.Booted, b1)
	}

	spec.Image = ref("b2")
	if _, err = f.BootcRoll(ctx, []Host{h}, spec); err != nil {
		t.Fatalf("b2: %v", err)
	}
	b2, _ := ImageDigest(spec.Image)
	if st, _ = f.BootcStatus(ctx, h); st.Booted != b2 {
		t.Fatalf("booted %s, want %s", st.Booted, b2)
	}

	// The bad image boots but fails its health check: it must come back to b2.
	spec.Image, spec.CheckWithin = ref("b3"), 15*time.Second
	rep, err = f.BootcRoll(ctx, []Host{h}, spec)
	if err == nil || !strings.Contains(err.Error(), ErrBootcReturned.Error()) {
		t.Fatalf("a bad image was accepted: %v", err)
	}
	st, _ = f.BootcStatus(ctx, h)
	t.Logf("after the bad image: booted %s (want %s), returned %v quarantined %v", st.Booted, b2, rep.Returned, rep.Quarantined)
	if st.Booted != b2 {
		t.Fatalf("not returned: booted %s", st.Booted)
	}
	if err := chk.Healthy(ctx, chk.URLFor(h.Service)); err != nil {
		t.Fatalf("not healthy after the return: %v", err)
	}
	t.Logf("MEASURED single VM through 3 reboots (no balancer, no drain possible with one host): sent %d, failed %d", sent.Load(), failed.Load())
	_ = fmt.Sprint
}
