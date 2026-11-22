package starters_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The reporting tests above use stand-ins for the test runners. These run the step against the real ones: node's own
// test runner always (it needs only node), and vitest and mocha with LAUNCHWAY_TEST_NETWORK=1, because each is
// installed from the package registry for the test. In each the repository lists no reporter, and the step must still
// leave a JUnit report with the tests in it.

func realProject(t *testing.T, scripts map[string]string, files map[string]string, dev string) string {
	t.Helper()
	d := t.TempDir()
	pkg := `{"name":"x","private":true,"type":"commonjs","scripts":{`
	i := 0
	for k, v := range scripts {
		if i > 0 {
			pkg += ","
		}
		pkg += `"` + k + `":"` + v + `"`
		i++
	}
	pkg += `}` + dev + `}`
	files["package.json"] = pkg
	for name, body := range files {
		p := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func runRealStep(t *testing.T, proj string, env ...string) (string, int) {
	t.Helper()
	script := scriptPath(t, "node/test.sh")
	common, _ := fs_readFile("common.sh")
	if err := os.WriteFile(filepath.Join(filepath.Dir(script), "..", "common.sh"), common, 0o644); err != nil {
		t.Skip("cannot place common.sh")
	}
	cmd := exec.Command("sh", script)
	cmd.Dir = proj
	cmd.Env = append(os.Environ(), append([]string{"HOME=" + t.TempDir()}, env...)...)
	b, err := cmd.CombinedOutput()
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(b), status
}

func wantReport(t *testing.T, proj, out string, status int, names ...string) {
	t.Helper()
	if status != 0 {
		t.Fatalf("exit %d\n%s", status, out)
	}
	b, err := os.ReadFile(filepath.Join(proj, "reports", "junit.xml"))
	if err != nil || len(b) == 0 {
		t.Fatalf("no reports/junit.xml (%v):\n%s", err, out)
	}
	for _, n := range names {
		if !strings.Contains(string(b), n) {
			t.Errorf("the JUnit report lacks the test %q:\n%s", n, b)
		}
	}
	if strings.Contains(out, "NO TEST REPORT WAS PRODUCED") {
		t.Errorf("the step says no report was produced, and there is one:\n%s", out)
	}
}

func TestRealNodeTestRunnerGetsAReport(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	proj := realProject(t, map[string]string{"test": "node --test"}, map[string]string{
		"sum.test.js": "const test=require('node:test');const assert=require('node:assert');test('adds two numbers',()=>assert.equal(1+1,2));test('adds zero',()=>assert.equal(1+0,1));",
	}, "")
	out, status := runRealStep(t, proj, "PARAM_COVERAGE=false")
	wantReport(t, proj, out, status, "adds two numbers", "adds zero")
}

func TestRealVitestAndMochaGetAReport(t *testing.T) {
	if os.Getenv("LAUNCHWAY_TEST_NETWORK") == "" {
		t.Skip("set LAUNCHWAY_TEST_NETWORK=1 to run vitest and mocha for real (they are installed from the registry)")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("no npm")
	}
	t.Run("vitest", func(t *testing.T) {
		proj := realProject(t, map[string]string{"test": "vitest run"}, map[string]string{
			"sum.test.js": "import {test,expect} from 'vitest';test('vitest adds',()=>expect(1+1).toBe(2));",
		}, `,"devDependencies":{"vitest":"3.2.4"}`)
		if out, err := exec.Command("npm", "install", "--prefix", proj, "--no-audit", "--no-fund", "--loglevel=error").CombinedOutput(); err != nil {
			t.Skipf("cannot install vitest: %v\n%s", err, out)
		}
		out, status := runRealStep(t, proj, "PARAM_COVERAGE=false")
		wantReport(t, proj, out, status, "vitest adds")
	})
	t.Run("mocha", func(t *testing.T) {
		proj := realProject(t, map[string]string{"test": "mocha"}, map[string]string{
			"test/sum.test.js": "const assert=require('assert');describe('sum',()=>{it('mocha adds',()=>assert.equal(1+1,2));});",
		}, `,"devDependencies":{"mocha":"11.7.1"}`)
		if out, err := exec.Command("npm", "install", "--prefix", proj, "--no-audit", "--no-fund", "--loglevel=error").CombinedOutput(); err != nil {
			t.Skipf("cannot install mocha: %v\n%s", err, out)
		}
		out, status := runRealStep(t, proj, "PARAM_COVERAGE=false")
		wantReport(t, proj, out, status, "mocha adds")
	})
}
