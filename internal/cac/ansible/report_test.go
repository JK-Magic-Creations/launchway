package ansible

import (
	"os"
	"strings"
	"testing"
)

// The fixtures are real reports, written by the launchway_report callback
// from ansible-core 2.19 running the test playbook against two systemd
// containers (see internal/cac/ansible/testdata/README).

func load(t *testing.T, name string, secrets ...string) *Report {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(b, secrets...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFirstRunChangedEverything(t *testing.T) {
	r := load(t, "first-run.json")
	if r.Summary.Recap != "2 hosts: 2 changed, 0 ok, 0 failed" {
		t.Fatalf("recap %q", r.Summary.Recap)
	}
	if r.Failure != nil || len(r.FailedHosts()) != 0 {
		t.Fatalf("unexpected failure %+v", r.Failure)
	}
	if !r.Diff || r.Check {
		t.Fatalf("diff=%v check=%v", r.Diff, r.Check)
	}
	if _, ok := r.Drift(); ok {
		t.Fatal("a real run has no drift to report")
	}
	if len(r.Hosts[0].ChangedTasks) < 3 {
		t.Fatalf("changed tasks %v", r.Hosts[0].ChangedTasks)
	}
}

func TestIdempotentSecondRun(t *testing.T) {
	r := load(t, "idempotent.json")
	if r.Summary.Changed != 0 || r.Summary.OK != 2 {
		t.Fatalf("second run: %+v", r.Summary)
	}
}

func TestFailedHostIsFirstAndRetryable(t *testing.T) {
	r := load(t, "one-failed.json")
	if r.Failure == nil || r.Failure.Host != "lw-cactest-2" || !strings.Contains(r.Failure.Msg, "told to fail") {
		t.Fatalf("failure %+v", r.Failure)
	}
	if got := r.FailedHosts(); len(got) != 1 || got[0] != "lw-cactest-2" {
		t.Fatalf("hosts to run again: %v", got)
	}
	if r.Summary.Failed != 1 || r.Summary.OK != 1 {
		t.Fatalf("summary %+v", r.Summary)
	}
}

func TestCheckModeDrift(t *testing.T) {
	r := load(t, "check-drift.json")
	d, ok := r.Drift()
	if !ok || d.Drifted != 2 || d.Clean {
		t.Fatalf("drift %+v ok=%v", d, ok)
	}
	if !strings.Contains(d.Text, "2 hosts drifted") || !strings.Contains(d.Text, "Install the demo configuration file") {
		t.Fatalf("text %q", d.Text)
	}
	if !strings.Contains(r.Summary.Recap, "would change") {
		t.Fatalf("a check run must not say it changed things: %q", r.Summary.Recap)
	}
}

// The canary: the token the playbook wrote under no_log is nowhere in the
// parsed report, and a secret the caller names is removed from messages.
func TestSecretsNeverSurvive(t *testing.T) {
	for _, f := range []string{"first-run.json", "idempotent.json", "one-failed.json", "check-drift.json"} {
		b, _ := os.ReadFile("testdata/" + f)
		if strings.Contains(string(b), "CANARY-s3cr3t") {
			t.Fatalf("%s: the callback let the canary through", f)
		}
	}
	in := `{"format":"launchway-ansible-report/1","plays":[{"name":"p","tasks":[
	 {"name":"a","no_log":true,"results":{"h":{"status":"failed","msg":"password=hunter22hunter","diff":[{"text":"CANARY-x"}]}}},
	 {"name":"b","results":{"h":{"status":"failed","msg":"curl https://u:pw0rd@host token=abcd1234efgh and CANARY-lit"}}}]}],"stats":{"h":{"failures":2}}}`
	r, err := Parse([]byte(in), "CANARY-lit")
	if err != nil {
		t.Fatal(err)
	}
	out := r.Plays[0].Tasks[0].Results["h"]
	if out.Msg == "password=hunter22hunter" || len(out.Diff) != 0 {
		t.Fatalf("no_log result kept: %+v", out)
	}
	msg := r.Plays[0].Tasks[1].Results["h"].Msg
	for _, bad := range []string{"pw0rd", "abcd1234efgh", "CANARY-lit"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("%q still in %q", bad, msg)
		}
	}
}

func TestHostileInput(t *testing.T) {
	for name, in := range map[string]string{
		"not json":       `nope`,
		"wrong format":   `{"format":"x"}`,
		"unknown status": `{"format":"launchway-ansible-report/1","plays":[{"name":"p","tasks":[{"name":"t","results":{"h":{"status":"exploded"}}}]}]}`,
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
