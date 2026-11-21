package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The command line's quality bar (docs/development/interface-quality.md): help for every command that works
// without a server, documented and tested exit codes, no color off a terminal or with NO_COLOR, output that reads
// without color, and errors that say what to do next.

// cli runs this program as the command line, with no credentials and an empty configuration directory.
func cli(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LAUNCHWAY_TEST_RUN_CLI=1", "LAUNCHWAY_CONFIG_DIR="+t.TempDir(), "LAUNCHWAY_SERVER=", "LAUNCHWAY_TOKEN=")
	cmd.Env = append(cmd.Env, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

// Every command named in the usage text answers --help with its help and exit 0, with no server and no sign-in.
func TestEveryCommandHasHelpThatNeedsNoServer(t *testing.T) {
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^  launchway ([a-z-]+)`).FindAllStringSubmatch(usage, -1) {
		names = append(names, m[1])
	}
	if len(names) < 20 {
		t.Fatalf("found only %d commands in the usage text: %v", len(names), names)
	}
	for _, name := range names {
		if name == "version" {
			continue
		}
		for _, flag := range []string{"--help", "-h"} {
			out, errs, code := cli(t, t.TempDir(), nil, name, flag)
			text := out + errs
			if code != 0 {
				t.Errorf("launchway %s %s: exit %d, want 0:\n%s", name, flag, code, text)
			}
			if len(strings.Fields(text)) < 5 {
				t.Errorf("launchway %s %s printed no help:\n%s", name, flag, text)
			}
			if strings.Contains(text, "not signed in") {
				t.Errorf("launchway %s %s asks for a sign-in before it helps:\n%s", name, flag, text)
			}
		}
	}
	// A subcommand's help too, the one most often asked for.
	for _, args := range [][]string{{"runs", "list", "--help"}, {"runs", "start", "-h"}, {"approvals", "approve", "--help"}} {
		if _, errs, code := cli(t, t.TempDir(), nil, args...); code != 0 || errs == "" {
			t.Errorf("launchway %v: exit %d, help %q", args, code, errs)
		}
	}
}

// The exit codes in the documentation are the ones the code uses, and each is explained.
func TestExitCodesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "cli.md"))
	if err != nil {
		t.Fatal(err)
	}
	table := string(doc)[strings.Index(string(doc), "## Exit codes"):]
	table = table[:strings.Index(table[3:], "\n## ")+3]
	for code, want := range map[int]string{0: "Success", 1: "failed", exitUsage: "written wrongly", 3: "canceled", exitAPI: "refused", exitTimeout: "time passed", 130: "Ctrl-C"} {
		re := regexp.MustCompile("(?m)^\\| `" + strconv.Itoa(code) + "` \\|.*" + want)
		if !re.MatchString(table) {
			t.Errorf("docs/reference/cli.md's exit code table has no row for %d mentioning %q", code, want)
		}
	}
}

// The exit code a command ends with is what the table says: 2 for a command written wrongly (an unknown command, a
// missing sign-in), 0 for success; nothing else is used by the cases a person meets first.
func TestExitCodesOfCommonCases(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		args []string
		code int
		say  string
	}{
		{nil, 2, "Launchway: build and deliver software."},
		{[]string{"nonsense"}, 2, "unknown command"},
		{[]string{"runs", "list"}, 2, "not signed in: run launchway login"},
		{[]string{"version"}, 0, "launchway"},
		{[]string{"help"}, 0, "Launchway: build and deliver software."},
	} {
		out, errs, code := cli(t, dir, nil, c.args...)
		if code != c.code || !strings.Contains(out+errs, c.say) {
			t.Errorf("launchway %v: exit %d (want %d), output %q should contain %q", c.args, code, c.code, out+errs, c.say)
		}
	}
}

// Off a terminal, or with NO_COLOR, nothing is colored; and what the colors would say is also in words.
func TestLocalRunHasNoColorOffATerminalAndStatusInWords(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".launchway"), 0o755); err != nil {
		t.Fatal(err)
	}
	pipeline := "pipeline: t\nsteps:\n  ok:\n    image: alpine:3.22\n    run: echo fine\n  bad:\n    image: alpine:3.22\n    run: exit 3\n"
	if err := os.WriteFile(filepath.Join(dir, ".launchway", "pipeline.yaml"), []byte(pipeline), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, env := range [][]string{nil, {"NO_COLOR=1"}, {"TERM=dumb"}} {
		out, errs, code := cli(t, dir, env, "run", "--executor", "host")
		if code != 1 {
			t.Errorf("env %v: a failed run exits 1, got %d\n%s", env, code, out+errs)
		}
		if strings.Contains(out+errs, "\x1b[") {
			t.Errorf("env %v: the output has color codes off a terminal:\n%q", env, out)
		}
		// Every outcome is a word, not only a symbol or a color.
		for _, w := range []string{"succeeded", "failed", "Failed"} {
			if !strings.Contains(out, w) {
				t.Errorf("env %v: the output never says %q:\n%s", env, w, out)
			}
		}
	}
}

// The sections and subcommands added after the first release answer --help with exit 0 and no server too.
func TestNewSubcommandsHaveHelpThatNeedsNoServer(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "security"}, {"doctor", "encryption"}, {"doctor", "ssh"}, {"doctor", "database"},
		{"deps", "list"}, {"deps", "check"}, {"deps", "update"}, {"deps", "ignore"},
		{"updates", "preview"}, {"updates", "run"}, {"updates", "settings"},
		{"repos", "import-all"}, {"runs", "trust"}, {"runs", "queue"}, {"runs", "lanes"},
		{"validate"}, {"install"}, {"notifications"}, {"users", "reset"}, {"encryption", "init"},
	} {
		out, errs, code := cli(t, t.TempDir(), nil, append(args, "--help")...)
		if code != 0 || len(strings.Fields(out+errs)) < 4 {
			t.Errorf("launchway %s --help: exit %d, output %q", strings.Join(args, " "), code, out+errs)
		}
	}
}
