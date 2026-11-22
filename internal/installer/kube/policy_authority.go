package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PolicyObjects is one kind of network-policy object found in a namespace (or cluster-wide).
type PolicyObjects struct {
	Kind, Group string
	Count       int
	Cluster     bool // a cluster-wide kind: it selects namespaces from outside
}

func (p PolicyObjects) String() string {
	g := p.Group
	if g == "" {
		g = "core"
	}
	where := ""
	if p.Cluster {
		where = " (cluster-wide)"
	}
	return fmt.Sprintf("%d %s (%s)%s", p.Count, p.Kind, g, where)
}

// ownPolicyLabel marks the policies Launchway's own chart made: they are not "someone else's".
const ownPolicyLabel = "app.kubernetes.io/part-of=launchway"

// DetectPolicyAuthority says whether the cluster already has a system that decides network policy in
// the namespace: Kubernetes NetworkPolicies that are not Launchway's own, or objects of any kind
// whose name contains NetworkPolicy from a non-core API group (a network plugin's own policy
// objects: CiliumNetworkPolicy, GlobalNetworkPolicy, CiliumClusterwideNetworkPolicy, ...), found by
// listing the API's kinds generically rather than by a fixed list. On a network plugin whose objects
// ADD to Kubernetes ones, creating the chart's policies as well can widen access (the plugin's allow
// rule admits what the chart would have denied), so the installer then leaves the chart's off.
// Failures to read degrade to "nothing found"; the answer is advice, never a refusal.
func DetectPolicyAuthority(ctx context.Context, cs kubernetes.Interface, ns string) []PolicyObjects {
	var out []PolicyObjects
	if l, err := cs.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{}); err == nil {
		n := 0
		for _, p := range l.Items {
			if !hasLabels(p.Labels, ownPolicyLabel) {
				n++
			}
		}
		if n > 0 {
			out = append(out, PolicyObjects{Kind: "NetworkPolicy", Group: "networking.k8s.io", Count: n})
		}
	}
	defer func() { _ = recover() }() // a discovery client with no REST client (a test double)
	lists, err := cs.Discovery().ServerPreferredResources()
	if err != nil && len(lists) == 0 {
		return out
	}
	for _, rl := range lists {
		gv := strings.SplitN(rl.GroupVersion, "/", 2)
		if len(gv) != 2 || gv[0] == "networking.k8s.io" {
			continue // core group, or the Kubernetes NetworkPolicy already counted
		}
		for _, r := range rl.APIResources {
			if !strings.Contains(strings.ToLower(r.Kind), "networkpolicy") || strings.Contains(r.Name, "/") {
				continue
			}
			path := "/apis/" + rl.GroupVersion + "/" + r.Name
			if r.Namespaced {
				path = "/apis/" + rl.GroupVersion + "/namespaces/" + ns + "/" + r.Name
			}
			b, err := cs.Discovery().RESTClient().Get().AbsPath(path).DoRaw(ctx)
			if err != nil {
				continue
			}
			var l struct {
				Items []json.RawMessage `json:"items"`
			}
			if json.Unmarshal(b, &l) == nil && len(l.Items) > 0 {
				out = append(out, PolicyObjects{Kind: r.Kind, Group: gv[0], Count: len(l.Items), Cluster: !r.Namespaced})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

func hasLabels(have map[string]string, want string) bool {
	kv := strings.SplitN(want, "=", 2)
	return len(kv) == 2 && have[kv[0]] == kv[1]
}

// PolicyAuthorityNote is the plain explanation of what was found and what Launchway does about it.
func PolicyAuthorityNote(found []PolicyObjects) string {
	if len(found) == 0 {
		return ""
	}
	var parts []string
	for _, p := range found {
		parts = append(parts, p.String())
	}
	return "this cluster already has network policy in place: " + strings.Join(parts, ", ") + ". " +
		"On a network plugin whose own policy objects add to Kubernetes ones, adding Launchway's NetworkPolicies on top can WIDEN access, " +
		"and objects it leaves behind stay when it is turned off. Let one system be the authority: Launchway's policies are left off " +
		"(networkPolicy.enabled: false) and yours apply. Turn them on only if you want the chart's to be the authority and have removed the others"
}
