package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

const reposUsage = `Usage:
  launchway repos list [--json]                          the repositories this server hosts that you may read
  launchway repos get OWNER/NAME [--json]                one repository, with its clone address
  launchway repos create NAME [--owner O] [--branch B]   make an empty repository (administrators)
                         [--visibility private|internal|public]
  launchway repos update OWNER/NAME [--visibility V]     change a repository (its administrators)
                         [--branch B] [--archived=false] [--rename-to OWNER/NAME]
  launchway repos delete OWNER/NAME                      move a repository to the trash

Git hosting must be turned on (the installation setting forge.hosting); otherwise every
command answers feature_off and nothing is lost. A repository is OWNER/NAME; its roles
come from the same role bindings as a project's, matched on that path. Changes are
recorded in the audit trail. Delete keeps the data and frees the name.
`

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
		switch args[0] {
		case "list":
			return cl.reposList(args[1:])
		case "get":
			return cl.reposGet(args[1:])
		case "create":
			return cl.reposCreate(args[1:])
		case "update":
			return cl.reposUpdate(args[1:])
		case "delete":
			return cl.reposDelete(args[1:])
		}
		return usageErr("unknown command repos %s\n\n%s", args[0], reposUsage)
	})
}

func repoPath(ref string) (string, error) {
	owner, name, ok := splitRepo(ref)
	if !ok {
		return "", usageErr("a repository is OWNER/NAME, not %q", ref)
	}
	return "/api/v1/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func splitRepo(ref string) (owner, name string, ok bool) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			owner, name = ref[:i], ref[i+1:]
			return owner, name, owner != "" && name != ""
		}
	}
	return "", "", false
}

func printRepo(cl *client, r forge.Repository) {
	state := string(r.Visibility)
	if r.Archived {
		state += ", archived"
	}
	fmt.Fprintf(cl.out, "%s (%s, default branch %s)\n  clone: %s\n", r.Path, state, r.DefaultBranch, r.Clone.HTTPS)
}

func (cl *client) reposList(args []string) error {
	fs := flag.NewFlagSet("repos list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var page forge.Page[forge.Repository]
	if err := cl.call("GET", "/api/v1/repositories", nil, &page); err != nil {
		return err
	}
	return cl.print(page, *asJSON, func() {
		if len(page.Items) == 0 {
			fmt.Fprintln(cl.out, "No hosted repositories yet.")
		}
		for _, r := range page.Items {
			printRepo(cl, r)
		}
	})
}

func (cl *client) reposGet(args []string) error {
	fs := flag.NewFlagSet("repos get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos get OWNER/NAME [--json]")
	}
	path, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var r forge.Repository
	if err := cl.call("GET", path, nil, &r); err != nil {
		return err
	}
	return cl.print(r, *asJSON, func() { printRepo(cl, r) })
}

func (cl *client) reposCreate(args []string) error {
	fs := flag.NewFlagSet("repos create", flag.ContinueOnError)
	owner := fs.String("owner", "", "the owner (default: the installation's default owner)")
	branch := fs.String("branch", "", "the default branch (main)")
	vis := fs.String("visibility", "", "private (default), internal or public")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos create NAME [--owner O] [--branch B] [--visibility V]")
	}
	var r forge.Repository
	body := map[string]any{"owner": *owner, "name": pos[0], "default_branch": *branch, "visibility": *vis}
	if err := cl.call("POST", "/api/v1/repositories", body, &r); err != nil {
		return err
	}
	return cl.print(r, *asJSON, func() { fmt.Fprint(cl.out, "Created "); printRepo(cl, r) })
}

func (cl *client) reposUpdate(args []string) error {
	fs := flag.NewFlagSet("repos update", flag.ContinueOnError)
	vis := fs.String("visibility", "", "private, internal or public")
	branch := fs.String("branch", "", "the default branch")
	archived := fs.Bool("archived", false, "archive the repository (no pushes); --archived=false brings it back")
	rename := fs.String("rename-to", "", "OWNER/NAME to rename or move it to; the old address stops working")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	given := visited(fs)
	if len(pos) != 1 || (!given["visibility"] && !given["branch"] && !given["archived"] && !given["rename-to"]) {
		return usageErr("launchway repos update OWNER/NAME [--visibility V] [--branch B] [--archived=false] [--rename-to OWNER/NAME]")
	}
	path, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	body := map[string]any{}
	if given["visibility"] {
		body["visibility"] = *vis
	}
	if given["branch"] {
		body["default_branch"] = *branch
	}
	if given["archived"] {
		body["archived"] = *archived
	}
	if given["rename-to"] {
		o, n, ok := splitRepo(*rename)
		if !ok {
			return usageErr("--rename-to is OWNER/NAME, not %q", *rename)
		}
		body["owner"], body["name"] = o, n
	}
	var r forge.Repository
	if err := cl.call("PATCH", path, body, &r); err != nil {
		return err
	}
	return cl.print(r, *asJSON, func() { fmt.Fprint(cl.out, "Updated "); printRepo(cl, r) })
}

func (cl *client) reposDelete(args []string) error {
	fs := flag.NewFlagSet("repos delete", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos delete OWNER/NAME")
	}
	path, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	if err := cl.call("DELETE", path, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Moved %s to the trash. Its data is kept and the name can be used again.\n", pos[0])
	return nil
}
