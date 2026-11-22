package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

const attentionUsage = `usage: launchway attention [--dismissed] [--json]

What needs a person, by the one rule the Home page uses: a failed Sortie is
listed only when nothing newer has taken its place, and only on a branch that
matters; a failed deployment that was not followed by a success or a return;
a decision that waits; a Sortie stuck or far slower than usual. Each line says
why it is listed and what to do. Failures that a newer Sortie is retrying are
shown after, as "being retried". Also: launchway runs list --needs-attention.
`

// attentionAnswer is GET /api/v1/attention.
type attentionAnswer struct {
	Needs      []engine.AttentionItem `json:"needs"`
	Dismissed  []engine.AttentionItem `json:"dismissed"`
	Retrying   []engine.Retry         `json:"retrying"`
	Count      int                    `json:"count"`
	Superseded int                    `json:"superseded"`
}

func cmdAttention(args []string) error {
	return runClient(func() error {
		fs := flag.NewFlagSet("attention", flag.ContinueOnError)
		dismissed := fs.Bool("dismissed", false, "also list what you set aside")
		asJSON := fs.Bool("json", false, "print JSON")
		fs.Usage = func() { fmt.Fprint(os.Stderr, attentionUsage) }
		if _, err := parseArgs(fs, args); err != nil {
			return err
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		var a attentionAnswer
		if err := cl.call("GET", "/api/v1/attention", nil, &a); err != nil {
			return err
		}
		return cl.print(a, *asJSON, func() {
			if len(a.Needs) == 0 {
				fmt.Fprintln(cl.out, "Nothing needs you.")
			}
			for _, n := range a.Needs {
				fmt.Fprintf(cl.out, "%-8s %s\n         why: %s\n         do:  %s\n", n.Kind, n.Title, n.Why, n.Do)
			}
			for _, r := range a.Retrying {
				fmt.Fprintf(cl.out, "retrying %s: Sortie #%d failed; #%d is %s\n", r.Project, r.FailedNo, r.NowNo, r.NowStatus)
			}
			if *dismissed {
				for _, n := range a.Dismissed {
					fmt.Fprintf(cl.out, "set aside %s (%s)\n", n.Title, time.UnixMilli(n.Since).Format(time.DateTime))
				}
			}
		})
	})
}
