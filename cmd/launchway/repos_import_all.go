package main

import (
	"flag"
	"fmt"
	"strconv"
)

// reposImportAll is `launchway repos import-all`: every repository a connection can see (or one owner's),
// each copied and checked ref by ref, for bulk moves. It is `repos import` with the two choices a bulk
// move always has made plain: ordinary repositories once copied (add --mirror to keep each a pull mirror of
// its source), and failed repositories tried again, because a transient failure of one of two hundred
// should not need a person.
func (cl *client) reposImportAll(args []string) error {
	fs := flag.NewFlagSet("repos import-all", flag.ContinueOnError)
	conn := fs.String("connection", "", "the connection to read from (launchway repos connection add)")
	owner := fs.String("owner", "", "only this user or organization of the source")
	target := fs.String("target-owner", "", "make them under this owner here")
	user := fs.String("username", "", "the user name that goes with the connection's credential")
	mirror := fs.Bool("mirror", false, "keep each repository a pull mirror of its source (the staged move)")
	urls := &multi{}
	fs.Var(urls, "url", "a repository address to import (repeat); for a plain connection")
	retries := fs.Int("retries", 2, "how many more times to try the repositories that failed")
	dry := fs.Bool("dry-run", false, "list what would be made and change nothing")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *conn == "" {
		return usageErr("launchway repos import-all --connection NAME [--owner OWNER] [--mirror] [--retries N] [--dry-run]")
	}
	a := []string{"--connection", *conn}
	if *owner != "" {
		a = append(a, "--owner", *owner)
	}
	if *target != "" {
		a = append(a, "--target-owner", *target)
	}
	if *user != "" {
		a = append(a, "--username", *user)
	}
	for _, u := range *urls {
		a = append(a, "--url", u)
	}
	if !*mirror {
		a = append(a, "--once")
	}
	if *dry {
		a = append(a, "--dry-run")
	}
	if *asJSON {
		a = append(a, "--json")
	}
	var last error
	for try := 0; try <= *retries; try++ {
		if try > 0 && !*asJSON {
			fmt.Fprintf(cl.out, "\nTrying the unfinished repositories again (%s of %s).\n", strconv.Itoa(try), strconv.Itoa(*retries))
		}
		if last = cl.reposImport(a); last == nil || *dry {
			return last
		}
	}
	return last
}
