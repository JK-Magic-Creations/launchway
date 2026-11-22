package kube

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Settings are the answers that become the chart's values: about ten lines, the rest is the
// chart's secure default.
type Settings struct {
	Release, Namespace string
	ExternalURL        string
	// Image: repository and one of tag or digest. Empty: the chart's own default.
	ImageRepo, ImageTag, ImageDigest string
	HA                               bool
	StorageClass                     string
	// TrialStorage runs the bundled database on temporary storage: all data is lost when its pod is
	// replaced. The installer applies this itself (the chart has no such switch), so the equivalent
	// Helm command cannot reproduce it.
	TrialStorage bool
	DatabaseURL  bool // the database URL is given separately (never written in the values file)
	// Way in.
	IngressClass   string
	IngressHost    string
	IngressTLSName string            // a Secret holding the certificate
	IngressAnnots  map[string]string // cert-manager.io/cluster-issuer
	Gateway        *GatewayRef
	GatewayHost    string
	PullSecret     string
	// NetworkPoliciesOff leaves the chart's NetworkPolicies off because another system already decides
	// network policy in the namespace (DetectPolicyAuthority).
	NetworkPoliciesOff bool
}

// GatewayRef is the Gateway an HTTPRoute attaches to.
type GatewayRef struct{ Name, Namespace, Section string }

// Values is the map handed to the chart.
func (s Settings) Values() map[string]any {
	v := map[string]any{"externalURL": s.ExternalURL}
	if s.ImageRepo != "" || s.ImageTag != "" || s.ImageDigest != "" {
		img := map[string]any{}
		if s.ImageRepo != "" {
			img["repository"] = s.ImageRepo
		}
		if s.ImageDigest != "" {
			img["digest"] = s.ImageDigest
		} else if s.ImageTag != "" {
			img["tag"] = s.ImageTag
		}
		v["image"] = img
	}
	if !s.HA {
		v["server"] = map[string]any{"replicas": 1}
		v["runner"] = map[string]any{"replicas": 1}
	}
	if s.StorageClass != "" {
		v["postgresql"] = map[string]any{"persistence": map[string]any{"storageClass": s.StorageClass}}
	}
	if s.IngressClass != "" {
		ing := map[string]any{"enabled": true, "className": s.IngressClass}
		if s.IngressHost != "" {
			ing["hosts"] = []any{s.IngressHost}
		}
		if len(s.IngressAnnots) > 0 {
			a := map[string]any{}
			for k, val := range s.IngressAnnots {
				a[k] = val
			}
			ing["annotations"] = a
		}
		if s.IngressTLSName != "" {
			ing["tls"] = []any{map[string]any{"secretName": s.IngressTLSName, "hosts": []any{s.IngressHost}}}
		}
		v["ingress"] = ing
	}
	if g := s.Gateway; g != nil {
		ref := map[string]any{"name": g.Name}
		if g.Namespace != "" {
			ref["namespace"] = g.Namespace
		}
		if g.Section != "" {
			ref["sectionName"] = g.Section
		}
		hr := map[string]any{"enabled": true, "parentRefs": []any{ref}}
		if s.GatewayHost != "" {
			hr["hostnames"] = []any{s.GatewayHost}
		}
		v["httpRoute"] = hr
	}
	if s.NetworkPoliciesOff {
		v["networkPolicy"] = map[string]any{"enabled": false}
	}
	if s.PullSecret != "" {
		v["imagePullSecrets"] = []any{map[string]any{"name": s.PullSecret}}
	}
	return v
}

// YAML is the small values file, with a header that says what it is.
func (s Settings) YAML() ([]byte, error) {
	b, err := yaml.Marshal(s.Values())
	if err != nil {
		return nil, err
	}
	head := "# Written by `launchway install`. About ten lines: everything not here is the chart's secure default.\n" +
		"# Safe to commit: it holds no secret. The root key, the database password and the setup code are made in the cluster, once.\n"
	return append([]byte(head), b...), nil
}

// HelmCommand is the Helm command that gives the same installation, for someone who would rather run it.
func (s Settings) HelmCommand(valuesFile, chartRef string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "helm upgrade --install %s %s --namespace %s --create-namespace -f %s", s.Release, chartRef, s.Namespace, valuesFile)
	if s.TrialStorage {
		b.WriteString("   # (the installer's temporary-storage trial has no Helm equivalent)")
	}
	return b.String()
}

// Merge adds extra top-level keys (used by tests and answers files) onto the values, deeply.
func Merge(dst, src map[string]any) map[string]any {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				dst[k] = Merge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

// SortedKeys is a helper for stable output.
func SortedKeys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// RenderValues are the values the installer renders its own preview with: strict, so a key the
// chart does not know fails here. The values FILE it writes (and the GitOps manifests) leave
// strictValues out, so the same file still renders under an older chart after a rollback.
func (s Settings) RenderValues() map[string]any {
	v := s.Values()
	v["strictValues"] = true
	return v
}
