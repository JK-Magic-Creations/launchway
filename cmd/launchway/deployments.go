package main

// launchway deployments: roll a service back after its deployment succeeded,
// see what can be rolled back to, and review emergency rollbacks. The same
// routes the web interface and MCP use; the server decides what is possible
// and who may, and its refusals are printed as they are.

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

var deploymentsUsage = themed(`Usage:
  launchway deployments options  --env E [--project P] [--service S] [--json]
        what a service can go back to in an {environment}, and what each choice does
  launchway deployments rollback --env E [--project P] [--service S] [--to DEPLOYMENT|VERSION]
                                 --reason TEXT [--queue] [--revert-toggles] [--dry-run]
        send a service back to an earlier successful deployment (the previous one by default): a {rollback}
  launchway deployments review list [--json]       {emergency_rollbacks} waiting for a review
  launchway deployments review RUN-ID --comment TEXT   acknowledge one, saying what you checked

A {rollback} never restores a database or configuration, and restores a version, not the feature
toggles: the plan lists the toggle changes made here since that version went live, and
--revert-toggles reverts them with the {rollback} (or: launchway toggles revert-since).
With --queue it waits behind
a deployment that holds the {environment}'s lock instead of being refused.
`)

func cmdDeployments(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Print(deploymentsUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).deployments(args)
	})
}

func (cl *client) deployments(args []string) error {
	switch args[0] {
	case "options":
		return cl.rollbackOptions(args[1:])
	case "rollback":
		return cl.rollback(args[1:])
	case "review":
		return cl.rollbackReview(args[1:])
	}
	return usageErr("unknown command deployments %s\n\n%s", args[0], deploymentsUsage)
}

// unitFlags are the flags that name a service of an environment.
type unitFlags struct{ env, project, service *string }

func addUnitFlags(fs *flag.FlagSet) unitFlags {
	return unitFlags{
		env:     fs.String("env", "", "the environment"),
		project: fs.String("project", "", "the project (found from the environment's services when it is the only one)"),
		service: fs.String("service", "", "the service (default: the project's name)"),
	}
}

// resolve fills in the project from the environments the server lists when
// it was not given and one service matches.
func (cl *client) resolveUnit(u unitFlags) (project, env, service string, err error) {
	env, project, service = *u.env, *u.project, *u.service
	if env == "" {
		return "", "", "", usageErr("say which environment: --env dev\n\n%s", deploymentsUsage)
	}
	if project != "" {
		return project, env, service, nil
	}
	var envs []engine.EnvironmentView
	if err := cl.call("GET", "/api/v1/environments", nil, &envs); err != nil {
		return "", "", "", err
	}
	var found []string
	for _, e := range envs {
		if e.Name != env {
			continue
		}
		for _, s := range e.Services {
			if service == "" || s.Service == service || (service == s.Project) {
				found = append(found, s.Project)
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], env, service, nil
	case 0:
		return "", "", "", usageErr("no service is deployed to %s that this token can see; give --project", env)
	}
	return "", "", "", usageErr("several projects deploy to %s (%s); give --project", env, strings.Join(found, ", "))
}

func (cl *client) planOf(project, env, service string) (*engine.RollbackPlan, error) {
	q := url.Values{"environment": {env}}
	if service != "" {
		q.Set("service", service)
	}
	var plan engine.RollbackPlan
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(project)+"/rollback?"+q.Encode(), nil, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

func (cl *client) printPlan(plan *engine.RollbackPlan) {
	a := plan.Action
	where := plan.Service + " in " + plan.Environment
	if plan.Current != nil {
		fmt.Fprintf(cl.out, "%s runs %s (run #%d)\n", where, plan.Current.Version, plan.Current.Number)
	}
	if !a.Available && !a.Queueable {
		fmt.Fprintf(cl.out, cl.say("Cannot {rollback:do} %s: %s\n"), where, a.Reason)
		if a.WouldHelp != "" {
			fmt.Fprintf(cl.out, "  would help: %s\n", a.WouldHelp)
		}
		return
	}
	if !a.Available {
		fmt.Fprintf(cl.out, cl.say("Cannot {rollback:do} %s yet: %s\n  instead: %s\n"), where, a.Reason, a.Instead)
	}
	if a.Emergency {
		fmt.Fprintln(cl.out, "This is an emergency action: the environment is protected, so no approval comes first and an approver reviews it afterwards.")
		if a.RequiresApproval {
			fmt.Fprintln(cl.out, "A setting asks for an approval first even then.")
		}
	}
	fmt.Fprintln(cl.out, "Earlier successful deployments:")
	for _, t := range a.Targets {
		mark := " "
		if t.Previous {
			mark = "*"
		}
		fmt.Fprintf(cl.out, " %s %s  deployed %s by run #%d (deployment %s)\n     %s\n", mark, t.Version, stamp(t.At), t.RunNumber, t.Deployment, t.Consequence)
	}
	fmt.Fprintln(cl.out, "  * the previous one, which is the default")
	fmt.Fprintln(cl.out, engine.RollbackNote)
}

func (cl *client) rollbackOptions(args []string) error {
	fs := flag.NewFlagSet("deployments options", flag.ContinueOnError)
	u := addUnitFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return usageErr("deployments options takes flags only\n\n%s", deploymentsUsage)
	}
	project, env, service, err := cl.resolveUnit(u)
	if err != nil {
		return err
	}
	plan, err := cl.planOf(project, env, service)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(plan, "", "  ")
		fmt.Fprintln(cl.out, string(b))
		return nil
	}
	cl.printPlan(plan)
	return nil
}

func (cl *client) rollback(args []string) error {
	fs := flag.NewFlagSet("deployments rollback", flag.ContinueOnError)
	u := addUnitFlags(fs)
	to := fs.String("to", "", "the deployment or version to restore (default: the previous successful one)")
	reason := fs.String("reason", "", "why; required, and recorded with the rollback")
	queue := fs.Bool("queue", false, "wait behind a deployment that holds the lock")
	revertToggles := fs.Bool("revert-toggles", false, "also revert the toggle changes made since that version went live")
	dry := fs.Bool("dry-run", false, "only say what it would do")
	if pos, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return usageErr("deployments rollback takes flags only\n\n%s", deploymentsUsage)
	}
	project, env, service, err := cl.resolveUnit(u)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*reason) == "" && !*dry {
		return usageErr("say why: --reason \"...\" (a rollback needs a reason, which is recorded)")
	}
	plan, err := cl.planOf(project, env, service)
	if err != nil {
		return err
	}
	cl.printPlan(plan)
	cl.printToggleChanges(project, env, service, *to)
	if *dry {
		return nil
	}
	body := map[string]any{"environment": env, "service": service, "to": *to, "reason": *reason, "queue": *queue, "revert_toggles": *revertToggles}
	var sum struct {
		engine.RunSummary
		ToggleReverts []struct {
			Toggle string `json:"toggle"`
			Error  string `json:"error"`
		} `json:"toggle_reverts"`
	}
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(project)+"/rollbacks", body, &sum); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, cl.say("\nStarted {rollback} {run} #%d (%s): %s\nFollow it with: launchway runs show %s\n"), sum.Number, sum.ID, sum.Message, sum.ID)
	for _, r := range sum.ToggleReverts {
		if r.Error != "" {
			fmt.Fprintf(cl.out, "Toggle %s could not be reverted: %s\n", r.Toggle, r.Error)
		} else {
			fmt.Fprintf(cl.out, "Toggle %s reverted.\n", r.Toggle)
		}
	}
	return nil
}

func (cl *client) rollbackReview(args []string) error {
	fs := flag.NewFlagSet("deployments review", flag.ContinueOnError)
	comment := fs.String("comment", "", "what you checked; required, and recorded")
	asJSON := fs.Bool("json", false, "print JSON (list)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 1 && pos[0] == "list" {
		var list []engine.ReviewItem
		if err := cl.call("GET", "/api/v1/rollbacks/review", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.MarshalIndent(list, "", "  ")
			fmt.Fprintln(cl.out, string(b))
			return nil
		}
		if len(list) == 0 {
			fmt.Fprintln(cl.out, cl.say("No {emergency_rollback} is waiting for review."))
			return nil
		}
		for _, it := range list {
			fmt.Fprintf(cl.out, "%s #%d  %s: %s → %s, by %s at %s  run %s\n  %s\n", it.Project, it.Number, it.Environment, it.FromVersion, it.ToVersion, it.Actor, stamp(it.At), it.Run, it.Reason)
			if !it.Acknowledge.Available {
				fmt.Fprintf(cl.out, "  you cannot acknowledge it: %s\n", it.Acknowledge.Reason)
			}
		}
		return nil
	}
	if len(pos) != 1 {
		return usageErr("say which rollback run: launchway deployments review <run-id> --comment TEXT\n\n%s", deploymentsUsage)
	}
	if strings.TrimSpace(*comment) == "" {
		return usageErr("say what you checked: --comment \"...\" (a review needs a comment, which is recorded)")
	}
	if err := cl.call("POST", "/api/v1/runs/"+url.PathEscape(pos[0])+"/review", map[string]string{"comment": *comment}, nil); err != nil {
		return err
	}
	fmt.Fprintln(cl.out, "Acknowledged. It is no longer waiting for review.")
	return nil
}

// printToggleChanges says which toggle changes a rollback would leave behind.
func (cl *client) printToggleChanges(project, env, service, to string) {
	q := url.Values{"environment": {env}, "service": {service}, "to": {to}}
	var plan struct {
		Toggles *struct {
			Changes []struct {
				ID     int64  `json:"id"`
				Toggle string `json:"toggle"`
				Actor  string `json:"actor"`
				Reason string `json:"reason"`
			} `json:"changes"`
		} `json:"toggles"`
	}
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(project)+"/rollback?"+q.Encode(), nil, &plan); err != nil || plan.Toggles == nil || len(plan.Toggles.Changes) == 0 {
		return
	}
	fmt.Fprintf(cl.out, "\nToggle changes made here since that version went live (a rollback leaves them as they are; --revert-toggles reverts them):\n")
	for _, c := range plan.Toggles.Changes {
		fmt.Fprintf(cl.out, "  #%d %s by %s: %s\n", c.ID, c.Toggle, c.Actor, c.Reason)
	}
}
