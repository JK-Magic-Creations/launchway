package main

import (
	"flag"
	"fmt"
	"os"
)

const runLimitsUsage = `Usage:
  launchway run-limits get [--json]                   the installation's limits on pipelines
  launchway run-limits set --max-run-timeout D        change them (administrators); "" removes the limit

max-run-timeout is the longest timeout a pipeline may ask for with its top-level timeout, such as
48h (1m to 720h). A pipeline that asks for more is refused when a run starts and by 'launchway
validate'. Changes need the admin role on every Flight and are recorded in the audit trail.
`

type runLimitsView struct {
	MaxRunTimeout string `json:"max_run_timeout"`
	UpdatedAt     int64  `json:"updated_at"`
	UpdatedBy     string `json:"updated_by"`
}

func cmdRunLimits(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, runLimitsUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "get":
			return cl.runLimitsGet(args[1:])
		case "set":
			return cl.runLimitsSet(args[1:])
		}
		return usageErr("unknown command run-limits %s\n\n%s", args[0], runLimitsUsage)
	})
}

func (cl *client) runLimitsGet(args []string) error {
	fs := flag.NewFlagSet("run-limits get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var v runLimitsView
	if err := cl.call("GET", "/api/v1/run-limits", nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		fmt.Fprintf(cl.out, "max run timeout: %s\n", orUnset(v.MaxRunTimeout))
	})
}

func (cl *client) runLimitsSet(args []string) error {
	fs := flag.NewFlagSet("run-limits set", flag.ContinueOnError)
	maxTimeout := fs.String("max-run-timeout", "", "the longest timeout a pipeline may ask for; \"\" removes the limit")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || !visited(fs)["max-run-timeout"] {
		return usageErr("launchway run-limits set --max-run-timeout D")
	}
	var out runLimitsView
	if err := cl.call("PUT", "/api/v1/run-limits", map[string]string{"max_run_timeout": *maxTimeout}, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "The longest run timeout is now: %s.\n", orUnset(out.MaxRunTimeout))
	})
}
