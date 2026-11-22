package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/kubernetes"
)

// MinKubeVersion is the oldest Kubernetes the chart supports (Chart.yaml kubeVersion).
const MinKubeVersion = "1.30.0"

// Facts are what the installer learned about the cluster, before it asks anything.
type Facts struct {
	ServerVersion string
	VersionOK     bool
	// Rights.
	CanCreateNamespace bool
	CanInstallInNS     map[string]bool // namespace -> may create deployments there
	NamespaceExists    map[string]bool
	NamespacePSA       map[string]string // the enforced pod security level, if labelled
	// Storage.
	StorageClasses []string
	DefaultStorage string
	// A way in.
	IngressClasses []string
	GatewayClasses []string // empty and GatewayAPI false when the CRDs are absent
	GatewayAPI     bool
	// TLS.
	CertManager bool
	Issuers     []string // ClusterIssuers when cert-manager is present
	// Policy enforcement: the network plugin, as best it can be told.
	CNI            string // "cilium", "calico", ... or ""
	NetpolEnforced Tri
	// PolicyAuthority is network policy already in place in the target namespace (see DetectPolicyAuthority).
	PolicyAuthority []PolicyObjects
	// Capacity.
	Nodes        int
	FreeCPUMilli int64
	FreeMemBytes int64
	// GitOps controllers that may manage this cluster.
	ArgoCD, Flux bool
	// APIs the chart's capabilities check can use.
	APIVersions []string
}

// Tri is a fact that may not be knowable from here.
type Tri int

const (
	Unknown Tri = iota
	Yes
	No
)

// Detect reads the cluster. Every probe that fails degrades to "unknown"; Detect itself fails only
// when the cluster cannot be reached at all.
func Detect(ctx context.Context, cs kubernetes.Interface, namespace string) (*Facts, error) {
	f := &Facts{CanInstallInNS: map[string]bool{}, NamespaceExists: map[string]bool{}, NamespacePSA: map[string]string{}}
	v, err := cs.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("the cluster did not answer: %w", err)
	}
	f.ServerVersion = v.GitVersion
	if sv, err := version.ParseGeneric(v.GitVersion); err == nil {
		f.VersionOK = sv.AtLeast(version.MustParseGeneric(MinKubeVersion))
	} else {
		f.VersionOK = true
	}
	f.CanCreateNamespace = can(ctx, cs, "", "create", "", "namespaces")
	f.CanInstallInNS[namespace] = can(ctx, cs, namespace, "create", "apps", "deployments") && can(ctx, cs, namespace, "create", "", "secrets")
	if ns, err := cs.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err == nil {
		f.NamespaceExists[namespace] = true
		f.NamespacePSA[namespace] = ns.Labels["pod-security.kubernetes.io/enforce"]
	}
	if scs, err := cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{}); err == nil {
		for _, sc := range scs.Items {
			f.StorageClasses = append(f.StorageClasses, sc.Name)
			if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" || sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
				f.DefaultStorage = sc.Name
			}
		}
	}
	if ics, err := cs.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{}); err == nil {
		for _, ic := range ics.Items {
			f.IngressClasses = append(f.IngressClasses, ic.Name)
		}
	}
	groups, _ := cs.Discovery().ServerGroups()
	if groups != nil {
		for _, g := range groups.Groups {
			switch g.Name {
			case "gateway.networking.k8s.io":
				f.GatewayAPI = true
			case "cert-manager.io":
				f.CertManager = true
			case "argoproj.io":
				f.ArgoCD = hasResource(cs, "argoproj.io/v1alpha1", "applications")
			case "helm.toolkit.fluxcd.io":
				f.Flux = true
			}
			for _, vv := range g.Versions {
				f.APIVersions = append(f.APIVersions, vv.GroupVersion)
			}
		}
	}
	if f.GatewayAPI {
		f.GatewayClasses = listNames(ctx, cs, "/apis/gateway.networking.k8s.io/v1/gatewayclasses")
	}
	if f.CertManager {
		f.Issuers = listNames(ctx, cs, "/apis/cert-manager.io/v1/clusterissuers")
	}
	f.CNI, f.NetpolEnforced = detectCNI(ctx, cs)
	f.PolicyAuthority = DetectPolicyAuthority(ctx, cs, namespace)
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil {
		f.Nodes = len(nodes.Items)
		var cpu, mem int64
		for _, n := range nodes.Items {
			cpu += n.Status.Allocatable.Cpu().MilliValue()
			mem += n.Status.Allocatable.Memory().Value()
		}
		var rc, rm int64
		if pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase!=Succeeded,status.phase!=Failed"}); err == nil {
			for _, p := range pods.Items {
				c, m := podRequests(p)
				rc, rm = rc+c, rm+m
			}
		}
		f.FreeCPUMilli, f.FreeMemBytes = cpu-rc, mem-rm
	}
	sort.Strings(f.StorageClasses)
	return f, nil
}

func hasResource(cs kubernetes.Interface, gv, name string) bool {
	rl, err := cs.Discovery().ServerResourcesForGroupVersion(gv)
	if err != nil || rl == nil {
		return false
	}
	for _, r := range rl.APIResources {
		if r.Name == name {
			return true
		}
	}
	return false
}

func can(ctx context.Context, cs kubernetes.Interface, ns, verb, group, res string) bool {
	r, err := cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authv1.ResourceAttributes{Namespace: ns, Verb: verb, Group: group, Resource: res}}}, metav1.CreateOptions{})
	return err == nil && r.Status.Allowed
}

func listNames(ctx context.Context, cs kubernetes.Interface, path string) (names []string) {
	defer func() {
		if recover() != nil { // a discovery client with no REST client (a test double)
			names = nil
		}
	}()
	b, err := cs.Discovery().RESTClient().Get().AbsPath(path).DoRaw(ctx)
	if err != nil {
		return nil
	}
	var l struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
		} `json:"items"`
	}
	if jsonUnmarshal(b, &l) != nil {
		return nil
	}
	var out []string
	for _, i := range l.Items {
		out = append(out, i.Metadata.Name)
	}
	sort.Strings(out)
	return out
}

func podRequests(p corev1.Pod) (cpu, mem int64) {
	add := func(rl corev1.ResourceList) {
		if q, ok := rl[corev1.ResourceCPU]; ok {
			cpu += q.MilliValue()
		}
		if q, ok := rl[corev1.ResourceMemory]; ok {
			mem += q.Value()
		}
	}
	for _, c := range p.Spec.Containers {
		add(c.Resources.Requests)
	}
	return
}

// cniEnforcers are network plugins known to enforce NetworkPolicy; others known not to (flannel
// alone, a bare kindnet from before it gained policy support) are named so the message is exact.
var cniEnforcers = []string{"cilium", "calico", "antrea", "kube-router", "weave", "canal", "kube-ovn", "romana"}

func detectCNI(ctx context.Context, cs kubernetes.Interface) (string, Tri) {
	pods, err := cs.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", Unknown
	}
	var names []string
	for _, p := range pods.Items {
		names = append(names, strings.ToLower(p.Name))
	}
	for _, e := range cniEnforcers {
		for _, n := range names {
			if strings.HasPrefix(n, e) || strings.Contains(n, "-"+e) {
				return e, Yes
			}
		}
	}
	for _, n := range names {
		if strings.HasPrefix(n, "kindnet") {
			return "kindnet", Unknown // newer kindnet enforces; older does not: cannot tell from the name
		}
		if strings.HasPrefix(n, "kube-flannel") || strings.Contains(n, "flannel") {
			return "flannel", No
		}
	}
	return "", Unknown
}

// Headroom is what a small installation asks for: CPU in millicores and memory in bytes
// (summed requests of the chart's pods with one server and one runner, rounded up).
const (
	NeedCPUMilli = 600
	NeedMemBytes = int64(1) << 30
)

func quantity(b int64) string { return resource.NewQuantity(b, resource.BinarySI).String() }
