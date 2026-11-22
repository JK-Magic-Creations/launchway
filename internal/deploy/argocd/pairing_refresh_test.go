package argocd

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// refreshOnly is a controller that notices a commit ONLY when it is asked for a hard refresh of the application that
// has to see it: its own poll interval is far longer than any test (an hour), so nothing it shows ever changes by
// itself. The parent application applies the new chart pin at the parentNeeds-th hard refresh asked after the manifest
// commit is in Git; the child application reports the values commit at the childNeeds-th one asked after that commit.
type refreshOnly struct {
	mu                      sync.Mutex
	t                       *testing.T
	bare                    string
	oldTip                  string
	parentNeeds, childNeeds int
	pinNew                  bool
	childShowsTip           bool
	parentAsks, childAsks   []time.Time
	parentAskedAfterValues  int // hard refreshes of the parent asked once the values were already written (must be none)
	askedBeforeCommit       int // hard refreshes asked before the commit they are for exists (must be none)
	normalRefreshesOfTheKid int
}

func (c *refreshOnly) head() string {
	cmd := exec.Command("git", "rev-parse", "main")
	cmd.Dir = c.bare
	out, err := cmd.CombinedOutput()
	if err != nil {
		c.t.Fatalf("%s", out)
	}
	return strings.TrimSpace(string(out))
}

func (c *refreshOnly) app(name string) map[string]any {
	pin, rev := "1.0.0", c.oldTip
	if c.pinNew {
		pin = "2.0.0"
	}
	if c.childShowsTip {
		rev = c.head()
	}
	return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": name, "namespace": "argocd", "resourceVersion": "7",
			"annotations": map[string]any{"argocd.argoproj.io/tracking-id": "root:argoproj.io/Application:argocd/shop"}},
		"spec": map[string]any{"sources": []any{
			map[string]any{"repoURL": "https://charts.example", "chart": "shop", "targetRevision": pin},
			map[string]any{"repoURL": c.bare, "targetRevision": "main"}}},
		"status": map[string]any{"sync": map[string]any{"status": "OutOfSync", "revisions": []any{pin, rev}}}}
}

// patch is the reactor for a change to an application: only a hard refresh makes the controller look again.
func (c *refreshOnly) patch(a k8stesting.Action) (bool, runtime.Object, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pa := a.(k8stesting.PatchAction)
	body, name, log := string(pa.GetPatch()), pa.GetName(), gitLog(c.t, c.bare)
	pinIn := strings.Contains(log, "Launchway-Deployment: dep_1-pin")
	valuesIn := strings.Contains(log, "Launchway-Deployment: dep_1\n")
	switch {
	case strings.Contains(body, `"normal"`) && name == "shop":
		c.normalRefreshesOfTheKid++
	case strings.Contains(body, `"hard"`) && name == "root":
		c.parentAsks = append(c.parentAsks, time.Now())
		switch {
		case !pinIn:
			c.askedBeforeCommit++
		case valuesIn:
			c.parentAskedAfterValues++
		case len(c.parentAsks) >= c.parentNeeds:
			c.pinNew = true
		}
	case strings.Contains(body, `"hard"`) && name == "shop":
		c.childAsks = append(c.childAsks, time.Now())
		if !valuesIn {
			c.askedBeforeCommit++
		} else if len(c.childAsks) >= c.childNeeds {
			c.childShowsTip = true
		}
	}
	return true, &unstructured.Unstructured{Object: c.app(name)}, nil
}

// The sequenced write-back against a controller that sees a commit only when asked: a hard refresh of the parent is
// asked after the manifest commit and again, with a growing pause, until the cluster shows the pin; only then are the
// values written, and a hard refresh of the application is asked after them and again until it reports that commit.
// The whole step takes milliseconds: nothing waits for the controller's own (hour-long) poll.
func TestSequencedWriteBackAsksTheControllerToRefreshAfterEachCommit(t *testing.T) {
	bare := pairRepo(t)
	c := &refreshOnly{t: t, bare: bare, parentNeeds: 3, childNeeds: 3}
	c.oldTip = c.head()
	dyn, cl := fakeCluster(t, c.app("shop"))
	dyn.PrependReactor("get", "applications", func(a k8stesting.Action) (bool, runtime.Object, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return true, &unstructured.Unstructured{Object: c.app(a.(k8stesting.GetAction).GetName())}, nil
	})
	dyn.PrependReactor("patch", "applications", c.patch)
	a := agentFor(t, bare, cl)
	const first = 10 * time.Millisecond
	a.PairEvery, a.PairTimeout, a.RefreshEvery, a.SeeEvery = 2*time.Millisecond, 20*time.Second, first, 2*time.Millisecond
	a.SeeTimeout = 20 * time.Second // never gives up on seeing the commit: only the controller showing it ends the wait

	start := time.Now()
	facts, err := a.Apply(context.Background(), pairJob(), func(string, ...any) {})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.parentAsks) != c.parentNeeds || len(c.childAsks) != c.childNeeds {
		t.Fatalf("hard refreshes asked: parent %d (the controller needed %d), application %d (needed %d)", len(c.parentAsks), c.parentNeeds, len(c.childAsks), c.childNeeds)
	}
	if c.askedBeforeCommit != 0 || c.parentAskedAfterValues != 0 {
		t.Fatalf("refreshes asked before their commit existed: %d; of the parent after the values were written: %d", c.askedBeforeCommit, c.parentAskedAfterValues)
	}
	if c.normalRefreshesOfTheKid == 0 {
		t.Error("the application was not asked for an ordinary refresh when its commit was written")
	}
	if !c.pinNew || !c.childShowsTip || facts.Commit != c.head() {
		t.Fatalf("pin shown %v, application shows the values commit %v, facts commit %s, head %s", c.pinNew, c.childShowsTip, facts.Commit, c.head())
	}
	// Bounded back-off: each ask comes at least the pause after the one before (first, then twice that), and the
	// pauses never exceed the bound of refresh.go.
	for name, asks := range map[string][]time.Time{"parent": c.parentAsks, "application": c.childAsks} {
		for i := 1; i < len(asks); i++ {
			if gap, least := asks[i].Sub(asks[i-1]), first<<(i-1); gap < least {
				t.Errorf("%s ask %d came %v after the one before; the pause is at least %v", name, i+1, gap, least)
			}
			if asks[i].Sub(asks[i-1]) > refreshMax {
				t.Errorf("%s ask %d waited longer than the bound %v", name, i+1, refreshMax)
			}
		}
	}
	if limit := testlib.Scaled(10 * time.Second); took > limit {
		t.Fatalf("the step took %v: it waited for the controller's own poll instead of asking it (limit %v)", took, limit)
	}
}
