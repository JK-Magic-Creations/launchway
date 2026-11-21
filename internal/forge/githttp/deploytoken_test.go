package githttp

import (
	"strings"
	"testing"
	"time"
)

func TestDeployTokens(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	rt := RunTokens{Key: []byte("0123456789abcdef0123"), Now: func() time.Time { return now }}
	c := DeployClaim{Repo: "r1", Project: "mcp-weather", Deployment: "dep_1", Branch: "main", Mode: ModePullRequest, Merge: "auto", Method: "squash"}
	tok, err := rt.MintForDeploy(c, 30*time.Minute, 3*time.Hour)
	if err != nil || !strings.HasPrefix(tok, DeployTokenPrefix) {
		t.Fatal(tok, err)
	}
	got, err := rt.VerifyDeploy(tok)
	if err != nil || got.Project != "mcp-weather" || got.Merge != "auto" || got.Method != "squash" || got.WorkBranch() != "launchway/deploy/dep_1" || !rt.CanPush(got) {
		t.Fatalf("%+v %v", got, err)
	}
	// After the push window the token still answers about the pull request, until its status window ends.
	later := rt
	later.Now = func() time.Time { return now.Add(time.Hour) }
	if g, err := later.VerifyDeploy(tok); err != nil || later.CanPush(g) {
		t.Fatalf("an hour later: %+v %v", g, err)
	}
	later.Now = func() time.Time { return now.Add(4 * time.Hour) }
	if _, err := later.VerifyDeploy(tok); err == nil {
		t.Fatal("a token past its window was accepted")
	}
	// A run token, a token made with another key, and a changed token are not deploy tokens.
	run, _ := rt.MintForRun("r1", "run1", time.Hour)
	other, _ := RunTokens{Key: []byte("another key another key")}.MintForDeploy(c, time.Hour, time.Hour)
	changed := strings.Replace(tok, tok[len(tok)-3:], "AAA", 1)
	for name, bad := range map[string]string{"run token": run, "other key": other, "changed": changed, "empty": ""} {
		if _, err := rt.VerifyDeploy(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := rt.Verify(tok); err == nil {
		t.Error("a deploy token was accepted as a run token")
	}
	for name, mod := range map[string]func(*DeployClaim){"mode": func(c *DeployClaim) { c.Mode = "x" }, "branch": func(c *DeployClaim) { c.Branch = "" }, "separator": func(c *DeployClaim) { c.Project = "a\x1fb" }} {
		cc := c
		mod(&cc)
		if _, err := rt.MintForDeploy(cc, time.Hour, time.Hour); err == nil {
			t.Errorf("%s: minted", name)
		}
	}
	if _, err := rt.MintForDeploy(c, 2*time.Hour, 3*time.Hour); err == nil {
		t.Error("a push window of two hours was allowed")
	}
}
