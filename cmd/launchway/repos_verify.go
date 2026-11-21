package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

type verifyItem struct {
	Source      string   `json:"source"`
	Repository  string   `json:"repository"`
	Status      string   `json:"status"`
	SourceRefs  int      `json:"source_refs"`
	HostedRefs  int      `json:"hosted_refs"`
	Differences []string `json:"differences"`
	Namespaces  []struct {
		Namespace string `json:"namespace"`
		Source    int    `json:"source"`
		Hosted    int    `json:"hosted"`
		Equal     int    `json:"equal"`
	} `json:"namespaces"`
	NotImported map[string]int `json:"not_imported"`
	Refs        []struct {
		Ref    string `json:"ref"`
		Source string `json:"source"`
		Hosted string `json:"hosted"`
		Status string `json:"status"`
	} `json:"refs"`
	Error string `json:"error"`
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
	allRefs := fs.Bool("refs", false, "list every ref with its hashes, not only the ones that differ")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	// --against takes a connection's name, or an address of one repository.
	if isAddress(*src.conn) {
		*src.urls, *src.conn = append(*src.urls, *src.conn), ""
	}
	if len(pos) != 0 || (*src.conn == "" && len(*src.urls) == 0) {
		return usageErr("launchway repos verify --against NAME|URL [--owner O] [--repo O/N] [--target-owner T] [--refs] [--json]")
	}
	body := src.body()
	if *allRefs {
		body["refs"] = true
	}
	var res verifyResult
	if err := cl.call("POST", "/api/v1/imports/verify", body, &res); err != nil {
		return err
	}
	_ = cl.print(res, *asJSON, func() { cl.printVerify(res) })
	if !res.Equal {
		return cliErr{1, ""}
	}
	return nil
}

// isAddress says whether a --against value is an address rather than a
// connection's name.
func isAddress(v string) bool {
	for _, p := range []string{"http://", "https://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

// printVerify writes the plain-text report: one table of counts per
// repository, the refs that differ (or every ref with --refs), and a summary.
func (cl *client) printVerify(res verifyResult) {
	for _, it := range res.Items {
		fmt.Fprintf(cl.out, "%s  %s\n", it.Status, it.Repository)
		if it.Error != "" {
			fmt.Fprintf(cl.out, "  %s\n", it.Error)
		}
		if len(it.Namespaces) > 0 {
			fmt.Fprintf(cl.out, "  %-20s %8s %8s %8s\n", "NAMESPACE", "SOURCE", "HERE", "EQUAL")
			for _, n := range it.Namespaces {
				fmt.Fprintf(cl.out, "  %-20s %8d %8d %8d\n", n.Namespace, n.Source, n.Hosted, n.Equal)
			}
		}
		if len(it.NotImported) > 0 {
			var ns []string
			for k, v := range it.NotImported {
				ns = append(ns, fmt.Sprintf("%s (%d)", k, v))
			}
			sort.Strings(ns)
			fmt.Fprintf(cl.out, "  not copied, other namespaces at the source: %s\n", strings.Join(ns, ", "))
		}
		if len(it.Refs) > 0 {
			fmt.Fprintf(cl.out, "  %-8s %-40s %-40s %s\n", "STATUS", "SOURCE", "HERE", "REF")
			for _, r := range it.Refs {
				fmt.Fprintf(cl.out, "  %-8s %-40s %-40s %s\n", r.Status, dash(r.Source), dash(r.Hosted), r.Ref)
			}
		} else {
			for _, d := range it.Differences {
				fmt.Fprintf(cl.out, "  %s\n", d)
			}
		}
	}
	if res.Equal {
		fmt.Fprintf(cl.out, "\nEvery ref of all %d repositories equals the source's.\n", len(res.Items))
	} else {
		fmt.Fprintf(cl.out, "\nNOT equal: %d equal, %d different, %d missing, %d could not be checked.\n",
			res.Summary["equal"], res.Summary["different"], res.Summary["missing"], res.Summary["error"])
	}
}
