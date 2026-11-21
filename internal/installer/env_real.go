package installer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func goos() string { return runtime.GOOS }

// startDetached starts a program that outlives the installer, in its own session, with its output
// in a file (mode 0600: a start-up message can carry a one-time code).
func startDetached(name string, args []string, env []string, dir, logFile string) (int, error) {
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer lf.Close()
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // G204: the installer starts this program with the arguments it wrote itself
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func openBrowser(url string) error {
	for _, c := range []string{"xdg-open", "open"} {
		if p, err := exec.LookPath(c); err == nil {
			return exec.CommandContext(context.Background(), p, url).Start() //nolint:gosec // G204: opens the address the installer printed, with the system's own opener
		}
	}
	return fmt.Errorf("no way to open a browser from here")
}
