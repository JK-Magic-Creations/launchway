package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

const reposUsage = `Usage:
  launchway repos mirror status [OWNER/NAME] [--behind] [--json]
                                    the pull and push mirrors, their last sync, last error and lag
  launchway repos mirror add OWNER/NAME --url URL [--direction pull|push] [--username U]
                     [--credential-env NAME | --credential-file PATH] [--interval 1h] [--no-prune]
                                    keep OWNER/NAME an exact copy of URL (pull, the default), or copy
                                    every accepted update of it to URL (push)
  launchway repos mirror sync OWNER/NAME [--id mir_...]     run the pull mirror (or the one given) now
  launchway repos mirror pause|resume OWNER/NAME --id mir_...
  launchway repos mirror remove OWNER/NAME --id mir_...     remove a push mirror
  launchway repos mirror stop OWNER/NAME [--final-sync]
                                    stop mirroring and make OWNER/NAME the primary: it takes pushes

A credential is the name of an environment variable of the SERVER or the path of a file on the server
that holds the token; the token itself is never given here, stored in a repository's record or shown.
An internal address is refused unless the server's forge.mirror_allow lists it. Changes need the admin
role on the repository (sync: operator) and are recorded in the audit trail.
`

// mirrorView is a mirror as the API shows it.
type mirrorView struct {
	ID              string `json:"id"`
	Repository      string `json:"repository"`
	Direction       string `json:"direction"`
	URL             string `json:"url"`
	Credential      string `json:"credential"`
	IntervalSeconds int64  `json:"interval_seconds"`
	Enabled         bool   `json:"enabled"`
	State           string `json:"state"`
	Running         bool   `json:"running"`
	LastSuccess     string `json:"last_success"`
	LastError       string `json:"last_error"`
	Failures        int    `json:"failures"`
	NextRun         string `json:"next_run"`
	Refs            int    `json:"refs"`
	LagSeconds      int64  `json:"lag_seconds"`
	Behind          bool   `json:"behind"`
}

type mirrorPage struct {
	Items []mirrorView `json:"items"`
}

func cmdRepos(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, reposUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		cl.http.Timeout = 2 * time.Hour // a first sync of a large repository takes a while
		switch args[0] {
		case "mirror":
			return cl.reposMirror(args[1:])
		}
		return usageErr("unknown command repos %s\n\n%s", args[0], reposUsage)
	})
}

func repoPath(s string) (string, error) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", usageErr("name the repository as OWNER/NAME, not %q", s)
	}
	return "/api/v1/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func (cl *client) reposMirror(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, reposUsage)
		return cliErr{exitUsage, ""}
	}
	switch args[0] {
	case "status":
		return cl.mirrorStatus(args[1:])
	case "add":
		return cl.mirrorAdd(args[1:])
	case "sync":
		return cl.mirrorSync(args[1:])
	case "pause", "resume", "remove":
		return cl.mirrorChange(args[0], args[1:])
	case "stop":
		return cl.mirrorStop(args[1:])
	}
	return usageErr("unknown command repos mirror %s\n\n%s", args[0], reposUsage)
}

func lag(s int64) string {
	if s <= 0 {
		return "-"
	}
	return (time.Duration(s) * time.Second).Round(time.Second).String()
}

func (cl *client) mirrorStatus(args []string) error {
	fs := flag.NewFlagSet("repos mirror status", flag.ContinueOnError)
	behind := fs.Bool("behind", false, "only mirrors that have fallen behind")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	var page mirrorPage
	switch len(pos) {
	case 0:
		path := "/api/v1/mirrors"
		if *behind {
			path += "?behind=true"
		}
		err = cl.call("GET", path, nil, &page)
	case 1:
		p, perr := repoPath(pos[0])
		if perr != nil {
			return perr
		}
		err = cl.call("GET", p+"/mirrors", nil, &page)
	default:
		return usageErr("launchway repos mirror status [OWNER/NAME] [--behind] [--json]")
	}
	if err != nil {
		return err
	}
	return cl.print(page, *asJSON, func() {
		if len(page.Items) == 0 {
			fmt.Fprintln(cl.out, "No mirrors.")
			return
		}
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "REPOSITORY\tID\tDIR\tSTATE\tLAG\tREFS\tSOURCE")
		for _, m := range page.Items {
			state := m.State
			switch {
			case !m.Enabled:
				state = "paused"
			case m.Running:
				state = "running"
			case m.Behind:
				state += " (behind)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", m.Repository, m.ID, m.Direction, state, lag(m.LagSeconds), m.Refs, m.URL)
		}
		_ = tw.Flush()
		for _, m := range page.Items {
			if m.LastError != "" {
				fmt.Fprintf(cl.out, "\n%s %s: %s (failed %d times in a row)\n", m.Repository, m.ID, m.LastError, m.Failures)
			}
		}
	})
}

func (cl *client) mirrorAdd(args []string) error {
	fs := flag.NewFlagSet("repos mirror add", flag.ContinueOnError)
	u := fs.String("url", "", "the https address of the other repository")
	dir := fs.String("direction", "pull", "pull (an exact copy of the address) or push (copy every update to it)")
	user := fs.String("username", "", "the user name that goes with the credential")
	cenv := fs.String("credential-env", "", "the NAME of an environment variable of the server that holds the token")
	cfile := fs.String("credential-file", "", "the path of a file on the server that holds the token")
	every := fs.Duration("interval", 0, "how often it runs, at least 1m (default 1h for pull)")
	noPrune := fs.Bool("no-prune", false, "keep refs the other side no longer has")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *u == "" || (*cenv != "" && *cfile != "") {
		return usageErr("launchway repos mirror add OWNER/NAME --url URL [--direction pull|push] [--credential-env NAME | --credential-file PATH]")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	body := map[string]any{"direction": *dir, "url": *u, "username": *user}
	if *every != 0 {
		body["interval_seconds"] = int64(every.Seconds())
	}
	if *noPrune {
		body["prune"] = false
	}
	switch {
	case *cenv != "":
		body["credential"] = map[string]string{"env": *cenv}
	case *cfile != "":
		body["credential"] = map[string]string{"file": *cfile}
	}
	var out mirrorView
	if err := cl.call("POST", p+"/mirrors", body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s is now a %s mirror of %s (%s). The first run starts at once; see launchway repos mirror status %s.\n",
			out.Repository, out.Direction, out.URL, out.ID, out.Repository)
	})
}

// pullMirror finds the id of a repository's pull mirror.
func (cl *client) pullMirror(path string) (string, error) {
	var page mirrorPage
	if err := cl.call("GET", path+"/mirrors", nil, &page); err != nil {
		return "", err
	}
	for _, m := range page.Items {
		if m.Direction == "pull" {
			return m.ID, nil
		}
	}
	return "", apiErr("this repository has no pull mirror; give --id for a push mirror")
}

func (cl *client) mirrorSync(args []string) error {
	fs := flag.NewFlagSet("repos mirror sync", flag.ContinueOnError)
	id := fs.String("id", "", "the mirror (default: the repository's pull mirror)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos mirror sync OWNER/NAME [--id mir_...]")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	if *id == "" {
		if *id, err = cl.pullMirror(p); err != nil {
			return err
		}
	}
	var out mirrorView
	if err := cl.call("POST", p+"/mirrors/"+url.PathEscape(*id)+"/sync", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s synced: %d refs, state %s, last success %s.\n", out.Repository, out.Refs, out.State, out.LastSuccess)
	})
}

func (cl *client) mirrorChange(verb string, args []string) error {
	fs := flag.NewFlagSet("repos mirror "+verb, flag.ContinueOnError)
	id := fs.String("id", "", "the mirror")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *id == "" {
		return usageErr("launchway repos mirror %s OWNER/NAME --id mir_...", verb)
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	path := p + "/mirrors/" + url.PathEscape(*id)
	if verb == "remove" {
		if err := cl.call("DELETE", path, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Removed %s.\n", *id)
		return nil
	}
	var out mirrorView
	if err := cl.call("PATCH", path, map[string]any{"enabled": verb == "resume"}, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%s is %s.\n", out.ID, map[bool]string{true: "running on its schedule", false: "paused"}[out.Enabled])
	return nil
}

func (cl *client) mirrorStop(args []string) error {
	fs := flag.NewFlagSet("repos mirror stop", flag.ContinueOnError)
	final := fs.Bool("final-sync", false, "run the mirror once more first; nothing changes if that fails")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos mirror stop OWNER/NAME [--final-sync]")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out map[string]any
	if err := cl.call("POST", p+"/stop-mirroring", map[string]any{"final_sync": *final}, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s is no longer a mirror: it is the primary now and takes pushes.\n", pos[0])
	})
}
