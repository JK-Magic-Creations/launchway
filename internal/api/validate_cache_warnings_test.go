package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The server's check, which the editor shows, warns about a cache of build
// output and still accepts the pipeline.
func TestValidateEndpointWarnsAboutUnsafeCaches(t *testing.T) {
	e := setup(t, time.Second)
	text := "pipeline: x\nsteps:\n  build:\n    image: i\n    cache:\n      - { name: out, paths: [target], key: { files: [pom.xml] } }\n    run: make\n"
	resp, err := uiPost(e.srv.URL+"/api/v1/validate", "text/yaml", strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if !out.OK || len(out.Warnings) == 0 || !strings.Contains(out.Warnings[0], "steps.build.cache[0]") || !strings.Contains(out.Warnings[0], "keeps build output (target)") {
		t.Fatalf("%+v", out)
	}
}
