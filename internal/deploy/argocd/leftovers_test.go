package argocd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

func liveObj(api, kind, name string, ann map[string]any, owners ...map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": "shop", "labels": map[string]any{"app.kubernetes.io/instance": "shop"}}
	if ann != nil {
		meta["annotations"] = ann
	}
	if len(owners) > 0 {
		var l []any
		for _, o := range owners {
			l = append(l, o)
		}
		meta["ownerReferences"] = l
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": api, "kind": kind, "metadata": meta}}
}

func leftoverCluster(t *testing.T, bare, commit string, status []any, live ...runtime.Object) *Cluster {
	t.Helper()
	app := map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"},
		"spec":     map[string]any{"source": map[string]any{"repoURL": bare}},
		"status": map[string]any{"sync": map[string]any{"status": "OutOfSync", "revision": commit}, "health": map[string]any{"status": "Healthy"},
			"resources": status}}
	objs := append([]runtime.Object{&unstructured.Unstructured{Object: app}}, live...)
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		appGVR: "ApplicationList", appProjects: "AppProjectList"}, objs...)
	return NewCluster(dyn, "argocd")
}

func pruneRes(group, kind, name string) map[string]any {
	return map[string]any{"group": group, "kind": kind, "name": name, "namespace": "shop", "status": "OutOfSync", "requiresPruning": true}
}

func observeLeftovers(t *testing.T, bare, commit string, cl *Cluster) (*deploy.App, deploy.Verdict) {
	t.Helper()
	a := agentFor(t, bare, cl)
	it := engine.WatchItem{Key: "k", Project: "shop", Target: "cluster-a", Application: "shop", Repo: "gitops", Branch: "main"}
	it.Spec.Kind, it.Spec.Commit, it.Spec.Health, it.Spec.Windows = "deploy", commit, "application", "wait"
	snap := a.Observe(context.Background(), it)
	if snap.Error != "" || snap.App == nil {
		t.Fatalf("%+v", snap)
	}
	return snap.App, deploy.Evaluate(it.Spec, snap)
}

// The release's own Helm hook objects (the bootstrap job's service account,
// role and binding) are never leftovers, even though the controller lists them
// as no longer rendered and the delete policy leaves them in place.
func TestHookObjectsOfTheCurrentRenderAreNotLeftovers(t *testing.T) {
	hook := map[string]any{"helm.sh/hook": "pre-install,pre-upgrade", "helm.sh/hook-delete-policy": "before-hook-creation"}
	bare, commit := bareRepo(t)
	cl := leftoverCluster(t, bare, commit, []any{
		pruneRes("", "ServiceAccount", "shop-bootstrap"), pruneRes("rbac.authorization.k8s.io", "Role", "shop-bootstrap"),
		pruneRes("rbac.authorization.k8s.io", "RoleBinding", "shop-bootstrap")},
		liveObj("v1", "ServiceAccount", "shop-bootstrap", hook),
		liveObj("rbac.authorization.k8s.io/v1", "Role", "shop-bootstrap", hook),
		liveObj("rbac.authorization.k8s.io/v1", "RoleBinding", "shop-bootstrap", map[string]any{"argocd.argoproj.io/hook": "PreSync"}))
	app, v := observeLeftovers(t, bare, commit, cl)
	if len(app.Leftovers) != 0 {
		t.Fatalf("hooks counted as leftovers: %+v", app.Leftovers)
	}
	if v.Phase == deploy.PhaseLeftovers {
		t.Fatalf("%+v", v)
	}
}

// A real leftover still shows (delivered with a problem, never failed); an
// object owned by a tracked object, and one that is not the release's, are ignored.
func TestRealLeftoverStaysAndOwnedOrForeignObjectsAreIgnored(t *testing.T) {
	foreign := liveObj("v1", "ConfigMap", "other", nil)
	foreign.SetLabels(map[string]string{"app.kubernetes.io/instance": "somebody-else"})
	bare, commit := bareRepo(t)
	cl := leftoverCluster(t, bare, commit, []any{
		map[string]any{"group": "batch", "kind": "Job", "name": "shop-job", "namespace": "shop", "status": "Synced"},
		pruneRes("networking.k8s.io", "NetworkPolicy", "shop-allow-all"),
		pruneRes("", "ConfigMap", "owned"), pruneRes("", "ConfigMap", "other")},
		liveObj("networking.k8s.io/v1", "NetworkPolicy", "shop-allow-all", nil),
		liveObj("v1", "ConfigMap", "owned", nil, map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": "shop-job"}),
		foreign)
	app, v := observeLeftovers(t, bare, commit, cl)
	if len(app.Leftovers) != 1 || app.Leftovers[0].Name != "shop-allow-all" {
		t.Fatalf("%+v", app.Leftovers)
	}
	if v.Outcome != deploy.Succeeded || v.Phase != deploy.PhaseLeftovers || !strings.Contains(v.Detail, "DELIVERED, WITH A PROBLEM TO FIX") ||
		!strings.Contains(v.Detail, "NetworkPolicy/shop-allow-all") || !strings.Contains(v.Detail, "not a reason to roll back") {
		t.Fatalf("%+v", v)
	}
}

func errorsAs(err error, target **HeldError) bool { return errors.As(err, target) }
