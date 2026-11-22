package main

import (
	"fmt"
	"net/url"
)

// prTemplate prints the description a pull request into a branch starts with, from the repository.
func (cl *client) prTemplate(args []string) error {
	f := newPRFlags("template")
	ref := f.fs.String("ref", "", "the target branch (default: the default branch)")
	if err := f.parse(args); err != nil {
		return err
	}
	path := f.base + "/pull-template"
	if *ref != "" {
		path += "?ref=" + url.QueryEscape(*ref)
	}
	var out struct {
		Path     string `json:"path"`
		Template string `json:"template"`
	}
	if err := cl.call("GET", path, nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if out.Path == "" {
			fmt.Fprintln(cl.out, "This repository has no pull request template. Add .launchway/pull_request_template.md to the branch.")
			return
		}
		fmt.Fprint(cl.out, out.Template)
	})
}
