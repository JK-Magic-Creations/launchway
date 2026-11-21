package main

// launchway templates list: the shared libraries, and a library's templates
// with each parameter's type and whether a secret reference takes its value
// from it. A client of GET /api/v1/libraries and
// GET /api/v1/libraries/{library}/templates.

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

const templatesUsage = `Usage:
  launchway templates list [library] [--version V] [--json]   the libraries, or a library's templates and their parameters
`

func cmdTemplates(args []string) error {
	return runClient(func() error {
		if len(args) == 0 || args[0] != "list" {
			fmt.Print(templatesUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).templatesList(args[1:])
	})
}

func (cl *client) templatesList(args []string) error {
	fs := flag.NewFlagSet("templates list", flag.ContinueOnError)
	version := fs.String("version", "", "a tag, branch or commit; default the newest tag")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageErr("%s", templatesUsage)
	}
	if len(pos) == 0 {
		var libs []struct {
			Name string `json:"name"`
		}
		if err := cl.call("GET", "/api/v1/libraries", nil, &libs); err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.MarshalIndent(libs, "", "  ")
			fmt.Fprintln(cl.out, string(b))
			return nil
		}
		if len(libs) == 0 {
			fmt.Fprintln(cl.out, "This server lists no shared libraries.")
		}
		for _, l := range libs {
			fmt.Fprintln(cl.out, l.Name)
		}
		return nil
	}
	path := "/api/v1/libraries/" + url.PathEscape(pos[0]) + "/templates"
	if *version != "" {
		path += "?version=" + url.QueryEscape(*version)
	}
	var got struct {
		Library   string                  `json:"library"`
		Version   string                  `json:"version"`
		Templates []pipeline.TemplateInfo `json:"templates"`
	}
	var raw json.RawMessage
	if err := cl.call("GET", path, nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		var v any
		_ = json.Unmarshal(raw, &v)
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(cl.out, string(b))
		return nil
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return apiErr("the server's answer is not what this command expects: %v", err)
	}
	fmt.Fprintf(cl.out, "%s at %s\n", got.Library, got.Version)
	for _, t := range got.Templates {
		fmt.Fprintf(cl.out, "\n%s", t.Name)
		if t.Description != "" {
			fmt.Fprintf(cl.out, ": %s", t.Description)
		}
		fmt.Fprintln(cl.out)
		for _, p := range t.Params {
			line := fmt.Sprintf("  %s (%s)", p.Name, p.Type)
			if p.Default == nil {
				line += " required"
			} else {
				line += " default " + *p.Default
			}
			if p.SecretReference {
				line += " [names a secret]"
			}
			if len(p.Options) > 0 {
				line += " one of " + strings.Join(p.Options, ", ")
			}
			fmt.Fprintln(cl.out, line)
		}
		if len(t.Problems) > 0 {
			fmt.Fprintf(cl.out, "  problem: %s\n", t.Problems[0].Message)
		}
	}
	return nil
}
