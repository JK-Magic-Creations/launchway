package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/toggles"
)

const togglesUsage = `Usage:
  launchway toggles list    PROJECT [--json]                 the project's toggles, with their state in each environment
  launchway toggles get     PROJECT KEY [--json]             one toggle: state and rules per environment, stale mentions
  launchway toggles create  PROJECT KEY --type boolean|string|number|json [--default V] [--kind K]
                            [--owner O] [--description D] [--expires 2027-03-01] [--client-visible] [--reason R]
  launchway toggles set     PROJECT KEY ENV (--on | --off | --percent N) [--serve JSON] [--reason R] [--at TIME]
        turn a toggle on or off in an environment, or roll it out to N percent (a boolean toggle: true
        for N percent of keys, false for the rest). The rules the environment already has are kept.
  launchway toggles rules   PROJECT KEY ENV -f FILE [--reason R] [--at TIME]   replace the environment's whole state from a YAML file
  launchway toggles evaluate PROJECT KEY --context JSON      what it serves for a context, in each environment (not counted)
  launchway toggles history PROJECT KEY [--environment E] [--json]
  launchway toggles revert  PROJECT CHANGE --reason R        go back over a change (never held for approval)
  launchway toggles waiting PROJECT                          changes that wait for an approval
  launchway toggles approve|reject|withdraw PROJECT CHANGE [--comment C]
  launchway toggles timeline PROJECT ENV                     the toggle changes of an environment, newest first
  launchway toggles revert-since PROJECT ENV --since MS --reason R [--dry-run]
  launchway toggles keys list PROJECT | create PROJECT ENV --kind server|client [--name N] [--origin URL]...
                       | rotate PROJECT ID [--overlap-hours N] | revoke PROJECT ID
  launchway toggles safeguards get PROJECT [--environment E]
  launchway toggles safeguards set PROJECT [--environment E] [--require-reason=BOOL] [--require-approval=BOOL]
                            [--approvals N] [--watch-minutes N] [--lock NAME]... --reason R
  launchway toggles export  PROJECT [-o FILE]                the project's toggles as a file (.launchway/toggles.yaml)
  launchway toggles plan    PROJECT -f FILE                  how the file and the live toggles differ; changes nothing
  launchway toggles apply   PROJECT -f FILE --reason R       make the live toggles match the file; never removes what it leaves out
  launchway toggles usage   PROJECT KEY                     where a toggle is evaluated and how often (counts only, nothing about who)
  launchway toggles schedules list PROJECT | cancel PROJECT ID   changes scheduled for later (made with set --at)
  launchway toggles lock|unlock PROJECT KEY [--reason R]     for a toggle shared from a level above: stop nearer levels defining it
  launchway toggles segments list PROJECT | set PROJECT NAME -f FILE [--reason R] | delete PROJECT NAME --reason R

Safeguards are on by default: a change needs a reason, and in a protected environment another person's approval.
A team may turn them down at its level (with a reason, which is recorded); a level above may lock them.
`

func cmdToggles(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, togglesUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		rest := args[1:]
		switch args[0] {
		case "list":
			return cl.togglesList(rest)
		case "get":
			return cl.togglesGet(rest)
		case "create":
			return cl.togglesCreate(rest)
		case "set":
			return cl.togglesSet(rest)
		case "rules":
			return cl.togglesRules(rest)
		case "evaluate":
			return cl.togglesEvaluate(rest)
		case "history":
			return cl.togglesHistory(rest)
		case "revert":
			return cl.togglesChange(rest, "revert")
		case "approve", "reject", "withdraw":
			return cl.togglesChange(rest, args[0])
		case "waiting":
			return cl.togglesGeneric(rest, "waiting", "GET", "/toggle-changes")
		case "timeline":
			return cl.togglesTimeline(rest)
		case "revert-since":
			return cl.togglesRevertSince(rest)
		case "keys":
			return cl.togglesKeys(rest)
		case "safeguards":
			return cl.togglesSafeguards(rest)
		case "export":
			return cl.togglesExport(rest)
		case "plan", "apply":
			return cl.togglesFile(rest, args[0])
		case "segments":
			return cl.togglesSegments(rest)
		case "usage":
			return cl.togglesUsage(rest)
		case "schedules":
			return cl.togglesSchedules(rest)
		case "lock", "unlock":
			return cl.togglesLock(rest, args[0])
		}
		return usageErr("unknown command toggles %s\n\n%s", args[0], togglesUsage)
	})
}

func tgPath(project, tail string) string { return "/api/v1/projects/" + url.PathEscape(project) + tail }

// togglesGeneric prints a GET's answer as JSON.
func (cl *client) togglesGeneric(args []string, name, method, tail string) error {
	fs := flag.NewFlagSet("toggles "+name, flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway toggles %s PROJECT", name)
	}
	var out any
	if err := cl.call(method, tgPath(pos[0], tail), nil, &out); err != nil {
		return err
	}
	return cl.print(out, true, nil)
}

type tgSummary struct {
	Key           string `json:"key"`
	Type          string `json:"type"`
	Kind          string `json:"kind"`
	Description   string `json:"description"`
	Owner         string `json:"owner"`
	Level         string `json:"level"`
	Inherited     bool   `json:"inherited"`
	ClientVisible bool   `json:"client_visible"`
	Environments  []struct {
		Environment string        `json:"environment"`
		Configured  bool          `json:"configured"`
		Mode        string        `json:"mode"`
		Serves      string        `json:"serves"`
		Waiting     int           `json:"waiting"`
		WatchUntil  int64         `json:"watch_until"`
		State       toggles.State `json:"state"`
	} `json:"environments"`
	Stale []struct {
		Reason string `json:"reason"`
		Detail string `json:"detail"`
	} `json:"stale"`
}

func (cl *client) printToggle(t tgSummary, long bool) {
	shared := ""
	if t.Inherited {
		shared = "  [shared from the " + t.Level + " level]"
	}
	fmt.Fprintf(cl.out, "%s  (%s, %s)%s\n", t.Key, t.Type, t.Kind, shared)
	if long && t.Description != "" {
		fmt.Fprintf(cl.out, "    %s\n", t.Description)
	}
	if long && t.Owner != "" {
		fmt.Fprintf(cl.out, "    owner: %s\n", t.Owner)
	}
	for _, e := range t.Environments {
		extra := ""
		if e.Waiting > 0 {
			extra += fmt.Sprintf("  [%d change(s) waiting for approval]", e.Waiting)
		}
		if e.WatchUntil > time.Now().UnixMilli() {
			extra += "  [watched until " + time.UnixMilli(e.WatchUntil).Format("15:04") + "]"
		}
		if !e.Configured {
			fmt.Fprintf(cl.out, "    %-14s never set: off\n", e.Environment)
			continue
		}
		fmt.Fprintf(cl.out, "    %-14s %s%s\n", e.Environment, e.Serves, extra)
		if long {
			for _, r := range e.State.Rules {
				fmt.Fprintf(cl.out, "        rule %s: %s\n", r.ID, toggles.Canonical(r))
			}
		}
	}
	for _, n := range t.Stale {
		fmt.Fprintf(cl.out, "    note (%s): %s\n", n.Reason, n.Detail)
	}
}

func (cl *client) togglesList(args []string) error {
	fs := flag.NewFlagSet("toggles list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway toggles list PROJECT [--json]")
	}
	var out struct {
		Toggles []tgSummary `json:"toggles"`
	}
	if err := cl.call("GET", tgPath(pos[0], "/toggles"), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Toggles) == 0 {
			fmt.Fprintln(cl.out, "No toggles yet. Make one with: launchway toggles create PROJECT KEY --type boolean")
		}
		for _, t := range out.Toggles {
			cl.printToggle(t, false)
		}
	})
}

func (cl *client) togglesGet(args []string) error {
	fs := flag.NewFlagSet("toggles get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway toggles get PROJECT KEY [--json]")
	}
	var t tgSummary
	if err := cl.call("GET", tgPath(pos[0], "/toggles/"+url.PathEscape(pos[1])), nil, &t); err != nil {
		return err
	}
	return cl.print(t, *asJSON, func() { cl.printToggle(t, true) })
}

func anyValue(typ, v string) (any, error) {
	switch typ {
	case "boolean":
		if v != "true" && v != "false" {
			return nil, usageErr("a boolean is true or false")
		}
		return v == "true", nil
	case "number":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, usageErr("%q is not a number", v)
		}
		return f, nil
	case "json":
		var x any
		if err := json.Unmarshal([]byte(v), &x); err != nil {
			return nil, usageErr("%q is not JSON", v)
		}
		return x, nil
	}
	return v, nil
}

func (cl *client) togglesCreate(args []string) error {
	fs := flag.NewFlagSet("toggles create", flag.ContinueOnError)
	typ := fs.String("type", "boolean", "boolean, string, number or json")
	def := fs.String("default", "", "what it serves while off")
	kind := fs.String("kind", "", "release, kill, experiment, operational or permission")
	owner := fs.String("owner", "", "who answers for it")
	desc := fs.String("description", "", "one line saying what it is for")
	expires := fs.String("expires", "", "the date it is expected to be removed")
	visible := fs.Bool("client-visible", false, "browsers and apps may ask for its value")
	reason := fs.String("reason", "", "why")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway toggles create PROJECT KEY --type T [...]")
	}
	body := map[string]any{"key": pos[1], "type": *typ, "description": *desc, "owner": *owner, "client_visible": *visible, "reason": *reason}
	if *kind != "" {
		body["kind"] = *kind
	}
	if *expires != "" {
		body["expires"] = *expires
	}
	if *def != "" {
		v, err := anyValue(*typ, *def)
		if err != nil {
			return err
		}
		body["default"] = v
	}
	var out any
	if err := cl.call("POST", tgPath(pos[0], "/toggles"), body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "Made %s. It is off in every environment; turn it on with: launchway toggles set %s %s ENV --on --reason \"...\"\n", pos[1], pos[0], pos[1])
	})
}

// stateOf reads a toggle's current state in an environment, to change part of it.
func (cl *client) stateOf(project, key, env string) (toggles.State, tgSummary, error) {
	var t tgSummary
	if err := cl.call("GET", tgPath(project, "/toggles/"+url.PathEscape(key)), nil, &t); err != nil {
		return toggles.State{}, t, err
	}
	for _, e := range t.Environments {
		if e.Environment == env {
			return e.State, t, nil
		}
	}
	return toggles.State{OffValue: false, Fallthrough: toggles.Serve{Value: false}}, t, nil
}

func (cl *client) togglesSet(args []string) error {
	fs := flag.NewFlagSet("toggles set", flag.ContinueOnError)
	on := fs.Bool("on", false, "turn on")
	off := fs.Bool("off", false, "turn off")
	percent := fs.Float64("percent", -1, "roll out to this percentage of keys")
	serve := fs.String("serve", "", "the value served when on, as JSON (default true for a boolean)")
	reason := fs.String("reason", "", "why; required by the safeguards")
	at := fs.String("at", "", "make the change at this time (RFC 3339) instead of now")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	n := 0
	for _, b := range []bool{*on, *off, *percent >= 0} {
		if b {
			n++
		}
	}
	if len(pos) != 3 || n != 1 {
		return usageErr("launchway toggles set PROJECT KEY ENV (--on | --off | --percent N) [--reason R]")
	}
	st, t, err := cl.stateOf(pos[0], pos[1], pos[2])
	if err != nil {
		return err
	}
	val := any(true)
	if *serve != "" {
		if err := json.Unmarshal([]byte(*serve), &val); err != nil {
			return usageErr("--serve is JSON: %v", err)
		}
	} else if t.Type != "boolean" && !*off {
		return usageErr("%s is a %s toggle: say the value it serves with --serve", pos[1], t.Type)
	}
	switch {
	case *off:
		st.On = false
	case *on:
		st.On = true
		st.Fallthrough = toggles.Serve{Value: val}
	default:
		if t.Type != "boolean" && *serve == "" {
			return usageErr("--percent serves --serve to that percentage and the off value to the rest")
		}
		w := int(*percent * 100)
		if w < 0 || w > 10000 {
			return usageErr("--percent is 0 to 100")
		}
		st.On = true
		st.Fallthrough = toggles.Serve{Rollout: &toggles.Rollout{Buckets: []toggles.Share{{Value: val, Weight: w}, {Value: st.OffValue, Weight: 10000 - w}}}}
	}
	return cl.putState(pos[0], pos[1], pos[2], st, *reason, *at, *asJSON)
}

func (cl *client) putState(project, key, env string, st toggles.State, reason, at string, asJSON bool) error {
	body := map[string]any{"state": st, "reason": reason, "at": at}
	var out struct {
		Waiting bool   `json:"waiting"`
		Needs   string `json:"needs"`
		Change  struct {
			ID int64 `json:"id"`
		} `json:"change"`
		Scheduled *struct {
			ID    int64 `json:"id"`
			RunAt int64 `json:"run_at"`
		} `json:"scheduled"`
	}
	if err := cl.call("PUT", tgPath(project, "/toggles/"+url.PathEscape(key)+"/environments/"+url.PathEscape(env)), body, &out); err != nil {
		return err
	}
	return cl.print(out, asJSON, func() {
		switch {
		case out.Scheduled != nil:
			fmt.Fprintf(cl.out, "Scheduled for %s (scheduled change %d).\n", time.UnixMilli(out.Scheduled.RunAt).Format(time.RFC3339), out.Scheduled.ID)
		case out.Waiting:
			fmt.Fprintf(cl.out, "Not made yet: it needs %s.\nChange %d waits; someone else approves it with: launchway toggles approve %s %d\n", out.Needs, out.Change.ID, project, out.Change.ID)
		default:
			fmt.Fprintf(cl.out, "Done (change %d). Undo it with: launchway toggles revert %s %d --reason \"...\"\n", out.Change.ID, project, out.Change.ID)
		}
	})
}

func (cl *client) togglesRules(args []string) error {
	fs := flag.NewFlagSet("toggles rules", flag.ContinueOnError)
	file := fs.String("f", "", "the YAML file of the Airfield's state (on, off_value, prerequisites, rules, fallthrough)")
	reason := fs.String("reason", "", "why")
	at := fs.String("at", "", "make the change at this time instead of now")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 3 || *file == "" {
		return usageErr("launchway toggles rules PROJECT KEY ENV -f FILE [--reason R]")
	}
	data, err := os.ReadFile(*file) //nolint:gosec // the person named their own file
	if err != nil {
		return usageErr("%v", err)
	}
	st, err := toggles.ParseState(data)
	if err != nil {
		return usageErr("%v", err)
	}
	return cl.putState(pos[0], pos[1], pos[2], st, *reason, *at, false)
}

func (cl *client) togglesEvaluate(args []string) error {
	fs := flag.NewFlagSet("toggles evaluate", flag.ContinueOnError)
	ctxText := fs.String("context", `{"key":"user-1"}`, "the context, as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway toggles evaluate PROJECT KEY --context JSON")
	}
	var c map[string]any
	if err := json.Unmarshal([]byte(*ctxText), &c); err != nil {
		return usageErr("--context is a JSON object: %v", err)
	}
	var out struct {
		Values map[string]struct {
			Value  any    `json:"value"`
			Reason string `json:"reason"`
			RuleID string `json:"rule_id"`
		} `json:"values"`
	}
	if err := cl.call("POST", tgPath(pos[0], "/toggles/"+url.PathEscape(pos[1])+"/evaluate"), map[string]any{"context": c}, &out); err != nil {
		return err
	}
	for env, v := range out.Values {
		rule := ""
		if v.RuleID != "" {
			rule = " (rule " + v.RuleID + ")"
		}
		fmt.Fprintf(cl.out, "%-14s %v  [%s%s]\n", env, v.Value, v.Reason, rule)
	}
	return nil
}

func (cl *client) togglesHistory(args []string) error {
	fs := flag.NewFlagSet("toggles history", flag.ContinueOnError)
	env := fs.String("environment", "", "only this Airfield")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway toggles history PROJECT KEY [--environment E]")
	}
	var out struct {
		Changes []struct {
			ID          int64  `json:"id"`
			At          int64  `json:"at"`
			Environment string `json:"environment"`
			Action      string `json:"action"`
			Actor       string `json:"actor"`
			Reason      string `json:"reason"`
			Via         string `json:"via"`
			Status      string `json:"status"`
			Reverts     int64  `json:"reverts"`
			RevertedBy  int64  `json:"reverted_by"`
		} `json:"changes"`
	}
	q := ""
	if *env != "" {
		q = "?environment=" + url.QueryEscape(*env)
	}
	if err := cl.call("GET", tgPath(pos[0], "/toggles/"+url.PathEscape(pos[1])+"/history"+q), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		for _, c := range out.Changes {
			note := ""
			switch {
			case c.Reverts != 0:
				note = fmt.Sprintf(" [reverts #%d]", c.Reverts)
			case c.RevertedBy != 0:
				note = fmt.Sprintf(" [reverted by #%d]", c.RevertedBy)
			}
			fmt.Fprintf(cl.out, "#%d  %s  %-9s %-10s %s by %s via %s%s: %s\n", c.ID, time.UnixMilli(c.At).Format("2006-01-02 15:04"), c.Status, c.Environment, c.Action, c.Actor, c.Via, note, c.Reason)
		}
	})
}

func (cl *client) togglesChange(args []string, what string) error {
	fs := flag.NewFlagSet("toggles "+what, flag.ContinueOnError)
	reason := fs.String("reason", "", "why")
	comment := fs.String("comment", "", "a comment")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway toggles %s PROJECT CHANGE", what)
	}
	var out any
	body := map[string]any{"reason": *reason, "comment": *comment}
	if err := cl.call("POST", tgPath(pos[0], "/toggle-changes/"+url.PathEscape(pos[1])+"/"+what), body, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Done: change %s %s.\n", pos[1], map[string]string{"revert": "reverted", "approve": "approved", "reject": "rejected", "withdraw": "withdrawn"}[what])
	return nil
}

func (cl *client) togglesTimeline(args []string) error {
	fs := flag.NewFlagSet("toggles timeline", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway toggles timeline PROJECT ENV")
	}
	var out any
	if err := cl.call("GET", tgPath(pos[0], "/toggle-timeline?environment="+url.QueryEscape(pos[1])), nil, &out); err != nil {
		return err
	}
	return cl.print(out, true, nil)
}

func (cl *client) togglesRevertSince(args []string) error {
	fs := flag.NewFlagSet("toggles revert-since", flag.ContinueOnError)
	since := fs.Int64("since", 0, "the time, in Unix milliseconds, after which changes are reverted")
	reason := fs.String("reason", "", "why")
	dry := fs.Bool("dry-run", false, "only list them")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || *since <= 0 {
		return usageErr("launchway toggles revert-since PROJECT ENV --since MS --reason R [--dry-run]")
	}
	var out any
	if err := cl.call("POST", tgPath(pos[0], "/toggle-revert-since"), map[string]any{"environment": pos[1], "since": *since, "reason": *reason, "dry_run": *dry}, &out); err != nil {
		return err
	}
	return cl.print(out, true, nil)
}

func (cl *client) togglesKeys(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway toggles keys list|create|rotate|revoke ...")
	}
	fs := flag.NewFlagSet("toggles keys", flag.ContinueOnError)
	kind := fs.String("kind", "server", "server (a secret, reads rules) or client (public, reads values)")
	name := fs.String("name", "", "what the key is for")
	overlap := fs.Int("overlap-hours", 0, "how long the old key keeps working when rotated (default 24)")
	var origins multi
	fs.Var(&origins, "origin", "a website allowed to use a client key; give it once for each")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(pos) != 1 {
			return usageErr("launchway toggles keys list PROJECT")
		}
		var out any
		if err := cl.call("GET", tgPath(pos[0], "/toggle-keys"), nil, &out); err != nil {
			return err
		}
		return cl.print(out, true, nil)
	case "create":
		if len(pos) != 2 {
			return usageErr("launchway toggles keys create PROJECT ENV --kind server|client [--name N]")
		}
		var out struct {
			Key  string `json:"key"`
			Note string `json:"note"`
		}
		if err := cl.call("POST", tgPath(pos[0], "/toggle-keys"), map[string]any{"environment": pos[1], "kind": *kind, "name": *name, "origins": []string(origins)}, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "%s\n\n%s\n", out.Key, out.Note)
		return nil
	case "rotate":
		if len(pos) != 2 {
			return usageErr("launchway toggles keys rotate PROJECT ID")
		}
		var out struct {
			Key  string `json:"key"`
			Note string `json:"note"`
		}
		if err := cl.call("POST", tgPath(pos[0], "/toggle-keys/"+url.PathEscape(pos[1])+"/rotate"), map[string]any{"overlap_hours": *overlap}, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "%s\n\n%s\n", out.Key, out.Note)
		return nil
	case "revoke":
		if len(pos) != 2 {
			return usageErr("launchway toggles keys revoke PROJECT ID")
		}
		if err := cl.call("DELETE", tgPath(pos[0], "/toggle-keys/"+url.PathEscape(pos[1])), nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(cl.out, "Revoked: the key stops working at once.")
		return nil
	}
	return usageErr("unknown command toggles keys %s", args[0])
}

func (cl *client) togglesSafeguards(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway toggles safeguards get|set PROJECT ...")
	}
	fs := flag.NewFlagSet("toggles safeguards", flag.ContinueOnError)
	env := fs.String("environment", "", "only this Airfield (default: all of them)")
	reasonRequired := fs.String("require-reason", "", "true or false")
	approval := fs.String("require-approval", "", "true or false")
	approvals := fs.Int("approvals", 0, "how many people must approve")
	watch := fs.Int("watch-minutes", -1, "how long after a change the Airfield is marked as watched")
	reason := fs.String("reason", "", "why; required when a safeguard is turned down")
	var locks multi
	fs.Var(&locks, "lock", "lock a safeguard so that levels below cannot change it; give it once for each")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway toggles safeguards get|set PROJECT")
	}
	switch args[0] {
	case "get":
		var out any
		q := ""
		if *env != "" {
			q = "?environment=" + url.QueryEscape(*env)
		}
		if err := cl.call("GET", tgPath(pos[0], "/toggle-safeguards"+q), nil, &out); err != nil {
			return err
		}
		return cl.print(out, true, nil)
	case "set":
		body := map[string]any{"environment": *env, "reason": *reason}
		for name, v := range map[string]string{"require_reason": *reasonRequired, "require_approval": *approval} {
			switch v {
			case "":
			case "true", "false":
				body[name] = v == "true"
			default:
				return usageErr("--%s is true or false", strings.ReplaceAll(name, "_", "-"))
			}
		}
		if *approvals > 0 {
			body["approvals"] = *approvals
		}
		if *watch >= 0 {
			body["watch_minutes"] = *watch
		}
		if len(locks) > 0 {
			body["locked"] = []string(locks)
		}
		var out any
		if err := cl.call("PUT", tgPath(pos[0], "/toggle-safeguards"), body, &out); err != nil {
			return err
		}
		return cl.print(out, true, nil)
	}
	return usageErr("unknown command toggles safeguards %s", args[0])
}

func (cl *client) togglesExport(args []string) error {
	fs := flag.NewFlagSet("toggles export", flag.ContinueOnError)
	outFile := fs.String("o", "", "write to this file instead of the screen")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway toggles export PROJECT [-o FILE]")
	}
	var out struct {
		File string `json:"file"`
	}
	if err := cl.call("GET", tgPath(pos[0], "/toggle-file"), nil, &out); err != nil {
		return err
	}
	if *outFile != "" {
		return os.WriteFile(*outFile, []byte(out.File), 0o600) //nolint:gosec // the person named their own file
	}
	fmt.Fprint(cl.out, out.File)
	return nil
}

func (cl *client) togglesFile(args []string, what string) error {
	fs := flag.NewFlagSet("toggles "+what, flag.ContinueOnError)
	file := fs.String("f", "", "the toggles file")
	reason := fs.String("reason", "", "why (apply)")
	origin := fs.String("origin", "", "where the file came from, such as config:<commit> (apply)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *file == "" {
		return usageErr("launchway toggles %s PROJECT -f FILE", what)
	}
	data, err := os.ReadFile(*file) //nolint:gosec // the person named their own file
	if err != nil {
		return usageErr("%v", err)
	}
	if _, err := toggles.ParseFile(data); err != nil {
		return usageErr("%v", err)
	}
	route := "/toggle-plan"
	hdr := map[string]string{}
	if what == "apply" {
		route = "/toggle-apply"
		hdr["X-Launchway-Via"] = "config"
	}
	var out map[string]any
	if err := cl.callWith("POST", tgPath(pos[0], route), map[string]any{"file": string(data), "reason": *reason, "origin": *origin}, &out, hdr); err != nil {
		return err
	}
	if what == "plan" {
		if drift, _ := out["drift"].([]any); len(drift) == 0 {
			fmt.Fprintln(cl.out, "The file and the live toggles agree.")
			return nil
		}
	}
	return cl.print(out, true, nil)
}

func (cl *client) togglesSegments(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway toggles segments list|set|delete PROJECT ...")
	}
	fs := flag.NewFlagSet("toggles segments", flag.ContinueOnError)
	file := fs.String("f", "", "the segment as YAML or JSON: included, excluded, rules")
	reason := fs.String("reason", "", "why")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return usageErr("launchway toggles segments list|set|delete PROJECT [NAME]")
	}
	switch args[0] {
	case "list":
		var out any
		if err := cl.call("GET", tgPath(pos[0], "/toggle-segments"), nil, &out); err != nil {
			return err
		}
		return cl.print(out, true, nil)
	case "set":
		if len(pos) != 2 || *file == "" {
			return usageErr("launchway toggles segments set PROJECT NAME -f FILE [--reason R]")
		}
		data, err := os.ReadFile(*file) //nolint:gosec // the person named their own file
		if err != nil {
			return usageErr("%v", err)
		}
		f, err := toggles.ParseFile([]byte("segments:\n  " + pos[1] + ": " + indentJSON(data)))
		if err != nil {
			return usageErr("%v", err)
		}
		body := map[string]any{"reason": *reason}
		raw, _ := json.Marshal(f.Segments[pos[1]])
		var seg map[string]any
		_ = json.Unmarshal(raw, &seg)
		for k, v := range seg {
			body[k] = v
		}
		var out any
		if err := cl.call("PUT", tgPath(pos[0], "/toggle-segments/"+url.PathEscape(pos[1])), body, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Saved segment %s.\n", pos[1])
		return nil
	case "delete":
		if len(pos) != 2 {
			return usageErr("launchway toggles segments delete PROJECT NAME --reason R")
		}
		if err := cl.call("DELETE", tgPath(pos[0], "/toggle-segments/"+url.PathEscape(pos[1])+"?reason="+url.QueryEscape(*reason)), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Deleted segment %s.\n", pos[1])
		return nil
	}
	return usageErr("unknown command toggles segments %s", args[0])
}

// indentJSON puts a block of YAML under a key: every line indented.
func indentJSON(data []byte) string {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	return "\n" + "    " + strings.Join(lines, "\n    ")
}

func (cl *client) togglesUsage(args []string) error {
	fs := flag.NewFlagSet("toggles usage", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway toggles usage PROJECT KEY")
	}
	var out any
	if err := cl.call("GET", tgPath(pos[0], "/toggles/"+url.PathEscape(pos[1])+"/usage"), nil, &out); err != nil {
		return err
	}
	return cl.print(out, true, nil)
}

func (cl *client) togglesSchedules(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway toggles schedules list PROJECT | cancel PROJECT ID")
	}
	fs := flag.NewFlagSet("toggles schedules", flag.ContinueOnError)
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	switch {
	case args[0] == "list" && len(pos) == 1:
		var out any
		if err := cl.call("GET", tgPath(pos[0], "/toggle-schedules?all=true"), nil, &out); err != nil {
			return err
		}
		return cl.print(out, true, nil)
	case args[0] == "cancel" && len(pos) == 2:
		if err := cl.call("DELETE", tgPath(pos[0], "/toggle-schedules/"+url.PathEscape(pos[1])), nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(cl.out, "Canceled: it will not run.")
		return nil
	}
	return usageErr("launchway toggles schedules list PROJECT | cancel PROJECT ID")
}

func (cl *client) togglesLock(args []string, what string) error {
	fs := flag.NewFlagSet("toggles "+what, flag.ContinueOnError)
	reason := fs.String("reason", "", "why (lock)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway toggles %s PROJECT KEY [--reason R]", what)
	}
	method, body := "DELETE", any(nil)
	if what == "lock" {
		method, body = "PUT", map[string]any{"reason": *reason}
	}
	if err := cl.call(method, tgPath(pos[0], "/toggles/"+url.PathEscape(pos[1])+"/lock"), body, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%sed %s.\n", strings.TrimSuffix(what, "k")+"k", pos[1])
	return nil
}
