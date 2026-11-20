//go:build unix

package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// The limits are applied by the launchway binary itself, which the runner
// starts as
//
//	launchway <reexecArg> <memory> <cpu> <file> <git> <git's arguments...>
//
// The re-executed copy sets the limits on itself and replaces itself with
// git (execve), so git starts with the limits in force and no shell is
// involved. main calls Reexec first thing; so must the TestMain of any test
// package that runs git with Limits.
const (
	reexecArg = "--launchway-gitexec-limits"
	reexecEnv = "LAUNCHWAY_GITEXEC_LIMITS"
	selfTest  = "selftest"
	okLine    = "launchway-gitexec-limits-ok"
)

// ErrLimitsUnavailable means a Spec asked for Limits but the running binary
// does not call Reexec. The run is refused rather than done without limits.
var ErrLimitsUnavailable = errors.New("git resource limits are not available in this binary (it does not call gitexec.Reexec)")

func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid is the process group: git and everything it started
		// (ssh, remote helpers, hooks).
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
}

// Reexec must be the first thing main does. When the process was started by
// the runner to apply limits and run git, it never returns; otherwise it
// returns at once.
func Reexec() {
	runEntries()
	if len(os.Args) < 2 || os.Args[1] != reexecArg || os.Getenv(reexecEnv) != "1" {
		return
	}
	if len(os.Args) == 3 && os.Args[2] == selfTest {
		fmt.Println(okLine)
		os.Exit(0)
	}
	if len(os.Args) < 7 {
		fmt.Fprintln(os.Stderr, "launchway: bad git limit arguments")
		os.Exit(125)
	}
	mem, e1 := strconv.ParseUint(os.Args[2], 10, 64)
	cpu, e2 := strconv.ParseUint(os.Args[3], 10, 64)
	file, e3 := strconv.ParseUint(os.Args[4], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil {
		fmt.Fprintln(os.Stderr, "launchway: bad git limit arguments")
		os.Exit(125)
	}
	for _, l := range []struct {
		res int
		val uint64
	}{{syscall.RLIMIT_DATA, mem}, {syscall.RLIMIT_CPU, cpu}, {syscall.RLIMIT_FSIZE, file}} {
		if l.val == 0 {
			continue
		}
		lim := syscall.Rlimit{Cur: l.val, Max: l.val}
		if err := syscall.Setrlimit(l.res, &lim); err != nil {
			fmt.Fprintf(os.Stderr, "launchway: cannot apply the git resource limit: %v\n", err)
			os.Exit(125)
		}
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if len(kv) > len(reexecEnv) && kv[:len(reexecEnv)+1] == reexecEnv+"=" {
			continue
		}
		env = append(env, kv)
	}
	prog := os.Args[5]
	argv := append([]string{"git"}, os.Args[6:]...)
	err := syscall.Exec(prog, argv, env) //nolint:gosec // prog is the git binary the parent resolved; no shell
	fmt.Fprintf(os.Stderr, "launchway: cannot run git: %v\n", err)
	os.Exit(127)
}

var (
	selfOnce sync.Once
	selfErr  error
)

// limitedCommand builds the re-exec command line, after checking once that
// this binary answers the re-exec protocol.
func limitedCommand(ctx context.Context, l Limits, prog string, argv []string) (*exec.Cmd, error) {
	selfOnce.Do(func() { selfErr = checkSelf() })
	if selfErr != nil {
		return nil, selfErr
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{reexecArg,
		strconv.FormatUint(l.MemoryBytes, 10), strconv.FormatUint(l.CPUSeconds, 10), strconv.FormatUint(l.FileBytes, 10),
		prog}
	return exec.CommandContext(ctx, self, append(args, argv...)...), nil //nolint:gosec // self is this binary; no shell
}

func checkSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, reexecArg, selfTest) //nolint:gosec // self is this binary
	cmd.Env = append(os.Environ(), reexecEnv+"=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil || !bytes.Contains(out.Bytes(), []byte(okLine)) {
		return ErrLimitsUnavailable
	}
	return nil
}

// ownedByMe reports whether the file belongs to the user running this process.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
