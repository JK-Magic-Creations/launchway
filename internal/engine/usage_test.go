package engine

import "testing"

// What a step attempt used is kept with the attempt and shown on the run:
// the numbers when they were measured, "not measured" when they were not (no
// guess), and nothing for an attempt that did not run.
func TestStepUsageIsKeptAndShown(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n  b: {image: x, run: y, needs: a}\n", Trigger{})
	j := take(t, e)
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Usage: &StepUsage{Measured: true, Method: "cgroup2",
		MemoryPeak: 1181116006, CPUPeakMilli: 1900, CPUMillis: 42000, ReservedMemory: 4 << 30, ReservedCPUMilli: 2000}}); err != nil {
		t.Fatal(err)
	}
	j2 := take(t, e)
	if err := e.Finish(j2.LeaseID, Result{Outcome: "succeeded", Usage: &StepUsage{Measured: false, Note: "no cgroup", MemoryPeak: 99}}); err != nil {
		t.Fatal(err)
	}
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	a, b := d.StepList[0].Usage, d.StepList[1].Usage
	if a == nil || !a.Measured || a.MemoryPeak != 1181116006 || a.ReservedMemory != 4<<30 || a.CPUPeakMilli != 1900 || a.Method != "cgroup2" || a.Attempt != 1 {
		t.Fatalf("step a: %+v", a)
	}
	if b == nil || b.Measured || b.MemoryPeak != 0 || b.Note != "no cgroup" {
		t.Fatalf("step b: %+v (an unmeasured step must carry no numbers)", b)
	}
	sv, err := e.stepView(id, d.StepList[0].ID)
	if err != nil || sv.Usage == nil || sv.Usage.MemoryPeak != 1181116006 {
		t.Fatalf("live view: %+v %v", sv, err)
	}
}
