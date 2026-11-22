package main

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
)

const reposHostUsage = `Keeping code on another Git host (administrators; see the guide "Keep your code on another host"):
  launchway repos host list                                connections to outside Git hosts
  launchway repos host add NAME --kind KIND                connect a host of kind github, gitlab, gitea or forgejo (the public service, or a self-managed
                         [--base-url URL] [--owner O]...   install with --base-url); --owner limits which owners or
                         [--credential-env NAME | --credential-file PATH]   groups Flights may use
                         [--webhook-secret-env NAME | --webhook-secret-file PATH]
  launchway repos host remove NAME                         forget a connection no Flight uses
  launchway repos host test NAME                           who the credential is, token expiry, which permissions it has and lacks
  launchway repos host repositories NAME [--query TEXT]    the repositories the credential reaches
  launchway repos outside show FLIGHT                     the outside repository of a Flight, and what its host cannot do
  launchway repos outside attach FLIGHT [--connection NAME --repository OWNER/NAME]
                         [--statuses=false] [--comment=false]   attach a Flight, and switch its reports
  launchway repos outside detach FLIGHT                   stop using the connection for a Flight
`

type outsideHostView struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	BaseURL       string   `json:"base_url"`
	Credential    string   `json:"credential"`
	WebhookSecret string   `json:"webhook_secret"`
	Owners        []string `json:"owners"`
}

func (cl *client) reposHost(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway repos host list|add|remove|test|repositories")
	}
	switch args[0] {
	case "list":
		var page struct {
			Items []outsideHostView `json:"items"`
		}
		if err := cl.call("GET", "/api/v1/outside-hosts", nil, &page); err != nil {
			return err
		}
		if len(page.Items) == 0 {
			fmt.Fprintln(cl.out, "No connections to outside Git hosts.")
		}
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, c := range page.Items {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.Kind, c.BaseURL, c.Credential, strings.Join(c.Owners, ","))
		}
		return tw.Flush()
	case "remove":
		if len(args) != 2 {
			return usageErr("launchway repos host remove NAME")
		}
		if err := cl.call("DELETE", "/api/v1/outside-hosts/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Removed %s.\n", args[1])
		return nil
	case "add":
		return cl.reposHostAdd(args[1:])
	case "test":
		if len(args) != 2 {
			return usageErr("launchway repos host test NAME")
		}
		var out struct {
			Report struct {
				Account      string  `json:"account"`
				TokenExpires *string `json:"token_expires"`
				Checks       []struct {
					Feature, Needs, Detail string
					OK                     bool
				} `json:"checks"`
			} `json:"report"`
		}
		if err := cl.call("POST", "/api/v1/outside-hosts/"+url.PathEscape(args[1])+"/test", map[string]any{}, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Signed in as %s.\n", out.Report.Account)
		if out.Report.TokenExpires != nil {
			fmt.Fprintf(cl.out, "The token expires %s.\n", *out.Report.TokenExpires)
		} else {
			fmt.Fprintln(cl.out, "The host does not say when the token expires.")
		}
		for _, c := range out.Report.Checks {
			if c.OK {
				fmt.Fprintf(cl.out, "  has   %s\n", c.Feature)
			} else {
				fmt.Fprintf(cl.out, "  lacks %s: grant %s. %s\n", c.Feature, c.Needs, c.Detail)
			}
		}
		return nil
	case "repositories":
		fs := flag.NewFlagSet("repos host repositories", flag.ContinueOnError)
		q := fs.String("query", "", "only repositories whose path contains this text")
		pos, err := parseArgs(fs, args[1:])
		if err != nil || len(pos) != 1 {
			return usageErr("launchway repos host repositories NAME [--query TEXT]")
		}
		var page struct {
			Items []struct{ Path, DefaultBranch string } `json:"items"`
			Next  string                                 `json:"next"`
		}
		if err := cl.call("GET", "/api/v1/outside-hosts/"+url.PathEscape(pos[0])+"/repositories?q="+url.QueryEscape(*q), nil, &page); err != nil {
			return err
		}
		for _, r := range page.Items {
			fmt.Fprintln(cl.out, r.Path)
		}
		if page.Next != "" {
			fmt.Fprintf(cl.out, "More repositories follow (cursor %s).\n", page.Next)
		}
		return nil
	}
	return usageErr("unknown command repos host %s", args[0])
}

func (cl *client) reposHostAdd(args []string) error {
	fs := flag.NewFlagSet("repos host add", flag.ContinueOnError)
	kind := fs.String("kind", "", "github, gitlab, gitea or forgejo")
	base := fs.String("base-url", "", "a self-managed install's address; empty is the public service")
	cenv := fs.String("credential-env", "", "the NAME of an environment variable of the server that holds the token")
	cfile := fs.String("credential-file", "", "the path of a file on the server that holds the token")
	wenv := fs.String("webhook-secret-env", "", "the NAME of an environment variable that holds the webhook secret")
	wfile := fs.String("webhook-secret-file", "", "the path of a file that holds the webhook secret")
	var owners stringList
	fs.Var(&owners, "owner", "an owner or group Flights may use (repeat)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *kind == "" || (*cenv != "" && *cfile != "") || (*wenv != "" && *wfile != "") {
		return usageErr("launchway repos host add NAME --kind KIND [--base-url URL] [--owner O] [--credential-env NAME | --credential-file PATH] [--webhook-secret-env NAME | --webhook-secret-file PATH]")
	}
	body := map[string]any{"name": pos[0], "kind": *kind, "base_url": *base, "owners": []string(owners)}
	if *cenv != "" {
		body["credential"] = map[string]string{"env": *cenv}
	} else if *cfile != "" {
		body["credential"] = map[string]string{"file": *cfile}
	}
	if *wenv != "" {
		body["webhook_secret"] = map[string]string{"env": *wenv}
	} else if *wfile != "" {
		body["webhook_secret"] = map[string]string{"file": *wfile}
	}
	var out outsideHostView
	if err := cl.call("POST", "/api/v1/outside-hosts", body, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Connection %s added (%s %s). Run: launchway repos host test %s\n", out.Name, out.Kind, out.BaseURL, out.Name)
	return nil
}

func (cl *client) reposOutside(args []string) error {
	if len(args) < 2 {
		return usageErr("launchway repos outside show|attach|detach FLIGHT")
	}
	path := "/api/v1/projects/" + url.PathEscape(args[1]) + "/outside"
	switch args[0] {
	case "show":
		var v struct {
			Connection, Repository string
			ReportStatuses         bool                         `json:"report_statuses"`
			ReportComment          bool                         `json:"report_comment"`
			Unsupported            map[string]map[string]string `json:"unsupported"`
		}
		if err := cl.call("GET", path, nil, &v); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "%s on %s. Commit statuses %v, pull request summary %v.\n", v.Repository, v.Connection, v.ReportStatuses, v.ReportComment)
		for k, d := range v.Unsupported {
			fmt.Fprintf(cl.out, "  this host cannot do %s: %s. %s\n", k, d["reason"], d["workaround"])
		}
		return nil
	case "detach":
		if err := cl.call("DELETE", path, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "%s no longer uses an outside connection.\n", args[1])
		return nil
	case "attach":
		fs := flag.NewFlagSet("repos outside attach", flag.ContinueOnError)
		conn := fs.String("connection", "", "the connection")
		repo := fs.String("repository", "", "OWNER/NAME on the host")
		st := fs.Bool("statuses", true, "write a commit status for each Sortie")
		cm := fs.Bool("comment", true, "write one summary comment on pull requests")
		if _, err := parseArgs(fs, args[2:]); err != nil {
			return err
		}
		body := map[string]any{"connection": *conn, "repository": *repo, "report_statuses": *st, "report_comment": *cm}
		if err := cl.call("PUT", path, body, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "%s is attached.\n", args[1])
		return nil
	}
	return usageErr("unknown command repos outside %s", args[0])
}
