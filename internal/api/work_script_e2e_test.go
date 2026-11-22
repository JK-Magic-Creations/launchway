package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// The reference agent (examples/agents/docs-agent/agent.sh) runs the whole
// loop against a real server through the real command line, with no model:
// register, label an item, run the script, and a pull request waits for a
// person.
func TestReferenceAgentScript(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the command line")
	}
	e, _ := hostedEnv(t, envOpts{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	src := t.TempDir()
	gitAt(t, src, "2026-06-01T10:00:00Z", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, "2026-06-01T10:00:00Z", "add", "-A")
	gitAt(t, src, "2026-06-01T10:00:00Z", "commit", "-q", "-m", "first")
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/main")

	base := e.srv.URL + "/api/v1/work"
	var reg struct {
		Agent work.Agent
		Token string
	}
	ada.do("POST", base+"/agents", `{"name":"docs","scope":{"kind":"installation"},"projects":["acme/shop"],"labels":["agent:docs"]}`).json(t, &reg)
	var it work.Item
	otto.do("POST", base+"/items?scope=project:acme/shop", `{"title":"Note the review","labels":["agent:docs"]}`).json(t, &it)

	bin := filepath.Join(t.TempDir(), "launchway")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "github.com/jk-magic-creations/launchway/cmd/launchway")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the command: %v\n%s", err, out)
	}
	script, _ := filepath.Abs("../../examples/agents/docs-agent/agent.sh")
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "LAUNCHWAY_SERVER="+e.srv.URL, "LAUNCHWAY_TOKEN="+reg.Token, "AGENT_REPO=acme/shop", "LAUNCHWAY_BIN="+bin, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "asked for a review") {
		t.Fatalf("the script: %v\n%s", err, out)
	}
	var det workDetail
	ada.get(base+"/items/"+it.Ref).json(t, &det)
	if det.State != "in_review" || det.Claim != nil || len(det.Code) != 2 || det.Usage == nil {
		t.Fatalf("%+v", det)
	}
}
