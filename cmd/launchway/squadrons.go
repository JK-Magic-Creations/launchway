package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
)

// The friendly commands for Squadrons (stored as units) and Flights (stored as projects). They are aliases over the
// same HTTP routes the web pages use; `launchway config ... units` and `--unit`/`--project` keep working as before.
const squadronsUsage = `Usage:
  launchway squadrons list [--json]                       the Squadrons, and the Flights that sit in none (Unfiled)
  launchway squadrons create NAME [--display-name TEXT] [--description TEXT]   make a Squadron with a name
  launchway squadrons show NAME [--json]                  its Flights and what needs attention, its people, shared settings and Operations
  launchway squadrons candidates [--query TEXT] [--connection NAME]   repositories a Flight could be made from (hosted ones with no Flight first)
  launchway squadrons add-flight NAME (--repository PATH | --repo ADDRESS) [--name FLIGHT] [--branch B]   make a Flight in the Squadron
  launchway squadrons move FLIGHT... (--to SQUADRON | --unfile) [--preview] [--comment TEXT]   move Flights; --preview changes nothing
  launchway squadrons organize [--apply] [--group NAME=FLIGHT,FLIGHT ...] [--undo FILE]   propose Squadrons for Flights in none; --apply files them

A Squadron's permanent name is unit and a Flight's is project: the API paths (/api/v1/units, /api/v1/projects),
the config kinds, --unit and --project flags and file keys keep those names. "units" works as an alias of "squadrons".
A move whose secret references would resolve to a different store, or to none, is blocked and says why; the others go
in one audited change, and the answer holds what undoes it (--json prints it; save it and use organize --undo FILE).
`

func cmdSquadrons(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, squadronsUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.squadronsList(args[1:])
		case "create":
			return cl.squadronsCreate(args[1:])
		case "show":
			return cl.squadronsShow(args[1:])
		case "candidates":
			return cl.squadronsCandidates(args[1:])
		case "add-flight":
			return cl.squadronsAddFlight(args[1:])
		case "move":
			return cl.squadronsMove(args[1:])
		case "organize":
			return cl.squadronsOrganize(args[1:])
		}
		return usageErr("unknown command squadrons %s\n\n%s", args[0], squadronsUsage)
	})
}

type sqFlight struct {
	Name    string `json:"name"`
	Last    string `json:"last"`
	Running int    `json:"running"`
	Needs   int    `json:"needs"`
}

func (cl *client) squadronsList(args []string) error {
	fs := flag.NewFlagSet("squadrons list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var v struct {
		Squadrons []struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Flights     int    `json:"flights"`
			Needs       int    `json:"needs"`
			Failing     int    `json:"failing"`
			Running     int    `json:"running"`
		} `json:"squadrons"`
		Unfiled []sqFlight `json:"unfiled"`
	}
	if err := cl.call("GET", "/api/v1/units", nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SQUADRON\tFLIGHTS\tNEED YOU\tFAILING\tRUNNING")
		for _, s := range v.Squadrons {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", s.Name, s.Flights, s.Needs, s.Failing, s.Running)
		}
		tw.Flush()
		if len(v.Unfiled) > 0 {
			names := make([]string, 0, len(v.Unfiled))
			for _, f := range v.Unfiled {
				names = append(names, f.Name)
			}
			fmt.Fprintf(cl.out, "\nUnfiled (%d): %s\nRun `launchway squadrons organize` for a proposal.\n", len(names), strings.Join(names, ", "))
		}
	})
}

func (cl *client) squadronsCreate(args []string) error {
	fs := flag.NewFlagSet("squadrons create", flag.ContinueOnError)
	display := fs.String("display-name", "", "how the name is shown")
	desc := fs.String("description", "", "what the Squadron is for")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("give the Squadron's name: launchway squadrons create NAME")
	}
	var out map[string]any
	if err := cl.call("POST", "/api/v1/units", map[string]string{"name": pos[0], "display_name": *display, "description": *desc}, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Made the Squadron %s. Add a Flight: launchway squadrons add-flight %s --repository OWNER/NAME\n", pos[0], pos[0])
	return nil
}

func (cl *client) squadronsShow(args []string) error {
	fs := flag.NewFlagSet("squadrons show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("give the Squadron's name: launchway squadrons show NAME")
	}
	var v struct {
		Name    string     `json:"name"`
		Flights []sqFlight `json:"flights"`
		Needs   []struct {
			Project string `json:"project"`
			Title   string `json:"title"`
		} `json:"needs"`
		People []struct {
			Who  string `json:"who"`
			Role string `json:"role"`
		} `json:"people"`
		Settings []struct {
			Title string `json:"title"`
			Count int    `json:"count"`
		} `json:"settings"`
		Operations []struct {
			Flight   string `json:"flight"`
			Pipeline string `json:"pipeline"`
			Last     string `json:"last"`
		} `json:"operations"`
	}
	if err := cl.call("GET", "/api/v1/units/"+url.PathEscape(pos[0]), nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		if len(v.Flights) == 0 {
			fmt.Fprintf(cl.out, "%s holds no Flights yet. A Squadron is a named folder of Flights with its own people and shared settings. Add one: launchway squadrons add-flight %s --repository OWNER/NAME\n", v.Name, v.Name)
		}
		for _, f := range v.Flights {
			fmt.Fprintf(cl.out, "%-28s latest %-10s needs you %d  running %d\n", f.Name, nzCLI(f.Last, "none yet"), f.Needs, f.Running)
		}
		for _, n := range v.Needs {
			fmt.Fprintf(cl.out, "needs you: %s: %s\n", n.Project, n.Title)
		}
		for _, p := range v.People {
			fmt.Fprintf(cl.out, "person: %s is %s\n", p.Who, p.Role)
		}
		for _, s := range v.Settings {
			fmt.Fprintf(cl.out, "shared: %d %s\n", s.Count, s.Title)
		}
		for _, o := range v.Operations {
			fmt.Fprintf(cl.out, "operation: %s (%s), started by hand; latest %s\n", o.Pipeline, o.Flight, nzCLI(o.Last, "none yet"))
		}
	})
}

func nzCLI(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (cl *client) squadronsCandidates(args []string) error {
	fs := flag.NewFlagSet("squadrons candidates", flag.ContinueOnError)
	q := fs.String("query", "", "part of a repository's path")
	conn := fs.String("connection", "", "also list what this connection can see")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var v struct {
		Hosted []struct {
			Path   string `json:"path"`
			Flight string `json:"flight"`
		} `json:"hosted"`
		Connections []string `json:"connections"`
		Items       []struct {
			Path   string `json:"path"`
			Flight string `json:"flight"`
		} `json:"connection_items"`
		Error string `json:"connection_error"`
	}
	path := "/api/v1/project-candidates?q=" + url.QueryEscape(*q)
	if *conn != "" {
		path += "&connection=" + url.QueryEscape(*conn)
	}
	if err := cl.call("GET", path, nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		for _, h := range v.Hosted {
			fmt.Fprintf(cl.out, "hosted  %-40s %s\n", h.Path, nzCLI("", map[bool]string{true: "has a Flight: " + h.Flight, false: "no Flight yet"}[h.Flight != ""]))
		}
		for _, h := range v.Items {
			fmt.Fprintf(cl.out, "%-7s %-40s %s\n", *conn, h.Path, map[bool]string{true: "has a Flight: " + h.Flight, false: "no Flight yet"}[h.Flight != ""])
		}
		if v.Error != "" {
			fmt.Fprintln(cl.out, v.Error)
		}
		if *conn == "" && len(v.Connections) > 0 {
			fmt.Fprintf(cl.out, "connections you can also search: %s (use --connection NAME)\n", strings.Join(v.Connections, ", "))
		}
	})
}

func (cl *client) squadronsAddFlight(args []string) error {
	fs := flag.NewFlagSet("squadrons add-flight", flag.ContinueOnError)
	repository := fs.String("repository", "", "a hosted repository, OWNER/NAME")
	repo := fs.String("repo", "", "the address of a repository")
	name := fs.String("name", "", "the Flight's name (default: the repository's)")
	branch := fs.String("branch", "", "the default branch")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*repository == "" && *repo == "") {
		return usageErr("say the Squadron and the repository: launchway squadrons add-flight NAME --repository OWNER/NAME")
	}
	var out struct {
		Name     string `json:"name"`
		Pipeline struct {
			State string `json:"state"`
			Note  string `json:"note"`
		} `json:"pipeline"`
	}
	body := map[string]string{"repository": *repository, "repo": *repo, "name": *name, "branch": *branch}
	if err := cl.call("POST", "/api/v1/units/"+url.PathEscape(pos[0])+"/projects", body, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Made the Flight %s in %s. %s\n", out.Name, pos[0], out.Pipeline.Note)
	return nil
}

type sqPlan struct {
	Flight  string `json:"flight"`
	From    string `json:"from"`
	Blocked bool   `json:"blocked"`
	Reasons []string
	Stops   []struct{ Kind, Name string }
	Starts  []struct{ Kind, Name string }
	Access  []struct{ Who, Effect, Before, After string }
	Vars    []struct{ Name, Before, After string } `json:"variables"`
}

func (cl *client) squadronsMove(args []string) error {
	fs := flag.NewFlagSet("squadrons move", flag.ContinueOnError)
	to := fs.String("to", "", "the Squadron to move to")
	unfile := fs.Bool("unfile", false, "take them out of any Squadron")
	preview := fs.Bool("preview", false, "show what would change, and change nothing")
	comment := fs.String("comment", "", "why")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 || (*to == "") == !*unfile {
		return usageErr("say the Flights and where they go: launchway squadrons move FLIGHT... --to SQUADRON (or --unfile)")
	}
	body := map[string]any{"flights": pos, "to": *to, "comment": *comment}
	if *preview {
		var out struct {
			Plans []sqPlan `json:"plans"`
		}
		if err := cl.call("POST", "/api/v1/unit-moves/preview", body, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			for _, p := range out.Plans {
				fmt.Fprintf(cl.out, "%s: from %q\n", p.Flight, p.From)
				for _, s := range p.Stops {
					fmt.Fprintf(cl.out, "  stops applying: %s %s\n", s.Kind, s.Name)
				}
				for _, s := range p.Starts {
					fmt.Fprintf(cl.out, "  starts applying: %s %s\n", s.Kind, s.Name)
				}
				for _, v := range p.Vars {
					fmt.Fprintf(cl.out, "  variable %s: %s becomes %s\n", v.Name, v.Before, v.After)
				}
				for _, a := range p.Access {
					fmt.Fprintf(cl.out, "  %s %s the ability to start runs (%s becomes %s)\n", a.Who, a.Effect, a.Before, a.After)
				}
				for _, r := range p.Reasons {
					fmt.Fprintf(cl.out, "  BLOCKED: %s\n", r)
				}
			}
		})
	}
	var res struct {
		Moved   []string          `json:"moved"`
		Blocked []sqPlan          `json:"blocked"`
		Undo    map[string]string `json:"undo"`
	}
	if err := cl.call("POST", "/api/v1/unit-moves", body, &res); err != nil {
		return err
	}
	return cl.print(res, *asJSON, func() {
		fmt.Fprintf(cl.out, "Moved %d Flight(s).\n", len(res.Moved))
		for _, b := range res.Blocked {
			fmt.Fprintf(cl.out, "Not moved: %s: %s\n", b.Flight, strings.Join(b.Reasons, " "))
		}
		if len(res.Moved) > 0 {
			fmt.Fprintln(cl.out, "To undo, run the same command with --json and send the undo answer, or move them back to where they were.")
		}
	})
}

func (cl *client) squadronsOrganize(args []string) error {
	fs := flag.NewFlagSet("squadrons organize", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "file the Flights as proposed (or as --group says)")
	undo := fs.String("undo", "", "undo an organize from the --json answer saved in this file")
	asJSON := fs.Bool("json", false, "print JSON")
	var groups stringList
	fs.Var(&groups, "group", "NAME=FLIGHT,FLIGHT: use this instead of the proposal (repeat)")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *undo != "" {
		raw, err := os.ReadFile(*undo)
		if err != nil {
			return err
		}
		var saved struct {
			Undo       map[string]string `json:"undo"`
			UndoDelete []string          `json:"undo_delete"`
		}
		if err := json.Unmarshal(raw, &saved); err != nil {
			return usageErr("%s is not the --json answer of an organize: %v", *undo, err)
		}
		var res map[string]any
		if err := cl.call("POST", "/api/v1/unit-organize/undo", map[string]any{"homes": saved.Undo, "delete_units": saved.UndoDelete}, &res); err != nil {
			return err
		}
		fmt.Fprintln(cl.out, "Undone: the Flights are back where they were.")
		return nil
	}
	var prop struct {
		Groups []struct {
			Name    string   `json:"name"`
			Flights []string `json:"flights"`
			Source  string   `json:"source"`
		} `json:"groups"`
		Leftover []string `json:"leftover"`
	}
	if err := cl.call("GET", "/api/v1/unit-organize", nil, &prop); err != nil {
		return err
	}
	type grp struct {
		Name    string   `json:"name"`
		Flights []string `json:"flights"`
	}
	var use []grp
	if len(groups) > 0 {
		for _, g := range groups {
			n, list, ok := strings.Cut(g, "=")
			if !ok || n == "" {
				return usageErr("--group is NAME=FLIGHT,FLIGHT, not %q", g)
			}
			use = append(use, grp{Name: n, Flights: strings.Split(list, ",")})
		}
	} else {
		for _, g := range prop.Groups {
			use = append(use, grp{Name: g.Name, Flights: g.Flights})
		}
	}
	if !*apply {
		return cl.print(prop, *asJSON, func() {
			if len(prop.Groups) == 0 {
				fmt.Fprintln(cl.out, "Nothing to propose: no Flights share a name prefix or a folder.")
			}
			for _, g := range prop.Groups {
				fmt.Fprintf(cl.out, "%s (from a %s): %s\n", g.Name, g.Source, strings.Join(g.Flights, ", "))
			}
			if len(prop.Leftover) > 0 {
				fmt.Fprintf(cl.out, "Stay unfiled: %s\n", strings.Join(prop.Leftover, ", "))
			}
			fmt.Fprintln(cl.out, "Apply it with: launchway squadrons organize --apply")
		})
	}
	var res struct {
		Created []string          `json:"created"`
		Moved   []string          `json:"moved"`
		Undo    map[string]string `json:"undo"`
	}
	if err := cl.call("POST", "/api/v1/unit-organize", map[string]any{"groups": use}, &res); err != nil {
		return err
	}
	return cl.print(res, *asJSON, func() {
		fmt.Fprintf(cl.out, "Made %d Squadron(s) and filed %d Flight(s). Run with --json and keep the answer to undo it: organize --undo FILE.\n", len(res.Created), len(res.Moved))
	})
}
