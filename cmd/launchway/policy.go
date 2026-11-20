package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const policyUsage = `Usage:
  launchway policy list [--json]                the policies in force, their sources and recent verdicts
  launchway policy run <run-id> [--json]        a run's policy decisions, rule by rule
  launchway waivers list [--project P] [--active] [--json]
  launchway waivers grant <project> --policy P --rule R --reason "..." --expires 72h
                  [--run RUN] [--environment E] [--finding FINGERPRINT] [--json]
  launchway waivers revoke <project> <waiver-id> [--reason "..."]
`

// ---- policies ----

type policyRule struct {
	Rule     string `json:"rule"`
	Verdict  string `json:"verdict"`
	Found    string `json:"found"`
	Want     string `json:"want"`
	Notes    []string
	Waiver   *struct{ ID, GrantedBy string } `json:"waiver"`
	Evidence []struct {
		Kind, Step, Run, Text string
	} `json:"evidence"`
}

type policyResult struct {
	Policy struct {
		Name, Mode, Source, Version string
	} `json:"policy"`
	Verdict string       `json:"verdict"`
	Rules   []policyRule `json:"rules"`
}

type policyDecision struct {
	Seq         int64          `json:"seq"`
	Point       string         `json:"point"`
	Step        string         `json:"step"`
	Action      string         `json:"action"`
	Environment string         `json:"environment"`
	At          int64          `json:"at"`
	Verdict     string         `json:"verdict"`
	Policies    []policyResult `json:"policies"`
}

func cmdPolicy(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, policyUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.policyList(args[1:])
		case "run":
			return cl.policyRun(args[1:])
		}
		return usageErr("unknown command policy %s\n\n%s", args[0], policyUsage)
	})
}

func (cl *client) policyList(args []string) error {
	fs := flag.NewFlagSet("policy list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var o struct {
		Sources []struct {
			Name, Kind, Ref, Version, Error, FetchError string
			Library                                     string `json:"library"`
			FetchErrorAt                                int64  `json:"fetch_error_at"`
			Policies                                    []struct {
				Name, Description, Mode string
				Scope                   map[string][]string
				Rules                   map[string]any
			} `json:"policies"`
		} `json:"sources"`
		Recent []struct {
			Policy, Project, Point, Step, Verdict string
			RunNumber                             int `json:"run_number"`
			At                                    int64
		} `json:"recent"`
	}
	if err := cl.call("GET", "/api/v1/policies?recent=5", nil, &o); err != nil {
		return err
	}
	return cl.print(o, *asJSON, func() {
		if len(o.Sources) == 0 {
			fmt.Fprintln(cl.out, "No policies are configured.")
		}
		for _, s := range o.Sources {
			where := "the server's configuration"
			if s.Kind == "git" {
				where = fmt.Sprintf("library %s at %s", s.Library, s.Ref)
			}
			fmt.Fprintf(cl.out, "Source %s (%s), version %s\n", s.Name, where, shortVersion(s.Version))
			if s.Error != "" {
				fmt.Fprintf(cl.out, "  FAILING CLOSED: %s\n", s.Error)
			} else if s.FetchError != "" {
				fmt.Fprintf(cl.out, "  last check failed: %s\n", s.FetchError)
			}
			for _, p := range s.Policies {
				var rules []string
				for r := range p.Rules {
					rules = append(rules, r)
				}
				fmt.Fprintf(cl.out, "  %-24s %-8s rules: %s\n", p.Name, p.Mode, strings.Join(sortedStrings(rules), ", "))
				if len(p.Scope) > 0 {
					var parts []string
					for k, v := range p.Scope {
						parts = append(parts, k+"="+strings.Join(v, ","))
					}
					fmt.Fprintf(cl.out, "  %-24s scope: %s\n", "", strings.Join(sortedStrings(parts), " "))
				}
			}
		}
		if len(o.Recent) > 0 {
			fmt.Fprintln(cl.out, "\nRecent verdicts:")
			for _, v := range o.Recent {
				where := v.Point
				if v.Step != "" {
					where += " " + v.Step
				}
				fmt.Fprintf(cl.out, "  %-24s %-7s %s #%d (%s) %s\n", v.Policy, v.Verdict, v.Project, v.RunNumber, where,
					time.UnixMilli(v.At).Format("2006-01-02 15:04"))
			}
		}
	})
}

func shortVersion(v string) string {
	if len(v) == 40 {
		return v[:12]
	}
	return v
}

func sortedStrings(s []string) []string { sort.Strings(s); return s }

func (cl *client) policyRun(args []string) error {
	fs := flag.NewFlagSet("policy run", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway policy run <run-id>")
	}
	var out struct {
		Decisions []policyDecision `json:"decisions"`
	}
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/policy", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Decisions) == 0 {
			fmt.Fprintln(cl.out, "No policy judged this run.")
		}
		for _, d := range out.Decisions {
			where := "run start"
			if d.Step != "" {
				where = d.Point + " " + d.Step
				if d.Environment != "" {
					where += " to " + d.Environment
				}
			}
			fmt.Fprintf(cl.out, "%s  %s: %s\n", time.UnixMilli(d.At).Format("2006-01-02 15:04:05"), where, strings.ToUpper(d.Verdict))
			for _, p := range d.Policies {
				fmt.Fprintf(cl.out, "  %s (%s, %s %s): %s\n", p.Policy.Name, p.Policy.Mode, p.Policy.Source, shortVersion(p.Policy.Version), p.Verdict)
				for _, r := range p.Rules {
					fmt.Fprintf(cl.out, "    %-18s %-6s %s\n", r.Rule, r.Verdict, r.Found)
					if r.Verdict != "pass" {
						fmt.Fprintf(cl.out, "    %-18s        needs %s\n", "", r.Want)
					}
					if r.Waiver != nil {
						fmt.Fprintf(cl.out, "    %-18s        waived by %s (%s)\n", "", r.Waiver.GrantedBy, r.Waiver.ID)
					}
					for _, n := range r.Notes {
						fmt.Fprintf(cl.out, "    %-18s        note: %s\n", "", n)
					}
				}
			}
		}
	})
}

// ---- waivers ----

type waiverView struct {
	ID          string `json:"id"`
	Project     string `json:"project"`
	Policy      string `json:"policy"`
	Rule        string `json:"rule"`
	Run         string `json:"run"`
	RunNumber   int    `json:"run_number"`
	Environment string `json:"environment"`
	Finding     string `json:"finding"`
	Reason      string `json:"reason"`
	GrantedBy   string `json:"granted_by"`
	ExpiresAt   int64  `json:"expires_at"`
	State       string `json:"state"`
	RevokedBy   string `json:"revoked_by"`
}

func cmdWaivers(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, policyUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.waiverList(args[1:])
		case "grant":
			return cl.waiverGrant(args[1:])
		case "revoke":
			return cl.waiverRevoke(args[1:])
		}
		return usageErr("unknown command waivers %s\n\n%s", args[0], policyUsage)
	})
}

func (w waiverView) line() string {
	scope := w.Project
	if w.Run != "" {
		scope += fmt.Sprintf(" run #%d", w.RunNumber)
	}
	if w.Environment != "" {
		scope += " in " + w.Environment
	}
	if w.Finding != "" {
		scope += " finding " + w.Finding
	}
	return fmt.Sprintf("%s  %-8s %s/%s for %s, by %s until %s: %s", w.ID, w.State, w.Policy, w.Rule, scope, w.GrantedBy,
		time.UnixMilli(w.ExpiresAt).Format("2006-01-02 15:04"), w.Reason)
}

func (cl *client) waiverList(args []string) error {
	fs := flag.NewFlagSet("waivers list", flag.ContinueOnError)
	project := fs.String("project", "", "only this project's waivers")
	active := fs.Bool("active", false, "only waivers in force")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	q := url.Values{}
	if *project != "" {
		q.Set("project", *project)
	}
	if *active {
		q.Set("active", "1")
	}
	var out struct {
		Waivers []waiverView `json:"waivers"`
	}
	if err := cl.call("GET", "/api/v1/waivers?"+q.Encode(), nil, &out); err != nil {
		return err
	}
	return cl.print(out.Waivers, *asJSON, func() {
		if len(out.Waivers) == 0 {
			fmt.Fprintln(cl.out, "No waivers.")
		}
		for _, w := range out.Waivers {
			fmt.Fprintln(cl.out, w.line())
		}
	})
}

func (cl *client) waiverGrant(args []string) error {
	fs := flag.NewFlagSet("waivers grant", flag.ContinueOnError)
	pol := fs.String("policy", "", "the policy")
	rule := fs.String("rule", "", "the rule of the policy")
	reason := fs.String("reason", "", "why (required; recorded and shown wherever the verdict is)")
	expires := fs.Duration("expires", 0, "how long it lasts, such as 72h (required)")
	run := fs.String("run", "", "only for this run")
	env := fs.String("environment", "", "only for this environment (deploy rules)")
	finding := fs.String("finding", "", "only for this finding's fingerprint (the findings rule)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *pol == "" || *rule == "" || *reason == "" || *expires <= 0 {
		return usageErr("launchway waivers grant <project> --policy P --rule R --reason \"...\" --expires 72h [--run RUN] [--environment E] [--finding F]")
	}
	hours := int((*expires + time.Hour - 1) / time.Hour)
	body := map[string]any{"policy": *pol, "rule": *rule, "reason": *reason, "expires_in_hours": hours, "run": *run, "environment": *env, "finding": *finding}
	var w waiverView
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(pos[0])+"/waivers", body, &w); err != nil {
		return err
	}
	return cl.print(w, *asJSON, func() { fmt.Fprintln(cl.out, "Granted "+w.line()) })
}

func (cl *client) waiverRevoke(args []string) error {
	fs := flag.NewFlagSet("waivers revoke", flag.ContinueOnError)
	reason := fs.String("reason", "", "why")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway waivers revoke <project> <waiver-id> [--reason \"...\"]")
	}
	var w waiverView
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(pos[0])+"/waivers/"+url.PathEscape(pos[1])+"/revoke", map[string]string{"reason": *reason}, &w); err != nil {
		return err
	}
	fmt.Fprintln(cl.out, "Revoked "+w.line())
	return nil
}
