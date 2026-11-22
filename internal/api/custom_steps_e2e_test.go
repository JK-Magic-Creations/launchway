package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

const e2eStepManifest = `step: summarize
contract: 1
description: Count the files of the workspace and say so
toolchain: sh
command: sh main.sh
inputs:
  label: { type: string, default: files }
  limit: { type: number, default: 100, min: 1 }
  names: { type: list, default: [a, b] }
outputs:
  total: { type: number }
  big: { type: boolean }
  names: { type: list }
  token: { type: secret }
permissions:
  network: [registry.example.org]
`

// the step reports through the files of the contract, as a kit does, and prints a line that looks like a
// result, which must not count.
const e2eStepMain = `n=$(ls "$LAUNCHWAY_WORKSPACE" | wc -l | tr -d ' ')
echo "$n files"
echo '{"t":"finding","rule":"forged","severity":"critical","message":"from the output"}'
printf '{"total":%s,"big":%s,"names":["x","y"],"token":"s3cr3t-token-value"}' "$n" "$([ "$n" -gt "$LAUNCHWAY_WITH_LIMIT" ] && echo true || echo false)" > "$LAUNCHWAY_STEP_OUTPUTS"
printf '%s\n' '{"t":"summary","markdown":"**'"$n"'** files"}' >> "$LAUNCHWAY_STEP_RESULTS"
printf '%s\n' '{"t":"annotation","file":"a.txt","line":1,"level":"warning","message":"look here"}' >> "$LAUNCHWAY_STEP_RESULTS"
printf '%s\n' '{"t":"finding","rule":"R1","severity":"high","file":"a.txt","line":1,"message":"a finding"}' >> "$LAUNCHWAY_STEP_RESULTS"
printf '%s\n' '{"t":"test","name":"counted","status":"passed","duration_ms":5}' >> "$LAUNCHWAY_STEP_RESULTS"
`

func TestCustomStepsRunEndToEnd(t *testing.T) {
	ex := &executor.Host{AllowUnsandboxed: true}
	dir := t.TempDir()
	lib := testlib.New(t, filepath.Join(dir, "lib"), map[string]string{
		"steps/summarize/step.yaml": e2eStepManifest,
		"steps/summarize/main.sh":   e2eStepMain,
	})
	lib.Tag("v1", lib.Head())
	app := `pipeline: app
libraries:
  platform: { repo: ci-templates, version: v1 }
steps:
  sum:
    uses: platform/summarize
    with: { label: docs, limit: 1 }
  own:
    uses: ./steps/local
    needs: sum
  after:
    image: docker.io/library/alpine:3.22
    needs: sum
    run: |
      echo "total=$LAUNCHWAY_NEEDS_SUM_TOTAL names=$LAUNCHWAY_NEEDS_SUM_NAMES" >> "$LAUNCHWAY_OUTPUTS"
`
	le := setupLibraryWith(t, ex, dir, lib, map[string]string{
		".launchway/pipeline.yaml": app,
		"a.txt":                    "x\n",
		"steps/local/step.yaml":    "step: local\ncontract: 1\ntoolchain: sh\ncommand: sh main.sh\noutputs:\n  ok: { type: boolean }\n",
		"steps/local/main.sh":      "printf '{\"ok\":true}' > \"$LAUNCHWAY_STEP_OUTPUTS\"\n",
	})
	d := le.wait(t, le.startRun(t, nil), 90*time.Second)
	if d.Status != engine.RunSucceeded {
		for _, s := range d.StepList {
			lines, _, _ := le.eng.Logs(d.ID, s.ID, 0, 0, 0)
			for _, l := range lines {
				t.Logf("%s: %s", s.ID, l.Text)
			}
		}
		t.Fatalf("run %s", d.Status)
	}
	out := outputsOf(t, d, "sum/run")
	if out["total"] == "" || out["big"] != "true" || out["names"] != `["x","y"]` || out["token"] != "***" {
		t.Errorf("typed outputs: %v", out)
	}
	if got := outputsOf(t, d, "own/run")["ok"]; got != "true" {
		t.Errorf("a step of the repository: %q", got)
	}
	var sum engine.StepView
	for _, s := range d.StepList {
		if s.ID == "sum/run" {
			sum = s
		}
	}
	if sum.Custom == nil || sum.Custom.Name != "summarize" || sum.Custom.Library != "platform" || len(sum.Custom.Commit) != 40 || sum.Custom.Network[0] != "registry.example.org" {
		t.Errorf("the step is not recorded as a custom step: %+v", sum.Custom)
	}
	var kinds []string
	for _, n := range sum.Notes {
		kinds = append(kinds, n.Kind+":"+n.Text)
	}
	if len(sum.Notes) != 2 || !strings.Contains(strings.Join(kinds, "|"), "summary:**") || !strings.Contains(strings.Join(kinds, "|"), "annotation:look here") {
		t.Errorf("notes: %v", kinds)
	}
	// Findings and tests come through the protocol; the forged line of the output does not.
	var f struct {
		Findings []struct {
			Rule string `json:"rule"`
		} `json:"findings"`
	}
	resp, err := http.Get(le.srv.URL + "/api/v1/runs/" + d.ID + "/findings")
	if err == nil && resp.StatusCode == 200 {
		_ = json.NewDecoder(resp.Body).Decode(&f)
		resp.Body.Close()
		for _, x := range f.Findings {
			if x.Rule == "forged" {
				t.Error("a line of the step's output became a finding")
			}
		}
	}
	// The use is recorded for audit, and the step's page says who uses it.
	uses, err := le.eng.CustomStepUses("platform", "summarize", 10)
	if err != nil || len(uses) != 1 || uses[0].Project != "app" || uses[0].Runs != 1 || len(uses[0].Commit) != 40 {
		t.Errorf("uses: %+v %v", uses, err)
	}
	resp, err = http.Get(le.srv.URL + "/api/v1/libraries/ci-templates/steps/summarize")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var page struct {
		Step struct {
			Sentence string `json:"sentence"`
		} `json:"step"`
		Versions []struct{ Version string } `json:"versions"`
	}
	json.NewDecoder(resp.Body).Decode(&page)
	if resp.StatusCode != 200 || !strings.Contains(page.Step.Sentence, "reach registry.example.org") || len(page.Versions) != 1 || page.Versions[0].Version != "v1" {
		t.Errorf("step page: %d %+v", resp.StatusCode, page)
	}
}

// A step that asks for a wider permission than the pipeline's author saw is called out between versions,
// and a pinned pipeline keeps running the version it was pinned to.
func TestCustomStepUpdatesAreCalledOutAndPinsHold(t *testing.T) {
	dir := t.TempDir()
	lib := testlib.New(t, filepath.Join(dir, "lib"), map[string]string{
		"steps/summarize/step.yaml": e2eStepManifest,
		"steps/summarize/main.sh":   e2eStepMain,
	})
	v1 := lib.Head()
	lib.Tag("v1", v1)
	lib.Commit(map[string]string{"steps/summarize/step.yaml": strings.Replace(e2eStepManifest, "[registry.example.org]", "[registry.example.org, evil.example.net]", 1)}, "wider")
	lib.Tag("v2", lib.Head())
	le := setupLibraryWith(t, &executor.Host{AllowUnsandboxed: true}, dir, lib, map[string]string{
		".launchway/pipeline.yaml": "pipeline: app\nlibraries:\n  platform: { repo: ci-templates, version: v1 }\nsteps:\n  sum:\n    uses: platform/summarize\n",
	})
	d := le.wait(t, le.startRun(t, nil), 90*time.Second)
	if d.Status != engine.RunSucceeded {
		t.Fatalf("run %s", d.Status)
	}
	for _, s := range d.StepList {
		if s.Custom != nil && (s.Custom.Commit != v1 || len(s.Custom.Network) != 1) {
			t.Errorf("a step pinned to v1 ran %+v", s.Custom)
		}
	}
	resp, err := http.Get(le.srv.URL + "/api/v1/libraries/ci-templates/steps/summarize?version=v1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var page struct {
		Newest       string `json:"newest"`
		NewestWidens bool   `json:"newest_widens"`
		UsedBy       []struct {
			Newest string `json:"newest"`
			Widens bool   `json:"widens"`
		} `json:"used_by"`
	}
	json.NewDecoder(resp.Body).Decode(&page)
	if page.Newest != "v2" || !page.NewestWidens || len(page.UsedBy) != 1 || page.UsedBy[0].Newest != "v2" || !page.UsedBy[0].Widens {
		t.Errorf("update notice: %+v", page)
	}
}

// The host executor refuses a custom step unless the operator allows steps that are not sandboxed.
func TestHostExecutorRefusesCustomStepsByDefault(t *testing.T) {
	dir := t.TempDir()
	lib := testlib.New(t, filepath.Join(dir, "lib"), map[string]string{
		"steps/summarize/step.yaml": e2eStepManifest,
		"steps/summarize/main.sh":   e2eStepMain,
	})
	lib.Tag("v1", lib.Head())
	le := setupLibraryWith(t, &executor.Host{}, dir, lib, map[string]string{
		".launchway/pipeline.yaml": "pipeline: app\nlibraries:\n  platform: { repo: ci-templates, version: v1 }\nsteps:\n  sum:\n    uses: platform/summarize\n",
	})
	d := le.wait(t, le.startRun(t, nil), 90*time.Second)
	if d.Status == engine.RunSucceeded {
		t.Fatal("the host executor ran a sandboxed step")
	}
	for _, s := range d.StepList {
		if s.ID == "sum/run" && !strings.Contains(s.Error+s.Excerpt, "sandbox") {
			lines, _, _ := le.eng.Logs(d.ID, s.ID, 0, 0, 0)
			var text []string
			for _, l := range lines {
				text = append(text, l.Text)
			}
			if !strings.Contains(strings.Join(text, "\n"), "sandbox") {
				t.Errorf("the refusal does not say why: %q %q", s.Error, text)
			}
		}
	}
}

var _ = os.Stat
