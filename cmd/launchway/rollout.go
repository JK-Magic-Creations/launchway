package main

// launchway rollout: look into a delivery and steer it. The same routes
// the web interface and MCP use; the server decides who may, and its
// refusals are printed as they are.

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/deploy/inspect"
)

const rolloutUsage = `Usage:
  launchway rollout status   RUN DEPLOYMENT [--json]    the strategy, steps, weights and what can be done now
  launchway rollout tree     RUN DEPLOYMENT [--json]    every object the delivery made, live, with health and roles
  launchway rollout manifest RUN DEPLOYMENT OBJECT      one object's manifest (OBJECT is Kind/namespace/name), secrets hidden
  launchway rollout logs     RUN DEPLOYMENT (--pod P | --replica-set R | --role stable|canary|preview|active | --job J)
                             [--container C] [--previous] [--tail N] [--follow]
  launchway rollout pause|resume|restart RUN DEPLOYMENT --reason TEXT [--object KIND/NS/NAME]
  launchway rollout scale    RUN DEPLOYMENT --replicas N --reason TEXT [--object KIND/NS/NAME]
  launchway rollout promote|promote-full|abort|retry RUN DEPLOYMENT --reason TEXT

RUN is a run's ID (run_...), DEPLOYMENT a deployment's (dep_...; launchway runs show lists them).
pause, resume, restart and scale act on the delivery's workload (found from the tree unless --object
is given) when its Airfield allows; promote, promote-full, abort and retry are commands to the rollout.
Every control needs a reason, which is kept in the Logbook. Roles: the tree and manifests need
the viewer role; logs and controls the operator role on the environment.
`

func cmdRollout(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Print(rolloutUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).rollout(args)
	})
}

func (cl *client) rollout(args []string) error {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status", "tree", "manifest", "logs", "pause", "resume", "restart", "scale", "promote", "promote-full", "abort", "retry":
	default:
		return usageErr("unknown command rollout %s\n\n%s", cmd, rolloutUsage)
	}
	fs := flag.NewFlagSet("rollout "+cmd, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	reason := fs.String("reason", "", "why; required for a control, kept in the Logbook")
	object := fs.String("object", "", "the workload, Kind/namespace/name")
	replicas := fs.Int("replicas", 0, "for scale: the new count")
	pod := fs.String("pod", "", "logs of a pod")
	rs := fs.String("replica-set", "", "logs of all pods of a ReplicaSet")
	role := fs.String("role", "", "logs of the pods of the ReplicaSet with this role: stable, canary, preview or active")
	job := fs.String("job", "", "logs of a Job's pods (a push test)")
	container := fs.String("container", "", "a container")
	previous := fs.Bool("previous", false, "the earlier container of a restarted pod")
	tail := fs.Int("tail", 200, "lines from the end")
	follow := fs.Bool("follow", false, "keep reading new lines")
	pos, err := parseArgs(fs, rest)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usageErr("say which deployment: RUN DEPLOYMENT\n\n%s", rolloutUsage)
	}
	base := "/api/v1/runs/" + url.PathEscape(pos[0]) + "/deployments/" + url.PathEscape(pos[1])
	switch cmd {
	case "status", "tree":
		var v api.TreeView
		if err := cl.call("GET", base+"/inspect", nil, &v); err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Fprintln(cl.out, string(b))
			return nil
		}
		cl.printRollout(&v, cmd == "tree")
		return nil
	case "manifest":
		if len(pos) < 3 {
			return usageErr("say which object: Kind/namespace/name\n\n%s", rolloutUsage)
		}
		var v struct {
			YAML string `json:"yaml"`
		}
		if err := cl.call("GET", base+"/inspect/manifest?object="+url.QueryEscape(pos[2]), nil, &v); err != nil {
			return err
		}
		fmt.Fprint(cl.out, v.YAML)
		return nil
	case "logs":
		return cl.rolloutLogs(base, logFlags{*pod, *rs, *role, *job, *container, *previous, *tail, *follow})
	case "promote", "promote-full", "abort", "retry":
		if strings.TrimSpace(*reason) == "" {
			return usageErr("say why: --reason \"...\" (kept in the Logbook)")
		}
		var out map[string]any
		if err := cl.call("POST", base+"/"+cmd, map[string]string{"comment": *reason}, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Asked for %s; a deploy runner carries it out.\n", cmd)
		return nil
	}
	return cl.rolloutWorkload(cmd, base, *object, *reason, *replicas)
}

type logFlags struct {
	pod, rs, role, job, container string
	previous                      bool
	tail                          int
	follow                        bool
}

func (cl *client) rolloutLogs(base string, f logFlags) error {
	q := url.Values{}
	for k, v := range map[string]string{"pod": f.pod, "replica_set": f.rs, "role": f.role, "job": f.job, "container": f.container} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 || f.pod == "" && f.rs == "" && f.role == "" && f.job == "" {
		return usageErr("say what to read: --pod, --replica-set, --role or --job\n\n%s", rolloutUsage)
	}
	if f.previous {
		q.Set("previous", "true")
	}
	q.Set("tail", fmt.Sprint(f.tail))
	if f.follow {
		return cl.followLogs(base + "/inspect/logs/stream?" + q.Encode())
	}
	var v struct {
		Lines []inspect.LogLine `json:"lines"`
		Notes []string          `json:"notes"`
	}
	if err := cl.call("GET", base+"/inspect/logs?"+q.Encode(), nil, &v); err != nil {
		return err
	}
	for _, l := range v.Lines {
		cl.printLogLine(l)
	}
	for _, n := range v.Notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	return nil
}

// rolloutWorkload pauses, resumes, restarts or scales the delivery's workload.
func (cl *client) rolloutWorkload(cmd, base, object, reason string, replicas int) error {
	if strings.TrimSpace(reason) == "" {
		return usageErr("say why: --reason \"...\" (kept in the Logbook)")
	}
	obj := object
	if obj == "" {
		var v api.TreeView
		if err := cl.call("GET", base+"/inspect", nil, &v); err != nil {
			return err
		}
		for _, n := range v.Tree.Nodes {
			if n.Parent == "" && (n.Kind == "Rollout" || n.Kind == "Deployment" && obj == "") && n.Change != inspect.ChangeLeftover {
				obj = n.ID
			}
		}
		if obj == "" {
			return usageErr("the delivery has no workload to act on; give --object")
		}
	}
	var out struct{ Result, Detail string }
	body := map[string]any{"action": cmd, "object": obj, "replicas": replicas, "reason": reason}
	if err := cl.call("POST", base+"/inspect/control", body, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%s: %s\n", out.Result, out.Detail)
	return nil
}

func (cl *client) printLogLine(l inspect.LogLine) {
	t := ""
	if l.T > 0 {
		t = time.UnixMilli(l.T).UTC().Format("15:04:05.000") + " "
	}
	fmt.Fprintf(cl.out, "%s[%s] %s\n", t, l.Src, l.Line)
}

// followLogs reads the server-sent events of a followed log until it ends.
func (cl *client) followLogs(path string) error {
	req, err := http.NewRequestWithContext(context.Background(), "GET", cl.c.Server+path, nil) //nolint:gosec // G704: the user's own configured server
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cl.c.Token)
	req.Header.Set("User-Agent", "launchway-cli/"+version)
	resp, err := (&http.Client{}).Do(req) //nolint:gosec // G704: the user's own configured server
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			switch event {
			case "lines":
				var ls []inspect.LogLine
				if json.Unmarshal([]byte(data), &ls) == nil {
					for _, l := range ls {
						cl.printLogLine(l)
					}
				}
			case "end":
				var e struct {
					State, Error string
					Notes        []string
				}
				_ = json.Unmarshal([]byte(data), &e)
				for _, n := range e.Notes {
					fmt.Fprintln(os.Stderr, "note:", n)
				}
				if e.Error != "" {
					return fmt.Errorf("the log ended: %s", e.Error)
				}
				return nil
			}
		}
	}
	return sc.Err()
}

func (cl *client) printRollout(v *api.TreeView, tree bool) {
	fmt.Fprintf(cl.out, "%s in %s (application %s, Airfield %s)%s\n", v.Deployment, v.Environment, v.Application, v.Target, map[bool]string{true: ", Launch over", false: ""}[v.Closed])
	if ro := v.Tree.Rollout; ro != nil {
		fmt.Fprintf(cl.out, "%s %s: %s", ro.Kind, ro.Name, nonEmptyStr(ro.Phase, "-"))
		if ro.Paused {
			fmt.Fprint(cl.out, ", paused")
		}
		if ro.Aborted {
			fmt.Fprint(cl.out, ", aborted")
		}
		fmt.Fprintf(cl.out, "; strategy %s\n", ro.Strategy)
		if ro.DesiredWeight >= 0 || ro.ActualWeight >= 0 {
			fmt.Fprintf(cl.out, "Traffic to the new version: wants %d%%, has %d%% (%s)\n", ro.DesiredWeight, ro.ActualWeight, ro.ActualSource)
		}
		if ro.PreTraffic != nil {
			fmt.Fprintf(cl.out, "0%% of real traffic; testing through %s\n", ro.PreTraffic.Address)
		}
		for _, s := range ro.Steps {
			mark := map[string]string{"done": "x", "current": ">", "next": " "}[s.State]
			fmt.Fprintf(cl.out, "  [%s] %d. %s\n", mark, s.Index+1, s.Label)
		}
	}
	fmt.Fprintln(cl.out, "Can do now:")
	for _, a := range v.Actions {
		if a.OK {
			fmt.Fprintf(cl.out, "  %s\n", a.Action)
		} else {
			fmt.Fprintf(cl.out, "  %s: not now: %s\n", a.Action, a.Why)
		}
	}
	for _, n := range v.Tree.Notes {
		fmt.Fprintln(cl.out, "note:", n)
	}
	if !tree {
		return
	}
	fmt.Fprintln(cl.out, "Objects:")
	for _, n := range v.Tree.Nodes {
		line := fmt.Sprintf("  %-9s %-60s", n.Health, n.ID)
		if n.Role != "" {
			line += " " + n.Role
		}
		if n.Replicas != nil {
			line += fmt.Sprintf(" %d/%d", n.Replicas.Ready, n.Replicas.Desired)
		}
		if n.Change != "" && n.Change != inspect.ChangeUnchanged {
			line += " [" + n.Change + "]"
		}
		if n.NotReady != nil {
			line += "  " + n.NotReady.Why
		} else if n.Message != "" {
			line += "  " + n.Message
		}
		fmt.Fprintln(cl.out, line)
	}
}
