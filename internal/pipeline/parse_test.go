package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const good = `
pipeline: api
on:
  push: { branches: [main, "release/*"] }
steps:
  lint:
    image: alpine:3.22
    run: echo lint
  test:
    image: golang:1.24
    run: go test ./...
    retry: 2
    timeout: 10m
  build:
    image: alpine:3.22
    needs: [lint, test]
    run: echo build
    env: { TARGET: linux }
  deploy:
    image: alpine:3.22
    needs: build
    when: { branch: main, event: [push, manual] }
    run: echo deploy
`

func TestParseGood(t *testing.T) {
	p, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "api" || len(p.Steps) != 4 {
		t.Fatalf("got %+v", p)
	}
	if got := []string{p.Steps[0].ID, p.Steps[1].ID, p.Steps[2].ID, p.Steps[3].ID}; strings.Join(got, ",") != "lint,test,build,deploy" {
		t.Fatalf("file order not kept: %v", got)
	}
	test := p.Step("test")
	if test.Retry != 2 || test.Timeout.D() != 10*time.Minute {
		t.Fatalf("retry/timeout: %+v", test)
	}
	if p.Step("lint").Timeout.D() != DefaultTimeout {
		t.Fatalf("default timeout not applied")
	}
	if got := p.Step("deploy").Needs; len(got) != 1 || got[0] != "build" {
		t.Fatalf("single needs value not accepted as a list: %v", got)
	}
	levels := p.Levels()
	want := [][]string{{"lint", "test"}, {"build"}, {"deploy"}}
	if b1, b2 := mustJSON(levels), mustJSON(want); b1 != b2 {
		t.Fatalf("levels %s, want %s", b1, b2)
	}
	if !p.On.MatchPush("release/1.2", "") || p.On.MatchPush("feature/x", "") {
		t.Fatalf("push matching wrong")
	}
	d := p.Step("deploy").When
	if !d.Match(Context{Event: "push", Branch: "main"}) || d.Match(Context{Event: "push", Branch: "dev"}) {
		t.Fatalf("condition matching wrong")
	}
}

func TestGraphJSONIsStable(t *testing.T) {
	a, _ := Parse([]byte(good))
	b, _ := Parse([]byte(good))
	if mustJSON(a) != mustJSON(b) {
		t.Fatal("same file produced different graphs")
	}
	if strings.Contains(mustJSON(a), "Line") {
		t.Fatal("source positions leaked into the graph")
	}
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(a)), &back); err != nil {
		t.Fatalf("graph does not round-trip: %v", err)
	}
	if mustJSON(&back) != mustJSON(a) {
		t.Fatal("graph changed on round-trip")
	}
}

func TestParseProblems(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"empty", ``, "empty"},
		{"not yaml", "pipeline: [", "did not find"},
		{"no name", "steps:\n  a: {image: x, run: y}\n", "a name is required"},
		{"bad name", "pipeline: My_App\nsteps:\n  a: {image: x, run: y}\n", "not a valid name"},
		{"no steps", "pipeline: a\n", "at least one step"},
		{"unknown top key", "pipeline: a\nstages: {}\nsteps:\n  a: {image: x, run: y}\n", `unknown key "stages"`},
		{"unknown step key", "pipeline: a\nsteps:\n  a: {image: x, run: y, script: z}\n", `unknown key "script"`},
		{"not yet", "pipeline: a\nsteps:\n  a: {image: x, run: y, matrix: {}}\n", "not supported yet"},
		{"uses with run", "pipeline: a\nlibraries:\n  lib: {repo: r, version: v1}\nsteps:\n  a: {image: x, run: y, uses: lib/t}\n", "takes no image"},
		{"missing image", "pipeline: a\nsteps:\n  a: {run: y}\n", "say what to run in"},
		{"unknown container", "pipeline: a\nsteps:\n  a: {container: maven, run: y}\n", `no container called "maven"`},
		{"both", "pipeline: a\ncontainers:\n  m: {image: x}\nsteps:\n  a: {container: m, image: x, run: y}\n", "not both"},
		{"step in service", "pipeline: a\ncontainers:\n  db: {image: pg, service: true}\nsteps:\n  a: {container: db, run: y}\n", "is a service"},
		{"bad size", "pipeline: a\ncontainers:\n  m: {image: x, memory: lots}\nsteps:\n  a: {container: m, run: y}\n", "not a size"},
		{"reserved name", "pipeline: a\ncontainers:\n  workspace: {image: x}\nsteps:\n  a: {image: x, run: y}\n", "reserved"},
		{"command on tool", "pipeline: a\ncontainers:\n  m: {image: x, command: [run]}\nsteps:\n  a: {container: m, run: y}\n", "only a service"},
		{"missing run", "pipeline: a\nsteps:\n  a: {image: x}\n", "a script to run is required"},
		{"unknown need", "pipeline: a\nsteps:\n  a: {image: x, run: y, needs: [b]}\n", `no step with that name`},
		{"self need", "pipeline: a\nsteps:\n  a: {image: x, run: y, needs: [a]}\n", "cannot need itself"},
		{"cycle", "pipeline: a\nsteps:\n  a: {image: x, run: y, needs: [c]}\n  b: {image: x, run: y, needs: [a]}\n  c: {image: x, run: y, needs: [b]}\n", "loop"},
		{"duplicate step", "pipeline: a\nsteps:\n  a: {image: x, run: y}\n  a: {image: x, run: y}\n", "written twice"},
		{"bad retry", "pipeline: a\nsteps:\n  a: {image: x, run: y, retry: 99}\n", "retry must be"},
		{"bad timeout", "pipeline: a\nsteps:\n  a: {image: x, run: y, timeout: soon}\n", "duration"},
		{"reserved env", "pipeline: a\nsteps:\n  a: {image: x, run: y, env: {LAUNCHWAY_X: 1}}\n", "reserved"},
		{"bad event", "pipeline: a\nsteps:\n  a: {image: x, run: y, when: {event: pr}}\n", "not known"},
		{"empty push", "pipeline: a\non: {push: {}}\nsteps:\n  a: {image: x, run: y}\n", "list branches or tags"},
		{"secrets not a mapping", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: [T]}\n", "secrets is a mapping"},
		{"secret not a mapping", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: {T: vault}}\n", "expected a mapping"},
		{"secret bad name", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: {1T: {store: env, key: K}}}\n", "not a valid variable name"},
		{"secret reserved name", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: {launchway_t: {store: env, key: K}}}\n", "reserved"},
		{"secret no store", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: {T: {key: K}}}\n", "name the store"},
		{"secret upper-case store", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: {T: {store: Vault, key: K}}}\n", "name the store"},
		{"secret no key", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: {T: {store: env, key: \" \"}}}\n", "a key is required"},
		{"secret unknown key", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: {T: {store: env, key: K, path: p}}}\n", `unknown key "path"`},
		{"secret twice", "pipeline: a\nsteps:\n  a: {image: x, run: y, secrets: {T: {store: env, key: K}, T: {store: env, key: L}}}\n", "written twice"},
		{"secret also in env", "pipeline: a\nsteps:\n  a: {image: x, run: y, env: {T: 1}, secrets: {T: {store: env, key: K}}}\n", "also set under env"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			if err == nil {
				t.Fatalf("expected a problem containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %q, want it to contain %q", err.Error(), c.want)
			}
			if _, ok := err.(Problems); !ok {
				t.Fatalf("error is %T, want Problems", err)
			}
		})
	}
}

func TestProblemsHaveLines(t *testing.T) {
	_, err := Parse([]byte("pipeline: a\nsteps:\n  a:\n    image: x\n    run: y\n    colour: blue\n"))
	ps := err.(Problems)
	if ps[0].Line != 6 {
		t.Fatalf("problem reported on line %d, want 6", ps[0].Line)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestContainers(t *testing.T) {
	p, err := Parse([]byte(`
pipeline: app
containers:
  maven: { image: maven:3.9, memory: 2Gi, cpu: "1", env: { MAVEN_OPTS: -Xmx1g } }
  gcloud: { image: gcr.io/cloud-sdk }
  db: { image: postgres:17, service: true, env: { POSTGRES_PASSWORD: test } }
steps:
  build: { container: maven, run: mvn package }
  lint: { image: alpine:3.22, run: echo }
  more: { image: alpine:3.22, run: echo }
  ship: { container: gcloud, run: gcloud version, needs: [build] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Step("build").Image != "maven:3.9" {
		t.Fatalf("image not filled from the container: %q", p.Step("build").Image)
	}
	rc := p.RunContainers()
	names := []string{}
	for _, c := range rc {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "maven,gcloud,db,image-1" {
		t.Fatalf("run containers %v", names)
	}
	if p.ContainerFor(p.Step("lint")) != "image-1" || p.ContainerFor(p.Step("more")) != "image-1" || p.ContainerFor(p.Step("ship")) != "gcloud" {
		t.Fatal("ContainerFor is wrong")
	}
	if c := p.Container("db"); !c.Service || c.Env["POSTGRES_PASSWORD"] != "test" {
		t.Fatalf("service %+v", c)
	}
}

func TestSecrets(t *testing.T) {
	p, err := Parse([]byte(`
pipeline: app
steps:
  push:
    image: alpine:3.22
    secrets:
      REGISTRY_PASSWORD: { store: vault, key: apps/harbor/robot, field: password }
      NPM_TOKEN:         { store: env,   key: NPM_TOKEN }
    run: echo "$REGISTRY_PASSWORD" | some-login
`))
	if err != nil {
		t.Fatal(err)
	}
	got := p.Step("push").Secrets
	want := map[string]SecretRef{
		"REGISTRY_PASSWORD": {Store: "vault", Key: "apps/harbor/robot", Field: "password"},
		"NPM_TOKEN":         {Store: "env", Key: "NPM_TOKEN"},
	}
	if mustJSON(got) != mustJSON(want) {
		t.Fatalf("secrets %s, want %s", mustJSON(got), mustJSON(want))
	}
	if s := got["REGISTRY_PASSWORD"].String(); s != "vault:apps/harbor/robot#password" {
		t.Fatalf("String() = %q", s)
	}
	// The references are part of the graph, and survive a round trip.
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(p)), &back); err != nil || mustJSON(back.Step("push").Secrets) != mustJSON(want) {
		t.Fatalf("graph round trip lost the references: %v %s", err, mustJSON(&back))
	}
}

func TestSecretProblemLine(t *testing.T) {
	_, err := Parse([]byte("pipeline: a\nsteps:\n  a:\n    image: x\n    run: y\n    env: {T: 1}\n    secrets:\n      T: {store: env, key: K}\n"))
	ps, ok := err.(Problems)
	if !ok || len(ps) != 1 || ps[0].Line != 8 {
		t.Fatalf("got %v, want one problem on line 8", err)
	}
}

func TestApprovalStep(t *testing.T) {
	p, err := Parse([]byte(`
pipeline: app
containers:
  tools: { image: alpine:3.22 }
steps:
  smoke-test:
    image: alpine:3.22
    run: ./smoke.sh
  approve-prod:
    kind: approval
    needs: smoke-test
    when: { branch: main }
    message: "  Deploy 2.14 to production?  "
  approve-later:
    kind: approval
    message: Again?
    timeout: 720h
  deploy-prod:
    kind: step
    container: tools
    needs: [approve-prod, approve-later]
    run: ./deploy.sh
`))
	if err != nil {
		t.Fatal(err)
	}
	a := p.Step("approve-prod")
	if !a.IsApproval() || a.Message != "Deploy 2.14 to production?" || a.Timeout.D() != DefaultApprovalTimeout {
		t.Fatalf("approval %+v", a)
	}
	if len(a.Needs) != 1 || a.When == nil || a.Image != "" || a.Run != "" {
		t.Fatalf("approval %+v", a)
	}
	if p.Step("approve-later").Timeout.D() != MaxApprovalTimeout {
		t.Fatalf("timeout %s", p.Step("approve-later").Timeout.D())
	}
	if d := p.Step("deploy-prod"); d.IsApproval() || d.Kind != "" {
		t.Fatalf("kind: step should be a plain step with no kind written: %+v", d)
	}
	// Approvals need no container, and no executor ever sees one.
	names := []string{}
	for _, c := range p.RunContainers() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "tools,image-1" {
		t.Fatalf("run containers %v", names)
	}
	if p.ContainerFor(a) != "" || p.ContainerFor(p.Step("smoke-test")) != "image-1" {
		t.Fatal("ContainerFor is wrong")
	}
	// The kind and message are part of the graph and survive a round trip.
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(p)), &back); err != nil || mustJSON(&back) != mustJSON(p) {
		t.Fatalf("round trip: %v", err)
	}
	if !back.Step("approve-prod").IsApproval() {
		t.Fatal("kind lost on a round trip")
	}
	if strings.Contains(mustJSON(p.Step("smoke-test")), `"kind"`) {
		t.Fatal("a plain step should not write a kind")
	}
}

func TestApprovalProblems(t *testing.T) {
	long := strings.Repeat("x", MaxMessageLen+1)
	cases := []struct {
		name, src, want string
		line            int
	}{
		{"no message", "pipeline: a\nsteps:\n  a:\n    kind: approval\n", "needs a message", 3},
		{"empty message", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: \"  \"\n", "needs a message", 3},
		{"two lines", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: |\n      one\n      two\n", "one line", 5},
		{"too long", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: " + long + "\n", "longer than 500", 5},
		{"image", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: ok?\n    image: alpine\n", "takes no image", 6},
		{"container", "pipeline: a\ncontainers:\n  m: {image: x}\nsteps:\n  a:\n    kind: approval\n    message: ok?\n    container: m\n", "takes no container", 8},
		{"run", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: ok?\n    run: echo\n", "takes no run", 6},
		{"env", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: ok?\n    env: {A: b}\n", "takes no env", 6},
		{"secrets", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: ok?\n    secrets: {T: {store: env, key: K}}\n", "takes no secrets", 6},
		{"retry", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: ok?\n    retry: 1\n", "takes no retry", 6},
		{"timeout too long", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: ok?\n    timeout: 721h\n", "between 1s and 720h", 6},
		{"step timeout over a day", "pipeline: a\nsteps:\n  a:\n    image: x\n    run: y\n    timeout: 25h\n", "between 1s and 24h", 6},
		{"message on a step", "pipeline: a\nsteps:\n  a:\n    image: x\n    run: y\n    message: hi\n", "only for approval steps", 6},
		{"kind not yet", "pipeline: a\nsteps:\n  a:\n    kind: release\n", `kind "release" is not supported yet`, 4},
		{"unknown kind", "pipeline: a\nsteps:\n  a:\n    kind: gate\n", `unknown kind "gate"`, 4},
		{"approval need missing", "pipeline: a\nsteps:\n  a:\n    kind: approval\n    message: ok?\n    needs: b\n", "no step with that name", 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			ps, ok := err.(Problems)
			if !ok {
				t.Fatalf("got %v, want Problems", err)
			}
			if len(ps) != 1 || !strings.Contains(ps[0].Message, c.want) || ps[0].Line != c.line {
				t.Fatalf("got %q, want one problem on line %d containing %q", err.Error(), c.line, c.want)
			}
		})
	}
}

func TestStepDescription(t *testing.T) {
	p, err := Parse([]byte(`pipeline: a
steps:
  build:
    description: "  Compile the server  "
    image: x
    run: y
  gate:
    kind: approval
    description: Ask before going on
    message: Go on?
    needs: build
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Step("build").Description; got != "Compile the server" {
		t.Fatalf("description %q", got)
	}
	if got := p.Step("gate").Description; got != "Ask before going on" {
		t.Fatalf("approval description %q", got)
	}
	if !strings.Contains(mustJSON(p), `"description":"Compile the server"`) {
		t.Fatalf("the description is not in the graph: %s", mustJSON(p))
	}
	for name, src := range map[string]string{
		"two lines": "pipeline: a\nsteps:\n  a:\n    description: |\n      one\n      two\n    image: x\n    run: y\n",
		"too long":  "pipeline: a\nsteps:\n  a:\n    description: " + strings.Repeat("x", MaxDescriptionLen+1) + "\n    image: x\n    run: y\n",
		"a list":    "pipeline: a\nsteps:\n  a:\n    description: [x]\n    image: x\n    run: y\n",
	} {
		_, err := Parse([]byte(src))
		ps, ok := err.(Problems)
		if !ok || ps[0].Line != 4 || !strings.Contains(ps[0].Path, "description") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDraftEvent(t *testing.T) {
	p, err := Parse([]byte(`pipeline: a
steps:
  always: {image: x, run: y}
  branch-only: {image: x, run: y, when: {branch: main}}
  pushes: {image: x, run: y, when: {event: push}}
  manual: {image: x, run: y, when: {event: [push, manual]}}
  drafts: {image: x, run: y, when: {event: draft}}
  draft-main: {image: x, run: y, when: {event: [manual, draft], branch: main}}
`))
	if err != nil {
		t.Fatal(err)
	}
	runs := func(ctx Context) []string {
		var out []string
		for _, s := range p.Steps {
			if s.When.Match(ctx) {
				out = append(out, s.ID)
			}
		}
		return out
	}
	for _, c := range []struct {
		ctx  Context
		want string
	}{
		{Context{Event: EventDraft, Branch: "main"}, "always drafts draft-main"},
		{Context{Event: EventDraft, Branch: "dev"}, "always drafts"},
		{Context{Event: EventManual, Branch: "main"}, "always branch-only manual draft-main"},
		{Context{Event: EventPush, Branch: "main"}, "always branch-only pushes manual"},
	} {
		if got := strings.Join(runs(c.ctx), " "); got != c.want {
			t.Errorf("%+v runs %q, want %q", c.ctx, got, c.want)
		}
	}
	_, err = Parse([]byte("pipeline: a\nsteps:\n  a: {image: x, run: y, when: {event: drafts}}\n"))
	if err == nil || !strings.Contains(err.Error(), "use push, manual, draft") {
		t.Fatalf("unknown event: %v", err)
	}
}

func TestSyntaxErrorsHaveLines(t *testing.T) {
	_, err := Parse([]byte("pipeline: a\nsteps:\n  a:\n    image: x\n   run: y\n"))
	ps, ok := err.(Problems)
	if !ok || len(ps) != 1 || ps[0].Line == 0 || strings.HasPrefix(ps[0].Message, "line") {
		t.Fatalf("%#v", err)
	}
}

func TestFileSizeLimit(t *testing.T) {
	big := "pipeline: a\nsteps:\n  a: {image: x, run: y}\n# " + strings.Repeat("x", MaxFileBytes)
	if _, err := Parse([]byte(big)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("%v", err)
	}
}

func TestStarterIsValid(t *testing.T) {
	for _, name := range []string{"pay-api", "Not A Name"} {
		p, err := Parse([]byte(Starter(name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "pay-api" && p.Name != "pay-api" {
			t.Fatalf("named %q", p.Name)
		}
	}
}

func TestNeedsProblemsPointAtNeeds(t *testing.T) {
	_, err := Parse([]byte("pipeline: a\nsteps:\n  a: {image: x, run: y}\n  b:\n    image: x\n    run: y\n    needs: [a, nope]\n"))
	ps, ok := err.(Problems)
	if !ok || ps[0].Line != 7 || !strings.Contains(ps[0].Message, `"nope"`) {
		t.Fatalf("%#v", err)
	}
}

// Pull requests are a trigger with no settings, and a when condition can
// name them. supersede is a top-level switch.
func TestPullRequestsAndSupersede(t *testing.T) {
	for _, on := range []string{"pull_request: {}", "pull_request:", "{ push: { branches: [main] }, pull_request: {} }"} {
		src := "pipeline: a\non:\n  " + on + "\nsupersede: true\nsteps:\n  a: {image: x, run: y, when: { event: pull_request }}\n"
		if strings.HasPrefix(on, "{") {
			src = "pipeline: a\non: " + on + "\nsupersede: true\nsteps:\n  a: {image: x, run: y, when: { event: pull_request }}\n"
		}
		p, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", on, err)
		}
		if !p.On.MatchPullRequest() || !p.Supersede || !p.UsesSchema9() {
			t.Fatalf("%s: %+v", on, p)
		}
		a := p.Step("a")
		if !a.When.Match(Context{Event: EventPullRequest}) || a.When.Match(Context{Event: EventPush, Branch: "main"}) {
			t.Fatalf("%s: when %+v", on, a.When)
		}
		j := mustJSON(p)
		if !strings.Contains(j, `"pull_request":{}`) || !strings.Contains(j, `"supersede":true`) {
			t.Fatalf("%s: %s", on, j)
		}
	}
	// Without them, the graph's JSON is as before, so older servers read it.
	p, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if j := mustJSON(p); strings.Contains(j, "pull_request") || strings.Contains(j, "supersede") || p.UsesSchema9() || p.On.MatchPullRequest() {
		t.Fatalf("%s", j)
	}
	// A branch condition never matches a pull request: it has no branch.
	if p.Step("deploy").When.Match(Context{Event: EventPullRequest}) {
		t.Fatal("deploy's when matched a pull request")
	}
	for name, c := range map[string]struct{ src, want string }{
		"branches":      {"pipeline: a\non: { pull_request: { branches: [main] } }\nsteps:\n  a: {image: x, run: y}\n", "write pull_request: { into: ["},
		"another key":   {"pipeline: a\non: { pull_request: { types: [opened] } }\nsteps:\n  a: {image: x, run: y}\n", `unknown key "types"; expected one of: into`},
		"a value":       {"pipeline: a\non: { pull_request: yes }\nsteps:\n  a: {image: x, run: y}\n", "write pull_request: {}"},
		"supersede":     {"pipeline: a\nsupersede: sometimes\nsteps:\n  a: {image: x, run: y}\n", "write true or false"},
		"unknown event": {"pipeline: a\nsteps:\n  a: {image: x, run: y, when: { event: pull-request }}\n", "pull_request"},
	} {
		_, err := Parse([]byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
		if ps, ok := err.(Problems); ok && ps[0].Line == 0 {
			t.Errorf("%s: no line: %v", name, ps)
		}
	}
}
