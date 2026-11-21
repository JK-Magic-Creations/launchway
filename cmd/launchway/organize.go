package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
)

const organizeUsage = `usage: launchway organize <command>

  list        [--area pipelines|repositories|projects|runs]       folders, and each item's folder and labels
  folder      create --scope S --name N [--parent ID] [--area A]  make a shared folder (S: base, organization, unit:NAME, project:NAME)
  folder      rename ID NAME | delete ID                          change or remove one (what was in it moves up)
  move        ITEM [--folder ID] [--area A]                       file an item in a folder (no --folder takes it out)
  label       ITEM LABEL... [--area A]                            replace an item's labels
  export      [--scope S]                                         a level's layouts and folders as one JSON document, to keep in Git
  import      FILE [--dry-run]                                    apply such a document; it adds and updates and never deletes

Changes to the shared arrangement need the right to arrange that level and are
recorded in the audit trail. Your own arrangement (favorites, order, hidden
sections) is changed on the pages, and never affects anyone else.
`

func cmdOrganize(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, organizeUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		fs := flag.NewFlagSet("organize "+args[0], flag.ContinueOnError)
		area := fs.String("area", "pipelines", "pipelines, repositories, projects or runs")
		scope := fs.String("scope", "base", "base, organization, unit:NAME or project:NAME")
		name := fs.String("name", "", "folder name")
		parent := fs.String("parent", "", "parent folder ID")
		folder := fs.String("folder", "", "folder ID")
		dry := fs.Bool("dry-run", false, "say what would change")
		asJSON := fs.Bool("json", true, "print JSON")
		rest, err := parseArgs(fs, args[1:])
		if err != nil {
			return err
		}
		var out json.RawMessage
		call := func(method, path string, body any) error {
			if err := cl.call(method, path, body, &out); err != nil {
				return err
			}
			return cl.print(out, *asJSON, func() {})
		}
		a := url.PathEscape(*area)
		switch args[0] {
		case "list":
			return call("GET", "/api/v1/organize/"+a, nil)
		case "folder":
			if len(rest) >= 1 && rest[0] == "rename" && len(rest) == 3 {
				return call("PATCH", "/api/v1/organize/folders/"+url.PathEscape(rest[1]), map[string]string{"name": rest[2]})
			}
			if len(rest) == 2 && rest[0] == "delete" {
				return call("DELETE", "/api/v1/organize/folders/"+url.PathEscape(rest[1]), nil)
			}
			if len(rest) == 1 && rest[0] == "create" {
				return call("POST", "/api/v1/organize/"+a+"/folders", map[string]string{"scope": *scope, "name": *name, "parent": *parent})
			}
			fmt.Fprint(os.Stderr, organizeUsage)
			return cliErr{exitUsage, ""}
		case "move":
			if len(rest) != 1 {
				fmt.Fprint(os.Stderr, organizeUsage)
				return cliErr{exitUsage, ""}
			}
			return call("POST", "/api/v1/organize/"+a+"/move", map[string]string{"item": rest[0], "folder": *folder})
		case "label":
			if len(rest) < 1 {
				fmt.Fprint(os.Stderr, organizeUsage)
				return cliErr{exitUsage, ""}
			}
			return call("PUT", "/api/v1/organize/"+a+"/labels", map[string]any{"item": rest[0], "labels": rest[1:]})
		case "export":
			return call("GET", "/api/v1/layouts-export?scope="+url.QueryEscape(*scope), nil)
		case "import":
			if len(rest) != 1 {
				fmt.Fprint(os.Stderr, organizeUsage)
				return cliErr{exitUsage, ""}
			}
			f, err := os.Open(rest[0])
			if err != nil {
				return err
			}
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, 1<<20))
			if err != nil {
				return err
			}
			p := "/api/v1/layouts-import"
			if *dry {
				p += "?dry_run=1"
			}
			return call("POST", p, json.RawMessage(b))
		}
		fmt.Fprint(os.Stderr, organizeUsage)
		return cliErr{exitUsage, ""}
	})
}
