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
		{"not yet", "pipeline: a\nsteps:\n  a: {image: x, run: y, uses: lib/t}\n", "not supported yet"},
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
