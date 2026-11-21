package source

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func TestDefaultPipelineSaysWhereItCameFrom(t *testing.T) {
	pp := &ProjectPipeline{Library: "shared", Version: "v1", Template: "service-ci", Default: "unit infra",
		On: pipeline.Triggers{Push: &pipeline.PushTrigger{Branches: []string{"main"}}}}
	text, err := configuredText("app", pp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "flight plan of its unit infra") {
		t.Errorf("the text does not say so:\n%s", text)
	}
	if _, err := pipeline.Parse([]byte(text)); err != nil {
		t.Fatal(err)
	}
	p := &project{Project: Project{Pipeline: pp}}
	if n := p.note(fromServer); !strings.Contains(n, "flight plan of the unit infra ran") {
		t.Errorf("note: %s", n)
	}
	if n := p.note(fromFile); !strings.Contains(n, "in place of the flight plan of the unit infra") {
		t.Errorf("note: %s", n)
	}
	pp.Default = ""
	if n := p.note(fromServer); !strings.Contains(n, "pipeline configured on the server ran") {
		t.Errorf("note: %s", n)
	}
}
