package engine

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Pools compose with the other rules of who takes a step: an image step
// goes only to a runner of a pool serving the project that also builds
// images; a run a policy governs is still judged (and its decision kept)
// when pools decide its runners, and needs the newest schema of both.
func TestPoolsComposeWithImagesAndPolicy(t *testing.T) {
	clk := newClock()
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, RunnerPools: testPools,
		Policy: PolicyOptions{Inline: mustPolicies(t, "policies:\n  - name: shape\n    mode: report\n    rules:\n      step_images: { digest: true }\n")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	web := enrol(t, e, "web")
	def := enrol(t, e, DefaultPool)
	src := strings.Replace(imagePipeline, "pipeline: shop", "pipeline: web-shop", 1)
	id := createIn(t, e, "web-shop", src, Trigger{Event: "push", Branch: "main", Ref: "refs/heads/main"})
	if sum, _ := e.Summary(id); sum.MinSchema != schemaPools {
		t.Fatalf("min_schema %d, want %d (pools, above images 15 and policy 16)", sum.MinSchema, schemaPools)
	}
	if d, _ := e.GetRun(id); d.Policy == nil {
		t.Fatal("the run was not judged by policy")
	}
	images := func(r *enrolled) RunnerInfo {
		i := r.info(t, e)
		i.Features = FeatureArtifacts + " " + FeatureImages
		return i
	}
	// A runner of a pool that does not serve web-shop never takes it, even
	// one that builds images.
	if j := leaseAs(t, e, images(def)); j != nil {
		t.Fatalf("the default pool took %s/%s", j.Project, j.StepID)
	}
	// A web runner that does not build images cannot take a run with image
	// steps (its steps share one workspace).
	if j := leaseAs(t, e, web.info(t, e)); j != nil {
		t.Fatalf("a web runner without images took %s", j.StepID)
	}
	// A web runner that builds images takes it.
	if j := leaseAs(t, e, images(web)); j == nil || j.RunID != id || j.StepID != "test" {
		t.Fatalf("the web runner with images: %+v", j)
	}
}
