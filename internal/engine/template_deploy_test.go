package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

const libDeployTemplate = `template: cd
params:
  environment: { type: environment }
  application: { type: application }
steps:
  build: { image: alpine:3.22, run: "echo tag=1 >> $LAUNCHWAY_OUTPUTS" }
  deploy:
    kind: deploy
    needs: build
    environment: { param: environment }
    version: { output: build.tag }
    argocd:
      application: { param: application }
      write_back: { repo: gitops, branch: main, changes: [ { file: a.yaml, image: team/a, tag: { output: build.tag } } ] }
  rollback: { kind: rollback, needs: deploy, deployment: deploy }
`

// A template does not authorise anything: its deploy step is the project's
// deploy step once expanded, and the environment's own list of projects
// decides whether it may run.
func TestTemplateDeployIsCheckedAgainstTheEnvironment(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	src := "pipeline: shop\nlibraries:\n  l: { repo: l, version: v1 }\nsteps:\n  cd:\n    uses: l/cd\n    with: { environment: prod, application: shop }\n"
	expand := func(env string) *pipeline.Pipeline {
		p := parse(t, strings.Replace(src, "environment: prod", "environment: "+env, 1))
		for _, l := range p.Libraries {
			l.Commit = strings.Repeat("ab", 20)
		}
		x, err := pipeline.Expand(p, func(*pipeline.Library, string) ([]byte, error) { return []byte(libDeployTemplate), nil })
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	src2 := gitSource
	src2.Libraries = []LibrarySource{{Name: "l", URL: "https://git.example/l.git", Commit: strings.Repeat("ab", 20)}}
	gitSource := src2
	if _, err := e.CreateRun(NewRun{Project: "shop", Pipeline: expand("prod"), Source: gitSource}); err != nil {
		t.Fatalf("the project's own environment: %v", err)
	}
	var ps pipeline.Problems
	_, err := e.CreateRun(NewRun{Project: "other", Pipeline: expand("prod"), Source: gitSource})
	if !errors.As(err, &ps) || !strings.Contains(ps[0].Message, "cd/deploy: project other may not deploy to prod") || ps[0].Line != 0 {
		t.Fatalf("a project the environment does not list: %v", err)
	}
	_, err = e.CreateRun(NewRun{Project: "shop", Pipeline: expand("staging"), Source: gitSource})
	if !errors.As(err, &ps) || !strings.Contains(ps[0].Message, `environment "staging" is not configured on this server`) {
		t.Fatalf("an environment the server does not have: %v", err)
	}
}
