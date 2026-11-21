package kube

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Object is one rendered Kubernetes object, and whether the chart marks it as a hook.
type Object struct {
	Obj    *unstructured.Unstructured
	Hook   []string // helm.sh/hook events: pre-install, pre-upgrade, post-install ...
	Weight int
	Delete []string // helm.sh/hook-delete-policy
}

// IsHook reports whether the object belongs to a hook that runs at event.
func (o Object) IsHook(event string) bool {
	for _, h := range o.Hook {
		if h == event {
			return true
		}
	}
	return false
}

// Rendered is the chart rendered for one release.
type Rendered struct {
	Objects []Object
	Notes   string
}

// Hooks returns the hook objects for an event, in weight order.
func (r Rendered) Hooks(event string) []Object {
	var hs []Object
	for _, o := range r.Objects {
		if o.IsHook(event) {
			hs = append(hs, o)
		}
	}
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].Weight < hs[j].Weight })
	return hs
}

// Body returns the objects that are part of the release (not hooks).
func (r Rendered) Body() []Object {
	var out []Object
	for _, o := range r.Objects {
		if len(o.Hook) == 0 {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return kindRank(out[i].Obj.GetKind()) < kindRank(out[j].Obj.GetKind()) })
	return out
}

// kindRank applies things in a safe order: what others refer to first (Helm's own install order, shortened).
func kindRank(kind string) int {
	order := []string{"Namespace", "ResourceQuota", "ServiceAccount", "Secret", "ConfigMap", "PersistentVolumeClaim", "Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding",
		"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "NetworkPolicy", "Service", "Deployment", "StatefulSet", "PodDisruptionBudget", "Ingress", "HTTPRoute"}
	for i, k := range order {
		if k == kind {
			return i
		}
	}
	return len(order)
}

// Caps are the cluster facts the chart's templates may read (.Capabilities).
type Caps struct {
	KubeVersion string
	APIVersions []string
}

// Render renders the chart with the values, as `helm template` would, with the cluster's capabilities.
func Render(c *chart.Chart, values map[string]any, release, namespace string, caps Caps) (*Rendered, error) {
	cp := *chartutil.DefaultCapabilities
	if caps.KubeVersion != "" {
		kv, err := chartutil.ParseKubeVersion(caps.KubeVersion)
		if err == nil {
			cp.KubeVersion = *kv
		}
	}
	if len(caps.APIVersions) > 0 {
		cp.APIVersions = append(chartutil.VersionSet{}, chartutil.DefaultVersionSet...)
		cp.APIVersions = append(cp.APIVersions, caps.APIVersions...)
	}
	rv, err := chartutil.ToRenderValues(c, values, chartutil.ReleaseOptions{Name: release, Namespace: namespace, IsInstall: true, Revision: 1}, &cp)
	if err != nil {
		return nil, fmt.Errorf("the values do not fit the chart: %w", err)
	}
	files, err := engine.Render(c, rv)
	if err != nil {
		return nil, fmt.Errorf("rendering the chart: %w", err)
	}
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	out := &Rendered{}
	for _, n := range names {
		base := n[strings.LastIndex(n, "/")+1:]
		if strings.HasPrefix(base, "_") || strings.TrimSpace(files[n]) == "" {
			continue
		}
		if base == "NOTES.txt" {
			out.Notes = strings.TrimSpace(files[n])
			continue
		}
		dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(files[n])), 4096)
		for {
			var m map[string]any
			if err := dec.Decode(&m); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("%s: %w", n, err)
			}
			if len(m) == 0 {
				continue
			}
			u := &unstructured.Unstructured{Object: m}
			o := Object{Obj: u}
			if h := u.GetAnnotations()["helm.sh/hook"]; h != "" {
				for _, e := range strings.Split(h, ",") {
					o.Hook = append(o.Hook, strings.TrimSpace(e))
				}
				o.Weight, _ = strconv.Atoi(u.GetAnnotations()["helm.sh/hook-weight"])
				for _, d := range strings.Split(u.GetAnnotations()["helm.sh/hook-delete-policy"], ",") {
					if d = strings.TrimSpace(d); d != "" {
						o.Delete = append(o.Delete, d)
					}
				}
			}
			out.Objects = append(out.Objects, o)
		}
	}
	return out, nil
}
