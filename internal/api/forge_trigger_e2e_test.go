package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// A push to a hosted repository starts the pipeline directly: the push itself
// makes the project that watches the repository look at it (no webhook, no
// waiting for the next poll, which here is an hour away), and the run carries
// the facts of the commit.

const triggerPipeline = `
pipeline: app
supersede: true
on: { push: { branches: [main] } }
steps:
  gate: {kind: approval, message: "Ship it?"}
`

func gitAt(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testlib.GitEnv(t, "GIT_AUTHOR_NAME=Dana Dev", "GIT_AUTHOR_EMAIL=dana@example.com", "GIT_COMMITTER_NAME=Dana Dev", "GIT_COMMITTER_EMAIL=dana@example.com",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPushToAHostedRepositoryStartsExactlyOneRunWithTheRightFacts(t *testing.T) {
	h := sshEnv(t)
	e := h.e
	old := egress.Default.Policy()
	t.Cleanup(func() { _ = egress.Default.Set(old) })
	if err := egress.Default.Set(egress.Policy{AllowLocal: true, Allow: []string{"127.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.Forge.Provider.Repositories().Create(t.Context(), forge.NewRepository{Home: "acme", Name: "shop", Visibility: forge.Public}); err != nil {
		t.Fatal(err)
	}
	ada := e.signIn(t, "ada")
	tok := e.tokenFor(t, "ada", "t")
	key, pub := h.key(t, "ada")
	h.register(t, ada, "a", pub)

	src := t.TempDir()
	gitAt(t, src, "2026-06-01T10:00:00Z", "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(src, ".launchway"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".launchway", "pipeline.yaml"), []byte(triggerPipeline), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, "2026-06-01T10:00:00Z", "add", "-A")
	gitAt(t, src, "2026-06-01T10:00:00Z", "commit", "-q", "-m", "first")
	httpsURL := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	mustGit(t, src, "push", "-q", httpsURL, "HEAD:refs/heads/main")

	// The project that watches the repository polls once an hour: only the push can make it look in time.
	if err := e.api.Projects.Reconfigure([]source.Project{{Name: "shop", URL: e.srv.URL + "/git/acme/shop.git", Poll: time.Hour}}, nil); err != nil {
		t.Fatal(err)
	}
	e.api.Projects.Start(t.Context())
	hostedWait(t, "the project's first look, which records history without running it", func() bool {
		seen, _ := e.eng.SeenRefs("shop")
		return seen["refs/heads/main"] != ""
	})
	runs := func() []engine.RunSummary {
		var rs []engine.RunSummary
		ada.get(e.srv.URL+"/api/v1/runs?project=shop").json(t, &rs)
		return rs
	}
	if n := len(runs()); n != 0 {
		t.Fatalf("history started %d runs", n)
	}

	push := func(name, date string, viaSSH bool) (sha string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		gitAt(t, src, date, "add", "-A")
		gitAt(t, src, date, "commit", "-q", "-m", "add "+name)
		sha = gitAt(t, src, date, "rev-parse", "HEAD")
		if viaSSH {
			h.mustGit(t, key, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
		} else {
			mustGit(t, src, "push", "-q", httpsURL, "HEAD:refs/heads/main")
		}
		return sha
	}
	for i, c := range []struct {
		name, date string
		ssh        bool
	}{{"a.txt", "2026-06-02T11:22:33Z", false}, {"b.txt", "2026-06-03T09:08:07Z", true}} {
		sha := push(c.name, c.date, c.ssh)
		hostedWait(t, "a run for the push", func() bool { return len(runs()) == i+1 })
		time.Sleep(300 * time.Millisecond) // a second run for the same push would show now
		rs := runs()
		if len(rs) != i+1 {
			t.Fatalf("push %d (ssh %v) started %d runs in all, want %d", i+1, c.ssh, len(rs), i+1)
		}
		r := rs[0] // newest first
		if r.Event != "push" || r.Ref != "refs/heads/main" || r.Branch != "main" || r.SHA != sha || r.Author != "Dana Dev" || r.Message != "add "+c.name {
			t.Errorf("push %d: run %+v", i+1, r)
		}
		trig, err := e.eng.RunTrigger(r.ID)
		if err != nil || trig.CommitTime != c.date || trig.Actor == "" {
			t.Errorf("push %d: trigger %+v (%v), want commit time %s", i+1, trig, err, c.date)
		}
	}
	// supersede: the second run replaced the first, which was still waiting at its gate.
	rs := runs()
	if rs[0].Status == engine.RunStatus("canceled") || rs[1].Status != engine.RunStatus("canceled") {
		t.Errorf("supersede: newest %s, older %s", rs[0].Status, rs[1].Status)
	}
	// A commit that says [skip ci] starts nothing, and the decision says why.
	if err := os.WriteFile(filepath.Join(src, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, "2026-06-04T00:00:00Z", "add", "-A")
	gitAt(t, src, "2026-06-04T00:00:00Z", "commit", "-q", "-m", "docs only [Skip CI]")
	mustGit(t, src, "push", "-q", httpsURL, "HEAD:refs/heads/main")
	hostedWait(t, "the decision for the skipped commit", func() bool {
		st, _ := e.api.Projects.Get("shop")
		for _, d := range st.Decisions {
			if d.Ref == "refs/heads/main" && d.Outcome == "ignored" && strings.Contains(d.Detail, "[skip ci]") {
				return true
			}
		}
		return false
	})
	if n := len(runs()); n != 2 {
		t.Errorf("a [skip ci] commit started a run: %d runs", n)
	}
	// A push to a branch the pipeline does not watch starts nothing, and says why.
	mustGit(t, src, "push", "-q", httpsURL, "HEAD:refs/heads/topic")
	hostedWait(t, "the decision for the topic branch", func() bool {
		st, _ := e.api.Projects.Get("shop")
		for _, d := range st.Decisions {
			if d.Ref == "refs/heads/topic" && d.Outcome == "ignored" {
				return true
			}
		}
		return false
	})
	if n := len(runs()); n != 2 {
		t.Errorf("a push to an unwatched branch started a run: %d runs", n)
	}
}

func hostedWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
