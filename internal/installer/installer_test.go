package installer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEnv is a machine with nothing on it: no systemd, no container engine, not root.
type fakeEnv struct {
	Env
	out      *bytes.Buffer
	started  [][]string
	commands []string
	open     map[string]bool
	programs map[string]bool
}

func newFake(t *testing.T) *fakeEnv {
	t.Helper()
	f := &fakeEnv{out: &bytes.Buffer{}, open: map[string]bool{}, programs: map[string]bool{}}
	home := t.TempDir()
	f.Env = Env{In: strings.NewReader(""), Out: f.out, GOOS: "linux", UID: 1000, Home: home, Root: t.TempDir(),
		Getenv:   func(string) string { return "" },
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		Version:  "v9.9.9",
		Binary:   filepath.Join(home, "launchway-bin")}
	_ = os.WriteFile(f.Binary, []byte("#!/bin/sh\n"), 0o755)
	f.LookPath = func(n string) (string, error) {
		if f.programs[n] {
			return "/usr/bin/" + n, nil
		}
		return "", errors.New("not found")
	}
	f.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.commands = append(f.commands, name+" "+strings.Join(args, " "))
		if name == "id" {
			return nil, errors.New("no such user")
		}
		return nil, nil
	}
	f.Start = func(name string, args []string, env []string, dir, log string) (int, error) {
		f.started = append(f.started, append([]string{name}, args...))
		// the server "starts": its address now answers
		return 4242, nil
	}
	f.Reachable = func(addr string) bool { return f.open[addr] }
	return f
}

func (f *fakeEnv) run(t *testing.T, answers map[string]string, o Options) (Result, error) {
	t.Helper()
	o.Answers = answers
	o.NoBrowser = true
	o.TranscriptTo = &bytes.Buffer{}
	// Starting the fake server opens its port.
	start := f.Start
	f.Start = func(name string, args []string, env []string, dir, log string) (int, error) {
		pid, err := start(name, args, env, dir, log)
		if a, ok := answers["local.port"]; ok {
			f.open["127.0.0.1:"+a] = true
		}
		return pid, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return Run(ctx, f.Env, o)
}

func TestAskerUnattendedNeedsRequiredAnswer(t *testing.T) {
	f := newFake(t)
	a := NewAsker(&f.Env, nil, NewTranscript(nil, nil))
	_, err := a.Ask(Question{Key: "x", Prompt: "p", Required: true})
	var na *NeedsAnswer
	if !errors.As(err, &na) || na.Q.Key != "x" {
		t.Fatalf("want NeedsAnswer, got %v", err)
	}
}

func TestAskerRejectsUnavailableChoice(t *testing.T) {
	f := newFake(t)
	a := NewAsker(&f.Env, map[string]string{"target": "small-kubernetes"}, NewTranscript(nil, nil))
	_, err := a.Ask(Question{Key: "target", Choices: []Choice{{Value: "small-kubernetes", Unavailable: "coming"}}})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("a menu entry that is not built must not be selectable: %v", err)
	}
}

func TestSmallKubernetesIsShownAsComingAndCannotBeChosen(t *testing.T) {
	f := newFake(t)
	_, err := f.run(t, map[string]string{"target": "small-kubernetes"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

func TestInteractiveMenuShowsComingAndAcceptsEnter(t *testing.T) {
	f := newFake(t)
	f.Interactive = true
	f.In = strings.NewReader("\n\n\n\n\n")
	_, err := f.run(t, nil, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if !strings.Contains(out, "coming, not available now") {
		t.Fatalf("the unbuilt entry must be labelled:\n%s", out)
	}
	if !strings.Contains(out, "Dry run: nothing was changed") {
		t.Fatalf("dry run must say so:\n%s", out)
	}
}

func TestUnusedAnswersAreReported(t *testing.T) {
	f := newFake(t)
	res, _ := f.run(t, map[string]string{"target": "this-machine", "local.mode": "trial", "local.dir": f.Home + "/lw", "local.port": "18999", "local.signin": "accounts", "locl.typo": "x"}, Options{DryRun: true})
	if len(res.Unused) != 1 || res.Unused[0] != "locl.typo" {
		t.Fatalf("unused = %v", res.Unused)
	}
}

func trialAnswers(dir string) map[string]string {
	return map[string]string{"target": "this-machine", "local.mode": "trial", "local.dir": dir, "local.port": "18998", "local.signin": "accounts"}
}

func TestTrialDryRunChangesNothing(t *testing.T) {
	f := newFake(t)
	dir := filepath.Join(f.Home, "lw")
	if _, err := f.run(t, trialAnswers(dir), Options{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a dry run made the folder")
	}
	if len(f.started) != 0 {
		t.Fatal("a dry run started a server")
	}
}

func TestTrialIsIdempotentAndKeepsTheSetupCode(t *testing.T) {
	f := newFake(t)
	dir := filepath.Join(f.Home, "lw")
	if _, err := f.run(t, trialAnswers(dir), Options{Assume: true}); err != nil {
		t.Fatal(err)
	}
	code1, _ := os.ReadFile(filepath.Join(dir, "run", "setup-code"))
	cfg1, _ := os.ReadFile(filepath.Join(dir, "launchway.yaml"))
	if len(code1) == 0 || !strings.Contains(string(cfg1), "127.0.0.1:18998") {
		t.Fatalf("code %q config %q", code1, cfg1)
	}
	st, _ := os.Stat(filepath.Join(dir, "run", "setup-code"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("setup code mode %v", st.Mode())
	}
	f.started = nil
	if _, err := f.run(t, trialAnswers(dir), Options{Assume: true}); err != nil {
		t.Fatal(err)
	}
	code2, _ := os.ReadFile(filepath.Join(dir, "run", "setup-code"))
	if string(code1) != string(code2) {
		t.Fatal("the setup code was regenerated")
	}
	if len(f.started) != 0 {
		t.Fatal("running again started a second server")
	}
}

func TestTrialNoSigninIsLabelledAndLoopbackOnly(t *testing.T) {
	f := newFake(t)
	dir := filepath.Join(f.Home, "lw")
	a := trialAnswers(dir)
	a["local.signin"] = "none"
	if _, err := f.run(t, a, Options{Assume: true}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "launchway.yaml"))
	if !strings.Contains(string(cfg), "mode: none") || !strings.Contains(string(cfg), "listen: 127.0.0.1:") {
		t.Fatalf("config:\n%s", cfg)
	}
	if !strings.Contains(f.out.String(), "NO sign-in on this local trial") {
		t.Fatal("the no sign-in choice must be said plainly")
	}
}

func srvAnswers(f *fakeEnv, extra map[string]string) map[string]string {
	a := map[string]string{"target": "this-machine", "local.mode": "server", "server.scope": "system", "server.url": "https://launchway.example.com",
		"server.tls": "proxy", "server.database": "builtin", "server.conf_dir": f.Root + "/etc/launchway", "server.data_dir": f.Root + "/var/lib/launchway"}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

func TestServerNeedsSystemdAndSaysSo(t *testing.T) {
	f := newFake(t)
	f.Root = ""
	_, err := f.run(t, srvAnswers(f, nil), Options{Assume: true})
	if err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("want a clear systemd message, got %v", err)
	}
}

func TestServerWithoutRightsPrintsTheCommandsInsteadOfFailing(t *testing.T) {
	f := newFake(t)
	f.Root = "" // real paths: not testing under a temp root, but nothing is written because it is not root
	f.programs["systemctl"] = true
	a := srvAnswers(f, map[string]string{"server.conf_dir": "/etc/launchway", "server.data_dir": "/var/lib/launchway"})
	_, err := f.run(t, a, Options{Assume: true})
	if err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{"not running as root", "useradd --system", "systemctl daemon-reload", "install -m 0755"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := os.Stat("/etc/launchway/root.key"); err == nil {
		t.Fatal("it wrote a root key without rights")
	}
}

func TestServerInstallWritesUnitKeepsRootKeyAndUninstallKeepsIt(t *testing.T) {
	f := newFake(t)
	f.programs["systemctl"] = true
	f.UID = 0
	a := srvAnswers(f, map[string]string{"server.tls": "http"})
	if _, err := f.run(t, a, Options{Assume: true}); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	key := filepath.Join(f.Root, "etc/launchway/root.key")
	k1, err := os.ReadFile(key)
	if err != nil || len(strings.TrimSpace(string(k1))) != 44 {
		t.Fatalf("root key: %q %v", k1, err)
	}
	if st, _ := os.Stat(key); st.Mode().Perm() != 0o600 {
		t.Fatalf("root key mode %v", st.Mode())
	}
	unit, _ := os.ReadFile(filepath.Join(f.Root, "etc/systemd/system/launchway.service"))
	for _, want := range []string{"User=launchway", "NoNewPrivileges=true", "ExecStart=" + f.Root + "/usr/local/bin/launchway server"} {
		if !strings.Contains(string(unit), want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
	if !strings.Contains(f.out.String(), "WARNING: plain http") {
		t.Fatal("plain http must carry a warning")
	}
	cfg, _ := os.ReadFile(filepath.Join(f.Root, "etc/launchway/launchway.yaml"))
	if !strings.Contains(string(cfg), "mode: local") || strings.Contains(string(cfg), "trust_proxy") {
		t.Fatalf("config:\n%s", cfg)
	}
	// Run again: the root key is untouched.
	if _, err := f.run(t, a, Options{Assume: true}); err != nil {
		t.Fatal(err)
	}
	k2, _ := os.ReadFile(key)
	if string(k1) != string(k2) {
		t.Fatal("the root key was regenerated")
	}
	// Uninstall: says what it keeps, removes the unit, keeps the key and the data.
	_ = os.MkdirAll(filepath.Join(f.Root, "var/lib/launchway"), 0o750)
	_ = os.WriteFile(filepath.Join(f.Root, "var/lib/launchway/launchway.db"), []byte("data"), 0o600)
	f.out.Reset()
	ua := map[string]string{"target": "this-machine", "local.mode": "server", "server.scope": "system", "server.conf_dir": f.Root + "/etc/launchway", "server.data_dir": f.Root + "/var/lib/launchway"}
	if _, err := f.run(t, ua, Options{Assume: true, Uninstall: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "ROOT KEY") {
		t.Fatalf("uninstall must say it keeps the root key:\n%s", f.out.String())
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatal("uninstall removed the root key")
	}
	if _, err := os.Stat(filepath.Join(f.Root, "var/lib/launchway/launchway.db")); err != nil {
		t.Fatal("uninstall removed the data")
	}
	if _, err := os.Stat(filepath.Join(f.Root, "etc/systemd/system/launchway.service")); err == nil {
		t.Fatal("uninstall left the unit")
	}
}

func TestServerBundledDatabaseNeedsAContainerEngine(t *testing.T) {
	f := newFake(t)
	f.programs["systemctl"] = true
	f.UID = 0
	_, err := f.run(t, srvAnswers(f, map[string]string{"server.database": "bundled"}), Options{Assume: true})
	if err == nil || !strings.Contains(err.Error(), "no container engine") {
		t.Fatalf("want the missing engine named, got %v", err)
	}
	f.programs["podman"] = true
	if _, err := f.run(t, srvAnswers(f, map[string]string{"server.database": "bundled"}), Options{Assume: true}); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	pg, err := os.ReadFile(filepath.Join(f.Root, "etc/systemd/system/launchway-postgres.service"))
	if err != nil || !strings.Contains(string(pg), "postgres:17") {
		t.Fatalf("%v %s", err, pg)
	}
	env, _ := os.ReadFile(filepath.Join(f.Root, "etc/launchway/launchway.env"))
	if !strings.Contains(string(env), "LAUNCHWAY_DATABASE=postgres://launchway:") {
		t.Fatalf("env %s", env)
	}
}

func TestTranscriptHasNoSecrets(t *testing.T) {
	var b bytes.Buffer
	tr := NewTranscript(&b, nil)
	tr.Hide("hunter2-secret-value")
	tr.Line("password is hunter2-secret-value and the code is S9VBG-R7EB5-WN5FU-VCKZJ-QT5YJ and url postgres://u:pw123@host/db and token=abc123")
	tr.Answer(Question{Key: "server.database_url", Secret: true}, "postgres://u:pw@h/db", "typed")
	s := b.String()
	for _, bad := range []string{"hunter2", "S9VBG", "pw123", "abc123", "postgres://u:pw@h"} {
		if strings.Contains(s, bad) {
			t.Fatalf("%q leaked into the transcript:\n%s", bad, s)
		}
	}
}

func TestSecretAnswerKeysAreRefused(t *testing.T) {
	if !IsSecretKey("server.database_password") || IsSecretKey("server.database_url_file") || IsSecretKey("target") {
		t.Fatal("secret key detection")
	}
}
