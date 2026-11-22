package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

const recentUsage = `usage: launchway recent [--limit N] [--json] [--clear] [--pause | --resume]

What you opened and did lately, newest first, with the address of each (and, for a Sortie, what changed since you were
there). It is your own trail only; nobody else, administrators included, can read it. --clear deletes it; --pause stops
it being written until --resume.
`

func cmdRecent(args []string) error {
	return runClient(func() error {
		fs := flag.NewFlagSet("recent", flag.ContinueOnError)
		limit := fs.Int("limit", 20, "at most this many")
		asJSON := fs.Bool("json", false, "print JSON")
		wipe := fs.Bool("clear", false, "delete your trail")
		pause := fs.Bool("pause", false, "stop writing your trail")
		resume := fs.Bool("resume", false, "start writing your trail again")
		fs.Usage = func() { fmt.Fprint(os.Stderr, recentUsage) }
		if _, err := parseArgs(fs, args); err != nil {
			return err
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch {
		case *wipe:
			return cl.call("DELETE", "/api/v1/activity", nil, nil)
		case *pause || *resume:
			return cl.call("PUT", "/api/v1/activity/pause", map[string]bool{"paused": *pause}, nil)
		}
		var out struct {
			Recent []struct {
				Kind, Title, Href, Action, Changed string
				At                                 int64
			} `json:"recent"`
			Paused bool `json:"paused"`
		}
		if err := cl.call("GET", fmt.Sprintf("/api/v1/activity?limit=%d", *limit), nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			if out.Paused {
				fmt.Fprintln(cl.out, "(your trail is paused)")
			}
			for _, a := range out.Recent {
				fmt.Fprintf(cl.out, "%s  %-8s %-10s %s  %s\n", time.UnixMilli(a.At).Format("01-02 15:04"), a.Action, a.Kind, a.Title, a.Href)
				if a.Changed != "" {
					fmt.Fprintf(cl.out, "                       %s\n", a.Changed)
				}
			}
		})
	})
}
