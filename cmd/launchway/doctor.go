package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
)

const doctorUsage = `Usage:
  launchway doctor [database|git] [--config launchway.yaml] [--json]
  launchway doctor ssh [--address HOST:PORT] [--config launchway.yaml]

Checks an installation and says what it finds in plain words. With no section
it runs both.
  git        the installed git, and whether it is new enough for Git hosting
  database   whether this installation would survive the loss of its database,
             and what would be lost
  ssh        whether Git over SSH is reachable, and which host keys it presents
`

// exitOn ends the program for an error from a command.
func exitOn(err error) {
	var ex exitErr
	if errors.As(err, &ex) {
		os.Exit(int(ex))
	}
	fmt.Fprintln(os.Stderr, "launchway:", err)
	os.Exit(1)
}

// unknownCommand says so and returns the exit status.
func unknownCommand(name string) int {
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", name, usage)
	return 2
}

// gitReport describes the installed git for the doctor.
type gitReport struct {
	Version string `json:"version,omitempty"`
	Minimum string `json:"hosting_minimum"`
	Hosting string `json:"hosting"` // "ok", "too old" or "no git"
	Message string `json:"message,omitempty"`
}

func checkGit(ctx context.Context) gitReport {
	v, err := hosting.CheckGit(ctx)
	r := gitReport{Version: v.String(), Minimum: hosting.MinGitText(), Hosting: "ok"}
	if err != nil {
		r.Message = err.Error()
		r.Hosting = "too old"
		if v.Raw == "" {
			r.Hosting = "no git"
		}
	}
	return r
}

func (r gitReport) lines() []string {
	switch r.Hosting {
	case "ok":
		return []string{fmt.Sprintf("Git: %s; Git hosting can be turned on (it needs %s or newer)", r.Version, r.Minimum)}
	default:
		return []string{"Git: " + r.Message}
	}
}

// cmdDoctor runs the doctor's sections, git and database. It reads the same configuration as the server (and LAUNCHWAY_DATABASE) and
// opens the database directly, not through a server.
func cmdDoctor(args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Print(doctorUsage)
		return nil
	}
	if len(args) > 0 && args[0] == "ssh" {
		return doctorSSH(args[1:])
	}
	section := ""
	if len(args) > 0 && (args[0] == "database" || args[0] == "git") {
		section, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgPath := fs.String("config", "launchway.yaml", "configuration file")
	asJSON := fs.Bool("json", false, "print JSON")
	_ = fs.Parse(args)
	if section == "git" {
		r := checkGit(context.Background())
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"git": r})
		}
		for _, l := range r.lines() {
			fmt.Println(l)
		}
		return nil
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	wait, _, err := cfg.Timings()
	if err != nil {
		return err
	}
	budget, err := cfg.StallBudget()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait+30*time.Second)
	defer cancel()
	st, err := engine.InspectDatabase(ctx, cfg.DatabaseAddr(), wait)
	if err != nil {
		return fmt.Errorf("the database cannot be reached, so nothing can be said about its availability: %w", err)
	}
	as := st.Assess()
	var gr *gitReport
	if section == "" {
		r := checkGit(ctx)
		gr = &r
	}
	if *asJSON {
		out := map[string]any{"database": st, "assessment": as, "stall_budget": budget.String()}
		if gr != nil {
			out["git"] = gr
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if gr != nil {
		for _, l := range gr.lines() {
			fmt.Println(l)
		}
	}
	fmt.Printf("Database availability: %s\n", as.Level)
	for _, l := range as.Lines {
		fmt.Println("  " + l)
	}
	fmt.Printf("  Riding through an outage: a request waits up to %s for the database (resilience.stall_budget), then is answered 503 with Retry-After; runners keep running steps and keep their logs and results on disk.\n", budget)
	return nil
}
