package source

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// What a server's check of a pipeline text gives, and what the command line
// gives from the library's directory, are the same steps.
func TestOfflineExpansionEqualsTheServersCheck(t *testing.T) {
	s := newLibSetup(t, testlib.AppPipeline("ci-templates", "v1"), nil)
	text := testlib.AppPipeline("ci-templates", "v1")
	server, err := s.m.Check(context.Background(), text, nil)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := pipeline.ExpandOffline([]byte(text), map[string]string{"platform": s.lib.Work}, pipeline.BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(server.Steps)
	b, _ := json.Marshal(offline.Steps)
	if string(a) != string(b) || len(server.Steps) < 2 {
		t.Fatalf("server steps:\n%s\noffline steps:\n%s", a, b)
	}
}
