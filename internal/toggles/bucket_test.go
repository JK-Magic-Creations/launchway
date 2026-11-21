package toggles

import (
	"fmt"
	"testing"
)

// The same salt and value land in the same bucket every time, and the buckets
// are spread evenly: over 100000 keys each tenth holds 10% within a small margin.
func TestBucketingIsStableAndEven(t *testing.T) {
	const n = 100000
	var tenths [10]int
	for i := 0; i < n; i++ {
		v := fmt.Sprintf("user-%d", i)
		b := Bucket("checkout", v)
		if b != Bucket("checkout", v) || b < 0 || b >= BucketCount {
			t.Fatalf("bucket of %s: %d", v, b)
		}
		tenths[b/1000]++
	}
	for i, c := range tenths {
		if c < n/10*95/100 || c > n/10*105/100 {
			t.Errorf("tenth %d holds %d of %d", i, c, n)
		}
	}
	if Bucket("a", "x") == Bucket("b", "x") && Bucket("a", "y") == Bucket("b", "y") && Bucket("a", "z") == Bucket("b", "z") {
		t.Error("two salts bucket alike")
	}
}

// Raising a rollout keeps everyone who was in: 5% is inside 25% is inside 50%,
// and the share that gets the value is close to what was asked for.
func TestRolloutGrowthKeepsEveryoneIn(t *testing.T) {
	mk := func(pct int) *Ruleset {
		return &Ruleset{Toggles: map[string]Flag{"f": {Key: "f", Type: Boolean, Salt: "f", State: State{On: true, OffValue: false,
			Fallthrough: Serve{Rollout: &Rollout{Buckets: []Share{{Value: true, Weight: pct * 100}, {Value: false, Weight: 10000 - pct*100}}}}}}}}
	}
	steps := []int{5, 25, 50, 100}
	in := map[int]map[string]bool{}
	for _, p := range steps {
		in[p] = map[string]bool{}
		rs := mk(p)
		for i := 0; i < 20000; i++ {
			k := fmt.Sprintf("u%d", i)
			if Evaluate(rs, "f", Context{"key": k}).Value == true {
				in[p][k] = true
			}
		}
		if got := float64(len(in[p])) / 200; got < float64(p)-1.5 || got > float64(p)+1.5 {
			t.Errorf("%d%% rollout reached %.2f%%", p, got)
		}
	}
	for i := 1; i < len(steps); i++ {
		for k := range in[steps[i-1]] {
			if !in[steps[i]][k] {
				t.Fatalf("%s was in at %d%% and out at %d%%", k, steps[i-1], steps[i])
			}
		}
	}
}

func TestValidationRefusesWhatCannotWork(t *testing.T) {
	ok := State{On: true, OffValue: false, Fallthrough: Serve{Value: true}}
	if err := CheckState(Boolean, &ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]State{
		"a string for a boolean":     {OffValue: "x", Fallthrough: Serve{Value: true}},
		"weights that do not add up": {OffValue: false, Fallthrough: Serve{Rollout: &Rollout{Buckets: []Share{{Value: true, Weight: 10}, {Value: false, Weight: 10}}}}},
		"a value and a rollout":      {OffValue: false, Fallthrough: Serve{Value: true, Rollout: &Rollout{Buckets: []Share{{Value: true, Weight: 10000}}}}},
		"an unknown operator":        {OffValue: false, Fallthrough: Serve{Value: true}, Rules: []Rule{{Clauses: []Clause{{Attribute: "a", Op: "matches", Values: []any{"x"}}}, Serve: Serve{Value: true}}}},
		"semver with text":           {OffValue: false, Fallthrough: Serve{Value: true}, Rules: []Rule{{Clauses: []Clause{{Attribute: "a", Op: OpSemverGt, Values: []any{"latest"}}}, Serve: Serve{Value: true}}}},
		"no attribute":               {OffValue: false, Fallthrough: Serve{Value: true}, Rules: []Rule{{Clauses: []Clause{{Op: OpEquals, Values: []any{"x"}}}, Serve: Serve{Value: true}}}},
		"two rules, one id":          {OffValue: false, Fallthrough: Serve{Value: true}, Rules: []Rule{{ID: "x", Serve: Serve{Value: true}}, {ID: "x", Serve: Serve{Value: true}}}},
	}
	for name, st := range bad {
		if err := CheckState(Boolean, &st); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	st := State{OffValue: false, Fallthrough: Serve{Value: true}, Rules: []Rule{{ID: "r1", Serve: Serve{Value: true}}, {Serve: Serve{Value: true}}, {Serve: Serve{Value: true}}}}
	if err := CheckState(Boolean, &st); err != nil || st.Rules[1].ID != "r2" || st.Rules[2].ID != "r3" {
		t.Fatalf("ids: %+v %v", st.Rules, err)
	}
	if err := CheckKey("Checkout"); err == nil {
		t.Error("an upper-case key was accepted")
	}
	if err := CheckKey("checkout-v2.new_ui"); err != nil {
		t.Error(err)
	}
}

func TestPrerequisiteLoops(t *testing.T) {
	flags := map[string]Flag{
		"a": {State: State{Prerequisites: []Prerequisite{{Toggle: "b", Value: true}}}},
		"b": {State: State{Prerequisites: []Prerequisite{{Toggle: "c", Value: true}}}},
		"c": {},
	}
	if l := PrerequisiteLoop(flags); l != nil {
		t.Fatalf("a chain is not a loop: %v", l)
	}
	flags["c"] = Flag{State: State{Prerequisites: []Prerequisite{{Toggle: "a", Value: true}}}}
	if l := PrerequisiteLoop(flags); len(l) != 4 {
		t.Fatalf("loop: %v", l)
	}
}

func TestSettled(t *testing.T) {
	if v, ok := Settled(State{On: true, Fallthrough: Serve{Value: true}}); !ok || v != "true" {
		t.Errorf("%q %v", v, ok)
	}
	if _, ok := Settled(State{On: true, Fallthrough: Serve{Rollout: &Rollout{}}}); ok {
		t.Error("a rollout is not settled")
	}
	if _, ok := Settled(State{On: false}); ok {
		t.Error("off is not settled")
	}
}
