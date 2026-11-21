package main

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"
)

type connectionView struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	BaseURL    string `json:"base_url"`
	Credential string `json:"credential"`
	Enabled    bool   `json:"enabled"`
}

func (cl *client) reposConnection(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway repos connection add|list|remove")
	}
	switch args[0] {
	case "list":
		var page struct {
			Items []connectionView `json:"items"`
		}
		if err := cl.call("GET", "/api/v1/connections", nil, &page); err != nil {
			return err
		}
		if len(page.Items) == 0 {
			fmt.Fprintln(cl.out, "No connections.")
		}
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, c := range page.Items {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Name, c.Kind, c.BaseURL, c.Credential)
		}
		return tw.Flush()
	case "remove":
		if len(args) != 2 {
			return usageErr("launchway repos connection remove NAME")
		}
		if err := cl.call("DELETE", "/api/v1/connections/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Removed %s.\n", args[1])
		return nil
	case "add":
		fs := flag.NewFlagSet("repos connection add", flag.ContinueOnError)
		kind := fs.String("kind", "", "gitea, forgejo or plain")
		base := fs.String("base-url", "", "the server's address")
		cenv := fs.String("credential-env", "", "the NAME of an environment variable of the server that holds the token")
		cfile := fs.String("credential-file", "", "the path of a file on the server that holds the token")
		pos, err := parseArgs(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || *kind == "" || (*cenv != "" && *cfile != "") {
			return usageErr("launchway repos connection add NAME --kind gitea|forgejo|plain [--base-url URL] [--credential-env NAME | --credential-file PATH]")
		}
		body := map[string]any{"name": pos[0], "kind": *kind, "base_url": *base}
		if *cenv != "" {
			body["credential"] = map[string]string{"env": *cenv}
		} else if *cfile != "" {
			body["credential"] = map[string]string{"file": *cfile}
		}
		var out connectionView
		if err := cl.call("POST", "/api/v1/connections", body, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Connection %s added (%s %s).\n", out.Name, out.Kind, out.BaseURL)
		return nil
	}
	return usageErr("unknown command repos connection %s", args[0])
}

type importItem struct {
	Source      string   `json:"source"`
	Repository  string   `json:"repository"`
	Visibility  string   `json:"visibility"`
	Action      string   `json:"action"`
	State       string   `json:"state"`
	Reason      string   `json:"reason"`
	Error       string   `json:"error"`
	SourceRefs  int      `json:"source_refs"`
	HostedRefs  int      `json:"hosted_refs"`
	Verified    bool     `json:"verified"`
	Differences []string `json:"differences"`
}

type importResult struct {
	DryRun  bool           `json:"dry_run"`
	Items   []importItem   `json:"items"`
	Summary map[string]int `json:"summary"`
}

// sourceFlags are the flags that say where repositories come from.
type sourceFlags struct {
	conn, owner, repo, target, user *string
	urls                            *multi
}

func addSourceFlags(fs *flag.FlagSet, connFlag string) *sourceFlags {
	f := &sourceFlags{urls: &multi{}}
	f.conn = fs.String(connFlag, "", "the connection to read from (launchway repos connection add)")
	f.owner = fs.String("owner", "", "only this user or organization of the source")
	f.repo = fs.String("repo", "", "only this repository of the source, OWNER/NAME")
	f.target = fs.String("target-owner", "", "make them under this owner here")
	f.user = fs.String("username", "", "the user name that goes with the connection's credential")
	fs.Var(f.urls, "url", "a repository address to import (repeat); for a plain connection, or none")
	return f
}

func (f *sourceFlags) body() map[string]any {
	b := map[string]any{}
	set := func(k string, v string) {
		if v != "" {
			b[k] = v
		}
	}
	set("connection", *f.conn)
	set("owner", *f.owner)
	set("repository", *f.repo)
	set("target_owner", *f.target)
	set("username", *f.user)
	if len(*f.urls) > 0 {
		b["urls"] = []string(*f.urls)
	}
	return b
}

func (cl *client) reposImport(args []string) error {
	fs := flag.NewFlagSet("repos import", flag.ContinueOnError)
	src := addSourceFlags(fs, "connection")
	dry := fs.Bool("dry-run", false, "list what would be created and change nothing")
	once := fs.Bool("once", false, "make ordinary repositories once copied and checked, not mirrors")
	every := fs.Duration("interval", 0, "the mirrors' schedule (default 1h)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || (*src.conn == "" && len(*src.urls) == 0) {
		return usageErr("launchway repos import --connection NAME [--owner O] [--repo O/N] [--dry-run] [--once]")
	}
	extra := func(b map[string]any) map[string]any {
		if *once {
			b["mirror"] = false
		}
		if *every != 0 {
			b["interval_seconds"] = int64(every.Seconds())
		}
		return b
	}
	// First, what there is: a dry run lists it and shows what is refused and why.
	listing := extra(src.body())
	listing["dry_run"] = true
	var plan importResult
	if err := cl.call("POST", "/api/v1/imports", listing, &plan); err != nil {
		return err
	}
	if *dry {
		return cl.print(plan, *asJSON, func() { cl.printImport(plan) })
	}
	// Then one repository at a time, each copied and checked before the next:
	// a failure stops nothing, and running the command again resumes.
	final := importResult{}
	failed := 0
	for _, it := range plan.Items {
		if it.Action == "skip" {
			final.Items = append(final.Items, it)
			if !*asJSON {
				fmt.Fprintf(cl.out, "skipped   %s: %s\n", it.Repository, it.Reason)
			}
			continue
		}
		b := extra(src.body())
		b["repository"], b["wait"] = it.Source, true
		started := time.Now()
		var one importResult
		if err := cl.call("POST", "/api/v1/imports", b, &one); err != nil {
			one = importResult{Items: []importItem{{Source: it.Source, Repository: it.Repository, State: "failed", Error: err.Error()}}}
		}
		for _, r := range one.Items {
			final.Items = append(final.Items, r)
			if r.State != "verified" {
				failed++
			}
			if !*asJSON {
				cl.importLine(r, time.Since(started))
			}
		}
	}
	final.Summary = map[string]int{"total": len(final.Items)}
	for _, r := range final.Items {
		final.Summary[r.State]++
	}
	if *asJSON {
		_ = cl.print(final, true, nil)
	} else {
		fmt.Fprintf(cl.out, "\n%d repositories: %d verified, %d skipped, %d failed.\n", len(final.Items), final.Summary["verified"], final.Summary["skipped"], failed)
	}
	if failed > 0 {
		return apiErr("%d repositories did not finish; run the same command again to resume them", failed)
	}
	return nil
}

func (cl *client) importLine(r importItem, took time.Duration) {
	switch r.State {
	case "verified":
		fmt.Fprintf(cl.out, "verified  %s: %d refs equal (%s)\n", r.Repository, r.HostedRefs, took.Round(time.Second))
	default:
		fmt.Fprintf(cl.out, "%-9s %s: %s\n", r.State, r.Repository, strings.TrimSpace(r.Error+" "+r.Reason))
		for _, d := range r.Differences {
			fmt.Fprintf(cl.out, "          %s\n", d)
		}
	}
}

func (cl *client) printImport(res importResult) {
	if len(res.Items) == 0 {
		fmt.Fprintln(cl.out, "Nothing to import.")
		return
	}
	tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tHERE\tVISIBILITY\tACTION\tNOTE")
	for _, it := range res.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.Source, it.Repository, it.Visibility, it.Action, it.Reason)
	}
	_ = tw.Flush()
	fmt.Fprintf(cl.out, "\n%d repositories. Nothing was changed; run without --dry-run to import them.\n", len(res.Items))
}
