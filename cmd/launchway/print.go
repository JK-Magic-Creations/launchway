package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// printer shows a local run in the terminal as it happens.
type printer struct {
	color   bool
	width   int
	colorOf map[string]string
	status  map[string]engine.StepStatus
	attempt map[string]int
	seq     map[string]int // per step+attempt
}

var palette = []string{"36", "35", "34", "33", "32", "96", "95", "94"}

func newPrinter(p *pipeline.Pipeline) *printer {
	pr := &printer{
		color:   isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == "",
		colorOf: map[string]string{}, status: map[string]engine.StepStatus{},
		attempt: map[string]int{}, seq: map[string]int{},
	}
	for i, s := range p.Steps {
		pr.colorOf[s.ID] = palette[i%len(palette)]
		pr.width = max(pr.width, len(s.ID))
	}
	return pr
}

func (pr *printer) c(code, s string) string {
	if !pr.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (pr *printer) header(p *pipeline.Pipeline, exec, dir string, t engine.Trigger) {
	where := dir
	if t.Branch != "" {
		where += " (" + t.Branch + ")"
	}
	fmt.Printf("%s %s · %d steps · %s\n%s\n\n", pr.c("1", "▶ "+p.Name), "", len(p.Steps), exec, pr.c("2", where))
}

func (pr *printer) prefix(step string) string {
	return pr.c(pr.colorOf[step], fmt.Sprintf("%-*s", pr.width, step)) + " │ "
}

func (pr *printer) update(eng *engine.Engine, d *engine.RunDetail) {
	for i := range d.StepList {
		s := &d.StepList[i]
		key := fmt.Sprintf("%s#%d", s.ID, s.Attempt)
		if s.Attempt > 0 && (s.Status.Active() || s.Status.Terminal()) {
			for {
				lines, _, err := eng.Logs(d.ID, s.ID, s.Attempt, pr.seq[key], 2000)
				if err != nil || len(lines) == 0 {
					break
				}
				for _, l := range lines {
					text := l.Text
					switch l.Stream {
					case "system":
						text = pr.c("2", text)
					case "stderr":
						text = pr.c("31", text)
					}
					fmt.Println(pr.prefix(s.ID) + text)
				}
				pr.seq[key] = lines[len(lines)-1].Seq
			}
		}
		if pr.status[s.ID] == s.Status && pr.attempt[s.ID] == s.Attempt {
			continue
		}
		pr.status[s.ID], pr.attempt[s.ID] = s.Status, s.Attempt
		switch s.Status {
		case engine.StepWaitingS:
			fmt.Println(pr.prefix(s.ID) + pr.c("33;1", "⏸ waiting for approval: ") + s.Message)
		case engine.StepSucceeded:
			if s.Kind == pipeline.KindApproval {
				fmt.Println(pr.prefix(s.ID) + pr.c("32", "✓ approved by "+s.DecidedBy))
				break
			}
			fmt.Println(pr.prefix(s.ID) + pr.c("32", "✓ succeeded "+took(s)))
		case engine.StepFailed:
			fmt.Println(pr.prefix(s.ID) + pr.c("31;1", "✗ failed: "+s.Error))
		case engine.StepSkippedS:
			fmt.Println(pr.prefix(s.ID) + pr.c("2", "↷ skipped: "+s.Error))
		case engine.StepCanceledS:
			fmt.Println(pr.prefix(s.ID) + pr.c("33", "■ canceled"))
		case engine.StepQueuedS:
			if s.Attempt > 1 {
				fmt.Println(pr.prefix(s.ID) + pr.c("33", fmt.Sprintf("↻ retrying (attempt %d)", s.Attempt)))
			}
		}
	}
}

func (pr *printer) summary(d *engine.RunDetail) {
	fmt.Println()
	for i := range d.StepList {
		s := &d.StepList[i]
		mark := map[engine.StepStatus]string{
			engine.StepSucceeded: pr.c("32", "✓"), engine.StepFailed: pr.c("31", "✗"),
			engine.StepSkippedS: pr.c("2", "↷"), engine.StepCanceledS: pr.c("33", "■"),
		}[s.Status]
		tests := ""
		if t := s.Tests; t != nil {
			tests = fmt.Sprintf("  tests: %d passed", t.Passed)
			if bad := t.Failed + t.Errored; bad > 0 {
				tests += ", " + pr.c("31", fmt.Sprintf("%d failed", bad))
			}
			if t.Skipped > 0 {
				tests += fmt.Sprintf(", %d skipped", t.Skipped)
			}
		}
		fmt.Printf("  %s %-*s  %-9s %s%s\n", mark, pr.width, s.ID, s.Status, pr.c("2", took(s)), tests)
	}
	total := time.Duration(d.FinishedAt-d.CreatedAt) * time.Millisecond
	label := map[engine.RunStatus]string{
		engine.RunSucceeded: pr.c("32;1", "Succeeded"), engine.RunFailed: pr.c("31;1", "Failed"),
		engine.RunCanceled: pr.c("33;1", "Canceled"),
	}[d.Status]
	fmt.Printf("\n%s in %s\n", label, total.Round(100*time.Millisecond))
}

func took(s *engine.StepView) string {
	if s.StartedAt == 0 || s.FinishedAt == 0 {
		return ""
	}
	d := time.Duration(s.FinishedAt-s.StartedAt) * time.Millisecond
	return strings.TrimSpace("in " + d.Round(100*time.Millisecond).String())
}
