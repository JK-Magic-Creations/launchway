package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

const cacheHead = "pipeline: p\nsteps:\n"

const pipCache = `
  test:
    image: docker.io/library/python:3.13-slim
    cache:
      - name: pip
        paths: [.cache/pip]
        key: { files: [requirements.txt, requirements-dev.txt], text: v1 }
        fallback: latest
        save: always
      - name: models
        paths: [.cache/models, data/*.bin]
        key: { text: v3 }
    run: pip install -r requirements.txt && pytest
`

func TestCacheIsParsed(t *testing.T) {
	p, err := Parse([]byte(cacheHead + pipCache))
	if err != nil {
		t.Fatal(err)
	}
	cs := p.Step("test").Cache
	if len(cs) != 2 {
		t.Fatalf("%+v", cs)
	}
	pip := cs[0]
	if pip.Name != "pip" || len(pip.Paths) != 1 || pip.Paths[0] != ".cache/pip" || len(pip.Key.Files) != 2 || pip.Key.Text != "v1" ||
		pip.Fallback != CacheFallbackLatest || pip.Save != CacheSaveAlways || pip.Line == 0 {
		t.Errorf("pip: %+v", pip)
	}
	if m := cs[1]; m.Fallback != "" || m.Save != "" || len(m.Key.Files) != 0 || len(m.Paths) != 2 {
		t.Errorf("models: %+v", m)
	}
	if p.Requires != SchemaCache || !p.UsesSchemaCache() {
		t.Errorf("a pipeline with a cache needs schema %d so that older servers leave its runs alone: requires %d", SchemaCache, p.Requires)
	}
	// It survives the JSON a run keeps its pipeline in.
	b, _ := json.Marshal(p)
	var back Pipeline
	if err := json.Unmarshal(b, &back); err != nil || len(back.Step("test").Cache) != 2 || back.Step("test").Cache[0].Key.Files[1] != "requirements-dev.txt" {
		t.Fatalf("%s: %v", b, err)
	}
	// A pipeline without one is as older servers know it.
	q, err := Parse([]byte(cacheHead + "  a: {image: x, run: a}\n"))
	if err != nil || q.Requires != 0 || q.UsesSchemaCache() {
		t.Errorf("requires %d: %v", q.Requires, err)
	}
}

func TestCacheOutcomes(t *testing.T) {
	for _, tc := range []struct {
		save    string
		outcome string
		want    bool
	}{
		{"", "succeeded", true}, {"", "failed", false}, {"success", "succeeded", true},
		{"always", "succeeded", true}, {"always", "failed", true}, {"always", "canceled", false},
		{"never", "succeeded", false},
	} {
		if got := (&Cache{Save: tc.save}).SaveAfter(tc.outcome); got != tc.want {
			t.Errorf("save %q after %s: %v", tc.save, tc.outcome, got)
		}
	}
}

func TestCacheMistakesAreExplained(t *testing.T) {
	one := func(c string) string {
		return cacheHead + "  a:\n    image: x\n    run: y\n    cache:\n      - " + c + "\n"
	}
	for src, want := range map[string]string{
		"{ paths: [a], key: { text: v1 } }":                                                                "needs a name",
		"{ name: Pip, paths: [a], key: { text: v1 } }":                                                     "not a valid cache name",
		"{ name: pip, key: { text: v1 } }":                                                                 "say which directories or files to keep",
		"{ name: pip, paths: [a] }":                                                                        "say what the key is made of",
		"{ name: pip, paths: [a], key: {} }":                                                               "a key needs files, text or both",
		"{ name: pip, paths: [a], key: { files: [] } }":                                                    "list at least one file",
		"{ name: pip, paths: [/etc], key: { text: v } }":                                                   "not relative to the workspace",
		"{ name: pip, paths: [../x], key: { text: v } }":                                                   "leaves the workspace",
		"{ name: pip, paths: [a], key: { files: [/etc/passwd] } }":                                         "not relative to the workspace",
		"{ name: pip, paths: [a], key: { text: v }, fallback: newest }":                                    "fallback is none (the default) or latest",
		"{ name: pip, paths: [a], key: { text: v }, save: sometimes }":                                     "save is success (the default), always or never",
		"{ name: pip, paths: [a], key: { text: v }, restore-keys: [x] }":                                   `unknown key "restore-keys"`,
		"{ name: pip, paths: [a], key: { text: v, hash: x } }":                                             `unknown key "hash"`,
		"{ name: pip, paths: [a], key: requirements.txt }":                                                 "key is a mapping",
		"{ name: pip, paths: [a], key: { text: v }, name: x }":                                             `"name" is written twice`,
		"{ name: {param: x}, paths: [a], key: { text: v } }":                                               "for templates",
		"{ name: pip, paths: [a], key: { text: { each: py } } }":                                           "{ each: py } is for steps repeated with for_each",
		"{ name: pip, paths: [a], key: { text: v } }\n      - { name: pip, paths: [b], key: { text: w } }": "cache pip is listed twice",
	} {
		_, err := Parse([]byte(one(src)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", src, want, err)
		}
	}
	// Every mistake says the line and the path.
	_, err := Parse([]byte(one("{ name: pip, paths: [a], key: { text: v }, save: sometimes }")))
	ps := problemsOf(t, err)
	p := findProblem(t, ps, "save is")
	if p.Line != 7 || p.Path != "steps.a.cache[0].save" {
		t.Errorf("%+v", p)
	}
	// Not a list, empty, too many.
	for src, want := range map[string]string{
		"cache: pip":   "cache is a list",
		"cache: []":    "list at least one cache",
		"cache: [pip]": "a cache is a mapping",
	} {
		_, err := Parse([]byte(cacheHead + "  a: { image: x, run: y, " + src + " }\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", src, want, err)
		}
	}
	var many []string
	for i := 0; i <= MaxCachesPerStep; i++ {
		many = append(many, "{ name: c"+string(rune('a'+i))+", paths: [a], key: { text: v } }")
	}
	_, err = Parse([]byte(cacheHead + "  a: { image: x, run: y, cache: [" + strings.Join(many, ", ") + "] }\n"))
	if err == nil || !strings.Contains(err.Error(), "at most 10 caches per step") {
		t.Errorf("%v", err)
	}
	var globs []string
	for i := 0; i <= MaxCachePaths; i++ {
		globs = append(globs, "d"+string(rune('a'+i)))
	}
	_, err = Parse([]byte(one("{ name: pip, paths: [" + strings.Join(globs, ", ") + "], key: { text: v } }")))
	if err == nil || !strings.Contains(err.Error(), "at most 20 paths") {
		t.Errorf("%v", err)
	}
	var keyFiles []string
	for i := 0; i <= MaxCacheKeyFiles; i++ {
		keyFiles = append(keyFiles, "f"+string(rune('a'+i)))
	}
	_, err = Parse([]byte(one("{ name: pip, paths: [a], key: { files: [" + strings.Join(keyFiles, ", ") + "] } }")))
	if err == nil || !strings.Contains(err.Error(), "at most 20 globs") {
		t.Errorf("%v", err)
	}
	_, err = Parse([]byte(one("{ name: pip, paths: [a], key: { text: \"" + strings.Repeat("x", MaxCacheKeyText+1) + "\" } }")))
	if err == nil || !strings.Contains(err.Error(), "key.text") {
		t.Errorf("%v", err)
	}
}

// Other kinds of step refuse a cache, saying why; an image step has a key
// of that name for layers, and the message for the others says so.
func TestOtherKindsRefuseCache(t *testing.T) {
	const c = "cache: [{ name: pip, paths: [a], key: { text: v } }]"
	for name, step := range map[string]string{
		"approval": "kind: approval\n    message: Go on?\n    " + c,
		"verify":   "kind: verify\n    deployment: a\n    " + c,
		"ai":       "kind: ai\n    model: local\n    prompt: hi\n    " + c,
		"deploy":   "kind: deploy\n    environment: prod\n    service: x\n    " + c,
	} {
		_, err := Parse([]byte(cacheHead + "  a:\n    " + step + "\n"))
		if err == nil || !strings.Contains(err.Error(), "cache") {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if name != "approval" && name != "deploy" && !strings.Contains(err.Error(), "image step has a cache key of its own") {
			t.Errorf("%s: the message must say that an image step's cache is another thing: %v", name, err)
		}
	}
	// An image step's own cache is layers in a registry, and is not this.
	p, err := Parse([]byte(cacheHead + "  a:\n    kind: image\n    tags: [{ registry: r, repository: a/b, tag: v1 }]\n    cache: { registry: r, repository: a/cache }\n"))
	if err != nil || len(p.Step("a").Cache) != 0 || p.Step("a").ImageBuild.Cache == nil || p.UsesSchemaCache() {
		t.Errorf("%+v %v", p, err)
	}
	// And a step that uses a template takes none.
	_, err = Parse([]byte("pipeline: p\nlibraries:\n  platform: { repo: ci, version: v1 }\nsteps:\n  a: { uses: platform/x, " + c + " }\n"))
	if err == nil || !strings.Contains(err.Error(), "a step that uses a template takes no cache") {
		t.Errorf("%v", err)
	}
}

func TestCacheInALoopTakesTheLoopsValues(t *testing.T) {
	src := cacheHead + `
  test:
    for_each: { py: ["3.12", "3.13"] }
    image: x
    run: y
    cache:
      - name: { each: py, format: "pip-%s" }
        paths: [{ each: py, format: ".cache/%s" }, shared]
        key: { files: [requirements.txt], text: { each: py, format: "v1-%s" } }
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Bind(p, nil, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, s := range b.Steps {
		if len(s.Cache) != 1 {
			continue
		}
		seen++
		py := s.Each["py"]
		c := s.Cache[0]
		if c.Name != "pip-"+py || c.Paths[0] != ".cache/"+py || c.Paths[1] != "shared" || c.Key.Text != "v1-"+py {
			t.Errorf("%s: %+v", s.ID, c)
		}
	}
	if seen != 2 {
		t.Fatalf("members with a cache: %d", seen)
	}
	// The pipeline before it was bound is not changed by binding.
	if c := p.Step("test").Cache[0]; !strings.Contains(c.Name, "{py}") {
		t.Errorf("binding changed the pipeline: %+v", c)
	}
	// A value that cannot be part of a name is refused with the loop's
	// member named.
	bad := strings.Replace(src, `["3.12", "3.13"]`, `["3.12+x"]`, 1)
	p, err = Parse([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Bind(p, nil, BindOptions{})
	if err == nil || !strings.Contains(err.Error(), "not a valid cache name") {
		t.Errorf("%v", err)
	}
}

func TestCacheInATemplateTakesParameters(t *testing.T) {
	src := "pipeline: p\nlibraries:\n  platform: { repo: ci, version: v1 }\nsteps:\n  b: { uses: platform/build, with: { lang: py } }\n"
	templates := map[string]string{"build": `template: build
params:
  lang: { type: string, default: go }
steps:
  compile:
    image: x
    run: make
    cache:
      - name: { param: lang, format: "deps-%s" }
        paths: [.cache]
        key: { text: { param: lang } }
`}
	p, err := Parse([]byte(src))
	if err != nil || p.Requires != 0 {
		t.Fatalf("%v requires %d", err, p.Requires)
	}
	out := mustExpand(t, src, templates)
	st := out.Step("b/compile")
	if st == nil || len(st.Cache) != 1 || st.Cache[0].Name != "deps-py" || st.Cache[0].Key.Text != "py" {
		t.Fatalf("%+v", out.Steps)
	}
	if out.Requires != SchemaCache {
		t.Fatalf("a template's cache reaches the run's pipeline, which must then require schema %d: %d", SchemaCache, out.Requires)
	}
}

// A pipeline that asks for resources and a cache needs the newer schema of
// the two.
func TestCacheAndResourcesTakeTheNewerSchema(t *testing.T) {
	p, err := Parse([]byte(cacheHead + "  a: { image: x, run: y, resources: { memory: 1Gi }, cache: [{ name: c, paths: [a], key: { text: v } }] }\n"))
	if err != nil || p.Requires != SchemaCache {
		t.Errorf("%v %d", err, p.Requires)
	}
}
