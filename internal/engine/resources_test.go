package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/resources"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// containerOf is the container of the job's image, as the runner is told to
// make it.
func containerOf(t *testing.T, j *Job, name string) pipeline.Container {
	t.Helper()
	for _, c := range j.Containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("the job has no container %s: %+v", name, j.Containers)
	return pipeline.Container{}
}

func firstContainer(t *testing.T, j *Job) pipeline.Container {
	t.Helper()
	return containerOf(t, j, j.Container)
}

// A server that starts with empty tables seeds them with the values
// compiled into the program, and those are what steps get.
func TestDefaultsAreSeededIntoTheDatabaseAndApplyToAStepThatAsksForNothing(t *testing.T) {
	e, _ := newEngine(t)
	cfg, err := e.StepResources()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Defaults) != 1 || cfg.Defaults[0].Kind != pipeline.KindStep || cfg.Defaults[0].Request != pipeline.SeedStepResources[pipeline.KindStep] ||
		cfg.Defaults[0].UpdatedBy != "seed" {
		t.Fatalf("seeded defaults: %+v", cfg.Defaults)
	}
	if cfg.Limits.MemoryPerThread != "1Gi" || cfg.Limits.MaxMemory != "" || cfg.Limits.MaxCPU != "" {
		t.Fatalf("seeded limits: %+v", cfg.Limits)
	}
	create(t, e, linear, Trigger{Event: "manual"})
	c := firstContainer(t, take(t, e))
	// What a step gets when it asks for nothing: promised 1 CPU and 2 GiB,
	// and may use 4 CPUs and 6 GiB, the memory of a build, not the
	// namespace's 2 GiB. Four parallel jobs fit in it.
	if c.CPU != "1" || c.CPULimit != "4" || c.Memory != "2Gi" || c.MemoryLimit != "6Gi" || c.Threads != 4 || c.ExplicitCPU || c.ExplicitMemory {
		t.Fatalf("a step that asks for nothing got %+v", c)
	}
}

// The compiled-in values are a seed only: once the row is in the database,
// the database decides, and restarting does not put the seed back.
func TestDefaultsAreTheDatabasesNotTheProgramsAfterTheSeed(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	before, after, err := e.SetStepResourceDefault(pipeline.KindStep, resources.Request{Memory: "12Gi", CPU: "6", CPULimit: "6"}, "ann")
	if err != nil || before.Memory != "2Gi" || after.Memory != "12Gi" || after.MemoryLimit != "" || after.UpdatedBy != "ann" {
		t.Fatalf("%+v %+v %v", before, after, err)
	}
	create(t, e, linear, Trigger{Event: "manual"})
	c := firstContainer(t, take(t, e))
	if c.Memory != "12Gi" || c.MemoryLimit != "12Gi" || c.CPU != "6" || c.CPULimit != "6" || c.Threads != 6 {
		t.Fatalf("the changed default was not used: %+v", c)
	}
	e.Close()

	// Another server on the same database, started later, sees it too, and
	// seeding did not undo it.
	e2 := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	defer e2.Close()
	cfg, err := e2.StepResources()
	if err != nil || cfg.Defaults[0].Memory != "12Gi" || cfg.Defaults[0].UpdatedBy != "ann" {
		t.Fatalf("%+v %v", cfg, err)
	}
	// An administrator who clears a default means it: no seed comes back.
	if _, _, err := e2.SetStepResourceDefault(pipeline.KindStep, resources.Request{}, "ann"); err != nil {
		t.Fatal(err)
	}
	e2.Close()
	e3 := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	defer e3.Close()
	create(t, e3, linear, Trigger{Event: "manual"})
	if c := firstContainer(t, take(t, e3)); c.Memory != "" || c.MemoryLimit != "" || c.CPU != "" || c.Threads != 0 {
		t.Fatalf("a cleared default must set nothing, so the namespace decides: %+v", c)
	}
}

// Only a step that runs a script has defaults: the others run in no
// container Launchway sizes.
func TestDefaultsAreForKindsThatRunInAContainer(t *testing.T) {
	e, _ := newEngine(t)
	for _, kind := range []string{"image", "deploy", "approval", "verify", "ai", "rollout", "rollback", ""} {
		_, _, err := e.SetStepResourceDefault(kind, resources.Request{Memory: "1Gi"}, "ann")
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "run in a container Launchway sizes") {
			t.Errorf("%q: %v", kind, err)
		}
	}
	for _, bad := range []resources.Request{{Memory: "lots"}, {CPU: "2Gi"}, {Memory: "8Gi", MemoryLimit: "4Gi"}} {
		if _, _, err := e.SetStepResourceDefault(pipeline.KindStep, bad, "ann"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

// A step that declares a value gets it: promised and capped at it, with the
// parallelism that fits.
func TestAStepThatDeclaresResourcesGetsThem(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, "pipeline: p\nsteps:\n  build:\n    image: golang:1.27\n    run: go build ./...\n    resources: { cpu: 16, memory: 6Gi }\n", Trigger{Event: "manual"})
	c := firstContainer(t, take(t, e))
	if c.Memory != "6Gi" || c.MemoryLimit != "6Gi" || c.CPU != "16" || c.CPULimit != "16" || !c.ExplicitCPU || !c.ExplicitMemory {
		t.Fatalf("%+v", c)
	}
	// 16 CPUs would be 16 compilations; 6 GiB feeds six.
	if c.Threads != 6 {
		t.Fatalf("threads %d: parallelism must match the memory granted", c.Threads)
	}
}

// What a step leaves out is filled in per resource: a step that writes only
// memory still gets the default's CPU, and the other way round.
func TestWhatAStepLeavesOutComesFromTheDefault(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, "pipeline: p\nsteps:\n  build: { image: x, run: a, resources: { memory: 8Gi } }\n", Trigger{Event: "manual"})
	c := firstContainer(t, take(t, e))
	if c.Memory != "8Gi" || c.MemoryLimit != "8Gi" || c.CPU != "1" || c.CPULimit != "4" || c.Threads != 4 {
		t.Fatalf("memory written, cpu from the default: %+v", c)
	}
	e2, _ := newEngine(t)
	create(t, e2, "pipeline: p\nsteps:\n  build: { image: x, run: a, resources: { cpu: 2 } }\n", Trigger{Event: "manual"})
	c = firstContainer(t, take(t, e2))
	if c.Memory != "2Gi" || c.MemoryLimit != "6Gi" || c.CPU != "2" || c.CPULimit != "2" || c.Threads != 2 {
		t.Fatalf("cpu written, memory from the default: %+v", c)
	}
}

// Steps that share an image share a container, which gets what the largest
// of them needs; another image is another container.
func TestAContainerIsSizedForTheLargestStepInIt(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, `pipeline: p
steps:
  lint: { image: golang:1.27, run: vet, resources: { cpu: 1, memory: 512Mi } }
  build: { image: golang:1.27, run: build, needs: lint, resources: { cpu: 4, memory: 6Gi } }
  ui: { image: node:24, run: npm, needs: lint, resources: { memory: 3Gi } }
`, Trigger{Event: "manual"})
	j := take(t, e) // lint
	if j.StepID != "lint" {
		t.Fatalf("%s", j.StepID)
	}
	if c := containerOf(t, j, "image-1"); c.Memory != "6Gi" || c.MemoryLimit != "6Gi" || c.CPULimit != "4" || c.Threads != 4 {
		t.Fatalf("golang: %+v", c)
	}
	if c := containerOf(t, j, "image-2"); c.Memory != "3Gi" || c.MemoryLimit != "3Gi" || c.Threads != 3 {
		t.Fatalf("node: %+v", c)
	}
}

// A container written with its own cpu and memory (which existed before)
// is passed on as it was written when no step asks for more, and counts as
// the step's default when a step asks for something else.
func TestAContainersOwnSizeStandsInForTheDefault(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, "pipeline: p\ncontainers:\n  go: { image: golang:1.27, cpu: 500m, memory: 1Gi }\nsteps:\n  a: { container: go, run: a }\n", Trigger{Event: "manual"})
	c := firstContainer(t, take(t, e))
	if c.CPU != "500m" || c.CPULimit != "" || c.Memory != "1Gi" || c.MemoryLimit != "1Gi" || !c.ExplicitCPU || !c.ExplicitMemory || c.Threads != 1 {
		t.Fatalf("as written: %+v", c)
	}
	e2, _ := newEngine(t)
	create(t, e2, "pipeline: p\ncontainers:\n  go: { image: golang:1.27, cpu: 500m, memory: 1Gi }\nsteps:\n  a: { container: go, run: a, resources: { memory: 4Gi } }\n", Trigger{Event: "manual"})
	c = firstContainer(t, take(t, e2))
	if c.Memory != "4Gi" || c.MemoryLimit != "4Gi" || c.CPU != "500m" || !c.ExplicitMemory {
		t.Fatalf("the step's memory, the container's cpu: %+v", c)
	}
}

func TestServicesAreLeftAsWritten(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, "pipeline: p\ncontainers:\n  db: { image: postgres:17, service: true, memory: 512Mi }\nsteps:\n  a: { image: x, run: a }\n", Trigger{Event: "manual"})
	j := take(t, e)
	if c := containerOf(t, j, "db"); c.Memory != "512Mi" || c.MemoryLimit != "" || c.Threads != 0 || c.ExplicitCPU || c.ExplicitMemory {
		t.Fatalf("%+v", c)
	}
}

// A step that asks for more than the installation allows is refused when the
// run is created, with a message that names the step, the value and the
// limit and says where the limit comes from.
func TestARunAskingForMoreThanTheInstallationAllowsIsRefusedBeforeItStarts(t *testing.T) {
	e, _ := newEngine(t)
	if _, _, err := e.SetStepResourceLimits(ResourceLimits{MaxMemory: "8Gi", MaxCPU: "8"}, "ann"); err != nil {
		t.Fatal(err)
	}
	p := parse(t, "pipeline: p\nsteps:\n  build:\n    image: x\n    run: a\n    resources: { memory: 12Gi }\n  ok: { image: x, run: b, resources: { memory: 8Gi, cpu: 8 } }\n")
	_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}})
	var ps pipeline.Problems
	if !errors.As(err, &ps) || len(ps) != 1 {
		t.Fatalf("want one problem, got %v", err)
	}
	for _, want := range []string{"step build", "memory 12Gi", "max_memory 8Gi", "the installation's step resource limits", "step-resources set-limits"} {
		if !strings.Contains(ps[0].Message, want) {
			t.Errorf("the message does not say %q: %s", want, ps[0].Message)
		}
	}
	if ps[0].Path != "steps.build.resources.memory" || ps[0].Line == 0 {
		t.Errorf("where: %+v", ps[0])
	}
	// Nothing was created.
	if runs, _ := e.ListRuns(RunFilter{}); len(runs) != 0 {
		t.Fatalf("%d runs were created", len(runs))
	}
	// A container's own memory is held to the same limit.
	p = parse(t, "pipeline: p\ncontainers:\n  big: { image: x, memory: 16Gi }\nsteps:\n  a: { container: big, run: a }\n")
	_, err = e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}})
	if !errors.As(err, &ps) || !strings.Contains(ps[0].Message, "container big asks for memory 16Gi") {
		t.Fatalf("%v", err)
	}
	// At the limit is allowed.
	create(t, e, "pipeline: p\nsteps:\n  a: { image: x, run: b, resources: { memory: 8Gi, cpu: 8 } }\n", Trigger{Event: "manual"})
}

// Limits lowered after a run was created still hold: a step that is not
// yet queued and now asks for too much is failed before any runner sees it,
// with a message that names the limit.
func TestLimitsLoweredAfterTheRunWasCreatedStillHold(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: p\nsteps:\n  a: { image: x, run: a }\n  b: { image: x, run: b, needs: a, resources: { memory: 6Gi } }\n", Trigger{Event: "manual"})
	j := take(t, e) // a is running; b waits for it
	if _, _, err := e.SetStepResourceDefault(pipeline.KindStep, resources.Request{Memory: "2Gi"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.SetStepResourceLimits(ResourceLimits{MaxMemory: "4Gi"}, "ann"); err != nil {
		t.Fatal(err)
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "run", "failed", "a", "succeeded", "b", "failed")
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.StepList {
		if s.ID == "b" {
			for _, w := range []string{"step b asks for memory 6Gi", "max_memory 4Gi", "step resource limits"} {
				if !strings.Contains(s.Error, w) {
					t.Errorf("the step's error does not say %q: %s", w, s.Error)
				}
			}
		}
	}
	if j, err := e.Lease(context.Background(), runner, 0); err != nil || j != nil {
		t.Fatalf("a runner was offered the step: %+v %v", j, err)
	}
}

// The configuration is consistent: a default may not be above a limit, nor a
// limit below a default.
func TestDefaultsAndLimitsAreHeldConsistent(t *testing.T) {
	e, _ := newEngine(t)
	if _, _, err := e.SetStepResourceLimits(ResourceLimits{MaxMemory: "4Gi"}, "ann"); err == nil ||
		!strings.Contains(err.Error(), "step default") || !strings.Contains(err.Error(), "max_memory 4Gi") || !strings.Contains(err.Error(), "lower the default first") {
		t.Fatalf("%v", err)
	}
	if _, _, err := e.SetStepResourceLimits(ResourceLimits{MaxMemory: "8Gi", MemoryPerThread: "10Mi"}, "ann"); err == nil || !strings.Contains(err.Error(), "memory_per_thread") {
		t.Fatalf("%v", err)
	}
	before, after, err := e.SetStepResourceLimits(ResourceLimits{MaxMemory: "8192Mi", MaxCPU: "8", MemoryPerThread: "2Gi"}, "ann")
	if err != nil || before.MaxMemory != "" || after.MaxMemory != "8Gi" || after.MaxCPU != "8" || after.MemoryPerThread != "2Gi" {
		t.Fatalf("%+v %+v %v", before, after, err)
	}
	if _, _, err := e.SetStepResourceDefault(pipeline.KindStep, resources.Request{Memory: "9Gi"}, "ann"); err == nil || !strings.Contains(err.Error(), "max_memory 8Gi") {
		t.Fatalf("%v", err)
	}
	// memory_per_thread feeds the parallelism: 6 GiB at 2 GiB each is 3.
	create(t, e, linear, Trigger{Event: "manual"})
	if c := firstContainer(t, take(t, e)); c.Threads != 3 {
		t.Fatalf("%+v", c)
	}
}

// A build that knows only the schema before step resources has no tables
// and sizes nothing: its runs are as they were.
func TestABuildWithoutStepResourcesSizesNothing(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, schemaPools, &syncBuffer{})
	defer e.Close()
	if _, err := e.StepResources(); !errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
	create(t, e, linear, Trigger{Event: "manual"})
	if c := firstContainer(t, take(t, e)); c.Memory != "" || c.Threads != 0 {
		t.Fatalf("%+v", c)
	}
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, "pipeline: p\nsteps:\n  a: { image: x, run: a, resources: { memory: 1Gi } }\n"),
		Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}}); err == nil || !strings.Contains(err.Error(), "does not know step resources") {
		t.Fatalf("%v", err)
	}
}

// An upgrade must not change existing pipelines: an installation that
// already has runs when it first finds the tables empty gets every kind
// seeded with nothing, so no step is sized; the administrator opts in with
// the recommended values.
func TestUpgradedInstallationIsSeededWithNothingSized(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	create(t, e, linear, Trigger{Event: "manual"}) // a run from before the upgrade
	// The state an upgrade finds: schema 22's tables, empty.
	for _, q := range []string{`DELETE FROM step_resource_defaults`, `DELETE FROM step_resource_limits`} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.Close()

	e2 := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	defer e2.Close()
	cfg, err := e2.StepResources()
	if err != nil || len(cfg.Defaults) != 1 {
		t.Fatalf("%+v %v", cfg, err)
	}
	if d := cfg.Defaults[0]; d.Kind != pipeline.KindStep || !d.Request.IsZero() || d.UpdatedBy != "upgrade" {
		t.Fatalf("an upgraded installation must start with nothing sized: %+v", d)
	}
	take(t, e2) // the old run's step, before a new one
	create(t, e2, linear, Trigger{Event: "manual"})
	j, err := e2.Lease(context.Background(), runner, 0)
	if err != nil || j == nil {
		t.Fatalf("%v %v", j, err)
	}
	if c := firstContainer(t, j); c.CPU != "" || c.CPULimit != "" || c.Memory != "" || c.MemoryLimit != "" || c.Threads != 0 || c.ExplicitCPU || c.ExplicitMemory {
		t.Fatalf("a step of an upgraded installation was sized: %+v", c)
	}

	// Opting in: the recommended values are what a fresh installation has.
	rec, err := e2.RecommendedStepResource(pipeline.KindStep)
	if err != nil || rec != pipeline.SeedStepResources[pipeline.KindStep] {
		t.Fatalf("%+v %v", rec, err)
	}
	if _, after, err := e2.SetStepResourceDefault(pipeline.KindStep, rec, "ann"); err != nil || after.Memory != "2Gi" || after.MemoryLimit != "6Gi" {
		t.Fatalf("%+v %v", after, err)
	}
	if _, err := e2.RecommendedStepResource("deploy"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a kind with no default: %v", err)
	}
}

// The runner is told which of a container's CPU and memory a step asked for,
// so that it fails the step for those and only lowers the defaults.
func TestContainersSayWhichOfCPUAndMemoryWereAskedFor(t *testing.T) {
	for _, c := range []struct {
		name, src   string
		cpu, memory bool
	}{
		{"nothing", "pipeline: p\nsteps:\n  a: { image: x, run: a }\n", false, false},
		{"memory", "pipeline: p\nsteps:\n  a: { image: x, run: a, resources: { memory: 3Gi } }\n", false, true},
		{"cpu", "pipeline: p\nsteps:\n  a: { image: x, run: a, resources: { cpu_limit: 2 } }\n", true, false},
		{"both", "pipeline: p\nsteps:\n  a: { image: x, run: a, resources: { cpu: 1, memory_limit: 3Gi } }\n", true, true},
		{"the container's own", "pipeline: p\ncontainers:\n  c: { image: x, memory: 1Gi }\nsteps:\n  a: { container: c, run: a }\n", false, true},
	} {
		e, _ := newEngine(t)
		create(t, e, c.src, Trigger{Event: "manual"})
		ct := firstContainer(t, take(t, e))
		if ct.ExplicitCPU != c.cpu || ct.ExplicitMemory != c.memory {
			t.Errorf("%s: explicit cpu %v memory %v, want %v %v", c.name, ct.ExplicitCPU, ct.ExplicitMemory, c.cpu, c.memory)
		}
	}
}

// A container is shared by the steps that use its image; whether a step
// asked for resources is the step's own, so that the host executor refuses
// only a step that asked.
func TestJobSaysWhetherThisStepAskedForResources(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, "pipeline: p\nsteps:\n  a: { image: x, run: a }\n  b: { image: x, run: b, needs: a, resources: { memory: 3Gi } }\n", Trigger{Event: "manual"})
	j := take(t, e)
	if j.StepID != "a" || j.ResourcesAsked {
		t.Fatalf("%s asked: %v", j.StepID, j.ResourcesAsked)
	}
	// Both run in the one container, which holds b's 3Gi.
	if c := firstContainer(t, j); c.Memory != "3Gi" || !c.ExplicitMemory {
		t.Fatalf("%+v", c)
	}
	ok(t, e, j, nil)
	if j = take(t, e); j.StepID != "b" || !j.ResourcesAsked {
		t.Fatalf("%s asked: %v", j.StepID, j.ResourcesAsked)
	}
}

// A container's own cpu and memory (written under containers, which existed
// before step resources) are held to limits lowered after the run was
// created, as a step's resources are, and a default never fails a step.
func TestLimitsLoweredAfterTheRunWasCreatedHoldAContainersOwnSizeToo(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: p\ncontainers:\n  big: { image: x, memory: 6Gi }\nsteps:\n  a: { image: y, run: a }\n  b: { container: big, run: b, needs: a }\n", Trigger{Event: "manual"})
	j := take(t, e)
	// The recommended defaults are 6Gi at most; a limit below them cannot be set, so lower the default first.
	if _, _, err := e.SetStepResourceDefault(pipeline.KindStep, resources.Request{Memory: "1Gi"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.SetStepResourceLimits(ResourceLimits{MaxMemory: "4Gi"}, "ann"); err != nil {
		t.Fatal(err)
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "run", "failed", "a", "succeeded", "b", "failed")
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.StepList {
		switch s.ID {
		case "b":
			if !strings.Contains(s.Error, "container big asks for memory 6Gi") || !strings.Contains(s.Error, "max_memory 4Gi") {
				t.Errorf("b: %s", s.Error)
			}
		case "a":
			if s.Error != "" {
				t.Errorf("a step that asked for nothing failed on a default: %s", s.Error)
			}
		}
	}
}
