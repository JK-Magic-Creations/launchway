package main

import "fmt"

// launchway pr checks: what each pipeline said about the head commit of a pull request.

type prChecksOut struct {
	Head    string `json:"head"`
	Target  string `json:"target_branch"`
	Verdict string `json:"policy_verdict"`
	Checks  []struct {
		Number   int    `json:"number"`
		Project  string `json:"project"`
		Pipeline string `json:"pipeline"`
		Sentence string `json:"sentence"`
		Detail   bool   `json:"detail"`
		Tests    *struct {
			Totals struct {
				Total, Passed, Failed, Errored, Skipped int
			} `json:"totals"`
			NewFailures []string `json:"new_failures"`
			NewCount    int      `json:"new_failure_count"`
			Fixed       int      `json:"fixed"`
			Compared    bool     `json:"compared"`
		} `json:"tests"`
		Coverage *struct {
			Lines  float64  `json:"lines_percent"`
			Change *float64 `json:"change"`
		} `json:"coverage"`
		Findings *struct {
			Counts struct {
				Total, Critical, High, Medium, Low, Unknown int
			} `json:"counts"`
			NewTotal int `json:"new_total"`
		} `json:"findings"`
		Policy string `json:"policy_verdict"`
	} `json:"checks"`
	Deployments []struct {
		Environment string `json:"environment"`
		Version     string `json:"version"`
		Phase       string `json:"phase"`
		Address     string `json:"address"`
	} `json:"deployments"`
	Notes []struct {
		Kind, File string
		Line       int
		Severity   string
		Message    string
	} `json:"annotations"`
}

func (cl *client) prChecks(args []string) error {
	f := newPRFlags("checks")
	if err := f.parse(args); err != nil {
		return err
	}
	n, _, err := prNumber(f.pos, "checks")
	if err != nil {
		return err
	}
	var out prChecksOut
	if err := cl.call("GET", fmt.Sprintf("%s/pulls/%d/checks", f.base, n), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if len(out.Checks) == 0 {
			fmt.Fprintln(cl.out, "No pipeline has run for the head commit.")
			return
		}
		for _, c := range out.Checks {
			fmt.Fprintf(cl.out, "%s #%d (%s): %s\n", c.Pipeline, c.Number, c.Project, c.Sentence)
			if !c.Detail {
				continue
			}
			if t := c.Tests; t != nil {
				fmt.Fprintf(cl.out, "  tests: %d passed, %d failed, %d skipped of %d", t.Totals.Passed, t.Totals.Failed+t.Totals.Errored, t.Totals.Skipped, t.Totals.Total)
				if t.Compared {
					fmt.Fprintf(cl.out, "; %d newly failing, %d fixed against %s", t.NewCount, t.Fixed, out.Target)
				}
				fmt.Fprintln(cl.out)
				for _, name := range t.NewFailures {
					fmt.Fprintln(cl.out, "    newly failing:", name)
				}
			}
			if cv := c.Coverage; cv != nil {
				fmt.Fprintf(cl.out, "  coverage: %.1f%%", cv.Lines)
				if cv.Change != nil {
					fmt.Fprintf(cl.out, " (%+.1f points)", *cv.Change)
				}
				fmt.Fprintln(cl.out)
			}
			if fi := c.Findings; fi != nil {
				k := fi.Counts
				fmt.Fprintf(cl.out, "  findings: %d (%d critical, %d high, %d medium, %d low); %d new in this change\n", k.Total, k.Critical, k.High, k.Medium, k.Low, fi.NewTotal)
			}
			if c.Policy != "" {
				fmt.Fprintln(cl.out, "  policy:", c.Policy)
			}
		}
		for _, d := range out.Deployments {
			fmt.Fprintf(cl.out, "deployed to %s: %s (%s) %s\n", d.Environment, d.Version, d.Phase, d.Address)
		}
		if out.Verdict != "" {
			fmt.Fprintln(cl.out, "policy verdict:", out.Verdict)
		}
		for _, a := range out.Notes {
			fmt.Fprintf(cl.out, "%s:%d  [%s %s] %s\n", a.File, a.Line, a.Kind, a.Severity, a.Message)
		}
	})
}
