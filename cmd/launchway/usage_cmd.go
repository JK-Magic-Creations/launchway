package main

import (
	"flag"
	"fmt"
	"net/url"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// usage prints what each step of a run used and where its memory and CPU came from.
func (cl *client) usage(args []string) error {
	fs := flag.NewFlagSet("runs usage", flag.ContinueOnError)
	step := fs.String("step", "", "only this step")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs usage <run-id> [--step NAME]")
	}
	path := "/api/v1/runs/" + url.PathEscape(pos[0]) + "/usage"
	if *step != "" {
		path += "?" + url.Values{"step": {*step}}.Encode()
	}
	var out struct {
		Steps []engine.StepUsageRecord `json:"steps"`
	}
	if err := cl.call("GET", path, nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Steps) == 0 {
			fmt.Fprintln(cl.out, "Nothing was recorded for this run.")
			return
		}
		for _, s := range out.Steps {
			for _, a := range s.Attempts {
				fmt.Fprintf(cl.out, "%s (attempt %d): %s\n", s.Step, a.Attempt, a.Sentence())
			}
		}
	})
}
