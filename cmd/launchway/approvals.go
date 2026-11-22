package main

// launchway approvals: list the approvals waiting for a decision, and approve
// or reject one. The same routes the web interface and MCP use; the server
// decides who may, and its refusals are printed as they are.

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const approvalsUsage = `Usage:
  launchway approvals list [--project P] [--json]       approvals waiting for a decision
  launchway approvals approve <run-id> <step> [--comment TEXT]
  launchway approvals reject <run-id> <step> [--comment TEXT]
`

func cmdApprovals(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Print(approvalsUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).approvals(args)
	})
}

// approvalView is an approval as GET /api/v1/approvals lists it.
type approvalView struct {
	RunID               string `json:"run_id"`
	Project             string `json:"project"`
	RunNumber           int    `json:"run_number"`
	StepID              string `json:"step_id"`
	Message             string `json:"message"`
	StartedBy           string `json:"started_by"`
	WaitingSince        int64  `json:"waiting_since"`
	Deadline            int64  `json:"deadline"`
	ApprovalsNeeded     int    `json:"approvals_needed"`
	ApprovalsNeededFrom string `json:"approvals_needed_from"`
	Approvals           []struct {
		Actor string `json:"actor"`
	} `json:"approvals"`
}

func (cl *client) approvals(args []string) error {
	switch args[0] {
	case "list":
		return cl.approvalsList(args[1:])
	case "approve":
		return cl.decideApproval(args[1:], "approve")
	case "reject":
		return cl.decideApproval(args[1:], "reject")
	}
	return usageErr("unknown command approvals %s\n\n%s", args[0], approvalsUsage)
}

func (cl *client) approvalsList(args []string) error {
	fs := flag.NewFlagSet("approvals list", flag.ContinueOnError)
	project := fs.String("project", "", "only this Flight's approvals")
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return usageErr("approvals list takes no arguments\n\n%s", approvalsUsage)
	}
	var raw json.RawMessage
	if err := cl.call("GET", "/api/v1/approvals", nil, &raw); err != nil {
		return err
	}
	var list []approvalView
	if err := json.Unmarshal(raw, &list); err != nil {
		return apiErr("the server's answer is not what this command expects: %v", err)
	}
	if *project != "" {
		kept := list[:0]
		for _, a := range list {
			if a.Project == *project {
				kept = append(kept, a)
			}
		}
		list = kept
	}
	if *asJSON {
		b, _ := json.MarshalIndent(list, "", "  ")
		fmt.Fprintln(cl.out, string(b))
		return nil
	}
	if len(list) == 0 {
		fmt.Fprintln(cl.out, "Nothing is waiting for approval.")
		return nil
	}
	for _, a := range list {
		fmt.Fprintf(cl.out, "%s #%d  %s  run %s\n  %s\n  started by %s, waiting since %s, expires %s\n", a.Project, a.RunNumber, a.StepID, a.RunID,
			a.Message, nonEmptyText(a.StartedBy, "someone"), stamp(a.WaitingSince), stamp(a.Deadline))
		if a.ApprovalsNeeded > 1 {
			names := make([]string, len(a.Approvals))
			for i, p := range a.Approvals {
				names[i] = p.Actor
			}
			line := fmt.Sprintf("  %d of %d approved", len(names), a.ApprovalsNeeded)
			if len(names) > 0 {
				line += " (" + strings.Join(names, ", ") + ")"
			}
			if a.ApprovalsNeededFrom != "" {
				line += "; the number comes from " + a.ApprovalsNeededFrom
			}
			fmt.Fprintln(cl.out, line)
		}
	}
	return nil
}

func (cl *client) decideApproval(args []string, verb string) error {
	fs := flag.NewFlagSet("approvals "+verb, flag.ContinueOnError)
	comment := fs.String("comment", "", "kept with the decision")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("say which approval: launchway approvals %s <run-id> <step> [--comment TEXT]", verb)
	}
	var got struct {
		Approvals []struct {
			Actor string `json:"actor"`
		} `json:"approvals"`
		Needed int  `json:"approvals_needed"`
		Done   bool `json:"done"`
	}
	path := "/api/v1/runs/" + url.PathEscape(pos[0]) + "/steps/" + url.PathEscape(pos[1]) + "/" + verb
	if err := cl.call("POST", path, map[string]string{"comment": *comment}, &got); err != nil {
		return err
	}
	switch {
	case verb == "reject":
		fmt.Fprintf(cl.out, "Rejected %s of run %s.\n", pos[1], pos[0])
	case got.Needed > 1 && !got.Done:
		fmt.Fprintf(cl.out, "Approved %s of run %s: %d of %d approved; it waits for more.\n", pos[1], pos[0], len(got.Approvals), got.Needed)
	default:
		fmt.Fprintf(cl.out, "Approved %s of run %s.\n", pos[1], pos[0])
	}
	return nil
}

func stamp(ms int64) string {
	if ms <= 0 {
		return "never"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}

func nonEmptyText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// serviceQuery asks the logs route for one service's lines.
func serviceQuery(service string) string {
	if service == "" {
		return ""
	}
	return "&stream=service&service=" + url.QueryEscape(service)
}
