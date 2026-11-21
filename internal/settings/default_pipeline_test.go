package settings

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

func bootFile(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load("../../examples/local.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func obj(kind, name, unit, spec string) engine.ConfigObject {
	return engine.ConfigObject{Kind: kind, Name: name, Unit: unit, Spec: json.RawMessage(spec), Version: 1}
}

// defaultsFixture: an organisation default, a unit with its own, and four
// projects: one plain, one in the unit, one that switches the default off,
// one that gives its own pipeline.
func defaultsFixture() []engine.ConfigObject {
	return []engine.ConfigObject{
		obj("library", "shared", "", `{"repo":"https://git.test/shared.git"}`),
		obj("organisation", "default", "", `{"display_name":"Acme","default_pipeline":{"library":"shared","version":"v1","template":"service-ci","with":{"skip":"false","lint_command":"ruff check ."},"on":{"push":{"branches":["main"]},"pull_request":{}}}}`),
		obj("unit", "infra", "", `{"display_name":"Infra","default_pipeline":{"library":"shared","version":"v2","template":"iac-ci"}}`),
		obj("project", "plain", "", `{"repo":"https://git.test/plain.git","default_pipeline":{"with":{"lint_command":"flake8"}}}`),
		obj("project", "tf", "infra", `{"repo":"https://git.test/tf.git"}`),
		obj("project", "quiet", "", `{"repo":"https://git.test/quiet.git","default_pipeline":{"enabled":false}}`),
		obj("project", "mine", "", `{"repo":"https://git.test/mine.git","pipeline":{"library":"shared","version":"v3","template":"other"}}`),
	}
}

func TestDefaultPipelineReachesProjectsWithoutOne(t *testing.T) {
	got, _, err := Compose(bootFile(t), defaultsFixture())
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(got, nil); err != nil {
		t.Fatal(err)
	}
	by := map[string]config.ProjectConfig{}
	for _, p := range got.Projects {
		by[p.Name] = p
	}
	plain := by["plain"]
	if plain.Pipeline == nil || plain.PipelineFrom != "organisation" || plain.Pipeline.Template != "service-ci" {
		t.Fatalf("plain: %+v from %q", plain.Pipeline, plain.PipelineFrom)
	}
	if plain.Pipeline.With["lint_command"] != "flake8" || plain.Pipeline.With["skip"] != "false" {
		t.Errorf("the project's value should win and the rest stay: %v", plain.Pipeline.With)
	}
	if tf := by["tf"]; tf.Pipeline == nil || tf.PipelineFrom != "unit infra" || tf.Pipeline.Template != "iac-ci" {
		t.Errorf("a unit's default takes the organisation's place: %+v", tf.Pipeline)
	}
	if q := by["quiet"]; q.Pipeline != nil {
		t.Errorf("switched off, yet it has %+v", q.Pipeline)
	}
	if m := by["mine"]; m.Pipeline.Template != "other" || m.PipelineFrom != "" {
		t.Errorf("a project's own pipeline stays whole: %+v from %q", m.Pipeline, m.PipelineFrom)
	}
	// The organisation's own values are not changed by one project's say.
	if by["plain"].Pipeline.With["lint_command"] == "ruff check ." {
		t.Error("plain lost its override")
	}
	src, err := got.SourceProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range src {
		if p.Name == "plain" && (p.Pipeline == nil || p.Pipeline.Default != "organisation" || p.Pipeline.On.PullRequest == nil) {
			t.Errorf("the source project: %+v", p.Pipeline)
		}
	}
}

func TestDefaultPipelineIsCheckedWithTheConfiguration(t *testing.T) {
	objs := defaultsFixture()
	objs[1] = obj("organisation", "default", "", `{"default_pipeline":{"library":"missing","version":"v1","template":"service-ci"}}`)
	got, _, err := Compose(bootFile(t), objs)
	if err != nil {
		t.Fatal(err)
	}
	err = Check(got, nil)
	if err == nil || !strings.Contains(err.Error(), `pipeline.library "missing"`) {
		t.Fatalf("got %v", err)
	}
}

func TestNoDefaultMeansNoPipeline(t *testing.T) {
	got, _, err := Compose(bootFile(t), []engine.ConfigObject{
		obj("project", "p", "", `{"repo":"https://git.test/p.git","default_pipeline":{"with":{"a":"b"}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Projects[0].Pipeline != nil {
		t.Fatal("a pipeline from nowhere")
	}
}
