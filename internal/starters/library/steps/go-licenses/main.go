package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// say prints a line the log shows as what the step decided.
func say(format string, a ...any) { fmt.Printf("launchway: "+format+"\n", a...) }

// run prints a command and runs it through the shell, as the starters' scripts do, without the
// variables that let a program write results.
func run(dir, command string) error {
	fmt.Println("+ " + command)
	cmd := exec.CommandContext(context.Background(), "sh", "-c", command) //nolint:gosec // G204: the commands are written in this file, as the starters' scripts run them
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Env = dir, os.Stdout, os.Stderr, Environ()
	return cmd.Run()
}

func main() {
	Run(func(s *Step) error {
		ws := os.Getenv("LAUNCHWAY_WORKSPACE")
		wd := s.String("working_directory")
		if wd == "" {
			wd = "."
		}
		if filepath.IsAbs(wd) || !filepath.IsLocal(wd) {
			return fmt.Errorf("working_directory must be a directory inside the repository, not %s", wd)
		}
		dir := filepath.Join(ws, wd)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() { //nolint:gosec // G703: working_directory was checked to be inside the workspace
			return fmt.Errorf("working_directory %s does not exist in this repository", wd)
		}
		deny := strings.Join(s.List("license_deny"), ",")
		env := os.Environ()
		os.Setenv("GOMODCACHE", filepath.Join(ws, ".cache", "go-mod"))
		os.Setenv("GOCACHE", filepath.Join(ws, ".cache", "go-build"))
		os.Setenv("GOFLAGS", os.Getenv("GOFLAGS")+" -modcacherw")
		_ = env
		gobin := os.Getenv("GOBIN")
		if gobin == "" {
			out, _ := exec.CommandContext(context.Background(), "go", "env", "GOPATH").Output()
			gobin = filepath.Join(strings.TrimSpace(string(out)), "bin")
		}
		os.Setenv("GOBIN", gobin)
		os.Setenv("PATH", gobin+string(os.PathListSeparator)+os.Getenv("PATH"))
		if _, err := exec.LookPath("go-licenses"); err != nil {
			if err := run(dir, "go install github.com/google/go-licenses@latest"); err != nil && deny == "" {
				say("go-licenses could not be installed, so the licenses are not listed")
				s.Output("checked", false)
				return nil
			}
		}
		if err := run(dir, "go-licenses report ./..."); err != nil {
			say("could not list the licenses")
		}
		if deny == "" {
			say("license_deny is empty, so licenses are listed and nothing fails (set license_deny: [forbidden, restricted] to block)")
			s.Output("checked", false)
			return nil
		}
		s.Output("checked", true)
		if err := run(dir, "go-licenses check ./... --disallowed_types="+deny); err != nil {
			s.Summary("A dependency has a license of a type on **license_deny**: " + deny)
			return fmt.Errorf("a dependency has a license that is on license_deny (%s)", deny)
		}
		return nil
	})
}
