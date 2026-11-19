package api

import (
	"crypto/elliptic"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/cryptoutils"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
	"github.com/jk-magic-creations/launchway/internal/imagebuild/imagetest"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

const imageE2EPipeline = `
pipeline: demo
steps:
  prepare:
    image: none
    run: |
      mkdir -p app
      echo "hello from run $LAUNCHWAY_RUN_NUMBER at $(date +%s%N)" > app/greeting.txt
      printf 'FROM docker.io/library/alpine:3.22\nARG VERSION\nCOPY greeting.txt /greeting.txt\nRUN --mount=type=secret,id=npm echo "the token is $(cat /run/secrets/npm)"\nRUN echo "$VERSION" > /version\n' > app/Dockerfile
      echo version=2.0.1 >> "$LAUNCHWAY_OUTPUTS"
  image:
    kind: image
    needs: prepare
    context: app
    build_args: { VERSION: { output: prepare.version } }
    secrets: [{ id: npm, secret: { store: files, key: npm } }]
    tags: [{ registry: secure, repository: team/demo, tag: { run: number } }]
    sbom: true
    sign: { key: { store: files, key: cosign.key }, password: { store: files, key: cosign.password } }
    scan: { fail_on: critical, mode: report }
  smoke:
    image: none
    needs: image
    run: |
      test -n "$LAUNCHWAY_NEEDS_IMAGE_DIGEST"
      echo "deploying $LAUNCHWAY_NEEDS_IMAGE_IMAGE"
`

// An image step from a pipeline to the registry and back through the API,
// with a real runner, a rootless BuildKit and a registry with TLS and a
// password: the build secret, the registry's password and the signing
// key's password are in no log line, API answer or database column.
func TestImageStepEndToEnd(t *testing.T) {
	imagetest.Skip(t)
	e := setupStore(t)
	reg := imagetest.StartRegistry(t, imagetest.RegistryOptions{Auth: true, TLS: imagetest.NewCA(t)})
	bk := imagetest.StartBuildKit(t, imagetest.BuildKitOptions{CAs: map[string]string{reg.Host: reg.CAFile}})

	dir := t.TempDir()
	npm := "npm_" + strings.Repeat("Q9", 12) + fmt.Sprint(time.Now().UnixNano()%1000000)
	keyPassword := "cosign-pw-" + fmt.Sprint(time.Now().UnixNano())
	priv, _, err := cryptoutils.GeneratePEMEncodedECDSAKeyPair(elliptic.P256(), cryptoutils.StaticPasswordFunc([]byte(keyPassword)))
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"npm": npm, "harbor": reg.Password, "cosign.key": string(priv), "cosign.password": keyPassword} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stores := secrets.NewResolver()
	stores.Add("files", secrets.File{Dir: dir}, nil, nil)
	r := e.newRunner("builder")
	r.Secrets = stores
	r.Images = &imagebuild.Config{
		Builders: []imagebuild.BuilderConfig{{Name: "local", Address: bk.Address, CAFile: bk.CAFile, CertFile: bk.CertFile,
			KeyFile: bk.KeyFile, ServerName: bk.ServerName}},
		Registries: map[string]imagebuild.RegistryConfig{"secure": {Host: reg.Host, CAFile: reg.CAFile,
			Credentials: &imagebuild.Credentials{Username: reg.User, Password: pipeline.SecretRef{Store: "files", Key: "harbor"}},
			Push:        []imagebuild.PushRule{{Projects: []string{"demo"}, Repositories: []string{"team/*"}}}}},
	}
	if err := r.Images.Check(); err != nil {
		t.Fatal(err)
	}
	// A runner without builders takes none of the run's steps.
	plain := e.newRunner("plain")
	e.startRunner(t, plain)
	id := e.start(t, imageE2EPipeline)
	time.Sleep(2 * time.Second)
	if d, _ := e.eng.GetRun(id); d.StepList[0].Status != engine.StepQueuedS {
		t.Fatalf("a runner without builders took %s: %s", d.StepList[0].ID, d.StepList[0].Status)
	}
	e.startRunner(t, r)
	d := e.wait(t, id, 15*time.Minute)
	if d.Status != "succeeded" {
		for _, s := range d.StepList {
			t.Logf("%s %s %s\n%s", s.ID, s.Status, s.Error, stepLogs(t, e, id, s.ID, max(s.Attempt, 1)))
		}
		t.Fatalf("run %s", d.Status)
	}

	var imgs struct {
		Images []engine.ImageView `json:"images"`
	}
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/images", &imgs)
	if len(imgs.Images) != 1 {
		t.Fatalf("images: %+v", imgs)
	}
	img := imgs.Images[0]
	if !img.Pushed || !img.Signed || !img.SBOM || img.Provenance != "attached" || img.Scan == nil || img.Scan.Mode != "report" ||
		img.SBOMArtifact != "sbom" || img.ProvenanceArtifact != "provenance" || img.Image != reg.Host+"/team/demo@"+img.Digest ||
		len(img.Tags) != 1 || img.Tags[0] != reg.Host+"/team/demo:1" {
		t.Fatalf("image: %+v", img)
	}
	for _, s := range d.StepList {
		if s.ID == "image" && (s.Outputs["digest"] != img.Digest || s.Outputs["pushed"] != "true") {
			t.Fatalf("outputs: %v", s.Outputs)
		}
	}
	if l := stepLogs(t, e, id, "smoke", 1); !strings.Contains(l, "deploying "+img.Image) {
		t.Fatalf("smoke log: %s", l)
	}
	var hits struct {
		Hits []engine.ImageHit `json:"hits"`
	}
	getJSON(t, e.srv.URL+"/api/v1/images?digest="+img.Digest, &hits)
	if len(hits.Hits) == 0 || hits.Hits[0].RunID != id {
		t.Fatalf("search by digest: %+v", hits)
	}
	getJSON(t, e.srv.URL+"/api/v1/images?repository="+reg.Host+"/team/demo", &hits)
	if len(hits.Hits) != 1 || hits.Hits[0].Tag != "1" || !hits.Hits[0].Signed {
		t.Fatalf("search by repository: %+v", hits)
	}
	// The SBOM and the provenance are artifacts; the scan's results are
	// findings, and the SBOM's components can be searched.
	var arts struct {
		Artifacts []engine.ArtifactView `json:"artifacts"`
	}
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/artifacts", &arts)
	names := map[string]int{}
	for _, a := range arts.Artifacts {
		names[a.Name] = len(a.Files)
	}
	if names["sbom"] != 1 || names["provenance"] != 2 {
		t.Fatalf("artifacts: %v", names)
	}
	var rf struct {
		SBOMs []engine.StepSBOM `json:"sboms"`
		Tools []string          `json:"tools"`
	}
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/findings", &rf)
	if len(rf.SBOMs) != 1 || rf.SBOMs[0].Components == 0 || len(rf.Tools) != 1 || !strings.EqualFold(rf.Tools[0], "trivy") {
		t.Fatalf("findings view: %+v", rf)
	}

	// No secret anywhere.
	logs := stepLogs(t, e, id, "image", 1)
	if !strings.Contains(logs, "the token is ***") {
		t.Fatalf("the build's output of the secret is not masked as expected:\n%s", logs)
	}
	everything := logs + dumpDatabase(t, e.db) + mustGet(t, e.srv.URL+"/api/v1/runs/"+id) + mustGet(t, e.srv.URL+"/api/v1/runs/"+id+"/images")
	for name, v := range map[string]string{"build secret": npm, "registry password": reg.Password, "key password": keyPassword,
		"private key": strings.Split(string(priv), "\n")[1]} {
		if strings.Contains(everything, v) {
			t.Fatalf("the %s is in a log, an API answer or the database", name)
		}
	}
}

func mustGet(t *testing.T, url string) string {
	t.Helper()
	var v any
	getJSON(t, url, &v)
	return fmt.Sprint(v)
}
