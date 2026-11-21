package api

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/cac/tfplan"
	"github.com/jk-magic-creations/launchway/internal/cac/tfstate"
)

// These tests run the real OpenTofu (from its official image, with podman)
// through the library's script against the built-in state backend of a real
// server. They need containers and the network (providers are downloaded),
// so they run only with LAUNCHWAY_TEST_CONTAINERS=1.

const tofuImage = "ghcr.io/opentofu/opentofu:1.10"

func needTofu(t *testing.T) {
	t.Helper()
	if os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman is not installed")
	}
	if err := exec.Command("podman", "image", "exists", tofuImage).Run(); err != nil {
		t.Skipf("the image %s is not pulled (podman pull %s)", tofuImage, tofuImage)
	}
}

// scratch makes a directory on the home file system (not /tmp).
func scratch(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".cache", "cac-scratch", "tests")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := os.MkdirTemp(base, "tofu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "unshare", "rm", "-rf", d).Run(); _ = os.RemoveAll(d) })
	return d
}

type tofuRun struct {
	t       *testing.T
	ws, lib string
	addr    string
	pass    string
}

func (r *tofuRun) run(mode string, env ...string) (string, error) {
	r.t.Helper()
	args := []string{"run", "--rm", "--network=host", "-v", r.ws + ":/work:Z", "-v", r.lib + ":/lw:ro,Z", "-w", "/work",
		"-e", "PARAM_MODE=" + mode, "-e", "PARAM_WORKSPACE=prod", "-e", "LAUNCHWAY_COMMIT=" + commitOf(env),
		"-e", "TF_HTTP_ADDRESS=" + r.addr, "-e", "TF_HTTP_LOCK_ADDRESS=" + r.addr + "/lock", "-e", "TF_HTTP_UNLOCK_ADDRESS=" + r.addr + "/lock",
		"-e", "TF_HTTP_LOCK_METHOD=LOCK", "-e", "TF_HTTP_UNLOCK_METHOD=UNLOCK", "-e", "TF_HTTP_USERNAME=launchway", "-e", "TF_HTTP_PASSWORD=" + r.pass}
	for _, e := range env {
		if !strings.HasPrefix(e, "LAUNCHWAY_COMMIT=") {
			args = append(args, "-e", e)
		}
	}
	args = append(args, "--entrypoint", "sh", tofuImage, "/lw/scripts/tofu/run.sh")
	var out bytes.Buffer
	cmd := exec.Command("podman", args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(8 * time.Minute):
		_ = cmd.Process.Kill()
		r.t.Fatalf("tofu timed out:\n%s", out.String())
		return "", nil
	}
}

func commitOf(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "LAUNCHWAY_COMMIT="); ok {
			return v
		}
	}
	return "c0ffee"
}

func TestOpenTofuThroughTheBuiltInStateBackend(t *testing.T) {
	needTofu(t)
	e := setupAuth(t, envOpts{})
	e.api.Secrets = testSecrets(t, e.eng)
	ada := e.signIn(t, "ada")
	_, tok := ada.newToken(e.srv.URL, "tofu")

	ws := scratch(t)
	src, err := os.ReadFile("../../test/cac/tffixture/main.tf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "main.tf"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	lib, _ := filepath.Abs("../starters/library")
	r := &tofuRun{t: t, ws: ws, lib: lib, addr: e.srv.URL + "/api/state/v1/pay-api/prod", pass: tok}
	api := e.srv.URL + "/api/v1/projects/pay-api/state/prod"
	planFile := func() ([]byte, []byte) {
		p, err := os.ReadFile(filepath.Join(ws, ".launchway/tofu/plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		m, _ := os.ReadFile(filepath.Join(ws, ".launchway/tofu/meta.json"))
		return p, m
	}

	// 1. plan: nothing is in the state yet, four things would be created.
	out, err := r.run("plan", "LAUNCHWAY_COMMIT=aaaa")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "state: the built-in backend") {
		t.Fatalf("the backend override was not used:\n%s", out)
	}
	pj, meta := planFile()
	plan, err := tfplan.Parse(pj)
	if err != nil || plan.Counts.Add != 4 || plan.Counts.Destroy != 0 {
		t.Fatalf("plan: %v %+v", err, plan)
	}
	if !bytes.Contains(meta, []byte(`"exit_code":2`)) || !bytes.Contains(meta, []byte(`"commit":"aaaa"`)) {
		t.Fatalf("meta %s", meta)
	}

	// 2. apply the reviewed plan; the state lands in the server, sealed, as version 1.
	if out, err := r.run("apply", "LAUNCHWAY_COMMIT=aaaa"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	var vs struct {
		Versions []tfstate.Version `json:"versions"`
	}
	vr := ada.do("GET", api+"/versions", "")
	vr.json(t, &vs)
	if len(vs.Versions) == 0 || vs.Versions[0].Resources < 4 {
		t.Fatalf("versions after apply: %s", vr.body)
	}
	first := vs.Versions[len(vs.Versions)-1].Version
	nAfterApply := len(vs.Versions)

	// 3. a second plan finds nothing to do.
	if out, err := r.run("plan", "LAUNCHWAY_COMMIT=aaaa"); err != nil {
		t.Fatalf("plan 2: %v\n%s", err, out)
	}
	pj, _ = planFile()
	if p2, err := tfplan.Parse(pj); err != nil || p2.Summary != "no changes: the infrastructure matches the code" {
		t.Fatalf("the second plan: %v %v", err, p2)
	}

	// 4. a stale plan is refused: plan A, then a different change lands, then A is applied.
	if out, err := r.run("plan", "LAUNCHWAY_COMMIT=aaaa", `PARAM_VARS={"name":"alpha"}`); err != nil {
		t.Fatalf("plan A: %v\n%s", err, out)
	}
	stale, err := os.ReadFile(filepath.Join(ws, ".launchway/tofu/plan.bin"))
	if err != nil {
		t.Fatal(err)
	}
	staleMeta, _ := os.ReadFile(filepath.Join(ws, ".launchway/tofu/meta.json"))
	if out, err := r.run("plan", "LAUNCHWAY_COMMIT=aaaa", `PARAM_VARS={"pet_count":3}`); err != nil {
		t.Fatalf("plan B: %v\n%s", err, out)
	}
	if out, err := r.run("apply", "LAUNCHWAY_COMMIT=aaaa"); err != nil {
		t.Fatalf("apply B: %v\n%s", err, out)
	}
	// Put plan A back and try to apply it.
	_ = os.WriteFile(filepath.Join(ws, ".launchway/tofu/plan.bin"), stale, 0o600)
	_ = os.WriteFile(filepath.Join(ws, ".launchway/tofu/meta.json"), staleMeta, 0o600)
	out, err = r.run("apply", "LAUNCHWAY_COMMIT=aaaa")
	if err == nil || !strings.Contains(out, "stale plan: the state changed") {
		t.Fatalf("a stale plan was not refused (err %v):\n%s", err, out)
	}
	// And a plan for another commit is refused too.
	_ = os.WriteFile(filepath.Join(ws, ".launchway/tofu/plan.bin"), stale, 0o600)
	_ = os.WriteFile(filepath.Join(ws, ".launchway/tofu/meta.json"), bytes.Replace(staleMeta, []byte("aaaa"), []byte("bbbb"), 1), 0o600)
	out, err = r.run("apply", "LAUNCHWAY_COMMIT=aaaa")
	if err == nil || !strings.Contains(out, "stale plan: it was made for commit bbbb") {
		t.Fatalf("a plan of another commit was not refused (err %v):\n%s", err, out)
	}

	// 5. lock contention: someone holds the lock, the run waits and then fails with the holder's name.
	if lr := ada.do("POST", api+"/lock", `{"reason":"maintenance window"}`); lr.status != 201 {
		t.Fatalf("lock: %d %s", lr.status, lr.body)
	}
	out, err = r.run("plan", "LAUNCHWAY_COMMIT=aaaa", "PARAM_LOCK_TIMEOUT=3s")
	if err == nil || !strings.Contains(strings.ToLower(out), "lock") {
		t.Fatalf("a locked state did not stop the plan (err %v):\n%s", err, out)
	}
	if ur := ada.do("DELETE", api+"/lock?reason=window+over", ""); ur.status != 200 {
		t.Fatalf("unlock: %d %s", ur.status, ur.body)
	}

	// 6. rollback to the version after the first apply: state goes back, nothing real changes,
	// and the next plan shows the difference.
	rb := ada.do("POST", api+"/rollback", `{"version":`+jint(first)+`,"reason":"test the rollback"}`)
	if rb.status != 200 {
		t.Fatalf("rollback: %d %s", rb.status, rb.body)
	}
	vr = ada.do("GET", api+"/versions", "")
	vr.json(t, &vs)
	if len(vs.Versions) <= nAfterApply || vs.Versions[0].Source != "rollback" {
		t.Fatalf("versions after rollback: %s", vr.body)
	}
	// The third pet was applied before the rollback and is no longer in the state: a plan that
	// still asks for three pets wants to add one, and says so. No real resource changed.
	if out, err := r.run("plan", "LAUNCHWAY_COMMIT=aaaa", `PARAM_VARS={"pet_count":3}`); err != nil {
		t.Fatalf("plan after rollback: %v\n%s", err, out)
	}
	pj, _ = planFile()
	if p3, err := tfplan.Parse(pj); err != nil || p3.Counts.Add != 1 {
		t.Fatalf("after the rollback the plan should add the third pet: %v %+v", err, p3)
	}

	// 7. what tofu generated is a secret, and it is nowhere in the database.
	pull := ada.do("GET", api+"/pull?reason=canary+check", "")
	var doc struct {
		Resources []struct {
			Type      string `json:"type"`
			Instances []struct {
				Attributes map[string]any `json:"attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	pull.json(t, &doc)
	secret := ""
	for _, rs := range doc.Resources {
		if rs.Type == "random_password" && len(rs.Instances) > 0 {
			secret, _ = rs.Instances[0].Attributes["result"].(string)
		}
	}
	if len(secret) < 16 {
		t.Fatalf("no generated password found in the state")
	}
	if strings.Contains(dumpDatabase(t, e.db), secret) {
		t.Fatal("the generated password is in the database in the clear")
	}
	show := ada.do("GET", api, "")
	if strings.Contains(string(show.body), secret) {
		t.Fatal("the state page shows the secret")
	}
	_ = json.Valid
}

func jint(n int) string { b, _ := json.Marshal(n); return string(b) }
