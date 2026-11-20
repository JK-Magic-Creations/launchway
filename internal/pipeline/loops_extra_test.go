package pipeline

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func loopExtraSrc(extra string) string {
	return `
pipeline: matrix
inputs:
  systems: { type: list, default: [debian, alpine] }
steps:
  test:
    for_each:
      python: ["3.12", "3.13"]
      os: [debian, alpine]
    exclude: [ { python: "3.13", os: alpine } ]
` + extra + `
    image: alpine:3.22
    run: pytest
  report:
    needs: test
    image: alpine:3.22
    run: echo done
`
}

func bindLoop(t *testing.T, src string) *Pipeline {
	t.Helper()
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vals, _ := ResolveInputs(p, nil)
	b, err := Bind(p, vals, BindOptions{})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	return b
}

// add appends combinations after the cross product, in the order written, and
// continue_for makes the selected members tolerate a failure; the ids of the
// members the cross product makes do not change when add is written.
func TestLoopAddAndContinueFor(t *testing.T) {
	plain := bindLoop(t, loopExtraSrc(""))
	b := bindLoop(t, loopExtraSrc(`    add: [ { python: "3.14", os: debian }, { python: "3.14", os: alpine }, { python: "3.13", os: alpine2 } ]
    continue_for: [ { python: "3.14" } ]`))
	var ids []string
	for _, s := range b.Steps {
		ids = append(ids, s.ID)
	}
	want := []string{
		"test[python=3.12,os=debian]", "test[python=3.12,os=alpine]", "test[python=3.13,os=debian]",
		"test[python=3.14,os=debian]", "test[python=3.14,os=alpine]", "test[python=3.13,os=alpine2]", "report",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("steps: %v", ids)
	}
	for i, s := range plain.Steps[:3] {
		if s.ID != b.Steps[i].ID {
			t.Fatalf("an existing member's id changed: %s", s.ID)
		}
	}
	if got := b.Step("report").Needs; !reflect.DeepEqual(got, want[:6]) {
		t.Fatalf("report needs every member, added ones included: %v", got)
	}
	var extra, allowed []string
	for _, m := range b.Loops[0].Members {
		if m.Extra {
			extra = append(extra, m.ID)
		}
		if m.AllowedFailure {
			allowed = append(allowed, m.ID)
		}
	}
	if !reflect.DeepEqual(extra, want[3:6]) || !reflect.DeepEqual(allowed, want[3:5]) {
		t.Fatalf("extra %v, allowed %v", extra, allowed)
	}
	if b.Step(want[3]).OnFailure != OnFailureContinue || b.Step(want[0]).OnFailure != "" || b.Step(want[5]).OnFailure != "" {
		t.Fatal("only the members continue_for selects tolerate a failure")
	}
	if b.Step(want[3]).Each["python"] != "3.14" {
		t.Fatalf("an added member has its values: %v", b.Step(want[3]).Each)
	}
	if !b.UsesSchemaFlow() || b.Requires < SchemaFlow {
		t.Fatalf("a tolerated member needs schema %d: %d", SchemaFlow, b.Requires)
	}
	// The graph reads back the same, and is deterministic.
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(b)), &back); err != nil || mustJSON(&back) != mustJSON(b) {
		t.Fatalf("round trip: %v", err)
	}
	if again := bindLoop(t, loopExtraSrc(`    add: [ { python: "3.14", os: debian }, { python: "3.14", os: alpine }, { python: "3.13", os: alpine2 } ]
    continue_for: [ { python: "3.14" } ]`)); mustJSON(again) != mustJSON(b) {
		t.Fatal("expansion is not deterministic")
	}
	// A loop without them has no new fields in its JSON.
	if strings.Contains(mustJSON(plain), "extra") || strings.Contains(mustJSON(plain), "allowed_failure") {
		t.Fatal("old loops gained fields")
	}
}

func TestLoopAddAndContinueForMistakes(t *testing.T) {
	cases := []struct{ name, extra, want string }{
		{"missing key", "    add: [ { python: \"3.14\" } ]", "names every key of the loop (python, os); this one lacks os"},
		{"unknown key", "    add: [ { python: \"3.14\", os: debian, arch: x } ]", "arch"},
		{"in the cross product", "    add: [ { python: \"3.12\", os: debian } ]", "already a combination of the lists"},
		{"excluded", "    add: [ { python: \"3.13\", os: alpine } ]", "left out by exclude"},
		{"twice", "    add: [ { python: \"3.14\", os: debian }, { python: \"3.14\", os: debian } ]", "added twice"},
		{"bad value", "    add: [ { python: \"3 14\", os: debian } ]", "cannot be a loop value"},
		{"empty", "    add: []", "list of combinations"},
		{"continue_for unknown key", "    continue_for: [ { arch: x } ]", "arch"},
		{"continue_for matches nothing", "    continue_for: [ { python: \"2.7\" } ]", "matches no member"},
		{"continue_for empty entry", "    continue_for: [ {} ]", "names at least one of python, os"},
		{"without for_each", "", ""},
	}
	for _, c := range cases {
		if c.want == "" {
			_, err := Parse([]byte("pipeline: x\nsteps:\n  a:\n    add: [ { v: \"1\" } ]\n    image: x\n    run: y\n"))
			if err == nil || !strings.Contains(err.Error(), "add goes with for_each") {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		_, err := Parse([]byte(loopExtraSrc(c.extra)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

// With a list from a run input the mistakes are found when the run is made,
// on the loop's entries.
func TestLoopExtraMistakesWithInputLists(t *testing.T) {
	src := `
pipeline: m
inputs:
  systems: { type: list, default: [debian, alpine] }
steps:
  test:
    for_each: { os: { input: systems } }
    add: [ { os: debian } ]
    continue_for: [ { os: nothing } ]
    image: alpine:3.22
    run: x
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := ResolveInputs(p, nil)
	_, err = Bind(p, vals, BindOptions{})
	if err == nil || !strings.Contains(err.Error(), "already a combination") || !strings.Contains(err.Error(), "matches no member") {
		t.Fatalf("%v", err)
	}
	// Added members count toward the limit.
	p2, _ := Parse([]byte(strings.Replace(strings.Replace(src, "add: [ { os: debian } ]", "add: [ { os: arch }, { os: fedora } ]", 1), "continue_for: [ { os: nothing } ]", "", 1)))
	if _, err := Bind(p2, vals, BindOptions{MaxLoopMembers: 3}); err == nil || !strings.Contains(err.Error(), "allows at most 3") {
		t.Fatalf("limit: %v", err)
	}
}

const extraTemplate = `
template: build
params:
  arch: { type: string }
  oses: { type: list, default: [linux] }
steps:
  compile:
    for_each: { os: { param: oses } }
    add: [ { os: plan9 } ]
    continue_for: [ { os: plan9 } ]
    image: golang:1.24
    run: go build
`

// Works in templates, and a member of a loop over a template passes the
// tolerance to the template's steps.
func TestLoopExtraInTemplates(t *testing.T) {
	src := `
pipeline: lib-loop
libraries: { platform: { repo: lib, version: v1 } }
steps:
  build:
    for_each: { arch: [amd64, arm64] }
    continue_for: [ { arch: arm64 } ]
    uses: platform/build
    with: { arch: { each: arch } }
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := ResolveInputs(p, nil)
	b, err := Bind(p, vals, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range b.Libraries {
		l.Commit = testCommit
	}
	x, err := Expand(b, func(*Library, string) ([]byte, error) { return []byte(extraTemplate), nil })
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"build[arch=amd64]/compile[os=linux]": "", "build[arch=amd64]/compile[os=plan9]": OnFailureContinue,
		"build[arch=arm64]/compile[os=linux]": OnFailureContinue, "build[arch=arm64]/compile[os=plan9]": OnFailureContinue,
	} {
		s := x.Step(id)
		if s == nil || s.OnFailure != want {
			t.Errorf("%s: %+v", id, s)
		}
	}
	if x.Requires < SchemaFlow {
		t.Fatalf("requires %d", x.Requires)
	}
}

const fetchLoopSrc = `
pipeline: fetch
steps:
  build:
    for_each: { os: [linux, mac], arch: [amd64, arm64] }
    exclude: [ { os: mac, arch: arm64 } ]
    image: x
    run: make
    artifacts: [{name: binary, paths: [bin/app]}]
  package:
    needs: build
    image: x
    run: tar
    fetch:
      - { from: build, name: binary, into: dist }
      - { from: build, name: binary, member: { os: mac, arch: amd64 }, into: one }
`

func TestFetchFromALoop(t *testing.T) {
	p, err := Parse([]byte(fetchLoopSrc))
	if err != nil {
		t.Fatal(err)
	}
	if p.Requires != SchemaParity2 {
		t.Fatalf("requires %d", p.Requires)
	}
	b := bindLoop(t, fetchLoopSrc)
	if b.Requires != SchemaParity2 || len(b.Step("package").Fetch) != 2 {
		t.Fatalf("bound: %d %+v", b.Requires, b.Step("package").Fetch)
	}
	if l := b.Loops[0]; l.MemberDir(l.Members[1]) != "linux_arm64" {
		t.Fatalf("member dir %s", l.MemberDir(l.Members[1]))
	}
	if !reflect.DeepEqual(b.Step("package").Needs, []string{"build[os=linux,arch=amd64]", "build[os=linux,arch=arm64]", "build[os=mac,arch=amd64]"}) {
		t.Fatalf("needs %v", b.Step("package").Needs)
	}
}

func TestFetchFromALoopMistakes(t *testing.T) {
	cases := []struct{ name, fetch, want string }{
		{"no into", "{ from: build, name: binary }", "say where the members' files go with into"},
		{"missing key", "{ from: build, name: binary, member: { os: linux }, into: o }", "lacks arch"},
		{"unknown key", "{ from: build, name: binary, member: { os: linux, arch: amd64, x: y }, into: o }", "x, which build does not have"},
		{"unknown value", "{ from: build, name: binary, member: { os: linux, arch: s390x }, into: o }", "has no member os=linux,arch=s390x"},
		{"excluded member", "{ from: build, name: binary, member: { os: mac, arch: arm64 }, into: o }", "has no member"},
		{"member on a plain step", "{ from: lint, name: binary, member: { os: linux }, into: o }", "member goes with a loop"},
		{"not a path", "{ from: build, name: binary, into: /abs }", "not relative"},
		{"twice", "{ from: build, name: binary, into: o }, { from: build, name: binary, into: p }", "fetched twice"},
	}
	for _, c := range cases {
		src := strings.Replace(fetchLoopSrc, "    fetch:\n      - { from: build, name: binary, into: dist }\n      - { from: build, name: binary, member: { os: mac, arch: amd64 }, into: one }\n",
			"    fetch: [ "+c.fetch+" ]\n", 1)
		src = strings.Replace(src, "steps:\n", "steps:\n  lint: {image: x, run: l, artifacts: [{name: binary, paths: [x]}]}\n", 1)
		src = strings.Replace(src, "    needs: build\n", "    needs: [build, lint]\n", 1)
		_, err := Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
	// A loop the step does not need.
	_, err := Parse([]byte(strings.Replace(fetchLoopSrc, "    needs: build\n", "", 1)))
	if err == nil || !strings.Contains(err.Error(), "does not need") {
		t.Errorf("not needed: %v", err)
	}
}

// With an input list the member's value is checked when the run is made.
func TestFetchFromALoopMemberChecksTheExpansion(t *testing.T) {
	src := `
pipeline: fetch
inputs:
  systems: { type: list, default: [linux, mac] }
steps:
  build:
    for_each: { os: { input: systems } }
    image: x
    run: make
    artifacts: [{name: binary, paths: [bin/app]}]
  package:
    needs: build
    image: x
    run: tar
    fetch: [ { from: build, name: binary, member: { os: beos }, into: one } ]
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := ResolveInputs(p, nil)
	if _, err := Bind(p, vals, BindOptions{}); err == nil || !strings.Contains(err.Error(), "has no member os=beos") {
		t.Fatalf("%v", err)
	}
}
