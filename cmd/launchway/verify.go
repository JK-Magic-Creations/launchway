package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

const verifyUsage = `Usage:
  launchway verifications list [--environment E] [--project P] [--service S] [--limit N] [--json]
  launchway verifications show <run-id> [--json]      a run's verifications, interval by interval
  launchway verifications mark <run-id> <step> passed|failed --reason TEXT
  launchway health-sources [--json]                   the sources verify steps may name
  launchway models [--json]                           the models ai steps may name, with today's use

Health sources and models are listed by name and limits only: never where
they are or how they are reached.
`

// cmdVerifications lists, shows and marks verifications.
func cmdVerifications(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, verifyUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.verifications(args[1:])
		case "show":
			return cl.showVerifications(args[1:])
		case "mark":
			return cl.markVerification(args[1:])
		}
		return usageErr("unknown command verifications %s\n\n%s", args[0], verifyUsage)
	})
}

// cmdHealthSources and cmdModels list what the server has configured.
func cmdHealthSources(args []string) error {
	return runClient(func() error {
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).healthSources(args)
	})
}

func cmdModels(args []string) error {
	return runClient(func() error {
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).models(args)
	})
}

func (cl *client) verifications(args []string) error {
	fs := flag.NewFlagSet("verifications list", flag.ContinueOnError)
	env := fs.String("environment", "", "only this Airfield's")
	project := fs.String("project", "", "only this project's")
	service := fs.String("service", "", "only this service's")
	limit := fs.Int("limit", 20, "at most this many")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	q := url.Values{}
	for k, v := range map[string]string{"environment": *env, "project": *project, "service": *service} {
		if v != "" {
			q.Set(k, v)
		}
	}
	q.Set("limit", strconv.Itoa(*limit))
	var list []engine.VerificationSummary
	if err := cl.call("GET", "/api/v1/verifications?"+q.Encode(), nil, &list); err != nil {
		return err
	}
	return cl.print(list, *asJSON, func() {
		for _, v := range list {
			state := v.Outcome
			switch {
			case v.OverriddenBy != "":
				state = "marked " + v.Outcome
			case v.Status == string(engine.StepWaitingS):
				state = "waiting"
			}
			fmt.Fprintf(cl.out, "%-24s #%-5d %-16s %-12s %-14s %-12s %d/%d  %s\n", v.RunID, v.RunNumber, v.Step, v.Environment,
				v.Service, state, v.Intervals, v.Total, oneLine(v.Summary, 100))
		}
	})
}

func (cl *client) showVerifications(args []string) error {
	fs := flag.NewFlagSet("verifications show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usageErr("verifications show takes a Sortie id\n\n%s", verifyUsage)
	}
	var list []engine.VerificationView
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(rest[0])+"/verifications", nil, &list); err != nil {
		return err
	}
	return cl.print(list, *asJSON, func() {
		if len(list) == 0 {
			fmt.Fprintln(cl.out, "This run has no verifications.")
		}
		for _, v := range list {
			state := v.Verdict.Outcome
			if v.Override != "" {
				state = "marked " + v.Override + " by " + v.OverrideBy + ": " + v.OverrideComment
			} else if v.Status == string(engine.StepWaitingS) {
				state = "inconclusive, waiting for a person"
			}
			fmt.Fprintf(cl.out, "%s: %s, %s in %s (%d of %d intervals, sensitivity %s)\n", v.Step, state, v.Service, v.Environment,
				len(v.Intervals), v.Total, v.Sensitivity)
			if v.Verdict.Summary != "" {
				fmt.Fprintf(cl.out, "  %s\n", v.Verdict.Summary)
			}
			for _, m := range v.Verdict.Metrics {
				fmt.Fprintf(cl.out, "  metric %-20s %-12s %s\n", m.Name, m.Outcome, oneLine(m.Reason, 120))
			}
			for _, l := range v.Verdict.Logs {
				fmt.Fprintf(cl.out, "  logs   %-20s %-12s %s\n", l.Name, l.Outcome, oneLine(l.Reason, 120))
				for i, c := range l.Clusters {
					if i == 5 {
						fmt.Fprintf(cl.out, "         ... %d more clusters\n", len(l.Clusters)-5)
						break
					}
					fmt.Fprintf(cl.out, "         %6d  %-20s %s\n", c.Count, c.Class, oneLine(c.Template, 100))
				}
			}
			if x := v.Explanation; x != nil && x.Error == "" {
				fmt.Fprintf(cl.out, "  %s (model output, untrusted): %s\n", x.Model, oneLine(x.Summary, 200))
			}
		}
	})
}

func (cl *client) markVerification(args []string) error {
	fs := flag.NewFlagSet("verifications mark", flag.ContinueOnError)
	reason := fs.String("reason", "", "why (required; kept in the audit trail)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 3 || (rest[2] != "passed" && rest[2] != "failed") {
		return usageErr("verifications mark takes a Sortie id, a step and passed or failed\n\n%s", verifyUsage)
	}
	if strings.TrimSpace(*reason) == "" {
		return usageErr("verifications mark needs --reason")
	}
	path := "/api/v1/runs/" + url.PathEscape(rest[0]) + "/steps/" + url.PathEscape(rest[1]) + "/verify/override"
	if err := cl.call("POST", path, map[string]string{"outcome": rest[2], "reason": *reason}, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%s marked %s\n", rest[1], rest[2])
	return nil
}

func (cl *client) healthSources(args []string) error {
	if len(args) > 0 && configVerb(args[0]) {
		return cl.config(args, "health-sources")
	}
	fs := flag.NewFlagSet("health-sources", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var list []struct {
		Name     string   `json:"name"`
		Type     string   `json:"type"`
		Projects []string `json:"projects"`
	}
	if err := cl.call("GET", "/api/v1/health-sources", nil, &list); err != nil {
		return err
	}
	return cl.print(list, *asJSON, func() {
		if len(list) == 0 {
			fmt.Fprintln(cl.out, "No health sources are configured on this server.")
		}
		for _, h := range list {
			fmt.Fprintf(cl.out, "%-20s %-14s %s\n", h.Name, h.Type, strings.Join(h.Projects, ","))
		}
	})
}

func (cl *client) models(args []string) error {
	if len(args) > 0 && configVerb(args[0]) {
		return cl.config(args, "models")
	}
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var out struct {
		Models []engine.ModelView `json:"models"`
	}
	if err := cl.call("GET", "/api/v1/ai/models", nil, &out); err != nil {
		return err
	}
	return cl.print(out.Models, *asJSON, func() {
		if len(out.Models) == 0 {
			fmt.Fprintln(cl.out, "No models are configured on this server: AI features are off, and everything else works.")
		}
		for _, m := range out.Models {
			where := "hosted"
			if m.Local {
				where = "local"
			}
			budget := "no daily budget"
			if m.TokensPerDay > 0 {
				budget = fmt.Sprintf("%d tokens a day per project", m.TokensPerDay)
			}
			fmt.Fprintf(cl.out, "%-16s %-10s %-28s %-6s max %d tokens a call, %d calls a Sortie, %s; projects %s\n", m.Name, m.Provider, m.Model,
				where, m.MaxTokens, m.CallsPerRun, budget, strings.Join(m.Projects, ","))
			for _, u := range m.UsedToday {
				fmt.Fprintf(cl.out, "    %s today: %d calls, %d in, %d out, $%.4f\n", u.Project, u.Calls, u.InputTokens, u.OutputTokens, u.CostUSD)
			}
		}
	})
}

// oneLine puts text on one line, at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
