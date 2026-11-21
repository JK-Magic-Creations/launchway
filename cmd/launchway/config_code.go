package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

const stateUsage = `Usage:
  launchway state list PROJECT [--json]                      the state workspaces Launchway holds for a project
  launchway state show PROJECT WORKSPACE [--version N]       resources and output names (never values)
  launchway state versions PROJECT WORKSPACE                 who wrote each version, when, from which run
  launchway state lock PROJECT WORKSPACE [--reason TEXT]     lock a workspace by hand (a maintenance window)
  launchway state unlock PROJECT WORKSPACE --reason TEXT     force-release a lock; refused while its run is going
  launchway state pull PROJECT WORKSPACE --reason TEXT [--version N]   the raw state document, to standard output
  launchway state push PROJECT WORKSPACE FILE|- [--adopt]    store a state document (an import or a repair)
  launchway state rollback PROJECT WORKSPACE --version N --reason TEXT   store an earlier version as the newest

State is the Terraform and OpenTofu state Launchway keeps for you (the http backend; nothing to
set up). It holds secrets in clear text by design of its format, so it is sealed in the database,
show and versions never print a value, and pull needs a reason that goes to the audit trail with
your name. Rollback changes the stored state only: no real resource changes, and the next plan
shows the difference. Listing needs the viewer role on the project; everything else, the operator role.
`

const configurationUsage = `Usage:
  launchway configuration overview PROJECT [--json]           per Airfield: last apply, drift, last plan, state
  launchway configuration report RUN [STEP] [--json]          the configuration reports of a run (Ansible, plans)
  launchway configuration rerun-failed RUN STEP               run again on the hosts that failed or were unreachable

A report is what an Ansible or plan step found: hosts by outcome, the first failed task, drift in
check mode, or the resources a plan would add, change, destroy and replace (sensitive values are
never shown). rerun-failed needs the pipeline to take its limit from an input named limit.
`

func cmdState(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, stateUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		return cl.state(args[0], args[1:])
	})
}

func cmdConfiguration(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, configurationUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		return cl.configuration(args[0], args[1:])
	})
}

func stateBase(project, ws string) string {
	return "/api/v1/projects/" + url.PathEscape(project) + "/state/" + url.PathEscape(ws)
}

func (cl *client) state(sub string, args []string) error {
	fs := flag.NewFlagSet("state "+sub, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	version := fs.Int("version", 0, "a version number")
	reason := fs.String("reason", "", "why")
	adopt := fs.Bool("adopt", false, "replace the stored lineage")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	need := func(n int) error {
		if len(pos) != n {
			return usageErr("%s", stateUsage)
		}
		return nil
	}
	switch sub {
	case "list":
		if err := need(1); err != nil {
			return err
		}
		var out struct {
			Workspaces []struct {
				Name   string `json:"name"`
				Serial int64  `json:"serial"`
				Ver    int    `json:"version"`
				Lock   *struct {
					Actor string `json:"actor"`
				} `json:"lock"`
			} `json:"workspaces"`
			Sealed bool `json:"sealed"`
		}
		if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/state", nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			if !out.Sealed {
				fmt.Fprintln(cl.out, "This server has no root key, so it cannot keep state: create one (launchway encryption init).")
			}
			if len(out.Workspaces) == 0 {
				fmt.Fprintln(cl.out, "No state yet. The first apply of a plan step creates it.")
			}
			for _, w := range out.Workspaces {
				lock := ""
				if w.Lock != nil {
					lock = "  LOCKED by " + w.Lock.Actor
				}
				fmt.Fprintf(cl.out, "%-24s version %d, serial %d%s\n", w.Name, w.Ver, w.Serial, lock)
			}
		})
	case "show":
		if err := need(2); err != nil {
			return err
		}
		path := stateBase(pos[0], pos[1])
		if *version > 0 {
			path += fmt.Sprintf("?version=%d", *version)
		}
		var out struct {
			State struct {
				Version struct {
					Version int `json:"version"`
					Serial  int `json:"serial"`
				} `json:"version"`
				Resources []struct {
					Address   string `json:"address"`
					Instances int    `json:"instances"`
				} `json:"resources"`
				Outputs []struct {
					Name      string `json:"name"`
					Sensitive bool   `json:"sensitive"`
				} `json:"outputs"`
			} `json:"state"`
		}
		if err := cl.call("GET", path, nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			fmt.Fprintf(cl.out, "Version %d, serial %d, %d resources (values are never shown)\n", out.State.Version.Version, out.State.Version.Serial, len(out.State.Resources))
			for _, r := range out.State.Resources {
				fmt.Fprintf(cl.out, "  %s (%d)\n", r.Address, r.Instances)
			}
			for _, o := range out.State.Outputs {
				s := ""
				if o.Sensitive {
					s = "  (sensitive)"
				}
				fmt.Fprintf(cl.out, "  output %s%s\n", o.Name, s)
			}
		})
	case "versions":
		if err := need(2); err != nil {
			return err
		}
		var out struct {
			Versions []struct {
				Version   int    `json:"version"`
				Serial    int64  `json:"serial"`
				Source    string `json:"source"`
				Actor     string `json:"actor"`
				RunID     string `json:"run_id"`
				Resources int    `json:"resources"`
				CreatedAt int64  `json:"created_at"`
				Restored  int    `json:"restored_from"`
			} `json:"versions"`
		}
		if err := cl.call("GET", stateBase(pos[0], pos[1])+"/versions", nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			for _, v := range out.Versions {
				extra := v.Source
				if v.Restored > 0 {
					extra = fmt.Sprintf("rollback to version %d", v.Restored)
				}
				fmt.Fprintf(cl.out, "%3d  serial %-4d %s  %-18s %3d resources  %s\n", v.Version, v.Serial, time.UnixMilli(v.CreatedAt).UTC().Format("2006-01-02 15:04"), v.Actor, v.Resources, extra)
			}
		})
	case "lock":
		if err := need(2); err != nil {
			return err
		}
		var out map[string]any
		if err := cl.call("POST", stateBase(pos[0], pos[1])+"/lock", map[string]string{"reason": *reason}, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() { fmt.Fprintln(cl.out, "Locked. No run can change this state until it is unlocked.") })
	case "unlock":
		if err := need(2); err != nil {
			return err
		}
		if *reason == "" {
			return usageErr("say why: --reason TEXT")
		}
		var out map[string]any
		if err := cl.call("DELETE", stateBase(pos[0], pos[1])+"/lock?reason="+url.QueryEscape(*reason), nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() { fmt.Fprintln(cl.out, "Unlocked. The holder is recorded in the audit trail.") })
	case "pull":
		if err := need(2); err != nil {
			return err
		}
		if *reason == "" {
			return usageErr("state holds secrets in clear text; say why you need it: --reason TEXT")
		}
		path := stateBase(pos[0], pos[1]) + "/pull?reason=" + url.QueryEscape(*reason)
		if *version > 0 {
			path += fmt.Sprintf("&version=%d", *version)
		}
		var raw json.RawMessage
		if err := cl.call("GET", path, nil, &raw); err != nil {
			return err
		}
		_, err := cl.out.Write(append(raw, '\n'))
		return err
	case "push":
		if err := need(3); err != nil {
			return err
		}
		var data []byte
		var rerr error
		if pos[2] == "-" {
			data, rerr = io.ReadAll(os.Stdin)
		} else {
			data, rerr = os.ReadFile(pos[2])
		}
		if rerr != nil {
			return usageErr("reading the state: %v", rerr)
		}
		path := stateBase(pos[0], pos[1]) + "/push"
		if *adopt {
			path += "?adopt=true"
		}
		var out map[string]any
		if err := cl.call("PUT", path, rawText(strings.TrimSpace(string(data))), &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "Stored as version %v (serial %v).\n", out["version"], out["serial"]) })
	case "rollback":
		if err := need(2); err != nil {
			return err
		}
		if *version < 1 || *reason == "" {
			return usageErr("launchway state rollback PROJECT WORKSPACE --version N --reason TEXT")
		}
		var out map[string]any
		if err := cl.call("POST", stateBase(pos[0], pos[1])+"/rollback", map[string]any{"version": *version, "reason": *reason}, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() { fmt.Fprintln(cl.out, out["note"]) })
	}
	return usageErr("unknown command state %s\n\n%s", sub, stateUsage)
}

func (cl *client) configuration(sub string, args []string) error {
	fs := flag.NewFlagSet("configuration "+sub, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	switch sub {
	case "overview":
		if len(pos) != 1 {
			return usageErr("%s", configurationUsage)
		}
		var out struct {
			Environments []struct {
				Name    string `json:"name"`
				Ansible *struct {
					Recap     string `json:"recap"`
					RunNumber int    `json:"run_number"`
				} `json:"ansible"`
				Drift *struct {
					Recap  string `json:"recap"`
					Status string `json:"status"`
				} `json:"drift"`
				Plan *struct {
					Recap string `json:"recap"`
				} `json:"plan"`
			} `json:"environments"`
		}
		if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/configuration", nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			if len(out.Environments) == 0 {
				fmt.Fprintln(cl.out, "No configuration runs yet.")
			}
			for _, e := range out.Environments {
				name := e.Name
				if name == "" {
					name = "(no Airfield named)"
				}
				fmt.Fprintln(cl.out, name)
				if e.Ansible != nil {
					fmt.Fprintf(cl.out, "  last apply:  %s\n", e.Ansible.Recap)
				}
				if e.Drift != nil {
					fmt.Fprintf(cl.out, "  drift check: %s (%s)\n", e.Drift.Recap, e.Drift.Status)
				}
				if e.Plan != nil {
					fmt.Fprintf(cl.out, "  last plan:   %s\n", e.Plan.Recap)
				}
			}
		})
	case "report":
		if len(pos) < 1 || len(pos) > 2 {
			return usageErr("%s", configurationUsage)
		}
		if len(pos) == 2 {
			var out map[string]any
			if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/config-reports/"+url.PathEscape(pos[1]), nil, &out); err != nil {
				return err
			}
			return cl.print(out, *asJSON, func() {
				rep, _ := out["report"].(map[string]any)
				fmt.Fprintf(cl.out, "%v: %v (%v)\n", rep["step"], rep["recap"], rep["status"])
				if sm, ok := out["summary"].(map[string]any); ok {
					if f, ok := sm["failure"].(map[string]any); ok && f != nil {
						fmt.Fprintf(cl.out, "First failure: %v on %v: %v\n", f["task"], f["host"], f["msg"])
					}
					if h, ok := sm["failed_hosts"].([]any); ok && len(h) > 0 {
						fmt.Fprintf(cl.out, "To run again: %v\n", h)
					}
				}
			})
		}
		var out struct {
			Reports []struct {
				Step, Kind, Status, Recap string
			} `json:"reports"`
		}
		if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/config-reports", nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			if len(out.Reports) == 0 {
				fmt.Fprintln(cl.out, "This run has no configuration reports.")
			}
			for _, r := range out.Reports {
				fmt.Fprintf(cl.out, "%-20s %-8s %-10s %s\n", r.Step, r.Kind, r.Status, r.Recap)
			}
		})
	case "rerun-failed":
		if len(pos) != 2 {
			return usageErr("%s", configurationUsage)
		}
		var out map[string]any
		if err := cl.call("POST", "/api/v1/runs/"+url.PathEscape(pos[0])+"/config-reports/"+url.PathEscape(pos[1])+"/rerun-failed", struct{}{}, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "Started a run on: %v\n", out["hosts"]) })
	}
	return usageErr("unknown command configuration %s\n\n%s", sub, configurationUsage)
}
