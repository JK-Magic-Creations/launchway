package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
)

// reposBrowseUsage is the part of the repos help about reading code.
const reposBrowseUsage = `
  launchway repos tree OWNER/NAME [PATH] [--ref R] [--json]
                                    a directory's entries with the last commit of each (older than the search
                                    bound: shown as "older")
  launchway repos cat OWNER/NAME PATH [--ref R] [--binary]
                                    a file's text on standard output; --binary writes bytes of a non-text file
  launchway repos log OWNER/NAME [--ref R] [--path P] [--limit N] [--json]
                                    the commits of a ref, or of one path (the same as repos commits)
  launchway repos blame OWNER/NAME PATH [--ref R] [--from N] [--to M] [--json]
                                    who last changed each line of a file, a range at a time
  launchway repos show OWNER/NAME COMMIT [--patch PATH] [--json]
                                    one commit: message, parents, runs for it and the files it changed; with
                                    --patch, the unified patch of one file
  launchway repos compare OWNER/NAME --head H [--base B] [--json]
                                    how far a branch is ahead of and behind another, with the commits and files
  launchway repos search OWNER/NAME QUERY [--ref R] [--regex] [--case] [--in content|path|both]
                                    [--dir D] [--ext go,ts] [--limit N] [--json]
                                    literal or regular-expression search over a repository's files at a ref
  launchway repos branches OWNER/NAME [--q TEXT] [--json]
                                    the branches with ahead and behind the default branch, protection and open pull request
  launchway repos branches start OWNER/NAME ITEM [--name NAME] [--from REF] [--dry-run]
                                    make a branch for a work item, named from its key and title, and tie the two together
                                    (--dry-run only prints the name it would use)
  launchway repos branches stale OWNER/NAME [--delete --yes] [--json]
                                    the merged branches with no commit for the hosting setting's days (default 90); --delete --yes
                                    removes exactly the ones listed (protected branches and open pull requests are never touched)
  launchway repos branches create OWNER/NAME NAME [--from REF]
                                    make a branch (operators; protected names: administrators)
  launchway repos branches delete OWNER/NAME NAME
                                    delete a branch and print how to bring it back
  launchway repos inspections OWNER/NAME [--json]
                                    whether an Inspection can be started now (and why not), and the latest ones
  launchway repos inspect OWNER/NAME [--ref R] [--json]
                                    start a run of only the Inspection at the default branch or REF (operators)
  launchway repos branches rename OWNER/NAME OLD NEW
                                    rename a branch (never the default branch)
  launchway repos tags OWNER/NAME [--q TEXT] [--json]
                                    the tags, with an annotated tag's message
  launchway repos tags create OWNER/NAME NAME [--from REF]
                                    make a lightweight tag (operators; protected names: administrators)
  launchway repos tags delete OWNER/NAME NAME
                                    delete a tag and print how to bring it back
`

func (cl *client) reposBrowse(cmd string, args []string) error {
	switch cmd {
	case "tree":
		return cl.reposTree(args)
	case "cat":
		return cl.reposCat(args)
	case "log":
		return cl.reposCommits(args)
	case "blame":
		return cl.reposBlame(args)
	case "show":
		return cl.reposShow(args)
	case "compare":
		return cl.reposCompare(args)
	case "search":
		return cl.reposSearch(args)
	case "branches":
		return cl.reposBranches(args)
	case "tags":
		return cl.reposTags(args)
	case "activity":
		return cl.reposActivity(args)
	case "languages":
		return cl.reposLanguages(args)
	case "contributors":
		return cl.reposContributors(args)
	case "inspections":
		return cl.reposInspections(args)
	case "inspect":
		return cl.reposInspect(args)
	}
	return usageErr("unknown command repos %s", cmd)
}

func (cl *client) reposTree(args []string) error {
	fs := flag.NewFlagSet("repos tree", flag.ContinueOnError)
	ref := fs.String("ref", "", "a branch, tag or commit (default: the default branch)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return usageErr("launchway repos tree OWNER/NAME [PATH] [--ref R]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := url.Values{"last": {"1"}}
	if len(pos) == 2 {
		q.Set("path", pos[1])
	}
	if *ref != "" {
		q.Set("ref", *ref)
	}
	var it struct {
		Type        string `json:"type"`
		Path        string `json:"path"`
		LastBounded bool   `json:"last_bounded"`
		Entries     []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Size int64  `json:"size"`
			Last *struct {
				SHA, Subject, At string
			} `json:"last"`
		} `json:"entries"`
	}
	if err := cl.call("GET", base+"/files?"+q.Encode(), nil, &it); err != nil {
		return err
	}
	return cl.print(it, *asJSON, func() {
		if it.Type != "dir" {
			fmt.Fprintf(cl.out, "%s is a file: use repos cat.\n", it.Path)
			return
		}
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, e := range it.Entries {
			name := e.Name
			if e.Type == "dir" {
				name += "/"
			}
			last := "older"
			if e.Last != nil {
				last = fmt.Sprintf("%.8s  %.10s  %s", e.Last.SHA, e.Last.At, e.Last.Subject)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Type, name, last)
		}
		_ = tw.Flush()
		if it.LastBounded {
			fmt.Fprintln(cl.out, "\"older\": last changed beyond the commits searched; use repos log --path.")
		}
	})
}

func (cl *client) reposCat(args []string) error {
	fs := flag.NewFlagSet("repos cat", flag.ContinueOnError)
	ref := fs.String("ref", "", "a branch, tag or commit (default: the default branch)")
	binary := fs.Bool("binary", false, "write the bytes of a file that is not text")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway repos cat OWNER/NAME PATH [--ref R] [--binary]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := url.Values{"path": {pos[1]}}
	if *ref != "" {
		q.Set("ref", *ref)
	}
	var it struct {
		Type, Path, Encoding, Content string
		Size                          int64
		Truncated                     bool
	}
	if err := cl.call("GET", base+"/files?"+q.Encode(), nil, &it); err != nil {
		return err
	}
	switch {
	case it.Type == "dir":
		return usageErr("%s is a directory: use repos tree", it.Path)
	case it.Type != "file":
		return usageErr("%s is a %s, not a file with text", it.Path, it.Type)
	case it.Truncated:
		return usageErr("%s is %d bytes, over what the API reads; clone the repository", it.Path, it.Size)
	case it.Encoding == "base64":
		if !*binary {
			return usageErr("%s is not text (%d bytes); add --binary to write its bytes", it.Path, it.Size)
		}
		b, err := base64.StdEncoding.DecodeString(it.Content)
		if err != nil {
			return err
		}
		_, err = cl.out.Write(b)
		return err
	}
	_, err = fmt.Fprint(cl.out, it.Content)
	return err
}

func (cl *client) reposBlame(args []string) error {
	fs := flag.NewFlagSet("repos blame", flag.ContinueOnError)
	ref := fs.String("ref", "", "a branch, tag or commit (default: the default branch)")
	from := fs.Int("from", 0, "first line")
	to := fs.Int("to", 0, "last line (at most 1000 lines at a time)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway repos blame OWNER/NAME PATH [--ref R] [--from N] [--to M]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := url.Values{"path": {pos[1]}}
	if *ref != "" {
		q.Set("ref", *ref)
	}
	if *from > 0 {
		q.Set("from", strconv.Itoa(*from))
	}
	if *to > 0 {
		q.Set("to", strconv.Itoa(*to))
	}
	var b struct {
		Total int `json:"total"`
		Next  int `json:"next"`
		Lines []struct {
			N    int
			SHA  string
			Text string
		} `json:"lines"`
		Commits map[string]struct{ Author, Subject string } `json:"commits"`
	}
	if err := cl.call("GET", base+"/blame?"+q.Encode(), nil, &b); err != nil {
		return err
	}
	return cl.print(b, *asJSON, func() {
		for _, l := range b.Lines {
			fmt.Fprintf(cl.out, "%.8s %-14.14s %5d  %s\n", l.SHA, b.Commits[l.SHA].Author, l.N, l.Text)
		}
		if b.Next > 0 {
			fmt.Fprintf(cl.out, "-- %d lines in all; the next range starts at line %d (--from %d)\n", b.Total, b.Next, b.Next)
		}
	})
}

func (cl *client) reposCompare(args []string) error {
	fs := flag.NewFlagSet("repos compare", flag.ContinueOnError)
	head := fs.String("head", "", "the branch (or tag) with the changes")
	baseRef := fs.String("base", "", "the branch to compare against (default: the default branch)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *head == "" {
		return usageErr("launchway repos compare OWNER/NAME --head H [--base B]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := url.Values{"head": {*head}}
	if *baseRef != "" {
		q.Set("base", *baseRef)
	}
	var c struct {
		Base, Head    string
		Ahead, Behind int
		ExistingPull  int `json:"existing_pull"`
		Commits       []struct{ SHA, Subject, Author string }
		Changes       struct {
			Total, Additions, Deletions int
			Files                       []struct {
				Path, Status string
				Additions    int
				Deletions    int
			}
		}
	}
	if err := cl.call("GET", base+"/compare?"+q.Encode(), nil, &c); err != nil {
		return err
	}
	return cl.print(c, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s is %d ahead of and %d behind %s; %d files, +%d -%d.\n", c.Head, c.Ahead, c.Behind, c.Base, c.Changes.Total, c.Changes.Additions, c.Changes.Deletions)
		for _, x := range c.Commits {
			fmt.Fprintf(cl.out, "  %.8s %s\n", x.SHA, x.Subject)
		}
		for _, f := range c.Changes.Files {
			fmt.Fprintf(cl.out, "  %-9s %s (+%d -%d)\n", f.Status, f.Path, f.Additions, f.Deletions)
		}
		if c.ExistingPull > 0 {
			fmt.Fprintf(cl.out, "Pull request #%d is open for these branches.\n", c.ExistingPull)
		}
	})
}

func (cl *client) reposSearch(args []string) error {
	fs := flag.NewFlagSet("repos search", flag.ContinueOnError)
	ref := fs.String("ref", "", "a branch, tag or commit (default: the default branch)")
	regex := fs.Bool("regex", false, "the query is a regular expression")
	matchCase := fs.Bool("case", false, "match upper and lower case exactly")
	in := fs.String("in", "", "content (default), path or both")
	dir := fs.String("dir", "", "only under this directory")
	ext := fs.String("ext", "", "only files with these extensions, comma separated")
	limit := fs.Int("limit", 0, "most matches (at most 500)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway repos search OWNER/NAME QUERY [--ref R] [--regex] [--case] [--in X] [--dir D] [--ext E] [--limit N]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := url.Values{"q": {pos[1]}}
	for k, v := range map[string]string{"ref": *ref, "in": *in, "dir": *dir, "ext": *ext} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if *regex {
		q.Set("regex", "1")
	}
	if *matchCase {
		q.Set("case", "1")
	}
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}
	var r struct {
		Matches int  `json:"matches"`
		Partial bool `json:"partial"`
		Reason  string
		Files   []struct {
			Path      string
			PathMatch bool `json:"path_match"`
			Matches   []struct {
				Line int
				Text string
			}
		}
	}
	if err := cl.call("GET", base+"/search?"+q.Encode(), nil, &r); err != nil {
		return err
	}
	return cl.print(r, *asJSON, func() {
		for _, f := range r.Files {
			if len(f.Matches) == 0 {
				fmt.Fprintln(cl.out, f.Path)
			}
			for _, m := range f.Matches {
				fmt.Fprintf(cl.out, "%s:%d: %s\n", f.Path, m.Line, strings.TrimSpace(m.Text))
			}
		}
		fmt.Fprintf(cl.out, "%d matches.\n", r.Matches)
		if r.Partial {
			fmt.Fprintf(cl.out, "Partial results: %s.\n", r.Reason)
		}
	})
}

func (cl *client) reposBranches(args []string) error {
	if len(args) > 0 && args[0] == "rename" {
		fs := flag.NewFlagSet("repos branches rename", flag.ContinueOnError)
		pos, err := parseArgs(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 3 {
			return usageErr("launchway repos branches rename OWNER/NAME OLD NEW")
		}
		base, err := repoPath(pos[0])
		if err != nil {
			return err
		}
		var out struct{ Name, Was, Commit, Note string }
		if err := cl.call("POST", base+"/branches/rename", map[string]string{"name": pos[1], "new_name": pos[2]}, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Renamed %s to %s (at %.10s).\n%s\n", out.Was, out.Name, out.Commit, out.Note)
		return nil
	}
	if len(args) > 0 && (args[0] == "create" || args[0] == "delete") {
		return cl.reposRefWrite("branches", "branch", args)
	}
	if len(args) > 0 && args[0] == "start" {
		return cl.reposStartBranch(args[1:])
	}
	if len(args) > 0 && args[0] == "stale" {
		return cl.reposStaleBranches(args[1:])
	}
	fs := flag.NewFlagSet("repos branches", flag.ContinueOnError)
	q := fs.String("q", "", "only names containing this text")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos branches OWNER/NAME [--q TEXT]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Total int
		Items []struct {
			Name          string
			Default       bool
			Protected     bool
			Ahead, Behind int
			OpenPull      int `json:"open_pull"`
			Subject       string
			Stale, Merged bool
			LastRun       *struct {
				Number           int
				Pipeline, Status string
			} `json:"last_run"`
		}
	}
	if err := cl.call("GET", base+"/branches?"+url.Values{"q": {*q}}.Encode(), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, b := range out.Items {
			flags := ""
			if b.Default {
				flags += " default"
			}
			if b.Protected {
				flags += " protected"
			}
			if b.OpenPull > 0 {
				flags += fmt.Sprintf(" pull #%d", b.OpenPull)
			}
			if b.Stale {
				flags += " stale"
			}
			if b.LastRun != nil {
				flags += fmt.Sprintf(" run #%d %s", b.LastRun.Number, b.LastRun.Status)
			}
			fmt.Fprintf(tw, "%s\t+%d -%d\t%s\t%s\n", b.Name, b.Ahead, b.Behind, strings.TrimSpace(flags), b.Subject)
		}
		_ = tw.Flush()
	})
}

func (cl *client) reposTags(args []string) error {
	if len(args) > 0 && (args[0] == "create" || args[0] == "delete") {
		return cl.reposRefWrite("tags", "tag", args)
	}
	fs := flag.NewFlagSet("repos tags", flag.ContinueOnError)
	q := fs.String("q", "", "only names containing this text")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos tags OWNER/NAME [--q TEXT]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			Name, Commit, Subject, Message string
			Annotated                      bool
		}
	}
	if err := cl.call("GET", base+"/tags?"+url.Values{"q": {*q}}.Encode(), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		for _, t := range out.Items {
			msg := t.Subject
			if t.Annotated {
				msg = strings.SplitN(t.Message, "\n", 2)[0]
			}
			fmt.Fprintf(cl.out, "%-20s %.10s  %s\n", t.Name, t.Commit, msg)
		}
	})
}

func (cl *client) reposShow(args []string) error {
	fs := flag.NewFlagSet("repos show", flag.ContinueOnError)
	patch := fs.String("patch", "", "print the unified patch of this file")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway repos show OWNER/NAME COMMIT [--patch PATH]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var c struct {
		Commit struct {
			SHA        string   `json:"sha"`
			Message    string   `json:"message"`
			Author     string   `json:"author"`
			AuthorAt   string   `json:"authored_at"`
			ParentSHAs []string `json:"parent_shas"`
		} `json:"commit"`
		Runs []struct {
			Project, Status string
			Number          int
		} `json:"runs"`
	}
	if err := cl.call("GET", base+"/commits/"+url.PathEscape(pos[1]), nil, &c); err != nil {
		return err
	}
	from := "root"
	if len(c.Commit.ParentSHAs) > 0 {
		from = c.Commit.ParentSHAs[0]
	}
	q := url.Values{"from": {from}, "to": {c.Commit.SHA}}
	if *patch != "" {
		q.Set("path", *patch)
		var pt struct {
			Diff   string `json:"diff"`
			Held   bool   `json:"held"`
			Binary bool   `json:"binary"`
		}
		if err := cl.call("GET", base+"/diff/patch?"+q.Encode(), nil, &pt); err != nil {
			return err
		}
		return cl.print(pt, *asJSON, func() {
			switch {
			case pt.Binary:
				fmt.Fprintln(cl.out, "A binary file: no patch.")
			case pt.Held:
				fmt.Fprintln(cl.out, "This patch is large and was held back; use the API with full=1.")
			default:
				fmt.Fprint(cl.out, pt.Diff)
			}
		})
	}
	var df struct {
		Total     int `json:"total"`
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
		Files     []struct {
			Path, Status         string
			Additions, Deletions int
		} `json:"files"`
	}
	if err := cl.call("GET", base+"/diff?"+q.Encode(), nil, &df); err != nil {
		return err
	}
	out := map[string]any{"commit": c.Commit, "runs": c.Runs, "files": df.Files, "total": df.Total}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "commit %s\nAuthor: %s\nDate:   %s\n", c.Commit.SHA, c.Commit.Author, c.Commit.AuthorAt)
		if len(c.Commit.ParentSHAs) > 0 {
			fmt.Fprintf(cl.out, "Parents: %s\n", strings.Join(c.Commit.ParentSHAs, " "))
		}
		fmt.Fprintf(cl.out, "\n%s\n\n", c.Commit.Message)
		for _, r := range c.Runs {
			fmt.Fprintf(cl.out, "run %s #%d: %s\n", r.Project, r.Number, r.Status)
		}
		for _, f := range df.Files {
			fmt.Fprintf(cl.out, "  %-9s %s (+%d -%d)\n", f.Status, f.Path, f.Additions, f.Deletions)
		}
		fmt.Fprintf(cl.out, "%d files, +%d -%d\n", df.Total, df.Additions, df.Deletions)
	})
}

func (cl *client) reposInspections(args []string) error {
	fs := flag.NewFlagSet("repos inspections", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos inspections OWNER/NAME [--json]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Available bool
		Reason    string
		Project   string
		Runs      []struct {
			ID     string
			Number int
			Status string
		}
	}
	if err := cl.call("GET", base+"/inspections", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if out.Available {
			fmt.Fprintf(cl.out, "An Inspection can be started (Flight %s): launchway repos inspect %s\n", out.Project, pos[0])
		} else {
			fmt.Fprintf(cl.out, "No Inspection can be started: %s\n", out.Reason)
		}
		for _, r := range out.Runs {
			fmt.Fprintf(cl.out, "  #%d  %s  %s\n", r.Number, r.Status, r.ID)
		}
	})
}

func (cl *client) reposInspect(args []string) error {
	fs := flag.NewFlagSet("repos inspect", flag.ContinueOnError)
	ref := fs.String("ref", "", "a branch or tag (default: the default branch)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos inspect OWNER/NAME [--ref R]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Project string
		Run     struct {
			ID     string
			Number int
		}
	}
	if err := cl.call("POST", base+"/inspections", map[string]string{"ref": *ref}, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "Started the Inspection as run #%d of %s (%s).\n", out.Run.Number, out.Project, out.Run.ID)
	})
}

// reposRefWrite makes or deletes a branch or a tag: repos branches create|delete, repos tags create|delete.
func (cl *client) reposRefWrite(plural, one string, args []string) error {
	verb := args[0]
	fs := flag.NewFlagSet("repos "+plural+" "+verb, flag.ContinueOnError)
	from := fs.String("from", "", "a branch, tag or commit to start from (default: the default branch)")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway repos %s %s OWNER/NAME NAME", plural, verb)
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	if verb == "create" {
		var out struct{ Name, Commit string }
		if err := cl.call("POST", base+"/"+plural, map[string]string{"name": pos[1], "from": *from}, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Made the %s %s at %.10s.\n", one, out.Name, out.Commit)
		return nil
	}
	var out struct{ Name, Was, Undo string }
	if err := cl.call("DELETE", base+"/"+plural+"?name="+url.QueryEscape(pos[1]), nil, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Deleted the %s %s (it was at %s).\nTo bring it back: %s\n", one, out.Name, out.Was, out.Undo)
	return nil
}

// reposStaleBranches lists the merged stale branches and, with --delete --yes, removes exactly those listed.
func (cl *client) reposStaleBranches(args []string) error {
	fs := flag.NewFlagSet("repos branches stale", flag.ContinueOnError)
	del := fs.Bool("delete", false, "delete the branches listed")
	yes := fs.Bool("yes", false, "with --delete: yes, delete these")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos branches stale OWNER/NAME [--delete --yes]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Days       int `json:"stale_days"`
		Candidates []string
		Deleted    []string
		Skipped    map[string]string
	}
	if err := cl.call("POST", base+"/branches/delete-stale", map[string]any{"confirm": false}, &out); err != nil {
		return err
	}
	if *del && *yes && len(out.Candidates) > 0 {
		if err := cl.call("POST", base+"/branches/delete-stale", map[string]any{"confirm": true, "names": out.Candidates}, &out); err != nil {
			return err
		}
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Deleted) > 0 || len(out.Skipped) > 0 {
			for _, n := range out.Deleted {
				fmt.Fprintln(cl.out, "deleted", n)
			}
			for n, why := range out.Skipped {
				fmt.Fprintf(cl.out, "left %s: %s\n", n, why)
			}
			return
		}
		if len(out.Candidates) == 0 {
			fmt.Fprintf(cl.out, "No merged branch has been quiet for %d days.\n", out.Days)
			return
		}
		fmt.Fprintf(cl.out, "Merged branches with no commit for %d days:\n", out.Days)
		for _, n := range out.Candidates {
			fmt.Fprintln(cl.out, "  "+n)
		}
		if *del {
			fmt.Fprintln(cl.out, "Add --yes to delete exactly these.")
		}
	})
}

// reposStartBranch makes a branch for a work item, or says what it would be called.
func (cl *client) reposStartBranch(args []string) error {
	fs := flag.NewFlagSet("repos branches start", flag.ContinueOnError)
	name := fs.String("name", "", "the branch name (default: made from the item's key and title)")
	from := fs.String("from", "", "a branch, tag or commit to start from (default: the default branch)")
	dry := fs.Bool("dry-run", false, "only print the name")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway repos branches start OWNER/NAME ITEM [--name NAME] [--from REF] [--dry-run]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Name, Commit, Item, From, LinkError string
		Linked                              bool
	}
	if *dry {
		if err := cl.call("GET", base+"/branches/suggest?item="+url.QueryEscape(pos[1]), nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "%s (from %s)\n", out.Name, out.From) })
	}
	if err := cl.call("POST", base+"/branches/from-item", map[string]string{"item": pos[1], "name": *name, "from": *from}, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "Made the branch %s at %.10s for %s.\n", out.Name, out.Commit, out.Item)
		if !out.Linked {
			fmt.Fprintf(cl.out, "The link on the item was not made: %s\n", out.LinkError)
		}
	})
}
