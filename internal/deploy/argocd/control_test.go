package argocd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// rolloutFixture is the captured rollout paused indefinitely, as namespace
// shop, name shop.
func rolloutFixture(t *testing.T) map[string]any {
	b, err := os.ReadFile(filepath.Join("..", "testdata", "rollout-paused-indefinite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	meta := m["metadata"].(map[string]any)
	meta["namespace"], meta["name"] = "shop", "shop"
	return m
}

// A promote at the expected pause patches the status subresource only, with
// the resourceVersion read; one for another pod template is not applied.
func TestCommandPatchesStatusOnly(t *testing.T) {
	ro := rolloutFixture(t)
	dyn, cl := fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"}})
	if err := dyn.Tracker().Create(rollouts, &unstructured.Unstructured{Object: ro}, "shop"); err != nil {
		t.Fatal(err)
	}
	a := agentFor(t, t.TempDir(), cl)
	red := deploy.ReduceRollout(ro, nil)
	j := &engine.CommandJob{ID: "cmd_1", Take: "t", Action: "promote", Project: "shop", Target: "cluster-a", Application: "shop",
		Rollout: pipeline.RolloutRef{Name: "shop", Namespace: "shop"}, Expect: deploy.Expect{PodHash: red.CurrentPodHash, Step: red.Step}}
	result, detail, err := a.Command(context.Background(), j, t.Logf)
	if err != nil || result != deploy.Applied {
		t.Fatalf("%s %s %v", result, detail, err)
	}
	var sub string
	var body map[string]any
	for _, act := range dyn.Actions() {
		if p, ok := act.(k8stesting.PatchAction); ok && p.GetResource() == rollouts {
			sub = p.GetSubresource()
			json.Unmarshal(p.GetPatch(), &body)
		}
	}
	st, _ := body["status"].(map[string]any)
	if sub != "status" || st == nil || st["pauseConditions"] != nil || len(st) != 1 || body["metadata"].(map[string]any)["resourceVersion"] == nil {
		t.Fatalf("patch %q %v", sub, body)
	}
	j.Expect.PodHash = "another"
	if result, _, _ := a.Command(context.Background(), j, t.Logf); result != deploy.NotApplied {
		t.Fatalf("superseded: %s", result)
	}
	j.Rollout.Namespace = "elsewhere"
	if result, _, _ := a.Command(context.Background(), j, t.Logf); result != deploy.NotApplied {
		t.Fatalf("a namespace not allowed: %s", result)
	}
}
