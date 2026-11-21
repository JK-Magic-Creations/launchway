package inspect

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func buildTree(t *testing.T, broken bool) *Tree {
	t.Helper()
	b := &Builder{R: Dynamic{Client: canaryFixture(broken)}, Red: NewRedactor(), Now: func() time.Time { return t0 }}
	return b.Build(context.Background(), Request{Application: "shop", Namespaces: []string{"shop"}, Resources: listed(), Since: t0.Add(-2 * time.Minute).UnixMilli()})
}

func node(t *testing.T, tr *Tree, id string) Node {
	t.Helper()
	for _, n := range tr.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %s in %d nodes", id, len(tr.Nodes))
	return Node{}
}

func TestTreeOwnershipRolesAndServices(t *testing.T) {
	tr := buildTree(t, false)
	if a, b := node(t, tr, "ReplicaSet/shop/shop-aaa"), node(t, tr, "ReplicaSet/shop/shop-bbb"); a.Role != RoleStable || b.Role != RoleCanary || a.Parent != "Rollout/shop/shop" {
		t.Fatalf("roles: %+v %+v", a, b)
	}
	if p := node(t, tr, "Pod/shop/shop-bbb-1"); p.Parent != "ReplicaSet/shop/shop-bbb" || p.Health != Healthy {
		t.Fatalf("pod: %+v", p)
	}
	st, ca := node(t, tr, "Service/shop/shop-stable"), node(t, tr, "Service/shop/shop-canary")
	if st.Role != RoleStable || len(st.Selects) != 1 || st.Selects[0] != "ReplicaSet/shop/shop-aaa" || ca.Role != RoleCanary || ca.Selects[0] != "ReplicaSet/shop/shop-bbb" {
		t.Fatalf("services: %+v %+v", st, ca)
	}
	// The push test: analysis run -> job -> pod.
	if j := node(t, tr, "Job/shop/shop-bbb-2-3-push"); j.Parent != "AnalysisRun/shop/shop-bbb-2-3" {
		t.Fatalf("job: %+v", j)
	}
	if p := node(t, tr, "Pod/shop/shop-bbb-2-3-push-x"); p.Parent != "Job/shop/shop-bbb-2-3-push" {
		t.Fatalf("test pod: %+v", p)
	}
	hr := node(t, tr, "HTTPRoute/shop/shop")
	if len(hr.Weights) != 2 || hr.Weights[1].Backend != "shop-canary" || hr.Weights[1].Weight != 20 {
		t.Fatalf("route: %+v", hr.Weights)
	}
	if cm := node(t, tr, "ConfigMap/shop/shop-config"); len(cm.UsedBy) != 1 || cm.UsedBy[0] != "Rollout/shop/shop" {
		t.Fatalf("used by: %+v", cm.UsedBy)
	}
}

func TestTreeScopeAndLeftovers(t *testing.T) {
	tr := buildTree(t, false)
	ids := map[string]Node{}
	for _, n := range tr.Nodes {
		ids[n.ID] = n
	}
	for _, bad := range []string{"ConfigMap/shop/someone-elses", "ConfigMap/other/elsewhere", "Namespace//shop"} {
		if _, ok := ids[bad]; ok {
			t.Errorf("%s is not this release's, or not in the Airfield's namespaces", bad)
		}
	}
	if n := ids["ConfigMap/shop/shop-old-config"]; n.Change != ChangeLeftover {
		t.Errorf("leftover: %+v", n)
	}
	if n := ids["MysteryKind/shop/x"]; !strings.Contains(n.Message, "not inspected") {
		t.Errorf("unknown kind: %+v", n)
	}
	if !strings.Contains(strings.Join(tr.Notes, "|"), "outside the namespaces") {
		t.Errorf("notes: %v", tr.Notes)
	}
	if n := ids["Rollout/shop/shop"]; n.Change != ChangeChanged {
		t.Errorf("rollout changed in this Launch: %+v", n)
	}
	if n := ids["ConfigMap/shop/shop-config"]; n.Change != ChangeUnchanged {
		t.Errorf("config: %+v", n)
	}
}

func TestNoSecretValueOrCanaryLeavesTheRunner(t *testing.T) {
	tr := buildTree(t, true)
	raw, _ := json.Marshal(tr)
	for _, s := range []string{canaryPassword, canarySecret, canaryToken, base64Of(canarySecret), "shopuser"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("the tree carries %q", s)
		}
	}
	sec := node(t, tr, "Secret/shop/shop-creds")
	if len(sec.Keys) != 2 || sec.Data != nil {
		t.Errorf("secret: %+v", sec)
	}
	cm := node(t, tr, "ConfigMap/shop/shop-config")
	if cm.Data["LOG_LEVEL"] != "info" || strings.Contains(cm.Data["plain"], canarySecret) {
		t.Errorf("configmap: %+v", cm.Data)
	}
}

func TestBrokenCanaryPodSaysWhy(t *testing.T) {
	tr := buildTree(t, true)
	p := node(t, tr, "Pod/shop/shop-bbb-1")
	if p.Health != Degraded || p.NotReady == nil || !strings.Contains(p.NotReady.Why, "CrashLoopBackOff") || !strings.Contains(p.NotReady.Why, "BackOff") {
		t.Fatalf("pod: %+v %+v", p, p.NotReady)
	}
	if len(p.Events) != 1 || strings.Contains(p.Events[0].Message, canaryPassword) {
		t.Fatalf("events: %+v", p.Events)
	}
	if !p.Containers[0].Previous || p.Containers[0].Restarts != 5 {
		t.Fatalf("containers: %+v", p.Containers)
	}
}

func TestRolloutView(t *testing.T) {
	tr := buildTree(t, false)
	v := tr.Rollout
	if v == nil || v.Strategy != "canary" || v.Current != 2 || v.DesiredWeight != 20 || v.ActualWeight != 20 || v.ActualSource != "traffic router" || !v.Paused {
		t.Fatalf("view: %+v", v)
	}
	if len(v.Steps) != 6 || v.Steps[1].State != "done" || v.Steps[2].State != "current" || v.Steps[3].State != "next" || v.Steps[5].Seconds != 300 {
		t.Fatalf("steps: %+v", v.Steps)
	}
	if v.PauseStarted == 0 || v.PauseEnds != 0 {
		t.Fatalf("pause: %+v", v)
	}
	if len(v.Analysis) != 1 || len(v.Analysis[0].Metrics) != 1 {
		t.Fatalf("analysis: %+v", v.Analysis)
	}
	m := v.Analysis[0].Metrics[0]
	if len(m.Points) != 2 || m.Points[1].Phase != "Failed" || m.Success != "result[0] >= 0.95" || !strings.Contains(m.Query, "http_requests_total") {
		t.Fatalf("metric: %+v", m)
	}
}
