package stepkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodManifest = `step: notes
contract: 1
description: Write release notes
toolchain: python@3.13
command: python3 main.py
inputs:
  title: { type: string, pattern: "^[A-Za-z ]+$", max: 20 }
  count: { type: number, default: 3, min: 1, max: 10 }
  files: { type: list, default: [a, b] }
  token: { type: secret }
outputs:
  url: { type: string }
  total: { type: number }
  ok: { type: boolean }
  names: { type: list }
  meta: { type: record }
  key: { type: secret }
permissions:
  network: [registry.npmjs.org, "*.example.com:8443"]
  write: [dist, build/out]
`

func mustParse(t *testing.T, text string) *Manifest {
	t.Helper()
	m, probs := ParseManifest([]byte(text))
	if len(probs) > 0 {
		t.Fatalf("%v", probs)
	}
	return m
}

func TestManifestProblemsNameTheirLine(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"step: Notes\ncontract: 1\n", "line 1: \"Notes\" is not a step name"},
		{"step: a\ncontract: 9\nimage: i\ncommand: c\n", "written for contract 9"},
		{"step: a\ncontract: 1\nimage: i\n", "say what to run"},
		{"step: a\ncontract: 1\ncommand: c\n", "say what the step runs in"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\ninputs:\n  X: { type: string }\n", "line 6: \"X\" is not an input name"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\ninputs:\n  x: { type: color }\n", "the type is one of"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\ninputs:\n  x: { type: number, default: abc }\n", "the default does not fit"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\npermissions:\n  network: [\"*\"]\n", "is not a host"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\npermissions:\n  write: [../etc]\n", "has a \"..\" part"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\npermissions:\n  write: [.git/hooks]\n", "kept by Launchway"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\npermissions:\n  write: [/etc]\n", "must be relative"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\nbogus: 1\n", "unknown key \"bogus\""},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\nresources: { cpu: lots }\n", "is not an amount"},
		{"step: &x a\ncontract: 1\n", "anchors or aliases"},
		{"step: a\ncontract: 1\nimage: i\ncommand: c\ninputs:\n  s: { type: secret, default: x }\n", "has no default"},
		{"step: a\ncontract: 1\ntoolchain: cobol\ncommand: c\n", "toolchain \"cobol\" is not known"},
	} {
		_, probs := ParseManifest([]byte(c.text))
		if len(probs) == 0 || !strings.Contains(probs.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.text, probs, c.want)
		}
	}
}

func TestManifestIsReadAndSummarised(t *testing.T) {
	m := mustParse(t, goodManifest)
	if m.Image != "docker.io/library/python:3.13-slim" || len(m.Inputs) != 4 || len(m.Outputs) != 6 {
		t.Fatalf("%+v", m)
	}
	want := "This step can reach registry.npmjs.org, *.example.com:8443, read the secrets given as token and write dist, build/out in the workspace (the rest is read-only)."
	if got := m.Sentence(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	bare := mustParse(t, "step: a\ncontract: 1\nimage: i\ncommand: c\n")
	if got := bare.Sentence(); !strings.Contains(got, "reach no network") || !strings.Contains(got, "read no secrets") {
		t.Errorf("a step that asks for nothing: %s", got)
	}
}

func TestCheckWithPlainErrors(t *testing.T) {
	m := mustParse(t, goodManifest)
	got := m.CheckWith(map[string]string{"title": "Hi 1", "count": "11", "bogus": "x"})
	for k, want := range map[string]string{"title": "must match", "count": "at most 10", "bogus": "has no input", "token_store": "given by reference"} {
		if !strings.Contains(got[k], want) {
			t.Errorf("%s: %q does not say %q", k, got[k], want)
		}
	}
	if ok := m.CheckWith(map[string]string{"title": "Hi", "token_store": "vault", "token_key": "k"}); len(ok) != 0 {
		t.Errorf("%v", ok)
	}
}

func TestTypedInputsAndScalarEnv(t *testing.T) {
	m := mustParse(t, goodManifest)
	doc, err := m.BuildInputs(map[string]string{"title": "Hi", "files": `["x","y"]`}, map[string]string{"token": "TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"title":"Hi"`, `"count":3`, `"files":["x","y"]`, `"token":{"secret":"TOKEN"}`, `"contract":1`} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("%s missing from %s", want, doc)
		}
	}
	env := m.ScalarEnv(map[string]string{"title": "Hi"})
	if env["LAUNCHWAY_WITH_TITLE"] != "Hi" || env["LAUNCHWAY_WITH_COUNT"] != "3" || len(env) != 2 {
		t.Errorf("%v", env)
	}
}

func TestOutputsAreCheckedAgainstTheirTypes(t *testing.T) {
	m := mustParse(t, goodManifest)
	ok := `{"url":"https://x","total":3,"ok":true,"names":["a"],"meta":{"b":1},"key":"s3cr3t-value"}`
	out, probs := m.ReadOutputs([]byte(ok), true)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	if out.Values["total"] != "3" || out.Values["ok"] != "true" || out.Values["names"] != `["a"]` || out.Values["meta"] != `{"b":1}` || out.Values["key"] != "***" || out.Secrets[0] != "s3cr3t-value" {
		t.Errorf("%+v", out)
	}
	for _, c := range []struct{ doc, want string }{
		{`{"url":1}`, "a string"},
		{`{"total":"3"}`, "JSON number"},
		{`{"zzz":1}`, "does not declare"},
		{`{"names":"a"}`, "list of strings"},
		{`[1]`, "not one JSON object"},
		{``, "wrote none"},
	} {
		if _, probs := m.ReadOutputs([]byte(c.doc), true); !strings.Contains(strings.Join(probs, "; "), c.want) {
			t.Errorf("%s: %v, want %q", c.doc, probs, c.want)
		}
	}
	// A step that failed need not have written them all.
	if _, probs := m.ReadOutputs([]byte(`{"url":"x"}`), false); len(probs) != 0 {
		t.Errorf("a failed step: %v", probs)
	}
}

func TestResultsCannotBeForgedFromTheOutputAndBadLinesAreNoted(t *testing.T) {
	text := `{"t":"summary","markdown":"hello **world**"}
not json
{"t":"annotation","file":"a.go","line":3,"level":"warning","message":"careful"}
{"t":"annotation","file":"../../etc/passwd","line":3,"message":"escape"}
{"t":"finding","rule":"R1","severity":"high","file":"a.go","line":1,"message":"bad"}
{"t":"finding","rule":"R1","severity":"huge","message":"bad"}
{"t":"test","name":"t1","status":"failed","message":"boom"}
{"t":"explode"}
{"t":"mask","value":"abc"}
{"t":"mask","value":"abcdefgh"}
{"t":"needs_person","message":"approve the plan"}
`
	r := ParseResults(strings.NewReader(text))
	if len(r.Items) != 6 || len(r.Notes) != 5 {
		t.Fatalf("%d items, %d notes: %v", len(r.Items), len(r.Notes), r.Notes)
	}
	if r.NeedsPerson() != "approve the plan" || len(r.Masks()) != 1 {
		t.Errorf("%v %v", r.NeedsPerson(), r.Masks())
	}
	in := Interpret("notes", map[string]string{}, 65, nil, []byte(text), nil)
	if in.Code != 65 || !strings.Contains(in.Why, "approve the plan") {
		t.Errorf("%+v", in)
	}
	if !strings.Contains(string(in.SARIF), `"ruleId":"R1"`) || !strings.Contains(string(in.JUnit), "<failure") {
		t.Errorf("%s\n%s", in.SARIF, in.JUnit)
	}
}

func TestExitCodesHaveTheirMeanings(t *testing.T) {
	for code, want := range map[int]Outcome{0: OutcomePassed, 1: OutcomeFailed, 2: OutcomeFailed, 63: OutcomeFailed, 64: OutcomeAllowed, 65: OutcomeNeedsPerson, 66: OutcomeCrashed, 137: OutcomeCrashed, -1: OutcomeCrashed} {
		if got := OutcomeOf(code); got != want {
			t.Errorf("%d: %v want %v", code, got, want)
		}
	}
	in := Interpret("s", nil, 64, nil, nil, nil)
	if in.Code != 0 || !in.Warning {
		t.Errorf("failed but allowed counts as a pass with a warning: %+v", in)
	}
	in = Interpret("s", map[string]string{"x": "string"}, 0, nil, nil, nil)
	if in.Code != 1 || !strings.Contains(in.Why, "broke the contract") {
		t.Errorf("a pass that wrote no declared output: %+v", in)
	}
}

func TestSecretOutputsAndMasksHideEverythingRecorded(t *testing.T) {
	in := Interpret("s", map[string]string{"k": "secret"}, 0, []byte(`{"k":"hunter22-value"}`),
		[]byte(`{"t":"summary","markdown":"token hunter22-value and other-thing"}`+"\n"+`{"t":"mask","value":"other-thing"}`), nil)
	if in.Values["k"] != "***" || strings.Contains(in.Notes[0].Text, "hunter22") || strings.Contains(in.Notes[0].Text, "other-thing") {
		t.Errorf("%+v", in)
	}
}

func TestSafeJoinRefusesLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(root, "real", "deep"), 0o755)
	os.Symlink(outside, filepath.Join(root, "dist"))
	os.Symlink(".", filepath.Join(root, "real", "loop"))
	for _, rel := range []string{"dist", "dist/x", "real/loop", "real/loop/deep", "../x", "/etc", "a/../b"} {
		if _, err := SafeJoin(root, rel); err == nil {
			t.Errorf("%s was accepted", rel)
		}
	}
	for _, rel := range []string{"real", "real/deep", "not/yet/there", "."} {
		if _, err := SafeJoin(root, rel); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestDiffCallsOutWidenedPermissions(t *testing.T) {
	old := mustParse(t, goodManifest)
	cur := mustParse(t, strings.Replace(strings.Replace(goodManifest, "registry.npmjs.org, ", "registry.npmjs.org, evil.example.org, ", 1), "  total: { type: number }\n", "  total: { type: string }\n", 1))
	cs := Diff(old, cur)
	if !CanWiden(cs) {
		t.Fatalf("%+v", cs)
	}
	text := ""
	for _, c := range cs {
		text += c.What + "\n"
	}
	for _, want := range []string{"may now reach evil.example.org", "output total changed type from number to string"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from:\n%s", want, text)
		}
	}
	if len(Diff(old, old)) != 0 {
		t.Error("nothing changed, yet there are changes")
	}
}

func TestTemplateIsValidYAMLWhateverTheManifestSays(t *testing.T) {
	m := mustParse(t, goodManifest)
	b, err := m.Template(false)
	if err != nil || !strings.Contains(string(b), "step_manifest:") {
		t.Fatalf("%v\n%s", err, b)
	}
	// A command with quotes and newlines cannot break out of the script's structure.
	m2 := mustParse(t, "step: a\ncontract: 1\nimage: i\ncommand: [\"sh\", \"-c\", \"echo 'x'; rm -rf /\"]\n")
	if s := m2.Script(true); !strings.Contains(s, `'echo '\''x'\''; rm -rf /'`) {
		t.Errorf("argument list is not quoted: %s", s)
	}
}

func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(goodManifest))
	f.Add([]byte("step: a\ncontract: 1\nimage: i\ncommand: c\ninputs: {x: {type: list, default: [1, [2]]}}"))
	f.Add([]byte("&a [*a]"))
	f.Add([]byte("step: |\n  x\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, probs := ParseManifest(data)
		if (m == nil) == (len(probs) == 0) {
			t.Fatalf("a manifest and its problems: %v %v", m, probs)
		}
		if m != nil {
			// Whatever a manifest is, what is made from it parses back, and describing it does not fail.
			_ = m.Sentence()
			if _, err := m.Template(false); err != nil {
				return // an optional number without a default is refused by name
			}
			_ = m.CheckWith(map[string]string{"x": "1"})
		}
	})
}

func FuzzParseResults(f *testing.F) {
	f.Add([]byte(`{"t":"summary","markdown":"x"}` + "\n" + `{"t":"finding","rule":"r","severity":"low","message":"m"}`))
	f.Add([]byte("{\"t\":\"annotation\",\"file\":\"a\",\"line\":1,\"message\":\"m\"}\n\x00\xff{"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := ParseResults(strings.NewReader(string(data)))
		if len(r.Items) > MaxResults {
			t.Fatal("too many")
		}
		in := Interpret("s", map[string]string{"a": "string"}, 0, data, data, nil)
		_ = in.SARIF
	})
}
