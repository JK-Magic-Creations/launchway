package importer

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func hostileFiles() map[string][]byte {
	// An alias bomb: each level holds ten references to the level before it.
	var b strings.Builder
	b.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i < 12; i++ {
		fmt.Fprintf(&b, "a%d: &a%d [", i, i)
		for k := 0; k < 10; k++ {
			if k > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "*a%d", i-1)
		}
		b.WriteString("]\n")
	}
	deep := strings.Repeat("[", 500) + strings.Repeat("]", 500)
	return map[string][]byte{
		"bomb":  []byte("jobs:\n  x:\n    steps: []\n" + b.String()),
		"deep":  []byte("jobs: " + deep + "\n"),
		"big":   []byte("jobs:\n  x:\n    steps:\n" + strings.Repeat("      - run: echo hello hello hello\n", 20000)),
		"tabs":  []byte("jobs:\n\t- x\n"),
		"empty": nil,
	}
}

func TestHostileYAMLIsRefused(t *testing.T) {
	for name, data := range hostileFiles() {
		_, err := ParseTree(data, DefaultLimits)
		switch name {
		case "bomb":
			if !errors.Is(err, ErrTooMany) {
				t.Errorf("%s: want ErrTooMany, got %v", name, err)
			}
		case "deep":
			if err == nil {
				t.Errorf("%s: a 500-level nesting was accepted", name)
			}
		case "big":
			if !errors.Is(err, ErrTooLarge) {
				t.Errorf("%s: want ErrTooLarge, got %v", name, err)
			}
		}
	}
}

func TestEveryReaderSurvivesHostileInput(t *testing.T) {
	paths := map[string]string{"hosted-workflows": ".github/workflows/x.yml", "stages-yaml": ".a-ci.yml", "jobs-config": ".ci/config.yml", "tasks-pipelines": "a-pipelines.yml", "groovy-declarative": "Pipelinefile"}
	groovy := map[string][]byte{
		"braces":  []byte("pipeline {" + strings.Repeat("stage('x') {", 3000)),
		"strings": []byte("pipeline { stages { stage('x') { steps { sh '''never closed } } } }"),
		"args":    []byte("pipeline " + strings.Repeat("[", 5000)),
		"tokens":  []byte(strings.Repeat("a ", 300000)),
	}
	for _, f := range formats {
		for name, data := range hostileFiles() {
			testRead(t, f, paths[f.ID], name, data)
		}
		if f.ID == "groovy-declarative" {
			for name, data := range groovy {
				testRead(t, f, "Pipelinefile", name, data)
			}
		}
	}
}

func testRead(t *testing.T, f *Format, path, name string, data []byte) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s/%s: panic: %v", f.ID, name, r)
		}
	}()
	p := f.Read(File{Path: path, Content: data}, map[string]File{path: {Path: path, Content: data}}, DefaultLimits)
	if len(data) > DefaultLimits.MaxBytes && len(p.Findings) == 0 {
		t.Errorf("%s/%s: an oversized file was read without a finding", f.ID, name)
	}
	if _, err := Write(p, Options{}); err != nil {
		_ = err // "nothing to run" is an acceptable answer
	}
}

func TestRequestLimits(t *testing.T) {
	var files []File
	for i := 0; i < MaxFiles+1; i++ {
		files = append(files, File{Path: fmt.Sprintf("f%d.yml", i), Content: []byte("a: b")})
	}
	if _, err := Import(Request{Files: files}); err == nil || !strings.Contains(err.Error(), "too many files") {
		t.Errorf("too many files accepted: %v", err)
	}
	big := File{Path: ".github/workflows/a.yml", Content: []byte(strings.Repeat("a", 3<<20))}
	if _, err := Import(Request{Files: []File{big, big}}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("oversized request accepted: %v", err)
	}
}

func fuzzFormat(f *testing.F, id, path string) {
	fm := FormatByID(id)
	for _, c := range []string{"testdata/" + id} {
		_ = c
	}
	files, _ := filepathGlob("testdata/" + id + "/*/in/" + strings.TrimPrefix(path, "") + "*")
	for _, p := range files {
		if b, err := readFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("jobs: {a: {steps: [{run: x}]}}"))
	f.Add([]byte("pipeline { stages { stage('a') { steps { sh 'x' } } } }"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		p := fm.Read(File{Path: path, Content: data}, map[string]File{path: {Path: path, Content: data}}, DefaultLimits)
		plan, err := Write(p, Options{Dry: len(data)%2 == 0})
		if err == nil && plan.Pipeline == "" {
			t.Fatal("empty pipeline without an error")
		}
	})
}

func FuzzHostedWorkflows(f *testing.F)   { fuzzFormat(f, "hosted-workflows", ".github/workflows/x.yml") }
func FuzzStagesYAML(f *testing.F)        { fuzzFormat(f, "stages-yaml", ".a-ci.yml") }
func FuzzGroovyDeclarative(f *testing.F) { fuzzFormat(f, "groovy-declarative", "Pipelinefile") }
func FuzzJobsConfig(f *testing.F)        { fuzzFormat(f, "jobs-config", ".ci/config.yml") }
func FuzzTasksPipelines(f *testing.F)    { fuzzFormat(f, "tasks-pipelines", "a-pipelines.yml") }

func TestDetect(t *testing.T) {
	cases := map[string]string{
		".github/workflows/ci.yml": "hosted-workflows",
		".x-ci.yml":                "stages-yaml",
		"Pipelinefile":              "groovy-declarative",
		"web/Pipelinefile":          "groovy-declarative",
		".ci/config.yml":           "jobs-config",
		"azure-pipelines.yml":      "tasks-pipelines",
	}
	for p, want := range cases {
		content := "pipeline {\n}\n"
		if want == "jobs-config" {
			content = "version: 2.1\njobs: {}\n"
		}
		got := Detect([]File{{Path: p, Content: []byte(content)}})
		if len(got) != 1 || got[0].Format != want {
			t.Errorf("%s: detected %v, want %s", p, got, want)
		}
	}
	for _, p := range []string{"README.md", ".github/dependabot.yml", ".vscode/config.yml", "src/a-ci.yml"} {
		if got := Detect([]File{{Path: p, Content: []byte("version: 2\n")}}); len(got) != 0 {
			t.Errorf("%s: wrongly detected %v", p, got)
		}
	}
}

func TestDryImportGatesEveryDeliveringStep(t *testing.T) {
	files := loadCase(t, "testdata/hosted-workflows/delivery")
	for _, dry := range []bool{true, false} {
		res, err := Import(Request{Files: files, Options: Options{Dry: dry}})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Plans[0].Pipeline
		if err := validates(text); err != nil {
			t.Fatalf("dry=%v does not validate: %v", dry, err)
		}
		has := strings.Contains(text, "enable_delivery")
		if has != dry {
			t.Errorf("dry=%v but the switch is present=%v", dry, has)
		}
		if dry {
			// Flipping the one switch still gives a valid pipeline.
			on := strings.Replace(text, "default: false", "default: true", 1)
			if err := validates(on); err != nil {
				t.Errorf("switched on does not validate: %v", err)
			}
		}
	}
}

func TestDocsTableIsComplete(t *testing.T) {
	for _, f := range formats {
		if len(f.Table) < 10 {
			t.Errorf("%s: a table of only %d constructs", f.ID, len(f.Table))
		}
	}
}
