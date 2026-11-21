package engine

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const loopFetchPipeline = `
pipeline: demo
steps:
  build:
    for_each: { os: [linux, mac], arch: [amd64] }
    exclude: [ { os: mac, arch: amd64 } ]
    add: [ { os: windows, arch: arm64 } ]
    continue_for: [ { os: windows } ]
    image: x
    run: make
    artifacts: [{name: binary, paths: [bin/app]}]
  package:
    image: x
    run: tar
    needs: build
    fetch:
      - { from: build, name: binary, into: dist }
      - { from: build, name: binary, member: { os: linux, arch: amd64 }, into: only }
`

// One fetch of a loop is a fetch of each member in a directory of its own,
// the member form fetches one directly, and the files are readable.
func TestFetchFromEveryMemberOfALoop(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := createBound(t, e, loopFetchPipeline, nil, Trigger{Event: "manual"})
	for _, os := range []string{"linux", "windows"} {
		j := take(t, e)
		if !strings.HasPrefix(j.StepID, "build[os="+os) {
			t.Fatalf("job %s", j.StepID)
		}
		keep(t, e, j.LeaseID, "binary", []upFile{{"bin/app", []byte("app for " + j.StepID), 0o755}})
		ok(t, e, j, nil)
	}
	p := take(t, e)
	if p.StepID != "package" || len(p.Fetch) != 3 {
		t.Fatalf("package %s fetch %+v", p.StepID, p.Fetch)
	}
	want := map[string]string{
		"build[os=linux,arch=amd64]": "dist/linux_amd64", "build[os=windows,arch=arm64]": "dist/windows_arm64",
	}
	seen := map[string]bool{}
	for _, f := range p.Fetch {
		if f.Problem != "" {
			t.Fatalf("%+v", f)
		}
		seen[f.Into] = true
		if into, ok := want[f.From]; ok && f.Into != into && f.Into != "only" {
			t.Fatalf("%s goes in %s", f.From, f.Into)
		}
		if got := readFetch(t, e, p.LeaseID, f.ArtifactID, "bin/app"); string(got) != "app for "+f.From {
			t.Fatalf("%s fetched %q", f.From, got)
		}
	}
	if !seen["dist/linux_amd64"] || !seen["dist/windows_arm64"] || !seen["only"] || len(seen) != 3 {
		t.Fatalf("destinations %v", seen)
	}
	ok(t, e, p, nil)
	// Every member's artifact is listed under its own step.
	list, _ := e.RunArtifacts(id)
	if len(list.Artifacts) != 2 {
		t.Fatalf("artifacts %+v", list.Artifacts)
	}
}

// A member that was allowed to fail and kept nothing is left out; one that
// succeeded without the artifact fails the fetching step.
func TestFetchFromALoopSkipsToleratedMembersAndFailsOnMissingOnes(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	createBound(t, e, loopFetchPipeline, nil, Trigger{Event: "manual"})
	a := take(t, e) // linux
	keep(t, e, a.LeaseID, "binary", []upFile{{"bin/app", []byte("x"), 0}})
	ok(t, e, a, nil)
	w := take(t, e) // windows: tolerated, fails, kept nothing
	fail(t, e, w)
	p := take(t, e)
	if p.StepID != "package" || len(p.Fetch) != 2 {
		t.Fatalf("package fetch: %+v", p.Fetch)
	}
	for _, f := range p.Fetch {
		if f.Problem != "" || strings.Contains(f.From, "windows") {
			t.Fatalf("%+v", f)
		}
	}
	ok(t, e, p, nil)

	// A member that succeeded without keeping the artifact is a problem.
	e2, _ := newStoreEngine(t, ArtifactOptions{})
	createBound(t, e2, loopFetchPipeline, nil, Trigger{Event: "manual"})
	a = take(t, e2)
	ok(t, e2, a, nil) // kept nothing
	w = take(t, e2)
	fail(t, e2, w)
	p = take(t, e2)
	problems := 0
	for _, f := range p.Fetch {
		if f.Problem != "" {
			problems++
		}
	}
	if problems == 0 {
		t.Fatalf("a successful member that kept nothing must be a problem: %+v", p.Fetch)
	}
}

// A server that does not know schema 27 leaves a run that fetches from a loop
// alone (it would look for a step called after the loop), and refuses to
// create one.
func TestOlderServerLeavesFetchFromLoopRunsAlone(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	cur := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	id := createBound(t, cur, loopFetchPipeline, nil, Trigger{Event: "manual"})
	if sum, err := cur.Summary(id); err != nil || sum.MinSchema != schemaParity2 {
		t.Fatalf("min_schema %d %v", sum.MinSchema, err)
	}
	cur.Close()
	old := mustOpenAs(t, addr, clk, schemaFlow, &syncBuffer{})
	none(t, old)
	if d, err := old.GetRun(id); err != nil || !d.NeedsNewerServer {
		t.Fatalf("%+v %v", d, err)
	}
	b, _ := pipeline.Parse([]byte(loopFetchPipeline))
	vals, _ := pipeline.ResolveInputs(b, nil)
	bound, err := pipeline.Bind(b, vals, pipeline.BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: bound, Trigger: Trigger{Event: "manual"},
		Source: Source{Kind: "dir", Path: "/tmp"}}); err == nil || !strings.Contains(err.Error(), "schema 27") {
		t.Fatalf("%v", err)
	}
}
