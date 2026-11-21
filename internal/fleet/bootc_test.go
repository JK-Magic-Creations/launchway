package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

const (
	dOld = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	dNew = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	dBad = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

func TestImageDigestRequiresADigest(t *testing.T) {
	if d, err := ImageDigest("registry.example/os/web@" + dNew); err != nil || d != dNew {
		t.Fatalf("%q %v", d, err)
	}
	for _, bad := range []string{"registry.example/os/web:latest", "web@sha256:abc", "web@" + dNew + " ; reboot", ""} {
		if _, err := ImageDigest(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestParseBootcStatus(t *testing.T) {
	st, err := ParseBootcStatus(`{"spec":{"image":{"image":"r/os@` + dNew + `"}},"status":{"booted":{"image":{"imageDigest":"` + dOld + `"}},"staged":{"image":{"imageDigest":"` + dNew + `"}},"rollback":null}}`)
	if err != nil || st.Booted != dOld || st.Staged != dNew || st.Rollback != "" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := ParseBootcStatus(`{"status":{"booted":null}}`); err == nil {
		t.Fatal("a host with no booted image was accepted")
	}
}

// bootcWorld simulates hosts that run bootc.
type bootcWorld struct {
	mu                     sync.Mutex
	booted, staged, rollbk map[string]string
	boots                  map[string]int
	log                    []string
	machineID              map[string]string
	drained                map[string]bool
	order                  []string
}

func newBootcWorld(hs []Host) *bootcWorld {
	w := &bootcWorld{booted: map[string]string{}, staged: map[string]string{}, rollbk: map[string]string{}, boots: map[string]int{}, machineID: map[string]string{}, drained: map[string]bool{}}
	for _, h := range hs {
		w.booted[h.Name], w.machineID[h.Name] = dOld, "id-"+h.Name
	}
	return w
}

func (w *bootcWorld) Run(_ context.Context, h Host, cmd string, _ io.Reader) (Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log = append(w.log, h.Name+": "+cmd)
	switch {
	case cmd == "cat /etc/machine-id":
		return Result{Stdout: w.machineID[h.Name]}, nil
	case strings.Contains(cmd, "bootc status"):
		st := ""
		if s := w.staged[h.Name]; s != "" {
			st = `,"staged":{"image":{"imageDigest":"` + s + `"}}`
		}
		return Result{Stdout: fmt.Sprintf(`{"spec":{"image":{"image":"r/os"}},"status":{"booted":{"image":{"imageDigest":"%s"}}%s}}`, w.booted[h.Name], st)}, nil
	case strings.Contains(cmd, "bootc switch"):
		i := strings.Index(cmd, "@sha256:")
		if i < 0 {
			return Result{Stdout: "", Exit: 1, Stderr: "no digest"}, nil
		}
		w.order = append(w.order, h.Name+" stage")
		w.staged[h.Name] = cmd[i+1 : i+1+71]
		return Result{}, nil
	case strings.Contains(cmd, "bootc rollback"):
		w.order = append(w.order, h.Name+" rollback")
		w.staged[h.Name], w.rollbk[h.Name] = w.rollbk[h.Name], w.booted[h.Name]
		return Result{}, nil
	case strings.Contains(cmd, "systemctl reboot"):
		w.order = append(w.order, h.Name+" reboot")
		if s := w.staged[h.Name]; s != "" {
			w.rollbk[h.Name], w.booted[h.Name], w.staged[h.Name] = w.booted[h.Name], s, ""
		}
		w.boots[h.Name]++
		return Result{}, nil
	case strings.Contains(cmd, "boot_id"):
		return Result{Stdout: fmt.Sprint("boot-", w.boots[h.Name])}, nil
	}
	return Result{Exit: 127, Stderr: "unknown " + cmd}, nil
}

type bootcCheck struct{ w *bootcWorld }

func (c bootcCheck) Healthy(_ context.Context, url string) error {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	host := strings.TrimPrefix(url, "http://")
	if c.w.booted[host] == dBad {
		return errors.New("answered 500")
	}
	return nil
}

func bootcFleet(t *testing.T, n int) (*Fleet, *bootcWorld, []Host, *fakeWorld) {
	hs := testHosts(n)
	bw := newBootcWorld(hs)
	lw := newWorld(hs, "x")
	f, _, _ := newFleet(t, hs)
	f.Remote, f.Check, f.Traffic = bw, bootcCheck{bw}, &recTraffic{lw, bw}
	return f, bw, hs, lw
}

// recTraffic records drains and admits in the same order as host commands.
type recTraffic struct {
	*fakeWorld
	b *bootcWorld
}

func (r *recTraffic) Drain(ctx context.Context, m string) error {
	r.b.mu.Lock()
	r.b.order = append(r.b.order, strings.TrimPrefix(m, "app/")+" drain")
	r.b.mu.Unlock()
	return r.fakeWorld.Drain(ctx, m)
}
func (r *recTraffic) Admit(ctx context.Context, m string) error {
	r.b.mu.Lock()
	r.b.order = append(r.b.order, strings.TrimPrefix(m, "app/")+" admit")
	r.b.mu.Unlock()
	return r.fakeWorld.Admit(ctx, m)
}

func fastBootc(img string, mu int) BootcSpec {
	return BootcSpec{Image: img, MaxUnavailable: mu, DrainMax: 50e6, BootWithin: 3e9, PollEvery: 1e6, CheckEvery: 1e6, CheckWithin: 20e6}
}

func TestBootcRollStagesDrainsRebootsChecksAdmitsOneHostAtATime(t *testing.T) {
	f, w, hs, _ := bootcFleet(t, 3)
	rep, err := f.BootcRoll(context.Background(), hs, fastBootc("r/os@"+dNew, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Updated) != 3 || rep.Hosts["web-3"] != dNew {
		t.Fatalf("%+v", rep)
	}
	want := "web-1 stage|web-1 drain|web-1 reboot|web-1 admit|web-2 stage|web-2 drain|web-2 reboot|web-2 admit|web-3 stage|web-3 drain|web-3 reboot|web-3 admit"
	if got := strings.Join(w.order, "|"); got != want {
		t.Fatalf("order:\n%s\nwant:\n%s", got, want)
	}
}

func TestBootcFailedHealthRollsBackEveryTouchedHost(t *testing.T) {
	f, w, hs, lw := bootcFleet(t, 3)
	_, err := f.BootcRoll(context.Background(), hs, fastBootc("r/os@"+dNew, 1))
	if err != nil {
		t.Fatal(err)
	}
	// The next image is bad: the first host fails its check and is rolled back.
	rep, err := f.BootcRoll(context.Background(), hs, fastBootc("r/os@"+dBad, 1))
	if !errors.Is(err, ErrBootcReturned) {
		t.Fatalf("err = %v", err)
	}
	for _, h := range hs {
		if w.booted[h.Name] != dNew {
			t.Fatalf("%s booted %s after the return", h.Name, w.booted[h.Name])
		}
		if lw.state[h.Member] != "up" {
			t.Fatalf("%s is %s in the balancer", h.Name, lw.state[h.Member])
		}
	}
	if len(rep.Returned) != 1 || len(rep.Quarantined) != 0 || w.boots["web-2"] != 1 {
		t.Fatalf("%+v; web-2 was rebooted %d times (once for the first update, never for the bad image)", rep, w.boots["web-2"])
	}
}

func TestBootcRefusesTheStationsOwnMachineAndTags(t *testing.T) {
	f, w, hs, _ := bootcFleet(t, 2)
	own := localMachineID()
	if own == "" {
		t.Skip("this machine has no /etc/machine-id")
	}
	w.machineID["web-2"] = own
	if _, err := f.BootcRoll(context.Background(), hs, fastBootc("r/os@"+dNew, 1)); err == nil || !strings.Contains(err.Error(), "machine this Station runs on") {
		t.Fatalf("own machine: %v", err)
	}
	for _, c := range w.log {
		if strings.Contains(c, "bootc switch") || strings.Contains(c, "reboot") {
			t.Fatalf("a mutation ran before the refusal: %s", c)
		}
	}
	if _, err := f.BootcRoll(context.Background(), hs, fastBootc("r/os:latest", 1)); err == nil {
		t.Fatal("a tag was accepted")
	}
	_ = os.Getenv
}
