package config

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Pod Security Standards "restricted" profile
// (https://kubernetes.io/docs/concepts/security/pod-security-standards/), checked against every pod
// the two Helm charts render, for the example values the charts ship. A chart change that weakens
// a pod fails here, not in a customer's namespace that enforces the profile. Needs helm.

type chartCase struct {
	name   string
	chart  string
	values []string
	set    []string
}

var chartCases = []chartCase{
	{"server with only the address set", "launchway", nil, []string{"externalURL=https://launchway.example.com"}},
	{"server with an Ingress and a Gateway route", "launchway", nil, []string{"externalURL=https://launchway.example.com", "ingress.enabled=true", "httpRoute.enabled=true", "httpRoute.parentRefs[0].name=gw"}},
	{"server, high availability example", "launchway", []string{"launchway/examples/ha.yaml"}, nil},
	{"runner, remote with network policy", "launchway-runner", []string{"launchway-runner/examples/remote.yaml", "launchway-runner/examples/networkpolicy.yaml"}, nil},
	{"runner, deploy runner example", "launchway-runner", []string{"launchway-runner/examples/deploy-runner.yaml"}, nil},
}

func renderDocs(t *testing.T, c chartCase) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	args := []string{"template", "t", "../../deploy/helm/" + c.chart}
	for _, v := range c.values {
		args = append(args, "-f", "../../deploy/helm/"+v)
	}
	for _, s := range c.set {
		args = append(args, "--set", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
	return docs
}

func dig(m any, path ...string) any {
	for _, p := range path {
		mm, ok := m.(map[string]any)
		if !ok {
			return nil
		}
		m = mm[p]
	}
	return m
}

func podSpecOf(doc map[string]any) map[string]any {
	var spec any
	switch doc["kind"] {
	case "Pod":
		spec = dig(doc, "spec")
	case "CronJob":
		spec = dig(doc, "spec", "jobTemplate", "spec", "template", "spec")
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		spec = dig(doc, "spec", "template", "spec")
	}
	m, _ := spec.(map[string]any)
	return m
}

// restrictedViolations lists what a pod spec does that the "restricted" profile forbids.
func restrictedViolations(pod map[string]any) []string {
	var bad []string
	for _, f := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if pod[f] == true {
			bad = append(bad, f+" is true")
		}
	}
	if vols, ok := pod["volumes"].([]any); ok {
		allowed := map[string]bool{"name": true, "configMap": true, "csi": true, "downwardAPI": true, "emptyDir": true, "ephemeral": true,
			"persistentVolumeClaim": true, "projected": true, "secret": true}
		for _, v := range vols {
			for k := range v.(map[string]any) {
				if !allowed[k] {
					bad = append(bad, "volume type "+k+" is not allowed")
				}
			}
		}
	}
	podSC, _ := pod["securityContext"].(map[string]any)
	seccomp := func(sc map[string]any) string {
		t, _ := dig(sc, "seccompProfile", "type").(string)
		return t
	}
	var all []any
	for _, k := range []string{"initContainers", "containers", "ephemeralContainers"} {
		if l, ok := pod[k].([]any); ok {
			all = append(all, l...)
		}
	}
	if len(all) == 0 {
		bad = append(bad, "no containers found (the test does not understand this pod)")
	}
	for _, c := range all {
		cm := c.(map[string]any)
		name := fmt.Sprint(cm["name"])
		sc, _ := cm["securityContext"].(map[string]any)
		if sc == nil {
			sc = map[string]any{}
		}
		if sc["privileged"] == true {
			bad = append(bad, name+": privileged")
		}
		if sc["allowPrivilegeEscalation"] != false {
			bad = append(bad, name+": allowPrivilegeEscalation is not false")
		}
		if sc["runAsNonRoot"] != true && podSC["runAsNonRoot"] != true {
			bad = append(bad, name+": runAsNonRoot is not true")
		}
		for _, sctx := range []map[string]any{sc, podSC} {
			if u, ok := sctx["runAsUser"].(int); ok && u == 0 {
				bad = append(bad, name+": runs as user 0")
			}
		}
		if t := seccomp(sc); t != "RuntimeDefault" && t != "Localhost" && seccomp(podSC) != "RuntimeDefault" && seccomp(podSC) != "Localhost" {
			bad = append(bad, name+": no RuntimeDefault or Localhost seccomp profile")
		}
		drop, _ := dig(sc, "capabilities", "drop").([]any)
		if len(drop) != 1 || fmt.Sprint(drop[0]) != "ALL" {
			bad = append(bad, name+": capabilities.drop is not [ALL]")
		}
		add, _ := dig(sc, "capabilities", "add").([]any)
		for _, a := range add {
			if fmt.Sprint(a) != "NET_BIND_SERVICE" {
				bad = append(bad, name+": adds capability "+fmt.Sprint(a))
			}
		}
		if ports, ok := cm["ports"].([]any); ok {
			for _, p := range ports {
				if _, has := p.(map[string]any)["hostPort"]; has {
					bad = append(bad, name+": uses a hostPort")
				}
			}
		}
	}
	return bad
}

func TestChartsRenderPodsThatMeetRestricted(t *testing.T) {
	for _, c := range chartCases {
		t.Run(c.name, func(t *testing.T) {
			pods := 0
			for _, d := range renderDocs(t, c) {
				spec := podSpecOf(d)
				if spec == nil {
					continue
				}
				pods++
				name := fmt.Sprintf("%v/%v", d["kind"], dig(d, "metadata", "name"))
				for _, v := range restrictedViolations(spec) {
					t.Errorf("%s: %s", name, v)
				}
				if spec["automountServiceAccountToken"] != false && dig(d, "kind") != nil {
					t.Logf("%s: the service account token is mounted (allowed only where the pod talks to the Kubernetes API)", name)
				}
			}
			if pods == 0 {
				t.Fatal("rendered no pods; the test is not looking at anything")
			}
		})
	}
}

// The checker itself: a weak pod is caught, a strong one passes.
func TestRestrictedCheckerCatchesWeakPods(t *testing.T) {
	weak := map[string]any{"hostNetwork": true, "containers": []any{map[string]any{"name": "c"}}}
	if got := restrictedViolations(weak); len(got) < 4 {
		t.Fatalf("a weak pod produced only %v", got)
	}
	strong := map[string]any{
		"securityContext": map[string]any{"runAsNonRoot": true, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"containers": []any{map[string]any{"name": "c", "securityContext": map[string]any{
			"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}}},
	}
	if got := restrictedViolations(strong); len(got) != 0 {
		t.Fatalf("a restricted pod was refused: %v", got)
	}
}

// Nothing a chart renders may carry a literal password, token or key: values come from Secrets.
func TestChartsRenderNoSecretValuesInConfigMaps(t *testing.T) {
	for _, c := range chartCases {
		t.Run(c.name, func(t *testing.T) {
			for _, d := range renderDocs(t, c) {
				if d["kind"] != "ConfigMap" {
					continue
				}
				data, _ := d["data"].(map[string]any)
				for k, v := range data {
					for _, line := range strings.Split(fmt.Sprint(v), "\n") {
						l := strings.ToLower(strings.TrimSpace(line))
						for _, w := range []string{"password:", "token:", "client_secret:", "private_key:"} {
							if strings.HasPrefix(l, w) && strings.TrimSpace(strings.TrimPrefix(l, w)) != "" {
								t.Errorf("ConfigMap %v key %s has a literal %s", dig(d, "metadata", "name"), k, w)
							}
						}
					}
				}
			}
		})
	}
}
