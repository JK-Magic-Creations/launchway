package inspect

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeLogs struct{ lines map[string][]string }

func (f fakeLogs) Open(_ context.Context, ns, pod, c string, o LogOptions) (io.ReadCloser, error) {
	ls, ok := f.lines[pod]
	if !ok {
		return nil, fmt.Errorf("pods %q is forbidden", pod)
	}
	return io.NopCloser(strings.NewReader(strings.Join(ls, "\n") + "\n")), nil
}

func ln(sec int, s string) string { return t0.Add(time.Duration(sec)*time.Second).Format(time.RFC3339Nano) + " " + s }

func TestLogsAreRedactedMergedAndTagged(t *testing.T) {
	tr := buildTree(t, false)
	lg := &Logger{Red: NewRedactor(canarySecret), Src: fakeLogs{lines: map[string][]string{
		"shop-aaa-1": {ln(1, "stable request ok"), ln(5, "stable password=" + canaryPassword)},
		"shop-aaa-2": {ln(3, "x")}, "shop-aaa-3": {ln(4, "y")},
		"shop-bbb-1": {ln(2, "canary booted"), ln(6, "using token " + canaryToken + " and " + canarySecret)},
	}}}
	targets, _, err := tr.Targets(LogSpec{Role: RoleCanary})
	if err != nil || len(targets) != 1 || targets[0].Source != "canary/shop-bbb-1" {
		t.Fatalf("canary targets: %+v %v", targets, err)
	}
	both := append(mustTargets(t, tr, LogSpec{Role: RoleStable}), targets...)
	var got []LogLine
	_, err = lg.Read(context.Background(), both, LogOptions{TailLines: 100}, func(l []LogLine) bool { got = append(got, l...); return true })
	if err != nil || len(got) != 6 {
		t.Fatalf("lines: %v %+v", err, got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].T < got[i-1].T {
			t.Fatalf("not merged by time: %+v", got)
		}
	}
	all := fmt.Sprint(got)
	for _, s := range []string{canaryPassword, canaryToken, canarySecret} {
		if strings.Contains(all, s) {
			t.Errorf("log carries %q: %s", s, all)
		}
	}
	if got[0].Src != "stable/shop-aaa-1" || got[1].Src != "canary/shop-bbb-1" {
		t.Errorf("sources: %+v", got[:2])
	}
}

func mustTargets(t *testing.T, tr *Tree, s LogSpec) []LogTarget {
	t.Helper()
	x, _, err := tr.Targets(s)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestLogsOnlyOfPodsInTheRelease(t *testing.T) {
	tr := buildTree(t, false)
	if _, _, err := tr.Targets(LogSpec{Pod: "someone-elses-pod"}); err == nil {
		t.Fatal("a pod outside the tree was resolved")
	}
	if _, _, err := tr.Targets(LogSpec{Pod: "shop-bbb-1", Container: "nope"}); err == nil {
		t.Fatal("an unknown container was resolved")
	}
	lg := &Logger{Src: fakeLogs{}}
	_, err := lg.Read(context.Background(), mustTargets(t, tr, LogSpec{Pod: "shop-bbb-1"}), LogOptions{}, func([]LogLine) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "rbac.inspect.logs") {
		t.Fatalf("forbidden should name the chart value: %v", err)
	}
}

func TestFollowStopsWhenTheReaderStops(t *testing.T) {
	tr := buildTree(t, false)
	lg := &Logger{Src: fakeLogs{lines: map[string][]string{"shop-bbb-1": {ln(1, "a"), ln(2, "b")}}}}
	n := 0
	_, err := lg.Read(context.Background(), mustTargets(t, tr, LogSpec{Pod: "shop-bbb-1"}), LogOptions{Follow: true}, func(l []LogLine) bool { n += len(l); return false })
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestControlActionsAndRefusals(t *testing.T) {
	fc := canaryFixture(false)
	d := Dynamic{Client: fc}
	b := &Builder{R: d, Red: NewRedactor(), Now: func() time.Time { return t0 }}
	tr := b.Build(context.Background(), Request{Application: "shop", Namespaces: []string{"shop"}, Resources: listed()})
	ro := Ref{Kind: "Rollout", Namespace: "shop", Name: "shop"}
	ctx := context.Background()
	if r, d := b.Apply(ctx, d, tr, Action{Action: ActPause, Target: ro}); r != Applied {
		t.Fatalf("pause: %s %s", r, d)
	}
	if r, _ := b.Apply(ctx, d, tr, Action{Action: ActPause, Target: ro}); r != AlreadyApplied {
		t.Fatalf("pause twice: %s", r)
	}
	if r, _ := b.Apply(ctx, d, tr, Action{Action: ActResume, Target: ro}); r != Applied {
		t.Fatalf("resume: %s", r)
	}
	if r, _ := b.Apply(ctx, d, tr, Action{Action: ActRestart, Target: ro}); r != Applied {
		t.Fatalf("restart: %s", r)
	}
	// Refusals: outside the release, scaling off, the autoscaler, out of bounds, wrong kind.
	if r, _ := b.Apply(ctx, d, tr, Action{Action: ActRestart, Target: Ref{Kind: "Rollout", Namespace: "other", Name: "x"}}); r != NotApplied {
		t.Fatalf("outside: %s", r)
	}
	if r, why := b.Apply(ctx, d, tr, Action{Action: ActScale, Target: ro, Replicas: 5}); r != NotApplied || !strings.Contains(why, "scale_max") {
		t.Fatalf("scaling off: %s %s", r, why)
	}
	if r, why := b.Apply(ctx, d, tr, Action{Action: ActScale, Target: ro, Replicas: 5, ScaleMax: 10}); r != NotApplied || !strings.Contains(why, "autoscaler") {
		t.Fatalf("hpa: %s %s", r, why)
	}
	if r, _ := b.Apply(ctx, d, tr, Action{Action: ActRestart, Target: Ref{Kind: "Service", Namespace: "shop", Name: "shop-stable"}}); r != NotApplied {
		t.Fatalf("a Service: %s", r)
	}
	for _, a := range Actions(tr, 0) {
		if a.Action == ActScale && (a.OK || a.Why == "") {
			t.Fatalf("scale should be refused with a reason: %+v", a)
		}
	}
}

func TestManifestIsRedactedAndScoped(t *testing.T) {
	b := &Builder{R: Dynamic{Client: canaryFixture(false)}, Red: NewRedactor(), Now: func() time.Time { return t0 }}
	tr := b.Build(context.Background(), Request{Application: "shop", Namespaces: []string{"shop"}, Resources: listed()})
	y, err := b.Manifest(context.Background(), tr, "Rollout/shop/shop")
	if err != nil || strings.Contains(y, canaryPassword) || !strings.Contains(y, "DB_PASSWORD") || strings.Contains(y, "managedFields") {
		t.Fatalf("rollout manifest: %v\n%s", err, y)
	}
	y, err = b.Manifest(context.Background(), tr, "Secret/shop/shop-creds")
	if err != nil || strings.Contains(y, base64Of(canarySecret)) || !strings.Contains(y, "hidden") {
		t.Fatalf("secret manifest: %v\n%s", err, y)
	}
	y, _ = b.Manifest(context.Background(), tr, "ConfigMap/shop/shop-config")
	if strings.Contains(y, canarySecret) || strings.Contains(y, canaryToken) || strings.Contains(y, canaryPassword) {
		t.Fatalf("configmap manifest:\n%s", y)
	}
	if _, err := b.Manifest(context.Background(), tr, "ConfigMap/shop/someone-elses"); err == nil {
		t.Fatal("an object outside the release was read")
	}
}
