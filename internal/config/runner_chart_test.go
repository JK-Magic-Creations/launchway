package config

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The launchway chart (servers and runners) and the launchway-runner chart
// (runners only) render their runners from the same templates, copied from
// deploy/helm/shared. These tests keep the copies and what they render the
// same, so that a runner installed with either chart gets the same rights.

const (
	mainChart   = "../../deploy/helm/launchway"
	runnerChart = "../../deploy/helm/launchway-runner"
)

func helmTemplate(t *testing.T, chart, namespace, values string) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	args := []string{"template", "t", chart, "-n", namespace, "-f", writeTemp(t, values)}
	if chart == mainChart {
		args = append(args, "--set", "externalURL=https://launchway.example.com")
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %s: %v\n%s", chart, err, out)
	}
	var docs []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

// TestChartsShareRunnerTemplates checks that each chart's copy of a shared
// template is the source in deploy/helm/shared (make charts-sync).
func TestChartsShareRunnerTemplates(t *testing.T) {
	sources, _ := filepath.Glob("../../deploy/helm/shared/_*.tpl")
	if len(sources) == 0 {
		t.Fatal("no shared templates")
	}
	for _, src := range sources {
		want, _ := os.ReadFile(src)
		for _, chart := range []string{mainChart, runnerChart} {
			dst := filepath.Join(chart, "templates", "_shared-"+strings.TrimPrefix(filepath.Base(src), "_"))
			got, err := os.ReadFile(dst)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s is not a copy of %s (err %v): run make charts-sync", dst, src, err)
			}
		}
	}
}

// access is what a chart grants and creates for one component, without names
// and labels: the step namespace's labels, and each Role's namespace and rules.
type access struct {
	NamespaceLabels map[string]any
	Roles           map[string]any // namespace -> rules
	Bindings        map[string]any // namespace -> subjects' namespaces
	Policies        []any          // admission policies' specs
}

func accessOf(docs []map[string]any, component string) access {
	a := access{Roles: map[string]any{}, Bindings: map[string]any{}}
	for _, d := range docs {
		md, _ := d["metadata"].(map[string]any)
		labels, _ := md["labels"].(map[string]any)
		if labels["app.kubernetes.io/component"] != component {
			continue
		}
		ns, _ := md["namespace"].(string)
		switch d["kind"] {
		case "Namespace":
			l := map[string]any{}
			for k, v := range labels {
				if strings.HasPrefix(k, "pod-security.kubernetes.io/") {
					l[k] = v
				}
			}
			a.NamespaceLabels = l
		case "Role":
			a.Roles[ns] = d["rules"]
		case "RoleBinding":
			var subjects []any
			for _, s := range d["subjects"].([]any) {
				s := s.(map[string]any)
				subjects = append(subjects, []any{s["kind"], s["namespace"]})
			}
			a.Bindings[ns] = []any{d["roleRef"].(map[string]any)["kind"], subjects}
		case "ValidatingAdmissionPolicy":
			spec := d["spec"].(map[string]any)
			// The condition names the ServiceAccount, whose name differs.
			a.Policies = append(a.Policies, []any{spec["matchConstraints"], spec["validations"]})
		}
	}
	return a
}

func TestRunnerChartRendersTheSameRBAC(t *testing.T) {
	main := helmTemplate(t, mainChart, "lw", `postgresql: { enabled: true }
runner:
  labels: [remote]
  accept: { projects: [shop] }
  kubernetes: { namespace: lw-steps, createNamespace: true }
delivery:
  environments: [ { name: prod, target: cluster-a, projects: [shop] } ]
  verification:
    sources: [ { name: metrics, type: prometheus, url: http://metrics.monitoring.svc, projects: [shop] } ]
  deployRunner:
    enabled: true
    target: cluster-a
    applications: [ { projects: [shop], names: [shop, shop-api] } ]
    rolloutNamespaces: [shop]
    repos: { gitops: { url: http://git.example/gitops.git, paths: [ { projects: [shop], files: ["apps/**"] } ] } }
    rbac: { controlRollouts: { shop: [shop] }, readDeployments: true }
`)
	runners := helmTemplate(t, runnerChart, "lw", `server: { url: "http://t-launchway.lw.svc:8080" }
token: { existingSecret: t-launchway-runner-token }
runner:
  labels: [remote]
  accept: { projects: [shop] }
  kubernetes: { namespace: lw-steps, createNamespace: true }
verification:
  sources: [ { name: metrics, type: prometheus, url: http://metrics.monitoring.svc, projects: [shop] } ]
deployRunner:
  enabled: true
  target: cluster-a
  token: { existingSecret: pool }
  applications: [ { projects: [shop], names: [shop, shop-api] } ]
  rolloutNamespaces: [shop]
  repos: { gitops: { url: http://git.example/gitops.git, paths: [ { projects: [shop], files: ["apps/**"] } ] } }
  rbac: { controlRollouts: { shop: [shop] }, readDeployments: true }
`)
	for _, component := range []string{"runner", "deploy-runner"} {
		m, r := accessOf(main, component), accessOf(runners, component)
		if len(m.Roles) == 0 {
			t.Fatalf("%s: the main chart rendered no Role", component)
		}
		if !reflect.DeepEqual(m, r) {
			mj, _ := json.MarshalIndent(m, "", " ")
			rj, _ := json.MarshalIndent(r, "", " ")
			t.Errorf("%s: the charts grant different rights:\nlaunchway:\n%s\nlaunchway-runner:\n%s", component, mj, rj)
		}
	}
	if l := accessOf(runners, "runner").NamespaceLabels; l["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Errorf("the step namespace is not restricted: %v", l)
	}
	// The runners' files are the same too, but for the server URL.
	files := func(docs []map[string]any) map[string]string {
		f := map[string]string{}
		for _, d := range docs {
			if d["kind"] == "ConfigMap" {
				labels := d["metadata"].(map[string]any)["labels"].(map[string]any)
				if c, _ := labels["app.kubernetes.io/component"].(string); c != "server" {
					f[c] = d["data"].(map[string]any)["runner.yaml"].(string)
				}
			}
		}
		return f
	}
	strip := func(s string) string {
		var lines []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "#") {
				lines = append(lines, l)
			}
		}
		return strings.Join(lines, "\n")
	}
	mf, rf := files(main), files(runners)
	for _, c := range []string{"runner", "deploy-runner"} {
		if strip(mf[c]) != strip(rf[c]) {
			t.Errorf("%s files differ:\n%s\n---\n%s", c, mf[c], rf[c])
		}
		if _, err := LoadRunner(writeTemp(t, rf[c])); err != nil {
			t.Errorf("the runner chart's %s file does not load: %v\n%s", c, err, rf[c])
		}
	}
}

// The runner chart's schema for runner.* and deployRunner.* is the main
// chart's (but for the server URL and the token, which differ).
func TestRunnerChartSchema(t *testing.T) {
	load := func(p string) map[string]any {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var s map[string]any
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return s
	}
	// The main chart's schema is open (unknown keys are reported by its templates, so a rollback can
	// render); the runner chart's stays closed. Compare them as if both were closed.
	var closed func(v any) any
	closed = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, e := range x {
				if k == "additionalProperties" && e == false {
					continue
				}
				out[k] = closed(e)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = closed(e)
			}
			return out
		}
		return v
	}
	m, r := closed(load(mainChart+"/values.schema.json")).(map[string]any), closed(load(runnerChart+"/values.schema.json")).(map[string]any)
	mp, rp := m["properties"].(map[string]any), r["properties"].(map[string]any)
	mr := mp["runner"].(map[string]any)["properties"].(map[string]any)
	rr := rp["runner"].(map[string]any)["properties"].(map[string]any)
	for k, v := range mr {
		if k == "serverURL" {
			continue
		}
		if !reflect.DeepEqual(v, rr[k]) {
			t.Errorf("runner.%s: the schemas differ", k)
		}
	}
	md := mp["delivery"].(map[string]any)["properties"].(map[string]any)["deployRunner"].(map[string]any)["properties"].(map[string]any)
	rd := rp["deployRunner"].(map[string]any)["properties"].(map[string]any)
	for k, v := range md {
		if k == "poolToken" {
			continue
		}
		if !reflect.DeepEqual(v, rd[k]) {
			t.Errorf("deployRunner.%s: the schemas differ", k)
		}
	}
	if !reflect.DeepEqual(m["definitions"], r["definitions"]) {
		t.Error("the schemas' definitions differ")
	}
}

func TestRunnerChartRefuses(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	for values, want := range map[string]string{
		"token: { existingSecret: t }\n": "server.url is required",
		"server: { url: https://x }\n":   "token.existingSecret is required",
		"server: { url: https://x }\njoin: { existingSecret: old }\nrunner: { join: { existingSecret: new } }\n": "Set only one of join.existingSecret",
		"server: { url: https://x }\nrunner: { enabled: false }\n":                                               "Nothing to run",
	} {
		out, err := exec.Command("helm", "template", "t", runnerChart, "-f", writeTemp(t, values)).CombinedOutput()
		if err == nil || !strings.Contains(string(out), want) {
			t.Errorf("%q: want an error saying %q, got %v\n%s", values, want, err, out)
		}
	}
}

func TestRunnerChartJoinTokens(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	for name, values := range map[string]string{
		"original alias":              "server: { url: https://x }\njoin: { existingSecret: j }\n",
		"runner value":                "server: { url: https://x }\nrunner: { join: { existingSecret: j } }\n",
		"join overrides shared token": "server: { url: https://x }\ntoken: { existingSecret: t }\njoin: { existingSecret: j }\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := exec.Command("helm", "template", "t", runnerChart, "-f", writeTemp(t, values)).CombinedOutput()
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, out)
			}
			text := string(out)
			if !strings.Contains(text, "name: LAUNCHWAY_JOIN") || !strings.Contains(text, "name: j") {
				t.Errorf("the join Secret is not mounted:\n%s", text)
			}
			if strings.Contains(text, "name: LAUNCHWAY_RUNNER_TOKEN") {
				t.Errorf("the shared token is mounted as well as the join token:\n%s", text)
			}
		})
	}
}

// runner.kubernetes.priorityClassName reaches the Station's file as priority_class_name in both charts, loads, and
// is absent when it is not set, so that nothing changes for an installation that does not use it.
func TestChartsPassThePriorityClassOfStepPods(t *testing.T) {
	file := func(docs []map[string]any) string {
		for _, d := range docs {
			if d["kind"] != "ConfigMap" {
				continue
			}
			labels := d["metadata"].(map[string]any)["labels"].(map[string]any)
			if c, _ := labels["app.kubernetes.io/component"].(string); c == "runner" {
				return d["data"].(map[string]any)["runner.yaml"].(string)
			}
		}
		t.Fatal("no runner.yaml rendered")
		return ""
	}
	const base = "runner:\n  labels: [remote]\n  kubernetes: { namespace: lw-steps%s }\n"
	for _, c := range []struct {
		chart, values string
	}{
		{mainChart, "postgresql: { enabled: true }\n" + base},
		{runnerChart, "server: { url: \"http://t-launchway.lw.svc:8080\" }\ntoken: { existingSecret: t-launchway-runner-token }\n" + base},
	} {
		with := file(helmTemplate(t, c.chart, "lw", strings.Replace(c.values, "%s", ", priorityClassName: builds-low", 1)))
		if !strings.Contains(with, "priority_class_name: builds-low") {
			t.Errorf("%s: the setting did not reach the file:\n%s", c.chart, with)
		}
		if _, err := LoadRunner(writeTemp(t, with)); err != nil {
			t.Errorf("%s: the file does not load: %v", c.chart, err)
		}
		if without := file(helmTemplate(t, c.chart, "lw", strings.Replace(c.values, "%s", "", 1))); strings.Contains(without, "priority_class_name") {
			t.Errorf("%s: an unset setting appears in the file:\n%s", c.chart, without)
		}
	}
}
