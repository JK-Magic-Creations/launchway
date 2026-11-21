package openfeature_test

import (
	"context"
	"testing"

	of "github.com/open-feature/go-sdk/openfeature"

	toggles "github.com/jk-magic-creations/launchway/sdk/go/toggles"
	lw "github.com/jk-magic-creations/launchway/sdk/go/toggles/openfeature"
)

func TestProviderThroughTheOpenFeatureClient(t *testing.T) {
	rs := toggles.Ruleset{Schema: 1, Environment: "x", Toggles: map[string]toggles.Flag{
		"checkout": {Key: "checkout", Type: toggles.Boolean, Salt: "checkout", State: toggles.State{On: true, OffValue: false, Fallthrough: toggles.Serve{Value: false},
			Rules: []toggles.Rule{{ID: "pro", Clauses: []toggles.Clause{{Attribute: "plan", Op: toggles.OpEquals, Values: []any{"pro"}}}, Serve: toggles.Serve{Value: true}}}}},
		"limit":   {Key: "limit", Type: toggles.Number, Salt: "limit", State: toggles.State{On: true, Fallthrough: toggles.Serve{Value: 5.0}}},
		"rollout": {Key: "rollout", Type: toggles.Boolean, Salt: "rollout", State: toggles.State{On: true, OffValue: false, Fallthrough: toggles.Serve{Rollout: &toggles.Rollout{Buckets: []toggles.Share{{Value: true, Weight: 5000}, {Value: false, Weight: 5000}}}}}},
	}}
	if err := of.SetProviderAndWait(lw.New(toggles.NewFromRuleset(rs))); err != nil {
		t.Fatal(err)
	}
	c := of.NewClient("test")
	ctx := context.Background()
	if v, err := c.BooleanValue(ctx, "checkout", false, of.NewEvaluationContext("u1", map[string]any{"plan": "pro"})); err != nil || !v {
		t.Fatalf("%v %v", v, err)
	}
	d, _ := c.BooleanValueDetails(ctx, "checkout", true, of.NewEvaluationContext("u1", map[string]any{"plan": "free"}))
	if d.Value || d.Reason != of.DefaultReason {
		t.Fatalf("%+v", d)
	}
	if n, err := c.IntValue(ctx, "limit", 1, of.NewEvaluationContext("u1", nil)); err != nil || n != 5 {
		t.Fatalf("%v %v", n, err)
	}
	if v, err := c.BooleanValue(ctx, "nope", true, of.NewEvaluationContext("u1", nil)); err == nil || v != true {
		t.Fatalf("an unknown toggle should give the default and an error: %v %v", v, err)
	}
	if _, err := c.StringValue(ctx, "checkout", "d", of.NewEvaluationContext("u1", nil)); err == nil {
		t.Fatal("a type mismatch should be an error")
	}
	if _, err := c.BooleanValue(ctx, "rollout", false, of.NewEvaluationContext("", nil)); err == nil {
		t.Fatal("a rollout with no targeting key should be an error")
	}
}
