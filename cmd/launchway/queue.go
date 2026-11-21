package main

import (
	"flag"
	"fmt"
	"net/url"
)

// queue lists the runs waiting in queues, or moves one:
// launchway runs queue [project] | next|hold|release <run-id>.
func (cl *client) queue(args []string) error {
	if len(args) == 2 {
		action := map[string]string{"next": "run_next", "hold": "hold", "release": "release"}[args[0]]
		if action == "" {
			return usageErr("say next, hold or release, then the run: launchway runs queue next <run-id>")
		}
		if err := cl.call("POST", "/api/v1/runs/"+url.PathEscape(args[1])+"/queue", map[string]string{"action": action}, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Done: %s for run %s.\n", args[0], args[1])
		return nil
	}
	fs := flag.NewFlagSet("runs queue", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	path := "/api/v1/queue"
	if len(pos) == 1 {
		path += "?project=" + url.QueryEscape(pos[0])
	}
	var q []struct {
		Run      string `json:"run"`
		Number   int    `json:"number"`
		Project  string `json:"project"`
		Position int    `json:"position"`
		Code     string `json:"code"`
		Reason   string `json:"reason"`
	}
	if err := cl.call("GET", path, nil, &q); err != nil {
		return err
	}
	return cl.print(q, *asJSON, func() {
		if len(q) == 0 {
			fmt.Fprintln(cl.out, "No run is waiting in a queue.")
		}
		for _, e := range q {
			fmt.Fprintf(cl.out, "%d. %s run #%d (%s): %s\n", e.Position, e.Project, e.Number, e.Run, e.Reason)
		}
	})
}

// trust approves a restricted run: "Run with secrets", recorded with your name.
func (cl *client) trust(args []string) error {
	fs := flag.NewFlagSet("runs trust", flag.ContinueOnError)
	comment := fs.String("comment", "", "why you trust the change; recorded")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs trust <run-id> [--comment text]")
	}
	if err := cl.call("POST", "/api/v1/runs/"+url.PathEscape(pos[0])+"/trust", map[string]string{"comment": *comment}, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Approved run %s: its steps that have not started may read secrets and deploy.\n", pos[0])
	return nil
}
