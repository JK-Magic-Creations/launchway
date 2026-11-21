// Package toggletest helps test code that reads toggles: a client whose toggles
// have the values a test sets, with no server and no network.
package toggletest

import (
	"fmt"

	toggles "github.com/jk-magic-creations/launchway/sdk/go/toggles"
)

// Values makes a client in which each key serves the given value to everyone:
// a bool, string, number (any Go number type) or anything else, which is a json toggle.
// The type of each toggle follows its value.
func Values(values map[string]any) *toggles.Client {
	rs := toggles.Ruleset{Schema: toggles.SchemaVersion, Environment: "test", Toggles: map[string]toggles.Flag{}}
	for k, v := range values {
		rs.Toggles[k] = toggles.Flag{Key: k, Type: typeOf(v), Salt: k, State: toggles.State{On: true, OffValue: v, Fallthrough: toggles.Serve{Value: v}}}
	}
	return toggles.NewFromRuleset(rs)
}

func typeOf(v any) toggles.ValueType {
	switch toggles.Normalize(v).(type) {
	case bool:
		return toggles.Boolean
	case string:
		return toggles.String
	case float64:
		return toggles.Number
	}
	return toggles.JSON
}

// Rules makes a client from a full ruleset, for a test of the rules themselves.
func Rules(rs toggles.Ruleset) *toggles.Client { return toggles.NewFromRuleset(rs) }

// MustBe fails the test with a message when a boolean toggle does not serve want.
func MustBe(t interface{ Fatalf(string, ...any) }, c *toggles.Client, key string, ctx toggles.Context, want bool) {
	if got := c.BoolDetail(key, ctx, !want); got.Value != want {
		t.Fatalf("toggle %s: got %v (%s), want %v", key, got.Value, got.Reason, want)
	}
}

var _ = fmt.Sprint
