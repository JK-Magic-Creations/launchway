package inspect

import (
	"context"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/jk-magic-creations/launchway/internal/deploy/inspect/inspectfake"
)

// A release of 300 pods builds into one tree quickly, and a tree of that size
// serializes to something a page can take.
func TestTreeOfThreeHundredPods(t *testing.T) {
	lists := KindLists()
	var objs []runtime.Object
	for i := 0; i < 300; i++ {
		objs = append(objs, inspectfake.PodNamed(fmt.Sprintf("bulk-%03d", i), "shop-aaa", "u-rsa", "aaa"))
	}
	fc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, append(inspectfake.Objects(false), objs...)...)
	b := &Builder{R: Dynamic{Client: fc}, Red: NewRedactor()}
	start := time.Now()
	tr := b.Build(context.Background(), Request{Application: "shop", Namespaces: []string{"shop"}, Resources: listed(), Since: 1})
	took := time.Since(start)
	pods := 0
	for _, n := range tr.Nodes {
		if n.Kind == "Pod" {
			pods++
		}
	}
	if pods < 300 {
		t.Fatalf("%d pods in the tree", pods)
	}
	t.Logf("tree of %d objects (%d pods) built in %s", len(tr.Nodes), pods, took)
	if took > 3*time.Second {
		t.Fatalf("building a tree of %d pods took %s", pods, took)
	}
}
