package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// The release template of the example library expands, its scripts exist, and its token comes from a
// secret store the pipeline names, never a value written in the file.
func TestReleasePublishTemplateExpands(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "library")
	b, err := os.ReadFile(filepath.Join(dir, TemplatePath("release-publish")))
	if err != nil {
		t.Fatal(err)
	}
	src := `pipeline: demo
libraries:
  platform: { repo: demo-library, version: v1 }
steps:
  release:
    uses: platform/release-publish
    with: { image: registry.example/tools/launchway:1, server: "https://launchway.example", publish: "true", files: "dist:dist/shop" }
`
	p := mustExpand(t, src, map[string]string{"release-publish": string(b)})
	d, pub := p.Step("release/draft"), p.Step("release/publish")
	if d == nil || pub == nil || len(pub.Needs) != 1 || pub.Needs[0] != "release/draft" {
		t.Fatalf("steps: %+v %+v", d, pub)
	}
	if d.Env["RELEASE_SERVER"] != "https://launchway.example" || d.Env["RELEASE_FILES"] != "dist:dist/shop" || pub.Env["RELEASE_PUBLISH"] != "true" {
		t.Fatalf("env: %v %v", d.Env, pub.Env)
	}
	for _, s := range []*Step{d, pub} {
		ref := s.Secrets["RELEASE_TOKEN"]
		if ref.Store != "vault" || ref.Key != "ci/launchway" || ref.Field != "token" {
			t.Errorf("%s: the token reference is %+v", s.ID, ref)
		}
	}
	for _, f := range []string{"scripts/release-draft.sh", "scripts/release-publish.sh"} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil || info.Mode()&0o111 == 0 {
			t.Errorf("%s: %v %v", f, err, info)
		}
	}
}
