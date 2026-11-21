package argocd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
)

func podsCluster(t *testing.T, withPod bool) (*dynfake.FakeDynamicClient, *Cluster) {
	t.Helper()
	var objs []runtime.Object
	if withPod {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": "smoke-abc", "namespace": "shop", "labels": map[string]any{"job-name": "smoke-job"}}}})
	}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		pods: "PodList", accessReviews: "SelfSubjectAccessReviewList"}, objs...)
	return dyn, NewCluster(dyn, "argocd")
}

// The runner asks the cluster's authorizer whether it may change a rollout's
// status, and says what an administrator must enable when it may not.
func TestRightsSaysWhatToEnable(t *testing.T) {
	dyn, cl := podsCluster(t, false)
	allowed := false
	dyn.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		obj := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		attrs, _, _ := unstructured.NestedMap(obj.Object, "spec", "resourceAttributes")
		allowed = attrs["resource"] == "pods"
		_ = unstructured.SetNestedField(obj.Object, allowed, "status", "allowed")
		return true, obj, nil
	})
	r := cl.Rights(context.Background(), "shop", "shop")
	if r == nil || r.Control == nil || *r.Control {
		t.Fatalf("control should be denied: %+v", r)
	}
	if !strings.Contains(r.ControlWhy, "delivery.deployRunner.rbac.controlRollouts") || !strings.Contains(r.ControlWhy, "shop/shop") {
		t.Fatalf("the reason must name the chart value: %q", r.ControlWhy)
	}
	if r.Logs == nil || !*r.Logs {
		t.Fatalf("logs should be allowed: %+v", r)
	}
}

// A failed job metric gets the tail of its job's log; a runner that may not
// read it says where the log is.
func TestJobLogTailOrNote(t *testing.T) {
	_, cl := podsCluster(t, true)
	cl.logs = func(context.Context, string, string, int) ([]string, error) {
		return []string{"GET /health ... ok", "curl: (6) Could not resolve host: new.shop.svc"}, nil
	}
	runs := []deploy.AnalysisRun{{Name: "a1", Phase: "Failed", Metrics: []deploy.Metric{{Name: "smoke", Phase: "Failed", Job: &deploy.JobRef{Name: "smoke-job", Namespace: "shop"}}}}}
	cl.addJobLogs(context.Background(), runs)
	l := runs[0].Metrics[0].JobLog
	if l == nil || len(l.Lines) != 2 || !strings.Contains(l.Lines[1], "Could not resolve host") {
		t.Fatalf("%+v", l)
	}

	_, cl2 := podsCluster(t, true)
	cl2.logs = func(context.Context, string, string, int) ([]string, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "smoke-abc", fmt.Errorf("no"))
	}
	runs = []deploy.AnalysisRun{{Phase: "Failed", Metrics: []deploy.Metric{{Name: "smoke", Phase: "Failed", Job: &deploy.JobRef{Name: "smoke-job", Namespace: "shop"}}}}}
	cl2.addJobLogs(context.Background(), runs)
	if l := runs[0].Metrics[0].JobLog; l == nil || len(l.Lines) != 0 || !strings.Contains(l.Note, "shop/smoke-job") || !strings.Contains(l.Note, "may not read it") {
		t.Fatalf("%+v", l)
	}
}
