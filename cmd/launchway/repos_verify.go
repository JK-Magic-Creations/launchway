package main

import (
	"flag"
	"fmt"
)

type verifyItem struct {
	Source      string   `json:"source"`
	Repository  string   `json:"repository"`
	Status      string   `json:"status"`
	SourceRefs  int      `json:"source_refs"`
	HostedRefs  int      `json:"hosted_refs"`
	Differences []string `json:"differences"`
	Error       string   `json:"error"`
}

type verifyResult struct {
	Equal   bool           `json:"equal"`
	Items   []verifyItem   `json:"items"`
	Summary map[string]int `json:"summary"`
}

func (cl *client) reposVerify(args []string) error {
	fs := flag.NewFlagSet("repos verify", flag.ContinueOnError)
	src := addSourceFlags(fs, "against")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || (*src.conn == "" && len(*src.urls) == 0) {
		return usageErr("launchway repos verify --against NAME [--owner O] [--repo O/N] [--target-owner T]")
	}
	var res verifyResult
	if err := cl.call("POST", "/api/v1/imports/verify", src.body(), &res); err != nil {
		return err
	}
	_ = cl.print(res, *asJSON, func() {
		for _, it := range res.Items {
			switch it.Status {
			case "equal":
				fmt.Fprintf(cl.out, "equal      %s: %d refs\n", it.Repository, it.HostedRefs)
			default:
				fmt.Fprintf(cl.out, "%-10s %s %s\n", it.Status, it.Repository, it.Error)
				for _, d := range it.Differences {
					fmt.Fprintf(cl.out, "           %s\n", d)
				}
			}
		}
		if res.Equal {
			fmt.Fprintf(cl.out, "\nEvery ref of all %d repositories equals the source's.\n", len(res.Items))
		} else {
			fmt.Fprintf(cl.out, "\nNOT equal: %d equal, %d different, %d missing, %d could not be checked.\n",
				res.Summary["equal"], res.Summary["different"], res.Summary["missing"], res.Summary["error"])
		}
	})
	if !res.Equal {
		return cliErr{1, ""}
	}
	return nil
}
