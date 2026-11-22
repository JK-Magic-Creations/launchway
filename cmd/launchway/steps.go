package main

// launchway steps list|get: the custom steps of the server's libraries, with
// what each takes, gives and may do, and a step's versions with what changed.
// A client of GET /api/v1/libraries/{library}/steps[/{step}] (see also
// `launchway step`, which works on files).

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const stepsUsage = `Usage:
  launchway steps list LIBRARY [--version V] [--json]    a library's custom steps: inputs, outputs and what each may do
  launchway steps get LIBRARY STEP [--version V] [--json]   one step's page: manifest, versions with what changed, who uses it, the update on offer
`

func cmdSteps(args []string) error {
	return runClient(func() error {
		if len(args) == 0 || (args[0] != "list" && args[0] != "get") {
			fmt.Print(stepsUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).steps(args[0], args[1:])
	})
}

func (cl *client) steps(verb string, args []string) error {
	fs := flag.NewFlagSet("steps "+verb, flag.ContinueOnError)
	version := fs.String("version", "", "a tag, branch or commit; default the newest tag")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	want := 1
	if verb == "get" {
		want = 2
	}
	if len(pos) != want {
		return usageErr("%s", stepsUsage)
	}
	path := "/api/v1/libraries/" + url.PathEscape(pos[0]) + "/steps"
	if verb == "get" {
		path += "/" + url.PathEscape(pos[1])
	}
	if *version != "" {
		path += "?version=" + url.QueryEscape(*version)
	}
	var raw json.RawMessage
	if err := cl.call("GET", path, nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		var v any
		_ = json.Unmarshal(raw, &v)
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(cl.out, string(b))
		return nil
	}
	if verb == "list" {
		var got struct {
			Library, Version string
			Steps            []struct {
				Name, Description, Sentence string
				Inputs                      []struct {
					Name, Type string
					Required   bool
				}
				Outputs []struct{ Name, Type string }
			}
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			return apiErr("the server's answer is not what this command expects: %v", err)
		}
		fmt.Fprintf(cl.out, "%s at %s\n", got.Library, got.Version)
		for _, s := range got.Steps {
			fmt.Fprintf(cl.out, "\n%s", s.Name)
			if s.Description != "" {
				fmt.Fprintf(cl.out, ": %s", s.Description)
			}
			fmt.Fprintf(cl.out, "\n  %s\n", s.Sentence)
			var in, out []string
			for _, i := range s.Inputs {
				r := ""
				if i.Required {
					r = " required"
				}
				in = append(in, i.Name+" ("+i.Type+")"+r)
			}
			for _, o := range s.Outputs {
				out = append(out, o.Name+" ("+o.Type+")")
			}
			fmt.Fprintf(cl.out, "  takes: %s\n  gives: %s\n", orNone(in), orNone(out))
		}
		return nil
	}
	var page struct {
		Library, Version, Commit string
		Step                     struct{ Name, Description, Sentence, Example string }
		Versions                 []struct {
			Version string
			Widens  bool
			Changes []struct{ What string }
		}
		Newest        string
		NewestWidens  bool                    `json:"newest_widens"`
		NewestChanges []struct{ What string } `json:"newest_changes"`
		UsedBy        []struct {
			Project, Commit string
			Runs            int
			Newest          string
			Widens          bool
		} `json:"used_by"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return apiErr("the server's answer is not what this command expects: %v", err)
	}
	fmt.Fprintf(cl.out, "%s/%s at %s (%s)\n%s\n\n%s\n", page.Library, page.Step.Name, page.Version, page.Commit[:min(8, len(page.Commit))], page.Step.Description, page.Step.Sentence)
	fmt.Fprintf(cl.out, "\nExample:\n%s", indentText(page.Step.Example))
	for _, v := range page.Versions {
		fmt.Fprintf(cl.out, "\n%s", v.Version)
		if v.Widens {
			fmt.Fprint(cl.out, "  (asks for more than the version before)")
		}
		fmt.Fprintln(cl.out)
		for _, c := range v.Changes {
			fmt.Fprintln(cl.out, "  "+c.What)
		}
	}
	if page.Newest != "" {
		fmt.Fprintf(cl.out, "\nNewer: %s", page.Newest)
		if page.NewestWidens {
			fmt.Fprint(cl.out, "  (asks for more permissions)")
		}
		fmt.Fprintln(cl.out)
		for _, c := range page.NewestChanges {
			fmt.Fprintln(cl.out, "  "+c.What)
		}
	}
	if len(page.UsedBy) > 0 {
		fmt.Fprintln(cl.out, "\nUsed by:")
		for _, u := range page.UsedBy {
			fmt.Fprintf(cl.out, "  %s at %s (%d runs)", u.Project, u.Commit[:min(8, len(u.Commit))], u.Runs)
			if u.Newest != "" {
				fmt.Fprintf(cl.out, ", %s is newer", u.Newest)
				if u.Widens {
					fmt.Fprint(cl.out, " and asks for more permissions")
				}
			}
			fmt.Fprintln(cl.out)
		}
	}
	return nil
}

func orNone(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}

func indentText(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ") + "\n"
}
