package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const inputsPipeline = `
pipeline: demo
containers:
  tools:
    image: golang:1.27
    env: {GOFLAGS: -mod=mod, SHARED: from-container}
steps:
  build:
    container: tools
    run: go build
  test:
    container: tools
    needs: build
    env: {SHARED: from-step, BIG: "%s"}
    secrets:
      TOKEN: {store: vault, key: ci/registry, field: token}
    run: go test ./...
`

func findVar(vars []InputVar, name string) (InputVar, bool) {
	for _, v := range vars {
		if v.Name == name {
			return v, true
		}
	}
	return InputVar{}, false
}

// Each attempt records what it was given, with where each value came from,
// in its step.leased event and in step_inputs; secrets by reference only.
func TestInputsAreRecordedPerAttempt(t *testing.T) {
	e, _ := newEngine(t)
	big := strings.Repeat("x", MaxInputValueBytes+100)
	id := create(t, e, strings.Replace(inputsPipeline, "%s", big, 1), Trigger{Event: "manual", Branch: "main", SHA: "abc123"})
	ok(t, e, take(t, e), map[string]string{"binary": "out/app"})

	j := take(t, e)
	if j.Env["BIG"] != big {
		t.Fatal("the runner must receive the value in full")
	}
	fail(t, e, j) // no retry: one attempt
	if err := e.Resume(id, "ann"); err != nil {
		t.Fatal(err)
	}
	// Handed back before it started, and leased again as the same attempt:
	// recorded once.
	j, err := e.Lease(context.Background(), runner, 0)
	if err != nil || j == nil {
		t.Fatalf("lease: %v, %v", j, err)
	}
	if err := e.Release(j.LeaseID); err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), nil)

	s := stepOf(t, e, id, "test")
	if len(s.Inputs) != 2 || s.Inputs[0].Attempt != 1 || s.Inputs[1].Attempt != 2 {
		t.Fatalf("attempts recorded: %+v", s.Inputs)
	}
	in := s.Inputs[1].Inputs
	if in.Image != "golang:1.27" || in.Container != "tools" || in.Script != "go test ./..." || in.Library != nil {
		t.Fatalf("inputs: %+v", in)
	}
	for name, want := range map[string]InputVar{
		"SHARED":                       {Value: "from-step", From: FromStep},
		"GOFLAGS":                      {Value: "-mod=mod", From: FromContainer},
		"LAUNCHWAY_NEEDS_BUILD_BINARY": {Value: "out/app", From: FromNeeds, Step: "build", Output: "binary"},
		"LAUNCHWAY_ATTEMPT":            {Value: "2", From: FromLaunchway},
		"LAUNCHWAY_COMMIT":             {Value: "abc123", From: FromLaunchway},
	} {
		got, found := findVar(in.Env, name)
		want.Name = name
		if !found || got != want {
			t.Errorf("%s: %+v, want %+v", name, got, want)
		}
	}
	if v, _ := findVar(in.Env, "BIG"); len(v.Value) != MaxInputValueBytes || !v.Truncated || !in.Truncated {
		t.Errorf("BIG kept %d bytes, truncated %v", len(v.Value), v.Truncated)
	}
	if _, found := findVar(in.Env, "TOKEN"); found {
		t.Error("a secret is listed as a variable")
	}
	if len(in.Secrets) != 1 || in.Secrets[0] != (InputSecret{Name: "TOKEN", Ref: "vault:ci/registry#token"}) {
		t.Errorf("secrets: %+v", in.Secrets)
	}

	// The event log carries the same, as the record; the table is derived.
	evs, _ := e.Events(id)
	var fromEvent *StepInputs
	for _, ev := range evs {
		if ev.Type == StepLeased && ev.Data.Step == "test" && ev.Data.Attempt == 2 {
			fromEvent = ev.Data.Inputs
		}
	}
	a, _ := json.Marshal(fromEvent)
	b, _ := json.Marshal(in)
	if fromEvent == nil || string(a) != string(b) {
		t.Fatalf("the event and the table disagree:\n%s\n%s", a, b)
	}
	var rows int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM step_inputs WHERE run_id = ? AND step_id = 'test' AND attempt = 2 AND kind = 'secret'
		AND value = 'vault:ci/registry#token'`, id).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("searching step_inputs: %d, %v", rows, err)
	}
}

// Everything an attempt records together stays within MaxInputsBytes.
func TestInputsSizeCap(t *testing.T) {
	in := &StepInputs{Script: strings.Repeat("s", MaxInputScriptBytes*2)}
	for i := range 40 {
		in.Env = append(in.Env, InputVar{Name: "V" + strings.Repeat("_", i), Value: strings.Repeat("v", MaxInputValueBytes)})
	}
	capInputs(in)
	total := len(in.Script)
	for _, v := range in.Env {
		total += len(v.Value)
	}
	if !in.ScriptTruncated || len(in.Script) != MaxInputScriptBytes || total > MaxInputsBytes || !in.Truncated {
		t.Fatalf("script %d (truncated %v), total %d", len(in.Script), in.ScriptTruncated, total)
	}
	if last := in.Env[len(in.Env)-1]; last.Value != "" || !last.Truncated {
		t.Fatalf("the last value, past the limit: %d bytes, truncated %v", len(last.Value), last.Truncated)
	}
	// Cutting never splits a character.
	in = &StepInputs{Env: []InputVar{{Name: "U", Value: strings.Repeat("é", MaxInputValueBytes)}}}
	capInputs(in)
	if v := in.Env[0].Value; len(v) > MaxInputValueBytes || !strings.HasSuffix(v, "é") {
		t.Fatalf("cut %d bytes, ends %q", len(v), v[len(v)-2:])
	}
}

// A draft whose steps may not read secrets records none: such a step is
// never leased, and a step without secrets lists none.
func TestDraftInputsHaveNoSecrets(t *testing.T) {
	e, _ := newEngine(t)
	id := createDraft(t, e, draftText, false)
	for j := leaseAs(t, e, runner); j != nil; j = leaseAs(t, e, runner) {
		ok(t, e, j, nil)
	}
	for _, s := range []string{"build", "draft-check"} {
		in := stepOf(t, e, id, s).Inputs
		if len(in) != 1 || len(in[0].Inputs.Secrets) != 0 {
			t.Fatalf("%s: %+v", s, in)
		}
	}
	if in := stepOf(t, e, id, "publish").Inputs; len(in) != 0 {
		t.Fatalf("publish was never leased, yet has inputs: %+v", in)
	}
}
