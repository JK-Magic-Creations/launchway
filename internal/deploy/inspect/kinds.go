package inspect

import (
	"context"
	"encoding/json"
	"k8s.io/apimachinery/pkg/types"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// kindInfo says where a kind lives and where it goes in the tree.
type kindInfo struct {
	gvr      schema.GroupVersionResource
	listKind string
	section  string
}

func gvr(g, v, r string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: g, Version: v, Resource: r}
}

// kinds are the only kinds the inspector reads. Anything else an
// application lists is shown by name, unread.
var kinds = map[string]kindInfo{
	"Rollout":                 {gvr("argoproj.io", "v1alpha1", "rollouts"), "RolloutList", "workloads"},
	"Deployment":              {gvr("apps", "v1", "deployments"), "DeploymentList", "workloads"},
	"StatefulSet":             {gvr("apps", "v1", "statefulsets"), "StatefulSetList", "workloads"},
	"DaemonSet":               {gvr("apps", "v1", "daemonsets"), "DaemonSetList", "workloads"},
	"CronJob":                 {gvr("batch", "v1", "cronjobs"), "CronJobList", "workloads"},
	"Job":                     {gvr("batch", "v1", "jobs"), "JobList", "workloads"},
	"ReplicaSet":              {gvr("apps", "v1", "replicasets"), "ReplicaSetList", ""},
	"Pod":                     {gvr("", "v1", "pods"), "PodList", ""},
	"Service":                 {gvr("", "v1", "services"), "ServiceList", "network"},
	"Ingress":                 {gvr("networking.k8s.io", "v1", "ingresses"), "IngressList", "network"},
	"HTTPRoute":               {gvr("gateway.networking.k8s.io", "v1", "httproutes"), "HTTPRouteList", "network"},
	"VirtualService":          {gvr("networking.istio.io", "v1", "virtualservices"), "VirtualServiceList", "network"},
	"DestinationRule":         {gvr("networking.istio.io", "v1", "destinationrules"), "DestinationRuleList", "network"},
	"NetworkPolicy":           {gvr("networking.k8s.io", "v1", "networkpolicies"), "NetworkPolicyList", "network"},
	"ConfigMap":               {gvr("", "v1", "configmaps"), "ConfigMapList", "config"},
	"Secret":                  {gvr("", "v1", "secrets"), "SecretList", "config"},
	"ServiceAccount":          {gvr("", "v1", "serviceaccounts"), "ServiceAccountList", "access"},
	"Role":                    {gvr("rbac.authorization.k8s.io", "v1", "roles"), "RoleList", "access"},
	"RoleBinding":             {gvr("rbac.authorization.k8s.io", "v1", "rolebindings"), "RoleBindingList", "access"},
	"HorizontalPodAutoscaler": {gvr("autoscaling", "v2", "horizontalpodautoscalers"), "HorizontalPodAutoscalerList", "scaling"},
	"PodDisruptionBudget":     {gvr("policy", "v1", "poddisruptionbudgets"), "PodDisruptionBudgetList", "scaling"},
	"PersistentVolumeClaim":   {gvr("", "v1", "persistentvolumeclaims"), "PersistentVolumeClaimList", "storage"},
	"AnalysisRun":             {gvr("argoproj.io", "v1alpha1", "analysisruns"), "AnalysisRunList", ""},
	"Experiment":              {gvr("argoproj.io", "v1alpha1", "experiments"), "ExperimentList", ""},
	"Event":                   {gvr("", "v1", "events"), "EventList", ""},
}

// KindLists maps each resource to its list kind, for fake clients in
// tests (the dynamic fake needs them registered).
func KindLists() map[schema.GroupVersionResource]string {
	m := map[schema.GroupVersionResource]string{}
	for _, k := range kinds {
		m[k.gvr] = k.listKind
	}
	return m
}

// Reader reads objects. It is the whole of what tree building asks of a
// cluster, so tests give a fake.
type Reader interface {
	// Get returns nil, nil when the object does not exist.
	Get(ctx context.Context, kind, ns, name string) (map[string]any, error)
	List(ctx context.Context, kind, ns string) ([]map[string]any, error)
}

// Dynamic reads through a dynamic client.
type Dynamic struct{ Client dynamic.Interface }

// Get implements Reader.
func (d Dynamic) Get(ctx context.Context, kind, ns, name string) (map[string]any, error) {
	k, ok := kinds[kind]
	if !ok {
		return nil, errUnknownKind(kind)
	}
	obj, err := d.Client.Resource(k.gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return obj.Object, nil
}

// List implements Reader.
func (d Dynamic) List(ctx context.Context, kind, ns string) ([]map[string]any, error) {
	k, ok := kinds[kind]
	if !ok {
		return nil, errUnknownKind(kind)
	}
	l, err := d.Client.Resource(k.gvr).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 2000})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(l.Items))
	for _, it := range l.Items {
		out = append(out, it.Object)
	}
	return out, nil
}

type errUnknownKind string

func (e errUnknownKind) Error() string {
	return "the inspector does not read objects of kind " + string(e)
}

// MergePatch implements Writer.
func (d Dynamic) MergePatch(ctx context.Context, kind, ns, name string, patch map[string]any) error {
	k, ok := kinds[kind]
	if !ok {
		return errUnknownKind(kind)
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = d.Client.Resource(k.gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{FieldManager: "launchway"})
	return err
}
