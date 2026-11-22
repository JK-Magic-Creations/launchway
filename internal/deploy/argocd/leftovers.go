package argocd

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/jk-magic-creations/launchway/internal/deploy"
)

// liveKinds are the kinds a release commonly leaves behind, by group and kind:
// the only ones the runner reads to judge a leftover. Others stay leftovers.
var liveKinds = map[[2]string]schema.GroupVersionResource{
	{"", "ServiceAccount"}:                              {Version: "v1", Resource: "serviceaccounts"},
	{"", "ConfigMap"}:                                   {Version: "v1", Resource: "configmaps"},
	{"", "Secret"}:                                      {Version: "v1", Resource: "secrets"},
	{"", "Service"}:                                     {Version: "v1", Resource: "services"},
	{"", "Pod"}:                                         {Version: "v1", Resource: "pods"},
	{"batch", "Job"}:                                    {Group: "batch", Version: "v1", Resource: "jobs"},
	{"apps", "Deployment"}:                              {Group: "apps", Version: "v1", Resource: "deployments"},
	{"rbac.authorization.k8s.io", "Role"}:               {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
	{"rbac.authorization.k8s.io", "RoleBinding"}:        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
	{"rbac.authorization.k8s.io", "ClusterRole"}:        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	{"rbac.authorization.k8s.io", "ClusterRoleBinding"}: {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
	{"networking.k8s.io", "NetworkPolicy"}:              {Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
	{"networking.k8s.io", "Ingress"}:                    {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	{"cilium.io", "CiliumNetworkPolicy"}:                {Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"},
	{"gateway.networking.k8s.io", "HTTPRoute"}:          {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
}

// hookAnnotations mark an object as a hook of a sync: Helm's and the
// controller's own.
var hookAnnotations = []string{"helm.sh/hook", "argocd.argoproj.io/hook"}

// LiveObject reads one object of a known kind; nil, nil when it is gone or the
// kind is not one the runner reads (ok false then).
func (c *Cluster) LiveObject(ctx context.Context, group, kind, ns, name string) (obj map[string]any, ok bool, err error) {
	gvr, known := liveKinds[[2]string{group, kind}]
	if !known {
		return nil, false, nil
	}
	var o map[string]any
	if ns == "" {
		u, e := c.dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return nil, true, nil
		}
		if e != nil {
			return nil, true, e
		}
		o = u.Object
	} else if o, err = c.get(ctx, gvr, ns, name); err != nil {
		return nil, true, err
	}
	return o, true, nil
}

// realLeftovers keeps, of the objects Argo CD says need pruning, only those
// that really are leftovers of an earlier render: they carry the release's
// tracking identity, they are not hooks of the current render (a hook object
// that its delete policy leaves in place is expected, and so are completed or
// failed hook Jobs and their pods), and nothing the current render makes
// (hooks included) owns them. An object the runner cannot read stays, as the
// check is then not able to clear it.
func (a *Agent) realLeftovers(ctx context.Context, cl *Cluster, appName string, app *deploy.App) {
	if len(app.Leftovers) == 0 {
		return
	}
	owners := map[string]bool{}
	for _, t := range app.Tracked {
		owners[t.Kind+"/"+t.Namespace+"/"+t.Name] = true
	}
	var keep []deploy.Leftover
	for _, l := range app.Leftovers {
		obj, ok, err := cl.LiveObject(ctx, l.Group, l.Kind, l.Namespace, l.Name)
		switch {
		case err != nil || !ok:
			keep = append(keep, l)
			continue
		case obj == nil:
			continue // already gone
		}
		meta := mapOf(obj, "metadata")
		if isHook(meta) || !carriesIdentity(meta, appName) || ownedBy(meta, l.Namespace, owners) {
			app.Tracked = append(app.Tracked, l)
			owners[l.Kind+"/"+l.Namespace+"/"+l.Name] = true
			continue
		}
		keep = append(keep, l)
	}
	app.Leftovers = keep
}

func isHook(meta map[string]any) bool {
	ann := mapOf(meta, "annotations")
	for _, k := range hookAnnotations {
		if _, ok := ann[k]; ok {
			return true
		}
	}
	return false
}

// carriesIdentity says the object is tracked as part of the application, by
// the controller's annotation or its instance label.
func carriesIdentity(meta map[string]any, app string) bool {
	if id, _ := mapOf(meta, "annotations")["argocd.argoproj.io/tracking-id"].(string); strings.HasPrefix(id, app+":") {
		return true
	}
	l, _ := mapOf(meta, "labels")["app.kubernetes.io/instance"].(string)
	return l == app
}

func ownedBy(meta map[string]any, ns string, owners map[string]bool) bool {
	for _, o := range listOf(meta, "ownerReferences") {
		om, _ := o.(map[string]any)
		kind, _ := om["kind"].(string)
		name, _ := om["name"].(string)
		if owners[kind+"/"+ns+"/"+name] || owners[kind+"//"+name] {
			return true
		}
	}
	return false
}
