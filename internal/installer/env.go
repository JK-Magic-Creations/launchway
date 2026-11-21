package installer

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Env is everything the installer touches outside its own memory. Tests replace the parts they
// need; Default fills the rest from the real machine.
type Env struct {
	In  io.Reader
	Out io.Writer
	// Interactive says a person can answer questions; without it every question is answered from
	// the answers or its default, and one that has neither stops the install.
	Interactive bool

	GOOS string
	UID  int
	Home string
	// Root is put in front of every absolute path the installer writes for a system-wide install
	// (/etc, /var/lib, /usr/local). Empty means the real root. Tests set a temporary directory.
	Root string

	Getenv   func(string) string
	LookPath func(string) (string, error)
	// Run runs a command and returns its combined output.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Start starts a command that keeps running after the installer exits (the laptop server).
	Start func(name string, args []string, env []string, dir, logFile string) (pid int, err error)
	// Reachable says whether an address (host:port) answers on this machine.
	Reachable   func(addr string) bool
	OpenBrowser func(url string) error
	Now         func() time.Time

	// Version is this build's version; Image is the container image reference of this build
	// (set at release time; empty on a development build, which then needs --image).
	Version string
	Image   string
	// Binary is the path of the running launchway executable.
	Binary string
}

// Default is the real machine.
func Default(in io.Reader, out io.Writer, version, image string) Env {
	home, _ := os.UserHomeDir()
	bin, _ := os.Executable()
	e := Env{In: in, Out: out, GOOS: goos(), UID: os.Getuid(), Home: home, Getenv: os.Getenv, LookPath: exec.LookPath,
		Now: time.Now, Version: version, Image: image, Binary: bin}
	e.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: the installer runs the programs it names itself (systemctl, useradd, chown, id) with paths it built
	}
	e.Start = startDetached
	e.Reachable = func(addr string) bool {
		c, err := (&net.Dialer{Timeout: 800 * time.Millisecond}).DialContext(context.Background(), "tcp", addr)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}
	e.OpenBrowser = openBrowser
	return e
}

func (e *Env) path(p string) string {
	if e.Root == "" {
		return p
	}
	return e.Root + p
}

func (e *Env) Printf(format string, a ...any) { fmt.Fprintf(e.Out, format, a...) }

// has reports whether a program is on the PATH.
func (e *Env) has(name string) bool {
	if e.LookPath == nil {
		return false
	}
	_, err := e.LookPath(name)
	return err == nil
}

func (e *Env) run(ctx context.Context, name string, args ...string) (string, error) {
	if e.Run == nil {
		return "", fmt.Errorf("cannot run %s here", name)
	}
	b, err := e.Run(ctx, name, args...)
	return strings.TrimSpace(string(b)), err
}
