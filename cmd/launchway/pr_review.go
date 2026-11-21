package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// The review-depth commands of launchway pr: owners, the merge queue, updating a branch, viewed files and the inbox.

func (cl *client) prOwners(args []string) error {
	f := newPRFlags("owners")
	check := f.fs.String("check", "", "an owners file to check (a path on this computer)")
	if err := f.parse(args); err != nil {
		return err
	}
	if *check != "" {
		text, err := os.ReadFile(*check)
		if err != nil {
			return err
		}
		var out struct {
			Valid    bool `json:"valid"`
			Problems []struct {
				Line    int    `json:"line"`
				Message string `json:"message"`
			} `json:"problems"`
		}
		if err := cl.call("POST", f.base+"/owners/check", map[string]any{"text": string(text)}, &out); err != nil {
			return err
		}
		return cl.print(out, *f.asJSON, func() {
			if out.Valid {
				fmt.Fprintln(cl.out, "The owners file is valid.")
				return
			}
			for _, p := range out.Problems {
				fmt.Fprintf(cl.out, "line %d: %s\n", p.Line, p.Message)
			}
		})
	}
	n, _, err := prNumber(f.pos, "owners")
	if err != nil {
		return err
	}
	var out struct {
		Owners struct {
			Present bool `json:"present"`
			Files   []struct {
				Path   string   `json:"path"`
				Owners []string `json:"owners"`
			} `json:"files"`
			Problems []struct {
				Line    int    `json:"line"`
				Message string `json:"message"`
			} `json:"problems"`
		} `json:"owners"`
	}
	if err := cl.call("GET", fmt.Sprintf("%s/pulls/%d/owners", f.base, n), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if !out.Owners.Present {
			fmt.Fprintln(cl.out, "The target branch has no .launchway/owners file, so no file has an owner.")
			return
		}
		for _, p := range out.Owners.Problems {
			fmt.Fprintf(cl.out, "owners file, line %d: %s\n", p.Line, p.Message)
		}
		for _, fl := range out.Owners.Files {
			who := strings.Join(fl.Owners, " ")
			if who == "" {
				who = "(no owner)"
			}
			fmt.Fprintf(cl.out, "%-50s %s\n", fl.Path, who)
		}
	})
}

func (cl *client) prQueue(args []string) error {
	f := newPRFlags("queue")
	method := f.fs.String("method", "", "merge, squash or rebase (default: the repository's)")
	priority := f.fs.Int("priority", 0, "a higher number goes first (administrators)")
	cancel := f.fs.Bool("cancel", false, "take it out of the Holding pattern")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "queue")
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s/pulls/%d/queue", f.base, n)
	if *cancel {
		if err := cl.call("DELETE", base, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(cl.out, "Taken out of the merge queue.")
		return nil
	}
	if f.given["method"] || f.given["priority"] {
		var e queueEntry
		if err := cl.call("POST", base, map[string]any{"method": *method, "priority": *priority}, &e); err != nil {
			return err
		}
		return cl.print(e, *f.asJSON, func() { fmt.Fprintln(cl.out, e.line()) })
	}
	var out struct {
		Enabled bool         `json:"enabled"`
		Branch  string       `json:"branch"`
		Entry   *queueEntry  `json:"entry"`
		Queue   []queueEntry `json:"queue"`
	}
	if err := cl.call("GET", base, nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if !out.Enabled {
			fmt.Fprintf(cl.out, "%s has no merge queue.\n", out.Branch)
			return
		}
		if out.Entry != nil {
			fmt.Fprintln(cl.out, out.Entry.line())
		} else {
			fmt.Fprintln(cl.out, "Not in the merge queue. Add it with: launchway pr queue", n, "--method merge")
		}
		for _, e := range out.Queue {
			fmt.Fprintf(cl.out, "  %d. #%d %s (%s)\n", e.Position, e.PullNumber, e.Title, e.State)
		}
	})
}

type queueEntry struct {
	PullNumber int    `json:"pull_number"`
	Title      string `json:"title"`
	Position   int    `json:"position"`
	State      string `json:"state"`
	Method     string `json:"method"`
	Reason     string `json:"reason"`
}

func (e queueEntry) line() string {
	switch e.State {
	case "removed":
		return fmt.Sprintf("#%d was taken out of the merge queue: %s", e.PullNumber, e.Reason)
	case "merged":
		return fmt.Sprintf("#%d merged.", e.PullNumber)
	case "testing":
		return fmt.Sprintf("#%d is number %d in the merge queue and is being tested.", e.PullNumber, e.Position)
	}
	return fmt.Sprintf("#%d is number %d in the merge queue.", e.PullNumber, e.Position)
}

func (cl *client) prUpdateBranch(args []string) error {
	f := newPRFlags("update-branch")
	rebase := f.fs.Bool("rebase", false, "replay the branch's commits on the target instead of merging it in (rewrites the branch)")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "update-branch")
	if err != nil {
		return err
	}
	method := "merge"
	if *rebase {
		method = "rebase"
	}
	var out struct {
		Head string `json:"head"`
	}
	if err := cl.call("POST", fmt.Sprintf("%s/pulls/%d/update-branch", f.base, n), map[string]any{"method": method}, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { fmt.Fprintf(cl.out, "The branch is up to date: %s.\n", short10(out.Head)) })
}

func (cl *client) prViewed(args []string) error {
	f := newPRFlags("viewed")
	mark := f.fs.String("mark", "", "files to mark viewed, comma separated")
	unmark := f.fs.String("unmark", "", "files to unmark, comma separated")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "viewed")
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s/pulls/%d/viewed", f.base, n)
	var out struct {
		Viewed []string `json:"viewed"`
	}
	switch {
	case *mark != "":
		err = cl.call("PUT", base, map[string]any{"paths": splitList(*mark), "viewed": true}, &out)
	case *unmark != "":
		err = cl.call("PUT", base, map[string]any{"paths": splitList(*unmark), "viewed": false}, &out)
	default:
		err = cl.call("GET", base, nil, &out)
	}
	if err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if len(out.Viewed) == 0 {
			fmt.Fprintln(cl.out, "No files marked viewed.")
		}
		for _, p := range out.Viewed {
			fmt.Fprintln(cl.out, p)
		}
	})
}

func (cl *client) prInbox(args []string) error {
	f := &prFlags{fs: flag.NewFlagSet("pr inbox", flag.ContinueOnError)}
	f.asJSON = f.fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(f.fs, args); err != nil {
		return err
	}
	var out struct {
		Items []struct {
			Repository string `json:"repository"`
			Number     int    `json:"number"`
			Title      string `json:"title"`
			Kind       string `json:"kind"`
			Why        string `json:"why"`
		} `json:"items"`
	}
	if err := cl.call("GET", "/api/v1/pulls/inbox", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if len(out.Items) == 0 {
			fmt.Fprintln(cl.out, "Nothing waits on you.")
		}
		for _, it := range out.Items {
			fmt.Fprintf(cl.out, "%s#%d  %s: %s\n", it.Repository, it.Number, it.Title, it.Why)
		}
	})
}
