package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Pull requests from forks and resolving conflicts, on the command line.

const prForksUsage = `  launchway pr create --head OWNER:BRANCH [-R OWNER/NAME] [--allow-edits]   a pull request from a branch of a fork (OWNER/NAME:BRANCH for a fork of another name)
  launchway pr conflicts N [-R OWNER/NAME]                what stops the target from merging into the branch, file by file
  launchway pr resolve N [-R OWNER/NAME] [--file F]... --take ours|theirs|both|delete
                                                          answer the conflicts in whole files: ours is the pull request's branch, theirs the target;
                                                          without --file every conflicting file takes the same answer. Makes the merge commit, by you.
  launchway pr maintainer-edits N [-R OWNER/NAME] --allow|--deny   let the target's maintainers update the branch in your fork
  launchway pr approve-run N [-R OWNER/NAME]               let the pipeline of a pull request from a fork run for its current head (without secrets)
`

func (cl *client) prConflicts(args []string) error {
	f := newPRFlags("conflicts")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "conflicts")
	if err != nil {
		return err
	}
	var out struct {
		Conflicts struct {
			Head   string `json:"head"`
			Target string `json:"target"`
			Clean  bool   `json:"clean"`
			Files  []struct {
				Path    string `json:"path"`
				Kind    string `json:"kind"`
				Message string `json:"message"`
				Choices []struct {
					Take  string `json:"take"`
					Label string `json:"label"`
				} `json:"choices"`
			} `json:"files"`
		} `json:"conflicts"`
		MayResolve bool   `json:"may_resolve"`
		WhyNot     string `json:"why_not"`
	}
	if err := cl.call("GET", fmt.Sprintf("%s/pulls/%d/conflicts", f.base, n), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		c := out.Conflicts
		if c.Clean || len(c.Files) == 0 {
			fmt.Fprintln(cl.out, "Nothing conflicts: the target merges into the branch cleanly.")
			return
		}
		fmt.Fprintf(cl.out, "%d conflicting files (branch %s, target %s):\n", len(c.Files), short10(c.Head), short10(c.Target))
		for _, file := range c.Files {
			fmt.Fprintf(cl.out, "  %s (%s): %s\n", file.Path, file.Kind, file.Message)
			for _, ch := range file.Choices {
				fmt.Fprintf(cl.out, "      --take %s: %s\n", ch.Take, ch.Label)
			}
		}
		if !out.MayResolve {
			fmt.Fprintf(cl.out, "You cannot resolve them here: %s\n", out.WhyNot)
		}
	})
}

func (cl *client) prResolve(args []string) error {
	f := newPRFlags("resolve")
	var files multi
	f.fs.Var(&files, "file", "a conflicting file to answer (repeat); without any, every conflicting file")
	take := f.fs.String("take", "", "ours (the pull request's branch), theirs (the target), both (text: ours then theirs) or delete")
	message := f.fs.String("message", "", "the merge commit's message")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "resolve")
	if err != nil {
		return err
	}
	switch *take {
	case "ours", "theirs", "both", "delete":
	default:
		return usageErr("--take is ours, theirs, both or delete; for anything finer use the web page or the API (regions, content)")
	}
	var listed struct {
		Conflicts struct {
			Head   string `json:"head"`
			Target string `json:"target"`
			Files  []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"conflicts"`
	}
	if err := cl.call("GET", fmt.Sprintf("%s/pulls/%d/conflicts", f.base, n), nil, &listed); err != nil {
		return err
	}
	paths := []string(files)
	if len(paths) == 0 {
		for _, file := range listed.Conflicts.Files {
			paths = append(paths, file.Path)
		}
	}
	if len(paths) == 0 {
		return apiErr("nothing conflicts; there is nothing to resolve")
	}
	var answers []map[string]any
	for _, p := range paths {
		answers = append(answers, map[string]any{"path": p, "take": *take})
	}
	var out struct {
		Head     string   `json:"head"`
		Resolved []string `json:"resolved"`
	}
	body := map[string]any{"expect_head": listed.Conflicts.Head, "expect_target": listed.Conflicts.Target, "files": answers, "message": *message}
	if err := cl.call("POST", fmt.Sprintf("%s/pulls/%d/conflicts/resolve", f.base, n), body, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		fmt.Fprintf(cl.out, "Resolved %s; the branch is at %s.\n", strings.Join(out.Resolved, ", "), short10(out.Head))
	})
}

func (cl *client) prMaintainerEdits(args []string) error {
	f := newPRFlags("maintainer-edits")
	allow := f.fs.Bool("allow", false, "let the target's maintainers update the branch")
	deny := f.fs.Bool("deny", false, "do not")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "maintainer-edits")
	if err != nil {
		return err
	}
	if *allow == *deny {
		return usageErr("say --allow or --deny")
	}
	var out json.RawMessage
	if err := cl.call("PUT", fmt.Sprintf("%s/pulls/%d/maintainer-edits", f.base, n), map[string]any{"allow": *allow}, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if *allow {
			fmt.Fprintln(cl.out, "Maintainers may update the branch in your fork.")
		} else {
			fmt.Fprintln(cl.out, "Maintainers may not update the branch in your fork.")
		}
	})
}

func (cl *client) prApproveRun(args []string) error {
	f := newPRFlags("approve-run")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "approve-run")
	if err != nil {
		return err
	}
	var out struct {
		Head string `json:"approved_head"`
	}
	if err := cl.call("POST", fmt.Sprintf("%s/pulls/%d/approve-run", f.base, n), map[string]any{}, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { fmt.Fprintf(cl.out, "The pipeline may run for %s (without secrets).\n", short10(out.Head)) })
}

// createFromFork opens a pull request from a branch of a fork.
func (cl *client) prCreateFromFork(f *prFlags, head, title, body, target string, draft, allowEdits bool) error {
	var p prSummary
	if err := cl.call("POST", f.base+"/pulls/from-fork", map[string]any{"head": head, "title": title, "body": body, "target_branch": target, "draft": draft,
		"allow_maintainer_edits": allowEdits}, &p); err != nil {
		return err
	}
	return cl.print(p, *f.asJSON, func() { fmt.Fprintf(cl.out, "Opened %s\n", p.line()) })
}

// bodyText is a description given as text or as a file the person names.
func forkBodyText(body, file string) (string, error) {
	if file == "" {
		return body, nil
	}
	b, err := os.ReadFile(file) //nolint:gosec // the person named their own file
	return string(b), err
}
