package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// The commands for forks and template repositories (docs/guide/forks.md and
// docs/guide/templates.md). They call the routes of the same names.

const reposForksUsage = `
Forks and templates (a fork is made in your own space, or as an administrator under any owner):
  launchway repos fork OWNER/NAME [--owner O] [--name N] [--visibility V]   fork a repository you can read
  launchway repos forks OWNER/NAME                       the forks of a repository
  launchway repos fork-status OWNER/NAME                 a repository's parent, forks, switches and, for a fork, how far it is from its parent
  launchway repos sync OWNER/NAME [--branch B] [--upstream B] [--merge]   bring a fork's branch up to date with its parent
  launchway repos fork-settings [OWNER/NAME | --owner O] [--allow-forks on|off|default] [--template=B]   who may fork, and mark a template
  launchway repos template show OWNER/NAME               the variables a template declares
  launchway repos template use OWNER/NAME --name N [--owner O] [--var NAME=VALUE]... [--all-branches] [--visibility V]
                                                         start a new repository from a template, as one commit with no history
`

func (cl *client) reposFork(args []string) error {
	fs := flag.NewFlagSet("repos fork", flag.ContinueOnError)
	owner := fs.String("owner", "", "where the fork goes (default: your own space)")
	name := fs.String("name", "", "the fork's name (default: the parent's)")
	vis := fs.String("visibility", "", "private, internal or public; never wider than the parent's")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos fork OWNER/NAME [--owner O] [--name N]")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out map[string]any
	if err := cl.call("POST", p+"/forks", map[string]any{"owner": *owner, "name": *name, "visibility": *vis}, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "Forked %s to %v (%v)\n", pos[0], out["path"], out["visibility"]) })
}

func (cl *client) reposForks(args []string) error {
	fs := flag.NewFlagSet("repos forks", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos forks OWNER/NAME")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := cl.call("GET", p+"/forks", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Items) == 0 {
			fmt.Fprintln(cl.out, "No forks you can see.")
		}
		for _, f := range out.Items {
			fmt.Fprintf(cl.out, "%v (%v) forked by %v\n", f["path"], f["visibility"], f["forked_by"])
		}
	})
}

func (cl *client) reposForkStatus(args []string) error {
	fs := flag.NewFlagSet("repos fork-status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos fork-status OWNER/NAME")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Parent        string `json:"parent"`
		Root          string `json:"root"`
		ParentGone    bool   `json:"parent_deleted"`
		Forks         int    `json:"forks"`
		AllowForks    bool   `json:"allow_forks"`
		AllowForksWhy string `json:"allow_forks_why"`
		Template      bool   `json:"template"`
		Standing      *struct {
			Branch   string `json:"branch"`
			Upstream string `json:"upstream"`
			Ahead    int    `json:"ahead"`
			Behind   int    `json:"behind"`
		} `json:"standing"`
	}
	if err := cl.call("GET", p+"/fork", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		switch {
		case out.ParentGone:
			fmt.Fprintln(cl.out, "A fork whose parent was deleted.")
		case out.Parent != "":
			fmt.Fprintf(cl.out, "A fork of %s (root %s).\n", out.Parent, out.Root)
		default:
			fmt.Fprintln(cl.out, "Not a fork.")
		}
		if s := out.Standing; s != nil {
			fmt.Fprintf(cl.out, "%s is %d ahead of and %d behind the parent's %s.\n", s.Branch, s.Ahead, s.Behind, s.Upstream)
		}
		fmt.Fprintf(cl.out, "%d forks. Forks allowed: %v (%s). Template: %v.\n", out.Forks, out.AllowForks, out.AllowForksWhy, out.Template)
	})
}

func (cl *client) reposSync(args []string) error {
	fs := flag.NewFlagSet("repos sync", flag.ContinueOnError)
	branch := fs.String("branch", "", "the fork's branch (default: its default branch)")
	upstream := fs.String("upstream", "", "the parent's branch (default: the same name)")
	merge := fs.Bool("merge", false, "merge when the branch has commits of its own (default: fast-forward only)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos sync OWNER/NAME [--branch B] [--merge]")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	mode := "fast-forward"
	if *merge {
		mode = "merge"
	}
	var out struct {
		Message string `json:"message"`
	}
	if err := cl.call("POST", p+"/sync", map[string]any{"branch": *branch, "upstream": *upstream, "mode": mode}, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() { fmt.Fprintln(cl.out, out.Message+".") })
}

func (cl *client) reposTemplate(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway repos template show|use OWNER/NAME ...")
	}
	switch args[0] {
	case "show":
		return cl.reposTemplateShow(args[1:])
	case "use":
		return cl.reposTemplateUse(args[1:])
	}
	return usageErr("unknown command repos template %s (show or use; mark a repository with repos fork-settings --template)", args[0])
}

func (cl *client) reposTemplateShow(args []string) error {
	fs := flag.NewFlagSet("repos template show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos template show OWNER/NAME")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	var out struct {
		Template  bool `json:"template"`
		Variables []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Pattern     string `json:"pattern"`
			Default     string `json:"default"`
		} `json:"variables"`
	}
	if err := cl.call("GET", p+"/template", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if !out.Template {
			fmt.Fprintf(cl.out, "%s is not a template repository.\n", pos[0])
			return
		}
		fmt.Fprintf(cl.out, "%s is a template with %d variables.\n", pos[0], len(out.Variables))
		for _, v := range out.Variables {
			fmt.Fprintf(cl.out, "  %s: %s (pattern %q, default %q)\n", v.Name, v.Description, v.Pattern, v.Default)
		}
	})
}

func (cl *client) reposTemplateUse(args []string) error {
	fs := flag.NewFlagSet("repos template use", flag.ContinueOnError)
	owner := fs.String("owner", "", "where the new repository goes (default: your own space)")
	name := fs.String("name", "", "the new repository's name")
	vis := fs.String("visibility", "", "never wider than the template's")
	all := fs.Bool("all-branches", false, "also bring the other branches, each as one commit")
	var vars multi
	fs.Var(&vars, "var", "NAME=VALUE for a variable the template declares (repeat)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *name == "" {
		return usageErr("launchway repos template use OWNER/NAME --name N [--var NAME=VALUE]...")
	}
	p, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, v := range vars {
		k, val, ok := strings.Cut(v, "=")
		if !ok || k == "" {
			return usageErr("--var is NAME=VALUE, not %q", v)
		}
		values[k] = val
	}
	var out map[string]any
	if err := cl.call("POST", p+"/generate", map[string]any{"owner": *owner, "name": *name, "visibility": *vis, "values": values, "include_branches": *all}, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "Made %v from the template %s.\n", out["path"], pos[0]) })
}

func (cl *client) reposForkSettings(args []string) error {
	fs := flag.NewFlagSet("repos fork-settings", flag.ContinueOnError)
	owner := fs.String("owner", "", "set the switch of every repository of this owner")
	allow := fs.String("allow-forks", "", "on, off or default")
	tpl := fs.String("template", "", "true or false: mark the repository as a template")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	var out map[string]any
	switch {
	case *owner != "" && len(pos) == 0:
		p := "/api/v1/owners/" + *owner + "/fork-settings"
		if *allow == "" {
			err = cl.call("GET", p, nil, &out)
		} else {
			err = cl.call("PUT", p, map[string]any{"allow_forks": *allow}, &out)
		}
	case *owner == "" && len(pos) == 1:
		p, perr := repoPath(pos[0])
		if perr != nil {
			return perr
		}
		body := map[string]any{}
		if *allow != "" {
			body["allow_forks"] = *allow
		}
		if *tpl != "" {
			body["template"] = *tpl == "true"
		}
		if len(body) == 0 {
			err = cl.call("GET", p+"/fork", nil, &out)
		} else {
			err = cl.call("PATCH", p, body, &out)
		}
	default:
		return usageErr("launchway repos fork-settings OWNER/NAME [--allow-forks on|off|default] [--template=true|false], or --owner O [--allow-forks V]")
	}
	if err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "%v\n", out) })
}

var _ = os.Stdout
