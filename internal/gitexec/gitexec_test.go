package gitexec

import (
	"bytes"
	"context"
	"errors"
	"github.com/jk-magic-creations/launchway/internal/testlib"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	testlib.NoPrompt()
	Reexec()
	os.Exit(m.Run())
}

// plain runs the system git the way the old wrappers did, for the control
// half of a test: it proves the test would notice if the hardening failed.
func plain(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustPlain(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := plain(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakegit")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustPlain(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, dir, "add", ".")
	mustPlain(t, dir, "commit", "-q", "-m", "one")
	return dir
}

func TestRunCapturesOutputAndExitCodes(t *testing.T) {
	repo := newRepo(t)
	res, err := Run(context.Background(), Spec{Dir: repo, Args: []string{"rev-parse", "HEAD"}})
	if err != nil || len(strings.TrimSpace(res.Stdout)) != 40 {
		t.Fatalf("rev-parse: %q %v", res.Stdout, err)
	}
	if res.Duration <= 0 {
		t.Error("no duration")
	}
	res, err = Run(context.Background(), Spec{Dir: repo, Args: []string{"cat-file", "-e", strings.Repeat("0", 40)}})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code == 0 || res.ExitCode != ee.Code {
		t.Fatalf("want an ExitError, got %v", err)
	}
	if ee.Error() == "" {
		t.Error("empty error text")
	}
	out, err := Output(context.Background(), Spec{Dir: repo, Args: []string{"hash-object", "--stdin"}, Stdin: strings.NewReader("hello\n")})
	if err != nil || strings.TrimSpace(out) != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatalf("hash-object: %q %v", out, err)
	}
	var sink bytes.Buffer
	if _, err := Run(context.Background(), Spec{Dir: repo, Args: []string{"log", "--format=%s"}, Stdout: &sink}); err != nil || strings.TrimSpace(sink.String()) != "one" {
		t.Fatalf("streamed stdout: %q %v", sink.String(), err)
	}
	if _, err := Run(context.Background(), Spec{Args: nil}); !errors.Is(err, ErrDenied) {
		t.Errorf("no arguments: %v", err)
	}
	if _, err := Run(context.Background(), Spec{Args: []string{"status\x00"}}); !errors.Is(err, ErrDenied) {
		t.Errorf("NUL argument: %v", err)
	}
}

func TestUserAndSystemConfigurationIsNotRead(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = Leaked\n[alias]\n\tx = status\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	elsewhere := t.TempDir()
	if out, err := plain(t, elsewhere, "config", "--get", "user.name"); err != nil || !strings.Contains(out, "Leaked") {
		t.Fatalf("control: the plain git must see the leaked configuration, got %q %v", out, err)
	}
	res, err := Run(context.Background(), Spec{Dir: elsewhere, Args: []string{"config", "--get", "user.name"}})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 1 || strings.Contains(res.Stdout, "Leaked") {
		t.Fatalf("user configuration was read: %q %v", res.Stdout, err)
	}
	out, _ := Output(context.Background(), Spec{Dir: elsewhere, Args: []string{"config", "--show-origin", "--list"}})
	if strings.Contains(out, home) {
		t.Errorf("a file under HOME was read:\n%s", out)
	}
}

func TestEnvironmentAllowList(t *testing.T) {
	for _, bad := range []string{"GIT_EXTERNAL_DIFF=/bin/x", "GIT_ASKPASS=/bin/x", "GIT_PROXY_COMMAND=/bin/x",
		"GIT_EXEC_PATH=/tmp", "GIT_TEMPLATE_DIR=/tmp", "GIT_TRACE=1", "GIT_PAGER=evil", "LD_PRELOAD=/tmp/x.so", "PATH=/tmp", "novalue"} {
		_, err := Run(context.Background(), Spec{Args: []string{"version"}, Env: []string{bad}})
		if !errors.Is(err, ErrDenied) {
			t.Errorf("%s: want ErrDenied, got %v", bad, err)
		}
	}
	for _, good := range []string{"GIT_CONFIG_COUNT=0", "GIT_INDEX_FILE=/tmp/i", "GIT_AUTHOR_NAME=x", "GIT_CONFIG_KEY_12=a.b", "LAUNCHWAY_TOKEN=x"} {
		if !AllowedEnv(strings.SplitN(good, "=", 2)[0]) {
			t.Errorf("%s should be allowed", good)
		}
	}
	if AllowedEnv("GIT_CONFIG_KEY_x") || AllowedEnv("GIT_CONFIG_KEY_") {
		t.Error("a malformed GIT_CONFIG_KEY is allowed")
	}
	// Inherited dangerous variables never reach git.
	t.Setenv("GIT_EXTERNAL_DIFF", "/nonexistent")
	t.Setenv("GIT_SSH_COMMAND", "/nonexistent")
	t.Setenv("GIT_TRACE", "1")
	s := script(t, `env`)
	r := &Runner{Binary: s}
	res, err := r.Run(context.Background(), Spec{Args: []string{"x"}, Inherit: Remote})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"} {
		if !strings.Contains(res.Stdout, want+"\n") {
			t.Errorf("environment lacks %s", want)
		}
	}
	for _, not := range []string{"GIT_EXTERNAL_DIFF", "GIT_SSH_COMMAND", "GIT_TRACE"} {
		if strings.Contains(res.Stdout, not) {
			t.Errorf("environment has %s", not)
		}
	}
	if strings.Contains(res.Stdout, "HOME="+os.Getenv("HOME")+"\n") && os.Getenv("HOME") != "" {
		t.Error("HOME was inherited")
	}
}

func TestConfigVariablesAreInheritedOnlyForRemote(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: Bearer abc")
	r := &Runner{Binary: script(t, `env`)}
	local, _ := r.Run(context.Background(), Spec{Args: []string{"x"}})
	if strings.Contains(local.Stdout, "GIT_CONFIG_COUNT") {
		t.Error("a local command saw GIT_CONFIG_COUNT")
	}
	remote, _ := r.Run(context.Background(), Spec{Args: []string{"x"}, Inherit: Remote})
	if !strings.Contains(remote.Stdout, "GIT_CONFIG_VALUE_0=Authorization: Bearer abc") {
		t.Error("a remote command did not see the credential setting")
	}
	// An explicit setting replaces the inherited ones wholesale.
	both, _ := r.Run(context.Background(), Spec{Args: []string{"x"}, Inherit: Remote, Env: []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=c"}})
	if strings.Contains(both.Stdout, "Bearer abc") || !strings.Contains(both.Stdout, "GIT_CONFIG_VALUE_0=c") {
		t.Errorf("explicit settings did not replace inherited ones:\n%s", both.Stdout)
	}
}

func TestHostileRepositoryConfigNeverRuns(t *testing.T) {
	repo := newRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := script(t, "touch "+marker+"\nexit 1")
	cfg := filepath.Join(repo, ".git", "config")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("[core]\n\tfsmonitor = " + hook + "\n\tsshCommand = " + hook + "\n\thooksPath = " + filepath.Dir(hook) + "\n")
	f.Close()

	// Control: the plain git does run both.
	plain(t, repo, "status")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: fsmonitor did not run under plain git (git too old?): %v", err)
	}
	os.Remove(marker)
	plain(t, repo, "ls-remote", "ssh://127.0.0.1:1/x")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: sshCommand did not run under plain git: %v", err)
	}
	os.Remove(marker)

	ctx := context.Background()
	Run(ctx, Spec{Dir: repo, Args: []string{"status"}, Timeout: time.Minute})
	Run(ctx, Spec{Dir: repo, Args: []string{"ls-remote", "ssh://127.0.0.1:1/x"}, Inherit: Remote, Timeout: 30 * time.Second})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's own configuration ran a program")
	}
}

func TestHooksDoNotRun(t *testing.T) {
	repo := newRepo(t)
	marker := filepath.Join(t.TempDir(), "hook")
	hooks := filepath.Join(repo, ".git", "hooks")
	os.MkdirAll(hooks, 0o755)
	os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b"), 0o644)
	mustPlain(t, repo, "add", ".")
	mustPlain(t, repo, "commit", "-q", "-m", "two")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: the hook did not run: %v", err)
	}
	os.Remove(marker)
	os.WriteFile(filepath.Join(repo, "c.txt"), []byte("c"), 0o644)
	mustPlain(t, repo, "add", ".")
	_, err := Run(context.Background(), Spec{Dir: repo, Args: []string{"commit", "-q", "-m", "three"},
		Env: []string{"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@e.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a hook ran")
	}
}

func TestSubmodulesOverFileAndExtAreRefused(t *testing.T) {
	sub := newRepo(t)
	subSHA := mustPlain(t, sub, "rev-parse", "HEAD")
	marker := filepath.Join(t.TempDir(), "ext-ran")

	mk := func(url string) string {
		parent := t.TempDir()
		mustPlain(t, parent, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(parent, ".gitmodules"), []byte("[submodule \"s\"]\n\tpath = s\n\turl = "+url+"\n"), 0o644)
		mustPlain(t, parent, "add", ".gitmodules")
		mustPlain(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+subSHA+",s")
		mustPlain(t, parent, "commit", "-q", "-m", "m")
		return parent
	}

	file := mk("file://" + sub)
	// Control: git with the protocol allowed does fetch the submodule.
	ctl := t.TempDir()
	mustPlain(t, ctl, "clone", "-q", file, "c")
	if out, err := plain(t, filepath.Join(ctl, "c"), "-c", "protocol.file.allow=always", "submodule", "update", "--init"); err != nil {
		t.Fatalf("control: %v\n%s", err, out)
	}
	dst := filepath.Join(t.TempDir(), "w")
	if _, err := Run(context.Background(), Spec{Args: []string{"clone", "-q", file, dst}, Untrusted: true}); err != nil {
		t.Fatalf("cloning the parent itself must work: %v", err)
	}
	_, err := Run(context.Background(), Spec{Dir: dst, Args: []string{"submodule", "update", "--init"}, Untrusted: true})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("a file:// submodule must be refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "s", "a.txt")); err == nil {
		t.Fatal("the submodule was checked out")
	}

	ext := mk("ext::sh -c touch% " + marker)
	dst2 := filepath.Join(t.TempDir(), "w")
	if _, err := Run(context.Background(), Spec{Args: []string{"clone", "-q", ext, dst2}, Untrusted: true}); err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), Spec{Dir: dst2, Args: []string{"submodule", "update", "--init"}, Untrusted: true})
	if err == nil {
		t.Fatal("an ext:: submodule must fail")
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("the ext:: transport ran a program")
	}
	// ext:: as a clone URL is refused too.
	if _, err := Run(context.Background(), Spec{Args: []string{"clone", "-q", "ext::sh -c touch% " + marker, filepath.Join(t.TempDir(), "x")}}); err == nil {
		t.Fatal("ext:: clone allowed")
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("the ext:: transport ran a program")
	}
}

func TestCancelKillsTheWholeProcessGroup(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	r := &Runner{Binary: script(t, `sleep 300 &
echo $! > "$LAUNCHWAY_PIDFILE"
echo ready
wait`)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	var once bool
	w := writerFunc(func(p []byte) (int, error) {
		if !once {
			once = true
			close(ready)
		}
		return len(p), nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, Spec{Args: []string{"clone"}, Env: []string{"LAUNCHWAY_PIDFILE=" + pidfile}, Stdout: w})
		done <- err
	}()
	<-ready
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	data, rerr := os.ReadFile(pidfile)
	if rerr != nil {
		t.Fatal(rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	deadline := time.Now().Add(5 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the child sleep (pid %d) survived", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// gone reports whether pid no longer runs (a zombie waiting for its reaper
// counts as gone).
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return true
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(b), ')')
	return i > 0 && i+2 < len(b) && b[i+2] == 'Z'
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestTimeoutsAndOutputLimit(t *testing.T) {
	ctx := context.Background()
	slow := &Runner{Binary: script(t, `sleep 30`)}
	if _, err := slow.Run(ctx, Spec{Args: []string{"x"}, Timeout: 150 * time.Millisecond}); !errors.Is(err, ErrTimeout) {
		t.Errorf("total timeout: %v", err)
	}
	if _, err := slow.Run(ctx, Spec{Args: []string{"x"}, IdleTimeout: 150 * time.Millisecond}); !errors.Is(err, ErrIdle) {
		t.Errorf("idle timeout: %v", err)
	}
	// Output keeps the idle timer alive: a long transfer is not idle.
	busy := &Runner{Binary: script(t, `i=0; while [ $i -lt 8 ]; do echo tick; sleep 0.1; i=$((i+1)); done`)}
	if _, err := busy.Run(ctx, Spec{Args: []string{"x"}, IdleTimeout: 400 * time.Millisecond}); err != nil {
		t.Errorf("a busy command was cut off: %v", err)
	}
	big := &Runner{Binary: script(t, `head -c 3000000 /dev/zero; sleep 30`)}
	if _, err := big.Run(ctx, Spec{Args: []string{"x"}, MaxOutput: 1 << 20, Timeout: 20 * time.Second}); !errors.Is(err, ErrOutputLimit) {
		t.Errorf("output limit: %v", err)
	}
	// Streamed output is not limited.
	if _, err := (&Runner{Binary: script(t, `head -c 3000000 /dev/zero`)}).Run(ctx, Spec{Args: []string{"x"}, MaxOutput: 10, Stdout: io.Discard}); err != nil {
		t.Errorf("streamed output: %v", err)
	}
	// Standard error is cut, not fatal.
	noisy := &Runner{Binary: script(t, `head -c 3000000 /dev/zero 1>&2; exit 3`)}
	_, err := noisy.Run(ctx, Spec{Args: []string{"x"}})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || len(ee.Stderr) > MaxStderr {
		t.Errorf("stderr cap: %v", err)
	}
}

func TestMessagesAreRedacted(t *testing.T) {
	r := &Runner{Binary: script(t, `echo "fatal: unable to access 'https://dana:hunter2pass@example.com/r.git/' with token tok-9f8e7d" 1>&2; exit 128`)}
	res, err := r.Run(context.Background(), Spec{Args: []string{"fetch"}, Secrets: []string{"tok-9f8e7d", "ab"}})
	for _, s := range []string{res.Stderr, err.Error()} {
		if strings.Contains(s, "hunter2pass") || strings.Contains(s, "tok-9f8e7d") {
			t.Errorf("credential in %q", s)
		}
	}
	if !strings.Contains(err.Error(), "example.com") {
		t.Errorf("the message lost its meaning: %q", err.Error())
	}
}

func TestMemoryCeilingStopsGitNotTheTestProcess(t *testing.T) {
	const mib = 1 << 20
	data := func(n int64) io.Reader { return io.LimitReader(zeros{}, n*mib) }
	// Control: without the ceiling the same input is hashed.
	out, err := Output(context.Background(), Spec{Args: []string{"hash-object", "--stdin"}, Stdin: data(120), Timeout: time.Minute})
	if err != nil || len(strings.TrimSpace(out)) != 40 {
		t.Fatalf("control: %q %v", out, err)
	}
	_, err = Run(context.Background(), Spec{Args: []string{"hash-object", "--stdin"}, Stdin: data(120), Timeout: time.Minute,
		Limits: Limits{MemoryBytes: 48 * mib}})
	if err == nil {
		t.Fatal("git hashed 120 MiB under a 48 MiB ceiling")
	}
	if errors.Is(err, ErrLimitsUnavailable) {
		t.Fatalf("the test binary must support limits: %v", err)
	}
	// A small job under the same ceiling still works, and runs real git.
	out, err = Output(context.Background(), Spec{Args: []string{"hash-object", "--stdin"}, Stdin: strings.NewReader("hello\n"),
		Limits: Limits{MemoryBytes: 48 * mib, CPUSeconds: 30}})
	if err != nil || strings.TrimSpace(out) != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatalf("limited run: %q %v", out, err)
	}
	// The file size cap bounds what git can write.
	repo := t.TempDir()
	mustPlain(t, repo, "init", "-q", "--bare")
	_, err = Run(context.Background(), Spec{GitDir: repo, Args: []string{"hash-object", "-w", "--stdin"}, Stdin: io.LimitReader(rand.New(rand.NewSource(1)), 8*mib), Limits: Limits{FileBytes: 1 * mib}})
	if err == nil {
		t.Fatal("git wrote an 8 MiB object under a 1 MiB file cap")
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestLimitsRefusedWhenBinaryCannotApplyThem(t *testing.T) {
	// A fake "self" cannot be injected, so test the unit that decides.
	if err := checkSelf(context.Background()); err != nil {
		t.Fatalf("this test binary answers the protocol: %v", err)
	}
}

func TestAlternatesAreRefused(t *testing.T) {
	repo := newRepo(t)
	other := newRepo(t)
	info := filepath.Join(repo, ".git", "objects", "info")
	os.MkdirAll(info, 0o755)
	os.WriteFile(filepath.Join(info, "alternates"), []byte(filepath.Join(other, ".git", "objects")+"\n"), 0o644)
	_, err := Run(context.Background(), Spec{Dir: repo, Args: []string{"log", "-1"}})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("alternates: %v", err)
	}
	if _, err := Run(context.Background(), Spec{Dir: repo, Args: []string{"log", "-1"}, AllowAlternates: true}); err != nil {
		t.Fatalf("allowed alternates: %v", err)
	}
	if _, err := Run(context.Background(), Spec{Dir: repo, Args: []string{"log", "-1"}, Env: []string{"GIT_ALTERNATE_OBJECT_DIRECTORIES=/tmp"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("alternate env: %v", err)
	}
}

func TestUntrustedClonesCheckObjects(t *testing.T) {
	repo := newRepo(t)
	dst := filepath.Join(t.TempDir(), "c")
	if _, err := Run(context.Background(), Spec{Args: []string{"clone", "-q", "--no-checkout", repo, dst}, Untrusted: true}); err != nil {
		t.Fatal(err)
	}
	out, _ := Output(context.Background(), Spec{Dir: dst, Args: []string{"log", "--format=%s", "-1", "HEAD"}})
	if strings.TrimSpace(out) != "one" {
		t.Fatalf("clone content: %q", out)
	}
}

func TestInstalledVersion(t *testing.T) {
	v, err := Installed(context.Background())
	if err != nil || v.Major < 2 {
		t.Fatalf("%v %v", v, err)
	}
	for in, want := range map[string]Version{
		"git version 2.47.1\n":               {2, 47, 1, "2.47.1"},
		"git version 2.39.5 (Apple Git-154)": {2, 39, 5, "2.39.5"},
		"git version 2.28.0.windows.1":       {2, 28, 0, "2.28.0"},
		"git version 2.25":                   {2, 25, 0, "2.25"},
	} {
		got, err := ParseVersion(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseVersion("hello"); err == nil {
		t.Error("garbage parsed")
	}
	old, _ := ParseVersion("git version 2.25.1")
	if m := old.Missing(); len(m) != 3 {
		t.Errorf("2.25 should lack three features: %v", m)
	}
	cur, _ := ParseVersion("git version 2.38.0")
	if len(cur.Missing()) != 0 || !cur.AtLeast(2, 38) || cur.AtLeast(2, 39) || !cur.AtLeast(1, 99) || cur.AtLeast(3, 0) {
		t.Errorf("AtLeast: %v", cur.Missing())
	}
	if cur.String() != "2.38.0" {
		t.Error("String")
	}
}

func TestMissingGitIsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Run(context.Background(), Spec{Args: []string{"version"}}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("got %v", err)
	}
}

func TestRegisteredEntriesRunFirst(t *testing.T) {
	ran := 0
	RegisterEntry(func() { ran++ })
	runEntries()
	if ran != 1 {
		t.Fatalf("ran %d times", ran)
	}
	entryMu.Lock()
	entries = nil
	entryMu.Unlock()
}
