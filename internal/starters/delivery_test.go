package starters_test

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

func expandWithFacts(t *testing.T, text string, facts pipeline.ProjectFacts, vars pipeline.Variables) (*pipeline.Pipeline, error) {
	t.Helper()
	p, err := pipeline.Parse([]byte(text))
	if err != nil {
		return nil, err
	}
	if p, err = pipeline.Bind(p, pipeline.PreviewValues(p), pipeline.BindOptions{Preview: true}); err != nil {
		return nil, err
	}
	for _, l := range p.Libraries {
		l.Commit = starters.Commit()
	}
	out, _, err := pipeline.ExpandWith(p, func(_ *pipeline.Library, name string) ([]byte, error) {
		if b, ok := starters.Template(name); ok {
			return b, nil
		}
		return nil, pipeline.ErrNoTemplate
	}, pipeline.WithProjectFacts(vars, facts))
	return out, err
}

// Every service Flight plan delivers to the project's environments in
// order, asks where the environment is protected, and checks health.
func TestServicePlansDeliverThroughTheProjectsEnvironments(t *testing.T) {
	facts := pipeline.ProjectFacts{Project: "shop", Environments: []string{"dev", "prod"}, Protected: []string{"prod"}}
	for _, plan := range []string{"python-service", "node-app", "go-service", "container-image", "static-site"} {
		t.Run(plan, func(t *testing.T) {
			p, err := expandWithFacts(t, "uses: launchway/"+plan+"\n", facts, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"ci/deliver[environment=dev]/deploy", "ci/deliver[environment=dev]/verify", "ci/deliver[environment=dev]/rollback",
				"ci/deliver[environment=prod]/approve", "ci/deliver[environment=prod]/deploy", "ci/deliver[environment=prod]/verify", "ci/deliver[environment=prod]/rollback"} {
				if p.Step(id) == nil {
					t.Errorf("no step %s", id)
				}
			}
			if p.Step("ci/deliver[environment=dev]/approve") != nil {
				t.Error("an unprotected environment asks for a decision")
			}
			dep := p.Step("ci/deliver[environment=prod]/deploy")
			if dep == nil || dep.ArgoCD.Application != "shop-prod" || dep.ArgoCD.WriteBack.Changes[0].File != "apps/shop-prod/base.yaml" ||
				dep.ArgoCD.WriteBack.Changes[0].Image != "launchway/shop" || dep.ArgoCD.WriteBack.Changes[0].Digest == nil {
				t.Fatalf("derived values: %+v", dep)
			}
			v := p.Step("ci/deliver[environment=prod]/verify")
			if v == nil || v.Verify.Metrics[0].Check == nil || v.Verify.Metrics[0].Check.URL != "http://shop.shop-prod.svc.cluster.local:8080/healthz" {
				t.Fatalf("health proof: %+v", v)
			}
			// Without environments nothing is delivered and the image is not pushed.
			p, err = expandWithFacts(t, "uses: launchway/"+plan+"\n", pipeline.ProjectFacts{Project: "shop"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range p.Steps {
				if strings.Contains(s.ID, "deliver") {
					t.Errorf("delivers with no environments: %s", s.ID)
				}
			}
		})
	}
}

// A value can be changed for one environment, a method chosen, and a stage
// switched off, in one line each.
func TestDeliveryOverridesAndMethods(t *testing.T) {
	facts := pipeline.ProjectFacts{Project: "shop", Environments: []string{"dev", "prod"}, Protected: []string{"prod"}}
	p, err := expandWithFacts(t, "uses: launchway/go-service\nskip: [licenses]\nwith: { method: helm-values, overrides: [prod.application=shop, prod.rollout=true, dev.verify=false] }\n", facts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Step("ci/licenses") != nil {
		t.Error("skip did not leave the stage out")
	}
	if p.Step("ci/deliver[environment=dev]/verify") != nil {
		t.Error("dev.verify=false did not turn the proof off")
	}
	dep := p.Step("ci/deliver[environment=prod]/deploy-rollout")
	if dep == nil || dep.ArgoCD.Application != "shop" || dep.Rollout == nil || len(dep.ArgoCD.WriteBack.Changes) != 2 || dep.ArgoCD.WriteBack.Changes[0].Set != "image.tag" {
		t.Fatalf("rollout with helm values: %+v", dep)
	}
}

// With sign-in off the built-in registry refuses to serve, so a starter must not default to pushing there. It
// falls back to the registry the server names (registry.fallback), or to a registry a person chose with the
// variable registry; with neither it does not push, and the Sortie records why.
func TestStartersDoNotPushToTheBuiltInRegistryThatCannotServe(t *testing.T) {
	envs := []string{"dev"}
	image := func(p *pipeline.Pipeline) *pipeline.ImageBuild {
		for _, s := range p.Steps {
			if s.ImageBuild != nil {
				return s.ImageBuild
			}
		}
		t.Fatal("no image step")
		return nil
	}
	for _, plan := range []string{"python-service", "node-app", "go-service", "container-image", "static-site"} {
		t.Run(plan, func(t *testing.T) {
			expand := func(facts pipeline.ProjectFacts, vars pipeline.Variables) *pipeline.ImageBuild {
				p, err := expandWithFacts(t, "uses: launchway/"+plan+"\n", facts, vars)
				if err != nil {
					t.Fatal(err)
				}
				return image(p)
			}
			// It serves: pushes to it, as before.
			b := expand(pipeline.ProjectFacts{Project: "shop", Environments: envs, Registry: &pipeline.RegistryFacts{Usable: true}}, nil)
			if !b.Pushes() || b.Tags[0].Registry != "launchway" {
				t.Fatalf("usable: %+v", b.Tags)
			}
			// Not told (a check made off the server): as if it served.
			if b := expand(pipeline.ProjectFacts{Project: "shop", Environments: envs}, nil); !b.Pushes() || b.Tags[0].Registry != "launchway" {
				t.Fatalf("not told: %+v", b.Tags)
			}
			// It cannot serve and nothing else is named: no push, and the facts say why.
			facts := pipeline.ProjectFacts{Project: "shop", Environments: envs, Registry: &pipeline.RegistryFacts{}}
			if b := expand(facts, nil); b.Pushes() {
				t.Fatalf("pushed to a registry that cannot serve: %+v", b.Tags)
			}
			v := pipeline.WithProjectFacts(nil, facts)[pipeline.VarPushing]
			if !strings.Contains(v.Display, "needs sign-in") || !strings.Contains(v.Display, "registry.fallback") || v.Text != "[]" {
				t.Fatalf("the reason: %+v", v)
			}
			// It cannot serve and the server names a registry: pushes there.
			facts.Registry = &pipeline.RegistryFacts{Fallback: "harbor"}
			if b := expand(facts, nil); !b.Pushes() || b.Tags[0].Registry != "harbor" {
				t.Fatalf("fallback: pushes %v to %+v", b.Pushes(), b.Tags)
			}
			// It cannot serve and a person set the variable registry: theirs wins, and it pushes.
			facts.Registry = &pipeline.RegistryFacts{}
			vars := pipeline.Variables{"registry": pipeline.BoundVariable{Name: "registry", Type: "string", Text: "quay", Display: "quay"}}
			if b := expand(facts, vars); !b.Pushes() || b.Tags[0].Registry != "quay" {
				t.Fatalf("variable: pushes %v to %+v", b.Pushes(), b.Tags)
			}
		})
	}
}
