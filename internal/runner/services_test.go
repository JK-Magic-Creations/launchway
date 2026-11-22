package runner

import (
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// A step that lists its services starts those and no others; a step that says nothing starts every service.
func TestStepsStartOnlyTheServicesTheyListed(t *testing.T) {
	cs := []pipeline.Container{{Name: "go", Image: "golang"}, {Name: "db", Image: "postgres", Service: true}, {Name: "cache", Image: "redis", Service: true}}
	r := &Runner{Exec: &executor.Host{}}
	names := func(job *engine.Job) []string {
		var out []string
		for _, c := range r.containerSpecs(job) {
			out = append(out, c.Name)
		}
		return out
	}
	for _, c := range []struct {
		name   string
		scoped bool
		list   []string
		want   string
	}{
		{"says nothing", false, nil, "go db cache"},
		{"lists db", true, []string{"db"}, "go db"},
		{"lists none", true, nil, "go"},
		{"lists both", true, []string{"cache", "db"}, "go db cache"},
	} {
		got := ""
		for _, n := range names(&engine.Job{Containers: cs, ScopedServices: c.scoped, Services: c.list}) {
			got += n + " "
		}
		if got != c.want+" " {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A Station that makes one pod for the whole run keeps the services every step of the run needs together, not only
// the first step's; with a step that names none, all of them.
func TestAPodForTheWholeRunKeepsTheServicesOfAllItsSteps(t *testing.T) {
	cs := []pipeline.Container{{Name: "go", Image: "golang"}, {Name: "db", Image: "postgres", Service: true}, {Name: "cache", Image: "redis", Service: true}, {Name: "mail", Image: "mailpit", Service: true}}
	r := &Runner{Exec: &executor.Kubernetes{}} // the zero value is not one pod for each step
	got := func(job *engine.Job) string {
		s := ""
		for _, c := range r.containerSpecs(job) {
			s += c.Name + " "
		}
		return s
	}
	first := engine.Job{Containers: cs, ScopedServices: true, Services: []string{"db"}}
	first.RunScopedServices, first.RunServices = true, []string{"cache", "db"}
	if g := got(&first); g != "go db cache " {
		t.Errorf("the services of the whole run: %q", g)
	}
	first.RunScopedServices = false
	if g := got(&first); g != "go db cache mail " {
		t.Errorf("a step that names none keeps every service: %q", g)
	}
}
