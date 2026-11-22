package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

const prUsage = `Usage:
  launchway pr list [-R OWNER/NAME] [--state open|closed|merged|all] [--mine] [--review-requested]
                    [--author U] [--label L] [--json]       the pull requests of a repository
  launchway pr view N [-R OWNER/NAME] [--files] [--commits] [--comments] [--json]
                                                          one pull request: what blocks the merge, in words, and what you can do
  launchway pr create --title T [--body B | --body-file F] [--head BRANCH] [--base BRANCH] [--draft]
                      [--label L] [--reviewer U] [-R OWNER/NAME]   open a pull request (the head defaults to the current branch)
  launchway pr checkout N [-R OWNER/NAME]                 fetch refs/pull/N/head and check it out as the branch pr/N
  launchway pr diff N [-R OWNER/NAME] [--name-only] [--file PATH] [--since REVISION] [--full]
                                                          the patch, the changed files, or the changes since a revision you reviewed
  launchway pr comment N [-R OWNER/NAME] --body TEXT [--path P --line L [--start-line S]] [--reply-to ID]
                      [--edit ID] [--resolve ID] [--reopen ID] [--delete ID]   comment on the conversation or on a line; manage a comment
  launchway pr review N [-R OWNER/NAME] (--approve | --request-changes | --comment) [--body TEXT] [--request A,B | --unrequest A,B]
                      [--dismiss ID --reason TEXT] [--request USER,USER] [--request-group G,G] [--unrequest-group G,G]
                                                          review, dismiss a review, ask people or groups to review
  launchway pr owners N [-R OWNER/NAME] [--json]          who owns each changed file (from .launchway/owners on the target branch)
  launchway pr owners --check FILE [-R OWNER/NAME]        check an owners file and say what is wrong with it, by line
  launchway pr merge N [-R OWNER/NAME] [--merge | --squash | --rebase] [--delete-branch] [--message M]
                      [--auto] [--bypass-reason TEXT]       merge it (or have it merge itself when the rules pass; --auto=false turns that off)
  launchway pr close N [-R OWNER/NAME]                    close without merging
  launchway pr reopen N [-R OWNER/NAME]                   open a closed pull request again
  launchway pr edit N [-R OWNER/NAME] [--title T] [--body B] [--ready] [--draft] [--label L,L] [--assignee U,U] [--base BRANCH]
  launchway pr revert N [-R OWNER/NAME]                   open a new pull request that undoes a merged one
  launchway pr apply N COMMENT-ID... [-R OWNER/NAME]      apply one suggestion, or several as one commit (needs push access)
  launchway pr queue N [-R OWNER/NAME] [--method M] [--priority P] [--cancel]
                                                          add it to the branch's merge queue, show its place, or take it out (--cancel)
  launchway pr update-branch N [-R OWNER/NAME] [--rebase]   bring the source branch up to date with the target
` + prForksUsage + `  launchway pr viewed N [-R OWNER/NAME] [--mark PATH,PATH] [--unmark PATH,PATH]   the files you marked viewed (a file that changes is unmarked)
  launchway pr inbox [--json]                             the pull requests that wait on you: reviews asked, changes asked, a queue that failed
  launchway pr settings [-R OWNER/NAME] [--allow-merge=B] [--allow-squash=B] [--allow-rebase=B] [--default-method M]
                      [--delete-branch=B] [--dismiss-stale=B] [--assign-reviewers owners|round_robin|least_loaded|off]
                                                          the merge methods and settings (set: repository administrators)
  launchway pr rules [-R OWNER/NAME] [--branch PATTERN --approvals N --approval-role operator|admin --check NAME,NAME
                      --up-to-date --resolved --no-self-approval=B --admin-bypass --owner-approval --dismiss-stale
                      --fresh-approval --queue --path-check PATHS=CHECK]   the merge rules of protected branches (set: administrators)

N is a pull request number. -R names the repository; without it the repository is the one that "origin" points to.
`

func cmdPR(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, prUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		cl.http.Timeout = 5 * time.Minute
		return cl.pr(args[0], args[1:])
	})
}

// prView is the part of a pull request's answer the command line prints.
type prSummary struct {
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	State        string   `json:"state"`
	Draft        bool     `json:"draft"`
	Author       string   `json:"author"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	HeadSHA      string   `json:"head_sha"`
	Labels       []string `json:"labels"`
	Reviewers    []string `json:"requested_reviewers"`
	Body         string   `json:"body"`
	AutoMerge    string   `json:"auto_merge"`
	Mergeability struct {
		State  string   `json:"state"`
		Files  []string `json:"conflicting_files"`
		Behind int      `json:"behind"`
	} `json:"mergeability"`
	Readiness struct {
		CanMerge  bool   `json:"can_merge"`
		Why       string `json:"why"`
		Approvals int    `json:"approvals"`
		Checks    []struct {
			Name     string `json:"name"`
			Status   string `json:"status"`
			Required bool   `json:"required"`
		} `json:"checks"`
	} `json:"readiness"`
	Actions []struct {
		Name      string `json:"name"`
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	} `json:"actions"`
	FetchRef string `json:"fetch_ref"`
}

func (p prSummary) line() string {
	state := p.State
	if p.Draft && p.State == "open" {
		state = "draft"
	}
	return fmt.Sprintf("#%d  %-6s  %s  (%s into %s, by %s)", p.Number, state, p.Title, p.SourceBranch, p.TargetBranch, p.Author)
}

// prRepo is the repository a command is about: -R, or what origin points to.
func prRepo(flagValue string) (string, error) {
	if flagValue != "" {
		if _, _, ok := splitRepo(flagValue); !ok {
			return "", usageErr("a repository is OWNER/NAME, not %q", flagValue)
		}
		return flagValue, nil
	}
	out, err := gitOut("config", "--get", "remote.origin.url")
	if err != nil {
		return "", usageErr("say which repository with -R OWNER/NAME (this directory has no \"origin\" remote)")
	}
	u := strings.TrimSuffix(strings.TrimSpace(out), ".git")
	if i := strings.LastIndexAny(u, ":/"); i >= 0 {
		rest := u[:i]
		if j := strings.LastIndexAny(rest, ":/"); j >= 0 {
			cand := rest[j+1:] + "/" + u[i+1:]
			if _, _, ok := splitRepo(cand); ok {
				return cand, nil
			}
		}
	}
	return "", usageErr("cannot tell the repository from origin (%s); use -R OWNER/NAME", strings.TrimSpace(out))
}

func gitOut(args ...string) (string, error) {
	// The person's own git in their own working copy: see gitexec.UserOutput.
	return gitexec.UserOutput(context.Background(), args...)
}

func prNumber(pos []string, verb string) (int, []string, error) {
	if len(pos) == 0 {
		return 0, nil, usageErr("launchway pr %s N ...: which pull request?", verb)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(pos[0], "#"))
	if err != nil || n < 1 {
		return 0, nil, usageErr("%q is not a pull request number", pos[0])
	}
	return n, pos[1:], nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type prFlags struct {
	fs     *flag.FlagSet
	repo   *string
	asJSON *bool
	given  map[string]bool
	pos    []string
	base   string
}

func newPRFlags(name string) *prFlags {
	f := &prFlags{fs: flag.NewFlagSet("pr "+name, flag.ContinueOnError)}
	f.repo = f.fs.String("R", "", "the repository, OWNER/NAME")
	f.asJSON = f.fs.Bool("json", false, "print as JSON")
	return f
}

func (f *prFlags) parse(args []string) error {
	pos, err := parseArgs(f.fs, args)
	if err != nil {
		return err
	}
	f.pos, f.given = pos, visited(f.fs)
	r, err := prRepo(*f.repo)
	if err != nil {
		return err
	}
	base, err := repoPath(r)
	if err != nil {
		return err
	}
	f.base = base
	return nil
}

func (cl *client) pr(verb string, args []string) error {
	switch verb {
	case "list":
		return cl.prList(args)
	case "view":
		return cl.prView(args)
	case "create":
		return cl.prCreate(args)
	case "checkout":
		return cl.prCheckout(args)
	case "diff":
		return cl.prDiff(args)
	case "comment":
		return cl.prComment(args)
	case "review":
		return cl.prReview(args)
	case "merge":
		return cl.prMerge(args)
	case "close", "reopen":
		return cl.prState(verb, args)
	case "edit":
		return cl.prEdit(args)
	case "revert":
		return cl.prRevert(args)
	case "apply":
		return cl.prApply(args)
	case "settings":
		return cl.prSettings(args)
	case "rules":
		return cl.prRules(args)
	case "owners":
		return cl.prOwners(args)
	case "queue":
		return cl.prQueue(args)
	case "update-branch":
		return cl.prUpdateBranch(args)
	case "conflicts":
		return cl.prConflicts(args)
	case "resolve":
		return cl.prResolve(args)
	case "maintainer-edits":
		return cl.prMaintainerEdits(args)
	case "approve-run":
		return cl.prApproveRun(args)
	case "viewed":
		return cl.prViewed(args)
	case "inbox":
		return cl.prInbox(args)
	}
	return usageErr("unknown command pr %s\n\n%s", verb, prUsage)
}

func (cl *client) prList(args []string) error {
	f := newPRFlags("list")
	state := f.fs.String("state", "open", "open, closed, merged or all")
	mine := f.fs.Bool("mine", false, "only pull requests you opened")
	review := f.fs.Bool("review-requested", false, "only pull requests that ask you to review")
	author := f.fs.String("author", "", "only this author's")
	label := f.fs.String("label", "", "only this label's")
	if err := f.parse(args); err != nil {
		return err
	}
	q := url.Values{"state": {*state}}
	if *mine {
		q.Set("author", "@me")
	} else if *author != "" {
		q.Set("author", *author)
	}
	if *review {
		q.Set("reviewer", "@me")
	}
	if *label != "" {
		q.Set("label", *label)
	}
	var out struct {
		Items []prSummary `json:"items"`
		Total int         `json:"total"`
	}
	if err := cl.call("GET", f.base+"/pulls?"+q.Encode(), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if len(out.Items) == 0 {
			fmt.Fprintln(cl.out, "No pull requests.")
		}
		for _, p := range out.Items {
			fmt.Fprintln(cl.out, p.line())
		}
	})
}

func (cl *client) prView(args []string) error {
	f := newPRFlags("view")
	files := f.fs.Bool("files", false, "list the changed files")
	commits := f.fs.Bool("commits", false, "list the commits")
	comments := f.fs.Bool("comments", false, "show the conversation, threads and reviews")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "view")
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s/pulls/%d", f.base, n)
	var p prSummary
	if err := cl.call("GET", base, nil, &p); err != nil {
		return err
	}
	if *f.asJSON {
		var raw map[string]any
		if err := cl.call("GET", base, nil, &raw); err != nil {
			return err
		}
		return cl.print(raw, true, nil)
	}
	fmt.Fprintln(cl.out, p.line())
	fmt.Fprintf(cl.out, "head %s  fetch with: git fetch origin %s\n", short10(p.HeadSHA), p.FetchRef)
	switch p.Mergeability.State {
	case "conflicts":
		fmt.Fprintf(cl.out, "Conflicts in: %s\n", strings.Join(p.Mergeability.Files, ", "))
	case "clean":
		if p.Mergeability.Behind > 0 {
			fmt.Fprintf(cl.out, "Merges cleanly, %d commit(s) behind %s.\n", p.Mergeability.Behind, p.TargetBranch)
		} else {
			fmt.Fprintln(cl.out, "Merges cleanly and is up to date.")
		}
	default:
		fmt.Fprintln(cl.out, "Checking whether it merges...")
	}
	if p.State == "open" {
		if p.Readiness.CanMerge {
			fmt.Fprintln(cl.out, "Ready to merge.")
		} else {
			fmt.Fprintf(cl.out, "Cannot merge yet: %s\n", p.Readiness.Why)
		}
	}
	for _, c := range p.Readiness.Checks {
		req := ""
		if c.Required {
			req = "  (required)"
		}
		fmt.Fprintf(cl.out, "  check %-24s %s%s\n", c.Name, c.Status, req)
	}
	var can, cannot []string
	for _, a := range p.Actions {
		if a.Available {
			can = append(can, a.Name)
		} else {
			cannot = append(cannot, a.Name+" ("+a.Reason+")")
		}
	}
	fmt.Fprintf(cl.out, "You can: %s\n", strings.Join(can, ", "))
	if len(cannot) > 0 {
		fmt.Fprintf(cl.out, "Not now: %s\n", strings.Join(cannot, "; "))
	}
	if p.Body != "" {
		fmt.Fprintf(cl.out, "\n%s\n", p.Body)
	}
	if *files {
		var fl struct {
			Files []struct {
				Path, Status         string
				Additions, Deletions int
			} `json:"files"`
		}
		if err := cl.call("GET", base+"/files?limit=500", nil, &fl); err != nil {
			return err
		}
		fmt.Fprintln(cl.out, "\nFiles:")
		for _, x := range fl.Files {
			fmt.Fprintf(cl.out, "  %-10s %s  +%d -%d\n", x.Status, x.Path, x.Additions, x.Deletions)
		}
	}
	if *commits {
		var cs struct {
			Items []struct{ SHA, Author, Subject string } `json:"items"`
		}
		if err := cl.call("GET", base+"/commits", nil, &cs); err != nil {
			return err
		}
		fmt.Fprintln(cl.out, "\nCommits:")
		for _, c := range cs.Items {
			fmt.Fprintf(cl.out, "  %s  %s  %s\n", short10(c.SHA), c.Author, c.Subject)
		}
	}
	if *comments {
		return cl.prConversation(base)
	}
	return nil
}

func short10(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func (cl *client) prConversation(base string) error {
	var cv struct {
		Comments []struct {
			ID, Author, Body string
		} `json:"comments"`
		Threads []struct {
			ID       string                              `json:"id"`
			Path     string                              `json:"path"`
			Line     int                                 `json:"line"`
			Outdated bool                                `json:"outdated"`
			Resolved bool                                `json:"resolved"`
			Comments []struct{ ID, Author, Body string } `json:"comments"`
		} `json:"threads"`
		Reviews []struct {
			Author, Verdict, Body, State string
			Stale                        bool
		} `json:"reviews"`
	}
	if err := cl.call("GET", base+"/conversation", nil, &cv); err != nil {
		return err
	}
	fmt.Fprintln(cl.out, "\nConversation:")
	for _, c := range cv.Comments {
		fmt.Fprintf(cl.out, "  %s (%s): %s\n", c.Author, c.ID, c.Body)
	}
	for _, r := range cv.Reviews {
		fmt.Fprintf(cl.out, "  review by %s: %s [%s]%s %s\n", r.Author, r.Verdict, r.State, map[bool]string{true: " (of an older head)"}[r.Stale], r.Body)
	}
	for _, th := range cv.Threads {
		flag := ""
		if th.Outdated {
			flag += " outdated"
		}
		if th.Resolved {
			flag += " resolved"
		}
		fmt.Fprintf(cl.out, "  thread %s on %s:%d%s\n", th.ID, th.Path, th.Line, flag)
		for _, c := range th.Comments {
			fmt.Fprintf(cl.out, "    %s (%s): %s\n", c.Author, c.ID, c.Body)
		}
	}
	return nil
}

func (cl *client) prCreate(args []string) error {
	f := newPRFlags("create")
	title := f.fs.String("title", "", "the title")
	body := f.fs.String("body", "", "the description (Markdown)")
	bodyFile := f.fs.String("body-file", "", "read the description from a file")
	head := f.fs.String("head", "", "the branch with the changes (default: the current branch)")
	target := f.fs.String("base", "", "the branch they go into (default: the repository's default branch)")
	draft := f.fs.Bool("draft", false, "open it as a draft")
	labels := f.fs.String("label", "", "labels, comma separated")
	reviewers := f.fs.String("reviewer", "", "people to ask to review, comma separated")
	allowEdits := f.fs.Bool("allow-edits", false, "for a branch of a fork: let the target's maintainers update it")
	if err := f.parse(args); err != nil {
		return err
	}
	if strings.Contains(*head, ":") { // OWNER:branch is a branch of a fork
		text, err := forkBodyText(*body, *bodyFile)
		if err != nil {
			return err
		}
		return cl.prCreateFromFork(f, *head, *title, text, *target, *draft, *allowEdits)
	}
	if *head == "" {
		out, err := gitOut("rev-parse", "--abbrev-ref", "HEAD")
		if err != nil || strings.TrimSpace(out) == "HEAD" {
			return usageErr("say the branch with --head (this directory is not on a branch)")
		}
		*head = strings.TrimSpace(out)
	}
	text, err := forkBodyText(*body, *bodyFile)
	if err != nil {
		return err
	}
	var p prSummary
	if err := cl.call("POST", f.base+"/pulls", map[string]any{"title": *title, "body": text, "source_branch": *head, "target_branch": *target, "draft": *draft,
		"labels": splitList(*labels), "requested_reviewers": splitList(*reviewers)}, &p); err != nil {
		return err
	}
	return cl.print(p, *f.asJSON, func() { fmt.Fprintf(cl.out, "Opened %s\n", p.line()) })
}

func (cl *client) prCheckout(args []string) error {
	f := newPRFlags("checkout")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "checkout")
	if err != nil {
		return err
	}
	var p prSummary
	if err := cl.call("GET", fmt.Sprintf("%s/pulls/%d", f.base, n), nil, &p); err != nil {
		return err
	}
	branch := fmt.Sprintf("pr/%d", n)
	if _, err := gitOut("fetch", "origin", p.FetchRef+":"+branch+"-fetch"); err != nil {
		return apiErr("git could not fetch %s from origin; is origin this server's repository, and are you signed in to git?", p.FetchRef)
	}
	if _, err := gitOut("branch", "-f", branch, branch+"-fetch"); err != nil {
		return apiErr("git could not make the branch %s (is it checked out already?)", branch)
	}
	_, _ = gitOut("branch", "-D", branch+"-fetch")
	if _, err := gitOut("checkout", branch); err != nil {
		return apiErr("git could not check out %s; commit or stash your changes first", branch)
	}
	fmt.Fprintf(cl.out, "On branch %s at %s (pull request #%d: %s)\n", branch, short10(p.HeadSHA), n, p.Title)
	return nil
}

func (cl *client) prDiff(args []string) error {
	f := newPRFlags("diff")
	nameOnly := f.fs.Bool("name-only", false, "list the files instead of the patch")
	file := f.fs.String("file", "", "only this file")
	since := f.fs.String("since", "", "the changes since this revision of the pull request (a revision you reviewed)")
	full := f.fs.Bool("full", false, "load a patch that is over the limits anyway")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "diff")
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s/pulls/%d", f.base, n)
	q := url.Values{}
	if *since != "" {
		q.Set("since", *since)
	}
	if *nameOnly {
		var fl struct {
			Files []struct {
				Path, Status         string
				Additions, Deletions int
			} `json:"files"`
		}
		if err := cl.call("GET", base+"/files?limit=500&"+q.Encode(), nil, &fl); err != nil {
			return err
		}
		return cl.print(fl, *f.asJSON, func() {
			for _, x := range fl.Files {
				fmt.Fprintf(cl.out, "%-10s %s  +%d -%d\n", x.Status, x.Path, x.Additions, x.Deletions)
			}
		})
	}
	if *file != "" {
		q.Add("path", *file)
	}
	if *full {
		q.Set("full", "1")
	}
	var pt struct {
		Diff      string `json:"diff"`
		Truncated bool   `json:"truncated"`
		Held      bool   `json:"held"`
		Binary    bool   `json:"binary"`
	}
	if err := cl.call("GET", base+"/patch?"+q.Encode(), nil, &pt); err != nil {
		return err
	}
	return cl.print(pt, *f.asJSON, func() {
		switch {
		case pt.Held:
			fmt.Fprintln(cl.out, "This file's patch is large and was not loaded; run again with --full to load it anyway.")
		case pt.Binary:
			fmt.Fprintln(cl.out, "Binary file.")
		default:
			fmt.Fprint(cl.out, pt.Diff)
			if pt.Truncated {
				fmt.Fprintln(cl.out, "\n(the patch was cut at the size limit)")
			}
		}
	})
}

func (cl *client) prComment(args []string) error {
	f := newPRFlags("comment")
	body := f.fs.String("body", "", "the comment (Markdown)")
	path := f.fs.String("path", "", "the file, for a comment on a line")
	line := f.fs.Int("line", 0, "the line (the last line of a range)")
	start := f.fs.Int("start-line", 0, "the first line of a range")
	reply := f.fs.String("reply-to", "", "the ID of a review comment to answer")
	edit := f.fs.String("edit", "", "the ID of your comment to change (with --body)")
	resolve := f.fs.String("resolve", "", "the ID of a comment whose thread to resolve")
	reopen := f.fs.String("reopen", "", "the ID of a comment whose thread to open again")
	del := f.fs.String("delete", "", "the ID of a comment to delete")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "comment")
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s/pulls/%d/comments", f.base, n)
	var out any
	switch {
	case *edit != "":
		err = cl.call("PATCH", base+"/"+url.PathEscape(*edit), map[string]any{"body": *body}, &out)
	case *resolve != "":
		err = cl.call("PATCH", base+"/"+url.PathEscape(*resolve), map[string]any{"resolved": true}, &out)
	case *reopen != "":
		err = cl.call("PATCH", base+"/"+url.PathEscape(*reopen), map[string]any{"resolved": false}, &out)
	case *del != "":
		err = cl.call("DELETE", base+"/"+url.PathEscape(*del), nil, nil)
	default:
		err = cl.call("POST", base, map[string]any{"body": *body, "path": *path, "line": *line, "start_line": *start, "reply_to": *reply}, &out)
	}
	if err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { fmt.Fprintln(cl.out, "Done.") })
}

func (cl *client) prReview(args []string) error {
	f := newPRFlags("review")
	approve := f.fs.Bool("approve", false, "approve")
	changes := f.fs.Bool("request-changes", false, "ask for changes")
	comment := f.fs.Bool("comment", false, "only comment")
	body := f.fs.String("body", "", "the summary")
	dismiss := f.fs.String("dismiss", "", "the ID of a review to dismiss (with --reason)")
	reason := f.fs.String("reason", "", "why it is dismissed")
	request := f.fs.String("request", "", "people to ask to review, comma separated")
	unrequest := f.fs.String("unrequest", "", "people to take off the list of those asked to review, comma separated")
	requestGroup := f.fs.String("request-group", "", "groups to ask to review (any member's review answers), comma separated")
	unrequestGroup := f.fs.String("unrequest-group", "", "groups to take off the list, comma separated")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "review")
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s/pulls/%d", f.base, n)
	switch {
	case *dismiss != "":
		if err := cl.call("POST", base+"/reviews/"+url.PathEscape(*dismiss)+"/dismiss", map[string]any{"reason": *reason}, nil); err != nil {
			return err
		}
		fmt.Fprintln(cl.out, "Dismissed.")
		return nil
	case *requestGroup != "":
		if err := cl.call("POST", base+"/review-requests", map[string]any{"groups": splitList(*requestGroup)}, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Asked group %s to review.\n", strings.Join(splitList(*requestGroup), ", "))
		return nil
	case *unrequestGroup != "":
		for _, name := range splitList(*unrequestGroup) {
			if err := cl.call("DELETE", base+"/review-requests/"+url.PathEscape("group:"+name), nil, nil); err != nil {
				return err
			}
		}
		fmt.Fprintf(cl.out, "No longer asking group %s to review.\n", strings.Join(splitList(*unrequestGroup), ", "))
		return nil
	case *request != "":
		var p prSummary
		if err := cl.call("POST", base+"/review-requests", map[string]any{"reviewers": splitList(*request)}, &p); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Asked %s to review.\n", strings.Join(splitList(*request), ", "))
		return nil
	case *unrequest != "":
		for _, name := range splitList(*unrequest) {
			if err := cl.call("DELETE", base+"/review-requests/"+url.PathEscape(name), nil, nil); err != nil {
				return err
			}
		}
		fmt.Fprintf(cl.out, "No longer asking %s to review.\n", strings.Join(splitList(*unrequest), ", "))
		return nil
	}
	verdict := ""
	for v, on := range map[string]bool{"approve": *approve, "request_changes": *changes, "comment": *comment} {
		if on {
			if verdict != "" {
				return usageErr("choose one of --approve, --request-changes, --comment")
			}
			verdict = v
		}
	}
	if verdict == "" {
		return usageErr("choose --approve, --request-changes or --comment")
	}
	var out struct {
		ID      string `json:"id"`
		Verdict string `json:"verdict"`
	}
	if err := cl.call("POST", base+"/reviews", map[string]any{"verdict": verdict, "body": *body}, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { fmt.Fprintf(cl.out, "Review submitted: %s (%s).\n", out.Verdict, out.ID) })
}

func (cl *client) prMerge(args []string) error {
	f := newPRFlags("merge")
	merge := f.fs.Bool("merge", false, "a merge commit")
	squash := f.fs.Bool("squash", false, "one commit")
	rebase := f.fs.Bool("rebase", false, "replay the commits on the target")
	del := f.fs.Bool("delete-branch", false, "delete the source branch after the merge")
	msg := f.fs.String("message", "", "the commit message")
	auto := f.fs.Bool("auto", false, "merge when the rules pass (--auto=false turns it off)")
	bypass := f.fs.String("bypass-reason", "", "an administrator's reason for merging past the rules")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "merge")
	if err != nil {
		return err
	}
	method := ""
	for m, on := range map[string]bool{"merge": *merge, "squash": *squash, "rebase": *rebase} {
		if on {
			if method != "" {
				return usageErr("choose one of --merge, --squash, --rebase")
			}
			method = m
		}
	}
	base := fmt.Sprintf("%s/pulls/%d", f.base, n)
	if f.given["auto"] {
		am := method
		if *auto && am == "" {
			am = "merge"
		}
		if !*auto {
			am = ""
		}
		var p prSummary
		if err := cl.call("PATCH", base, map[string]any{"auto_merge": am}, &p); err != nil {
			return err
		}
		if am == "" {
			fmt.Fprintln(cl.out, "Auto-merge is off.")
		} else {
			fmt.Fprintf(cl.out, "Auto-merge is on (%s): it merges when the rules pass.\n", am)
		}
		return nil
	}
	body := map[string]any{"method": method, "message": *msg, "bypass_reason": *bypass}
	if f.given["delete-branch"] {
		body["delete_branch"] = *del
	}
	var out struct {
		Commit        string `json:"commit"`
		BranchDeleted bool   `json:"branch_deleted"`
		Pull          prSummary
	}
	if err := cl.call("POST", base+"/merge", body, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		fmt.Fprintf(cl.out, "Merged #%d as %s%s\n", n, short10(out.Commit), map[bool]string{true: "; the branch was deleted"}[out.BranchDeleted])
	})
}

func (cl *client) prState(verb string, args []string) error {
	f := newPRFlags(verb)
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, verb)
	if err != nil {
		return err
	}
	state := map[string]string{"close": "closed", "reopen": "open"}[verb]
	var p prSummary
	if err := cl.call("PATCH", fmt.Sprintf("%s/pulls/%d", f.base, n), map[string]any{"state": state}, &p); err != nil {
		return err
	}
	return cl.print(p, *f.asJSON, func() { fmt.Fprintln(cl.out, p.line()) })
}

func (cl *client) prEdit(args []string) error {
	f := newPRFlags("edit")
	title := f.fs.String("title", "", "a new title")
	body := f.fs.String("body", "", "a new description")
	ready := f.fs.Bool("ready", false, "mark it ready for review")
	draft := f.fs.Bool("draft", false, "turn it back into a draft")
	labels := f.fs.String("label", "", "the labels, comma separated (replaces them)")
	assignees := f.fs.String("assignee", "", "the assignees, comma separated (replaces them)")
	target := f.fs.String("base", "", "retarget: the branch it goes into")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "edit")
	if err != nil {
		return err
	}
	patch := map[string]any{}
	if f.given["title"] {
		patch["title"] = *title
	}
	if f.given["body"] {
		patch["body"] = *body
	}
	if *ready {
		patch["draft"] = false
	}
	if *draft {
		patch["draft"] = true
	}
	if f.given["label"] {
		patch["labels"] = splitList(*labels)
	}
	if f.given["assignee"] {
		patch["assignees"] = splitList(*assignees)
	}
	if f.given["base"] {
		patch["target_branch"] = *target
	}
	if len(patch) == 0 {
		return usageErr("say what to change: --title, --body, --ready, --draft, --label, --assignee or --base")
	}
	var p prSummary
	if err := cl.call("PATCH", fmt.Sprintf("%s/pulls/%d", f.base, n), patch, &p); err != nil {
		return err
	}
	return cl.print(p, *f.asJSON, func() { fmt.Fprintln(cl.out, p.line()) })
}

func (cl *client) prRevert(args []string) error {
	f := newPRFlags("revert")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "revert")
	if err != nil {
		return err
	}
	var p prSummary
	if err := cl.call("POST", fmt.Sprintf("%s/pulls/%d/revert", f.base, n), map[string]any{}, &p); err != nil {
		return err
	}
	return cl.print(p, *f.asJSON, func() { fmt.Fprintf(cl.out, "Opened %s\n", p.line()) })
}

func (cl *client) prApply(args []string) error {
	f := newPRFlags("apply")
	if err := f.parse(args); err != nil {
		return err
	}
	n, rest, err := prNumber(f.pos, "apply")
	if err != nil || len(rest) < 1 {
		return usageErr("launchway pr apply N COMMENT-ID...")
	}
	var out struct {
		Commit string `json:"commit"`
	}
	if len(rest) == 1 {
		err = cl.call("POST", fmt.Sprintf("%s/pulls/%d/comments/%s/apply", f.base, n, url.PathEscape(rest[0])), map[string]any{}, &out)
	} else {
		err = cl.call("POST", fmt.Sprintf("%s/pulls/%d/suggestions/apply", f.base, n), map[string]any{"comment_ids": rest}, &out)
	}
	if err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { fmt.Fprintf(cl.out, "Applied as %s.\n", short10(out.Commit)) })
}

func (cl *client) prSettings(args []string) error {
	f := newPRFlags("settings")
	am := f.fs.Bool("allow-merge", true, "allow merge commits")
	as := f.fs.Bool("allow-squash", true, "allow squash merges")
	ar := f.fs.Bool("allow-rebase", true, "allow rebase merges")
	dm := f.fs.String("default-method", "", "merge, squash or rebase")
	db := f.fs.Bool("delete-branch", false, "delete the source branch after a merge by default")
	ds := f.fs.Bool("dismiss-stale", true, "dismiss approvals when new commits arrive")
	assign := f.fs.String("assign-reviewers", "", "how reviewers are chosen from the owners: owners, round_robin, least_loaded or off")
	if err := f.parse(args); err != nil {
		return err
	}
	patch := map[string]any{}
	if f.given["assign-reviewers"] {
		if *assign == "off" {
			*assign = ""
		}
		patch["assign_reviewers"] = *assign
	}
	for flagName, key := range map[string]string{"allow-merge": "allow_merge", "allow-squash": "allow_squash", "allow-rebase": "allow_rebase", "delete-branch": "delete_branch_after_merge", "dismiss-stale": "dismiss_stale_approvals"} {
		if f.given[flagName] {
			patch[key] = map[string]bool{"allow-merge": *am, "allow-squash": *as, "allow-rebase": *ar, "delete-branch": *db, "dismiss-stale": *ds}[flagName]
		}
	}
	if f.given["default-method"] {
		patch["default_method"] = *dm
	}
	method, path := "GET", f.base+"/pull-settings"
	if len(patch) > 0 {
		method = "PUT"
	}
	var out map[string]any
	if err := cl.call(method, path, orNil(patch), &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		fmt.Fprintf(cl.out, "merge %v, squash %v, rebase %v; default %v; delete the branch after a merge %v; dismiss stale approvals %v\n",
			out["allow_merge"], out["allow_squash"], out["allow_rebase"], out["default_method"], out["delete_branch_after_merge"], out["dismiss_stale_approvals"])
	})
}

func orNil(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func (cl *client) prRules(args []string) error {
	f := newPRFlags("rules")
	branch := f.fs.String("branch", "", "the branch or pattern the rule covers (to set a rule)")
	approvals := f.fs.Int("approvals", 0, "approvals required")
	role := f.fs.String("approval-role", "operator", "the lowest role an approver may have: operator or admin")
	checks := f.fs.String("check", "", "required checks (a pipeline, or pipeline/step), comma separated")
	upToDate := f.fs.Bool("up-to-date", false, "the branch must contain the target's tip")
	resolved := f.fs.Bool("resolved", false, "every thread must be resolved")
	noSelf := f.fs.Bool("no-self-approval", true, "the author's approval does not count")
	bypass := f.fs.Bool("admin-bypass", false, "an administrator may merge past the rules, saying why")
	ownerOK := f.fs.Bool("owner-approval", false, "an owner of each changed file must approve")
	stale := f.fs.Bool("dismiss-stale", false, "dismiss approvals when new commits arrive")
	fresh := f.fs.Bool("fresh-approval", false, "the latest push must be approved by someone other than who pushed it")
	queue := f.fs.Bool("queue", false, "merge through the merge queue")
	pathChecks := f.fs.String("path-check", "", "checks required only when matching files change: PATHS=CHECK;PATHS=CHECK (paths comma separated, such as docs/,*.md=docs)")
	if err := f.parse(args); err != nil {
		return err
	}
	var out struct {
		Rules []struct {
			Pattern           string   `json:"pattern"`
			RequiredApprovals int      `json:"required_approvals"`
			ApprovalRole      string   `json:"approval_role"`
			RequiredChecks    []string `json:"required_checks"`
			RequireUpToDate   bool     `json:"require_up_to_date"`
			RequireResolved   bool     `json:"require_resolved_threads"`
			NoSelfApproval    bool     `json:"no_self_approval"`
			BypassAdmins      bool     `json:"allow_admin_bypass"`
			OwnerApproval     bool     `json:"require_owner_approval"`
			DismissStale      bool     `json:"dismiss_stale_approvals"`
			FreshApproval     bool     `json:"require_latest_push_approved_by_other"`
			MergeQueue        bool     `json:"merge_queue"`
			PathChecks        []struct {
				Paths []string `json:"paths"`
				Check string   `json:"check"`
			} `json:"required_checks_by_path"`
		} `json:"rules"`
	}
	method, body := "GET", any(nil)
	if *branch != "" {
		method = "PUT"
		var pcs []map[string]any
		for _, part := range strings.Split(*pathChecks, ";") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			paths, check, ok := strings.Cut(part, "=")
			if !ok {
				return usageErr("--path-check wants PATHS=CHECK, such as docs/,*.md=docs; %q has no =", part)
			}
			pcs = append(pcs, map[string]any{"paths": splitList(paths), "check": strings.TrimSpace(check)})
		}
		body = map[string]any{"rules": []map[string]any{{"pattern": *branch, "require_owner_approval": *ownerOK, "dismiss_stale_approvals": *stale,
			"require_latest_push_approved_by_other": *fresh, "merge_queue": *queue, "required_checks_by_path": pcs, "required_approvals": *approvals, "approval_role": *role, "required_checks": splitList(*checks),
			"require_up_to_date": *upToDate, "require_resolved_threads": *resolved, "no_self_approval": *noSelf, "allow_admin_bypass": *bypass}}}
	}
	if err := cl.call(method, f.base+"/merge-rules", body, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if len(out.Rules) == 0 {
			fmt.Fprintln(cl.out, "No merge rules.")
		}
		for _, r := range out.Rules {
			fmt.Fprintf(cl.out, "%s: %d approval(s) from %s; checks [%s]; up to date %v; resolved %v; no self-approval %v; admin bypass %v\n", r.Pattern, r.RequiredApprovals,
				r.ApprovalRole, strings.Join(r.RequiredChecks, ", "), r.RequireUpToDate, r.RequireResolved, r.NoSelfApproval, r.BypassAdmins)
			var more []string
			for name, on := range map[string]bool{"owner approval": r.OwnerApproval, "dismiss stale approvals": r.DismissStale, "latest push approved by someone else": r.FreshApproval, "merge queue": r.MergeQueue} {
				if on {
					more = append(more, name)
				}
			}
			sort.Strings(more)
			for _, pc := range r.PathChecks {
				more = append(more, fmt.Sprintf("%s when %s change", pc.Check, strings.Join(pc.Paths, ", ")))
			}
			if len(more) > 0 {
				fmt.Fprintf(cl.out, "  also: %s\n", strings.Join(more, "; "))
			}
		}
	})
}
