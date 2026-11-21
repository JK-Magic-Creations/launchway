package cac_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// The Maneuvers and Flight plans of configuration as code are files of the
// built-in library (internal/starters/library/templates). They must expand
// to valid pipelines, with the secrets as references and nothing secret in
// the graph. The built-in library's Go package belongs to another branch;
// here the files are read from disk the way a Git library's would be.

func expand(t *testing.T, use, with string) *pipeline.Pipeline {
	t.Helper()
	src := "pipeline: p\nlibraries: { launchway: { repo: lib, version: v1 } }\nsteps:\n  s:\n    uses: launchway/" + use + "\n    with: { " + with + " }\n"
	p, err := pipeline.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := pipeline.ResolveInputs(p, nil)
	b, err := pipeline.Bind(p, vals, pipeline.BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range b.Libraries {
		l.Commit = strings.Repeat("a", 40)
	}
	x, err := pipeline.Expand(b, func(_ *pipeline.Library, name string) ([]byte, error) {
		return os.ReadFile(filepath.Join("../starters/library", pipeline.TemplatePath(name)))
	})
	if err != nil {
		t.Fatalf("%s: %v", use, err)
	}
	return x
}

func ids(p *pipeline.Pipeline) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.ID)
	}
	return out
}

func TestEveryManeuverAndFlightPlanExpands(t *testing.T) {
	cases := map[string]string{
		"ansible-run":             `playbook: site.yml, hosts: "a=10.0.0.1", known_hosts: "a ssh-ed25519 AAAA"`,
		"ansible-lint":            ``,
		"ansible-playbook":        `environment: prod, hosts: "a=10.0.0.1"`,
		"tofu-plan":               `workspace: prod`,
		"tofu-apply":              `workspace: prod`,
		"tofu-check":              ``,
		"iac-scan":                ``,
		"infrastructure":          `environment: prod, workspace: prod`,
		"provision-and-configure": `environment: prod, workspace: prod`,
	}
	for name, with := range cases {
		t.Run(name, func(t *testing.T) {
			p := expand(t, name, with)
			if len(p.Steps) == 0 {
				t.Fatal("no steps")
			}
			t.Log(ids(p))
		})
	}
}

func TestAnsibleSecretsAreReferencesAndVariantsAreExclusive(t *testing.T) {
	none := expand(t, "ansible-run", `playbook: x.yml`)
	if len(none.Steps) != 1 || len(none.Steps[0].Secrets) != 0 {
		t.Fatalf("without a key: %v", ids(none))
	}
	key := expand(t, "ansible-run", `auth: key, ssh_key_store: vault, ssh_key_key: ops/ssh`)
	ref := key.Steps[0].Secrets["ANSIBLE_SSH_KEY"]
	if len(key.Steps) != 1 || ref.Store != "vault" || ref.Key != "ops/ssh" || ref.Field != "private_key" {
		t.Fatalf("with a key: %v %+v", ids(key), ref)
	}
	both := expand(t, "ansible-run", `auth: "key+vault"`)
	if len(both.Steps) != 1 || both.Steps[0].Secrets["ANSIBLE_VAULT_PASSWORD"].Key != "ansible/vault" {
		t.Fatalf("with a key and a vault: %v", ids(both))
	}
}

func TestStatePasswordIsTheReservedStoreAndOpenTofuIsTheDefault(t *testing.T) {
	p := expand(t, "tofu-plan", `workspace: prod`)
	step := p.Steps[0]
	ref := step.Secrets["TF_HTTP_PASSWORD"]
	if ref.Store != "launchway" || ref.Key != "state" || ref.Field != "prod" {
		t.Fatalf("%+v", ref)
	}
	if !strings.Contains(step.Image, "opentofu") {
		t.Fatalf("the default image is %q: the open tool must be the default", step.Image)
	}
	if step.Env["PARAM_TOOL"] != "" && step.Env["PARAM_TOOL"] != "tofu" {
		t.Fatal("the default tool is not tofu")
	}
}
