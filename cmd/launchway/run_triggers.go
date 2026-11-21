package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

const runTriggersUsage = `Usage:
  launchway run-triggers list PROJECT [--json]           triggers of PROJECT on other projects' results
  launchway run-triggers create PROJECT --name NAME --on UPSTREAM [--launch] [--when RESULT] [--pipeline NAME]
                                [--filter EXPR] [--ref REF] [--map INPUT=EXPRESSION ...]
  launchway run-triggers delete PROJECT NAME
  launchway run-triggers graph PROJECT [--create] [--json]   follow the software graph: what PROJECT depends on, and triggers for it
  launchway run-triggers allow UPSTREAM DOWNSTREAM [--share-outputs]   let DOWNSTREAM trigger on UPSTREAM's results
  launchway run-triggers allowed UPSTREAM [--json]       who may trigger on UPSTREAM
  launchway run-triggers revoke UPSTREAM DOWNSTREAM

graph shows the projects the software graph says PROJECT's repository depends on, in build order, whether each has
allowed PROJECT, and whether a trigger exists; with --create it makes a trigger (named graph-UPSTREAM, on a run that
ended well) for each that allowed it. Each project's result starts the next level, so providers build before
consumers. Nothing is made without --create.

When a run (or, with --launch, a deployment) of UPSTREAM ends, a run of PROJECT starts, with inputs mapped from
the result by expressions over run (id, number, project, pipeline, status, ref, branch, tag, sha, url), launch
(verdict, environment, service, version) and outputs (only if UPSTREAM allowed --share-outputs).
--when is succeeded, failed, canceled or any for a run; deployed, failed, rolled_back or any for a launch.
UPSTREAM must have allowed PROJECT first (an operator of UPSTREAM does that), and a cycle is refused.
No secret, log or file crosses: the result is described by reference.
`

func cmdRunTriggers(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, runTriggersUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.triggersList(args[1:])
		case "create":
			return cl.triggersCreate(args[1:])
		case "delete":
			return cl.triggersDelete(args[1:])
		case "graph":
			return cl.triggersGraph(args[1:])
		case "allow":
			return cl.triggersAllow(args[1:])
		case "allowed":
			return cl.triggersAllowed(args[1:])
		case "revoke":
			return cl.triggersRevoke(args[1:])
		}
		return usageErr("unknown command run-triggers %s\n\n%s", args[0], runTriggersUsage)
	})
}

func (cl *client) triggersList(args []string) error {
	fs := flag.NewFlagSet("run-triggers list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			err = usageErr("say which project: launchway run-triggers list PROJECT")
		}
		return err
	}
	var list []engine.RunTrigger
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/run-triggers", nil, &list); err != nil {
		return err
	}
	return cl.print(list, *asJSON, func() {
		if len(list) == 0 {
			fmt.Fprintln(cl.out, "No triggers on other projects' results.")
		}
		for _, t := range list {
			fmt.Fprintf(cl.out, "%s: when a %s of %s ends %s, start a run of %s\n", t.Name, t.Kind, t.UpstreamProject, t.When, t.Project)
			keys := make([]string, 0, len(t.Mapping))
			for k := range t.Mapping {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(cl.out, "    %s = %s\n", k, t.Mapping[k])
			}
		}
	})
}

func (cl *client) triggersCreate(args []string) error {
	fs := flag.NewFlagSet("run-triggers create", flag.ContinueOnError)
	name := fs.String("name", "", "the trigger's name")
	on := fs.String("on", "", "the upstream project")
	launch := fs.Bool("launch", false, "trigger on a deployment that ended, not a run")
	when := fs.String("when", "", "which result starts it")
	pl := fs.String("pipeline", "", "only results of the upstream pipeline with this name")
	filter := fs.String("filter", "", "an expression that must be true")
	ref := fs.String("ref", "", "the branch or tag whose pipeline runs")
	var maps stringList
	fs.Var(&maps, "map", "INPUT=EXPRESSION (repeat for each input)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *name == "" || *on == "" {
		return usageErr("say the project, --name and --on\n\n%s", runTriggersUsage)
	}
	t := engine.RunTrigger{Name: *name, UpstreamProject: *on, When: *when, UpstreamPipeline: *pl, Filter: *filter, Ref: *ref, Mapping: map[string]string{}, Kind: engine.TriggerOnRun}
	if *launch {
		t.Kind = engine.TriggerOnLaunch
	}
	for _, kv := range maps {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || v == "" {
			return usageErr("--map takes INPUT=EXPRESSION, not %q", kv)
		}
		t.Mapping[k] = v
	}
	var out engine.RunTrigger
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(pos[0])+"/run-triggers", t, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Made the trigger %s: a run of %s starts when a %s of %s ends %s.\n", out.Name, out.Project, out.Kind, out.UpstreamProject, out.When)
	return nil
}

func (cl *client) triggersDelete(args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("run-triggers delete", flag.ContinueOnError), args)
	if err != nil || len(pos) != 2 {
		if err == nil {
			err = usageErr("say the project and the trigger: launchway run-triggers delete PROJECT NAME")
		}
		return err
	}
	if err := cl.call("DELETE", "/api/v1/projects/"+url.PathEscape(pos[0])+"/run-triggers/"+url.PathEscape(pos[1]), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Deleted the trigger %s.\n", pos[1])
	return nil
}

func (cl *client) triggersAllow(args []string) error {
	fs := flag.NewFlagSet("run-triggers allow", flag.ContinueOnError)
	share := fs.Bool("share-outputs", false, "also let it read the step outputs of the upstream's runs")
	pos, err := parseArgs(fs, args)
	if err != nil || len(pos) != 2 {
		if err == nil {
			err = usageErr("say the upstream and the downstream project: launchway run-triggers allow UPSTREAM DOWNSTREAM")
		}
		return err
	}
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(pos[0])+"/run-trigger-allows", map[string]any{"downstream_project": pos[1], "share_outputs": *share}, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%s may now trigger on the results of %s.\n", pos[1], pos[0])
	return nil
}

func (cl *client) triggersAllowed(args []string) error {
	fs := flag.NewFlagSet("run-triggers allowed", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			err = usageErr("say which project: launchway run-triggers allowed UPSTREAM")
		}
		return err
	}
	var list []engine.RunTriggerAllow
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/run-trigger-allows", nil, &list); err != nil {
		return err
	}
	return cl.print(list, *asJSON, func() {
		if len(list) == 0 {
			fmt.Fprintln(cl.out, "No project may trigger on this project's results.")
		}
		for _, a := range list {
			extra := ""
			if a.ShareOutputs {
				extra = " (and read step outputs)"
			}
			fmt.Fprintf(cl.out, "%s%s\n", a.Downstream, extra)
		}
	})
}

func (cl *client) triggersRevoke(args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("run-triggers revoke", flag.ContinueOnError), args)
	if err != nil || len(pos) != 2 {
		if err == nil {
			err = usageErr("say the upstream and the downstream project: launchway run-triggers revoke UPSTREAM DOWNSTREAM")
		}
		return err
	}
	if err := cl.call("DELETE", "/api/v1/projects/"+url.PathEscape(pos[0])+"/run-trigger-allows/"+url.PathEscape(pos[1]), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%s may no longer trigger on the results of %s.\n", pos[1], pos[0])
	return nil
}

func (cl *client) triggersGraph(args []string) error {
	fs := flag.NewFlagSet("run-triggers graph", flag.ContinueOnError)
	create := fs.Bool("create", false, "make the triggers (without it, only show what would be made)")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			err = usageErr("say which project: launchway run-triggers graph PROJECT [--create]")
		}
		return err
	}
	method := "GET"
	var body any
	if *create {
		method, body = "POST", map[string]any{}
	}
	var plan struct {
		Project    string   `json:"project"`
		Repository string   `json:"repository"`
		Note       string   `json:"note"`
		Cycles     []string `json:"cycles"`
		Items      []struct {
			Upstream string `json:"upstream_project"`
			Repo     string `json:"upstream_repository"`
			Level    int    `json:"level"`
			Allowed  bool   `json:"allowed"`
			Trigger  string `json:"trigger"`
			Created  bool   `json:"created"`
			Note     string `json:"note"`
		} `json:"items"`
	}
	if err := cl.call(method, "/api/v1/projects/"+url.PathEscape(pos[0])+"/run-triggers/graph", body, &plan); err != nil {
		return err
	}
	return cl.print(plan, *asJSON, func() {
		if plan.Note != "" {
			fmt.Fprintln(cl.out, plan.Note)
		}
		for _, it := range plan.Items {
			fmt.Fprintf(cl.out, "level %d  %s (%s): %s\n", it.Level, it.Upstream, it.Repo, it.Note)
		}
		for _, c := range plan.Cycles {
			fmt.Fprintf(cl.out, "cycle: %s\n", c)
		}
	})
}
