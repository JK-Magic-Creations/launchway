package main

import (
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// The Overview of a repository: recent activity, languages by share, contributors.

const reposOverviewUsage = `  launchway repos activity OWNER[/NAME] [--kind K,...] [--limit N] [--all] [--json]
                                    what happened lately, newest first: pushes, tags, pull requests (kind pull), releases, runs;
                                    OWNER alone is every repository of the owner you may read; --all follows the pages
  launchway repos languages OWNER/NAME [--ref REF] [--json]
                                    the languages by share (vendored and generated paths left out; .launchway/languages overrides)
  launchway repos contributors OWNER/NAME [--period 30d|90d|365d|all] [--wait] [--json]
                                    commit counts per author, made in the background for the head commit; --wait asks again until done
`

func (cl *client) reposActivity(args []string) error {
	fs := flag.NewFlagSet("repos activity", flag.ContinueOnError)
	kind := fs.String("kind", "", "comma-separated kinds: push, tag, pull, pull_opened, pull_merged, pull_closed, release, run")
	limit := fs.Int("limit", 0, "items per page (at most 100)")
	all := fs.Bool("all", false, "follow every page")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos activity OWNER[/NAME] [--kind K] [--limit N] [--all]")
	}
	base := "/api/v1/owners/" + url.PathEscape(pos[0]) + "/activity"
	if strings.Contains(pos[0], "/") {
		if base, err = repoPath(pos[0]); err != nil {
			return err
		}
		base += "/activity"
	}
	type item struct {
		Kind, Actor, Repository, Title, Ref, Project, Status, Version string
		At                                                            int64
		Number                                                        int
	}
	var all2 struct {
		Items []item `json:"items"`
		Next  string `json:"next"`
	}
	before := ""
	for {
		q := url.Values{}
		if *kind != "" {
			q.Set("kind", *kind)
		}
		if *limit > 0 {
			q.Set("limit", strconv.Itoa(*limit))
		}
		if before != "" {
			q.Set("before", before)
		}
		var page struct {
			Items []item `json:"items"`
			Next  string `json:"next"`
		}
		if err := cl.call("GET", base+"?"+q.Encode(), nil, &page); err != nil {
			return err
		}
		all2.Items = append(all2.Items, page.Items...)
		all2.Next = page.Next
		if !*all || page.Next == "" || len(all2.Items) >= 1000 {
			break
		}
		before = page.Next
	}
	return cl.print(all2, *asJSON, func() {
		if len(all2.Items) == 0 {
			fmt.Fprintln(cl.out, "Nothing has happened yet.")
			return
		}
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, it := range all2.Items {
			what := it.Title
			switch it.Kind {
			case "pull_opened", "pull_merged", "pull_closed":
				what = fmt.Sprintf("#%d %s", it.Number, it.Title)
			case "run":
				what = fmt.Sprintf("%s #%d %s %s", it.Project, it.Number, it.Status, it.Ref)
			case "release":
				what = it.Project + " " + it.Version
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", time.UnixMilli(it.At).UTC().Format("2006-01-02 15:04"), it.Kind, it.Repository, it.Actor, what)
		}
		_ = tw.Flush()
		if all2.Next != "" {
			fmt.Fprintf(cl.out, "More: --all, or --limit with the page after %s\n", all2.Next)
		}
	})
}

func (cl *client) reposLanguages(args []string) error {
	fs := flag.NewFlagSet("repos languages", flag.ContinueOnError)
	ref := fs.String("ref", "", "a branch, tag or commit (default: the default branch)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos languages OWNER/NAME [--ref REF]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Commit    string `json:"commit"`
		Languages []struct {
			Name  string
			Bytes int64
			Files int
			Share float64
		}
		Excluded  int    `json:"excluded_files"`
		Truncated bool   `json:"truncated"`
		Rules     string `json:"rules"`
	}
	q := ""
	if *ref != "" {
		q = "?ref=" + url.QueryEscape(*ref)
	}
	if err := cl.call("GET", base+"/languages"+q, nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Languages) == 0 {
			fmt.Fprintln(cl.out, "No files with a known language.")
			return
		}
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, l := range out.Languages {
			fmt.Fprintf(tw, "%s\t%.2f%%\t%d files\t%d bytes\n", l.Name, l.Share, l.Files, l.Bytes)
		}
		_ = tw.Flush()
		note := fmt.Sprintf("Rules: %s; %d files left out", out.Rules, out.Excluded)
		if out.Truncated {
			note += "; the tree is larger than the pass, shares are of the first files"
		}
		fmt.Fprintln(cl.out, note)
	})
}

func (cl *client) reposContributors(args []string) error {
	fs := flag.NewFlagSet("repos contributors", flag.ContinueOnError)
	period := fs.String("period", "", "30d, 90d, 365d or all (default 90d)")
	wait := fs.Bool("wait", false, "ask again until the background job has made the answer")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos contributors OWNER/NAME [--period P] [--wait]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := ""
	if *period != "" {
		q = "?period=" + url.QueryEscape(*period)
	}
	var out struct {
		State        string `json:"state"`
		Period       string `json:"period"`
		Error        string `json:"error"`
		Walked       int    `json:"walked"`
		AtLeast      bool   `json:"at_least"`
		Contributors []struct {
			Name, Email string
			Commits     int
		}
	}
	for i := 0; ; i++ {
		if err := cl.call("GET", base+"/contributors"+q, nil, &out); err != nil {
			return err
		}
		if out.State != "computing" || !*wait || i >= 150 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	return cl.print(out, *asJSON, func() {
		switch out.State {
		case "computing":
			fmt.Fprintln(cl.out, "The answer is being made in the background; ask again in a few seconds (or use --wait).")
			return
		case "failed":
			fmt.Fprintln(cl.out, out.Error)
			return
		}
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, c := range out.Contributors {
			more := ""
			if out.AtLeast {
				more = "+"
			}
			fmt.Fprintf(tw, "%s%s\t%s\t<%s>\n", strconv.Itoa(c.Commits), more, c.Name, c.Email)
		}
		_ = tw.Flush()
		if out.AtLeast {
			fmt.Fprintf(cl.out, "Only the newest %d commits were looked at: every count is at least that many.\n", out.Walked)
		}
	})
}
